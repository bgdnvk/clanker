package notion

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

// Me returns the bot user behind the integration token. The bot envelope
// carries `workspace_name` which is the only way to label the workspace
// — Notion has no `GET /workspace` endpoint.
func (c *Client) Me(ctx context.Context) (*User, *Workspace, error) {
	var u User
	if err := c.Do(ctx, "GET", "/users/me", nil, &u); err != nil {
		return nil, nil, err
	}
	ws := &Workspace{BotID: u.ID}
	if u.Bot != nil {
		ws.WorkspaceName = u.Bot.WorkspaceName
	}
	return &u, ws, nil
}

// ListUsers paginates through every user the integration can see.
// pageSize must be 1..100; 0 → 100. The integration scope determines
// the result set — workspaces with SSO often hide non-bot users from
// internal integrations.
func (c *Client) ListUsers(ctx context.Context, pageSize int) ([]User, error) {
	if pageSize <= 0 {
		pageSize = 100
	}
	pageSize = min(pageSize, 100)
	var out []User
	cursor := ""
	for {
		path := fmt.Sprintf("/users?page_size=%d", pageSize)
		if cursor != "" {
			path += "&start_cursor=" + url.QueryEscape(cursor)
		}
		var resp PaginatedResponse
		if err := c.Do(ctx, "GET", path, nil, &resp); err != nil {
			return nil, err
		}
		for _, raw := range resp.Results {
			var u User
			if err := json.Unmarshal(raw, &u); err != nil {
				return nil, fmt.Errorf("decode user: %w", err)
			}
			out = append(out, u)
		}
		if !resp.HasMore || resp.NextCursor == "" {
			break
		}
		cursor = resp.NextCursor
	}
	return out, nil
}

// GetUser fetches a single user by ID.
func (c *Client) GetUser(ctx context.Context, id string) (*User, error) {
	var u User
	if err := c.Do(ctx, "GET", "/users/"+url.PathEscape(id), nil, &u); err != nil {
		return nil, err
	}
	return &u, nil
}
