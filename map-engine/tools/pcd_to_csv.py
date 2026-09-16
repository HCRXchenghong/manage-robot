#!/usr/bin/env python3
# 把真实激光雷达 PCD（ascii / binary）转成运营大屏「配置点云」可加载的 CSV。
# 仅标准库，无依赖。
#
# 用法：
#   python3 pcd_to_csv.py 输入.pcd 输出.csv [--step N] [--zmin F] [--zmax F]
#
# 示例（把车端代码里 ndt_omp 的真地图转出来）：
#   python3 map-engine/tools/pcd_to_csv.py \
#     ../../车端代码/src/navigation/ndt_omp/data/251370668.pcd /tmp/ra-ndt-map.csv

import argparse
import struct
import sys


def parse_header(f):
    h = {}
    while True:
        line = f.readline().decode("ascii", "ignore").strip()
        if not line or line.startswith("#"):
            continue
        key, _, val = line.partition(" ")
        h[key.upper()] = val
        if key.upper() == "DATA":
            break
    return h


def struct_fmt(h):
    out = []
    for size, typ in zip(h["SIZE"].split(), h["TYPE"].split()):
        s = int(size)
        if typ == "F" and s == 4:
            out.append("f")
        elif typ == "F" and s == 8:
            out.append("d")
        elif typ in ("U", "I") and s == 1:
            out.append("b")
        elif typ in ("U", "I") and s == 2:
            out.append("h")
        elif typ in ("U", "I") and s == 4:
            out.append("i")
        elif typ in ("U", "I") and s == 8:
            out.append("q")
        else:
            sys.exit("不支持的 PCD 字段类型: " + typ + str(s))
    return "<" + "".join(out)


def main():
    ap = argparse.ArgumentParser(description="PCD -> 大屏 CSV（x,y,z[,intensity]）")
    ap.add_argument("src")
    ap.add_argument("dst")
    ap.add_argument("--step", type=int, default=1, help="每 step 个点取 1 个（降采样）")
    ap.add_argument("--zmin", type=float, default=None, help="过滤高度下限")
    ap.add_argument("--zmax", type=float, default=None, help="过滤高度上限")
    args = ap.parse_args()

    with open(args.src, "rb") as f:
        h = parse_header(f)
        fields = h["FIELDS"].split()
        idx = {name: i for i, name in enumerate(fields)}
        for k in ("x", "y", "z"):
            if k not in idx:
                sys.exit("PCD 缺少字段: " + k)
        has_i = "intensity" in idx
        n = int(h["POINTS"])

        rows = []

        def emit(vals):
            x, y, z = vals[idx["x"]], vals[idx["y"]], vals[idx["z"]]
            if args.zmin is not None and z < args.zmin:
                return
            if args.zmax is not None and z > args.zmax:
                return
            if has_i:
                rows.append((x, y, z, vals[idx["intensity"]]))
            else:
                rows.append((x, y, z))

        if h["DATA"] == "ascii":
            i = 0
            for line in f:
                if i % args.step == 0:
                    parts = line.split()
                    if len(parts) >= len(fields):
                        emit([float(p) for p in parts[: len(fields)]])
                i += 1
        elif h["DATA"] == "binary":
            fmt = struct_fmt(h)
            rec = struct.calcsize(fmt)
            raw = f.read(rec * n)
            for i in range(0, n, args.step):
                emit(struct.unpack_from(fmt, raw, i * rec))
        else:
            sys.exit("不支持的 PCD DATA 类型: " + h["DATA"])

    with open(args.dst, "w") as out:
        for r in rows:
            out.write(",".join("%.3f" % v for v in r) + "\n")
    print("转换完成：%d 点 -> %s" % (len(rows), args.dst))


if __name__ == "__main__":
    main()
