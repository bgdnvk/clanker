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
	// Offline is explicitly NOT terminal — WaitInstanceRunning should keep
	// polling because a stopped instance may come back up.
	if isTerminalInstanceStatus(StatusOffline) {
		t.Error("offline should NOT be terminal for WaitInstanceRunning")
	}
	if !isTerminalVolumeStatus("attached") {
		t.Error("attached should be terminal")
	}
	if isTerminalVolumeStatus("cloning") {
		t.Error("cloning should NOT be terminal")
	}
}

func TestLooksLikeUUIDString(t *testing.T) {
	cases := map[string]bool{
		"4d04ce40-aed8-4bed-aa73-648e74b188c7": true,
		"4D04CE40-AED8-4BED-AA73-648E74B188C7": false, // caller should lowercase
		"not-a-uuid":                           false,
		"":                                     false,
	}
	for in, want := range cases {
		if got := looksLikeUUIDString(in); got != want {
			t.Errorf("looksLikeUUIDString(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestResolveInstanceIDByHostname(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/oauth2/token":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(TokenResponse{AccessToken: "tok", ExpiresIn: 3600})
		case "/v1/instances":
			_, _ = w.Write([]byte(`[
				{"id": "abc111aa-0000-0000-0000-000000000000", "hostname": "training-box", "status": "running"},
				{"id": "def222bb-0000-0000-0000-000000000000", "hostname": "infer-box", "status": "running"}
			]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	c := &Client{clientID: "id", clientSecret: "sec", httpClient: ts.Client()}
	origBase := baseURLForTest
	baseURLForTest = ts.URL
	defer func() { baseURLForTest = origBase }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Hostname path.
	id, err := c.ResolveInstanceID(ctx, "infer-box")
	if err != nil {
		t.Fatalf("resolve hostname: %v", err)
	}
	if id != "def222bb-0000-0000-0000-000000000000" {
		t.Errorf("wrong id: %s", id)
	}

	// UUID short-circuit — should not call /v1/instances a second time.
	got, err := c.ResolveInstanceID(ctx, "ABC111AA-0000-0000-0000-000000000000")
	if err != nil {
		t.Fatalf("uuid short-circuit: %v", err)
	}
	if got != "abc111aa-0000-0000-0000-000000000000" {
		t.Errorf("uuid not lowercased: %s", got)
	}

	// Unknown hostname.
	if _, err := c.ResolveInstanceID(ctx, "nonexistent"); err == nil {
		t.Error("expected error for unknown hostname")
	}

	// Empty input.
	if _, err := c.ResolveInstanceID(ctx, ""); err == nil {
		t.Error("expected error for empty input")
	}
}

func TestSleepCtxRespectsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	start := time.Now()
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	err := sleepCtx(ctx, 10*time.Second)
	if err == nil {
		t.Fatal("expected context error")
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("sleepCtx ignored cancellation, slept %v", elapsed)
	}
}

func TestSleepCtxZero(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := sleepCtx(ctx, 0); err != nil {
		t.Errorf("zero duration should return nil, got %v", err)
	}
}

func TestRunVerdaCLIReturnsErrCLINotInstalled(t *testing.T) {
	// Stash PATH so exec.LookPath can't find any binary for the duration of
	// this test, guaranteeing the RunVerdaCLI path takes the error branch
	// regardless of what the CI machine has installed.
	t.Setenv("PATH", "")

	c := &Client{clientID: "id", clientSecret: "sec"}
	_, err := c.RunVerdaCLI("vm", "list")
	if err == nil {
		t.Fatal("expected error when verda binary is absent")
	}
	if !IsCLINotInstalled(err) {
		t.Errorf("expected ErrCLINotInstalled, got %T: %v", err, err)
	}
	// The message should mention both the docs URL and the REST fallback so
	// users can choose their path without reading the sentinel's wrapping.
	msg := err.Error()
	if !strings.Contains(msg, "docs.verda.com") {
		t.Errorf("error message missing docs link: %q", msg)
	}
	if !strings.Contains(msg, "client_id") {
		t.Errorf("error message missing REST fallback hint: %q", msg)
	}
}

func TestCLIInstalledOnEmptyPath(t *testing.T) {
	t.Setenv("PATH", "")
	if CLIInstalled() {
		t.Error("CLIInstalled should be false when PATH is empty")
	}
}
