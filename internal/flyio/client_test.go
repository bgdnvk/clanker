package flyio

import (
	"strings"
	"testing"
)

func TestNewClientRequiresToken(t *testing.T) {
	if _, err := NewClient("", "", false); err == nil {
		t.Fatal("expected error for empty token")
	}
	c, err := NewClient("token-xyz", "personal", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.GetAPIToken() != "token-xyz" {
		t.Errorf("token = %q, want token-xyz", c.GetAPIToken())
	}
	if c.GetOrgSlug() != "personal" {
		t.Errorf("orgSlug = %q, want personal", c.GetOrgSlug())
	}
}

func TestNewClientWithCredentials(t *testing.T) {
	if _, err := NewClientWithCredentials(nil, false); err == nil {
		t.Fatal("expected error for nil credentials")
	}
	if _, err := NewClientWithCredentials(&BackendFlyioCredentials{}, false); err == nil {
		t.Fatal("expected error for empty token in credentials")
	}
	creds := &BackendFlyioCredentials{APIToken: "tok", OrgSlug: "my-org"}
	c, err := NewClientWithCredentials(creds, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.GetOrgSlug() != "my-org" {
		t.Errorf("orgSlug = %q, want my-org", c.GetOrgSlug())
	}
}

func TestRawToggle(t *testing.T) {
	c, _ := NewClient("tok", "", false)
	if c.Raw() {
		t.Error("Raw() should default to false")
	}
	c.SetRaw(true)
	if !c.Raw() {
		t.Error("Raw() should be true after SetRaw(true)")
	}
}

func TestWithOrg_AppendsScope(t *testing.T) {
	c, _ := NewClient("tok", "my-org", false)
	got := c.withOrg("/apps")
	if !strings.Contains(got, "org_slug=my-org") {
		t.Errorf("withOrg(/apps) = %q, want to contain org_slug=my-org", got)
	}
}

func TestWithOrg_NoScopeWhenOrgEmpty(t *testing.T) {
	c, _ := NewClient("tok", "", false)
	got := c.withOrg("/apps")
	if got != "/apps" {
		t.Errorf("withOrg(/apps) with empty org = %q, want unchanged", got)
	}
}

func TestWithOrg_DoesNotDoubleScope(t *testing.T) {
	c, _ := NewClient("tok", "my-org", false)
	got := c.withOrg("/apps?org_slug=other")
	if strings.Count(got, "org_slug=") != 1 {
		t.Errorf("withOrg should not append a second org_slug, got %q", got)
	}
}

func TestWithOrg_PreservesQueryString(t *testing.T) {
	c, _ := NewClient("tok", "my-org", false)
	got := c.withOrg("/apps?limit=10")
	if !strings.Contains(got, "limit=10") || !strings.Contains(got, "org_slug=my-org") {
		t.Errorf("withOrg should preserve query and append org, got %q", got)
	}
	if !strings.Contains(got, "?limit=10&org_slug=") {
		t.Errorf("withOrg should use & separator when ? present, got %q", got)
	}
}

func TestCheckAPIError_NoError(t *testing.T) {
	cases := []string{
		"",
		"not json",
		`{"apps":[]}`,
		`{"machines":[{"id":"abc"}]}`,
	}
	for _, body := range cases {
		if err := checkAPIError(body); err != nil {
			t.Errorf("checkAPIError(%q) = %v, want nil", body, err)
		}
	}
}

func TestCheckAPIError_TopLevelError(t *testing.T) {
	body := `{"error":"app not found"}`
	err := checkAPIError(body)
	if err == nil {
		t.Fatal("expected error for top-level error envelope")
	}
	if !strings.Contains(err.Error(), "app not found") {
		t.Errorf("error message missing detail: %v", err)
	}
}

func TestCheckAPIError_ErrorsArray(t *testing.T) {
	body := `{"errors":[{"message":"unauthorized"}]}`
	err := checkAPIError(body)
	if err == nil {
		t.Fatal("expected error for errors-array envelope")
	}
	if !strings.Contains(err.Error(), "unauthorized") {
		t.Errorf("error message missing detail: %v", err)
	}
}

func TestCheckGraphQLError(t *testing.T) {
	body := `{"errors":[{"message":"viewer not found"}]}`
	err := checkGraphQLError(body)
	if err == nil {
		t.Fatal("expected error for graphql errors envelope")
	}
	if !strings.Contains(err.Error(), "viewer not found") {
		t.Errorf("graphql error missing detail: %v", err)
	}

	// No errors -> nil.
	if err := checkGraphQLError(`{"data":{"viewer":{"id":"u1"}}}`); err != nil {
		t.Errorf("checkGraphQLError of clean response = %v, want nil", err)
	}
}

func TestIsRetryableError(t *testing.T) {
	cases := map[string]bool{
		"rate_limited":          true,
		"too_many_requests":     true,
		"internal_server_error": true,
		"bad_gateway":           true,
		"service_unavailable":   true,
		"gateway_timeout":       true,
		"connection refused":    true,
		"timed out":             true,
		"app_not_found":         false,
		"forbidden":             false,
		"unauthorized":          false,
		"":                      false,
	}
	for input, want := range cases {
		if got := isRetryableError(input); got != want {
			t.Errorf("isRetryableError(%q) = %v, want %v", input, got, want)
		}
	}
}

func TestErrorHint(t *testing.T) {
	cases := map[string]string{
		"unauthorized":       "FLY_API_TOKEN",
		"forbidden":          "scope",
		"app_not_found":      "check name and org scope",
		"rate limit":         "rate limited",
		"region_unavailable": "capacity",
		"quota_exceeded":     "quota",
		"app_in_use":         "already taken",
		"unknown stuff":      "",
	}
	for input, wantContains := range cases {
		got := errorHint(input)
		if wantContains == "" {
			if got != "" {
				t.Errorf("errorHint(%q) = %q, want empty", input, got)
			}
			continue
		}
		if !strings.Contains(got, wantContains) {
			t.Errorf("errorHint(%q) = %q, want to contain %q", input, got, wantContains)
		}
	}
}

func TestMentionsMachineKeywords(t *testing.T) {
	cases := map[string]bool{
		"how many machines are running":    true,
		"start a vm in iad":                true,
		"list my replicas":                 true,
		"deploy the app":                   false,
		"what postgres clusters do i have": false,
	}
	for q, want := range cases {
		if got := mentionsMachineKeywords(q); got != want {
			t.Errorf("mentionsMachineKeywords(%q) = %v, want %v", q, got, want)
		}
	}
}

func TestFlyctlInstalled_DoesNotPanic(t *testing.T) {
	// Just exercise the function — result depends on the test host.
	_ = FlyctlInstalled()
}
