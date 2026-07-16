package cli

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// loadEnvFiles reads simple KEY=VALUE files and applies WARDEN_* keys that are
// not already set in the real environment (so `WARDEN_ISSUER=… warden login`
// and flags always win). Two locations, first hit per key wins:
//
//	./.env          — repo-local dev setup (running from a checkout)
//	~/.warden/env   — machine-wide setup (e.g. WARDEN_ENV=dev on a dev box)
//
// Lines starting with # and blank lines are ignored; values may be quoted.
func loadEnvFiles() {
	paths := []string{".env"}
	if home, err := wardenHome(); err == nil {
		paths = append(paths, filepath.Join(home, "env"))
	}
	for _, p := range paths {
		applyEnvFile(p)
	}
}

func applyEnvFile(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if !strings.HasPrefix(key, "WARDEN_") {
			continue // only our own namespace — a repo .env may hold other tools' vars
		}
		val = strings.TrimSpace(val)
		val = strings.Trim(val, `"'`)
		if os.Getenv(key) == "" {
			_ = os.Setenv(key, val)
		}
	}
}
