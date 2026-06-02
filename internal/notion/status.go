package notion

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

// GatherAccountStatus collects an at-a-glance snapshot for the conversation
// history. Two Notion calls in parallel: `/users/me` for the workspace
// name, and an unfiltered `/search` whose results we bucket by object
// type to derive page + database counts in a single round-trip.
//
// AccessiblePages stays at zero when the user hasn't shared anything with
// the integration — surfacing the share gotcha at the prompt level.
func GatherAccountStatus(ctx context.Context, c *Client) (*AccountStatus, error) {
	status := &AccountStatus{
		Timestamp: time.Now(),
	}

	var (
		wg            sync.WaitGroup
		workspaceName string
		pageCount     int
		dbCount       int
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		if _, ws, err := c.Me(ctx); err == nil && ws != nil {
			workspaceName = ws.WorkspaceName
		}
	}()
	go func() {
		defer wg.Done()
		results, _, _, err := c.Search(ctx, SearchOptions{PageSize: 100})
		if err != nil {
			return
		}
		for _, raw := range results {
			var probe struct {
				Object string `json:"object"`
			}
			if err := json.Unmarshal(raw, &probe); err != nil {
				continue
			}
			switch probe.Object {
			case "page":
				pageCount++
			case "database":
				dbCount++
			}
		}
	}()
	wg.Wait()

	status.WorkspaceName = workspaceName
	status.AccessiblePages = pageCount
	status.DatabaseCount = dbCount
	return status, nil
}
