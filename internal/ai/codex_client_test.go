package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
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
