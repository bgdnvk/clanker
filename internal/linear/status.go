package linear

import (
	"context"
	"time"

	"golang.org/x/sync/errgroup"
)

// GatherAccountStatus collects an at-a-glance snapshot for the conversation
// history. The four queries run concurrently — ask cold-start latency is
// dominated by these round-trips and they're independent. Errors are
// non-fatal: a partial snapshot beats blocking the ask command on a single
// flaky endpoint.
func GatherAccountStatus(ctx context.Context, c *Client, workspaceID string) (*AccountStatus, error) {
	status := &AccountStatus{
		Timestamp:   time.Now(),
		WorkspaceID: workspaceID,
	}

	g, gctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		ws, _, err := c.GetWorkspace(gctx)
		if err == nil && ws != nil {
			status.WorkspaceName = ws.Name
		}
		return nil
	})
	g.Go(func() error {
		teams, err := c.ListTeams(gctx)
		if err == nil {
			status.TeamCount = len(teams)
		}
		return nil
	})
	g.Go(func() error {
		issues, _, err := c.ListIssues(gctx, IssueFilter{StateType: "started"}, 100, "")
		if err == nil {
			status.StartedIssueCount = len(issues)
		}
		return nil
	})
	g.Go(func() error {
		projects, _, err := c.ListProjects(gctx, ProjectFilter{State: "started"}, 100, "")
		if err == nil {
			status.ActiveProjectCount = len(projects)
		}
		return nil
	})

	_ = g.Wait()
	return status, nil
}
