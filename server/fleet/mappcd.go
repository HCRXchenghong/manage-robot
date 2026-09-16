package main

// mappcd.go：服务端解析三维点云地图（.pcd ascii/binary、.csv），
// 供地图中心浏览器内 3D 预览（GET /api/maps/{id}/points）。
// 口径与 map-engine/tools/map_convert.py 一致：仅需 x/y/z 字段，intensity 可选；
// PCD 二进制按 PCL 约定小端序；非有限值点丢弃。

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"
)

type pcdField struct {
	name string
	size int
	typ  byte // 'F' 浮点 / 'U' 无符号 / 'I' 有符号
	off  int  // 记录内偏移（字节）
}

// parsePCD 解析 PCD（ascii 或 binary），返回 (xyz 扁平, intensity 扁平, 错误)。
// intensity 缺失时返回空切片。
func parsePCD(data []byte) ([]float64, []float64, error) {
	// 头：逐行读到 DATA 行为止
	var fields, sizes, types []string
	var counts []string
	points := 0
	dataMode := ""
	rest := data
	for {
		i := bytesIndexByte(rest, '\n')
		if i < 0 {
			return nil, nil, fmt.Errorf("PCD 头不完整（缺 DATA 行）")
		}
		line := strings.TrimSpace(string(rest[:i]))
		rest = rest[i+1:]
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		switch strings.ToUpper(parts[0]) {
		case "FIELDS":
			fields = parts[1:]
		case "SIZE":
			sizes = parts[1:]
		case "TYPE":
			types = parts[1:]
		case "COUNT":
			counts = parts[1:]
		case "POINTS":
			n, err := strconv.Atoi(parts[1])
			if err != nil {
				return nil, nil, fmt.Errorf("PCD POINTS 非法：%s", parts[1])
			}
			points = n
		case "DATA":
			dataMode = strings.ToLower(parts[1])
		}
		if dataMode != "" {
			break
		}
	}
	if len(fields) == 0 || len(sizes) != len(fields) || len(types) != len(fields) {
		return nil, nil, fmt.Errorf("PCD 头字段不齐（FIELDS/SIZE/TYPE 不一致）")
	}
	if points <= 0 {
		return nil, nil, fmt.Errorf("PCD POINTS=0，无点可读")
	}

	// 各字段偏移与记录步长（COUNT>1 的字段按 size*count 占位，取值只取第一个）
	fl := make([]pcdField, len(fields))
	stride := 0
	for i, f := range fields {
		sz, err := strconv.Atoi(sizes[i])
		if err != nil || sz <= 0 || sz > 8 {
			return nil, nil, fmt.Errorf("PCD SIZE 非法：字段 %s=%s", f, sizes[i])
		}
		cnt := 1
		if i < len(counts) {
			if c, err := strconv.Atoi(counts[i]); err == nil && c > 0 {
				cnt = c
			}
		}
		fl[i] = pcdField{name: strings.ToLower(f), size: sz, typ: strings.ToUpper(types[i])[0], off: stride}
		stride += sz * cnt
	}
	idx := map[string]int{}
	for i, f := range fl {
		if _, ok := idx[f.name]; !ok {
			idx[f.name] = i
		}
	}
	for _, need := range []string{"x", "y", "z"} {
		if _, ok := idx[need]; !ok {
			return nil, nil, fmt.Errorf("PCD 缺字段 %s（fields=%s）", need, strings.Join(fields, ","))
		}
	}

	pos := make([]float64, 0, points*3)
	var inten []float64
	_, hasI := idx["intensity"]
	if hasI {
		inten = make([]float64, 0, points)
	}

	decodeAt := func(f pcdField, b []byte) float64 {
		switch {
		case f.typ == 'F' && f.size == 4:
			return float64(math.Float32frombits(binary.LittleEndian.Uint32(b)))
		case f.typ == 'F' && f.size == 8:
			return math.Float64frombits(binary.LittleEndian.Uint64(b))
		case f.typ == 'U':
			switch f.size {
			case 1:
				return float64(b[0])
			case 2:
				return float64(binary.LittleEndian.Uint16(b))
			case 4:
				return float64(binary.LittleEndian.Uint32(b))
			case 8:
				return float64(binary.LittleEndian.Uint64(b))
			}
		case f.typ == 'I':
			switch f.size {
			case 1:
				return float64(int8(b[0]))
			case 2:
				return float64(int16(binary.LittleEndian.Uint16(b)))
			case 4:
				return float64(int32(binary.LittleEndian.Uint32(b)))
			case 8:
				return float64(int64(binary.LittleEndian.Uint64(b)))
			}
		}
		return math.NaN()
	}

	valsPerPt := 0
	fieldValOff := make([]int, len(fl)) // 字段首值在记录值序列中的序号
	for i := range fl {
		fieldValOff[i] = valsPerPt
		cnt := 1
		if i < len(counts) {
			if c, err := strconv.Atoi(counts[i]); err == nil && c > 0 {
				cnt = c
			}
		}
		valsPerPt += cnt
	}

	switch dataMode {
	case "ascii":
		toks := strings.FieldsFunc(string(rest), func(r rune) bool {
			return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == ','
		})
		if valsPerPt == 0 {
			return nil, nil, fmt.Errorf("PCD ascii 无法确定每点值个数")
		}
		for p := 0; p < points && (p+1)*valsPerPt <= len(toks); p++ {
			b := p * valsPerPt
			at := func(name string) float64 {
				v, err := strconv.ParseFloat(toks[b+fieldValOff[idx[name]]], 64)
				if err != nil {
					return math.NaN()
				}
				return v
			}
			x, y, z := at("x"), at("y"), at("z")
			if !finite(x) || !finite(y) || !finite(z) {
				continue
			}
			pos = append(pos, x, y, z)
			if hasI {
				inten = append(inten, at("intensity"))
			}
		}
	case "binary":
		if len(rest) < points*stride {
			return nil, nil, fmt.Errorf("PCD 二进制体不足：%d < %d*%d", len(rest), points, stride)
		}
		for p := 0; p < points; p++ {
			rec := rest[p*stride : (p+1)*stride]
			x := decodeAt(fl[idx["x"]], rec[fl[idx["x"]].off:])
			y := decodeAt(fl[idx["y"]], rec[fl[idx["y"]].off:])
			z := decodeAt(fl[idx["z"]], rec[fl[idx["z"]].off:])
			if !finite(x) || !finite(y) || !finite(z) {
				continue
			}
			pos = append(pos, x, y, z)
			if hasI {
				f := fl[idx["intensity"]]
				inten = append(inten, decodeAt(f, rec[f.off:]))
			}
		}
	default:
		return nil, nil, fmt.Errorf("暂不支持的 PCD DATA 类型：%s（仅 ascii/binary）", dataMode)
	}
	if len(pos) == 0 {
		return nil, nil, fmt.Errorf("PCD 无有效点")
	}
	return pos, inten, nil
}

// parseCSVPoints 解析 x,y,z[,intensity] CSV（首行可为表头，脏行跳过）。
// 与 map-engine/tools/pcd_to_csv.py 的输出同口径。
func parseCSVPoints(data []byte) ([]float64, []float64, error) {
	var pos, inten []float64
	for _, ln := range strings.Split(string(data), "\n") {
		parts := strings.Split(strings.TrimSpace(ln), ",")
		if len(parts) < 3 {
			continue
		}
		x, e1 := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
		y, e2 := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
		z, e3 := strconv.ParseFloat(strings.TrimSpace(parts[2]), 64)
		if e1 != nil || e2 != nil || e3 != nil ||
			!finite(x) || !finite(y) || !finite(z) {
			continue
		}
		pos = append(pos, x, y, z)
		if len(parts) >= 4 {
			if iv, err := strconv.ParseFloat(strings.TrimSpace(parts[3]), 64); err == nil {
				inten = append(inten, iv)
			}
		}
	}
	if len(pos) == 0 {
		return nil, nil, fmt.Errorf("CSV 无有效点（需 x,y,z[,intensity]）")
	}
	return pos, inten, nil
}

func bytesIndexByte(b []byte, c byte) int {
	for i := range b {
		if b[i] == c {
			return i
		}
	}
	return -1
}

// finite：Go 标准库无 IsFinite，NaN/±Inf 均视为无效点。
func finite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}
