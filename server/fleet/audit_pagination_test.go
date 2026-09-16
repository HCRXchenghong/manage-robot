package main

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAuditCursorRoundTripAndSourceValidation(t *testing.T) {
	raw := encodeAuditCursor(auditCursor{TsNS: time.Now().UnixNano(), ID: 42, Source: "api"})
	got, err := decodeAuditCursor(raw)
	if err != nil {
		t.Fatalf("decode cursor: %v", err)
	}
	if got.ID != 42 || got.Source != "api" {
		t.Fatalf("decoded cursor=%+v", got)
	}
	platform := encodeAuditCursor(auditCursor{TsNS: got.TsNS, ID: got.ID, Source: "platform"})
	if decoded, err := decodeAuditCursor(platform); err != nil || decoded.Source != "platform" {
		t.Fatalf("platform audit cursor was rejected: cursor=%+v err=%v", decoded, err)
	}
	if _, err := decodeAuditCursor(strings.Repeat("a", 513)); err == nil {
		t.Fatal("oversized audit cursor was accepted")
	}
}

func TestAuditMemoryPageUsesCursorAndBoundedFallback(t *testing.T) {
	o := NewOpenAPI(nil, nil, nil)
	newest := time.Now().UnixNano()
	o.logAudit(AuditEntry{TsNS: newest, ID: 3, KeyID: "k", Result: "ok"})
	o.logAudit(AuditEntry{TsNS: newest - 1, ID: 2, KeyID: "k", Result: "ok"})
	o.logAudit(AuditEntry{TsNS: newest - 2, ID: 1, KeyID: "k", Result: "denied"})
	page := o.auditMemoryPage(AuditFilter{Limit: 2, KeyID: "k", Result: "ok"})
	if len(page.Entries) != 2 || page.Entries[0].ID != 3 || page.Entries[1].ID != 2 {
		t.Fatalf("first fallback page=%+v", page.Entries)
	}
	cursor := encodeAuditCursor(auditCursor{TsNS: newest, ID: 3, Source: "api"})
	page = o.auditMemoryPage(AuditFilter{Limit: 2, KeyID: "k", Cursor: cursor})
	if len(page.Entries) != 2 || page.Entries[0].ID != 2 || page.Entries[1].ID != 1 {
		t.Fatalf("cursor fallback page=%+v", page.Entries)
	}
	if _, err := o.AuditPage(AuditFilter{Limit: 2, Cursor: "not-a-cursor"}); err == nil {
		t.Fatal("invalid cursor was silently accepted")
	}
}

func TestAuditFilterDateBounds(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/audit?limit=20&from=2026-09-16&to=2026-09-17", nil)
	f, err := auditFilterFromQuery(r)
	if err != nil {
		t.Fatalf("date filter: %v", err)
	}
	if f.From == nil || f.To == nil || f.To.Sub(*f.From) != 48*time.Hour {
		t.Fatalf("bounds from=%v to=%v", f.From, f.To)
	}
	bad := httptest.NewRequest("GET", "/api/audit?from=2026-09-18&to=2026-09-17", nil)
	if _, err := auditFilterFromQuery(bad); err == nil {
		t.Fatal("reversed date range was accepted")
	}
}

func TestOpenAPIClientIPDoesNotTrustUnconfiguredForwardingHeader(t *testing.T) {
	cfg := NewConfigStore()
	o := NewOpenAPI(nil, cfg, nil)
	direct := httptest.NewRequest("GET", "/open/v1/vehicles", nil)
	direct.RemoteAddr = "10.10.10.10:443"
	direct.Header.Set("X-Forwarded-For", "203.0.113.10")
	if got := o.clientIP(direct); got != "10.10.10.10" {
		t.Fatalf("untrusted X-Forwarded-For changed source IP to %q", got)
	}
	if _, rejected, err := cfg.SetForActor(map[string]any{"trusted_proxy_cidrs": "10.10.10.0/24"}, "ops_admin"); err != nil || len(rejected) != 0 {
		t.Fatalf("trusted proxy config rejected: err=%v rejected=%v", err, rejected)
	}
	if got := o.clientIP(direct); got != "203.0.113.10" {
		t.Fatalf("configured proxy did not provide client IP, got %q", got)
	}
}
