package clankercloud

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestNormalizeSandboxAPIBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "default host with api path", raw: "https://clankercloud.ai/api/", want: "https://clankercloud.ai/api"},
		{name: "host without path appends api", raw: "https://clankercloud.ai", want: "https://clankercloud.ai/api"},
		{name: "nested api path", raw: "https://gateway.example.com/v1/api", want: "https://gateway.example.com/v1/api"},
		{name: "loopback http allowed", raw: "http://127.0.0.1:8080/api", want: "http://127.0.0.1:8080/api"},
		{name: "remote http rejected", raw: "http://example.com/api", wantErr: true},
		{name: "query rejected", raw: "https://clankercloud.ai/api?x=1", wantErr: true},
		{name: "userinfo rejected", raw: "https://token@clankercloud.ai/api", wantErr: true},
		{name: "wrong path rejected", raw: "https://clankercloud.ai/v1", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeSandboxAPIBaseURL(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("NormalizeSandboxAPIBaseURL(%q) succeeded, want error", tt.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeSandboxAPIBaseURL(%q): %v", tt.raw, err)
			}
			if got != tt.want {
				t.Fatalf("NormalizeSandboxAPIBaseURL(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestSandboxClientCreateAndCommand(t *testing.T) {
	var createToken string
	var commandToken string
	var createPayload SandboxCreateRequest
	var commandPayload SandboxCommandRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/sandboxes":
			if r.Method != http.MethodPost {
				t.Fatalf("create method = %s, want POST", r.Method)
			}
			createToken = r.Header.Get("X-API-Key")
			if err := json.NewDecoder(r.Body).Decode(&createPayload); err != nil {
				t.Fatalf("decode create payload: %v", err)
			}
			w.Header().Set("Set-Cookie", "session=secret")
			w.Header().Set("X-Request-ID", "request-123")
			w.Header().Set("X-Internal-Token", "internal-secret")
			_, _ = w.Write([]byte(`{"box":{"id":"box_123","expiresAt":"2026-07-07T10:00:00Z"},"sandboxToken":"sandbox-token","nested":{"apiKey":"nested-secret","githubToken":"provider-secret"}}`))
		case "/api/sandboxes/box_123/commands":
			if r.Method != http.MethodPost {
				t.Fatalf("command method = %s, want POST", r.Method)
			}
			commandToken = r.Header.Get("X-API-Key")
			if err := json.NewDecoder(r.Body).Decode(&commandPayload); err != nil {
				t.Fatalf("decode command payload: %v", err)
			}
			_, _ = w.Write([]byte(`{"ok":true,"output":"done"}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewSandboxClient(SandboxClientOptions{
		BaseURL:      server.URL + "/api",
		AccountKey:   "account-token",
		SandboxToken: "sandbox-token",
		HTTPClient:   server.Client(),
	})

	created, err := client.Create(context.Background(), SandboxCreateRequest{Name: "repo-check", Agent: "codex", Region: "earth"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !SandboxResultOK(created) {
		t.Fatalf("Create status = %d, want 2xx", created.Status)
	}
	if createToken != "account-token" {
		t.Fatalf("create X-API-Key = %q, want account-token", createToken)
	}
	if createPayload.Name != "repo-check" || createPayload.Agent != "codex" || createPayload.Region != "earth" {
		t.Fatalf("create payload = %+v", createPayload)
	}
	if got := ExtractSandboxID(created.Body); got != "box_123" {
		t.Fatalf("ExtractSandboxID = %q, want box_123", got)
	}
	if got := ExtractSandboxToken(created.Body); got != "" {
		t.Fatalf("redacted create body exposed sandbox token %q", got)
	}
	if got := client.SandboxToken(); got != "sandbox-token" {
		t.Fatalf("client sandbox token = %q, want cached token", got)
	}
	body, ok := created.Body.(map[string]any)
	if !ok || body["sandboxToken"] != "[REDACTED]" {
		t.Fatalf("create body was not redacted: %#v", created.Body)
	}
	nested, ok := body["nested"].(map[string]any)
	if !ok || nested["apiKey"] != "[REDACTED]" {
		t.Fatalf("nested credential was not redacted: %#v", created.Body)
	}
	if nested["githubToken"] != "[REDACTED]" {
		t.Fatalf("provider token was not redacted: %#v", created.Body)
	}
	if created.Headers["X-Request-Id"] != "request-123" {
		t.Fatalf("safe request id header missing: %#v", created.Headers)
	}
	if _, ok := created.Headers["Set-Cookie"]; ok {
		t.Fatalf("unsafe Set-Cookie header exposed: %#v", created.Headers)
	}
	if _, ok := created.Headers["X-Internal-Token"]; ok {
		t.Fatalf("unsafe internal token header exposed: %#v", created.Headers)
	}

	result, err := client.Command(context.Background(), "box_123", SandboxCommandRequest{Command: "go test ./...", TimeoutSeconds: 600})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if !SandboxResultOK(result) {
		t.Fatalf("Command status = %d, want 2xx", result.Status)
	}
	if commandToken != "sandbox-token" {
		t.Fatalf("command X-API-Key = %q, want sandbox-token", commandToken)
	}
	if commandPayload.Command != "go test ./..." || commandPayload.TimeoutSeconds != 600 {
		t.Fatalf("command payload = %+v", commandPayload)
	}
}

func TestSandboxClientRejectsCrossOriginCredentialRedirect(t *testing.T) {
	var redirectedRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectedRequests.Add(1)
		if got := r.Header.Get("X-API-Key"); got != "" {
			t.Errorf("redirect target received X-API-Key %q", got)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/api/sandboxes", http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	client := NewSandboxClient(SandboxClientOptions{
		BaseURL:    source.URL + "/api",
		AccountKey: "account-token",
		HTTPClient: source.Client(),
	})
	_, err := client.List(context.Background())
	if err == nil || !strings.Contains(err.Error(), "cross-origin") {
		t.Fatalf("List error = %v, want cross-origin redirect refusal", err)
	}
	if got := redirectedRequests.Load(); got != 0 {
		t.Fatalf("redirect target requests = %d, want 0", got)
	}
}

func TestSandboxClientBoundsResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = fmt.Fprint(w, strings.Repeat("x", maxCloudAPIResponseBytes+1))
	}))
	defer server.Close()

	client := NewSandboxClient(SandboxClientOptions{
		BaseURL:    server.URL + "/api",
		AccountKey: "account-token",
		HTTPClient: server.Client(),
	})
	_, err := client.List(context.Background())
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("List error = %v, want bounded response error", err)
	}
}

func TestSandboxDelete404IsIdempotent(t *testing.T) {
	var deleteCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deleteCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"ok":false,"error":"sandbox not found"}`))
	}))
	defer server.Close()

	client := NewSandboxClient(SandboxClientOptions{
		BaseURL:      server.URL + "/api",
		SandboxToken: "sandbox-token",
		HTTPClient:   server.Client(),
	})
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := client.Dispose(cancelled, "box_missing")
	if err != nil {
		t.Fatalf("Dispose: %v", err)
	}
	if !SandboxResultOK(result) || result.Status != http.StatusNotFound {
		t.Fatalf("Dispose result = %#v, want idempotent 404", result)
	}
	if deleteCalls.Load() != 1 {
		t.Fatalf("delete calls = %d, want cleanup despite cancelled parent", deleteCalls.Load())
	}
}

func TestSandboxDeleteDoesNotAcceptArbitrary404(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"ok":false,"error":"sandbox action not found"}`))
	}))
	defer server.Close()

	client := NewSandboxClient(SandboxClientOptions{
		BaseURL:      server.URL + "/api",
		SandboxToken: "sandbox-token",
		HTTPClient:   server.Client(),
	})
	result, err := client.Dispose(context.Background(), "box_missing")
	if err == nil || !strings.Contains(err.Error(), "status 404") {
		t.Fatalf("Dispose error = %v, want unrecognized 404 failure", err)
	}
	if SandboxResultOK(result) {
		t.Fatalf("arbitrary 404 was treated as successful deletion: %#v", result)
	}
}

func TestSandboxDeleteFallsBackToAccountForManageableCleanup(t *testing.T) {
	var deleteCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deleteCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch r.Header.Get("X-API-Key") {
		case "stale-sandbox-token":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"ok":false,"error":"invalid sandbox token"}`))
		case "account-token":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected credential %q", r.Header.Get("X-API-Key"))
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	defer server.Close()

	client := NewSandboxClient(SandboxClientOptions{
		BaseURL:      server.URL + "/api",
		AccountKey:   "account-token",
		SandboxToken: "stale-sandbox-token",
		HTTPClient:   server.Client(),
	})
	result, err := client.Dispose(context.Background(), "box_owned")
	if err != nil || !SandboxResultOK(result) {
		t.Fatalf("Dispose result=%#v err=%v", result, err)
	}
	if deleteCalls.Load() != 2 {
		t.Fatalf("delete calls = %d, want sandbox-token attempt then account fallback", deleteCalls.Load())
	}
}

func TestSandboxClientRejectsEmptyResourceID(t *testing.T) {
	client := NewSandboxClient(SandboxClientOptions{BaseURL: "https://clankercloud.ai/api"})
	if _, err := client.Delete(context.Background(), " "); err == nil {
		t.Fatal("Delete without a sandbox id succeeded")
	}
	if _, err := client.Command(context.Background(), "", SandboxCommandRequest{Command: "true"}); err == nil {
		t.Fatal("Command without a sandbox id succeeded")
	}
}

func TestSandboxClientDoesNotAttachConfiguredCredentialToUntrustedExplicitBase(t *testing.T) {
	t.Setenv("CLANKER_CLOUD_API_KEY", "configured-secret")
	t.Setenv("CLANKER_SANDBOX_TOKEN", "configured-sandbox-secret")

	untrusted := NewSandboxClient(SandboxClientOptions{BaseURL: "https://example.com/api"})
	if untrusted.AccountKey() != "" || untrusted.SandboxToken() != "" {
		t.Fatalf("untrusted explicit base inherited configured credentials")
	}

	official := NewSandboxClient(SandboxClientOptions{BaseURL: DefaultSandboxAPIBaseURL})
	if official.AccountKey() != "configured-secret" || official.SandboxToken() != "configured-sandbox-secret" {
		t.Fatalf("official base did not inherit configured credentials")
	}

	explicit := NewSandboxClient(SandboxClientOptions{
		BaseURL:      "https://example.com/api",
		AccountKey:   "explicit-account",
		SandboxToken: "explicit-sandbox",
	})
	if explicit.AccountKey() != "explicit-account" || explicit.SandboxToken() != "explicit-sandbox" {
		t.Fatalf("explicit custom credentials were not preserved")
	}
}
