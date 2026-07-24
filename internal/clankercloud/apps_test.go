package clankercloud

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAppsClientLifecycleRoutesAndPrivatePayloads(t *testing.T) {
	var requests []string
	var createPayload map[string]any
	var deploymentPayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		if got := r.Header.Get("X-API-Key"); got != "account-token" {
			t.Errorf("%s X-API-Key = %q, want account-token", r.URL.Path, got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /api/apps":
			_, _ = w.Write([]byte(`{"ok":true,"apps":[]}`))
		case "POST /api/apps":
			if got := r.Header.Get("Idempotency-Key"); got != "create-key" {
				t.Errorf("create Idempotency-Key = %q", got)
			}
			if err := json.NewDecoder(r.Body).Decode(&createPayload); err != nil {
				t.Errorf("decode create payload: %v", err)
			}
			_, _ = w.Write([]byte(`{"ok":true,"app":{"id":"app_123","visibility":"private"}}`))
		case "GET /api/apps/app_123":
			_, _ = w.Write([]byte(`{"ok":true,"app":{"id":"app_123"}}`))
		case "DELETE /api/apps/app_123":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"ok":false,"error":"app not found"}`))
		case "GET /api/apps/app_123/deployments":
			_, _ = w.Write([]byte(`{"ok":true,"deployments":[]}`))
		case "POST /api/apps/app_123/deployments":
			if got := r.Header.Get("Idempotency-Key"); got != "deployment-key" {
				t.Errorf("deployment Idempotency-Key = %q", got)
			}
			if err := json.NewDecoder(r.Body).Decode(&deploymentPayload); err != nil {
				t.Errorf("decode deployment payload: %v", err)
			}
			_, _ = w.Write([]byte(`{"ok":true,"deployment":{"id":"dep_123","status":"private"}}`))
		case "POST /api/apps/app_123/deployments/dep_123/activate":
			_, _ = w.Write([]byte(`{"ok":true,"publicUrl":"https://apps.example/app_123"}`))
		case "POST /api/apps/app_123/unpublish":
			_, _ = w.Write([]byte(`{"ok":true,"published":false}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := NewAppsClient(AppsClientOptions{
		BaseURL:    server.URL + "/api",
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
	indexHTML := "<h1>Revision 2</h1>"
	emptyCSS := ""
	deploymentResult, err := client.CreateDeployment(ctx, "app_123", AppDeploymentCreateRequest{
		AppDeploymentInput: AppDeploymentInput{
			Files: []AppFile{
				{Path: "index.html", Content: &indexHTML},
				{Path: "assets/empty.css", Content: &emptyCSS},
			},
			Entrypoint: "index.html",
			DataSummary: map[string]any{
				"records": 12,
			},
			NetworkPolicy: "none",
			Exposure: map[string]any{
				"publicFields": []any{"name", "company"},
			},
		},
		IdempotencyKey: "deployment-key",
	})
	if err != nil || !AppsResultOK(deploymentResult) {
		t.Fatalf("CreateDeployment result=%#v err=%v", deploymentResult, err)
	}
	if deploymentPayload["networkPolicy"] != "none" {
		t.Fatalf("deployment payload = %#v", deploymentPayload)
	}
	filesPayload, ok := deploymentPayload["files"].([]any)
	if !ok || len(filesPayload) != 2 {
		t.Fatalf("deployment files payload = %#v", deploymentPayload["files"])
	}
	emptyFile, ok := filesPayload[1].(map[string]any)
	if !ok || emptyFile["path"] != "assets/empty.css" {
		t.Fatalf("empty file payload = %#v", filesPayload[1])
	}
	if content, present := emptyFile["content"]; !present || content != "" {
		t.Fatalf("empty file content was omitted: %#v", emptyFile)
	}
	for _, forbidden := range []string{"name", "activate"} {
		if _, ok := deploymentPayload[forbidden]; ok {
			t.Fatalf("private deployment payload included %q: %#v", forbidden, deploymentPayload)
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
		"GET /api/apps",
		"POST /api/apps",
		"GET /api/apps/app_123",
		"GET /api/apps/app_123/deployments",
		"POST /api/apps/app_123/deployments",
		"POST /api/apps/app_123/deployments/dep_123/activate",
		"POST /api/apps/app_123/unpublish",
		"DELETE /api/apps/app_123",
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
	if _, err := client.CreateDeployment(context.Background(), "app_123", AppDeploymentCreateRequest{
		AppDeploymentInput: AppDeploymentInput{HTML: "<h1>unsafe</h1>", NetworkPolicy: "connect"},
		IdempotencyKey:     "deploy-valid-key",
	}); err == nil {
		t.Fatal("CreateDeployment accepted a network-enabled static app")
	}
}

func TestAppsClientRequiresExactlyOneArtifactSourceAndFileEncoding(t *testing.T) {
	content := "<h1>hello</h1>"
	base64Content := "PGgxPmhlbGxvPC9oMT4="
	client := NewAppsClient(AppsClientOptions{
		BaseURL:    "https://clankercloud.ai/api",
		AccountKey: "account-token",
	})
	ctx := context.Background()

	cases := []AppDeploymentCreateRequest{
		{IdempotencyKey: "artifact-none-key"},
		{
			AppDeploymentInput: AppDeploymentInput{
				HTML:  content,
				Files: []AppFile{{Path: "index.html", Content: &content}},
			},
			IdempotencyKey: "artifact-both-key",
		},
		{
			AppDeploymentInput: AppDeploymentInput{
				Files: []AppFile{{Path: "index.html"}},
			},
			IdempotencyKey: "encoding-none-key",
		},
		{
			AppDeploymentInput: AppDeploymentInput{
				Files: []AppFile{{Path: "index.html", Content: &content, Base64: &base64Content}},
			},
			IdempotencyKey: "encoding-both-key",
		},
	}
	for index, payload := range cases {
		if _, err := client.CreateDeployment(ctx, "app_123", payload); err == nil {
			t.Fatalf("invalid artifact case %d succeeded", index)
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
