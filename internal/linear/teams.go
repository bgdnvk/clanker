package linear

import "context"

const queryTeams = `
query Teams($first: Int!) {
  teams(first: $first) {
    nodes {
      id
      key
      name
      description
      createdAt
    }
    pageInfo { hasNextPage endCursor }
  }
}`

const queryTeam = `
query Team($id: String!) {
  team(id: $id) {
    id
    key
    name
    description
    createdAt
    states {
      nodes { id name type color }
    }
  }
}`

func (c *Client) ListTeams(ctx context.Context) ([]Team, error) {
	var out struct {
		Teams struct {
			Nodes    []Team   `json:"nodes"`
			PageInfo PageInfo `json:"pageInfo"`
		} `json:"teams"`
	}
	if err := c.Do(ctx, queryTeams, map[string]any{"first": 100}, &out); err != nil {
		return nil, err
	}
	return out.Teams.Nodes, nil
}

// GetTeam returns a team with its workflow states inlined so the kanban
// renderer doesn't need a second round-trip per team.
func (c *Client) GetTeam(ctx context.Context, idOrKey string) (*Team, []WorkflowState, error) {
	var out struct {
		Team struct {
			Team
			States struct {
				Nodes []WorkflowState `json:"nodes"`
			} `json:"states"`
		} `json:"team"`
	}
	if err := c.Do(ctx, queryTeam, map[string]any{"id": idOrKey}, &out); err != nil {
		return nil, nil, err
	}
	states := out.Team.States.Nodes
	for i := range states {
		states[i].TeamID = out.Team.ID
	}
	return &out.Team.Team, states, nil
}
