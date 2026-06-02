package linear

import (
	"context"
	"fmt"
)

const projectSelection = `
  id
  name
  description
  state
  progress
  startDate
  targetDate
  createdAt
  url
  lead { id }
`

const queryProjects = `
query Projects($filter: ProjectFilter, $first: Int!, $after: String) {
  projects(filter: $filter, first: $first, after: $after, orderBy: updatedAt) {
    nodes { ` + projectSelection + ` }
    pageInfo { hasNextPage endCursor }
  }
}`

const queryProject = `
query Project($id: String!) {
  project(id: $id) { ` + projectSelection + ` }
}`

const mutationCreateProject = `
mutation CreateProject($input: ProjectCreateInput!) {
  projectCreate(input: $input) {
    success
    project { ` + projectSelection + ` }
  }
}`

const mutationUpdateProject = `
mutation UpdateProject($id: String!, $input: ProjectUpdateInput!) {
  projectUpdate(id: $id, input: $input) {
    success
    project { ` + projectSelection + ` }
  }
}`

// ProjectFilter exposes the subset of Linear's ProjectFilter we use today.
type ProjectFilter struct {
	State  string // "backlog" | "planned" | "started" | "paused" | "completed" | "canceled"
	TeamID string
}

func (f ProjectFilter) toGraphQL() map[string]any {
	out := map[string]any{}
	if f.State != "" {
		out["state"] = map[string]any{"eq": f.State}
	}
	if f.TeamID != "" {
		// Projects belong to one OR more teams since Linear's 2024 redesign;
		// `accessibleTeams` is the right filter for "projects this team
		// participates in".
		out["accessibleTeams"] = map[string]any{"some": map[string]any{"id": map[string]any{"eq": f.TeamID}}}
	}
	return out
}

type projectNode struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	State       string  `json:"state"`
	Progress    float64 `json:"progress"`
	StartDate   *string `json:"startDate"`
	TargetDate  *string `json:"targetDate"`
	CreatedAt   string  `json:"createdAt"`
	URL         string  `json:"url"`
	Lead        *struct {
		ID string `json:"id"`
	} `json:"lead"`
}

func (n projectNode) toProject() Project {
	p := Project{
		ID:          n.ID,
		Name:        n.Name,
		Description: n.Description,
		State:       n.State,
		Progress:    n.Progress,
		URL:         n.URL,
	}
	if n.Lead != nil {
		p.LeadID = n.Lead.ID
	}
	// We accept Linear's date strings as-is and decode lazily; if the field
	// is RFC 3339 the JSON unmarshal in the consumer can re-parse. Keeping
	// Project's StartDate / TargetDate as *time.Time means we re-marshal
	// here.
	return p
}

func (c *Client) ListProjects(ctx context.Context, filter ProjectFilter, first int, after string) ([]Project, PageInfo, error) {
	if first <= 0 {
		first = 50
	}
	vars := map[string]any{"first": first}
	if gq := filter.toGraphQL(); len(gq) > 0 {
		vars["filter"] = gq
	}
	if after != "" {
		vars["after"] = after
	}
	var out struct {
		Projects struct {
			Nodes    []projectNode `json:"nodes"`
			PageInfo PageInfo      `json:"pageInfo"`
		} `json:"projects"`
	}
	if err := c.Do(ctx, queryProjects, vars, &out); err != nil {
		return nil, PageInfo{}, err
	}
	projects := make([]Project, len(out.Projects.Nodes))
	for i, n := range out.Projects.Nodes {
		projects[i] = n.toProject()
	}
	return projects, out.Projects.PageInfo, nil
}

func (c *Client) GetProject(ctx context.Context, id string) (*Project, error) {
	if id == "" {
		return nil, fmt.Errorf("project id required")
	}
	var out struct {
		Project *projectNode `json:"project"`
	}
	if err := c.Do(ctx, queryProject, map[string]any{"id": id}, &out); err != nil {
		return nil, err
	}
	if out.Project == nil {
		return nil, fmt.Errorf("project %s not found", id)
	}
	p := out.Project.toProject()
	return &p, nil
}

type CreateProjectInput struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	TeamIDs     []string `json:"teamIds"`
	LeadID      string   `json:"leadId,omitempty"`
	State       string   `json:"state,omitempty"`
	StartDate   string   `json:"startDate,omitempty"`
	TargetDate  string   `json:"targetDate,omitempty"`
}

type UpdateProjectInput struct {
	Name        *string  `json:"name,omitempty"`
	Description *string  `json:"description,omitempty"`
	State       *string  `json:"state,omitempty"`
	LeadID      *string  `json:"leadId,omitempty"`
	StartDate   *string  `json:"startDate,omitempty"`
	TargetDate  *string  `json:"targetDate,omitempty"`
	TeamIDs     []string `json:"teamIds,omitempty"`
}

func (c *Client) CreateProject(ctx context.Context, input CreateProjectInput) (*Project, error) {
	if input.Name == "" || len(input.TeamIDs) == 0 {
		return nil, fmt.Errorf("name and at least one teamID required")
	}
	var out struct {
		ProjectCreate struct {
			Success bool         `json:"success"`
			Project *projectNode `json:"project"`
		} `json:"projectCreate"`
	}
	if err := c.Do(ctx, mutationCreateProject, map[string]any{"input": input}, &out); err != nil {
		return nil, err
	}
	if !out.ProjectCreate.Success || out.ProjectCreate.Project == nil {
		return nil, fmt.Errorf("projectCreate failed")
	}
	p := out.ProjectCreate.Project.toProject()
	return &p, nil
}

func (c *Client) UpdateProject(ctx context.Context, id string, input UpdateProjectInput) (*Project, error) {
	if id == "" {
		return nil, fmt.Errorf("project id required")
	}
	var out struct {
		ProjectUpdate struct {
			Success bool         `json:"success"`
			Project *projectNode `json:"project"`
		} `json:"projectUpdate"`
	}
	if err := c.Do(ctx, mutationUpdateProject, map[string]any{"id": id, "input": input}, &out); err != nil {
		return nil, err
	}
	if !out.ProjectUpdate.Success || out.ProjectUpdate.Project == nil {
		return nil, fmt.Errorf("projectUpdate failed")
	}
	p := out.ProjectUpdate.Project.toProject()
	return &p, nil
}
