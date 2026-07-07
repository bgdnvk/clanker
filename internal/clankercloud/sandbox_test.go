package clankercloud

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
			_, _ = w.Write([]byte(`{"box":{"id":"box_123","expiresAt":"2026-07-07T10:00:00Z"},"sandboxToken":"sandbox-token"}`))
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
	if got := ExtractSandboxToken(created.Body); got != "sandbox-token" {
		t.Fatalf("ExtractSandboxToken = %q, want sandbox-token", got)
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
