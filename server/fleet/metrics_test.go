package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMetricsAccessAndExposition(t *testing.T) {
	st := &State{metrics: newRuntimeMetrics(), hub: NewHub()}
	st.metrics.httpRequests.Add(2)
	st.metrics.httpErrors.Add(1)

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:9800/metrics", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	w := httptest.NewRecorder()
	serveMetrics(st, w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "robot_agent_http_requests_total 2") {
		t.Fatalf("loopback metrics unavailable: code=%d body=%s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "http://fleet.example/metrics", nil)
	req.RemoteAddr = "10.0.0.8:12345"
	w = httptest.NewRecorder()
	serveMetrics(st, w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("metrics should be loopback-only without token: %d", w.Code)
	}

	st.setMetricsToken("metrics-secret")
	req = httptest.NewRequest(http.MethodGet, "http://fleet.example/metrics", nil)
	req.RemoteAddr = "10.0.0.8:12345"
	w = httptest.NewRecorder()
	serveMetrics(st, w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("token-protected metrics accepted missing token: %d", w.Code)
	}
	req.Header.Set("Authorization", "Bearer metrics-secret")
	w = httptest.NewRecorder()
	serveMetrics(st, w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("valid metrics token rejected: %d", w.Code)
	}
}
