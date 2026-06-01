package sentry

import (
	"context"
	"time"
)

// GatherAccountStatus collects an at-a-glance snapshot for the conversation
// history. Errors are non-fatal — we degrade gracefully because the ask
// command should never fail just because one secondary fetch broke.
func GatherAccountStatus(ctx context.Context, c *Client, orgSlug string) (*AccountStatus, error) {
	status := &AccountStatus{
		Timestamp:        time.Now(),
		OrganizationSlug: orgSlug,
	}

	projects, err := c.ListProjects(ctx, orgSlug)
	if err == nil {
		status.ProjectCount = len(projects)
	}

	unresolved, _, err := c.ListIssues(ctx, orgSlug, IssueListOptions{
		Query:       "is:unresolved",
		StatsPeriod: "24h",
		Limit:       100,
	})
	if err == nil {
		status.UnresolvedCount = len(unresolved)
	}

	errors24h, _, err := c.ListIssues(ctx, orgSlug, IssueListOptions{
		Query:       "level:error",
		StatsPeriod: "24h",
		Limit:       100,
	})
	if err == nil {
		status.ErrorCount24h = len(errors24h)
	}

	return status, nil
}
