package notion

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newTestClient wires a Client at an httptest server. We rewrite the
// destination URL via a custom transport so production header building
// (Authorization: Bearer + Notion-Version) stays in scope of the test
// path — handlers assert what arrived.
func newTestClient(t *testing.T, ts *httptest.Server) *Client {
	t.Helper()
	c, err := NewClient("secret_test_token", "", false)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	c.SetHTTPClient(&http.Client{
		Transport: rewritingTransport{target: ts.URL},
		Timeout:   5 * time.Second,
	})
	return c
}

type rewritingTransport struct {
	target string
}

func (rt rewritingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	newReq, err := http.NewRequestWithContext(req.Context(), req.Method, rt.target+req.URL.RequestURI(), req.Body)
	if err != nil {
		return nil, err
	}
	newReq.Header = cloned.Header
	return http.DefaultTransport.RoundTrip(newReq)
}

func TestClientDo_HappyPath(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer secret_test_token" {
			t.Errorf("Authorization should be 'Bearer <token>', got %q (Bearer prefix is required for Notion, unlike Linear)", got)
		}
		if got := r.Header.Get("Notion-Version"); got != "2022-06-28" {
			t.Errorf("Notion-Version header missing or wrong: %q", got)
		}
		if !strings.HasSuffix(r.URL.Path, "/users/me") {
			t.Errorf("path mismatch: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"object":"user","id":"bot-id","type":"bot","name":"test bot","bot":{"workspace_name":"Test WS"}}`))
	}))
	defer ts.Close()

	c := newTestClient(t, ts)
	user, ws, err := c.Me(context.Background())
	if err != nil {
		t.Fatalf("Me: %v", err)
	}
	if user.ID != "bot-id" {
		t.Errorf("ID: got %s, want bot-id", user.ID)
	}
	if ws.WorkspaceName != "Test WS" {
		t.Errorf("WorkspaceName: got %q", ws.WorkspaceName)
	}
}

func TestClientDo_RateLimitBackoff(t *testing.T) {
	var attempts int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n == 1 {
			w.Header().Set("Retry-After", "0.05")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"object":"error","status":429,"code":"rate_limited","message":"slow down"}`))
			return
		}
		_, _ = w.Write([]byte(`{"object":"list","results":[],"has_more":false,"next_cursor":null}`))
	}))
	defer ts.Close()

	c := newTestClient(t, ts)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, _, _, err := c.Search(ctx, SearchOptions{Query: "x"}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Errorf("attempts: got %d, want 2 (1 throttle + 1 success)", got)
	}
}

func TestClientDo_APIError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"object":"error","status":401,"code":"unauthorized","message":"API token is invalid"}`))
	}))
	defer ts.Close()

	c := newTestClient(t, ts)
	_, _, err := c.Me(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if apiErr.Status != 401 || apiErr.Code != "unauthorized" {
		t.Errorf("unexpected envelope: %+v", apiErr)
	}
	if !IsAuthError(err) {
		t.Error("IsAuthError(401/unauthorized) should be true")
	}
}

func TestClientDo_ShareFailureEmptyResults(t *testing.T) {
	// When no pages have been shared with the integration, search returns
	// an empty list — NOT an error. This is the Notion share gotcha. Our
	// `ask` flow surfaces guidance to the user when this happens; the
	// client itself must treat the response as successful so callers can
	// distinguish "no access" from "transport failure".
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"object":"list","results":[],"has_more":false,"next_cursor":null}`))
	}))
	defer ts.Close()

	c := newTestClient(t, ts)
	pages, err := c.SearchPages(context.Background(), "anything", 25)
	if err != nil {
		t.Fatalf("SearchPages: %v", err)
	}
	if len(pages) != 0 {
		t.Errorf("expected empty result, got %d", len(pages))
	}
}

func TestParseRetryWait_NumericHeader(t *testing.T) {
	resp := &http.Response{Header: http.Header{}}
	resp.Header.Set("Retry-After", "1.5")
	got := parseRetryWait(resp)
	if got < 1500*time.Millisecond || got > 1600*time.Millisecond {
		t.Errorf("Retry-After=1.5 should parse near 1.5s, got %s", got)
	}
}

func TestIsRetryableNetErr_PermanentDrop(t *testing.T) {
	// Permanent errors (DNS, refused-connection) should NOT retry —
	// matches the post-review Linear behaviour.
	cases := []struct {
		msg      string
		expected bool
	}{
		{"context deadline exceeded (timeout)", true},
		{"read: connection reset by peer", true},
		{"unexpected EOF", true},
		{"dial tcp: lookup foo: no such host", false},
		{"connect: connection refused", false},
	}
	for _, tc := range cases {
		if got := isRetryableNetErr(errFromString(tc.msg)); got != tc.expected {
			t.Errorf("isRetryableNetErr(%q): got %v, want %v", tc.msg, got, tc.expected)
		}
	}
}

func errFromString(s string) error { return &simpleErr{s} }

type simpleErr struct{ s string }

func (e *simpleErr) Error() string { return e.s }

func TestClientDo_DecodeErrorBubblesPreview(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not-json`))
	}))
	defer ts.Close()

	c := newTestClient(t, ts)
	_, _, err := c.Me(context.Background())
	if err == nil {
		t.Fatal("expected decode error, got nil")
	}
	if !strings.Contains(err.Error(), "decode notion response") {
		t.Errorf("error should mention decode: %v", err)
	}
}

// silence the io import unused warning when running with -count=1
var _ = io.Discard
