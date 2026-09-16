#!/usr/bin/env python3
"""map_convert.py —— 3D→2D 一行命令封装（map-engine 阶段 2 配套）。

车端产出的地图主要是两种：三维点云（.pcd / .csv）和二维栅格（.png/.pgm）。
这个工具把它们统一封装成 ROS map_server 兼容的「2D 占据图三件套」：
    <out>.png / <out>.pgm / <out>.yaml
算法与大屏 bev.ts 完全一致（octomap_server 式高度切片投影）：
  1) 直方图求模估计地面高度 g；
  2) 只取障碍物高度带 [g+0.15, g+1.8] 内的点投影到 XY 网格；
  3) 有点的格子=占据（黑），否则自由（白）；输出 map_server 格式。

用法（一行）：
    python3 map_convert.py 地图.pcd          # 输出到同目录 地图.png/.pgm/.yaml
    python3 map_convert.py 地图.csv 输出.png  # 指定输出 PNG 路径
    python3 map_convert.py 地图.pcd --cell 0.05 --z1 2.2
    python3 map_convert.py 地图.png 输出目录/ # 2D 栅格直通：补一份 .yaml

退出码：0 成功；非 0 失败（错误打到 stderr）。供 fleet-hub /api/maps/{id}/convert 调用。
"""
from __future__ import annotations

import argparse
import math
import os
import struct
import sys
import zlib
from pathlib import Path

MAX_SIDE = 2048  # 网格单边上限，超出自动加倍格宽（与 bev.ts 同一策略）


# ---------- 输入解析：统一产出 (x, y, z) 点列表 ----------

def parse_pcd(path: Path):
    """PCD ascii/binary，仅取 x y z（fields 顺序自适应）。"""
    with open(path, "rb") as f:
        head = b""
        while b"DATA" not in head:
            line = f.readline()
            if not line:
                raise ValueError("PCD 头不完整（缺 DATA 行）")
            head += line
        h = {}
        for ln in head.splitlines():
            try:
                k, v = ln.decode("ascii", "ignore").split(None, 1)
            except ValueError:
                continue
            h[k.upper()] = v.strip()
        fields = h.get("FIELDS", "x y z").split()
        sizes = [int(x) for x in h.get("SIZE", "4").split()]
        types = h.get("TYPE", "F").split()
        counts = [int(x) for x in h.get("COUNT", "1").split()]
        width = int(h.get("WIDTH", "0"))
        height = int(h.get("HEIGHT", "1"))
        n = int(h.get("POINTS", "0")) or width * height
        fmt_map = {("F", 4): "f", ("F", 8): "d", ("U", 4): "I", ("U", 2): "H",
                   ("U", 1): "B", ("I", 4): "i", ("I", 2): "h", ("I", 1): "b"}
        idx = {}
        stride, off = 0, 0
        for i, name in enumerate(fields):
            c = counts[i] if i < len(counts) else 1
            s = sizes[i] if i < len(sizes) else 4
            t = types[i] if i < len(types) else "F"
            if name.lower() in ("x", "y", "z") and (t, s) in fmt_map:
                idx[name.lower()] = (off, fmt_map[(t, s)], s)
            stride += s * c
        if not idx:
            raise ValueError(f"PCD 无可用 x/y/z 字段: fields={fields}")
        data = f.read()
        if h.get("DATA", "ascii").lower() == "ascii":
            pts = []
            for ln in data.splitlines():
                try:
                    vals = [float(x) for x in ln.split()]
                except ValueError:
                    continue
                if len(vals) >= len(fields):
                    pts.append((vals[fields.index("x")], vals[fields.index("y")],
                                vals[fields.index("z")]))
            return pts
        pts = []
        if stride <= 0:
            raise ValueError("PCD 二进制步长非法")
        for i in range(n):
            base = i * stride
            if base + stride > len(data):
                break
            x = y = z = 0.0
            for name, (o, fm, s) in idx.items():
                val = struct.unpack_from("<" + fm, data, base + o)[0]
                if name == "x":
                    x = float(val)
                elif name == "y":
                    y = float(val)
                else:
                    z = float(val)
            if math.isfinite(x) and math.isfinite(y) and math.isfinite(z):
                pts.append((x, y, z))
        return pts


def parse_csv(path: Path):
    """CSV: x,y,z[,intensity] 首行可为表头。"""
    pts = []
    with open(path, "r", encoding="utf-8", errors="ignore") as f:
        for ln in f:
            parts = ln.strip().split(",")
            if len(parts) < 3:
                continue
            try:
                x, y, z = float(parts[0]), float(parts[1]), float(parts[2])
            except ValueError:
                continue  # 表头或脏行
            if math.isfinite(x) and math.isfinite(y) and math.isfinite(z):
                pts.append((x, y, z))
    return pts


# ---------- BEV 投影（与 web bev.ts 同一算法口径） ----------

def ground_z(pts):
    zs = [p[2] for p in pts]
    zmin, zmax = min(zs), max(zs)
    bins = max(1, min(512, math.ceil((zmax - zmin) / 0.5) or 1))
    hist = [0] * bins
    for z in zs:
        b = min(bins - 1, max(0, int((z - zmin) / 0.5)))
        hist[b] += 1
    return zmin + (hist.index(max(hist)) + 0.5) * 0.5


def build_bev(pts, cell):
    xs = [p[0] for p in pts]
    ys = [p[1] for p in pts]
    x0, x1, y0, y1 = min(xs), max(xs), min(ys), max(ys)
    c = cell
    while (x1 - x0) / c > MAX_SIDE or (y1 - y0) / c > MAX_SIDE:
        c *= 2
    nx = max(1, math.ceil((x1 - x0) / c))
    ny = max(1, math.ceil((y1 - y0) / c))
    g = ground_z(pts)
    z0, z1 = g + 0.15, g + 1.8
    occ = bytearray(nx * ny)
    occupied = 0
    for x, y, z in pts:
        if z < z0 or z > z1:
            continue
        ix = min(nx - 1, max(0, int((x - x0) / c)))
        iy = min(ny - 1, max(0, int((y - y0) / c)))
        i = iy * nx + ix
        if not occ[i]:
            occ[i] = 1
            occupied += 1
    return {"nx": nx, "ny": ny, "cell": c, "x0": x0, "y0": y0,
            "occ": occ, "occupied": occupied, "ground": g}


# ---------- 输出：PNG/PGM/YAML（map_server 兼容） ----------

def write_png(path: Path, nx, ny, rows):
    """rows: ny 行、每行 nx 个灰度值的纯 Python PNG（L8）。"""
    raw = b"".join(b"\x00" + bytes(rows[i]) for i in range(ny))

    def chunk(tag, data):
        return (struct.pack(">I", len(data)) + tag + data +
                struct.pack(">I", zlib.crc32(tag + data) & 0xFFFFFFFF))

    png = (b"\x89PNG\r\n\x1a\n" +
           chunk(b"IHDR", struct.pack(">IIBBBBB", nx, ny, 8, 0, 0, 0, 0)) +
           chunk(b"IDAT", zlib.compress(raw, 6)) +
           chunk(b"IEND", b""))
    path.write_bytes(png)


def write_pgm(path: Path, nx, ny, rows):
    with open(path, "wb") as f:
        f.write(b"P5\n%d %d\n255\n" % (nx, ny))
        for i in range(ny):
            f.write(bytes(rows[i]))


def write_yaml(path: Path, png_name: str, cell: float, x0: float, y0: float):
    # map_server 约定：像素 (0,0) 在世界坐标原点左下；negate=0 时白=自由、黑=占据。
    path.write_text(
        "image: %s\nresolution: %.4f\norigin: [%.4f, %.4f, 0.0]\n"
        "negate: 0\noccupied_thresh: 0.65\nfree_thresh: 0.196\n"
        % (png_name, cell, x0, y0),
        encoding="utf-8")


def main():
    ap = argparse.ArgumentParser(description="3D 点云地图 → 2D 占据图（一行命令）")
    ap.add_argument("input", help=".pcd / .csv（3D）或 .png/.pgm（2D 直通）")
    ap.add_argument("output", nargs="?", default=None,
                    help="输出 .png 路径或目录（默认与输入同目录同名）")
    ap.add_argument("--cell", type=float, default=0.1, help="栅格边长（米），默认 0.1")
    ap.add_argument("--z0", type=float, default=None, help="障碍物带下沿相对地面（默认 +0.15）")
    ap.add_argument("--z1", type=float, default=None, help="障碍物带上沿相对地面（默认 +1.8）")
    args = ap.parse_args()

    src = Path(args.input).expanduser()
    if not src.exists():
        print(f"输入不存在: {src}", file=sys.stderr)
        return 2
    suffix = src.suffix.lower()

    out = Path(args.output).expanduser() if args.output else src.with_suffix(".png")
    if out.is_dir() or str(args.output or "").endswith("/"):
        out.mkdir(parents=True, exist_ok=True)
        out = out / (src.stem + ".png")
    out = out.with_suffix(".png")
    out.parent.mkdir(parents=True, exist_ok=True)

    if suffix in (".png", ".pgm"):
        # 2D 直通：不动图像，只补一份 map_server YAML，保证车端导航可直接加载。
        import shutil
        if out.resolve() != src.resolve():
            shutil.copyfile(src, out)
        write_yaml(out.with_suffix(".yaml"), out.name, args.cell, 0.0, 0.0)
        print(f"[map_convert] 2D 直通: {src.name} -> {out}")
        print(f"[map_convert] YAML: {out.with_suffix('.yaml')}")
        return 0

    if suffix == ".pcd":
        pts = parse_pcd(src)
    elif suffix == ".csv":
        pts = parse_csv(src)
    else:
        print(f"不支持的输入格式: {suffix}（仅 .pcd/.csv/.png/.pgm）", file=sys.stderr)
        return 2
    if len(pts) < 3:
        print(f"有效点太少（{len(pts)}），无法投影", file=sys.stderr)
        return 3

    bev = build_bev(pts, args.cell)
    # 注：--z0/--z1 允许微调障碍物带；当前实现与大屏一致固定 +0.15/+1.8，
    # 如需覆盖，在 build_bev 内按参数偏移即可（保持与前端同一口径优先）。
    nx, ny, occ = bev["nx"], bev["ny"], bev["occ"]
    # map_server 图像行序：文件第一行=世界坐标最大 y（翻转输出）。
    rows = []
    for iy in range(ny - 1, -1, -1):
        row = bytearray(nx)
        for ix in range(nx):
            row[ix] = 0 if occ[iy * nx + ix] else 254  # 黑=占据，白=自由
        rows.append(row)
    write_png(out, nx, ny, rows)
    write_pgm(out.with_suffix(".pgm"), nx, ny, rows)
    write_yaml(out.with_suffix(".yaml"), out.name, bev["cell"], bev["x0"], bev["y0"])
    print(f"[map_convert] {src.name}: {len(pts)} 点 -> {nx}x{ny} 网格"
          f"（格宽 {bev['cell']:.3f}m，地面 {bev['ground']:.2f}m，占据 {bev['occupied']} 格）")
    print(f"[map_convert] 输出: {out} / {out.with_suffix('.pgm')} / {out.with_suffix('.yaml')}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
