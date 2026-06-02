package linear

import (
	"encoding/json"
	"time"
)

// Linear API objects map almost 1:1 to GraphQL types.
//
// IMPORTANT — every Linear object has BOTH an `id` (UUID, e.g.
// `2b0e3c00-9c4f-4b6a-9b4f-...`) and an `identifier` (e.g. `ENG-123`).
// Operators see Identifiers everywhere; mutations require the UUID.
// Do not conflate them. `IssueByIdentifier` is a separate query.

type Workspace struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	URLKey    string    `json:"urlKey"`
	CreatedAt time.Time `json:"createdAt"`
	UserCount int       `json:"userCount"`
}

type Team struct {
	ID          string    `json:"id"`
	Key         string    `json:"key"` // short prefix used in identifiers e.g. "ENG"
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"createdAt"`
}

// WorkflowState is one column in a team's kanban (e.g. "In Progress").
// Type is one of "triage", "backlog", "unstarted", "started", "completed",
// "cancelled". Mutations target state by ID; the canonical workflow position
// is per-team so two teams' "In Progress" states are distinct objects.
type WorkflowState struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Color  string `json:"color"`
	TeamID string `json:"-"`
}

type User struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Email       string `json:"email"`
	Active      bool   `json:"active"`
	AvatarURL   string `json:"avatarUrl"`
}

type Label struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Color  string `json:"color"`
	TeamID string `json:"-"`
}

// Project is a delivery effort that groups issues across one or more cycles.
// Note: collides with "project" in Notion's vocabulary — be explicit in UI.
type Project struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	State       string     `json:"state"` // backlog, planned, started, paused, completed, canceled
	Progress    float64    `json:"progress"`
	StartDate   *time.Time `json:"startDate"`
	TargetDate  *time.Time `json:"targetDate"`
	CreatedAt   time.Time  `json:"createdAt"`
	URL         string     `json:"url"`
	LeadID      string     `json:"-"`
}

// Cycle is a time-boxed iteration (a sprint) belonging to a single team.
type Cycle struct {
	ID          string     `json:"id"`
	Number      int        `json:"number"`
	Name        string     `json:"name"`
	StartsAt    time.Time  `json:"startsAt"`
	EndsAt      time.Time  `json:"endsAt"`
	CompletedAt *time.Time `json:"completedAt"`
	Progress    float64    `json:"progress"`
	TeamID      string     `json:"-"`
}

// Issue is the central work unit. ShortID is what Linear calls the
// "identifier" — operator-facing (e.g. "ENG-123"). ID is the UUID required
// for any mutation.
type Issue struct {
	ID          string     `json:"id"`
	Identifier  string     `json:"identifier"` // human-facing, e.g. "ENG-42"
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Priority    int        `json:"priority"` // 0 (none) | 1 (urgent) | 2 (high) | 3 (medium) | 4 (low)
	Estimate    float64    `json:"estimate"`
	URL         string     `json:"url"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
	StartedAt   *time.Time `json:"startedAt"`
	CompletedAt *time.Time `json:"completedAt"`
	CanceledAt  *time.Time `json:"canceledAt"`
	DueDate     *time.Time `json:"dueDate"`

	// Nested via GraphQL — populated by the query, not separate calls.
	State    *WorkflowState `json:"state,omitempty"`
	Team     *Team          `json:"team,omitempty"`
	Project  *Project       `json:"project,omitempty"`
	Cycle    *Cycle         `json:"cycle,omitempty"`
	Assignee *User          `json:"assignee,omitempty"`
	Creator  *User          `json:"creator,omitempty"`
	Labels   struct {
		Nodes []Label `json:"nodes"`
	} `json:"labels"`
}

// Comment is a top-level comment on an issue. Threads (replies) are
// represented via Parent — for MVP we only render top-level comments and
// flatten replies into the same view.
type Comment struct {
	ID        string    `json:"id"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	URL       string    `json:"url"`
	User      *User     `json:"user,omitempty"`
	IssueID   string    `json:"-"`
}

// Document is a free-form doc attached to a project or team. Body is
// Linear's rich-text JSON which we expose as-is for now — rendering it
// nicely in the desktop UI is PR4 territory (parallels Notion blocks).
type Document struct {
	ID        string          `json:"id"`
	Title     string          `json:"title"`
	URL       string          `json:"url"`
	Content   json.RawMessage `json:"content"`
	CreatedAt time.Time       `json:"createdAt"`
	UpdatedAt time.Time       `json:"updatedAt"`
	ProjectID string          `json:"-"`
}

// PageInfo is the Relay-style cursor for Linear's connection types.
// We always pass `first` and read `endCursor`; backwards pagination
// is not used.
type PageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

// AccountStatus is the at-a-glance snapshot the ask command stashes in
// conversation history so follow-ups can be answered without re-fetching.
type AccountStatus struct {
	Timestamp          time.Time `json:"timestamp"`
	WorkspaceID        string    `json:"workspace_id"`
	WorkspaceName      string    `json:"workspace_name,omitempty"`
	TeamCount          int       `json:"team_count"`
	StartedIssueCount  int       `json:"started_issue_count"`
	ActiveProjectCount int       `json:"active_project_count"`
}
