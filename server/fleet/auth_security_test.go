package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPasswordHashUsesPBKDF2AndConstantTimeVerification(t *testing.T) {
	salt := "7b4c1f8e9a2d"
	got := hashPassword(salt, "Strong!Password9")
	if !strings.HasPrefix(got, "pbkdf2-sha256$310000$") {
		t.Fatalf("password hash format=%q, want PBKDF2 format", got)
	}
	if !verifyPassword(salt, "Strong!Password9", got) {
		t.Fatal("correct password was rejected")
	}
	if verifyPassword(salt, "wrong-password", got) {
		t.Fatal("incorrect password was accepted")
	}
}

func TestCSRFRequiresDoubleSubmitTokenForAPIWrites(t *testing.T) {
	without := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9800/api/config", nil)
	if validCSRFRequest(without) {
		t.Fatal("API write without CSRF token was accepted")
	}

	with := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9800/api/config", nil)
	with.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "token-123"})
	with.Header.Set("X-CSRF-Token", "token-123")
	if !validCSRFRequest(with) {
		t.Fatal("matching CSRF cookie/header was rejected")
	}

	wrong := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9800/api/config", nil)
	wrong.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "token-123"})
	wrong.Header.Set("X-CSRF-Token", "token-456")
	if validCSRFRequest(wrong) {
		t.Fatal("mismatched CSRF token was accepted")
	}
}

func TestCSRFDoesNotGateSafeOrOpenAPIRequests(t *testing.T) {
	get := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:9800/api/config", nil)
	if !validCSRFRequest(get) {
		t.Fatal("safe API read was incorrectly gated by CSRF")
	}
	open := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9800/open/v1/nav/command", nil)
	if !validCSRFRequest(open) {
		t.Fatal("HMAC-authenticated open API was incorrectly gated by browser CSRF")
	}
}

func TestPasswordHashAcceptsLegacyRecordForMigration(t *testing.T) {
	salt := "legacy-salt"
	digest := sha256.Sum256([]byte(salt + ":" + "Strong!Password9"))
	legacy := hex.EncodeToString(digest[:])
	if !verifyPassword(salt, "Strong!Password9", legacy) {
		t.Fatal("legacy password record was not accepted for migration")
	}
	if verifyPassword(salt, "wrong-password", legacy) {
		t.Fatal("incorrect password matched legacy record")
	}
}
