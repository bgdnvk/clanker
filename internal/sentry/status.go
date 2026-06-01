package sentry

import (
	"context"
	"time"

	"golang.org/x/sync/errgroup"
)

// GatherAccountStatus collects an at-a-glance snapshot for the conversation
// history. The three Sentry calls run concurrently — `ask` cold-start
// latency is dominated by these network round-trips and they're independent.
// Errors are non-fatal: we want a partial snapshot rather than blocking the
// ask command on a single flaky endpoint.
func GatherAccountStatus(ctx context.Context, c *Client, orgSlug string) (*AccountStatus, error) {
	status := &AccountStatus{
		Timestamp:        time.Now(),
		OrganizationSlug: orgSlug,
	}

	g, gctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		if projects, err := c.ListProjects(gctx, orgSlug); err == nil {
			status.ProjectCount = len(projects)
		}
		return nil
	})
	g.Go(func() error {
		unresolved, _, err := c.ListIssues(gctx, orgSlug, IssueListOptions{
			Query:       "is:unresolved",
			StatsPeriod: "24h",
			Limit:       100,
		})
		if err == nil {
			status.UnresolvedCount = len(unresolved)
		}
		return nil
	})
	g.Go(func() error {
		errs, _, err := c.ListIssues(gctx, orgSlug, IssueListOptions{
			Query:       "level:error",
			StatsPeriod: "24h",
			Limit:       100,
		})
		if err == nil {
			status.ErrorCount24h = len(errs)
		}
		return nil
	})

	_ = g.Wait() // every goroutine swallows its own error; Wait can't fail
	return status, nil
}
