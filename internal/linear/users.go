package linear

import "context"

const queryUsers = `
query Users($first: Int!) {
  users(first: $first) {
    nodes {
      id
      name
      displayName
      email
      active
      avatarUrl
    }
  }
}`

func (c *Client) ListUsers(ctx context.Context) ([]User, error) {
	var out struct {
		Users struct {
			Nodes []User `json:"nodes"`
		} `json:"users"`
	}
	if err := c.Do(ctx, queryUsers, map[string]any{"first": 250}, &out); err != nil {
		return nil, err
	}
	return out.Users.Nodes, nil
}

// FindUserByDisplayName scans the user list for an exact match. Used by
// the assign command which takes a username. For large workspaces this is
// O(n) but n is bounded by the workspace's user count (typically <500).
func (c *Client) FindUserByDisplayName(ctx context.Context, displayName string) (*User, error) {
	users, err := c.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	for i, u := range users {
		if u.DisplayName == displayName || u.Email == displayName || u.Name == displayName {
			return &users[i], nil
		}
	}
	return nil, nil
}
