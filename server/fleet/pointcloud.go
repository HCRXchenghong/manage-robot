package main

// pointcloud.go：阶段 1 合成激光雷达点云（演示场站场景）。
// GET /api/pointcloud?vehicle_id= 返回：
//   static   —— 静态基础设施（地面网格 + 围墙 + 集装箱障碍），前端渲染为暗蓝；
//   vehicles —— 每辆车绕其 pose 的 360° x 3 线局部扫描，前端按车辆状态着色。
// 阶段 2 将换成 COPC/PCD 真实切片，接口形状保持不变。
// positions 为 xyz xyz ... 扁平数组；数值保留 2 位小数控制体积。

import (
	"hash/fnv"
	"math"
	"math/rand"
	"time"
)

type cloudPart struct {
	Count       int       `json:"count"`
	Positions   []float64 `json:"positions"`
	Intensities []float64 `json:"intensities"`
}

type vehicleCloud struct {
	VehicleID   string    `json:"vehicle_id"`
	Pose        Pose      `json:"pose"`
	Count       int       `json:"count"`
	Positions   []float64 `json:"positions"`
	Intensities []float64 `json:"intensities"`
}

type pointCloudResp struct {
	SceneID     string         `json:"scene_id"`
	GeneratedNS int64          `json:"generated_ns"`
	Static      cloudPart      `json:"static"`
	Vehicles    []vehicleCloud `json:"vehicles"`
}

type pointCloudGen struct {
	static cloudPart
}

// newPointCloudGen 固定种子：静态场景每次启动完全一致（可复现、可对照）。
func newPointCloudGen() *pointCloudGen {
	r := rand.New(rand.NewSource(20260823))
	return &pointCloudGen{static: buildStaticScene(r)}
}

// buildStaticScene 240m x 180m 场站：地面网格 + 四面围墙 + 集装箱障碍。
func buildStaticScene(r *rand.Rand) cloudPart {
	var pos, inten []float64
	add := func(x, y, z, i float64) {
		pos = append(pos, round2(x), round2(y), round2(z))
		inten = append(inten, round2(i))
	}
	for x := 0.0; x <= 240; x += 4 {
		for y := 0.0; y <= 180; y += 4 {
			add(x+r.NormFloat64()*0.3, y+r.NormFloat64()*0.3, r.Float64()*0.06, 0.15+r.Float64()*0.2)
		}
	}
	wall := func(x0, y0, x1, y1 float64) {
		steps := int(math.Hypot(x1-x0, y1-y0) / 2)
		for i := 0; i <= steps; i++ {
			t := float64(i) / float64(max(steps, 1))
			x := x0 + (x1-x0)*t
			y := y0 + (y1-y0)*t
			for z := 0.0; z <= 2.8; z += 0.4 {
				add(x+r.NormFloat64()*0.05, y+r.NormFloat64()*0.05, z+r.Float64()*0.1, 0.5+r.Float64()*0.3)
			}
		}
	}
	wall(0, 0, 240, 0)
	wall(240, 0, 240, 180)
	wall(240, 180, 0, 180)
	wall(0, 180, 0, 0)
	boxes := []struct{ cx, cy, w, d, h float64 }{
		{60, 60, 12, 4, 3}, {150, 110, 8, 8, 2.5}, {100, 40, 6, 3, 2},
		{180, 150, 10, 5, 3.5}, {30, 100, 5, 5, 1.8},
	}
	for _, b := range boxes {
		for i := 0; i < 140; i++ {
			var x, y float64
			switch r.Intn(4) {
			case 0:
				x, y = b.cx-b.w/2+r.Float64()*b.w, b.cy-b.d/2
			case 1:
				x, y = b.cx-b.w/2+r.Float64()*b.w, b.cy+b.d/2
			case 2:
				x, y = b.cx-b.w/2, b.cy-b.d/2+r.Float64()*b.d
			default:
				x, y = b.cx+b.w/2, b.cy-b.d/2+r.Float64()*b.d
			}
			add(x, y, r.Float64()*b.h, 0.6+r.Float64()*0.3)
		}
	}
	return cloudPart{Count: len(inten), Positions: pos, Intensities: inten}
}

// generate 组装响应：静态场景 + 车辆扫描（可按 vehicle_id 过滤）。
func (g *pointCloudGen) generate(vehicles []VehicleSnap, only string) pointCloudResp {
	resp := pointCloudResp{
		SceneID:     "demo-yard-v1",
		GeneratedNS: time.Now().UnixNano(),
		Static:      g.static,
		Vehicles:    []vehicleCloud{},
	}
	for _, v := range vehicles {
		if only != "" && v.VehicleID != only {
			continue
		}
		resp.Vehicles = append(resp.Vehicles, vehicleScan(v))
	}
	return resp
}

// vehicleScan 绕车辆 pose 生成 360° x 3 线模拟扫描；种子含秒级时间，
// 每秒微变（让画面有呼吸感），同一秒内请求结果一致。
func vehicleScan(v VehicleSnap) vehicleCloud {
	seed := int64(fnvOf(v.VehicleID)) + time.Now().Unix()
	r := rand.New(rand.NewSource(seed))
	var pos, inten []float64
	for ray := 0; ray < 360; ray++ {
		a := float64(ray) * math.Pi / 180
		base := 6 + 4*math.Abs(math.Sin(3*a+float64(fnvOf(v.VehicleID)%7))) + r.Float64()*1.5
		for line := 0; line < 3; line++ {
			d := base * (1 - float64(line)*0.06)
			z := -0.4 + float64(line)*0.35 + r.Float64()*0.05
			pos = append(pos,
				round2(v.Pose.X+math.Cos(a)*d),
				round2(v.Pose.Y+math.Sin(a)*d),
				round2(z))
			inten = append(inten, round2(0.4+r.Float64()*0.6))
		}
	}
	return vehicleCloud{
		VehicleID:   v.VehicleID,
		Pose:        v.Pose,
		Count:       len(inten),
		Positions:   pos,
		Intensities: inten,
	}
}

// fnvOf 稳定字符串哈希（位姿表取模 / 扫描种子用）。
func fnvOf(s string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(s))
	return h.Sum32()
}
