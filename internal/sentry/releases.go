package sentry

import (
	"context"
	"fmt"
)

// ListReleases returns recent releases for a project (newest first).
func (c *Client) ListReleases(ctx context.Context, orgSlug, projectSlug string) ([]Release, error) {
	org := c.resolveOrg(orgSlug)
	if org == "" || projectSlug == "" {
		return nil, fmt.Errorf("org and project slug are required")
	}
	_, body, err := c.Do(ctx, "GET", fmt.Sprintf("/projects/%s/%s/releases/", org, projectSlug), nil)
	if err != nil {
		return nil, err
	}
	var releases []Release
	if err := DecodeJSON(body, &releases); err != nil {
		return nil, err
	}
	return releases, nil
}

// GetRelease fetches a single release by version.
func (c *Client) GetRelease(ctx context.Context, orgSlug, projectSlug, version string) (*Release, error) {
	org := c.resolveOrg(orgSlug)
	if org == "" || projectSlug == "" || version == "" {
		return nil, fmt.Errorf("org, project slug, and version are required")
	}
	_, body, err := c.Do(ctx, "GET", fmt.Sprintf("/projects/%s/%s/releases/%s/", org, projectSlug, version), nil)
	if err != nil {
		return nil, err
	}
	var r Release
	if err := DecodeJSON(body, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// GetReleaseHealth returns crash-free session/user metrics for a release.
// Uses /organizations/{org}/sessions/ with the v2 sessions endpoint.
func (c *Client) GetReleaseHealth(ctx context.Context, orgSlug, projectSlug, version string) (*SessionsResponse, error) {
	org := c.resolveOrg(orgSlug)
	if org == "" {
		return nil, fmt.Errorf("org slug is required")
	}
	params := map[string]string{
		"field":       "sum(session)",
		"groupBy":     "session.status",
		"statsPeriod": "24h",
		"project":     projectSlug,
		"query":       fmt.Sprintf("release:%s", version),
	}
	_, body, err := c.Do(ctx, "GET", fmt.Sprintf("/organizations/%s/sessions/%s", org, BuildQuery(params)), nil)
	if err != nil {
		return nil, err
	}
	var r SessionsResponse
	if err := DecodeJSON(body, &r); err != nil {
		return nil, err
	}
	return &r, nil
}
