package ai

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestExtractEmailFromIDToken(t *testing.T) {
	t.Run("valid JWT with email", func(t *testing.T) {
		payload := `{"email":"user@test.com"}`
		encoded := base64.RawURLEncoding.EncodeToString([]byte(payload))
		fakeJWT := fmt.Sprintf("header.%s.signature", encoded)

		email := ExtractEmailFromIDToken(fakeJWT)
		if email != "user@test.com" {
			t.Errorf("expected user@test.com, got %q", email)
		}
	})

	t.Run("empty string", func(t *testing.T) {
		email := ExtractEmailFromIDToken("")
		if email != "" {
			t.Errorf("expected empty string, got %q", email)
		}
	})

	t.Run("no dots", func(t *testing.T) {
		email := ExtractEmailFromIDToken("nodots")
		if email != "" {
			t.Errorf("expected empty string, got %q", email)
		}
	})

	t.Run("missing email claim", func(t *testing.T) {
		payload := `{"sub":"123"}`
		encoded := base64.RawURLEncoding.EncodeToString([]byte(payload))
		fakeJWT := fmt.Sprintf("header.%s.signature", encoded)

		email := ExtractEmailFromIDToken(fakeJWT)
		if email != "" {
			t.Errorf("expected empty string, got %q", email)
		}
	})
}

func TestSaveAndLoadOAuthTokens(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	original := &OAuthTokens{
		AccessToken:  "access_abc123",
		RefreshToken: "refresh_xyz789",
		ExpiresAt:    1700000000,
		Email:        "test@example.com",
	}

	if err := SaveOAuthTokens(original); err != nil {
		t.Fatalf("SaveOAuthTokens failed: %v", err)
	}

	loaded, err := LoadOAuthTokens()
	if err != nil {
		t.Fatalf("LoadOAuthTokens failed: %v", err)
	}

	if loaded.AccessToken != original.AccessToken {
		t.Errorf("AccessToken: got %q, want %q", loaded.AccessToken, original.AccessToken)
	}
	if loaded.RefreshToken != original.RefreshToken {
		t.Errorf("RefreshToken: got %q, want %q", loaded.RefreshToken, original.RefreshToken)
	}
	if loaded.ExpiresAt != original.ExpiresAt {
		t.Errorf("ExpiresAt: got %d, want %d", loaded.ExpiresAt, original.ExpiresAt)
	}
	if loaded.Email != original.Email {
		t.Errorf("Email: got %q, want %q", loaded.Email, original.Email)
	}
}

func TestGetValidOAuthToken_EnvVar(t *testing.T) {
	t.Setenv("OPENAI_OAUTH_TOKEN", "env_token_value")

	token, err := GetValidOAuthToken()
	if err != nil {
		t.Fatalf("GetValidOAuthToken failed: %v", err)
	}
	if token != "env_token_value" {
		t.Errorf("expected env_token_value, got %q", token)
	}
}

func TestRefreshTokenPreservation(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"access_token":  "new_access",
			"refresh_token": "",
			"expires_in":    3600,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer mockServer.Close()

	origEndpoint := openAIOAuthTokenEndpoint
	openAIOAuthTokenEndpoint = mockServer.URL
	t.Cleanup(func() { openAIOAuthTokenEndpoint = origEndpoint })

	tokens, err := RefreshOAuthToken("original_refresh_token")
	if err != nil {
		t.Fatalf("RefreshOAuthToken failed: %v", err)
	}

	if tokens.AccessToken != "new_access" {
		t.Errorf("AccessToken: got %q, want %q", tokens.AccessToken, "new_access")
	}
	if tokens.RefreshToken != "original_refresh_token" {
		t.Errorf("RefreshToken should be preserved: got %q, want %q", tokens.RefreshToken, "original_refresh_token")
	}
}
