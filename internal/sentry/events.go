package sentry

import (
	"context"
	"fmt"
)

// ListProjectEvents returns recent events for a project (newest first).
// limit caps the page size (max 100 per Sentry's API).
func (c *Client) ListProjectEvents(ctx context.Context, orgSlug, projectSlug string, limit int) ([]Event, error) {
	org := c.resolveOrg(orgSlug)
	if org == "" || projectSlug == "" {
		return nil, fmt.Errorf("org and project slug are required")
	}
	params := map[string]string{}
	if limit > 0 {
		params["limit"] = fmt.Sprintf("%d", limit)
	}
	_, body, err := c.Do(ctx, "GET", fmt.Sprintf("/projects/%s/%s/events/%s", org, projectSlug, BuildQuery(params)), nil)
	if err != nil {
		return nil, err
	}
	var events []Event
	if err := DecodeJSON(body, &events); err != nil {
		return nil, err
	}
	return events, nil
}

// GetEvent fetches a single event by eventID within a project. eventID is the
// 32-char hex string (not the numeric primary key).
func (c *Client) GetEvent(ctx context.Context, orgSlug, projectSlug, eventID string) (*Event, error) {
	org := c.resolveOrg(orgSlug)
	if org == "" || projectSlug == "" || eventID == "" {
		return nil, fmt.Errorf("org, project slug, and event ID are required")
	}
	_, body, err := c.Do(ctx, "GET", fmt.Sprintf("/projects/%s/%s/events/%s/", org, projectSlug, eventID), nil)
	if err != nil {
		return nil, err
	}
	var ev Event
	if err := DecodeJSON(body, &ev); err != nil {
		return nil, err
	}
	return &ev, nil
}
