package vercel

// Project represents a Vercel project.
// Fields are a subset of the /v9/projects response — enough for listing and
// drawer display, not the full schema.
type Project struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	AccountID   string `json:"accountId"`
	Framework   string `json:"framework,omitempty"`
	CreatedAt   int64  `json:"createdAt,omitempty"`
	UpdatedAt   int64  `json:"updatedAt,omitempty"`
	NodeVersion string `json:"nodeVersion,omitempty"`
	Link        *struct {
		Type             string `json:"type"`
		Repo             string `json:"repo,omitempty"`
		RepoID           int64  `json:"repoId,omitempty"`
		ProductionBranch string `json:"productionBranch,omitempty"`
	} `json:"link,omitempty"`
	LatestDeployments []Deployment `json:"latestDeployments,omitempty"`
	Targets           map[string]struct {
		ID    string   `json:"id"`
		URL   string   `json:"url"`
		Alias []string `json:"alias,omitempty"`
	} `json:"targets,omitempty"`
}

// Deployment represents a Vercel deployment.
type Deployment struct {
	UID        string `json:"uid"`
	Name       string `json:"name,omitempty"`
	URL        string `json:"url,omitempty"`
	State      string `json:"state,omitempty"`      // READY, BUILDING, ERROR, CANCELED, QUEUED
	ReadyState string `json:"readyState,omitempty"` // legacy alias used by /v6
	Target     string `json:"target,omitempty"`     // "production" | "" (preview)
	Type       string `json:"type,omitempty"`       // LAMBDAS
	ProjectID  string `json:"projectId,omitempty"`
	Created    int64  `json:"created,omitempty"`
	Ready      int64  `json:"ready,omitempty"`
	Creator    *struct {
		UID      string `json:"uid"`
		Username string `json:"username"`
		Email    string `json:"email,omitempty"`
	} `json:"creator,omitempty"`
}

// Domain represents a custom domain.
type Domain struct {
	Name        string   `json:"name"`
	ProjectID   string   `json:"projectId,omitempty"`
	Verified    bool     `json:"verified"`
	CreatedAt   int64    `json:"createdAt,omitempty"`
	UpdatedAt   int64    `json:"updatedAt,omitempty"`
	Nameservers []string `json:"nameservers,omitempty"`
}

// EnvVar represents a project environment variable.
// The `value` field is only returned when explicitly requested and when the
// caller's token has `read:env` scope; otherwise the UI must treat keys as
// opaque and not persist them.
type EnvVar struct {
	ID     string   `json:"id"`
	Key    string   `json:"key"`
	Value  string   `json:"value,omitempty"`
	Type   string   `json:"type,omitempty"`   // plain, encrypted, system, secret
	Target []string `json:"target,omitempty"` // production, preview, development
}

// Team represents a Vercel team (org).
type Team struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// User represents the authenticated Vercel user.
type User struct {
	UID      string `json:"uid"`
	ID       string `json:"id,omitempty"`
	Username string `json:"username"`
	Email    string `json:"email,omitempty"`
	Name     string `json:"name,omitempty"`
}

// UsageSummary summarizes recent consumption for cost estimation and analytics.
type UsageSummary struct {
	Bandwidth                 int64   `json:"bandwidth,omitempty"`
	FunctionInvocations       int64   `json:"functionInvocations,omitempty"`
	EdgeMiddlewareInvocations int64   `json:"edgeMiddlewareInvocations,omitempty"`
	BuildMinutes              float64 `json:"buildMinutes,omitempty"`
	ImageOptimizations        int64   `json:"imageOptimizations,omitempty"`
	Period                    string  `json:"period,omitempty"`
}

// --- Storage products (phase 1 read-only) ---

// KVDatabase represents a Vercel KV (Upstash Redis) store.
type KVDatabase struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Status    string `json:"status,omitempty"`
	CreatedAt int64  `json:"createdAt,omitempty"`
}

// BlobStore represents a Vercel Blob store.
type BlobStore struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Status    string `json:"status,omitempty"`
	CreatedAt int64  `json:"createdAt,omitempty"`
}

// PostgresDatabase represents a Vercel Postgres (Neon) instance.
type PostgresDatabase struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Status    string `json:"status,omitempty"`
	CreatedAt int64  `json:"createdAt,omitempty"`
}

// EdgeConfig represents a Vercel Edge Config store.
type EdgeConfig struct {
	ID        string `json:"id"`
	Slug      string `json:"slug,omitempty"`
	CreatedAt int64  `json:"createdAt,omitempty"`
	UpdatedAt int64  `json:"updatedAt,omitempty"`
}
