package cmd

import (
	"net/http"
	"net/http/httptest"
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

func TestAppDeploymentFlagsRequireOneSourceAndPreserveEmptyFiles(t *testing.T) {
	both := appDeploymentFlags{
		html:      "<h1>hello</h1>",
		filesJSON: `[{"path":"index.html","content":"hello"}]`,
	}
	if _, err := both.input(); err == nil {
		t.Fatal("deployment flags accepted both HTML and files")
	}

	emptyFile := appDeploymentFlags{
		filesJSON:  `[{"path":"index.html","content":""}]`,
		entrypoint: "index.html",
	}
	input, err := emptyFile.input()
	if err != nil {
		t.Fatalf("decode empty file: %v", err)
	}
	if len(input.Files) != 1 || input.Files[0].Content == nil || *input.Files[0].Content != "" {
		t.Fatalf("empty file was not preserved: %#v", input.Files)
	}
}
