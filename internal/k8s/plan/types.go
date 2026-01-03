package plan

import (
	"time"
)

const CurrentPlanVersion = 1

// K8sPlan represents an execution plan for K8s operations
type K8sPlan struct {
	Version     int         `json:"version"`
	CreatedAt   time.Time   `json:"createdAt"`
	Operation   string      `json:"operation"`   // create-cluster, deploy, scale, delete
	ClusterType string      `json:"clusterType"` // eks, kubeadm, k3s
	ClusterName string      `json:"clusterName"`
	Region      string      `json:"region"`
	Profile     string      `json:"profile"`
	Summary     string      `json:"summary"`
	Steps       []Step      `json:"steps"`
	Notes       []string    `json:"notes,omitempty"`
	Connection  *Connection `json:"connection,omitempty"`
}

// Step represents a single step in the execution plan
type Step struct {
	ID           string            `json:"id"`
	Description  string            `json:"description"`
	Command      string            `json:"command"` // eksctl, aws, ssh, kubectl
	Args         []string          `json:"args"`
	Reason       string            `json:"reason,omitempty"`
	Produces     map[string]string `json:"produces,omitempty"`
	WaitFor      *WaitConfig       `json:"waitFor,omitempty"`
	ConfigChange *ConfigChange     `json:"configChange,omitempty"`
	SSHConfig    *SSHStepConfig    `json:"sshConfig,omitempty"`
}

// WaitConfig configures async waiting behavior
type WaitConfig struct {
	Type        string        `json:"type"` // cluster-ready, node-ready, instance-running
	Resource    string        `json:"resource,omitempty"`
	Timeout     time.Duration `json:"timeout"`
	Interval    time.Duration `json:"interval"`
	Description string        `json:"description,omitempty"`
}

// ConfigChange tracks changes to configuration files
type ConfigChange struct {
	File        string `json:"file"`
	Description string `json:"description"`
	Before      string `json:"before,omitempty"`
	After       string `json:"after,omitempty"`
	Diff        string `json:"diff,omitempty"`
}

// SSHStepConfig holds SSH-specific configuration for a step
type SSHStepConfig struct {
	Host       string `json:"host"`
	User       string `json:"user"`
	KeyPath    string `json:"keyPath"`
	Script     string `json:"script"`
	ScriptName string `json:"scriptName,omitempty"`
}

// Connection holds information for connecting to the cluster
type Connection struct {
	Kubeconfig string   `json:"kubeconfig"`
	Endpoint   string   `json:"endpoint"`
	Commands   []string `json:"commands"`
}

// ExecOptions configures plan execution
type ExecOptions struct {
	Profile    string
	Region     string
	Debug      bool
	DryRun     bool
	SSHKeyPath string
}

// ExecResult holds the result of plan execution
type ExecResult struct {
	Success    bool
	Connection *Connection
	Bindings   map[string]string
	Errors     []string
}

// StepResult holds the result of a single step execution
type StepResult struct {
	StepID   string
	Success  bool
	Output   string
	Error    error
	Bindings map[string]string
}

// PlanDisplayOptions configures how the plan is displayed
type PlanDisplayOptions struct {
	ShowCommands bool
	ShowSSH      bool
	Verbose      bool
}
