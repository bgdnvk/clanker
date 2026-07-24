package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bgdnvk/clanker/internal/clankercloud"
)

func TestCloudAppsCreateGeneratesCanonicalIdempotencyHeader(t *testing.T) {
	var idempotencyKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idempotencyKey = r.Header.Get("Idempotency-Key")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true,"app":{"id":"app_0123456789abcdefabcd","status":"draft"}}`))
	}))
	defer server.Close()

	client := func() *clankercloud.AppsClient {
		return clankercloud.NewAppsClient(clankercloud.AppsClientOptions{
			BaseURL:    server.URL + "/api",
			AccountKey: "account-token",
			HTTPClient: server.Client(),
		})
	}
	command := newCloudAppsCreateCmd(client)
	command.SetArgs([]string{"Team CRM"})
	if err := command.Execute(); err != nil {
		t.Fatalf("create command: %v", err)
	}
	if _, err := clankercloud.ValidateAppIdempotencyKey(idempotencyKey); err != nil {
		t.Fatalf("generated Idempotency-Key %q: %v", idempotencyKey, err)
	}
	if !strings.HasPrefix(idempotencyKey, "cli-create-") {
		t.Fatalf("generated Idempotency-Key = %q, want cli-create prefix", idempotencyKey)
	}
}

func TestResolveCLIAppIdempotencyKeyPreservesValidExplicitKey(t *testing.T) {
	const explicit = "manual-retry-key"
	got, err := resolveCLIAppIdempotencyKey(explicit, "create")
	if err != nil {
		t.Fatalf("resolve explicit key: %v", err)
	}
	if got != explicit {
		t.Fatalf("resolved key = %q, want %q", got, explicit)
	}
}

func TestAppDeploymentFlagsRequireAndDecodeDeclarativeSpec(t *testing.T) {
	if _, err := (appDeploymentFlags{}).input(); err == nil {
		t.Fatal("deployment flags accepted a missing app spec")
	}
	flags := appDeploymentFlags{
		appSpecJSON: `{
			"schemaVersion":1,
			"title":"Team CRM",
			"blocks":[{"type":"table","columns":["Name"],"rows":[["Ada"]]}]
		}`,
	}
	input, err := flags.input()
	if err != nil {
		t.Fatalf("decode app spec: %v", err)
	}
	if input.Title != "Team CRM" || input.Theme != "light" || len(input.Blocks) != 1 {
		t.Fatalf("decoded app spec = %#v", input)
	}
}

func TestCloudAppsDeploySendsOnlyAppSpec(t *testing.T) {
	var payload map[string]any
	var idempotencyKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idempotencyKey = r.Header.Get("Idempotency-Key")
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true,"deployment":{"id":"dep_123","status":"ready"}}`))
	}))
	defer server.Close()

	client := func() *clankercloud.AppsClient {
		return clankercloud.NewAppsClient(clankercloud.AppsClientOptions{
			BaseURL:    server.URL + "/api",
			AccountKey: "account-token",
			HTTPClient: server.Client(),
		})
	}
	command := newCloudAppsDeployCmd(client)
	command.SetArgs([]string{
		"app_123",
		"--app-spec-json",
		`{"schemaVersion":1,"title":"Team CRM","blocks":[{"type":"text","body":"Ready"}]}`,
	})
	if err := command.Execute(); err != nil {
		t.Fatalf("deploy command: %v", err)
	}
	if len(payload) != 1 {
		t.Fatalf("deployment payload = %#v, want exactly appSpec", payload)
	}
	appSpec, ok := payload["appSpec"].(map[string]any)
	if !ok || appSpec["title"] != "Team CRM" || appSpec["theme"] != "light" {
		t.Fatalf("appSpec payload = %#v", payload["appSpec"])
	}
	for _, forbidden := range []string{
		"html", "files", "entrypoint", "spa", "networkPolicy", "exposure", "dataSummary",
	} {
		if _, exists := payload[forbidden]; exists {
			t.Fatalf("deployment payload contains legacy field %q: %#v", forbidden, payload)
		}
	}
	if _, err := clankercloud.ValidateAppIdempotencyKey(idempotencyKey); err != nil {
		t.Fatalf("generated Idempotency-Key %q: %v", idempotencyKey, err)
	}
}

func TestCloudAppsDeployDoesNotExposeLegacyArtifactFlags(t *testing.T) {
	command := newCloudAppsDeployCmd(func() *clankercloud.AppsClient { return nil })
	for _, legacy := range []string{
		"html", "html-file", "files-json", "entrypoint", "spa",
		"data-summary-json", "exposure-json", "network-policy",
	} {
		if command.Flags().Lookup(legacy) != nil {
			t.Fatalf("legacy deployment flag --%s is still exposed", legacy)
		}
	}
}

func TestReadBoundedAppFileWithLimitRequiresRegularBoundedInput(t *testing.T) {
	directory := t.TempDir()
	exactPath := filepath.Join(directory, "exact.json")
	if err := os.WriteFile(exactPath, []byte("1234"), 0o600); err != nil {
		t.Fatalf("write exact file: %v", err)
	}
	content, err := readBoundedAppFileWithLimit(exactPath, 4)
	if err != nil {
		t.Fatalf("read exact-limit file: %v", err)
	}
	if string(content) != "1234" {
		t.Fatalf("exact-limit content = %q, want 1234", content)
	}

	oversizePath := filepath.Join(directory, "oversize.json")
	if err := os.WriteFile(oversizePath, []byte("12345"), 0o600); err != nil {
		t.Fatalf("write oversize file: %v", err)
	}
	if _, err := readBoundedAppFileWithLimit(oversizePath, 4); err == nil ||
		!strings.Contains(err.Error(), "exceeds 4 bytes") {
		t.Fatalf("oversize error = %v, want bounded-size rejection", err)
	}

	if _, err := readBoundedAppFileWithLimit(directory, 4); err == nil ||
		!strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("directory error = %v, want non-regular-file rejection", err)
	}
}
