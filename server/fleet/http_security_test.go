package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGuardedPprofRequiresLoopbackAndSuperSession(t *testing.T) {
	handler := guardedPprof(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})

	cases := []struct {
		name   string
		remote string
		sess   *Session
		want   int
	}{
		{name: "remote is hidden", remote: "203.0.113.10:1234", want: http.StatusNotFound},
		{name: "loopback needs auth", remote: "127.0.0.1:1234", want: http.StatusUnauthorized},
		{name: "group admin is forbidden", remote: "127.0.0.1:1234", sess: &Session{Role: "group_admin"}, want: http.StatusForbidden},
		{name: "super admin reaches handler", remote: "127.0.0.1:1234", sess: &Session{Role: "super"}, want: http.StatusTeapot},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:9800/debug/pprof/", nil)
			req.RemoteAddr = tc.remote
			if tc.sess != nil {
				req = req.WithContext(context.WithValue(req.Context(), ctxSession, tc.sess))
			}
			resp := httptest.NewRecorder()
			handler(resp, req)
			if resp.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%q", resp.Code, tc.want, resp.Body.String())
			}
			if tc.want != http.StatusTeapot && strings.Contains(resp.Body.String(), "profile") {
				t.Fatal("guard rejected request but returned profile content")
			}
		})
	}
}

func TestReadJSONBodyEnforcesHardLimitForKnownAndChunkedBodies(t *testing.T) {
	tooLarge := strings.Repeat("a", 4097)
	for _, contentLength := range []int64{int64(len(tooLarge)), -1} {
		t.Run("content-length", func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9800/api/test", strings.NewReader(tooLarge))
			req.ContentLength = contentLength
			resp := httptest.NewRecorder()
			if got := readJSONBody(resp, req); got != nil {
				t.Fatal("oversized JSON body was parsed")
			}
			if resp.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status=%d want=%d", resp.Code, http.StatusRequestEntityTooLarge)
			}
		})
	}

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9800/api/test", strings.NewReader(`{"ok":true}`))
	resp := httptest.NewRecorder()
	got := readJSONBody(resp, req)
	if resp.Code != 200 || got["ok"] != true {
		t.Fatalf("valid JSON rejected: status=%d body=%v", resp.Code, got)
	}
}
