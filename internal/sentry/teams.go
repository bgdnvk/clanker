package sentry

import (
	"context"
	"fmt"
)

// ListTeams returns teams in an org.
func (c *Client) ListTeams(ctx context.Context, orgSlug string) ([]Team, error) {
	org := c.resolveOrg(orgSlug)
	if org == "" {
		return nil, fmt.Errorf("org slug is required")
	}
	_, body, err := c.Do(ctx, "GET", fmt.Sprintf("/organizations/%s/teams/", org), nil)
	if err != nil {
		return nil, err
	}
	var teams []Team
	if err := DecodeJSON(body, &teams); err != nil {
		return nil, err
	}
	return teams, nil
}

// ListMembers returns members in an org.
func (c *Client) ListMembers(ctx context.Context, orgSlug string) ([]Member, error) {
	org := c.resolveOrg(orgSlug)
	if org == "" {
		return nil, fmt.Errorf("org slug is required")
	}
	_, body, err := c.Do(ctx, "GET", fmt.Sprintf("/organizations/%s/members/", org), nil)
	if err != nil {
		return nil, err
	}
	var members []Member
	if err := DecodeJSON(body, &members); err != nil {
		return nil, err
	}
	return members, nil
}
