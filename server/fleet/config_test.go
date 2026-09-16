package main

import "testing"

func TestConfigSetForActorIsValidatedAndAtomic(t *testing.T) {
	c := NewConfigStore()
	before := c.All()

	if _, rejected, err := c.SetForActor(map[string]any{
		"bev_cell": 0.2,
		// Browser-only settings must never become server configuration by
		// accident; this also proves an invalid batch is all-or-nothing.
		"amap_key": "browser-only",
	}, "ops_admin"); err == nil || len(rejected) != 1 {
		t.Fatalf("invalid batch should be rejected atomically: err=%v rejected=%v", err, rejected)
	}
	if got := c.All()["bev_cell"]; got != before["bev_cell"] {
		t.Fatalf("invalid batch partially changed bev_cell: before=%v after=%v", before["bev_cell"], got)
	}

	merged, rejected, err := c.SetForActor(map[string]any{
		"bev_cell":               0.2,
		"telemetry_hz":           5.0,
		"open_api_enabled":       false,
		"open_lockout_threshold": 3.0,
	}, "ops_admin")
	if err != nil || len(rejected) != 0 {
		t.Fatalf("valid batch rejected: err=%v rejected=%v", err, rejected)
	}
	if merged["bev_cell"] != 0.2 || merged["telemetry_hz"] != 5.0 || merged["open_api_enabled"] != false {
		t.Fatalf("valid values not applied: %+v", merged)
	}
}

func TestConfigSetForActorRejectsUnsafeRanges(t *testing.T) {
	c := NewConfigStore()
	for key, value := range map[string]any{
		"bev_cell":        0.0,
		"telemetry_hz":    100.0,
		"replay_window_s": 0.0,
		"rate_limit":      1.5,
		"tdt_origin":      "91,181",
		"chassis_type":    "unknown",
	} {
		if _, rejected, err := c.SetForActor(map[string]any{key: value}, "ops_admin"); err == nil || len(rejected) != 1 {
			t.Fatalf("unsafe value accepted for %s: err=%v rejected=%v", key, err, rejected)
		}
	}
}
