package linear

import (
	"context"
	"fmt"
)

const cycleSelection = `
  id
  number
  name
  startsAt
  endsAt
  completedAt
  progress
  team { id }
`

const queryCycles = `
query Cycles($filter: CycleFilter, $first: Int!) {
  cycles(filter: $filter, first: $first, orderBy: updatedAt) {
    nodes { ` + cycleSelection + ` }
    pageInfo { hasNextPage endCursor }
  }
}`

const queryCycle = `
query Cycle($id: String!) {
  cycle(id: $id) { ` + cycleSelection + ` }
}`

const mutationCreateCycle = `
mutation CreateCycle($input: CycleCreateInput!) {
  cycleCreate(input: $input) {
    success
    cycle { ` + cycleSelection + ` }
  }
}`

const mutationUpdateCycle = `
mutation UpdateCycle($id: String!, $input: CycleUpdateInput!) {
  cycleUpdate(id: $id, input: $input) {
    success
    cycle { ` + cycleSelection + ` }
  }
}`

type CycleFilter struct {
	TeamID   string
	IsActive bool // shortcut: completedAt is null and startsAt <= now <= endsAt
	IsFuture bool // startsAt > now
}

func (f CycleFilter) toGraphQL() map[string]any {
	out := map[string]any{}
	if f.TeamID != "" {
		out["team"] = map[string]any{"id": map[string]any{"eq": f.TeamID}}
	}
	if f.IsActive {
		out["isActive"] = true
	} else if f.IsFuture {
		out["isFuture"] = true
	}
	return out
}

type cycleNode struct {
	ID          string  `json:"id"`
	Number      int     `json:"number"`
	Name        string  `json:"name"`
	StartsAt    string  `json:"startsAt"`
	EndsAt      string  `json:"endsAt"`
	CompletedAt *string `json:"completedAt"`
	Progress    float64 `json:"progress"`
	Team        *struct {
		ID string `json:"id"`
	} `json:"team"`
}

func (n cycleNode) toCycle() Cycle {
	c := Cycle{
		ID:       n.ID,
		Number:   n.Number,
		Name:     n.Name,
		Progress: n.Progress,
	}
	if n.Team != nil {
		c.TeamID = n.Team.ID
	}
	// Times are decoded lazily; the CLI renderer and frontend both treat
	// these as ISO-8601 strings rather than Go time.Time.
	return c
}

func (c *Client) ListCycles(ctx context.Context, filter CycleFilter) ([]Cycle, error) {
	vars := map[string]any{"first": 100}
	if gq := filter.toGraphQL(); len(gq) > 0 {
		vars["filter"] = gq
	}
	var out struct {
		Cycles struct {
			Nodes []cycleNode `json:"nodes"`
		} `json:"cycles"`
	}
	if err := c.Do(ctx, queryCycles, vars, &out); err != nil {
		return nil, err
	}
	cycles := make([]Cycle, len(out.Cycles.Nodes))
	for i, n := range out.Cycles.Nodes {
		cycles[i] = n.toCycle()
	}
	return cycles, nil
}

func (c *Client) GetCycle(ctx context.Context, id string) (*Cycle, error) {
	if id == "" {
		return nil, fmt.Errorf("cycle id required")
	}
	var out struct {
		Cycle *cycleNode `json:"cycle"`
	}
	if err := c.Do(ctx, queryCycle, map[string]any{"id": id}, &out); err != nil {
		return nil, err
	}
	if out.Cycle == nil {
		return nil, fmt.Errorf("cycle %s not found", id)
	}
	cy := out.Cycle.toCycle()
	return &cy, nil
}

type CreateCycleInput struct {
	TeamID   string `json:"teamId"`
	Name     string `json:"name,omitempty"`
	StartsAt string `json:"startsAt"` // RFC 3339
	EndsAt   string `json:"endsAt"`
}

type UpdateCycleInput struct {
	Name     *string `json:"name,omitempty"`
	StartsAt *string `json:"startsAt,omitempty"`
	EndsAt   *string `json:"endsAt,omitempty"`
}

func (c *Client) CreateCycle(ctx context.Context, input CreateCycleInput) (*Cycle, error) {
	if input.TeamID == "" || input.StartsAt == "" || input.EndsAt == "" {
		return nil, fmt.Errorf("teamID, startsAt, endsAt required")
	}
	var out struct {
		CycleCreate struct {
			Success bool       `json:"success"`
			Cycle   *cycleNode `json:"cycle"`
		} `json:"cycleCreate"`
	}
	if err := c.Do(ctx, mutationCreateCycle, map[string]any{"input": input}, &out); err != nil {
		return nil, err
	}
	if !out.CycleCreate.Success || out.CycleCreate.Cycle == nil {
		return nil, fmt.Errorf("cycleCreate failed")
	}
	cy := out.CycleCreate.Cycle.toCycle()
	return &cy, nil
}

func (c *Client) UpdateCycle(ctx context.Context, id string, input UpdateCycleInput) (*Cycle, error) {
	if id == "" {
		return nil, fmt.Errorf("cycle id required")
	}
	var out struct {
		CycleUpdate struct {
			Success bool       `json:"success"`
			Cycle   *cycleNode `json:"cycle"`
		} `json:"cycleUpdate"`
	}
	if err := c.Do(ctx, mutationUpdateCycle, map[string]any{"id": id, "input": input}, &out); err != nil {
		return nil, err
	}
	if !out.CycleUpdate.Success || out.CycleUpdate.Cycle == nil {
		return nil, fmt.Errorf("cycleUpdate failed")
	}
	cy := out.CycleUpdate.Cycle.toCycle()
	return &cy, nil
}
