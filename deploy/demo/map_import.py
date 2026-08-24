#!/usr/bin/env python3
# 通用点云地图导入工具：把各自动驾驶栈用激光雷达（含 Livox MID360）扫出的
# 地图/扫描数据，统一转成大屏可加载的 CSV（每行 x,y,z[,intensity]）。
#
# 支持输入：
#   ROS 1 .bag          sensor_msgs/PointCloud2、sensor_msgs/LaserScan、
#                       livox_ros_driver/CustomMsg（MID360 非重复扫描，多帧累积）
#   ROS 2 .mcap/.db3/目录  sensor_msgs/msg/PointCloud2、livox_ros_driver2/msg/CustomMsg
#                       （Autoware 默认即 PointCloud2；ROS2 bag 两种存储都认）
#   .pcd                ascii/binary（Autoware 点云地图、Apollo 导出、FAST-LIO 产物）
#   .csv/.txt/.xyz      通用文本点云
#   Apollo .record      需 CyberRT 解析，本工具不直接读；请用 Apollo 自带导出或
#                       ROS 桥先转成 PCD/CSV 再导入（见 docs/plan-map-engine.md）
#
# 用法：
#   python3 map_import.py INPUT OUT.csv [--step N] [--zmin F] [--zmax F]
#                       [--max-frames N] [--scan-height F]
import argparse
import csv
import bz2
import math
import struct
import sys
from pathlib import Path

# ---------- PCD ----------

def parse_pcd(path: Path):
    raw = path.read_bytes()
    header = raw[:2048].decode("latin1")
    fields, sizes, types, count = [], [], [], []
    width = height = 0
    data_off = 0
    data_kind = "ascii"
    for line in header.splitlines():
        parts = line.split()
        if not parts or parts[0].startswith("#"):
            continue
        k, rest = parts[0], parts[1:]
        if k == "FIELDS":
            fields = rest
        elif k == "SIZE":
            sizes = [int(x) for x in rest]
        elif k == "TYPE":
            types = rest
        elif k == "COUNT":
            count = [int(x) for x in rest]
        elif k == "WIDTH":
            width = int(rest[0])
        elif k == "HEIGHT":
            height = int(rest[0])
        elif k == "DATA":
            data_kind = rest[0]
            data_off = raw.find(b"\n", raw.find(line.encode("latin1"))) + 1
            break
    idx = {f: i for i, f in enumerate(fields)}
    for need in ("x", "y", "z"):
        if need not in idx:
            raise SystemExit("PCD 缺字段 " + need)
    ii = idx.get("intensity", idx.get("i", -1))
    pts = []
    if data_kind == "ascii":
        for line in raw[data_off:].decode("latin1").splitlines():
            v = line.split()
            if len(v) <= max(idx.values()):
                continue
            pts.append((float(v[idx["x"]]), float(v[idx["y"]]), float(v[idx["z"]]),
                        float(v[ii]) if ii >= 0 else 0.0))
        return pts
    # binary little-endian
    strides = []
    off = 0
    for s, c in zip(sizes, count):
        strides.append((off, s, c))
        off += s * c
    step = off
    fmt = {1: "b", 2: "B", 4: "f", 8: "d"}

    def read_at(buf, o, s):
        code = fmt.get(s)
        if code is None:
            return 0.0
        return struct.unpack_from("<" + code, buf, o)[0]

    base = 0
    offs = {}
    for fi, f in enumerate(fields):
        o = 0
        for j in range(fi):
            o += sizes[j] * count[j]
        offs[f] = o
    n = width * height
    for i in range(n):
        p = data_off + i * step
        if p + step > len(raw):
            break
        x = read_at(raw, p + offs["x"], sizes[idx["x"]])
        y = read_at(raw, p + offs["y"], sizes[idx["y"]])
        z = read_at(raw, p + offs["z"], sizes[idx["z"]])
        it = read_at(raw, p + offs["intensity"], sizes[ii]) if ii >= 0 else 0.0
        pts.append((x, y, z, it))
    return pts

# ---------- 文本 ----------

def parse_text(path: Path):
    pts = []
    for line in path.read_text(errors="ignore").splitlines():
        v = [float(x) for x in line.replace(",", " ").replace(";", " ").split()] if line.strip() else []
        if len(v) >= 3:
            pts.append((v[0], v[1], v[2], v[3] if len(v) > 3 else 0.0))
    return pts

# ---------- bag（ROS1/ROS2，rosbags 0.11 API） ----------

# ---------- ROS1 bag 线性扫描（索引损坏/老旧格式时的恢复模式） ----------

def _fields(buf):
    out = {}
    p = 0
    while p + 4 <= len(buf):
        (fl,) = struct.unpack_from("<I", buf, p)
        p += 4
        kv = buf[p:p + fl].decode("latin1")
        p += fl
        if "=" in kv:
            k, v = kv.split("=", 1)
            out[k] = v
    return out


def _iter_records(buf):
    p = 0
    n = len(buf)
    while p + 8 <= n:
        (hlen,) = struct.unpack_from("<I", buf, p)
        if p + 4 + hlen + 4 > n:
            break
        h = _fields(buf[p + 4:p + 4 + hlen])
        ds = p + 4 + hlen
        (dlen,) = struct.unpack_from("<I", buf, ds)
        if ds + 4 + dlen > n:
            break
        ds += 4
        yield h, buf[ds:ds + dlen]
        p = ds + dlen


def ros1_linear(path: Path):
    raw = path.read_bytes()
    if raw.startswith(b"#ROSBAG"):
        raw = raw[raw.find(b"\n") + 1:]
    conns = {}
    for h, data in _iter_records(raw):
        op = h.get("op", "")
        if op == "\x05" or "compression" in h:
            comp = h.get("compression", "none")
            inner = data
            if comp == "bz2":
                inner = bz2.decompress(data)
            elif comp == "lz4":
                try:
                    import lz4.block  # type: ignore
                    inner = lz4.block.decompress(
                        data,
                        uncompressed_size=int.from_bytes(h.get("size", "").encode("latin1"), "little"),
                    )
                except Exception:
                    continue
            elif comp != "none":
                continue
            for h2, d2 in _iter_records(inner):
                if "topic" in h2:
                    conns[h2.get("conn", h2.get("id", ""))] = h2.get("type") or _guess_type(h2.get("topic", ""), True)
                elif "conn" in h2 and "time" in h2:
                    yield conns.get(h2.get("conn", ""), ""), d2
        elif "topic" in h:
            conns[h.get("conn", h.get("id", ""))] = h.get("type") or _guess_type(h.get("topic", ""), True)
        elif "conn" in h and "time" in h:
            yield conns.get(h.get("conn", ""), ""), data


LIVOX_POINT = """float32 x
float32 y
float32 z
float32 reflectance
uint16 time
uint8 line"""

LIVOX_MSG = """std_msgs/Header header
uint64 timebase
uint8 lidar_id
livox_ros_driver/CustomPoint[] points
uint32 point_num
uint8[2] rsvd1
uint8[2] rsvd2"""

PC2_DT = {1: "b", 2: "B", 3: "h", 4: "H", 5: "i", 6: "I", 7: "f", 8: "d"}

def _guess_type(topic, ros1):
    t = (topic or "").lower()
    mid = "/msg"
    if "livox" in t or "custom" in t:
        return "livox_ros_driver" + mid + "/CustomMsg"
    if "cloud" in t or "points" in t or "pcd" in t:
        return "sensor_msgs" + mid + "/PointCloud2"
    if "scan" in t:
        return "sensor_msgs" + mid + "/LaserScan"
    return ""

def _store_type(t):
    # rosbags 0.11 的 typestore 统一用 pkg/msg/X 命名；老 ROS1 bag 里是 pkg/X
    if "/msg/" in t or "/" not in t:
        return t
    pkg, rest = t.split("/", 1)
    return pkg + "/msg/" + rest

def pc2_points(msg):
    names = [f.name for f in msg.fields]
    offs = {f.name: f.offset for f in msg.fields}
    dts = {f.name: PC2_DT.get(f.datatype, "f") for f in msg.fields}
    ii = "intensity" if "intensity" in offs else None
    data = bytes(msg.data)
    step = msg.point_step
    n = len(data) // step
    for i in range(n):
        p = i * step
        x = struct.unpack_from("<" + dts["x"], data, p + offs["x"])[0]
        y = struct.unpack_from("<" + dts["y"], data, p + offs["y"])[0]
        z = struct.unpack_from("<" + dts["z"], data, p + offs["z"])[0]
        it = struct.unpack_from("<" + dts[ii], data, p + offs[ii])[0] if ii else 0.0
        yield float(x), float(y), float(z), float(it)

def scan_points(msg, height):
    r0 = msg.range_min
    for k, r in enumerate(msg.ranges):
        if not (r0 <= r <= msg.range_max) or r != r:
            continue
        a = msg.angle_min + k * msg.angle_increment
        yield r * math.cos(a), r * math.sin(a), height, 0.0

def livox_points(msg):
    for p in msg.points:
        yield float(p.x), float(p.y), float(p.z), float(p.reflectance)

def read_bag(path: Path, max_frames, scan_height):
    from rosbags.highlevel import AnyReader
    from rosbags.typesys import Stores, get_typestore, get_types_from_msg

    magic = path.read_bytes()[:8]
    is_ros1 = magic.startswith(b"#ROSBAG")
    store = get_typestore(Stores.ROS1_NOETIC if is_ros1 else Stores.ROS2_HUMBLE)
    for pkg in ("livox_ros_driver", "livox_ros_driver2"):
        try:
            cp = pkg + "/msg/CustomPoint"
            cm = pkg + "/msg/CustomMsg"
            t1 = get_types_from_msg(LIVOX_POINT, cp)
            t2 = get_types_from_msg(LIVOX_MSG.replace("livox_ros_driver/CustomPoint[]", cp + "[]"), cm)
            store.register({**t1, **t2})
        except Exception:
            pass

    deser = store.deserialize_ros1 if is_ros1 else store.deserialize_cdr
    pts = []
    frames = 0
    topics_seen = {}

    def handle(t, raw):
        nonlocal frames
        base = t.split("/")[-1].split("<")[0]
        tn = _store_type(t)
        try:
            if base == "PointCloud2":
                pts.extend(pc2_points(deser(raw, tn)))
            elif base == "CustomMsg":
                pts.extend(livox_points(deser(raw, tn)))
            elif base == "LaserScan":
                pts.extend(scan_points(deser(raw, tn), scan_height))
            else:
                return
            frames += 1
        except Exception as e:
            topics_seen[t + " DECODE_ERR"] = str(e)[:60]

    try:
        with AnyReader([path]) as reader:
            for conn, _ts, raw in reader.messages():
                topics_seen[conn.topic + " (" + conn.msgtype + ")"] = (
                    topics_seen.get(conn.topic + " (" + conn.msgtype + ")", 0) + 1)
                handle(conn.msgtype, raw)
                if max_frames and frames >= max_frames:
                    break
    except Exception as e:
        if not is_ros1:
            raise
        print("索引损坏/格式老旧，启用线性扫描恢复模式：", str(e)[:80])
        for t, raw in ros1_linear(path):
            if not t:
                continue
            topics_seen[t] = topics_seen.get(t, 0) + 1
            handle(t, raw)
            if max_frames and frames >= max_frames:
                break
    print("bag topics 消息数：")
    for k, v in topics_seen.items():
        print("  ", k, v)
    return pts

def main():
    ap = argparse.ArgumentParser(description="通用点云地图导入（ROS1/ROS2/Autoware/Apollo-PCD/MID360）")
    ap.add_argument("input")
    ap.add_argument("output")
    ap.add_argument("--step", type=int, default=1, help="每 N 个点取 1 个（抽稀）")
    ap.add_argument("--zmin", type=float, default=None)
    ap.add_argument("--zmax", type=float, default=None)
    ap.add_argument("--max-frames", type=int, default=0, help="bag 最多累积多少帧，0=全部")
    ap.add_argument("--scan-height", type=float, default=0.2, help="LaserScan 平面高度")
    args = ap.parse_args()

    p = Path(args.input)
    suf = p.suffix.lower()
    if suf == ".pcd":
        pts = parse_pcd(p)
    elif suf in (".csv", ".txt", ".xyz"):
        pts = parse_text(p)
    elif suf in (".bag", ".mcap", ".db3") or p.is_dir():
        pts = read_bag(p, args.max_frames, args.scan_height)
    else:
        raise SystemExit("不支持的格式 " + suf + "（.las/.laz 请先用 PDAL 转 PCD；Apollo .record 请先导出 PCD/CSV）")

    if args.zmin is not None:
        pts = [q for q in pts if q[2] >= args.zmin]
    if args.zmax is not None:
        pts = [q for q in pts if q[2] <= args.zmax]
    if args.step > 1:
        pts = pts[:: args.step]

    with open(args.output, "w", newline="") as f:
        w = csv.writer(f)
        for q in pts:
            w.writerow([round(q[0], 3), round(q[1], 3), round(q[2], 3), round(q[3], 3)])
    print(f"✓ 写出 {len(pts)} 个点 -> {args.output}")

if __name__ == "__main__":
    main()
