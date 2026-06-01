package sentry

import (
	"encoding/json"
	"time"
)

// Sentry response shapes use camelCase JSON and ISO-8601 timestamp strings.
// IDs are always returned as strings even when numeric — never unmarshal to int.
// See https://docs.sentry.io/api/ for the canonical reference.

type Organization struct {
	ID          string    `json:"id"`
	Slug        string    `json:"slug"`
	Name        string    `json:"name"`
	DateCreated time.Time `json:"dateCreated"`
}

type Project struct {
	ID           string        `json:"id"`
	Slug         string        `json:"slug"`
	Name         string        `json:"name"`
	Platform     string        `json:"platform"`
	DateCreated  time.Time     `json:"dateCreated"`
	IsBookmarked bool          `json:"isBookmarked"`
	Organization *Organization `json:"organization,omitempty"`
}

type Issue struct {
	ID            string          `json:"id"`
	ShortID       string          `json:"shortId"`
	Title         string          `json:"title"`
	Culprit       string          `json:"culprit"`
	Permalink     string          `json:"permalink"`
	Logger        string          `json:"logger"`
	Level         string          `json:"level"`
	Status        string          `json:"status"`
	StatusDetails json.RawMessage `json:"statusDetails"`
	IsPublic      bool            `json:"isPublic"`
	Platform      string          `json:"platform"`
	Project       *Project        `json:"project,omitempty"`
	Type          string          `json:"type"`
	Metadata      map[string]any  `json:"metadata"`
	NumComments   int             `json:"numComments"`
	AssignedTo    json.RawMessage `json:"assignedTo"`
	IsBookmarked  bool            `json:"isBookmarked"`
	IsSubscribed  bool            `json:"isSubscribed"`
	HasSeen       bool            `json:"hasSeen"`
	FirstSeen     time.Time       `json:"firstSeen"`
	LastSeen      time.Time       `json:"lastSeen"`
	Count         string          `json:"count"`
	UserCount     int             `json:"userCount"`
}

type Tag struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Event entries are a polymorphic array — each entry has a `type` field
// (exception, breadcrumbs, request, message, ...) and a `data` payload whose
// shape depends on the type. Callers that need to introspect should dispatch
// on Type and unmarshal Data with the appropriate concrete struct.
type EventEntry struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

type Event struct {
	ID           string                     `json:"id"`
	EventID      string                     `json:"eventID"`
	GroupID      string                     `json:"groupID"`
	ProjectID    string                     `json:"projectID"`
	Title        string                     `json:"title"`
	Message      string                     `json:"message"`
	Platform     string                     `json:"platform"`
	Type         string                     `json:"type"`
	DateCreated  time.Time                  `json:"dateCreated"`
	DateReceived time.Time                  `json:"dateReceived"`
	Tags         []Tag                      `json:"tags"`
	User         json.RawMessage            `json:"user"`
	Entries      []EventEntry               `json:"entries"`
	Contexts     map[string]json.RawMessage `json:"contexts"`
}

type Release struct {
	Version      string     `json:"version"`
	ShortVersion string     `json:"shortVersion"`
	Ref          string     `json:"ref"`
	URL          string     `json:"url"`
	DateCreated  time.Time  `json:"dateCreated"`
	DateReleased *time.Time `json:"dateReleased"`
	NewGroups    int        `json:"newGroups"`
	Projects     []struct {
		Slug string `json:"slug"`
		Name string `json:"name"`
	} `json:"projects"`
}

// SessionGroup carries release-health rollups returned by
// /organizations/{org}/sessions/. The shape is a tuple-array under `groups`;
// caller selects fields via the `field=` query (e.g. `sum(session)`).
type SessionGroup struct {
	By     map[string]string    `json:"by"`
	Totals map[string]float64   `json:"totals"`
	Series map[string][]float64 `json:"series"`
}

type SessionsResponse struct {
	Start     time.Time      `json:"start"`
	End       time.Time      `json:"end"`
	Intervals []time.Time    `json:"intervals"`
	Groups    []SessionGroup `json:"groups"`
}

// IssueAlertRule is a Sentry issue alert rule (the legacy /rules/ endpoint).
// MetricAlertRule (the newer /alert-rules/ endpoint) has a different shape.
type IssueAlertRule struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Environment string            `json:"environment"`
	Frequency   int               `json:"frequency"`
	ActionMatch string            `json:"actionMatch"`
	FilterMatch string            `json:"filterMatch"`
	Conditions  []json.RawMessage `json:"conditions"`
	Filters     []json.RawMessage `json:"filters"`
	Actions     []json.RawMessage `json:"actions"`
	DateCreated time.Time         `json:"dateCreated"`
	CreatedBy   json.RawMessage   `json:"createdBy"`
}

type MetricAlertRule struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Environment string            `json:"environment"`
	DataSet     string            `json:"dataset"`
	Query       string            `json:"query"`
	Aggregate   string            `json:"aggregate"`
	TimeWindow  float64           `json:"timeWindow"`
	Threshold   float64           `json:"threshold"`
	Triggers    []json.RawMessage `json:"triggers"`
	Projects    []string          `json:"projects"`
	DateCreated time.Time         `json:"dateCreated"`
}

type Monitor struct {
	Slug    string          `json:"slug"`
	Name    string          `json:"name"`
	Status  string          `json:"status"`
	Type    string          `json:"type"`
	IsMuted bool            `json:"isMuted"`
	Config  json.RawMessage `json:"config"`
	Project struct {
		Slug string `json:"slug"`
		Name string `json:"name"`
	} `json:"project"`
	DateCreated time.Time `json:"dateCreated"`
}

type MonitorCheckin struct {
	ID          string          `json:"id"`
	Status      string          `json:"status"`
	Duration    *float64        `json:"duration"`
	DateCreated time.Time       `json:"dateCreated"`
	Attachment  json.RawMessage `json:"attachment"`
}

type Team struct {
	ID          string    `json:"id"`
	Slug        string    `json:"slug"`
	Name        string    `json:"name"`
	IsMember    bool      `json:"isMember"`
	MemberCount int       `json:"memberCount"`
	DateCreated time.Time `json:"dateCreated"`
}

type Member struct {
	ID    string          `json:"id"`
	Email string          `json:"email"`
	Name  string          `json:"name"`
	Role  string          `json:"role"`
	User  json.RawMessage `json:"user"`
}

// ProjectStatsPoint is one bucket of `/projects/{org}/{project}/stats/`.
// The endpoint returns tuples of [unix_seconds, count].
type ProjectStatsPoint struct {
	Timestamp int64
	Count     int64
}

// UnmarshalJSON handles the tuple shape `[<unix>, <count>]`.
func (p *ProjectStatsPoint) UnmarshalJSON(data []byte) error {
	var tuple [2]json.Number
	if err := json.Unmarshal(data, &tuple); err != nil {
		return err
	}
	ts, err := tuple[0].Int64()
	if err != nil {
		return err
	}
	count, err := tuple[1].Int64()
	if err != nil {
		return err
	}
	p.Timestamp = ts
	p.Count = count
	return nil
}

// MarshalJSON keeps round-trip parity with the upstream tuple format so
// tests that re-encode a fixture and diff against the input don't drift.
func (p ProjectStatsPoint) MarshalJSON() ([]byte, error) {
	return json.Marshal([2]int64{p.Timestamp, p.Count})
}

// AccountStatus is the at-a-glance snapshot the ask command stashes in
// conversation history so follow-up questions can be answered with
// orientation context (project count, recent error volume) without
// re-fetching everything.
type AccountStatus struct {
	Timestamp        time.Time `json:"timestamp"`
	OrganizationSlug string    `json:"organization_slug"`
	ProjectCount     int       `json:"project_count"`
	UnresolvedCount  int       `json:"unresolved_count"`
	ErrorCount24h    int       `json:"error_count_24h"`
}
