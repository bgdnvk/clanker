package logging

import (
	"context"
	"time"
)

// K8sClient interface for kubectl operations
type K8sClient interface {
	Run(ctx context.Context, args ...string) (string, error)
	RunWithNamespace(ctx context.Context, namespace string, args ...string) (string, error)
	RunJSON(ctx context.Context, args ...string) ([]byte, error)
}

// AIDecisionFunc is a function type for AI powered analysis
type AIDecisionFunc func(ctx context.Context, prompt string) (string, error)

// LogScope indicates the scope of log collection
type LogScope string

const (
	ScopePod        LogScope = "pod"
	ScopeDeployment LogScope = "deployment"
	ScopeNode       LogScope = "node"
	ScopeCluster    LogScope = "cluster"
	ScopeNamespace  LogScope = "namespace"
)

// LogLevel represents log severity
type LogLevel string

const (
	LevelError LogLevel = "error"
	LevelWarn  LogLevel = "warn"
	LevelInfo  LogLevel = "info"
	LevelDebug LogLevel = "debug"
)

// QueryOptions contains options for log queries
type QueryOptions struct {
	Namespace      string
	AllNamespaces  bool
	PodName        string
	DeploymentName string
	NodeName       string
	Container      string
	TailLines      int
	Since          string // Duration like "1h", "30m"
	SinceTime      *time.Time
	Follow         bool
	Previous       bool
	Timestamps     bool
	Patterns       []string   // Filter patterns (e.g., "503", "error")
	LevelFilter    []LogLevel // Filter by log level
	AnalyzeMode    bool       // Enable AI analysis
	Scope          LogScope
}

// ResponseType indicates the type of response
type ResponseType string

const (
	ResponseTypeRawLogs  ResponseType = "raw_logs"
	ResponseTypeAnalysis ResponseType = "analysis"
	ResponseTypeSummary  ResponseType = "summary"
	ResponseTypeError    ResponseType = "error"
)

// Response from the logging subagent
type Response struct {
	Type     ResponseType
	Message  string
	RawLogs  string
	Analysis *LogAnalysis
	Summary  *LogSummary
	Error    error
}

// LogEntry represents a single log line with metadata
type LogEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Pod       string    `json:"pod"`
	Container string    `json:"container"`
	Namespace string    `json:"namespace"`
	Node      string    `json:"node,omitempty"`
	Message   string    `json:"message"`
	Level     LogLevel  `json:"level"`
	IsError   bool      `json:"isError"`
	Pattern   string    `json:"pattern,omitempty"` // Matched pattern if any
}

// PodInfo contains basic pod information for log collection
type PodInfo struct {
	Name      string
	Namespace string
	Node      string
}

// AggregatedLogs contains logs from multiple sources
type AggregatedLogs struct {
	Source     string     `json:"source"`
	Scope      LogScope   `json:"scope"`
	TotalLines int        `json:"totalLines"`
	PodCount   int        `json:"podCount"`
	TimeRange  TimeRange  `json:"timeRange"`
	Entries    []LogEntry `json:"entries"`
	ErrorCount int        `json:"errorCount"`
	WarnCount  int        `json:"warnCount"`
	RawOutput  string     `json:"rawOutput,omitempty"`
}

// TimeRange represents a time window
type TimeRange struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// LogSummary provides a quick overview of logs
type LogSummary struct {
	TotalLines      int            `json:"totalLines"`
	ErrorCount      int            `json:"errorCount"`
	WarnCount       int            `json:"warnCount"`
	PodCount        int            `json:"podCount"`
	TopErrors       []ErrorPattern `json:"topErrors"`
	TimeRange       TimeRange      `json:"timeRange"`
	StatusBreakdown map[string]int `json:"statusBreakdown,omitempty"`
}

// ErrorPattern represents a recurring error pattern
type ErrorPattern struct {
	Pattern     string    `json:"pattern"`
	Count       int       `json:"count"`
	FirstSeen   time.Time `json:"firstSeen"`
	LastSeen    time.Time `json:"lastSeen"`
	SampleLines []string  `json:"sampleLines"`
}

// LogAnalysis contains AI powered analysis results
type LogAnalysis struct {
	Query            string            `json:"query"`
	Summary          string            `json:"summary"`
	IssuesFound      []IdentifiedIssue `json:"issuesFound"`
	RootCause        string            `json:"rootCause"`
	ImpactAssessment string            `json:"impactAssessment"`
	Recommendations  []Recommendation  `json:"recommendations"`
	Confidence       float64           `json:"confidence"` // 0-1
	AnalyzedAt       time.Time         `json:"analyzedAt"`
}

// IdentifiedIssue represents an issue found in logs
type IdentifiedIssue struct {
	Type         string    `json:"type"`
	Severity     string    `json:"severity"`
	Description  string    `json:"description"`
	Occurrences  int       `json:"occurrences"`
	FirstSeen    time.Time `json:"firstSeen"`
	LastSeen     time.Time `json:"lastSeen"`
	AffectedPods []string  `json:"affectedPods"`
}

// Recommendation is a suggested action
type Recommendation struct {
	Priority    int      `json:"priority"`
	Action      string   `json:"action"`
	Description string   `json:"description"`
	Command     string   `json:"command,omitempty"`
	Args        []string `json:"args,omitempty"`
	Risk        string   `json:"risk"` // low, medium, high
}

// QueryAnalysis represents parsed natural language query
type QueryAnalysis struct {
	Scope          LogScope
	ResourceType   string
	ResourceName   string
	Namespace      string
	NodeName       string
	Patterns       []string
	LevelFilter    []LogLevel
	TimeConstraint string
	WantsAnalysis  bool
	WantsFix       bool
}
