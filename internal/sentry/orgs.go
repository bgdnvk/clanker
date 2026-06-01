package sentry

import (
	"context"
	"fmt"
)

// ListOrganizations returns the auth-token holder's accessible orgs.
// Paginates internally and accumulates all pages.
func (c *Client) ListOrganizations(ctx context.Context) ([]Organization, error) {
	var all []Organization
	path := "/organizations/"
	for {
		resp, body, err := c.Do(ctx, "GET", path, nil)
		if err != nil {
			return nil, err
		}
		var page []Organization
		if err := DecodeJSON(body, &page); err != nil {
			return nil, err
		}
		all = append(all, page...)
		next := ParseNextCursor(resp)
		if next == "" {
			break
		}
		path = fmt.Sprintf("/organizations/%s", BuildQuery(map[string]string{"cursor": next}))
	}
	return all, nil
}

// GetOrganization fetches a single org by slug.
func (c *Client) GetOrganization(ctx context.Context, slug string) (*Organization, error) {
	_, body, err := c.Do(ctx, "GET", fmt.Sprintf("/organizations/%s/", slug), nil)
	if err != nil {
		return nil, err
	}
	var org Organization
	if err := DecodeJSON(body, &org); err != nil {
		return nil, err
	}
	return &org, nil
}
