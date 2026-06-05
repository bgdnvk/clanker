package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

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
		var codexReq CodexRequest
		if err := json.NewDecoder(r.Body).Decode(&codexReq); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		if strings.TrimSpace(codexReq.Instructions) == "" {
			t.Error("expected codex instructions to be populated")
		}
		if codexReq.Instructions != defaultCodexInstructions {
			t.Errorf("unexpected instructions: %q", codexReq.Instructions)
		}

		resp := map[string]interface{}{
			"output": []map[string]interface{}{
				{
					"type": "message",
					"content": []map[string]interface{}{
						{
							"type": "output_text",
							"text": "hello world",
						},
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
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
		var codexReq CodexRequest
		if err := json.NewDecoder(r.Body).Decode(&codexReq); err != nil {
			t.Errorf("decode request body: %v", err)
		}
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
