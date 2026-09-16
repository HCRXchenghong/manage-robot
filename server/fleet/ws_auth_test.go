package main

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestAllowWSOrigin(t *testing.T) {
	cases := []struct {
		name   string
		host   string
		origin string
		want   bool
	}{
		{name: "production same origin", host: "fleet.example.test", origin: "https://fleet.example.test", want: true},
		{name: "local Vite proxy", host: "127.0.0.1:9800", origin: "http://127.0.0.1:5173", want: true},
		{name: "different public origin", host: "fleet.example.test", origin: "https://evil.example.test", want: false},
		{name: "missing origin", host: "fleet.example.test", origin: "", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "http://"+tc.host+"/ws/fleet", nil)
			r.Host = tc.host
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if got := allowWSOrigin(r); got != tc.want {
				t.Fatalf("allowWSOrigin()=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestFilterSnapForKeepsOnlyAuthorizedScope(t *testing.T) {
	snap := FleetSnap{
		ServerTimeNS: time.Now().UnixNano(),
		Vehicles:     []VehicleSnap{{VehicleID: "vehicle-a", Group: "group-a"}, {VehicleID: "vehicle-b", Group: "group-b"}},
		Takeover:     TakeoverSnap{Active: true, Driver: "other-group-driver", LeaseID: "secret-lease"},
		Events: []EventSnap{
			{VehicleID: "vehicle-a", Text: "group-a event"},
			{VehicleID: "vehicle-b", Text: "group-b event"},
			{Text: "platform event"},
		},
	}

	filtered := filterSnapFor(snap, &Session{Role: "user", Groups: []string{"group-a"}})
	if len(filtered.Vehicles) != 1 || filtered.Vehicles[0].VehicleID != "vehicle-a" {
		t.Fatalf("vehicles leaked across groups: %+v", filtered.Vehicles)
	}
	if len(filtered.Events) != 1 || filtered.Events[0].VehicleID != "vehicle-a" {
		t.Fatalf("events leaked across groups: %+v", filtered.Events)
	}
	if filtered.Takeover.Active || filtered.Takeover.Driver != "" || filtered.Takeover.LeaseID != "" {
		t.Fatalf("unscoped takeover metadata leaked: %+v", filtered.Takeover)
	}

	super := filterSnapFor(snap, &Session{Role: "super"})
	if len(super.Vehicles) != 2 || len(super.Events) != 3 || !super.Takeover.Active {
		t.Fatalf("super scope unexpectedly filtered: %+v", super)
	}
}

func TestSessionSnapshotIsolatedAndRevocable(t *testing.T) {
	auth := &AuthStore{sessions: map[string]*Session{
		"token": {Token: "token", Username: "operator", Role: "user", Groups: []string{"group-a"}, CreatedNS: time.Now().UnixNano(), lastUse: time.Now()},
	}}

	snap := auth.SessionSnapshot("token")
	if snap == nil {
		t.Fatal("expected active session")
	}
	snap.Groups[0] = "mutated"
	if auth.sessions["token"].Groups[0] != "group-a" {
		t.Fatal("session snapshot aliases mutable authorization groups")
	}
	delete(auth.sessions, "token")
	if got := auth.SessionSnapshot("token"); got != nil {
		t.Fatalf("revoked session remained valid: %+v", got)
	}
}
