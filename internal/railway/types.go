package railway

// User represents the authenticated Railway user (from the `me` query).
type User struct {
	ID    string `json:"id"`
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
}

// Workspace represents a Railway workspace / team. v1 public API has limited
// workspace surface so we keep this minimal.
type Workspace struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	Slug string `json:"slug,omitempty"`
}

// Project represents a Railway project. Projects hold environments, services,
// and plugins.
type Project struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	Description  string        `json:"description,omitempty"`
	TeamID       string        `json:"teamId,omitempty"`
	CreatedAt    string        `json:"createdAt,omitempty"`
	UpdatedAt    string        `json:"updatedAt,omitempty"`
	Environments []Environment `json:"environments,omitempty"`
	Services     []Service     `json:"services,omitempty"`
	Plugins      []Plugin      `json:"plugins,omitempty"`
}

// Environment represents a Railway environment (e.g. production, staging).
type Environment struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	ProjectID string `json:"projectId,omitempty"`
	CreatedAt string `json:"createdAt,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

// Service represents a Railway service (container/app) inside a project.
type Service struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	ProjectID string `json:"projectId,omitempty"`
	CreatedAt string `json:"createdAt,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

// Deployment represents a Railway deployment for a given service+environment.
type Deployment struct {
	ID            string `json:"id"`
	Status        string `json:"status,omitempty"` // SUCCESS, FAILED, BUILDING, DEPLOYING, REMOVED, CRASHED, INITIALIZING, QUEUED, SKIPPED
	StaticURL     string `json:"staticUrl,omitempty"`
	URL           string `json:"url,omitempty"`
	ServiceID     string `json:"serviceId,omitempty"`
	EnvironmentID string `json:"environmentId,omitempty"`
	ProjectID     string `json:"projectId,omitempty"`
	CreatedAt     string `json:"createdAt,omitempty"`
	UpdatedAt     string `json:"updatedAt,omitempty"`
	CanRedeploy   bool   `json:"canRedeploy,omitempty"`
	CanRollback   bool   `json:"canRollback,omitempty"`
	Meta          struct {
		CommitHash    string `json:"commitHash,omitempty"`
		CommitMessage string `json:"commitMessage,omitempty"`
		Branch        string `json:"branch,omitempty"`
	} `json:"meta,omitempty"`
}

// Domain represents either a Railway-managed service domain (xxx.up.railway.app)
// or a user-configured custom domain. IsCustom distinguishes the two.
type Domain struct {
	ID            string `json:"id"`
	Domain        string `json:"domain"`
	IsCustom      bool   `json:"isCustom"`
	ServiceID     string `json:"serviceId,omitempty"`
	EnvironmentID string `json:"environmentId,omitempty"`
	ProjectID     string `json:"projectId,omitempty"`
	Status        string `json:"status,omitempty"`
	TargetPort    int    `json:"targetPort,omitempty"`
	CreatedAt     string `json:"createdAt,omitempty"`
}

// Variable represents a Railway service environment variable.
// The Value field is only returned when the caller explicitly requests raw
// values; listings typically return keys only.
type Variable struct {
	Name          string `json:"name"`
	Value         string `json:"value,omitempty"`
	ServiceID     string `json:"serviceId,omitempty"`
	EnvironmentID string `json:"environmentId,omitempty"`
}

// Volume represents a persistent volume attached to a Railway service.
type Volume struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	ProjectID string `json:"projectId,omitempty"`
	ServiceID string `json:"serviceId,omitempty"`
	MountPath string `json:"mountPath,omitempty"`
	SizeMB    int64  `json:"sizeMB,omitempty"`
	CreatedAt string `json:"createdAt,omitempty"`
}

// Plugin represents a managed plugin resource inside a Railway project
// (e.g. Railway-managed Postgres, Redis, MySQL).
type Plugin struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	ProjectID string `json:"projectId,omitempty"`
	Status    string `json:"status,omitempty"`
	CreatedAt string `json:"createdAt,omitempty"`
}

// UsageSummary is a best-effort view of aggregate workspace usage (CPU,
// memory, egress) over the configured billing window. Shape mirrors the
// Vercel UsageSummary so the desktop UI can render it uniformly.
type UsageSummary struct {
	CPUSeconds        float64 `json:"cpuSeconds,omitempty"`
	MemoryGBHours     float64 `json:"memoryGbHours,omitempty"`
	NetworkEgressGB   float64 `json:"networkEgressGb,omitempty"`
	DiskGBHours       float64 `json:"diskGbHours,omitempty"`
	EstimatedCostUSD  float64 `json:"estimatedCostUsd,omitempty"`
	BillingPeriodFrom string  `json:"billingPeriodFrom,omitempty"`
	BillingPeriodTo   string  `json:"billingPeriodTo,omitempty"`
	Period            string  `json:"period,omitempty"`
}
