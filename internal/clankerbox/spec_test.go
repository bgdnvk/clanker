package clankerbox

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestNewManifestValidatesAgentAndRegion(t *testing.T) {
	manifest, err := NewManifest("Prod Agent", "claude", "us-east4", ManifestOptions{
		ProjectID:            "clanker-prod",
		Image:                "us-east4-docker.pkg.dev/clanker-prod/clanker/box:latest",
		ServiceAccountEmail:  "box@clanker-prod.iam.gserviceaccount.com",
		ArtifactRepository:   "clanker",
		StateBucket:          "clanker-box-state",
		RequireAuth:          true,
		WebSocketTimeoutMins: 45,
	})
	if err != nil {
		t.Fatalf("NewManifest returned error: %v", err)
	}
	if manifest.Agent.ID != "claude-code" {
		t.Fatalf("expected claude-code alias, got %q", manifest.Agent.ID)
	}
	if manifest.Region.ID != "us-east4" {
		t.Fatalf("expected region us-east4, got %q", manifest.Region.ID)
	}
	if !strings.HasPrefix(manifest.ServiceName, "clanker-box-claude-code-prod-agent-") {
		t.Fatalf("unexpected service name %q", manifest.ServiceName)
	}
	if manifest.Environment["CLANKER_BOX_REQUIRE_AUTH"] != "true" {
		t.Fatalf("expected auth env true, got %#v", manifest.Environment)
	}
	if manifest.Size.MaxInstances != 1 || manifest.Size.Concurrency != 1 {
		t.Fatalf("unexpected beta size: %#v", manifest.Size)
	}
	if !manifest.Security.PerBoxServiceAccount || !manifest.Security.NoCloudSQL || manifest.Security.AllowUnauthenticated {
		t.Fatalf("unexpected security defaults: %#v", manifest.Security)
	}
}

func TestNewManifestRejectsUnknownRegion(t *testing.T) {
	_, err := NewManifest("Prod Agent", "hermes", "moon-1", ManifestOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "unsupported Cloud Run region") {
		t.Fatalf("unexpected error: %v", err)
	}
}

type fakeRunner struct{}

func (fakeRunner) RunAgentMessage(ctx context.Context, cfg RuntimeConfig, req MessageRequest) (string, error) {
	return "reply:" + req.Message, nil
}

func TestServerMessageRequiresToken(t *testing.T) {
	server := NewServer(RuntimeConfig{Name: "test", Agent: "clanker-cli", Region: "us-central1", RequireAuth: true, APIToken: "secret"}, fakeRunner{})
	req := httptest.NewRequest(http.MethodPost, "/v1/box/messages", bytes.NewBufferString(`{"message":"hello"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestServerMessageRunsAgent(t *testing.T) {
	server := NewServer(RuntimeConfig{Name: "test", Agent: "clanker-cli", Region: "us-central1", RequireAuth: true, APIToken: "secret"}, fakeRunner{})
	req := httptest.NewRequest(http.MethodPost, "/v1/box/messages", bytes.NewBufferString(`{"sessionId":"s1","message":"hello"}`))
	req.Header.Set("X-API-Key", "secret")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp MessageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.OK || resp.Message != "reply:hello" || resp.SessionID != "s1" {
		t.Fatalf("unexpected response %#v", resp)
	}
}

func TestServerTerminalRunsCommand(t *testing.T) {
	server := httptest.NewServer(NewServer(RuntimeConfig{Name: "test", Agent: "clanker-cli", Region: "us-central1", RequireAuth: true, EnableTerminal: true, APIToken: "secret"}, fakeRunner{}).Handler())
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/box/terminal?token=secret"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial terminal: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteJSON(TerminalRequest{SessionID: "term-1", Command: "printf terminal-ok", TimeoutSeconds: 5}); err != nil {
		t.Fatalf("write terminal request: %v", err)
	}
	var resp TerminalResponse
	if err := conn.ReadJSON(&resp); err != nil {
		t.Fatalf("read terminal response: %v", err)
	}
	if !resp.OK || resp.SessionID != "term-1" || resp.ExitCode != 0 || resp.Output != "terminal-ok" {
		t.Fatalf("unexpected terminal response %#v", resp)
	}
}
