package maker

import "time"

// RemediationPhase represents the current phase in the ReAct remediation loop
type RemediationPhase string

const (
	PhaseDiagnose  RemediationPhase = "diagnose"
	PhaseRemediate RemediationPhase = "remediate"
	PhaseVerify    RemediationPhase = "verify"
	PhaseComplete  RemediationPhase = "complete"
	PhaseFailed    RemediationPhase = "failed"
)

// CommandType indicates whether remediation is for AWS CLI or shell commands
type CommandType string

const (
	CommandTypeAWS   CommandType = "aws"
	CommandTypeShell CommandType = "shell"
)

// AgenticRemediationState tracks the full state of a remediation loop
type AgenticRemediationState struct {
	// Identity
	SessionID string    `json:"session_id"`
	StartedAt time.Time `json:"started_at"`

	// Context from the failed command
	FailedCommand []string           `json:"failed_command"`
	FailedOutput  string             `json:"failed_output"`
	ErrorCategory AWSFailureCategory `json:"error_category"`
	CommandType   CommandType        `json:"command_type"` // aws or shell

	// Conversation history for multi-turn LLM interaction
	History []ConversationTurn `json:"history"`

	// Current phase and iteration tracking
	Phase     RemediationPhase `json:"phase"`
	Iteration int              `json:"iteration"`

	// Accumulated context
	DiagnosticOutput   map[string]string   `json:"diagnostic_output"`
	RemediationActions []RemediationAction `json:"remediation_actions"`
	VerificationResult *VerificationResult `json:"verification_result"`

	// Bindings learned during remediation
	LearnedBindings map[string]string `json:"learned_bindings"`

	// Budget tracking
	Budget *RemediationBudget `json:"budget"`
}

// ConversationTurn represents a single turn in the LLM conversation
type ConversationTurn struct {
	Role      string           `json:"role"`
	Content   string           `json:"content"`
	Phase     RemediationPhase `json:"phase"`
	Timestamp time.Time        `json:"timestamp"`
}

// RemediationBudget tracks resource consumption
type RemediationBudget struct {
	MaxIterations       int           `json:"max_iterations"`
	MaxCommandsPerPhase int           `json:"max_commands_per_phase"`
	MaxAPICallsTotal    int           `json:"max_api_calls_total"`
	MaxDuration         time.Duration `json:"max_duration"`

	// Current consumption
	IterationsUsed   int `json:"iterations_used"`
	APICallsUsed     int `json:"api_calls_used"`
	CommandsExecuted int `json:"commands_executed"`
}

// RemediationAction represents a single action taken during remediation
type RemediationAction struct {
	Phase      RemediationPhase `json:"phase"`
	Command    []string         `json:"command"`
	Reason     string           `json:"reason"`
	Output     string           `json:"output"`
	Success    bool             `json:"success"`
	ExecutedAt time.Time        `json:"executed_at"`
}

// VerificationResult captures the result of a verification command
type VerificationResult struct {
	Command    []string `json:"command"`
	Output     string   `json:"output"`
	Success    bool     `json:"success"`
	Confidence float64  `json:"confidence"`
}

// DiagnosticResponse is returned by LLM during diagnose phase
type DiagnosticResponse struct {
	Analysis   string              `json:"analysis"`
	Hypothesis string              `json:"hypothesis"`
	Commands   []DiagnosticCommand `json:"commands"`
	Notes      []string            `json:"notes,omitempty"`
}

// DiagnosticCommand is a read-only command to gather information
type DiagnosticCommand struct {
	Args       []string `json:"args"`
	Purpose    string   `json:"purpose"`
	BindResult string   `json:"bind_result,omitempty"`
}

// RemediationLLMResponse is returned by LLM during remediate phase
type RemediationLLMResponse struct {
	RootCause string            `json:"root_cause"`
	Fix       string            `json:"fix"`
	Commands  []Command         `json:"commands"`
	Skip      bool              `json:"skip,omitempty"`
	Bindings  map[string]string `json:"bindings,omitempty"`
	Notes     []string          `json:"notes,omitempty"`
}

// VerificationAssessment is returned by LLM after verification command runs
type VerificationAssessment struct {
	Success     bool    `json:"success"`
	Confidence  float64 `json:"confidence"`
	Explanation string  `json:"explanation"`
	NextAction  string  `json:"next_action"`
}

// PhaseCommandPolicy defines what operations are allowed per phase
type PhaseCommandPolicy struct {
	AllowedPrefixes []string
	BlockedOps      map[string]bool
	MaxCommands     int
	AllowedServices []string
}

// Default budget constants
const (
	DefaultMaxIterations       = 3
	DefaultMaxCommandsPerPhase = 5
	DefaultMaxAPICallsTotal    = 15
	DefaultMaxDuration         = 5 * time.Minute
)

// Phase-specific command policies
var diagnosticPolicy = PhaseCommandPolicy{
	AllowedPrefixes: []string{
		"describe-", "list-", "get-", "head-", "wait",
	},
	BlockedOps:      map[string]bool{},
	MaxCommands:     5,
	AllowedServices: []string{},
}

var remediationPhasePolicy = PhaseCommandPolicy{
	AllowedPrefixes: []string{
		"create-", "put-", "update-", "add-", "attach-",
		"associate-", "register-", "enable-", "tag-",
		"set-", "modify-", "authorize-",
	},
	BlockedOps:      remediationBlockedOps,
	MaxCommands:     8,
	AllowedServices: []string{},
}

var verificationPolicy = PhaseCommandPolicy{
	AllowedPrefixes: []string{
		"describe-", "list-", "get-", "head-", "wait",
	},
	BlockedOps:      map[string]bool{},
	MaxCommands:     2,
	AllowedServices: []string{},
}
