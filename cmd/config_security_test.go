package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRedactConfigForDisplay(t *testing.T) {
	input := []byte(`ai:
  providers:
    openai:
      api_key: sk-live-secret
      api_key_env: OPENAI_API_KEY
backend:
  api_key: backend-secret
verda:
  client_id: public-client
  client_secret: private-client-secret
`)

	got := redactConfigForDisplay(input)
	for _, secret := range []string{"sk-live-secret", "backend-secret", "private-client-secret"} {
		if strings.Contains(got, secret) {
			t.Fatalf("redacted config leaked %q in:\n%s", secret, got)
		}
	}
	for _, want := range []string{
		"api_key: [redacted]",
		"api_key_env: OPENAI_API_KEY",
		"client_id: public-client",
		"client_secret: [redacted]",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("redacted config missing %q in:\n%s", want, got)
		}
	}
}

func TestHardenUserConfigFileTightensLooseConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode bits not meaningful on Windows")
	}
	path := filepath.Join(t.TempDir(), ".clanker.yaml")
	if err := os.WriteFile(path, []byte("backend:\n  api_key: secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := hardenUserConfigFile(path); err != nil {
		t.Fatalf("hardenUserConfigFile: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("config mode = %04o, want 0600", got)
	}
}
