package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestApplyEnvFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "env")
	content := "# comment\n\nWARDEN_ENV=dev\nWARDEN_ISSUER=\"http://example:1234\"\nOTHER_TOOL=ignored\nWARDEN_PRESET=file-loses\n"
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WARDEN_ENV", "")
	t.Setenv("WARDEN_ISSUER", "")
	t.Setenv("OTHER_TOOL", "")
	t.Setenv("WARDEN_PRESET", "real-env-wins")

	applyEnvFile(p)

	if got := os.Getenv("WARDEN_ENV"); got != "dev" {
		t.Fatalf("WARDEN_ENV = %q, want dev", got)
	}
	if got := os.Getenv("WARDEN_ISSUER"); got != "http://example:1234" {
		t.Fatalf("WARDEN_ISSUER = %q (quotes should be stripped)", got)
	}
	if got := os.Getenv("OTHER_TOOL"); got != "" {
		t.Fatalf("OTHER_TOOL leaked from env file: %q", got)
	}
	if got := os.Getenv("WARDEN_PRESET"); got != "real-env-wins" {
		t.Fatalf("real environment must beat the file, got %q", got)
	}
}
