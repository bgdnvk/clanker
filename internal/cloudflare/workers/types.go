package workers

import "time"

// Worker represents a Cloudflare Worker
type Worker struct {
	ID         string          `json:"id"`
	Name       string          `json:"script_name,omitempty"`
	Etag       string          `json:"etag,omitempty"`
	Size       int             `json:"size,omitempty"`
	Handlers   []string        `json:"handlers,omitempty"`
	CreatedOn  time.Time       `json:"created_on,omitempty"`
	ModifiedOn time.Time       `json:"modified_on,omitempty"`
	Bindings   []WorkerBinding `json:"bindings,omitempty"`
}

// WorkerBinding represents a binding to a Worker
type WorkerBinding struct {
	Name string `json:"name"`
	Type string `json:"type"` // kv_namespace, r2_bucket, d1, durable_object, etc.
}

// KVNamespace represents a Workers KV namespace
type KVNamespace struct {
	ID                  string `json:"id"`
	Title               string `json:"title"`
	SupportsURLEncoding bool   `json:"supports_url_encoding,omitempty"`
}

// D1Database represents a D1 database
type D1Database struct {
	UUID      string    `json:"uuid"`
	Name      string    `json:"name"`
	Version   string    `json:"version,omitempty"`
	NumTables int       `json:"num_tables,omitempty"`
	FileSize  int64     `json:"file_size,omitempty"`
	CreatedAt time.Time `json:"created_at,omitempty"`
}

// R2Bucket represents an R2 storage bucket
type R2Bucket struct {
	Name         string    `json:"name"`
	CreationDate time.Time `json:"creation_date,omitempty"`
	Location     string    `json:"location,omitempty"`
}

// PagesProject represents a Cloudflare Pages project
type PagesProject struct {
	ID               string           `json:"id"`
	Name             string           `json:"name"`
	Subdomain        string           `json:"subdomain"`
	ProductionBranch string           `json:"production_branch,omitempty"`
	CreatedOn        time.Time        `json:"created_on,omitempty"`
	Domains          []string         `json:"domains,omitempty"`
	Source           *PagesSource     `json:"source,omitempty"`
	LatestDeployment *PagesDeployment `json:"latest_deployment,omitempty"`
}

// PagesSource represents the source configuration for a Pages project
type PagesSource struct {
	Type   string             `json:"type"` // github, gitlab, etc.
	Config *PagesSourceConfig `json:"config,omitempty"`
}

// PagesSourceConfig contains source-specific configuration
type PagesSourceConfig struct {
	Owner              string `json:"owner,omitempty"`
	RepoName           string `json:"repo_name,omitempty"`
	ProductionBranch   string `json:"production_branch,omitempty"`
	DeploymentsEnabled bool   `json:"deployments_enabled,omitempty"`
}

// PagesDeployment represents a Pages deployment
type PagesDeployment struct {
	ID          string    `json:"id"`
	Environment string    `json:"environment"` // production, preview
	URL         string    `json:"url"`
	CreatedOn   time.Time `json:"created_on,omitempty"`
	ModifiedOn  time.Time `json:"modified_on,omitempty"`
}

// QueryOptions contains options for Workers queries
type QueryOptions struct {
	AccountID string `json:"account_id,omitempty"`
}

// ResponseType indicates the type of response
type ResponseType string

const (
	ResponseTypeResult ResponseType = "result"
	ResponseTypePlan   ResponseType = "plan"
	ResponseTypeError  ResponseType = "error"
)

// Response represents the result of a Workers operation
type Response struct {
	Type    ResponseType `json:"type"`
	Result  string       `json:"result,omitempty"`
	Plan    *Plan        `json:"plan,omitempty"`
	Error   error        `json:"error,omitempty"`
	Message string       `json:"message,omitempty"`
}

// Plan represents a Workers modification plan
type Plan struct {
	Summary  string    `json:"summary"`
	Commands []Command `json:"commands"`
}

// Command represents a single Workers command (wrangler or API)
type Command struct {
	Tool     string   `json:"tool"` // wrangler, api
	Args     []string `json:"args,omitempty"`
	Method   string   `json:"method,omitempty"`
	Endpoint string   `json:"endpoint,omitempty"`
	Body     string   `json:"body,omitempty"`
	Reason   string   `json:"reason"`
}

// QueryAnalysis contains the result of analyzing a Workers query
type QueryAnalysis struct {
	IsReadOnly   bool
	Operation    string // list, get, create, delete, deploy
	ResourceType string // worker, kv, d1, r2, pages
	ResourceName string
}
