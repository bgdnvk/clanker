package linear

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newTestClient wires a Client at an httptest server. We swap the HTTP
// client's transport so it rewrites all outbound requests to the test
// target — keeps the production code path (Authorization header, payload
// shape) intact while letting handlers assert on what arrived.
func newTestClient(t *testing.T, ts *httptest.Server) *Client {
	t.Helper()
	c, err := NewClient("test-key", "test-workspace", "ENG", false)
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
	newReq, err := http.NewRequestWithContext(req.Context(), req.Method, rt.target, req.Body)
	if err != nil {
		return nil, err
	}
	newReq.Header = cloned.Header
	return http.DefaultTransport.RoundTrip(newReq)
}

func TestClientDo_HappyPath(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "test-key" {
			t.Errorf("Authorization should be raw key, got %q (NO 'Bearer ' prefix expected)", got)
		}
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if !strings.Contains(payload.Query, "teams(first") {
			t.Errorf("expected teams query, got %q", payload.Query)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":{"teams":{"nodes":[{"id":"t1","key":"ENG","name":"Engineering","description":"","createdAt":"2024-01-01T00:00:00Z"}],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}`)
	}))
	defer ts.Close()
	c := newTestClient(t, ts)
	teams, err := c.ListTeams(context.Background())
	if err != nil {
		t.Fatalf("ListTeams: %v", err)
	}
	if len(teams) != 1 || teams[0].Key != "ENG" {
		t.Errorf("unexpected teams: %+v", teams)
	}
}

// TestClientDo_AuthHeaderNoBearer confirms the #1 Linear footgun is closed:
// the Authorization header must NOT contain a "Bearer " prefix. Sending
// Bearer returns a confusing 400 from Linear.
func TestClientDo_AuthHeaderNoBearer(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("Authorization")
		if strings.HasPrefix(got, "Bearer ") {
			t.Errorf("Authorization carried Bearer prefix: %q — Linear rejects this", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":{"teams":{"nodes":[]}}}`)
	}))
	defer ts.Close()
	c := newTestClient(t, ts)
	if _, err := c.ListTeams(context.Background()); err != nil {
		t.Fatalf("ListTeams: %v", err)
	}
}

func TestClientDo_RateLimit_RetryAfter(t *testing.T) {
	var attempts atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"errors":[{"message":"rate limited"}]}`)
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"data":{"teams":{"nodes":[]}}}`)
	}))
	defer ts.Close()
	c := newTestClient(t, ts)
	if _, err := c.ListTeams(context.Background()); err != nil {
		t.Fatalf("ListTeams after retry: %v", err)
	}
	if attempts.Load() != 2 {
		t.Errorf("expected 2 attempts, got %d", attempts.Load())
	}
}

func TestClientDo_GraphQLErrors(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// GraphQL servers commonly return 200 with an `errors` envelope.
		// Decode must surface this as an APIError, not a silent empty.
		fmt.Fprint(w, `{"data":null,"errors":[{"message":"team not found","extensions":{"code":"NOT_FOUND"}}]}`)
	}))
	defer ts.Close()
	c := newTestClient(t, ts)
	_, err := c.ListTeams(context.Background())
	if err == nil {
		t.Fatal("expected APIError, got nil")
	}
	if !strings.Contains(err.Error(), "team not found") {
		t.Errorf("error should surface GraphQL message: %v", err)
	}
}

// TestFilterToGraphQL confirms IssueFilter produces the exact GraphQL input
// shape Linear's schema expects. Each clause is wrapped in the eq/in
// operator object — getting this wrong returns a confusing 400.
func TestFilterToGraphQL(t *testing.T) {
	f := IssueFilter{
		StateType:  "started",
		TeamKey:    "ENG",
		LabelName:  "infra:lambda:arn:foo",
		AssigneeID: "user-1",
	}
	g := f.toGraphQL()
	state := g["state"].(map[string]any)["type"].(map[string]any)
	if state["eq"] != "started" {
		t.Errorf("state.type.eq = %v, want started", state["eq"])
	}
	team := g["team"].(map[string]any)["key"].(map[string]any)
	if team["eq"] != "ENG" {
		t.Errorf("team.key.eq = %v, want ENG", team["eq"])
	}
	labels := g["labels"].(map[string]any)["name"].(map[string]any)
	if labels["eq"] != "infra:lambda:arn:foo" {
		t.Errorf("labels.name.eq mismatch: %v", labels["eq"])
	}
	assignee := g["assignee"].(map[string]any)["id"].(map[string]any)
	if assignee["eq"] != "user-1" {
		t.Errorf("assignee.id.eq = %v", assignee["eq"])
	}
}

// TestUpdateIssue_PartialPatch confirms the typed pointers in
// UpdateIssueInput serialise correctly — only non-nil fields land in the
// GraphQL variables, so callers can ship a one-field patch without nuking
// the rest of the issue.
func TestUpdateIssue_PartialPatch(t *testing.T) {
	got := make(chan map[string]any, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			Variables map[string]any `json:"variables"`
		}
		_ = json.Unmarshal(body, &payload)
		got <- payload.Variables
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"data":{"issueUpdate":{"success":true,"issue":{"id":"i1","identifier":"ENG-1","title":"t","priority":0,"createdAt":"2024-01-01T00:00:00Z","updatedAt":"2024-01-01T00:00:00Z","labels":{"nodes":[]}}}}}`)
	}))
	defer ts.Close()
	c := newTestClient(t, ts)
	state := "state-123"
	if _, err := c.UpdateIssue(context.Background(), "issue-1", UpdateIssueInput{StateID: &state}); err != nil {
		t.Fatalf("UpdateIssue: %v", err)
	}
	vars := <-got
	input := vars["input"].(map[string]any)
	if _, ok := input["title"]; ok {
		t.Errorf("title should be omitted when nil")
	}
	if input["stateId"] != "state-123" {
		t.Errorf("stateId = %v, want state-123", input["stateId"])
	}
}
