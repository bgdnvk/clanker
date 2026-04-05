package ai

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// OAuthTokens holds the persisted OpenAI OAuth token set.
type OAuthTokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"`
	Email        string `json:"email"`
}

const (
	openAIOAuthTokenEndpoint = "https://auth.openai.com/oauth/token"
	openAIOAuthClientID      = "app_EMoamEEZ73f0CkXaXp7hrann"
	oauthTokenFileName       = "openai-auth.json"
)

func oauthTokenFilePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	return filepath.Join(home, ".clanker", oauthTokenFileName), nil
}

// LoadOAuthTokens reads the saved OpenAI OAuth tokens from disk.
func LoadOAuthTokens() (*OAuthTokens, error) {
	p, err := oauthTokenFilePath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var tokens OAuthTokens
	if err := json.Unmarshal(data, &tokens); err != nil {
		return nil, fmt.Errorf("failed to parse oauth token file: %w", err)
	}
	return &tokens, nil
}

// SaveOAuthTokens writes the OpenAI OAuth tokens to disk.
func SaveOAuthTokens(tokens *OAuthTokens) error {
	p, err := oauthTokenFilePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}
	data, err := json.MarshalIndent(tokens, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0600)
}

// RemoveOAuthTokens deletes the saved token file.
func RemoveOAuthTokens() error {
	p, err := oauthTokenFilePath()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// RefreshOAuthToken exchanges a refresh token for a new access token.
func RefreshOAuthToken(refreshToken string) (*OAuthTokens, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {openAIOAuthClientID},
		"refresh_token": {refreshToken},
	}

	resp, err := http.Post(openAIOAuthTokenEndpoint, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("token refresh request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read refresh response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token refresh failed (HTTP %d): %s", resp.StatusCode, string(body))
	}

	var raw struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse refresh response: %w", err)
	}

	newRefreshToken := raw.RefreshToken
	if newRefreshToken == "" {
		newRefreshToken = refreshToken // preserve original when server omits it
	}
	tokens := &OAuthTokens{
		AccessToken:  raw.AccessToken,
		RefreshToken: newRefreshToken,
		ExpiresAt:    time.Now().Unix() + raw.ExpiresIn,
	}

	// Preserve email from the id_token if available.
	if raw.IDToken != "" {
		if email := ExtractEmailFromIDToken(raw.IDToken); email != "" {
			tokens.Email = email
		}
	}

	return tokens, nil
}

// GetValidOAuthToken loads the stored tokens, refreshes if they expire within
// 5 minutes, saves updated tokens back to disk, and returns the access token.
// If OPENAI_OAUTH_TOKEN is set in the environment (forwarded by the backend),
// it takes precedence over the saved token file.
func GetValidOAuthToken() (string, error) {
	if envToken := strings.TrimSpace(os.Getenv("OPENAI_OAUTH_TOKEN")); envToken != "" {
		return envToken, nil
	}

	tokens, err := LoadOAuthTokens()
	if err != nil {
		return "", err
	}

	const refreshMarginSeconds int64 = 300 // 5 minutes
	if time.Now().Unix()+refreshMarginSeconds >= tokens.ExpiresAt {
		refreshed, err := RefreshOAuthToken(tokens.RefreshToken)
		if err != nil {
			return "", fmt.Errorf("failed to refresh oauth token: %w", err)
		}
		// Keep the original email if the refresh didn't return one.
		if refreshed.Email == "" {
			refreshed.Email = tokens.Email
		}
		if err := SaveOAuthTokens(refreshed); err != nil {
			return "", fmt.Errorf("failed to save refreshed tokens: %w", err)
		}
		return refreshed.AccessToken, nil
	}

	return tokens.AccessToken, nil
}

// ExtractEmailFromIDToken decodes the payload of a JWT id_token (without
// verifying the signature) and returns the "email" claim if present.
func ExtractEmailFromIDToken(idToken string) string {
	parts := strings.Split(idToken, ".")
	if len(parts) < 2 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	return claims.Email
}
