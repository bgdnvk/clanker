package linear

import (
	"context"
	"fmt"
	"regexp"
)

// identifierPattern matches Linear's human identifier like "ENG-42". Linear
// slugs are 2-5 uppercase letters; we allow longer for safety against future
// changes but disallow the lowercase that would shadow a UUID's 8-4-4-4-12 form.
var identifierPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*-\d+$`)

// ResolveIssueID accepts either a UUID or a human identifier (ENG-42) and
// returns the UUID. Linear's `issue(id:)` query accepts either form so this
// is cheap; we make it explicit because mutation endpoints (`issueUpdate`,
// `commentCreate`) only accept the UUID and silently 4xx on identifiers.
func (c *Client) ResolveIssueID(ctx context.Context, idOrIdentifier string) (string, error) {
	if idOrIdentifier == "" {
		return "", fmt.Errorf("issue id required")
	}
	if !identifierPattern.MatchString(idOrIdentifier) {
		return idOrIdentifier, nil // assume already a UUID
	}
	issue, err := c.GetIssue(ctx, idOrIdentifier)
	if err != nil {
		return "", fmt.Errorf("resolve %s to UUID: %w", idOrIdentifier, err)
	}
	return issue.ID, nil
}

// IssueFilter mirrors a subset of Linear's GraphQL IssueFilter input.
// Empty fields are omitted from the marshalled GraphQL variables. The
// shape is intentionally narrow — we expose the filters the ask flow
// and the kanban actually use; more can be added when needed.
type IssueFilter struct {
	StateType       string // "started" | "completed" | "cancelled" | "unstarted" | "backlog" | "triage"
	TeamID          string // UUID
	TeamKey         string // e.g. "ENG" — convenience for the CLI
	ProjectID       string
	CycleID         string
	AssigneeID      string   // UUID
	AssigneeIsMe    bool     // filter to issues assigned to the API key's viewer
	LabelName       string   // exact label name (used by annotation lookup)
	LabelIDs        []string // multi-label match
	Priority        int      // 0..4; 0 means "any"
	IncludeArchived bool
}

// toGraphQL turns the filter into Linear's IssueFilter shape. Empty fields
// stay absent so we don't accidentally constrain to e.g. teamId=null.
func (f IssueFilter) toGraphQL() map[string]any {
	out := map[string]any{}
	if f.StateType != "" {
		out["state"] = map[string]any{"type": map[string]any{"eq": f.StateType}}
	}
	if f.TeamID != "" {
		out["team"] = map[string]any{"id": map[string]any{"eq": f.TeamID}}
	} else if f.TeamKey != "" {
		out["team"] = map[string]any{"key": map[string]any{"eq": f.TeamKey}}
	}
	if f.ProjectID != "" {
		out["project"] = map[string]any{"id": map[string]any{"eq": f.ProjectID}}
	}
	if f.CycleID != "" {
		out["cycle"] = map[string]any{"id": map[string]any{"eq": f.CycleID}}
	}
	if f.AssigneeID != "" {
		out["assignee"] = map[string]any{"id": map[string]any{"eq": f.AssigneeID}}
	} else if f.AssigneeIsMe {
		out["assignee"] = map[string]any{"isMe": map[string]any{"eq": true}}
	}
	if f.LabelName != "" {
		out["labels"] = map[string]any{"name": map[string]any{"eq": f.LabelName}}
	} else if len(f.LabelIDs) > 0 {
		out["labels"] = map[string]any{"id": map[string]any{"in": f.LabelIDs}}
	}
	if f.Priority > 0 {
		out["priority"] = map[string]any{"eq": f.Priority}
	}
	return out
}

const issueSelection = `
  id
  identifier
  title
  description
  priority
  estimate
  url
  createdAt
  updatedAt
  startedAt
  completedAt
  canceledAt
  dueDate
  state { id name type color }
  team { id key name }
  project { id name state progress }
  cycle { id number name startsAt endsAt }
  assignee { id name displayName email avatarUrl }
  creator { id name displayName email }
  labels(first: 20) { nodes { id name color } }
`

const queryIssues = `
query Issues($filter: IssueFilter, $first: Int!, $after: String) {
  issues(filter: $filter, first: $first, after: $after, orderBy: updatedAt) {
    nodes { ` + issueSelection + ` }
    pageInfo { hasNextPage endCursor }
  }
}`

const queryIssueByID = `
query Issue($id: String!) {
  issue(id: $id) { ` + issueSelection + ` }
}`

const queryIssueComments = `
query IssueComments($id: String!, $first: Int!) {
  issue(id: $id) {
    comments(first: $first, orderBy: createdAt) {
      nodes {
        id
        body
        url
        createdAt
        updatedAt
        user { id name displayName email }
      }
    }
  }
}`

// ListIssues returns a page of issues. cursor may be empty for the first
// page; pass the returned PageInfo.EndCursor on subsequent calls.
func (c *Client) ListIssues(ctx context.Context, filter IssueFilter, first int, after string) ([]Issue, PageInfo, error) {
	if first <= 0 {
		first = 50
	}
	if first > 250 {
		first = 250
	}
	vars := map[string]any{"first": first}
	if gq := filter.toGraphQL(); len(gq) > 0 {
		vars["filter"] = gq
	}
	if after != "" {
		vars["after"] = after
	}
	var out struct {
		Issues struct {
			Nodes    []Issue  `json:"nodes"`
			PageInfo PageInfo `json:"pageInfo"`
		} `json:"issues"`
	}
	if err := c.Do(ctx, queryIssues, vars, &out); err != nil {
		return nil, PageInfo{}, err
	}
	return out.Issues.Nodes, out.Issues.PageInfo, nil
}

// GetIssue fetches a single issue by ID (UUID).
func (c *Client) GetIssue(ctx context.Context, id string) (*Issue, error) {
	if id == "" {
		return nil, fmt.Errorf("issue ID required")
	}
	var out struct {
		Issue *Issue `json:"issue"`
	}
	if err := c.Do(ctx, queryIssueByID, map[string]any{"id": id}, &out); err != nil {
		return nil, err
	}
	if out.Issue == nil {
		return nil, fmt.Errorf("issue %s not found", id)
	}
	return out.Issue, nil
}

// GetIssueComments returns top-level comments for an issue in creation order
// (oldest first — matches Linear's GraphQL `orderBy: createdAt` ascending
// default and lets the prompt builder render the thread linearly).
func (c *Client) GetIssueComments(ctx context.Context, issueID string, limit int) ([]Comment, error) {
	if limit <= 0 {
		limit = 50
	}
	var out struct {
		Issue struct {
			Comments struct {
				Nodes []Comment `json:"nodes"`
			} `json:"comments"`
		} `json:"issue"`
	}
	if err := c.Do(ctx, queryIssueComments, map[string]any{"id": issueID, "first": limit}, &out); err != nil {
		return nil, err
	}
	for i := range out.Issue.Comments.Nodes {
		out.Issue.Comments.Nodes[i].IssueID = issueID
	}
	return out.Issue.Comments.Nodes, nil
}

const mutationCreateIssue = `
mutation CreateIssue($input: IssueCreateInput!) {
  issueCreate(input: $input) {
    success
    issue { ` + issueSelection + ` }
  }
}`

const mutationUpdateIssue = `
mutation UpdateIssue($id: String!, $input: IssueUpdateInput!) {
  issueUpdate(id: $id, input: $input) {
    success
    issue { ` + issueSelection + ` }
  }
}`

const mutationCreateComment = `
mutation CreateComment($input: CommentCreateInput!) {
  commentCreate(input: $input) {
    success
    comment { id body url createdAt }
  }
}`

// CreateIssueInput is the strongly-typed subset of IssueCreateInput we
// expose. Linear's full input is larger; we add fields here as the agent
// surface needs them.
type CreateIssueInput struct {
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	TeamID      string   `json:"teamId"`
	ProjectID   string   `json:"projectId,omitempty"`
	CycleID     string   `json:"cycleId,omitempty"`
	StateID     string   `json:"stateId,omitempty"`
	AssigneeID  string   `json:"assigneeId,omitempty"`
	Priority    int      `json:"priority,omitempty"`
	Estimate    float64  `json:"estimate,omitempty"`
	LabelIDs    []string `json:"labelIds,omitempty"`
	DueDate     string   `json:"dueDate,omitempty"` // YYYY-MM-DD
}

// UpdateIssueInput is the patch for issueUpdate. Empty fields are omitted,
// so callers can pass a tiny struct to update one property.
type UpdateIssueInput struct {
	Title       *string  `json:"title,omitempty"`
	Description *string  `json:"description,omitempty"`
	StateID     *string  `json:"stateId,omitempty"`
	AssigneeID  *string  `json:"assigneeId,omitempty"`
	ProjectID   *string  `json:"projectId,omitempty"`
	CycleID     *string  `json:"cycleId,omitempty"`
	Priority    *int     `json:"priority,omitempty"`
	Estimate    *float64 `json:"estimate,omitempty"`
	LabelIDs    []string `json:"labelIds,omitempty"`
	DueDate     *string  `json:"dueDate,omitempty"`
}

func (c *Client) CreateIssue(ctx context.Context, input CreateIssueInput) (*Issue, error) {
	if input.TeamID == "" || input.Title == "" {
		return nil, fmt.Errorf("title and teamId are required")
	}
	var out struct {
		IssueCreate struct {
			Success bool   `json:"success"`
			Issue   *Issue `json:"issue"`
		} `json:"issueCreate"`
	}
	if err := c.Do(ctx, mutationCreateIssue, map[string]any{"input": input}, &out); err != nil {
		return nil, err
	}
	if !out.IssueCreate.Success || out.IssueCreate.Issue == nil {
		return nil, fmt.Errorf("issueCreate failed")
	}
	return out.IssueCreate.Issue, nil
}

func (c *Client) UpdateIssue(ctx context.Context, id string, input UpdateIssueInput) (*Issue, error) {
	if id == "" {
		return nil, fmt.Errorf("issue id required")
	}
	var out struct {
		IssueUpdate struct {
			Success bool   `json:"success"`
			Issue   *Issue `json:"issue"`
		} `json:"issueUpdate"`
	}
	if err := c.Do(ctx, mutationUpdateIssue, map[string]any{"id": id, "input": input}, &out); err != nil {
		return nil, err
	}
	if !out.IssueUpdate.Success || out.IssueUpdate.Issue == nil {
		return nil, fmt.Errorf("issueUpdate failed")
	}
	return out.IssueUpdate.Issue, nil
}

func (c *Client) AddComment(ctx context.Context, issueID, body string) (*Comment, error) {
	if issueID == "" || body == "" {
		return nil, fmt.Errorf("issueID and body required")
	}
	input := map[string]any{"issueId": issueID, "body": body}
	var out struct {
		CommentCreate struct {
			Success bool    `json:"success"`
			Comment Comment `json:"comment"`
		} `json:"commentCreate"`
	}
	if err := c.Do(ctx, mutationCreateComment, map[string]any{"input": input}, &out); err != nil {
		return nil, err
	}
	if !out.CommentCreate.Success {
		return nil, fmt.Errorf("commentCreate failed")
	}
	out.CommentCreate.Comment.IssueID = issueID
	return &out.CommentCreate.Comment, nil
}
