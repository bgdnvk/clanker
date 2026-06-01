package sentry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newTestClient returns a Client wired to point at the given test server.
// We override Host but then have to swap the http.Client because the real
// Client always prepends https://; the http.Client transport is mocked to
// rewrite the URL back to the httptest target.
func newTestClient(t *testing.T, ts *httptest.Server) *Client {
	t.Helper()
	c, err := NewClient("test-token", "test-org", "sentry.io", false)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	c.SetHTTPClient(&http.Client{
		Transport: rewritingTransport{target: ts.URL},
		Timeout:   5 * time.Second,
	})
	return c
}

// rewritingTransport sends every request to the test server regardless of
// the URL the client constructed — saves us from having to plumb a base URL
// override through every helper.
type rewritingTransport struct {
	target string
}

func (rt rewritingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Preserve the path + query so handlers can assert on them.
	target := rt.target + req.URL.Path
	if req.URL.RawQuery != "" {
		target += "?" + req.URL.RawQuery
	}
	cloned := req.Clone(req.Context())
	newReq, err := http.NewRequestWithContext(req.Context(), req.Method, target, req.Body)
	if err != nil {
		return nil, err
	}
	newReq.Header = cloned.Header
	return http.DefaultTransport.RoundTrip(newReq)
}

func TestClientDo_HappyPath(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("missing Bearer token, got %q", got)
		}
		if got := r.URL.Path; got != "/api/0/organizations/test-org/projects/" {
			t.Errorf("unexpected path: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"id":"1","slug":"backend","name":"Backend","platform":"go","dateCreated":"2024-01-01T00:00:00Z","isBookmarked":false}]`)
	}))
	defer ts.Close()

	c := newTestClient(t, ts)
	projects, err := c.ListProjects(context.Background(), "")
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 1 {
		t.Fatalf("got %d projects, want 1", len(projects))
	}
	if projects[0].Slug != "backend" {
		t.Errorf("slug = %q, want backend", projects[0].Slug)
	}
}

// TestClientDo_RateLimit_RetryAfter exercises the 429 backoff: the first
// response is 429 with Retry-After:0 (so the test doesn't actually wait
// seconds), the second succeeds.
func TestClientDo_RateLimit_RetryAfter(t *testing.T) {
	var attempts atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"detail":"rate limited"}`)
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `[]`)
	}))
	defer ts.Close()

	c := newTestClient(t, ts)
	_, err := c.ListProjects(context.Background(), "")
	if err != nil {
		t.Fatalf("ListProjects after retry: %v", err)
	}
	if attempts.Load() != 2 {
		t.Errorf("expected 2 attempts, got %d", attempts.Load())
	}
}

// TestClientDo_APIError surfaces non-200 responses as an APIError with the
// detail extracted from the upstream JSON envelope.
func TestClientDo_APIError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"detail":"You do not have permission to perform this action."}`)
	}))
	defer ts.Close()

	c := newTestClient(t, ts)
	_, err := c.ListProjects(context.Background(), "")
	if err == nil {
		t.Fatal("expected APIError, got nil")
	}
	if !strings.Contains(err.Error(), "permission") {
		t.Errorf("error should mention 'permission': %v", err)
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("expected *APIError, got %T", err)
	}
	if apiErr.Status != http.StatusForbidden {
		t.Errorf("Status = %d, want 403", apiErr.Status)
	}
}

func TestParseNextCursor(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   string
	}{
		{
			name:   "no link header",
			header: "",
			want:   "",
		},
		{
			name:   "next with results=true",
			header: `<https://sentry.io/api/0/x/?cursor=abc:0:0>; rel="previous"; results="false"; cursor="abc:0:0", <https://sentry.io/api/0/x/?cursor=def:0:0>; rel="next"; results="true"; cursor="def:0:0"`,
			want:   "def:0:0",
		},
		{
			name:   "next with results=false stops pagination",
			header: `<https://sentry.io/api/0/x/?cursor=abc:0:0>; rel="next"; results="false"; cursor="abc:0:0"`,
			want:   "",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			resp := &http.Response{Header: http.Header{"Link": []string{tc.header}}}
			if got := ParseNextCursor(resp); got != tc.want {
				t.Errorf("ParseNextCursor = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBuildQuery(t *testing.T) {
	got := BuildQuery(map[string]string{
		"query":       "is:unresolved",
		"environment": "",
		"statsPeriod": "24h",
	})
	// Map iteration order is non-deterministic, so check the substring shape.
	if !strings.HasPrefix(got, "?") {
		t.Errorf("missing leading ?: %q", got)
	}
	if !strings.Contains(got, "query=is%3Aunresolved") {
		t.Errorf("expected encoded query, got %q", got)
	}
	if strings.Contains(got, "environment=") {
		t.Errorf("empty value should be stripped, got %q", got)
	}
}

func TestBuildQuery_AllEmpty(t *testing.T) {
	got := BuildQuery(map[string]string{"a": "", "b": ""})
	if got != "" {
		t.Errorf("expected empty string when all values empty, got %q", got)
	}
}

func TestUpdateIssues_RepeatedIDQuery(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" {
			t.Errorf("expected PUT, got %s", r.Method)
		}
		// Sentry expects ?id=A&id=B (repeated keys) not ?id=A,B.
		// http.Request.URL.Query() already URL-decodes values, so a
		// well-escaped client should produce three distinct ids even when
		// one of them contains characters that would otherwise inject a
		// new query parameter.
		ids := r.URL.Query()["id"]
		if len(ids) != 3 {
			t.Errorf("expected 3 ?id= params, got %d (%v)", len(ids), ids)
		}
		body, _ := io.ReadAll(r.Body)
		var got IssueUpdate
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if got.Status != "resolved" {
			t.Errorf("status = %q, want resolved", got.Status)
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{}`)
	}))
	defer ts.Close()

	c := newTestClient(t, ts)
	if err := c.ResolveIssues(context.Background(), "", []string{"a", "b", "c"}); err != nil {
		t.Fatalf("ResolveIssues: %v", err)
	}
}

// TestUpdateIssues_EscapesMaliciousID confirms that an ID containing query
// metacharacters (& = #) is properly escaped, so it can't smuggle a new
// query parameter into the PUT — e.g. `1&status=resolved` must not arrive
// at Sentry as two separate ids plus an injected status.
func TestUpdateIssues_EscapesMaliciousID(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		ids := q["id"]
		if len(ids) != 1 {
			t.Errorf("expected exactly 1 ?id= param after escape, got %d (%v)", len(ids), ids)
		}
		if ids[0] != "1&status=resolved" {
			t.Errorf("id should round-trip with escape, got %q", ids[0])
		}
		// And we should NOT see an injected status= in the query.
		if got := q.Get("status"); got != "" {
			t.Errorf("status query param should be absent (only in body), got %q", got)
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{}`)
	}))
	defer ts.Close()

	c := newTestClient(t, ts)
	// `1&status=resolved` is the canonical attack: without escaping it
	// would split into id=1 + status=resolved at the server.
	if err := c.IgnoreIssues(context.Background(), "", []string{"1&status=resolved"}); err != nil {
		t.Fatalf("IgnoreIssues: %v", err)
	}
}

func TestProjectStatsPoint_UnmarshalJSON(t *testing.T) {
	var pts []ProjectStatsPoint
	if err := json.Unmarshal([]byte(`[[1700000000, 42], [1700003600, 17]]`), &pts); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(pts) != 2 {
		t.Fatalf("len = %d, want 2", len(pts))
	}
	if pts[0].Timestamp != 1700000000 || pts[0].Count != 42 {
		t.Errorf("point[0] = %+v", pts[0])
	}
}

// TestExtractErrorDetail probes both error envelope shapes Sentry returns —
// the single-detail form and the field-bag form. The latter happens on
// validation failures.
func TestExtractErrorDetail(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"empty", "", ""},
		{"single detail", `{"detail":"nope"}`, "nope"},
		{"field bag string", `{"slug":"slug is required"}`, "slug: slug is required"},
		{"field bag array", `{"name":["This field is required."]}`, "name: This field is required."},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := extractErrorDetail([]byte(tc.body))
			if got != tc.want {
				t.Errorf("extractErrorDetail = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestParseRetryWait_PrefersLonger checks we wait the *longer* of the
// Retry-After / X-Sentry-Rate-Limit-Reset values when both are advertised,
// so we don't hammer back in too early.
func TestParseRetryWait_PrefersLonger(t *testing.T) {
	resp := &http.Response{Header: http.Header{}}
	resp.Header.Set("Retry-After", "0.5")
	resp.Header.Set("X-Sentry-Rate-Limit-Reset", strconv.FormatInt(time.Now().Add(3*time.Second).Unix(), 10))
	wait := parseRetryWait(resp)
	if wait < time.Second {
		t.Errorf("expected wait >= 1s, got %v", wait)
	}
	if wait > 30*time.Second {
		t.Errorf("wait should be capped at 30s, got %v", wait)
	}
}

// TestValidateHost_BlocksSSRF exercises the SSRF guard added to NewClient.
// Without this guard, a hostile sentry.host config or SENTRY_HOST env var
// could make the CLI ship the auth token at internal endpoints like
// 169.254.169.254 (cloud metadata).
func TestValidateHost_BlocksSSRF(t *testing.T) {
	cases := []struct {
		host    string
		wantErr bool
	}{
		// Allowed
		{"sentry.io", false},
		{"acme.sentry.io", false},
		{"sentry.mycompany.com", false},

		// Blocked
		{"127.0.0.1", true},
		{"169.254.169.254", true},
		{"localhost", true},
		{"app.localhost", true},
		{"metadata.google.internal", true},
		{"sentry.io:8080", true},
		{"sentry.io/admin", true},
		{"evil@sentry.io", true},
		{"::1", true},
	}
	for _, c := range cases {
		c := c
		t.Run(c.host, func(t *testing.T) {
			t.Parallel()
			err := validateHost(c.host)
			gotErr := err != nil
			if gotErr != c.wantErr {
				t.Errorf("validateHost(%q) err=%v, wantErr=%v", c.host, err, c.wantErr)
			}
		})
	}
}

func TestNewClient_RejectsHostileHost(t *testing.T) {
	if _, err := NewClient("tok", "org", "169.254.169.254", false); err == nil {
		t.Errorf("expected error for IP host, got nil")
	}
}
