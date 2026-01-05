package plan

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const CurrentPlanVersion = 1

// MakerPlan represents a plan in AWS maker-compatible format
type MakerPlan struct {
	Version   int             `json:"version"`
	CreatedAt time.Time       `json:"createdAt"`
	Question  string          `json:"question"`
	Summary   string          `json:"summary"`
	Commands  []MakerCommand  `json:"commands"`
	Notes     []string        `json:"notes,omitempty"`
}

// MakerCommand represents a command in AWS maker-compatible format
type MakerCommand struct {
	Args     []string          `json:"args"`
	Reason   string            `json:"reason,omitempty"`
	Produces map[string]string `json:"produces,omitempty"`
}

// ToMakerPlan converts a K8sPlan to AWS maker-compatible format
func (p *K8sPlan) ToMakerPlan(question string) *MakerPlan {
	mp := &MakerPlan{
		Version:   p.Version,
		CreatedAt: p.CreatedAt,
		Question:  question,
		Summary:   p.Summary,
		Notes:     p.Notes,
		Commands:  make([]MakerCommand, 0, len(p.Steps)),
	}

	for _, step := range p.Steps {
		// Build the full command args
		args := []string{step.Command}
		args = append(args, step.Args...)

		cmd := MakerCommand{
			Args:     args,
			Reason:   step.Reason,
			Produces: step.Produces,
		}

		// Add description as reason if reason is empty
		if cmd.Reason == "" && step.Description != "" {
			cmd.Reason = step.Description
		}

		mp.Commands = append(mp.Commands, cmd)
	}

	return mp
}

// SavePlan saves the plan to a JSON file in ~/.clanker/plans/
func (p *K8sPlan) SavePlan(question string) (string, error) {
	// Create plans directory
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}

	plansDir := filepath.Join(home, ".clanker", "plans")
	if err := os.MkdirAll(plansDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create plans directory: %w", err)
	}

	// Generate filename with timestamp
	timestamp := time.Now().Format("20060102-150405")
	filename := fmt.Sprintf("k8s-%s-%s.json", sanitizeFilename(p.ClusterName), timestamp)
	planPath := filepath.Join(plansDir, filename)

	// Convert to maker format
	makerPlan := p.ToMakerPlan(question)

	// Marshal to JSON
	data, err := json.MarshalIndent(makerPlan, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal plan: %w", err)
	}

	// Write to file
	if err := os.WriteFile(planPath, data, 0644); err != nil {
		return "", fmt.Errorf("failed to write plan file: %w", err)
	}

	return planPath, nil
}

// sanitizeFilename removes or replaces characters that aren't safe for filenames
func sanitizeFilename(name string) string {
	name = strings.ReplaceAll(name, "/", "-")
	name = strings.ReplaceAll(name, "\\", "-")
	name = strings.ReplaceAll(name, ":", "-")
	name = strings.ReplaceAll(name, "*", "-")
	name = strings.ReplaceAll(name, "?", "-")
	name = strings.ReplaceAll(name, "\"", "-")
	name = strings.ReplaceAll(name, "<", "-")
	name = strings.ReplaceAll(name, ">", "-")
	name = strings.ReplaceAll(name, "|", "-")
	return name
}

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
