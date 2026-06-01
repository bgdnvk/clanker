package sentry

import (
	"context"
	"fmt"
)

// ListProjects returns all projects in the client's org. orgSlug overrides
// the client's default when non-empty.
func (c *Client) ListProjects(ctx context.Context, orgSlug string) ([]Project, error) {
	org := c.resolveOrg(orgSlug)
	if org == "" {
		return nil, fmt.Errorf("org slug is required to list projects")
	}
	var all []Project
	path := fmt.Sprintf("/organizations/%s/projects/", org)
	for {
		resp, body, err := c.Do(ctx, "GET", path, nil)
		if err != nil {
			return nil, err
		}
		var page []Project
		if err := DecodeJSON(body, &page); err != nil {
			return nil, err
		}
		all = append(all, page...)
		next := ParseNextCursor(resp)
		if next == "" {
			break
		}
		path = fmt.Sprintf("/organizations/%s/projects/%s", org, BuildQuery(map[string]string{"cursor": next}))
	}
	return all, nil
}

// GetProject fetches a single project.
func (c *Client) GetProject(ctx context.Context, orgSlug, projectSlug string) (*Project, error) {
	org := c.resolveOrg(orgSlug)
	if org == "" || projectSlug == "" {
		return nil, fmt.Errorf("org and project slug are required")
	}
	_, body, err := c.Do(ctx, "GET", fmt.Sprintf("/projects/%s/%s/", org, projectSlug), nil)
	if err != nil {
		return nil, err
	}
	var p Project
	if err := DecodeJSON(body, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// GetProjectStats returns event-volume buckets for a project. `stat` is
// typically "received" or "rejected"; `resolution` is "10s" | "1h" | "1d".
func (c *Client) GetProjectStats(ctx context.Context, orgSlug, projectSlug, stat, resolution string) ([]ProjectStatsPoint, error) {
	org := c.resolveOrg(orgSlug)
	if org == "" || projectSlug == "" {
		return nil, fmt.Errorf("org and project slug are required")
	}
	q := BuildQuery(map[string]string{
		"stat":       stat,
		"resolution": resolution,
	})
	_, body, err := c.Do(ctx, "GET", fmt.Sprintf("/projects/%s/%s/stats/%s", org, projectSlug, q), nil)
	if err != nil {
		return nil, err
	}
	var points []ProjectStatsPoint
	if err := DecodeJSON(body, &points); err != nil {
		return nil, err
	}
	return points, nil
}

func (c *Client) resolveOrg(override string) string {
	if override != "" {
		return override
	}
	return c.orgSlug
}
