package linear

import (
	"context"
	"fmt"
)

const queryLabels = `
query Labels($filter: IssueLabelFilter, $first: Int!) {
  issueLabels(filter: $filter, first: $first) {
    nodes { id name color team { id } }
    pageInfo { hasNextPage endCursor }
  }
}`

const mutationCreateLabel = `
mutation CreateLabel($input: IssueLabelCreateInput!) {
  issueLabelCreate(input: $input) {
    success
    issueLabel { id name color team { id } }
  }
}`

// ListLabels returns labels, optionally filtered to a single team. Pass
// teamID="" for org-wide labels (rare — most labels are team-scoped).
func (c *Client) ListLabels(ctx context.Context, teamID string) ([]Label, error) {
	vars := map[string]any{"first": 100}
	if teamID != "" {
		vars["filter"] = map[string]any{"team": map[string]any{"id": map[string]any{"eq": teamID}}}
	}
	var out struct {
		IssueLabels struct {
			Nodes []struct {
				ID    string `json:"id"`
				Name  string `json:"name"`
				Color string `json:"color"`
				Team  *struct {
					ID string `json:"id"`
				} `json:"team,omitempty"`
			} `json:"nodes"`
		} `json:"issueLabels"`
	}
	if err := c.Do(ctx, queryLabels, vars, &out); err != nil {
		return nil, err
	}
	labels := make([]Label, len(out.IssueLabels.Nodes))
	for i, n := range out.IssueLabels.Nodes {
		labels[i] = Label{ID: n.ID, Name: n.Name, Color: n.Color}
		if n.Team != nil {
			labels[i].TeamID = n.Team.ID
		}
	}
	return labels, nil
}

// FindLabelByName returns the first label matching name (case-sensitive)
// within an optional team scope. Used by the annotation layer to look up
// `infra:<type>:<id>` labels without paginating the full set.
func (c *Client) FindLabelByName(ctx context.Context, teamID, name string) (*Label, error) {
	filter := map[string]any{"name": map[string]any{"eq": name}}
	if teamID != "" {
		filter["team"] = map[string]any{"id": map[string]any{"eq": teamID}}
	}
	var out struct {
		IssueLabels struct {
			Nodes []struct {
				ID    string `json:"id"`
				Name  string `json:"name"`
				Color string `json:"color"`
			} `json:"nodes"`
		} `json:"issueLabels"`
	}
	if err := c.Do(ctx, queryLabels, map[string]any{"first": 1, "filter": filter}, &out); err != nil {
		return nil, err
	}
	if len(out.IssueLabels.Nodes) == 0 {
		return nil, nil
	}
	n := out.IssueLabels.Nodes[0]
	return &Label{ID: n.ID, Name: n.Name, Color: n.Color, TeamID: teamID}, nil
}

// CreateLabel creates a team-scoped label. color is a hex string like "#5e6ad2".
func (c *Client) CreateLabel(ctx context.Context, teamID, name, color string) (*Label, error) {
	if teamID == "" || name == "" {
		return nil, fmt.Errorf("teamID and name are required")
	}
	input := map[string]any{"teamId": teamID, "name": name}
	if color != "" {
		input["color"] = color
	}
	var out struct {
		IssueLabelCreate struct {
			Success    bool `json:"success"`
			IssueLabel struct {
				ID    string `json:"id"`
				Name  string `json:"name"`
				Color string `json:"color"`
			} `json:"issueLabel"`
		} `json:"issueLabelCreate"`
	}
	if err := c.Do(ctx, mutationCreateLabel, map[string]any{"input": input}, &out); err != nil {
		return nil, err
	}
	if !out.IssueLabelCreate.Success {
		return nil, fmt.Errorf("issueLabelCreate returned success=false")
	}
	return &Label{
		ID:     out.IssueLabelCreate.IssueLabel.ID,
		Name:   out.IssueLabelCreate.IssueLabel.Name,
		Color:  out.IssueLabelCreate.IssueLabel.Color,
		TeamID: teamID,
	}, nil
}
