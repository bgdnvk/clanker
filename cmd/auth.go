package cmd

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/bgdnvk/clanker/internal/ai"
	"github.com/spf13/cobra"
)

const (
	oauthAuthorizeURL = "https://auth.openai.com/oauth/authorize"
	oauthTokenURL     = "https://auth.openai.com/oauth/token"
	oauthClientID     = "app_EMoamEEZ73f0CkXaXp7hrann"
	oauthRedirectURI  = "http://localhost:1455/auth/callback"
	oauthCallbackPort = "1455"
)

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Manage OpenAI OAuth authentication",
	Long:  "Login, logout, and check authentication status for OpenAI OAuth (ChatGPT account).",
}

var authLoginCmd = &cobra.Command{
	Use:   "login",
	Short: "Login with your OpenAI (ChatGPT) account via OAuth",
	RunE:  runAuthLogin,
}

var authLogoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Remove saved OpenAI OAuth credentials",
	RunE:  runAuthLogout,
}

var authStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show current OpenAI OAuth login status",
	RunE:  runAuthStatus,
}

func init() {
	authCmd.AddCommand(authLoginCmd)
	authCmd.AddCommand(authLogoutCmd)
	authCmd.AddCommand(authStatusCmd)
	rootCmd.AddCommand(authCmd)
}

func runAuthLogin(_ *cobra.Command, _ []string) error {
	// Generate PKCE code verifier (32 random bytes, base64url encoded).
	verifierBytes := make([]byte, 32)
	if _, err := rand.Read(verifierBytes); err != nil {
		return fmt.Errorf("failed to generate code verifier: %w", err)
	}
	codeVerifier := base64.RawURLEncoding.EncodeToString(verifierBytes)

	// Derive code challenge (SHA-256 of verifier, base64url encoded).
	h := sha256.Sum256([]byte(codeVerifier))
	codeChallenge := base64.RawURLEncoding.EncodeToString(h[:])

	// Build the authorization URL.
	params := url.Values{
		"client_id":             {oauthClientID},
		"redirect_uri":          {oauthRedirectURI},
		"response_type":         {"code"},
		"scope":                 {"openid profile email offline_access"},
		"code_challenge":        {codeChallenge},
		"code_challenge_method": {"S256"},
	}
	authURL := oauthAuthorizeURL + "?" + params.Encode()

	// Channel to receive the authorization code from the callback.
	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	// Start local HTTP server to receive the OAuth callback.
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/callback", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		if code == "" {
			errMsg := r.URL.Query().Get("error")
			if errMsg == "" {
				errMsg = "no authorization code received"
			}
			http.Error(w, "Authorization failed: "+errMsg, http.StatusBadRequest)
			errCh <- fmt.Errorf("authorization failed: %s", errMsg)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body><h2>Login successful!</h2><p>You can close this window and return to the terminal.</p></body></html>`)
		codeCh <- code
	})

	server := &http.Server{
		Addr:    ":" + oauthCallbackPort,
		Handler: mux,
	}

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("callback server failed: %w", err)
		}
	}()

	// Open the authorization URL in the default browser.
	fmt.Println("Opening browser for OpenAI login...")
	fmt.Printf("If the browser does not open, visit this URL:\n%s\n\n", authURL)
	openBrowser(authURL)

	// Wait for the callback or an error.
	var authCode string
	select {
	case authCode = <-codeCh:
	case err := <-errCh:
		server.Close()
		return err
	case <-time.After(5 * time.Minute):
		server.Close()
		return fmt.Errorf("login timed out after 5 minutes")
	}

	// Shut down the callback server.
	server.Close()

	// Exchange authorization code for tokens.
	tokens, email, err := exchangeAuthCode(authCode, codeVerifier)
	if err != nil {
		return err
	}

	// Save tokens to disk.
	if err := ai.SaveOAuthTokens(tokens); err != nil {
		return fmt.Errorf("failed to save tokens: %w", err)
	}

	if email != "" {
		fmt.Printf("Logged in as %s\n", email)
	} else {
		fmt.Println("Logged in successfully")
	}
	return nil
}

func exchangeAuthCode(code, codeVerifier string) (*ai.OAuthTokens, string, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {oauthClientID},
		"code":          {code},
		"code_verifier": {codeVerifier},
		"redirect_uri":  {oauthRedirectURI},
	}

	resp, err := http.Post(oauthTokenURL, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, "", fmt.Errorf("token exchange request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("failed to read token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("token exchange failed (HTTP %d): %s", resp.StatusCode, string(body))
	}

	var raw struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, "", fmt.Errorf("failed to parse token response: %w", err)
	}

	email := ai.ExtractEmailFromIDToken(raw.IDToken)

	tokens := &ai.OAuthTokens{
		AccessToken:  raw.AccessToken,
		RefreshToken: raw.RefreshToken,
		ExpiresAt:    time.Now().Unix() + raw.ExpiresIn,
		Email:        email,
	}

	return tokens, email, nil
}

func runAuthLogout(_ *cobra.Command, _ []string) error {
	if err := ai.RemoveOAuthTokens(); err != nil {
		return fmt.Errorf("failed to remove auth tokens: %w", err)
	}
	fmt.Println("Logged out")
	return nil
}

func runAuthStatus(_ *cobra.Command, _ []string) error {
	tokens, err := ai.LoadOAuthTokens()
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("Not logged in")
			return nil
		}
		return fmt.Errorf("failed to read auth tokens: %w", err)
	}

	if tokens.Email != "" {
		fmt.Printf("Logged in as %s\n", tokens.Email)
	} else {
		fmt.Println("Logged in (email not available)")
	}

	expiresAt := time.Unix(tokens.ExpiresAt, 0)
	if time.Now().After(expiresAt) {
		fmt.Println("Token expired (will be refreshed on next use)")
	} else {
		fmt.Printf("Token expires at %s\n", expiresAt.Format(time.RFC3339))
	}

	return nil
}

// openBrowser opens the given URL in the default browser.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
