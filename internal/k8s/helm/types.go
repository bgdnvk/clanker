package helm

import (
	"context"
	"time"
)

// HelmClient defines the interface for helm operations
// This interface is satisfied by a helm CLI wrapper via an adapter
type HelmClient interface {
	Run(ctx context.Context, args ...string) (string, error)
	RunWithNamespace(ctx context.Context, namespace string, args ...string) (string, error)
}

// ResourceType identifies the type of helm resource
type ResourceType string

const (
	ResourceRelease ResourceType = "release"
	ResourceChart   ResourceType = "chart"
	ResourceRepo    ResourceType = "repo"
)

// ResponseType indicates the type of response from the sub-agent
type ResponseType string

const (
	ResponseTypeResult ResponseType = "result"
	ResponseTypePlan   ResponseType = "plan"
)

// QueryOptions contains options for helm queries
type QueryOptions struct {
	Namespace     string
	AllNamespaces bool
	OutputFormat  string
}

// Response represents the response from the helm sub-agent
type Response struct {
	Type    ResponseType
	Data    interface{}
	Plan    *HelmPlan
	Message string
}

// HelmPlan represents a plan for helm modifications
type HelmPlan struct {
	Version   int        `json:"version"`
	CreatedAt time.Time  `json:"createdAt"`
	Summary   string     `json:"summary"`
	Steps     []HelmStep `json:"steps"`
	Notes     []string   `json:"notes,omitempty"`
}

// HelmStep represents a single step in a helm plan
type HelmStep struct {
	ID          string            `json:"id"`
	Description string            `json:"description"`
	Command     string            `json:"command"`
	Args        []string          `json:"args"`
	Reason      string            `json:"reason,omitempty"`
	Produces    map[string]string `json:"produces,omitempty"`
	WaitFor     *WaitCondition    `json:"waitFor,omitempty"`
}

// WaitCondition specifies a condition to wait for
type WaitCondition struct {
	Resource  string        `json:"resource"`
	Condition string        `json:"condition"`
	Timeout   time.Duration `json:"timeout"`
}

// ReleaseInfo contains Helm release information
type ReleaseInfo struct {
	Name         string            `json:"name"`
	Namespace    string            `json:"namespace"`
	Revision     int               `json:"revision"`
	Status       string            `json:"status"`
	Chart        string            `json:"chart"`
	ChartVersion string            `json:"chartVersion"`
	AppVersion   string            `json:"appVersion"`
	Updated      time.Time         `json:"updated"`
	Description  string            `json:"description,omitempty"`
	Notes        string            `json:"notes,omitempty"`
	Labels       map[string]string `json:"labels,omitempty"`
}

// ReleaseHistoryEntry contains a single revision entry
type ReleaseHistoryEntry struct {
	Revision    int       `json:"revision"`
	Updated     time.Time `json:"updated"`
	Status      string    `json:"status"`
	Chart       string    `json:"chart"`
	AppVersion  string    `json:"appVersion"`
	Description string    `json:"description"`
}

// ChartInfo contains Helm chart information
type ChartInfo struct {
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	AppVersion  string   `json:"appVersion"`
	Description string   `json:"description"`
	Home        string   `json:"home,omitempty"`
	Sources     []string `json:"sources,omitempty"`
	Keywords    []string `json:"keywords,omitempty"`
	Maintainers []string `json:"maintainers,omitempty"`
	Deprecated  bool     `json:"deprecated,omitempty"`
}

// RepoInfo contains Helm repository information
type RepoInfo struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// InstallOptions contains options for installing a Helm chart
type InstallOptions struct {
	ReleaseName     string
	Chart           string
	Namespace       string
	CreateNamespace bool
	Version         string
	Values          map[string]interface{}
	ValuesFiles     []string
	Set             []string
	Wait            bool
	Timeout         time.Duration
	DryRun          bool
	Description     string
	Labels          map[string]string
}

// UpgradeOptions contains options for upgrading a Helm release
type UpgradeOptions struct {
	ReleaseName string
	Chart       string
	Namespace   string
	Version     string
	Values      map[string]interface{}
	ValuesFiles []string
	Set         []string
	Wait        bool
	Timeout     time.Duration
	DryRun      bool
	ReuseValues bool
	ResetValues bool
	Force       bool
	Install     bool
	Description string
}

// RollbackOptions contains options for rolling back a Helm release
type RollbackOptions struct {
	ReleaseName string
	Revision    int
	Namespace   string
	Wait        bool
	Timeout     time.Duration
	DryRun      bool
	Force       bool
}

// UninstallOptions contains options for uninstalling a Helm release
type UninstallOptions struct {
	ReleaseName string
	Namespace   string
	KeepHistory bool
	DryRun      bool
	Wait        bool
	Timeout     time.Duration
	Description string
}

// AddRepoOptions contains options for adding a Helm repository
type AddRepoOptions struct {
	Name     string
	URL      string
	Username string
	Password string
	CAFile   string
	CertFile string
	KeyFile  string
	Insecure bool
}

// ReleaseStatus represents the status of a Helm release
type ReleaseStatus string

const (
	StatusDeployed     ReleaseStatus = "deployed"
	StatusFailed       ReleaseStatus = "failed"
	StatusPending      ReleaseStatus = "pending-install"
	StatusUninstalling ReleaseStatus = "uninstalling"
	StatusSuperseded   ReleaseStatus = "superseded"
	StatusUnknown      ReleaseStatus = "unknown"
)

// AKS Azure Container Registry constants
const (
	// AKSACRURLFormat is the OCI format for Azure Container Registry
	// Format: oci://{registry}.azurecr.io/{repository}
	AKSACRURLFormat = "oci://%s.azurecr.io/%s"
)

// AKS recommended repositories
const (
	// AKSRepoAzureMarketplace is the Azure Marketplace charts
	AKSRepoAzureMarketplace = "https://marketplace.azurecr.io/helm/v1/repo"
	// AKSRepoMicrosoft is the Microsoft charts repository
	AKSRepoMicrosoft = "https://microsoft.github.io/charts"
)
