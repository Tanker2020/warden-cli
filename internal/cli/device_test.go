package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// TestDeviceFlow drives requestDeviceCode + pollForToken against a fake
// control plane speaking the Better Auth device-authorization shapes:
// pending twice, then approved.
func TestDeviceFlow(t *testing.T) {
	var polls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/device/code", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["client_id"] != "warden-cli" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_client"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code":               "dev-123",
			"user_code":                 "WDJB-MJHT",
			"verification_uri":          "/device",
			"verification_uri_complete": "/device?user_code=WDJB-MJHT",
			"expires_in":                600,
			"interval":                  0, // exercise the CLI's minimum-interval floor
		})
	})
	mux.HandleFunc("/api/auth/device/token", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["grant_type"] != deviceGrantType || body["device_code"] != "dev-123" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
			return
		}
		if atomic.AddInt32(&polls, 1) < 3 {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "authorization_pending"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "session-token-abc",
			"token_type":   "Bearer",
			"expires_in":   3600,
		})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	da, err := requestDeviceCode(ts.URL, "warden-cli")
	if err != nil {
		t.Fatal(err)
	}
	if da.UserCode != "WDJB-MJHT" {
		t.Fatalf("user code = %q", da.UserCode)
	}
	// Relative verification URIs must be resolved against the issuer.
	if da.VerificationURI != ts.URL+"/device" {
		t.Fatalf("verification_uri = %q, want %q", da.VerificationURI, ts.URL+"/device")
	}
	if da.Interval < 1 {
		t.Fatalf("interval floor not applied: %d", da.Interval)
	}

	// Poll fast for the test.
	tr, err := pollForToken(ts.URL, "warden-cli", da.DeviceCode, 0, 60)
	if err != nil {
		t.Fatal(err)
	}
	if tr.AccessToken != "session-token-abc" {
		t.Fatalf("access token = %q", tr.AccessToken)
	}
	if got := atomic.LoadInt32(&polls); got != 3 {
		t.Fatalf("polls = %d, want 3 (two pending, one success)", got)
	}
}

// TestDeviceFlowDenied pins the access_denied error path.
func TestDeviceFlowDenied(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/device/token", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "access_denied"})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	if _, err := pollForToken(ts.URL, "warden-cli", "dev-123", 0, 60); err == nil {
		t.Fatal("want error for denied authorization")
	}
}
