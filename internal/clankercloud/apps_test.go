package clankercloud

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAppsClientLifecycleRoutesAndPrivatePayloads(t *testing.T) {
	var requests []string
	var createPayload map[string]any
	var deploymentPayload map[string]any
	var deploymentRaw []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		if got := r.Header.Get("X-API-Key"); got != "account-token" {
			t.Errorf("%s X-API-Key = %q, want account-token", r.URL.Path, got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/apps":
			_, _ = w.Write([]byte(`{"ok":true,"apps":[]}`))
		case "POST /api/v1/apps":
			if got := r.Header.Get("Idempotency-Key"); got != "create-key" {
				t.Errorf("create Idempotency-Key = %q", got)
			}
			if err := json.NewDecoder(r.Body).Decode(&createPayload); err != nil {
				t.Errorf("decode create payload: %v", err)
			}
			_, _ = w.Write([]byte(`{"ok":true,"app":{"id":"app_123","visibility":"private"}}`))
		case "GET /api/v1/apps/app_123":
			_, _ = w.Write([]byte(`{"ok":true,"app":{"id":"app_123"}}`))
		case "DELETE /api/v1/apps/app_123":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"ok":false,"error":"app not found"}`))
		case "GET /api/v1/apps/app_123/deployments":
			_, _ = w.Write([]byte(`{"ok":true,"deployments":[]}`))
		case "POST /api/v1/apps/app_123/deployments":
			if got := r.Header.Get("Idempotency-Key"); got != "deployment-key" {
				t.Errorf("deployment Idempotency-Key = %q", got)
			}
			var err error
			deploymentRaw, err = io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read deployment payload: %v", err)
			} else if err := json.Unmarshal(deploymentRaw, &deploymentPayload); err != nil {
				t.Errorf("decode deployment payload: %v", err)
			}
			_, _ = w.Write([]byte(`{"ok":true,"deployment":{"id":"dep_123","status":"private"}}`))
		case "POST /api/v1/apps/app_123/deployments/dep_123/activate":
			_, _ = w.Write([]byte(`{"ok":true,"publicUrl":"https://apps.example/app_123"}`))
		case "POST /api/v1/apps/app_123/unpublish":
			_, _ = w.Write([]byte(`{"ok":true,"published":false}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := NewAppsClient(AppsClientOptions{
		BaseURL:    server.URL + "/api/",
		AccountKey: "account-token",
		HTTPClient: server.Client(),
	})
	ctx := context.Background()

	if result, err := client.ListApps(ctx); err != nil || !AppsResultOK(result) {
		t.Fatalf("ListApps result=%#v err=%v", result, err)
	}
	createResult, err := client.CreateApp(ctx, AppCreateRequest{
		Name:        "Customer CRM",
		Description: "Shared contact workspace",
		ProjectID:   "project_123",
		Metadata: map[string]any{
			"source": "clanker-cli",
		},
		IdempotencyKey: "create-key",
	})
	if err != nil || !AppsResultOK(createResult) {
		t.Fatalf("CreateApp result=%#v err=%v", createResult, err)
	}
	if createPayload["name"] != "Customer CRM" || createPayload["description"] != "Shared contact workspace" || createPayload["projectId"] != "project_123" {
		t.Fatalf("create payload = %#v", createPayload)
	}
	for _, forbidden := range []string{"html", "files", "entrypoint", "spa", "activate"} {
		if _, ok := createPayload[forbidden]; ok {
			t.Fatalf("metadata-only create payload included %q: %#v", forbidden, createPayload)
		}
	}

	if result, err := client.GetApp(ctx, "app_123"); err != nil || !AppsResultOK(result) {
		t.Fatalf("GetApp result=%#v err=%v", result, err)
	}
	if result, err := client.ListDeployments(ctx, "app_123"); err != nil || !AppsResultOK(result) {
		t.Fatalf("ListDeployments result=%#v err=%v", result, err)
	}
	appSpec := AppSpec{
		SchemaVersion: 1,
		Title:         "Customer CRM",
		Description:   "<public> & shared contact workspace",
		Blocks: []AppBlock{
			{
				Type:    "table",
				Title:   "Contacts",
				Columns: []string{"Name", "Company"},
				Rows:    [][]string{{"<Ada>", "Analytical & Engines"}},
			},
		},
	}
	deploymentResult, err := client.CreateDeployment(ctx, "app_123", AppDeploymentCreateRequest{
		AppSpec:        &appSpec,
		IdempotencyKey: "deployment-key",
	})
	if err != nil || !AppsResultOK(deploymentResult) {
		t.Fatalf("CreateDeployment result=%#v err=%v", deploymentResult, err)
	}
	if len(deploymentPayload) != 1 {
		t.Fatalf("deployment payload = %#v", deploymentPayload)
	}
	appSpecPayload, ok := deploymentPayload["appSpec"].(map[string]any)
	if !ok {
		t.Fatalf("deployment appSpec payload = %#v", deploymentPayload["appSpec"])
	}
	if appSpecPayload["theme"] != "light" {
		t.Fatalf("deployment appSpec theme = %#v, want default light", appSpecPayload["theme"])
	}
	if strings.Contains(string(deploymentRaw), `\u003c`) ||
		!strings.Contains(string(deploymentRaw), "<public>") ||
		!strings.Contains(string(deploymentRaw), "<Ada>") {
		t.Fatalf("deployment display text was inflated by HTML escaping: %s", deploymentRaw)
	}
	for _, forbidden := range []string{
		"html", "files", "entrypoint", "spa", "networkPolicy", "exposure",
		"dataSummary", "name", "activate",
	} {
		if _, ok := deploymentPayload[forbidden]; ok {
			t.Fatalf("declarative deployment payload included %q: %#v", forbidden, deploymentPayload)
		}
	}

	if result, err := client.ActivateDeployment(ctx, "app_123", "dep_123"); err != nil || !AppsResultOK(result) {
		t.Fatalf("ActivateDeployment result=%#v err=%v", result, err)
	}
	if result, err := client.UnpublishApp(ctx, "app_123"); err != nil || !AppsResultOK(result) {
		t.Fatalf("UnpublishApp result=%#v err=%v", result, err)
	}
	if result, err := client.DeleteApp(ctx, "app_123"); err != nil || !AppsResultOK(result) {
		t.Fatalf("DeleteApp result=%#v err=%v", result, err)
	}

	want := []string{
		"GET /api/v1/apps",
		"POST /api/v1/apps",
		"GET /api/v1/apps/app_123",
		"GET /api/v1/apps/app_123/deployments",
		"POST /api/v1/apps/app_123/deployments",
		"POST /api/v1/apps/app_123/deployments/dep_123/activate",
		"POST /api/v1/apps/app_123/unpublish",
		"DELETE /api/v1/apps/app_123",
	}
	if len(requests) != len(want) {
		t.Fatalf("requests = %#v, want %#v", requests, want)
	}
	for index := range want {
		if requests[index] != want[index] {
			t.Fatalf("request[%d] = %q, want %q", index, requests[index], want[index])
		}
	}
}

func TestAppsClientBaseURLNormalizationKeepsStableAPIBase(t *testing.T) {
	for _, name := range []string{
		"CLANKER_CLOUD_APPS_API_BASE_URL",
		"CLANKER_CLOUD_SANDBOX_API_BASE_URL",
		"CLANKER_SANDBOX_API_BASE_URL",
	} {
		t.Setenv(name, "")
	}

	tests := []struct {
		name    string
		baseURL string
		want    string
		wantErr bool
	}{
		{
			name: "default production base",
			want: DefaultCloudAPIBaseURL,
		},
		{
			name:    "production trailing slash",
			baseURL: "https://clankercloud.ai/api/",
			want:    DefaultCloudAPIBaseURL,
		},
		{
			name:    "host gets stable api prefix",
			baseURL: "https://clankercloud.ai",
			want:    DefaultCloudAPIBaseURL,
		},
		{
			name:    "version is not part of base",
			baseURL: "https://clankercloud.ai/api/v1",
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := NewAppsClient(AppsClientOptions{BaseURL: test.baseURL})
			got, err := client.BaseURL()
			if test.wantErr {
				if err == nil {
					t.Fatalf("BaseURL() = %q, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("BaseURL(): %v", err)
			}
			if got != test.want {
				t.Fatalf("BaseURL() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestAppsClientSendsExactFeatureCompleteHTMLAndPreservesRuntimeMetadata(t *testing.T) {
	html := `<!doctype html>
<script>
localStorage.setItem("crm", "ready")
fetch("https://api.example.test/contacts")
window.open("https://example.test")
</script>
<form action="https://example.test/import"><input type="file"><button>Import</button></form>
<a download href="data:text/plain,hello">Download</a>`
	var payload map[string]any
	var rawPayload []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		rawPayload, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read HTML deployment payload: %v", err)
		} else if err := json.Unmarshal(rawPayload, &payload); err != nil {
			t.Errorf("decode HTML deployment payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{
			"ok":true,
			"deployment":{
				"id":"dep_html",
				"status":"ready",
				"runtime":"clanker-html-v1",
				"appSpecVersion":0,
				"networkPolicy":"browser",
				"fileCount":1,
				"totalBytes":303
			}
		}`))
	}))
	defer server.Close()

	client := NewAppsClient(AppsClientOptions{
		BaseURL:    server.URL + "/api",
		AccountKey: "account-token",
		HTTPClient: server.Client(),
	})
	result, err := client.CreateDeployment(context.Background(), "app_123", AppDeploymentCreateRequest{
		HTML:           &html,
		IdempotencyKey: "html-deployment-key",
	})
	if err != nil || !AppsResultOK(result) {
		t.Fatalf("CreateDeployment result=%#v err=%v", result, err)
	}
	if len(payload) != 1 || payload["html"] != html {
		t.Fatalf("HTML deployment payload = %#v, want exact html only", payload)
	}
	if strings.Contains(string(rawPayload), `\u003c`) ||
		!strings.Contains(string(rawPayload), "<script>") ||
		!strings.Contains(string(rawPayload), "<form") {
		t.Fatalf("HTML transport changed supplied browser code: %s", rawPayload)
	}

	body, ok := result.Body.(map[string]any)
	if !ok {
		t.Fatalf("result body = %#v", result.Body)
	}
	deployment, ok := body["deployment"].(map[string]any)
	if !ok {
		t.Fatalf("deployment body = %#v", body["deployment"])
	}
	if deployment["runtime"] != "clanker-html-v1" ||
		deployment["networkPolicy"] != "browser" ||
		deployment["fileCount"] != float64(1) ||
		deployment["totalBytes"] != float64(303) {
		t.Fatalf("HTML runtime metadata = %#v", deployment)
	}
}

func TestAppsClientPreservesWhitespaceOnlyHTML(t *testing.T) {
	html := " \n\t "
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode whitespace HTML payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true,"deployment":{"id":"dep_whitespace","status":"ready"}}`))
	}))
	defer server.Close()

	client := NewAppsClient(AppsClientOptions{
		BaseURL:    server.URL + "/api",
		AccountKey: "account-token",
		HTTPClient: server.Client(),
	})
	result, err := client.CreateDeployment(context.Background(), "app_123", AppDeploymentCreateRequest{
		HTML:           &html,
		IdempotencyKey: "whitespace-html-key",
	})
	if err != nil || !AppsResultOK(result) {
		t.Fatalf("CreateDeployment result=%#v err=%v", result, err)
	}
	if len(payload) != 1 || payload["html"] != html {
		t.Fatalf("whitespace HTML payload = %#v, want exact bytes", payload)
	}
}

func TestAppsClientCanonicalizesOptInAgenticHTMLAndPreservesExposure(t *testing.T) {
	html := `<!doctype html><script>fetch("/a/demo/__clanker/llm")</script>`
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode agentic HTML payload: %v", err)
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

	client := NewAppsClient(AppsClientOptions{
		BaseURL:    server.URL + "/api",
		AccountKey: "account-token",
		HTTPClient: server.Client(),
	})
	result, err := client.CreateDeployment(context.Background(), "app_123", AppDeploymentCreateRequest{
		HTML: &html,
		Agentic: &AppAgenticConfig{
			Providers:          []string{" KIMI ", "gemini"},
			DailyRequestLimit:  DefaultAppAgenticDailyRequests,
			MaxInputCharacters: DefaultAppAgenticInputCharacters,
			MaxOutputTokens:    DefaultAppAgenticOutputTokens,
		},
		IdempotencyKey: "agentic-html-key",
	})
	if err != nil || !AppsResultOK(result) {
		t.Fatalf("CreateDeployment result=%#v err=%v", result, err)
	}
	if len(payload) != 2 || payload["html"] != html {
		t.Fatalf("agentic HTML payload = %#v", payload)
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

	body := result.Body.(map[string]any)
	deployment := body["deployment"].(map[string]any)
	exposure := deployment["exposure"].(map[string]any)
	responseAgentic := exposure["agentic"].(map[string]any)
	if responseAgentic["enabled"] != true ||
		responseAgentic["endpoint"] != "/a/{slug}/__clanker/llm" {
		t.Fatalf("agentic response exposure = %#v", responseAgentic)
	}
}

func TestAppsClientRequiresNamesAndIDs(t *testing.T) {
	client := NewAppsClient(AppsClientOptions{BaseURL: "https://clankercloud.ai/api", AccountKey: "account-token"})
	if _, err := client.CreateApp(context.Background(), AppCreateRequest{}); err == nil {
		t.Fatal("CreateApp without a name succeeded")
	}
	if _, err := client.GetApp(context.Background(), " "); err == nil {
		t.Fatal("GetApp without an id succeeded")
	}
	if _, err := client.ActivateDeployment(context.Background(), "app_123", " "); err == nil {
		t.Fatal("ActivateDeployment without a deployment id succeeded")
	}
	if _, err := client.CreateApp(context.Background(), AppCreateRequest{
		Name:           "Too many retries",
		IdempotencyKey: strings.Repeat("x", 129),
	}); err == nil {
		t.Fatal("CreateApp accepted an oversized idempotency key")
	}
	for _, key := range []string{"", "1234567", "invalid key", "invalid/key"} {
		if _, err := client.CreateApp(context.Background(), AppCreateRequest{
			Name:           "Invalid retry key",
			IdempotencyKey: key,
		}); err == nil {
			t.Fatalf("CreateApp accepted invalid idempotency key %q", key)
		}
	}
	emptyBlocks := AppSpec{SchemaVersion: 1, Title: "No blocks"}
	if _, err := client.CreateDeployment(context.Background(), "app_123", AppDeploymentCreateRequest{
		AppSpec:        &emptyBlocks,
		IdempotencyKey: "deploy-valid-key",
	}); err == nil {
		t.Fatal("CreateDeployment accepted an app spec without blocks")
	}
}

func TestAppsClientRejectsInvalidDeclarativeDeploymentsBeforeNetwork(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	client := NewAppsClient(AppsClientOptions{
		BaseURL:    server.URL + "/api",
		AccountKey: "account-token",
		HTTPClient: server.Client(),
	})
	ctx := context.Background()

	cases := []AppSpec{
		{},
		{
			SchemaVersion: 1,
			Title:         "Unsupported execution",
			Blocks:        []AppBlock{{Type: "html", Body: "<script>alert(1)</script>"}},
		},
		{
			SchemaVersion: 1,
			Title:         "Broken table",
			Blocks: []AppBlock{{
				Type:    "table",
				Columns: []string{"Name"},
				Rows:    [][]string{{"Ada", "extra"}},
			}},
		},
		{
			SchemaVersion: 1,
			Title:         "Credential leak",
			Blocks: []AppBlock{{
				Type: "text",
				Body: "Authorization: Bearer abcdefghijklmnopqrstuvwxyz",
			}},
		},
	}
	for index, spec := range cases {
		spec := spec
		if _, err := client.CreateDeployment(ctx, "app_123", AppDeploymentCreateRequest{
			AppSpec:        &spec,
			IdempotencyKey: "declarative-test-key",
		}); err == nil {
			t.Fatalf("invalid app spec case %d succeeded", index)
		}
	}
	if requests != 0 {
		t.Fatalf("invalid app specs made %d network requests", requests)
	}
}

func TestAppsClientRejectsInvalidRuntimeSelectionAndHTMLBeforeNetwork(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	client := NewAppsClient(AppsClientOptions{
		BaseURL:    server.URL + "/api",
		AccountKey: "account-token",
		HTTPClient: server.Client(),
	})
	ctx := context.Background()
	validSpec := AppSpec{
		SchemaVersion: 1,
		Title:         "Ready",
		Blocks:        []AppBlock{{Type: "text", Body: "Ready"}},
	}
	validHTML := "<!doctype html><script>document.body.textContent = 'ready'</script>"
	emptyHTML := ""
	invalidHTML := string([]byte{0xff, 0xfe})
	oversizedHTML := strings.Repeat("x", MaxAppHTMLBytes+1)
	validAgentic := AppAgenticConfig{
		Providers:          []string{"gemini"},
		DailyRequestLimit:  1,
		MaxInputCharacters: 1,
		MaxOutputTokens:    1,
	}
	invalidAgentic := AppAgenticConfig{
		Providers:          []string{"openai"},
		DailyRequestLimit:  1,
		MaxInputCharacters: 1,
		MaxOutputTokens:    1,
	}
	cases := []AppDeploymentCreateRequest{
		{IdempotencyKey: "runtime-none-key"},
		{AppSpec: &validSpec, HTML: &validHTML, IdempotencyKey: "runtime-both-key"},
		{HTML: &emptyHTML, IdempotencyKey: "html-empty-key"},
		{HTML: &invalidHTML, IdempotencyKey: "html-utf8-key"},
		{HTML: &oversizedHTML, IdempotencyKey: "html-size-key"},
		{AppSpec: &validSpec, Agentic: &validAgentic, IdempotencyKey: "spec-agentic-key"},
		{HTML: &validHTML, Agentic: &invalidAgentic, IdempotencyKey: "invalid-agentic-key"},
	}
	for index, payload := range cases {
		if _, err := client.CreateDeployment(ctx, "app_123", payload); err == nil {
			t.Fatalf("invalid runtime case %d succeeded", index)
		}
	}
	if requests != 0 {
		t.Fatalf("invalid runtime inputs made %d network requests", requests)
	}
}

func TestValidateAppAgenticConfigCanonicalizesAndBounds(t *testing.T) {
	config, err := ValidateAppAgenticConfig(AppAgenticConfig{
		Providers:          []string{" KIMI ", "Gemini"},
		DailyRequestLimit:  25,
		MaxInputCharacters: 16000,
		MaxOutputTokens:    1024,
	})
	if err != nil {
		t.Fatalf("validate agentic config: %v", err)
	}
	if strings.Join(config.Providers, ",") != "gemini,kimi" ||
		config.DailyRequestLimit != 25 ||
		config.MaxInputCharacters != 16000 ||
		config.MaxOutputTokens != 1024 {
		t.Fatalf("canonical agentic config = %#v", config)
	}

	cases := []AppAgenticConfig{
		{},
		{Providers: []string{"openai"}, DailyRequestLimit: 1, MaxInputCharacters: 1, MaxOutputTokens: 1},
		{Providers: []string{"gemini", "GEMINI"}, DailyRequestLimit: 1, MaxInputCharacters: 1, MaxOutputTokens: 1},
		{Providers: []string{"gemini"}, DailyRequestLimit: 0, MaxInputCharacters: 1, MaxOutputTokens: 1},
		{Providers: []string{"gemini"}, DailyRequestLimit: 26, MaxInputCharacters: 1, MaxOutputTokens: 1},
		{Providers: []string{"gemini"}, DailyRequestLimit: 1, MaxInputCharacters: 0, MaxOutputTokens: 1},
		{Providers: []string{"gemini"}, DailyRequestLimit: 1, MaxInputCharacters: 16001, MaxOutputTokens: 1},
		{Providers: []string{"gemini"}, DailyRequestLimit: 1, MaxInputCharacters: 1, MaxOutputTokens: 0},
		{Providers: []string{"gemini"}, DailyRequestLimit: 1, MaxInputCharacters: 1, MaxOutputTokens: 1025},
	}
	for index, input := range cases {
		if _, err := ValidateAppAgenticConfig(input); err == nil {
			t.Fatalf("invalid agentic config %d was accepted: %#v", index, input)
		}
	}
}

func TestAppsDeleteRejectsUnstructuredRouteNotFound(t *testing.T) {
	result := &AppsAPIResult{
		Method: http.MethodDelete,
		Status: http.StatusNotFound,
		Body:   map[string]any{"error": "app route not found"},
	}
	if AppsResultOK(result) {
		t.Fatal("unstructured route 404 was treated as successful deletion")
	}
	if err := AppsResultStatusError(result); err == nil {
		t.Fatal("unstructured route 404 did not return an error")
	}
}
