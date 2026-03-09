package maker

import (
	"sort"
	"strings"
)

// CommandPriority defines execution priority (lower = earlier)
// KEY: Compute BEFORE load balancing, load balancing BEFORE CDN
type CommandPriority struct {
	ServiceOp string
	Priority  int
}

var commandPriorities = []CommandPriority{
	// Phase 1: Discovery (priority 10-19)
	{"ec2 describe-vpcs", 10},
	{"ec2 describe-subnets", 11},
	{"ssm get-parameter", 12},
	{"sts get-caller-identity", 13},

	// Phase 1: IAM (priority 20-29)
	{"iam create-role", 20},
	{"iam put-role-policy", 21},
	{"iam attach-role-policy", 22},
	{"iam create-instance-profile", 23},
	{"iam add-role-to-instance-profile", 24},
	{"iam get-instance-profile", 25},

	// Phase 1: Security groups for EC2 (priority 30-39)
	{"ec2 create-security-group", 30},
	{"ec2 authorize-security-group-ingress", 31},
	{"ec2 authorize-security-group-egress", 32},

	// Phase 1: ECR (priority 40-49)
	{"ecr create-repository", 40},
	{"ecr describe-repositories", 41},
	{"ecr get-authorization-token", 42},

	// Phase 1: Secrets (priority 50-59)
	{"secretsmanager create-secret", 50},

	// Phase 2: COMPUTE - Launch instances (priority 60-69)
	{"ec2 run-instances", 60},
	{"ecs create-service", 61},
	{"lambda create-function", 62},

	// Phase 2: Wait for compute to be ready (priority 70-79)
	{"ec2 wait instance-running", 70},
	{"ecs wait services-stable", 71},

	// Phase 3: SERVICE HEALTH VERIFICATION happens here (handled in exec.go)
	// Priority 80 is reserved for health checks

	// Phase 4: Load Balancing - AFTER compute is healthy (priority 90-99)
	{"elbv2 create-target-group", 90},
	{"elbv2 create-load-balancer", 91},
	{"elbv2 wait load-balancer-available", 92},
	{"elbv2 create-listener", 93},
	{"elbv2 register-targets", 94},
	{"elbv2 wait target-in-service", 95},

	// Phase 5: CDN - AFTER ALB is ready (priority 100-109)
	{"cloudfront create-distribution", 100},
	{"cloudfront wait distribution-deployed", 101},
	{"route53 change-resource-record-sets", 102},
}

// buildPriorityMap creates a map from service+op prefix to priority
func buildPriorityMap() map[string]int {
	m := make(map[string]int)
	for _, p := range commandPriorities {
		m[p.ServiceOp] = p.Priority
	}
	return m
}

// getCommandPriority returns the priority for a command
func getCommandPriority(cmd Command, priorityMap map[string]int) int {
	if len(cmd.Args) < 2 {
		return 100 // Default priority
	}

	service := strings.ToLower(cmd.Args[0])
	op := strings.ToLower(cmd.Args[1])
	if service == "aws" && len(cmd.Args) >= 3 {
		service = strings.ToLower(cmd.Args[1])
		op = strings.ToLower(cmd.Args[2])
	}

	key := service + " " + op
	if p, ok := priorityMap[key]; ok {
		return p
	}

	// Try prefix match
	for k, p := range priorityMap {
		if strings.HasPrefix(key, k) {
			return p
		}
	}

	return 100 // Default priority
}

// ReorderPlanCommands reorders commands to satisfy dependencies
// Returns a new plan with commands in optimal order
func ReorderPlanCommands(plan *Plan) *Plan {
	if plan == nil || len(plan.Commands) <= 1 {
		return plan
	}

	priorityMap := buildPriorityMap()

	// Create sortable slice with original indices
	type indexedCmd struct {
		cmd      Command
		origIdx  int
		priority int
	}

	indexed := make([]indexedCmd, len(plan.Commands))
	for i, cmd := range plan.Commands {
		indexed[i] = indexedCmd{
			cmd:      cmd,
			origIdx:  i,
			priority: getCommandPriority(cmd, priorityMap),
		}
	}

	// Stable sort by priority (preserves relative order for same priority)
	sort.SliceStable(indexed, func(i, j int) bool {
		return indexed[i].priority < indexed[j].priority
	})

	// Build reordered plan
	reordered := &Plan{
		Version:   plan.Version,
		CreatedAt: plan.CreatedAt,
		Provider:  plan.Provider,
		Question:  plan.Question,
		Summary:   plan.Summary,
		Notes:     plan.Notes,
		Commands:  make([]Command, len(plan.Commands)),
	}

	for i, ic := range indexed {
		reordered.Commands[i] = ic.cmd
	}

	return reordered
}

// ShouldReorderPlan checks if the plan would benefit from reordering
func ShouldReorderPlan(plan *Plan) bool {
	if plan == nil || len(plan.Commands) <= 1 {
		return false
	}

	warnings := ValidatePlanSequencing(plan)
	return len(warnings) > 0
}
