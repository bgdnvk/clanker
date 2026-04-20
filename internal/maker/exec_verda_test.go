package maker

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// withVerdaTestServer swaps the verda package baseURL for the test server's
// URL. We can't import internal/verda's test hook from this package, so the
// trick is to run the executor against a real httptest server that serves
// /v1/oauth2/token and the target endpoints — verda.NewClient picks up the
// configured baseURL via the package's exported const... actually the
// internal baseURL is a var in the verda package so we need to set it via
// a tiny test-only helper. We accomplish that via the public SetBaseURL()
// shim added alongside these tests. If the shim is missing we skip.

func TestValidateVerdaCommand_Shape(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		destroy bool
		ok      bool
	}{
		{"too few args", []string{"verda-api", "GET"}, false, false},
		{"too many args", []string{"verda-api", "GET", "/v1/balance", "", "extra"}, false, false},
		{"wrong verb", []string{"verda", "GET", "/v1/balance"}, false, false},
		{"bad method", []string{"verda-api", "QUERY", "/v1/balance", ""}, false, false},
		{"bad path prefix", []string{"verda-api", "GET", "v1/balance", ""}, false, false},
		{"newline in body", []string{"verda-api", "POST", "/v1/instances", "{\"hostname\":\"h\nevil\"}"}, false, false},
		{"valid GET", []string{"verda-api", "GET", "/v1/balance", ""}, false, true},
		{"valid POST with body", []string{"verda-api", "POST", "/v1/instances", `{"instance_type":"1H100.80S.22V"}`}, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateVerdaCommand(tc.args, tc.destroy)
			if tc.ok && err != nil {
				t.Errorf("expected ok, got: %v", err)
			}
			if !tc.ok && err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestValidateVerdaCommand_DestructiveGate(t *testing.T) {
	// DELETE is always destructive.
	if err := validateVerdaCommand([]string{"verda-api", "DELETE", "/v1/volumes/abc", ""}, false); err == nil {
		t.Error("DELETE should be blocked without destroyer")
	}
	if err := validateVerdaCommand([]string{"verda-api", "DELETE", "/v1/volumes/abc", ""}, true); err != nil {
		t.Errorf("DELETE should pass with destroyer: %v", err)
	}

	// PUT /v1/instances with action=delete is destructive.
	bodyDelete := `{"action":"delete","id":"abc"}`
	if err := validateVerdaCommand([]string{"verda-api", "PUT", "/v1/instances", bodyDelete}, false); err == nil {
		t.Error("PUT action=delete should be blocked without destroyer")
	}
	if err := validateVerdaCommand([]string{"verda-api", "PUT", "/v1/instances", bodyDelete}, true); err != nil {
		t.Errorf("PUT action=delete should pass with destroyer: %v", err)
	}

	// PUT /v1/instances with action=start is NOT destructive.
	bodyStart := `{"action":"start","id":"abc"}`
	if err := validateVerdaCommand([]string{"verda-api", "PUT", "/v1/instances", bodyStart}, false); err != nil {
		t.Errorf("PUT action=start should pass: %v", err)
	}

	// PUT /v1/clusters with action=discontinue is destructive.
	bodyDiscontinue := `{"action":"discontinue","id":"abc"}`
	if err := validateVerdaCommand([]string{"verda-api", "PUT", "/v1/clusters", bodyDiscontinue}, false); err == nil {
		t.Error("PUT cluster action=discontinue should be blocked without destroyer")
	}
}

func TestIsVerdaDestructive(t *testing.T) {
	cases := []struct {
		method, path, body string
		want               bool
	}{
		{"GET", "/v1/instances", "", false},
		{"POST", "/v1/instances", `{"instance_type":"1"}`, false},
		{"PUT", "/v1/instances", `{"action":"start","id":"a"}`, false},
		{"PUT", "/v1/instances", `{"action":"shutdown","id":"a"}`, false},
		{"PUT", "/v1/instances", `{"action":"delete","id":"a"}`, true},
		{"PUT", "/v1/instances", `{"action":"force_shutdown","id":"a"}`, true},
		{"PUT", "/v1/instances", `{"action":"hibernate","id":"a"}`, true},
		{"DELETE", "/v1/volumes/abc", "", true},
		{"PUT", "/v1/clusters", `{"action":"discontinue","id":"a"}`, true},
		{"PATCH", "/v1/container-deployments/foo", "{}", false},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			if got := isVerdaDestructive(tc.method, tc.path, tc.body); got != tc.want {
				t.Errorf("isVerdaDestructive(%s %s) = %v, want %v", tc.method, tc.path, got, tc.want)
			}
		})
	}
}

func TestExecuteVerdaPlan_EndToEnd(t *testing.T) {
	var tokenCalls, balanceCalls, createCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&tokenCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token": "tok",
			"token_type":   "Bearer",
			"expires_in":   3600,
			"scope":        "cloud-api-v1",
		})
	})
	mux.HandleFunc("/v1/balance", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "bad token", http.StatusUnauthorized)
			return
		}
		atomic.AddInt32(&balanceCalls, 1)
		_, _ = w.Write([]byte(`{"amount": 100, "currency": "usd"}`))
	})
	mux.HandleFunc("/v1/instances", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		atomic.AddInt32(&createCalls, 1)
		// Echo the created instance id so binding capture exercises the
		// `produces` JSONPath flow.
		_, _ = w.Write([]byte(`{"id": "new-instance-uuid"}`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// Redirect the verda package at the test server.
	origBase := verdaTestBaseURL(t, ts.URL)
	defer verdaTestBaseURL(t, origBase)

	plan := &Plan{
		Version:  CurrentPlanVersion,
		Provider: "verda",
		Commands: []Command{
			{Args: []string{"verda-api", "GET", "/v1/balance", ""}, Reason: "check balance"},
			{
				Args:     []string{"verda-api", "POST", "/v1/instances", `{"instance_type":"1H100.80S.22V","image":"ubuntu","hostname":"h","description":"d","location_code":"FIN-01","ssh_key_ids":["k"]}`},
				Reason:   "create instance",
				Produces: map[string]string{"INSTANCE_ID": "$.id"},
			},
		},
	}

	var out bytes.Buffer
	err := ExecuteVerdaPlan(context.Background(), plan, ExecOptions{
		VerdaClientID:     "cid",
		VerdaClientSecret: "secret",
		VerdaProjectID:    "proj-1",
		Writer:            &out,
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	if atomic.LoadInt32(&tokenCalls) != 1 {
		t.Errorf("expected 1 token fetch (cached between calls), got %d", tokenCalls)
	}
	if atomic.LoadInt32(&balanceCalls) != 1 {
		t.Errorf("expected 1 balance call, got %d", balanceCalls)
	}
	if atomic.LoadInt32(&createCalls) != 1 {
		t.Errorf("expected 1 create call, got %d", createCalls)
	}
	if !strings.Contains(out.String(), "verda-api GET /v1/balance") {
		t.Errorf("expected progress line for balance call, got:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "verda-api POST /v1/instances") {
		t.Errorf("expected progress line for create call, got:\n%s", out.String())
	}
}

func TestExecuteVerdaPlan_BindingFlow(t *testing.T) {
	// Smoke-test that a placeholder <INSTANCE_ID> emitted by the first
	// command's `produces` is substituted into the second command before
	// execution.
	var lastBody string
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"access_token": "tok", "expires_in": 3600})
	})
	mux.HandleFunc("/v1/instances", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			_, _ = w.Write([]byte(`{"id": "abc-123"}`))
		case http.MethodPut:
			buf := new(bytes.Buffer)
			_, _ = buf.ReadFrom(r.Body)
			lastBody = buf.String()
			_, _ = w.Write([]byte(`{"accepted": true}`))
		}
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	orig := verdaTestBaseURL(t, ts.URL)
	defer verdaTestBaseURL(t, orig)

	plan := &Plan{
		Version:  CurrentPlanVersion,
		Provider: "verda",
		Commands: []Command{
			{
				Args:     []string{"verda-api", "POST", "/v1/instances", `{"instance_type":"1H100","image":"u","hostname":"h","description":"d","location_code":"FIN-01"}`},
				Produces: map[string]string{"INSTANCE_ID": "$.id"},
			},
			{
				Args: []string{"verda-api", "PUT", "/v1/instances", `{"action":"start","id":"<INSTANCE_ID>"}`},
			},
		},
	}

	var out bytes.Buffer
	if err := ExecuteVerdaPlan(context.Background(), plan, ExecOptions{
		VerdaClientID:     "cid",
		VerdaClientSecret: "secret",
		Writer:            &out,
	}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(lastBody, `"id":"abc-123"`) {
		t.Errorf("placeholder not substituted; PUT body was %q", lastBody)
	}
}

func TestExecuteVerdaPlan_MissingCreds(t *testing.T) {
	plan := &Plan{Provider: "verda", Commands: []Command{
		{Args: []string{"verda-api", "GET", "/v1/balance", ""}},
	}}
	var out bytes.Buffer
	err := ExecuteVerdaPlan(context.Background(), plan, ExecOptions{Writer: &out})
	if err == nil {
		t.Fatal("expected error on missing creds")
	}
	if !strings.Contains(err.Error(), "client_id") {
		t.Errorf("error should mention client_id: %v", err)
	}
}
