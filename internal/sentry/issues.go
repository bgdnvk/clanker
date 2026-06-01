package sentry

import (
	"context"
	"fmt"
	"strings"
)

// IssueListOptions controls /organizations/{org}/issues/. Query is Sentry's
// search-syntax string (e.g. `is:unresolved level:error environment:prod`)
// and is passed verbatim — we do no client-side parsing.
type IssueListOptions struct {
	Query       string
	Environment string
	StatsPeriod string // e.g. "24h", "14d"
	Project     string // project ID (numeric string) — multiple allowed via repeated params; we keep single for simplicity
	Sort        string // "new" | "priority" | "freq" | "user"
	Limit       int
	Cursor      string
}

// ListIssues fetches a page of issues. Pagination is exposed via the returned
// NextCursor — callers that want all pages can iterate.
func (c *Client) ListIssues(ctx context.Context, orgSlug string, opts IssueListOptions) ([]Issue, string, error) {
	org := c.resolveOrg(orgSlug)
	if org == "" {
		return nil, "", fmt.Errorf("org slug is required to list issues")
	}
	params := map[string]string{
		"query":       opts.Query,
		"environment": opts.Environment,
		"statsPeriod": opts.StatsPeriod,
		"project":     opts.Project,
		"sort":        opts.Sort,
		"cursor":      opts.Cursor,
	}
	if opts.Limit > 0 {
		params["limit"] = fmt.Sprintf("%d", opts.Limit)
	}
	resp, body, err := c.Do(ctx, "GET", fmt.Sprintf("/organizations/%s/issues/%s", org, BuildQuery(params)), nil)
	if err != nil {
		return nil, "", err
	}
	var issues []Issue
	if err := DecodeJSON(body, &issues); err != nil {
		return nil, "", err
	}
	return issues, ParseNextCursor(resp), nil
}

// GetIssue fetches a single issue by ID.
func (c *Client) GetIssue(ctx context.Context, issueID string) (*Issue, error) {
	if issueID == "" {
		return nil, fmt.Errorf("issue ID is required")
	}
	_, body, err := c.Do(ctx, "GET", fmt.Sprintf("/issues/%s/", issueID), nil)
	if err != nil {
		return nil, err
	}
	var issue Issue
	if err := DecodeJSON(body, &issue); err != nil {
		return nil, err
	}
	return &issue, nil
}

// GetIssueEvents returns events for a given issue, newest first. Limit caps
// the number returned (Sentry's per-page max is 100).
func (c *Client) GetIssueEvents(ctx context.Context, issueID string, limit int) ([]Event, error) {
	if issueID == "" {
		return nil, fmt.Errorf("issue ID is required")
	}
	params := map[string]string{}
	if limit > 0 {
		params["limit"] = fmt.Sprintf("%d", limit)
	}
	_, body, err := c.Do(ctx, "GET", fmt.Sprintf("/issues/%s/events/%s", issueID, BuildQuery(params)), nil)
	if err != nil {
		return nil, err
	}
	var events []Event
	if err := DecodeJSON(body, &events); err != nil {
		return nil, err
	}
	return events, nil
}

// IssueUpdate is the payload Sentry expects on PUT /organizations/{org}/issues/.
// Status is one of "resolved" | "unresolved" | "ignored"; AssignedTo is a
// username string (or "" to clear).
type IssueUpdate struct {
	Status     string `json:"status,omitempty"`
	AssignedTo string `json:"assignedTo,omitempty"`
}

// UpdateIssues bulk-mutates issues. IDs are passed as repeated `id=` query
// params; the body carries the new status/assignment.
func (c *Client) UpdateIssues(ctx context.Context, orgSlug string, ids []string, update IssueUpdate) error {
	org := c.resolveOrg(orgSlug)
	if org == "" {
		return fmt.Errorf("org slug is required")
	}
	if len(ids) == 0 {
		return fmt.Errorf("at least one issue ID is required")
	}
	// Sentry expects ?id=A&id=B&id=C — url.Values handles repeats. We can't
	// use BuildQuery here because that flattens to a single value per key.
	var qb strings.Builder
	qb.WriteByte('?')
	for i, id := range ids {
		if i > 0 {
			qb.WriteByte('&')
		}
		qb.WriteString("id=")
		qb.WriteString(id)
	}
	_, _, err := c.Do(ctx, "PUT", fmt.Sprintf("/organizations/%s/issues/%s", org, qb.String()), update)
	return err
}

// ResolveIssues is a convenience wrapper.
func (c *Client) ResolveIssues(ctx context.Context, orgSlug string, ids []string) error {
	return c.UpdateIssues(ctx, orgSlug, ids, IssueUpdate{Status: "resolved"})
}

// IgnoreIssues marks issues as ignored.
func (c *Client) IgnoreIssues(ctx context.Context, orgSlug string, ids []string) error {
	return c.UpdateIssues(ctx, orgSlug, ids, IssueUpdate{Status: "ignored"})
}

// AssignIssue assigns a single issue to a username.
func (c *Client) AssignIssue(ctx context.Context, orgSlug, issueID, assignee string) error {
	return c.UpdateIssues(ctx, orgSlug, []string{issueID}, IssueUpdate{AssignedTo: assignee})
}
