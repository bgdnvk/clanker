package verda

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDecodeAPIErrorBody(t *testing.T) {
	err := decodeAPIErrorBody([]byte(`{"code":"insufficient_funds","message":"balance too low"}`), 402)
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("expected *APIError, got %T", err)
	}
	if apiErr.Code != "insufficient_funds" {
		t.Errorf("unexpected code: %q", apiErr.Code)
	}
	if !strings.Contains(apiErr.Error(), "insufficient_funds") {
		t.Errorf("Error() missing code: %q", apiErr.Error())
	}
}

func TestDecodeAPIErrorBodyFallback(t *testing.T) {
	err := decodeAPIErrorBody([]byte("plain text error"), 500)
	if err == nil {
		t.Fatal("expected error")
	}
	if _, ok := err.(*APIError); ok {
		t.Errorf("non-JSON body should not decode to *APIError")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("expected status in fallback message, got %q", err.Error())
	}
}

func TestDecodeActionResults(t *testing.T) {
	body := `[{"instanceId":"abc","action":"start","status":"success"},{"instanceId":"def","action":"start","status":"error","error":"not found","statusCode":404}]`
	results, err := DecodeActionResults(body)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[1].StatusCode != 404 {
		t.Errorf("expected statusCode 404, got %d", results[1].StatusCode)
	}
}

func TestDecodeActionResultsEmpty(t *testing.T) {
	if results, err := DecodeActionResults(""); err != nil || results != nil {
		t.Errorf("empty body should return nil results, got %v / %v", results, err)
	}
	// Non-array JSON means the endpoint returned a scalar success body.
	if results, err := DecodeActionResults(`{"id":"abc"}`); err != nil || results != nil {
		t.Errorf("non-array body should return nil results, got %v / %v", results, err)
	}
}

func TestClientEnsureTokenCachesAcrossCalls(t *testing.T) {
	var tokenCalls int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/oauth2/token":
			atomic.AddInt32(&tokenCalls, 1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(TokenResponse{
				AccessToken: "tok-1", TokenType: "Bearer", ExpiresIn: 3600, Scope: "cloud-api-v1",
			})
		case r.URL.Path == "/v1/balance":
			if r.Header.Get("Authorization") != "Bearer tok-1" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"amount": 100, "currency":"usd"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	c := &Client{clientID: "id", clientSecret: "sec", httpClient: ts.Client()}
	// Swap the base URL via a test double — point doRequest at the test server.
	origBase := baseURLForTest
	baseURLForTest = ts.URL
	defer func() { baseURLForTest = origBase }()

	for i := 0; i < 3; i++ {
		if _, err := c.RunAPIWithContext(context.Background(), http.MethodGet, "/v1/balance", ""); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}

	if got := atomic.LoadInt32(&tokenCalls); got != 1 {
		t.Errorf("expected token endpoint hit once, got %d", got)
	}
}

func TestClientRetriesOn429(t *testing.T) {
	var instCalls int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/oauth2/token" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(TokenResponse{AccessToken: "tok", ExpiresIn: 3600})
			return
		}
		if r.URL.Path == "/v1/instances" {
			n := atomic.AddInt32(&instCalls, 1)
			if n < 2 {
				w.Header().Set("Retry-After", "0")
				http.Error(w, `{"code":"rate_limit_exceeded","message":"slow down"}`, http.StatusTooManyRequests)
				return
			}
			_, _ = w.Write([]byte(`[]`))
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	c := &Client{clientID: "id", clientSecret: "sec", httpClient: ts.Client()}
	origBase := baseURLForTest
	baseURLForTest = ts.URL
	defer func() { baseURLForTest = origBase }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := c.RunAPIWithContext(ctx, http.MethodGet, "/v1/instances", ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := atomic.LoadInt32(&instCalls); got < 2 {
		t.Errorf("expected at least 2 attempts, got %d", got)
	}
}

func TestReadVerdaCredentialsYAML(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	dir := filepath.Join(tmpHome, ".verda")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	yaml := `active_profile: prod
profiles:
  prod:
    client_id: id-prod
    client_secret: secret-prod
  dev:
    client_id: id-dev
    client_secret: secret-dev
`
	if err := os.WriteFile(filepath.Join(dir, "credentials"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	entry, err := readVerdaCredentials()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if entry.ClientID != "id-prod" || entry.ClientSecret != "secret-prod" {
		t.Errorf("wrong profile selected: %+v", entry)
	}
}

func TestReadVerdaCredentialsFlat(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	dir := filepath.Join(tmpHome, ".verda")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	flat := `client_id: id-flat
client_secret: secret-flat
`
	if err := os.WriteFile(filepath.Join(dir, "credentials"), []byte(flat), 0o644); err != nil {
		t.Fatal(err)
	}

	entry, err := readVerdaCredentials()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if entry.ClientID != "id-flat" || entry.ClientSecret != "secret-flat" {
		t.Errorf("wrong flat entry: %+v", entry)
	}
}

func TestTerminalStatusHelpers(t *testing.T) {
	if !isTerminalInstanceStatus(StatusRunning) {
		t.Error("running should be terminal")
	}
	if isTerminalInstanceStatus(StatusProvisioning) {
		t.Error("provisioning should NOT be terminal")
	}
	if !isTerminalVolumeStatus("attached") {
		t.Error("attached should be terminal")
	}
	if isTerminalVolumeStatus("cloning") {
		t.Error("cloning should NOT be terminal")
	}
}
