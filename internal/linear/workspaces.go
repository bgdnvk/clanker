package linear

import "context"

const queryWorkspace = `
query Workspace {
  viewer {
    id
    name
    displayName
    email
    organization {
      id
      name
      urlKey
      createdAt
      userCount
    }
  }
}`

// GetWorkspace returns the workspace the API key belongs to plus the viewer
// (the user the key was issued for). Linear calls the workspace
// `viewer.organization` — there's no direct "current workspace" query.
func (c *Client) GetWorkspace(ctx context.Context) (*Workspace, *User, error) {
	var out struct {
		Viewer struct {
			ID           string    `json:"id"`
			Name         string    `json:"name"`
			DisplayName  string    `json:"displayName"`
			Email        string    `json:"email"`
			Organization Workspace `json:"organization"`
		} `json:"viewer"`
	}
	if err := c.Do(ctx, queryWorkspace, nil, &out); err != nil {
		return nil, nil, err
	}
	return &out.Viewer.Organization, &User{
		ID:          out.Viewer.ID,
		Name:        out.Viewer.Name,
		DisplayName: out.Viewer.DisplayName,
		Email:       out.Viewer.Email,
		Active:      true,
	}, nil
}
