package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Credentials is the token material `warden login` persists to
// ~/.warden/credentials.json. The access token is a Better Auth session token
// issued by the control plane's device-authorization flow — it is opaque (not
// a JWT) and has no refresh token; when it expires the user logs in again.
type Credentials struct {
	IssuerURL       string    `json:"issuer_url"`
	ClientID        string    `json:"client_id"`
	ControlPlaneURL string    `json:"control_plane_url,omitempty"`
	IngestURL       string    `json:"ingest_url,omitempty"`
	EventToken      string    `json:"event_token,omitempty"` // X-SAC-Token for the tenant's /events
	TokenType       string    `json:"token_type,omitempty"`
	AccessToken     string    `json:"access_token"`
	Expiry          time.Time `json:"expiry,omitempty"`
}

// wardenHome is the directory holding credentials.json. WARDEN_HOME overrides
// the default ~/.warden (used by tests and non-default setups).
func wardenHome() (string, error) {
	if h := os.Getenv("WARDEN_HOME"); h != "" {
		return h, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".warden"), nil
}

func credentialsPath() (string, error) {
	dir, err := wardenHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "credentials.json"), nil
}

// loadCredentials reads the stored credentials. A missing file surfaces as
// os.ErrNotExist so callers can distinguish "not logged in" from a real error.
func loadCredentials() (*Credentials, error) {
	p, err := credentialsPath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var c Credentials
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", p, err)
	}
	return &c, nil
}

func saveCredentials(c *Credentials) error {
	dir, err := wardenHome()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "credentials.json"), b, 0o600)
}

func clearCredentials() error {
	p, err := credentialsPath()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// expired reports whether the access token is at/near expiry (60s skew). A
// zero Expiry is treated as non-expiring.
func (c *Credentials) expired() bool {
	if c.Expiry.IsZero() {
		return false
	}
	return time.Now().Add(60 * time.Second).After(c.Expiry)
}

// resolveBearerToken returns the token to send to the control plane, in order
// of precedence: an explicit value (--token), the WARDEN_TOKEN env var, then
// the token stored by `warden login`. Returns "" when none exist so the caller
// can produce a clear "run warden login" error.
func resolveBearerToken(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if t := os.Getenv("WARDEN_TOKEN"); t != "" {
		return t, nil
	}
	c, err := loadCredentials()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	if c.AccessToken == "" {
		return "", nil
	}
	if c.expired() {
		return "", fmt.Errorf("login expired — run `warden login`")
	}
	return c.AccessToken, nil
}
