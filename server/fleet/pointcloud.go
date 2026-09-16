package main

// pointcloud.go defines the wire shape used by the 3D fleet view. Data is
// sourced only from a map uploaded by a vehicle or an operator; it must never
// synthesize a scene or a vehicle scan.

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

// pointCloudResp keeps the viewer contract while exposing provenance of the
// actual map parsed on the server.
type pointCloudResp struct {
	SceneID     string         `json:"scene_id"`
	MapID       string         `json:"map_id"`
	VehicleID   string         `json:"vehicle_id"`
	Version     int            `json:"version"`
	Name        string         `json:"name"`
	GeneratedNS int64          `json:"generated_ns"`
	Static      cloudPart      `json:"static"`
	Vehicles    []vehicleCloud `json:"vehicles"`
}
