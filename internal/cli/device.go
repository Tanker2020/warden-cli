package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// The control plane runs Better Auth's device-authorization plugin, which
// speaks RFC 8628 shapes over JSON bodies at these paths (relative to the
// Nyxtra base URL):
//
//	POST /api/auth/device/code   {client_id}                → device_code, user_code, verification_uri…
//	POST /api/auth/device/token  {grant_type, device_code, client_id}
//
// The returned access_token is a Better Auth session token (opaque, no
// refresh); errors use the RFC codes (authorization_pending, slow_down, …).
const deviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"

// deviceAuthResp is the response from the device authorization endpoint.
type deviceAuthResp struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
	Error                   string `json:"error"`
	ErrorDesc               string `json:"error_description"`
}

// tokenResp is the (subset of the) token endpoint response.
type tokenResp struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
	Scope       string `json:"scope"`
	Error       string `json:"error"`
	ErrorDesc   string `json:"error_description"`
}

func deviceHTTP() *http.Client { return &http.Client{Timeout: 30 * time.Second} }

func postDeviceJSON(url string, payload any, out any) (int, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	resp, err := deviceHTTP().Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			return resp.StatusCode, fmt.Errorf("bad response (%d): %s", resp.StatusCode, strings.TrimSpace(string(b)))
		}
	}
	return resp.StatusCode, nil
}

// requestDeviceCode starts the device authorization grant, returning the user
// code + verification URL to show the user and the device code to poll with.
func requestDeviceCode(issuer, clientID string) (*deviceAuthResp, error) {
	var d deviceAuthResp
	status, err := postDeviceJSON(strings.TrimRight(issuer, "/")+"/api/auth/device/code",
		map[string]string{"client_id": clientID}, &d)
	if err != nil {
		return nil, fmt.Errorf("device authorization: %w", err)
	}
	if status != http.StatusOK || d.DeviceCode == "" {
		msg := d.ErrorDesc
		if msg == "" {
			msg = d.Error
		}
		return nil, fmt.Errorf("device authorization failed (%d): %s", status, msg)
	}
	// Relative verification URIs (the plugin default "/device") resolve
	// against the issuer.
	if strings.HasPrefix(d.VerificationURI, "/") {
		d.VerificationURI = strings.TrimRight(issuer, "/") + d.VerificationURI
	}
	if strings.HasPrefix(d.VerificationURIComplete, "/") {
		d.VerificationURIComplete = strings.TrimRight(issuer, "/") + d.VerificationURIComplete
	}
	if d.Interval <= 0 {
		d.Interval = 5
	}
	return &d, nil
}

// pollForToken polls the token endpoint with the device code until the user
// approves in the browser, the code expires, or an error is returned. It
// honors the RFC 8628 authorization_pending / slow_down back-pressure signals.
func pollForToken(issuer, clientID, deviceCode string, interval, expiresIn int) (*tokenResp, error) {
	payload := map[string]string{
		"grant_type":  deviceGrantType,
		"device_code": deviceCode,
		"client_id":   clientID,
	}
	if expiresIn <= 0 {
		expiresIn = 600
	}
	deadline := time.Now().Add(time.Duration(expiresIn) * time.Second)
	wait := time.Duration(interval) * time.Second
	url := strings.TrimRight(issuer, "/") + "/api/auth/device/token"
	for {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("device code expired before authorization")
		}
		time.Sleep(wait)
		var tr tokenResp
		status, err := postDeviceJSON(url, payload, &tr)
		if err != nil {
			return nil, err
		}
		switch tr.Error {
		case "":
			if status == http.StatusOK && tr.AccessToken != "" {
				return &tr, nil
			}
			return nil, fmt.Errorf("token endpoint returned %d without a token", status)
		case "authorization_pending":
			// User hasn't approved yet — keep waiting at the current interval.
		case "slow_down":
			wait += 5 * time.Second
		case "access_denied":
			return nil, fmt.Errorf("authorization was denied")
		case "expired_token":
			return nil, fmt.Errorf("device code expired before authorization")
		default:
			return nil, fmt.Errorf("authorization error: %s: %s", tr.Error, tr.ErrorDesc)
		}
	}
}
