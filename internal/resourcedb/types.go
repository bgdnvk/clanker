package resourcedb

import "time"

// Resource represents a cloud resource created during a deployment run
type Resource struct {
	ID           int64             `json:"id,omitempty"`
	RunID        string            `json:"run_id"`
	CommandIndex int               `json:"command_index"`
	Provider     string            `json:"provider"`      // aws, gcp, azure, cloudflare, digitalocean, hetzner
	Service      string            `json:"service"`       // ec2, rds, elbv2, etc.
	Operation    string            `json:"operation"`     // create-instance, create-load-balancer
	ResourceType string            `json:"resource_type"` // ec2:instance, elbv2:load-balancer
	ResourceID   string            `json:"resource_id"`   // i-xxx, sg-xxx
	ResourceARN  string            `json:"resource_arn"`  // arn:aws:...
	ResourceName string            `json:"resource_name"` // user-provided name
	Region       string            `json:"region"`
	Profile      string            `json:"profile"`
	AccountID    string            `json:"account_id"`
	ParentRunID  string            `json:"parent_run_id,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"` // filtered, no secrets
	Tags         map[string]string `json:"tags,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
}

// RunSummary provides a summary of resources created in a run
type RunSummary struct {
	RunID         string    `json:"run_id"`
	Provider      string    `json:"provider"`
	Region        string    `json:"region"`
	Profile       string    `json:"profile"`
	ResourceCount int       `json:"resource_count"`
	CreatedAt     time.Time `json:"created_at"`
}
