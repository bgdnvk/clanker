package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func decodeCodexRequest(t *testing.T, r *http.Request) CodexRequest {
	t.Helper()

	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("decode raw request body: %v", err)
	}
	rawStore, ok := raw["store"]
	if !ok {
		t.Fatal("expected codex request to include store=false")
	}
	store, ok := rawStore.(bool)
	if !ok || store {
		t.Fatalf("expected store=false, got %#v", rawStore)
	}
	rawStream, ok := raw["stream"]
	if !ok {
		t.Fatal("expected codex request to include stream=true")
	}
	stream, ok := rawStream.(bool)
	if !ok || !stream {
		t.Fatalf("expected stream=true, got %#v", rawStream)
	}

	var codexReq CodexRequest
	if err := json.Unmarshal(body, &codexReq); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	return codexReq
}

func TestIsOpenAIOAuthActive_EnvVar(t *testing.T) {
	// Point HOME to a temp dir so no real token file is found.
	t.Setenv("HOME", t.TempDir())

	t.Run("with env var set", func(t *testing.T) {
		t.Setenv("OPENAI_OAUTH_TOKEN", "some_token")
		if !IsOpenAIOAuthActive() {
			t.Error("expected IsOpenAIOAuthActive to return true when env var is set")
		}
	})

	t.Run("without env var", func(t *testing.T) {
		t.Setenv("OPENAI_OAUTH_TOKEN", "")
		if IsOpenAIOAuthActive() {
			t.Error("expected IsOpenAIOAuthActive to return false when no env var and no token file")
		}
	})
}

func TestAskCodexWithToken(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify the authorization header
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test_token" {
			t.Errorf("unexpected Authorization header: %q", auth)
		}
		codexReq := decodeCodexRequest(t, r)
		if strings.TrimSpace(codexReq.Instructions) == "" {
			t.Error("expected codex instructions to be populated")
		}
		if codexReq.Instructions != defaultCodexInstructions {
			t.Errorf("unexpected instructions: %q", codexReq.Instructions)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello \"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"world\"}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer mockServer.Close()

	origURL := codexResponsesURL
	codexResponsesURL = mockServer.URL
	t.Cleanup(func() { codexResponsesURL = origURL })

	result, err := askCodexWithToken(context.Background(), "test_token", "test-model", "say hello")
	if err != nil {
		t.Fatalf("askCodexWithToken failed: %v", err)
	}

	if result != "hello world" {
		t.Errorf("expected %q, got %q", "hello world", result)
	}
}

func TestAskCodexStreamSendsInstructions(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "Bearer stream_token" {
			t.Errorf("unexpected Authorization header: %q", auth)
		}
		codexReq := decodeCodexRequest(t, r)
		if strings.TrimSpace(codexReq.Instructions) == "" {
			t.Error("expected codex stream instructions to be populated")
		}
		if !codexReq.Stream {
			t.Error("expected stream request")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer mockServer.Close()

	origURL := codexResponsesURL
	codexResponsesURL = mockServer.URL
	t.Cleanup(func() { codexResponsesURL = origURL })

	textCh, errCh := AskCodexStream(context.Background(), "stream_token", "test-model", "say hello")
	var result strings.Builder
	for part := range textCh {
		result.WriteString(part)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("AskCodexStream failed: %v", err)
	}
	if result.String() != "hello" {
		t.Fatalf("expected streamed text %q, got %q", "hello", result.String())
	}
}

func TestAskCodexWithHistorySendsStoreFalseAndStreams(t *testing.T) {
	t.Setenv("OPENAI_OAUTH_TOKEN", "history_token")

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "Bearer history_token" {
			t.Errorf("unexpected Authorization header: %q", auth)
		}
		codexReq := decodeCodexRequest(t, r)
		if codexReq.Instructions != "Be precise." {
			t.Errorf("unexpected instructions: %q", codexReq.Instructions)
		}
		if len(codexReq.Input) != 1 || codexReq.Input[0].Content != "say hello" {
			t.Fatalf("unexpected input: %#v", codexReq.Input)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"history \"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer mockServer.Close()

	origURL := codexResponsesURL
	codexResponsesURL = mockServer.URL
	t.Cleanup(func() { codexResponsesURL = origURL })

	client := &Client{aiProfile: "openai"}
	result, err := client.askCodexWithHistory(context.Background(), &ConversationContext{
		SystemPrompt: "  Be precise.  ",
		Messages: []Message{
			{Role: "user", Content: "say hello"},
		},
	})
	if err != nil {
		t.Fatalf("askCodexWithHistory failed: %v", err)
	}
	if result != "history hello" {
		t.Errorf("expected %q, got %q", "history hello", result)
	}
}

func TestAskClankerCloudMessagesUsesAppTokenHeaders(t *testing.T) {
	t.Setenv("CLANKER_CLOUD_CLIENT", "desktop-app")

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/llm/chat/completions" {
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "" {
			t.Errorf("Clanker Cloud should not send app token through Authorization, got %q", auth)
		}
		if apiKey := r.Header.Get("X-API-Key"); apiKey != "cloud-token-xyz" {
			t.Errorf("unexpected X-API-Key header: %q", apiKey)
		}
		if client := r.Header.Get("X-Clanker-Cloud-Client"); client != "desktop-app" {
			t.Errorf("unexpected X-Clanker-Cloud-Client header: %q", client)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"cloud hello"}}]}`))
	}))
	defer mockServer.Close()

	client := &Client{
		provider: "clanker-cloud",
		apiKey:   "cloud-token-xyz",
		baseURL:  mockServer.URL + "/v1/llm",
	}
	result, err := client.askClankerCloudMessages(context.Background(), []Message{{Role: "user", Content: "say hello"}})
	if err != nil {
		t.Fatalf("askClankerCloudMessages failed: %v", err)
	}
	if result != "cloud hello" {
		t.Fatalf("expected cloud hello, got %q", result)
	}
}

func TestCodexInstructionsUsesSystemPrompt(t *testing.T) {
	got := codexInstructions("  Be precise.  ")
	if got != "Be precise." {
		t.Fatalf("expected trimmed system prompt, got %q", got)
	}
}

func TestShouldUseOpenAIOAuth_EnvTokenBeatsAPIKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPENAI_OAUTH_TOKEN", "oauth-token")

	if !shouldUseOpenAIOAuth("sk-stale-quota-key", defaultOpenAIBaseURL) {
		t.Fatal("expected explicit OAuth token to take precedence over OpenAI API key")
	}
}

func TestShouldUseOpenAIOAuth_EnvTokenBeatsLocalInference(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPENAI_OAUTH_TOKEN", "oauth-token")

	if !shouldUseOpenAIOAuth("sk-stale-quota-key", "http://127.0.0.1:8085/v1") {
		t.Fatal("expected explicit OAuth token to take precedence over local model inference")
	}
}

func TestShouldUseOpenAIOAuth_SavedTokenDoesNotOverrideLocalInference(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPENAI_OAUTH_TOKEN", "")
	if err := SaveOAuthTokens(&OAuthTokens{
		AccessToken:  "saved-token",
		RefreshToken: "saved-refresh",
		ExpiresAt:    time.Now().Add(time.Hour).Unix(),
	}); err != nil {
		t.Fatalf("SaveOAuthTokens failed: %v", err)
	}

	if shouldUseOpenAIOAuth("", "http://127.0.0.1:8080/v1") {
		t.Fatal("expected local model inference endpoint to remain active without explicit OAuth token")
	}
}
