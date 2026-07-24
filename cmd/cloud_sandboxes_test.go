package cmd

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bgdnvk/clanker/internal/clankercloud"
)

func TestExecuteSandboxRunDisposesAfterSuccess(t *testing.T) {
	var messageCalls atomic.Int32
	var deleteCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "POST /api/sandboxes":
			_, _ = w.Write([]byte(`{"box":{"id":"box_123"},"sandboxToken":"runtime-secret"}`))
		case "POST /api/sandboxes/box_123/messages":
			messageCalls.Add(1)
			if got := r.Header.Get("X-API-Key"); got != "runtime-secret" {
				t.Errorf("message token = %q, want cached sandbox token", got)
			}
			_, _ = w.Write([]byte(`{"ok":true,"message":"done"}`))
		case "DELETE /api/sandboxes/box_123":
			deleteCalls.Add(1)
			if got := r.Header.Get("X-API-Key"); got != "runtime-secret" {
				t.Errorf("delete token = %q, want cached sandbox token", got)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := clankercloud.NewSandboxClient(clankercloud.SandboxClientOptions{
		BaseURL:    server.URL + "/api",
		AccountKey: "account-token",
		HTTPClient: server.Client(),
	})
	result, err := executeSandboxRun(
		context.Background(),
		client,
		clankercloud.SandboxCreateRequest{Name: "temporary"},
		clankercloud.SandboxMessageRequest{Content: "build it"},
		false,
	)
	if err != nil {
		t.Fatalf("executeSandboxRun: %v", err)
	}
	if result["disposed"] != true {
		t.Fatalf("result = %#v, want disposed=true", result)
	}
	if messageCalls.Load() != 1 || deleteCalls.Load() != 1 {
		t.Fatalf("message calls=%d delete calls=%d, want 1 each", messageCalls.Load(), deleteCalls.Load())
	}
	created, ok := result["created"].(*clankercloud.SandboxAPIResult)
	if !ok {
		t.Fatalf("created result type = %T", result["created"])
	}
	if strings.Contains(strings.ToLower(toJSONForTest(created)), "runtime-secret") {
		t.Fatalf("created result exposed sandbox token: %#v", created)
	}
}

func TestExecuteSandboxRunDisposesAfterMessageFailure(t *testing.T) {
	var deleteCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "POST /api/sandboxes":
			_, _ = w.Write([]byte(`{"box":{"id":"box_123"},"sandboxToken":"runtime-secret"}`))
		case "POST /api/sandboxes/box_123/messages":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"agent failed"}`))
		case "DELETE /api/sandboxes/box_123":
			deleteCalls.Add(1)
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"ok":false,"error":"sandbox not found"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := clankercloud.NewSandboxClient(clankercloud.SandboxClientOptions{
		BaseURL:    server.URL + "/api",
		AccountKey: "account-token",
		HTTPClient: server.Client(),
	})
	result, err := executeSandboxRun(
		context.Background(),
		client,
		clankercloud.SandboxCreateRequest{Name: "temporary"},
		clankercloud.SandboxMessageRequest{Content: "build it"},
		false,
	)
	if err == nil || !strings.Contains(err.Error(), "status 500") {
		t.Fatalf("executeSandboxRun error = %v, want message status error", err)
	}
	if result["disposed"] != true || result["alreadyDisposed"] != true {
		t.Fatalf("result = %#v, want idempotent disposal metadata", result)
	}
	if deleteCalls.Load() != 1 {
		t.Fatalf("delete calls=%d, want 1", deleteCalls.Load())
	}
}

func TestExecuteSandboxRunKeepSandboxOptOut(t *testing.T) {
	var deleteCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "POST /api/sandboxes":
			_, _ = w.Write([]byte(`{"box":{"id":"box_123"},"sandboxToken":"runtime-secret"}`))
		case "POST /api/sandboxes/box_123/messages":
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "DELETE /api/sandboxes/box_123":
			deleteCalls.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := clankercloud.NewSandboxClient(clankercloud.SandboxClientOptions{
		BaseURL:    server.URL + "/api",
		AccountKey: "account-token",
		HTTPClient: server.Client(),
	})
	result, err := executeSandboxRun(
		context.Background(),
		client,
		clankercloud.SandboxCreateRequest{Name: "retained"},
		clankercloud.SandboxMessageRequest{Content: "inspect it"},
		true,
	)
	if err != nil {
		t.Fatalf("executeSandboxRun: %v", err)
	}
	if result["sandboxRetained"] != true || deleteCalls.Load() != 0 {
		t.Fatalf("result=%#v delete calls=%d", result, deleteCalls.Load())
	}
}

func TestExecuteSandboxRunReportsManageableCleanupFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "POST /api/sandboxes":
			_, _ = w.Write([]byte(`{"box":{"id":"box_cleanup"},"sandboxToken":"runtime-secret"}`))
		case "POST /api/sandboxes/box_cleanup/messages":
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "DELETE /api/sandboxes/box_cleanup":
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"ok":false,"error":"cleanup temporarily unavailable"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := clankercloud.NewSandboxClient(clankercloud.SandboxClientOptions{
		BaseURL:    server.URL + "/api",
		AccountKey: "account-token",
		HTTPClient: server.Client(),
	})
	result, err := executeSandboxRun(
		context.Background(),
		client,
		clankercloud.SandboxCreateRequest{Name: "temporary"},
		clankercloud.SandboxMessageRequest{Content: "build it"},
		false,
	)
	if err == nil || !strings.Contains(err.Error(), "status 503") {
		t.Fatalf("executeSandboxRun error = %v, want cleanup status error", err)
	}
	if result["sandboxId"] != "box_cleanup" || result["disposed"] != false || result["cleanupRequired"] != true {
		t.Fatalf("cleanup failure result = %#v", result)
	}
	if hint, _ := result["cleanupHint"].(string); !strings.Contains(hint, "clanker cloud sandboxes delete box_cleanup") {
		t.Fatalf("cleanup hint = %q", hint)
	}
}

func TestExecuteSandboxRunDisposesAfterParentCancellation(t *testing.T) {
	messageStarted := make(chan struct{})
	var deleteCalls atomic.Int32
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.Method + " " + r.URL.Path {
		case "POST /api/sandboxes":
			return testHTTPResponse(http.StatusOK, `{"box":{"id":"box_cancel"},"sandboxToken":"runtime-secret"}`), nil
		case "POST /api/sandboxes/box_cancel/messages":
			close(messageStarted)
			<-r.Context().Done()
			return nil, r.Context().Err()
		case "DELETE /api/sandboxes/box_cancel":
			deleteCalls.Add(1)
			if err := r.Context().Err(); err != nil {
				t.Errorf("cleanup inherited cancelled context: %v", err)
			}
			return testHTTPResponse(http.StatusNoContent, ""), nil
		default:
			return testHTTPResponse(http.StatusNotFound, `{"ok":false,"error":"route not found"}`), nil
		}
	})}

	client := clankercloud.NewSandboxClient(clankercloud.SandboxClientOptions{
		BaseURL:    "http://127.0.0.1:8080/api",
		AccountKey: "account-token",
		HTTPClient: httpClient,
	})
	ctx, cancel := context.WithCancel(context.Background())
	type runResult struct {
		value map[string]any
		err   error
	}
	done := make(chan runResult, 1)
	go func() {
		value, err := executeSandboxRun(
			ctx,
			client,
			clankercloud.SandboxCreateRequest{Name: "temporary"},
			clankercloud.SandboxMessageRequest{Content: "wait"},
			false,
		)
		done <- runResult{value: value, err: err}
	}()

	select {
	case <-messageStarted:
		cancel()
	case <-time.After(5 * time.Second):
		t.Fatal("message request did not start")
	}

	select {
	case result := <-done:
		if result.err == nil || !strings.Contains(result.err.Error(), "context canceled") {
			t.Fatalf("executeSandboxRun error = %v, want cancellation", result.err)
		}
		if result.value["disposed"] != true {
			t.Fatalf("cancellation result = %#v, want disposed=true", result.value)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("executeSandboxRun did not finish after cancellation")
	}
	if deleteCalls.Load() != 1 {
		t.Fatalf("delete calls = %d, want cleanup after cancellation", deleteCalls.Load())
	}
}

func TestExecuteSandboxRunRequiresManageableAccount(t *testing.T) {
	client := clankercloud.NewSandboxClient(clankercloud.SandboxClientOptions{
		BaseURL: "https://clankercloud.ai/api",
	})
	result, err := executeSandboxRun(
		context.Background(),
		client,
		clankercloud.SandboxCreateRequest{Name: "unmanaged"},
		clankercloud.SandboxMessageRequest{Content: "run"},
		false,
	)
	if err == nil || !strings.Contains(err.Error(), "account API key is required") {
		t.Fatalf("executeSandboxRun error = %v, want account requirement", err)
	}
	if len(result) != 0 {
		t.Fatalf("account failure result = %#v, want no created sandbox", result)
	}
}

func TestTrustedMCPSandboxTaskMetadataReservesProvenance(t *testing.T) {
	metadata := trustedMCPSandboxTaskMetadata(map[string]any{
		"source": "spoofed",
		"mode":   "persistent",
		"Source": "also-spoofed",
		"ticket": "INC-123",
	})
	if metadata["source"] != "clanker-mcp" || metadata["mode"] != "sandbox-task" {
		t.Fatalf("trusted metadata was overwritten: %#v", metadata)
	}
	if metadata["ticket"] != "INC-123" {
		t.Fatalf("user metadata was lost: %#v", metadata)
	}
	if _, present := metadata["Source"]; present {
		t.Fatalf("case-variant provenance key was retained: %#v", metadata)
	}
}

func TestMCPSandboxSchemasDoNotExposeCredentialsOrEndpoints(t *testing.T) {
	types := []reflect.Type{
		reflect.TypeOf(cloudSandboxCreateArgs{}),
		reflect.TypeOf(cloudSandboxListArgs{}),
		reflect.TypeOf(cloudSandboxIDArgs{}),
		reflect.TypeOf(cloudSandboxCommandArgs{}),
		reflect.TypeOf(cloudSandboxMessageArgs{}),
		reflect.TypeOf(cloudSandboxTaskArgs{}),
	}
	for _, typ := range types {
		for index := 0; index < typ.NumField(); index++ {
			field := typ.Field(index)
			jsonName := strings.Split(field.Tag.Get("json"), ",")[0]
			switch jsonName {
			case "apiBaseUrl", "apiKey", "sandboxToken":
				t.Fatalf("%s exposes forbidden MCP field %q", typ.Name(), jsonName)
			}
		}
	}
}

func toJSONForTest(value any) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func testHTTPResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
