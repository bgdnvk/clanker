package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bgdnvk/clanker/internal/clankercloud"
)

func TestCloudAppsPublishAndShareActivateAndSurfacePublicURL(t *testing.T) {
	for _, alias := range []string{"publish", "share"} {
		t.Run(alias, func(t *testing.T) {
			var requestMethod string
			var requestPath string
			httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				requestMethod = request.Method
				requestPath = request.URL.Path
				return testHTTPResponse(http.StatusOK, `{
					"ok":true,
					"app":{
						"id":"app_123",
						"publicUrl":"https://apps.example.test/a/team-crm"
					},
					"deployment":{"id":"dep_123"}
				}`), nil
			})}
			client := func() *clankercloud.AppsClient {
				return clankercloud.NewAppsClient(clankercloud.AppsClientOptions{
					BaseURL:    "http://127.0.0.1:8080/api",
					AccountKey: "account-token",
					HTTPClient: httpClient,
				})
			}
			command := newCloudAppsCommand(client)
			command.SilenceErrors = true
			command.SilenceUsage = true
			command.SetArgs([]string{alias, "app_123", "dep_123"})
			var stderr bytes.Buffer
			command.SetErr(&stderr)

			stdout, err := captureCloudAppsStdout(t, command.Execute)
			if err != nil {
				t.Fatalf("%s command: %v", alias, err)
			}
			if requestMethod != http.MethodPost ||
				requestPath != "/api/v1/apps/app_123/deployments/dep_123/activate" {
				t.Fatalf("%s request = %s %s", alias, requestMethod, requestPath)
			}
			if got := stderr.String(); got != "Public URL: https://apps.example.test/a/team-crm\n" {
				t.Fatalf("%s stderr = %q", alias, got)
			}

			var result clankercloud.AppsAPIResult
			if err := json.Unmarshal([]byte(stdout), &result); err != nil {
				t.Fatalf("%s stdout is not compatible JSON: %v\n%s", alias, err, stdout)
			}
			body, ok := result.Body.(map[string]any)
			app, appOK := body["app"].(map[string]any)
			if !ok || !appOK || app["publicUrl"] != "https://apps.example.test/a/team-crm" {
				t.Fatalf("%s JSON body = %#v", alias, result.Body)
			}
		})
	}
}

func TestCloudAppsUnpublishCommandUsesLifecycleRoute(t *testing.T) {
	var requestMethod string
	var requestPath string
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestMethod = request.Method
		requestPath = request.URL.Path
		return testHTTPResponse(http.StatusOK, `{"ok":true,"published":false}`), nil
	})}
	command := newCloudAppsCommand(func() *clankercloud.AppsClient {
		return clankercloud.NewAppsClient(clankercloud.AppsClientOptions{
			BaseURL:    "http://127.0.0.1:8080/api",
			AccountKey: "account-token",
			HTTPClient: httpClient,
		})
	})
	command.SilenceErrors = true
	command.SilenceUsage = true
	command.SetArgs([]string{"unpublish", "app_123"})

	stdout, err := captureCloudAppsStdout(t, command.Execute)
	if err != nil {
		t.Fatalf("unpublish command: %v", err)
	}
	if requestMethod != http.MethodPost || requestPath != "/api/v1/apps/app_123/unpublish" {
		t.Fatalf("unpublish request = %s %s", requestMethod, requestPath)
	}
	var result clankercloud.AppsAPIResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("unpublish stdout is not compatible JSON: %v\n%s", err, stdout)
	}
}

func TestCloudAppsDeleteCommandIsPermanentlyRetryable(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
	}{
		{name: "deleted", status: http.StatusNoContent},
		{name: "already deleted", status: http.StatusNotFound, body: `{"ok":false,"error":"app not found"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var requestMethod string
			var requestPath string
			httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				requestMethod = request.Method
				requestPath = request.URL.Path
				return testHTTPResponse(test.status, test.body), nil
			})}
			command := newCloudAppsCommand(func() *clankercloud.AppsClient {
				return clankercloud.NewAppsClient(clankercloud.AppsClientOptions{
					BaseURL:    "http://127.0.0.1:8080/api",
					AccountKey: "account-token",
					HTTPClient: httpClient,
				})
			})
			command.SilenceErrors = true
			command.SilenceUsage = true
			command.SetArgs([]string{"delete", "app_123"})

			stdout, err := captureCloudAppsStdout(t, command.Execute)
			if err != nil {
				t.Fatalf("delete command: %v", err)
			}
			if requestMethod != http.MethodDelete || requestPath != "/api/v1/apps/app_123" {
				t.Fatalf("delete request = %s %s", requestMethod, requestPath)
			}
			var result clankercloud.AppsAPIResult
			if err := json.Unmarshal([]byte(stdout), &result); err != nil {
				t.Fatalf("delete stdout is not compatible JSON: %v\n%s", err, stdout)
			}
			if result.Status != test.status {
				t.Fatalf("delete status = %d, want %d", result.Status, test.status)
			}
		})
	}
}

func TestCloudAppsParentRejectsUnknownBuildInsteadOfReturningHelpSuccess(t *testing.T) {
	command := newCloudAppsCommand(func() *clankercloud.AppsClient {
		t.Fatal("unknown command attempted an API request")
		return nil
	})
	command.SilenceErrors = true
	command.SilenceUsage = true
	command.SetArgs([]string{"build"})

	err := command.Execute()
	if err == nil {
		t.Fatal("unknown build command returned success")
	}
	if !strings.Contains(err.Error(), `unknown command "build"`) {
		t.Fatalf("unknown build error = %v", err)
	}
}

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

func TestAppDeploymentFlagsRequireExactlyOneRuntimeInput(t *testing.T) {
	if _, err := (appDeploymentFlags{}).input(); err == nil {
		t.Fatal("deployment flags accepted a missing runtime input")
	}
	if _, err := (appDeploymentFlags{
		html:        "<h1>Hello</h1>",
		appSpecJSON: `{"schemaVersion":1}`,
	}).input(); err == nil {
		t.Fatal("deployment flags accepted HTML and appSpec together")
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
	if input.AppSpec == nil ||
		input.AppSpec.Title != "Team CRM" ||
		input.AppSpec.Theme != "light" ||
		len(input.AppSpec.Blocks) != 1 ||
		input.HTML != nil {
		t.Fatalf("decoded app spec input = %#v", input)
	}
}

func TestAppDeploymentFlagsPreserveExecutableHTMLWithoutContentFiltering(t *testing.T) {
	html := `<!doctype html>
<script>
localStorage.setItem("crm", "ready")
fetch("https://api.example.test/contacts").then((response) => response.json())
window.open("https://example.test")
</script>
<form action="https://example.test/import"><input type="file"><button>Import</button></form>
<a download href="data:text/plain,hello">Download</a>`
	input, err := (appDeploymentFlags{html: html}).input()
	if err != nil {
		t.Fatalf("accept HTML runtime: %v", err)
	}
	if input.HTML == nil || *input.HTML != html || input.AppSpec != nil {
		t.Fatalf("HTML input = %#v, want exact supplied document", input)
	}
}

func TestAppDeploymentFlagsPreserveWhitespaceOnlyHTML(t *testing.T) {
	html := " \n\t "
	input, err := (appDeploymentFlags{html: html}).input()
	if err != nil {
		t.Fatalf("accept whitespace HTML: %v", err)
	}
	if input.HTML == nil || *input.HTML != html || input.Agentic != nil {
		t.Fatalf("whitespace HTML input = %#v, want exact bytes", input)
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

func TestCloudAppsDeploySendsOnlyExactHTML(t *testing.T) {
	html := `<!doctype html><script>document.body.textContent = "<CRM & ready>"</script>`
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{
			"ok":true,
			"deployment":{
				"id":"dep_123",
				"status":"ready",
				"runtime":"clanker-html-v1",
				"networkPolicy":"browser",
				"fileCount":1,
				"totalBytes":76
			}
		}`))
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
	command.SetArgs([]string{"app_123", "--html", html})
	if err := command.Execute(); err != nil {
		t.Fatalf("deploy HTML command: %v", err)
	}
	if len(payload) != 1 || payload["html"] != html {
		t.Fatalf("HTML deployment payload = %#v, want exact html only", payload)
	}
}

func TestCloudAppsDeploySendsCanonicalOptInAgenticConfig(t *testing.T) {
	html := `<!doctype html><script>fetch("/a/demo/__clanker/llm")</script>`
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{
			"ok":true,
			"deployment":{
				"id":"dep_agentic",
				"status":"ready",
				"runtime":"clanker-html-v1",
				"networkPolicy":"browser",
				"fileCount":1,
				"totalBytes":64,
				"exposure":{
					"agentic":{
						"enabled":true,
						"endpoint":"/a/{slug}/__clanker/llm",
						"providers":["gemini","kimi"],
						"dailyRequestLimit":25,
						"maxInputCharacters":16000,
						"maxOutputTokens":1024
					}
				}
			}
		}`))
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
		"--html", html,
		"--agentic-provider", "KIMI",
		"--agentic-provider", "gemini",
	})
	if err := command.Execute(); err != nil {
		t.Fatalf("deploy agentic HTML command: %v", err)
	}
	if len(payload) != 2 || payload["html"] != html {
		t.Fatalf("agentic deployment payload = %#v", payload)
	}
	agentic, ok := payload["agentic"].(map[string]any)
	if !ok {
		t.Fatalf("agentic payload = %#v", payload["agentic"])
	}
	providers, ok := agentic["providers"].([]any)
	if !ok ||
		len(agentic) != 4 ||
		len(providers) != 2 ||
		providers[0] != "gemini" ||
		providers[1] != "kimi" ||
		agentic["dailyRequestLimit"] != float64(25) ||
		agentic["maxInputCharacters"] != float64(16000) ||
		agentic["maxOutputTokens"] != float64(1024) {
		t.Fatalf("canonical agentic payload = %#v", agentic)
	}
}

func TestCloudAppsDeployExposesBothSupportedRuntimeInputsOnly(t *testing.T) {
	command := newCloudAppsDeployCmd(func() *clankercloud.AppsClient { return nil })
	for _, supported := range []string{"html", "html-file", "app-spec-json", "app-spec-file"} {
		if command.Flags().Lookup(supported) == nil {
			t.Fatalf("deployment flag --%s is missing", supported)
		}
	}
	for _, agentic := range []string{
		"agentic-provider",
		"agentic-daily-requests",
		"agentic-max-input-chars",
		"agentic-max-output-tokens",
	} {
		if command.Flags().Lookup(agentic) == nil {
			t.Fatalf("agentic deployment flag --%s is missing", agentic)
		}
	}
	for _, legacy := range []string{
		"files-json", "entrypoint", "spa",
		"data-summary-json", "exposure-json", "network-policy",
	} {
		if command.Flags().Lookup(legacy) != nil {
			t.Fatalf("legacy deployment flag --%s is still exposed", legacy)
		}
	}
}

func TestAppDeploymentFlagsRequireAgenticProviderAndHTMLRuntime(t *testing.T) {
	spec := `{"schemaVersion":1,"title":"Status","blocks":[{"type":"text","body":"Ready"}]}`
	maxDefaults := appDeploymentFlags{
		agenticProviders:          []string{"gemini"},
		agenticDailyRequests:      clankercloud.DefaultAppAgenticDailyRequests,
		agenticMaxInputCharacters: clankercloud.DefaultAppAgenticInputCharacters,
		agenticMaxOutputTokens:    clankercloud.DefaultAppAgenticOutputTokens,
	}

	withSpec := maxDefaults
	withSpec.appSpecJSON = spec
	if _, err := withSpec.input(); err == nil ||
		!strings.Contains(err.Error(), "only supported with --html") {
		t.Fatalf("declarative agentic error = %v", err)
	}

	limitsOnly := appDeploymentFlags{
		html:             "<!doctype html>",
		agenticLimitsSet: true,
	}
	if _, err := limitsOnly.input(); err == nil ||
		!strings.Contains(err.Error(), "--agentic-provider is required") {
		t.Fatalf("agentic limits without provider error = %v", err)
	}

	command := newCloudAppsDeployCmd(func() *clankercloud.AppsClient { return nil })
	command.SetArgs([]string{
		"app_123",
		"--html", "<!doctype html>",
		"--agentic-daily-requests", "3",
	})
	if err := command.Execute(); err == nil ||
		!strings.Contains(err.Error(), "--agentic-provider is required") {
		t.Fatalf("command agentic limits without provider error = %v", err)
	}
}

func TestAppDeploymentFlagsApplyCustomAgenticLimits(t *testing.T) {
	input, err := (appDeploymentFlags{
		html:                      "<!doctype html>",
		agenticProviders:          []string{"kimi"},
		agenticDailyRequests:      3,
		agenticMaxInputCharacters: 4000,
		agenticMaxOutputTokens:    256,
		agenticLimitsSet:          true,
	}).input()
	if err != nil {
		t.Fatalf("custom agentic limits: %v", err)
	}
	if input.Agentic == nil ||
		strings.Join(input.Agentic.Providers, ",") != "kimi" ||
		input.Agentic.DailyRequestLimit != 3 ||
		input.Agentic.MaxInputCharacters != 4000 ||
		input.Agentic.MaxOutputTokens != 256 {
		t.Fatalf("custom agentic input = %#v", input.Agentic)
	}
}

func TestAppDeploymentFlagsReadBoundedRuntimeFiles(t *testing.T) {
	directory := t.TempDir()
	html := "<!doctype html><script>document.body.textContent = 'works'</script>"
	htmlPath := filepath.Join(directory, "index.html")
	if err := os.WriteFile(htmlPath, []byte(html), 0o600); err != nil {
		t.Fatalf("write HTML file: %v", err)
	}
	htmlInput, err := (appDeploymentFlags{htmlFile: htmlPath}).input()
	if err != nil {
		t.Fatalf("read HTML file: %v", err)
	}
	if htmlInput.HTML == nil || *htmlInput.HTML != html || htmlInput.AppSpec != nil {
		t.Fatalf("HTML file input = %#v", htmlInput)
	}

	specPath := filepath.Join(directory, "app-spec.json")
	if err := os.WriteFile(specPath, []byte(
		`{"schemaVersion":1,"title":"Status","blocks":[{"type":"text","body":"Ready"}]}`,
	), 0o600); err != nil {
		t.Fatalf("write appSpec file: %v", err)
	}
	specInput, err := (appDeploymentFlags{appSpecFile: specPath}).input()
	if err != nil {
		t.Fatalf("read appSpec file: %v", err)
	}
	if specInput.AppSpec == nil || specInput.AppSpec.Title != "Status" || specInput.HTML != nil {
		t.Fatalf("appSpec file input = %#v", specInput)
	}
}

func TestAppDeploymentFlagsRejectInvalidOrOversizedHTML(t *testing.T) {
	directory := t.TempDir()
	invalidPath := filepath.Join(directory, "invalid.html")
	if err := os.WriteFile(invalidPath, []byte{0xff, 0xfe}, 0o600); err != nil {
		t.Fatalf("write invalid UTF-8 HTML: %v", err)
	}
	if _, err := (appDeploymentFlags{htmlFile: invalidPath}).input(); err == nil ||
		!strings.Contains(err.Error(), "valid UTF-8") {
		t.Fatalf("invalid UTF-8 error = %v", err)
	}

	if _, err := (appDeploymentFlags{html: strings.Repeat("x", clankercloud.MaxAppHTMLBytes+1)}).input(); err == nil ||
		!strings.Contains(err.Error(), "exceeds 2097152 bytes") {
		t.Fatalf("oversized inline HTML error = %v", err)
	}

	oversizePath := filepath.Join(directory, "oversize.html")
	if err := os.WriteFile(oversizePath, []byte(strings.Repeat("x", clankercloud.MaxAppHTMLBytes+1)), 0o600); err != nil {
		t.Fatalf("write oversized HTML: %v", err)
	}
	if _, err := (appDeploymentFlags{htmlFile: oversizePath}).input(); err == nil ||
		!strings.Contains(err.Error(), "exceeds 2097152 bytes") {
		t.Fatalf("oversized HTML file error = %v", err)
	}

	if _, err := validateCLIAppHTML(nil, "app HTML"); err == nil ||
		!strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("empty HTML error = %v", err)
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

func captureCloudAppsStdout(t *testing.T, run func() error) (string, error) {
	t.Helper()

	previous := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("capture stdout: %v", err)
	}
	os.Stdout = writer
	defer func() {
		os.Stdout = previous
		_ = reader.Close()
		_ = writer.Close()
	}()

	runErr := run()
	if err := writer.Close(); err != nil {
		t.Fatalf("close captured stdout: %v", err)
	}
	os.Stdout = previous
	content, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read captured stdout: %v", err)
	}
	return string(content), runErr
}
