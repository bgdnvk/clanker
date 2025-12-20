package maker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// EnrichPlan expands certain high-level destructive steps into explicit prerequisite steps.
// This is used during planning time so the user can review exactly what will be deleted.
//
// Currently:
//   - iam delete-role: expands into detach managed policies, delete inline policies,
//     remove from instance profiles, delete permissions boundary, then delete-role.
func EnrichPlan(ctx context.Context, plan *Plan, opts ExecOptions) error {
	if plan == nil {
		return fmt.Errorf("nil plan")
	}
	if opts.Profile == "" {
		return fmt.Errorf("missing aws profile")
	}
	if opts.Region == "" {
		return fmt.Errorf("missing aws region")
	}

	var expanded []Command
	var notes []string

	deleteEverythingRelated := wantsDeleteEverythingRelated(plan.Question)

	state := &enrichState{
		roleCreated:        map[string]bool{},
		roleTrustSet:       map[string]bool{},
		rolePolicyAttached: map[string]bool{},
	}

	expanders := defaultExpanders(opts, deleteEverythingRelated, state)

	for _, cmd := range plan.Commands {
		args := normalizeArgs(cmd.Args)
		handled := false
		for _, exp := range expanders {
			if !exp.match(args) {
				continue
			}
			didExpand, steps, stepNotes := exp.expand(ctx, cmd, args)
			if len(stepNotes) > 0 {
				notes = append(notes, stepNotes...)
			}
			if didExpand {
				expanded = append(expanded, steps...)
				handled = true
				break
			}
		}
		if !handled {
			expanded = append(expanded, cmd)
		}
	}

	if len(expanded) > 0 {
		plan.Commands = dedupeCommands(expanded)
	}
	if len(notes) > 0 {
		plan.Notes = append(plan.Notes, notes...)
	}

	return nil
}

func dedupeCommands(cmds []Command) []Command {
	seen := map[string]bool{}
	out := make([]Command, 0, len(cmds))
	for _, c := range cmds {
		if len(c.Args) == 0 {
			continue
		}
		k := strings.Join(c.Args, "\x00")
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, c)
	}
	return out
}

type enrichState struct {
	roleCreated        map[string]bool
	roleTrustSet       map[string]bool
	rolePolicyAttached map[string]bool
	roleTrustCache     map[string][]string
}

type commandExpander struct {
	name   string
	match  func(args []string) bool
	expand func(ctx context.Context, cmd Command, args []string) (bool, []Command, []string)
}

func defaultExpanders(opts ExecOptions, deleteEverythingRelated bool, state *enrichState) []commandExpander {
	return []commandExpander{
		{
			name: "iam:ensure-roles",
			match: func(args []string) bool {
				// Only consider real AWS CLI commands with at least service/op.
				return len(args) >= 2
			},
			expand: func(ctx context.Context, cmd Command, args []string) (bool, []Command, []string) {
				// Avoid expanding IAM-mutating commands (prevents accidental loops).
				if args[0] == "iam" {
					return false, nil, nil
				}

				reqs, stepNotes := inferRoleRequirements(args)
				if len(reqs) == 0 {
					return false, nil, nil
				}

				var out []Command
				agg := aggregateRoleRequirements(reqs)
				for roleName, a := range agg {
					ensureRoleForServices(ctx, opts, &out, state, roleName, a.servicePrincipals)
					for _, p := range a.managedPolicyArns {
						reason := a.policyReason
						if reason == "" {
							reason = "Ensure service role has required managed policy"
						}
						ensureRoleHasManagedPolicy(&out, state, roleName, p, reason)
					}
				}

				out = append(out, Command{Args: args, Reason: cmd.Reason})
				return true, out, stepNotes
			},
		},
		{
			name: "lambda:delete-function",
			match: func(args []string) bool {
				return len(args) >= 2 && args[0] == "lambda" && args[1] == "delete-function"
			},
			expand: func(ctx context.Context, cmd Command, args []string) (bool, []Command, []string) {
				fn := flagValue(args, "--function-name")
				if fn == "" {
					return false, nil, nil
				}
				steps, stepNotes := expandDeleteLambdaFunction(ctx, opts, fn, deleteEverythingRelated)
				if len(steps) == 0 {
					return false, nil, stepNotes
				}
				return true, steps, stepNotes
			},
		},
		{
			name: "iam:delete-role",
			match: func(args []string) bool {
				return len(args) >= 2 && args[0] == "iam" && args[1] == "delete-role"
			},
			expand: func(ctx context.Context, cmd Command, args []string) (bool, []Command, []string) {
				roleName := flagValue(args, "--role-name")
				if roleName == "" {
					return false, nil, nil
				}
				steps, stepNotes := expandDeleteRole(ctx, opts, roleName)
				if len(steps) == 0 {
					return false, nil, stepNotes
				}
				return true, steps, stepNotes
			},
		},
		{
			name: "iam:delete-policy",
			match: func(args []string) bool {
				return len(args) >= 2 && args[0] == "iam" && args[1] == "delete-policy"
			},
			expand: func(ctx context.Context, cmd Command, args []string) (bool, []Command, []string) {
				policyArn := flagValue(args, "--policy-arn")
				if policyArn == "" {
					return false, nil, nil
				}
				steps, stepNotes := expandDeletePolicy(ctx, opts, policyArn)
				if len(steps) == 0 {
					return false, nil, stepNotes
				}
				return true, steps, stepNotes
			},
		},
		{
			name: "ec2:delete-security-group",
			match: func(args []string) bool {
				return len(args) >= 2 && args[0] == "ec2" && args[1] == "delete-security-group"
			},
			expand: func(ctx context.Context, cmd Command, args []string) (bool, []Command, []string) {
				groupID := flagValue(args, "--group-id")
				if groupID == "" {
					return false, nil, nil
				}
				steps, stepNotes := expandDeleteSecurityGroup(ctx, opts, groupID)
				if len(steps) == 0 {
					return false, nil, stepNotes
				}
				return true, steps, stepNotes
			},
		},
	}
}

type roleRequirement struct {
	roleName          string
	servicePrincipal  string
	managedPolicyArns []string
	policyReason      string
}

func inferRoleRequirements(args []string) ([]roleRequirement, []string) {
	service := ""
	op := ""
	if len(args) >= 1 {
		service = strings.TrimSpace(args[0])
	}
	if len(args) >= 2 {
		op = strings.TrimSpace(args[1])
	}
	if service == "" {
		return nil, nil
	}

	// Collect common role flags used across AWS CLI.
	// Note: If a service embeds RoleArn inside JSON (e.g., scheduler targets), we do not parse that here.
	flagRoles := []struct {
		flag string
		kind string
	}{
		{"--role", "service"},
		{"--role-arn", "service"},
		{"--service-role-arn", "service"},
		{"--execution-role-arn", "execution"},
		{"--task-role-arn", "task"},
		{"--job-role-arn", "execution"},
		{"--node-role", "ec2"},
	}

	seen := map[string]bool{}
	var out []roleRequirement
	var notes []string
	for _, fr := range flagRoles {
		roleName := roleNameFromFlag(args, fr.flag)
		if roleName == "" {
			continue
		}
		key := fr.flag + "|" + roleName
		if seen[key] {
			continue
		}
		seen[key] = true

		req := roleRequirement{
			roleName:         roleName,
			servicePrincipal: guessServicePrincipal(service, op, fr.kind),
		}

		// Attach conservative baseline managed policies for known execution roles.
		// This is intentionally NOT exhaustive; it's focused on "make it work" for common services.
		switch service {
		case "lambda":
			if fr.flag == "--role" {
				req.managedPolicyArns = append(req.managedPolicyArns, "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole")
				req.policyReason = "Allow service to write operational logs"
			}
		case "ecs":
			// ECS task definitions specify execution/task roles.
			if fr.flag == "--execution-role-arn" {
				req.managedPolicyArns = append(req.managedPolicyArns, "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy")
				req.policyReason = "Allow ECS tasks to pull images and write logs"
			}
		case "states":
			if fr.flag == "--role-arn" {
				if hasFlag(args, "--logging-configuration") {
					req.managedPolicyArns = append(req.managedPolicyArns, "arn:aws:iam::aws:policy/CloudWatchLogsFullAccess")
					req.policyReason = "Allow service to deliver logs to CloudWatch Logs"
				}
			}
		case "batch":
			// Batch service roles are used in compute environment setup.
			if fr.kind == "service" {
				req.managedPolicyArns = append(req.managedPolicyArns, "arn:aws:iam::aws:policy/service-role/AWSBatchServiceRole")
				req.policyReason = "Allow AWS Batch to manage resources"
			}
		}

		out = append(out, req)
	}

	jsonRoleArns := extractRoleArnsFromInlineJSONArgs(args)
	for _, arn := range jsonRoleArns {
		roleName := roleNameFromArn(arn)
		if roleName == "" {
			continue
		}
		key := "json|" + roleName
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, roleRequirement{
			roleName:         roleName,
			servicePrincipal: guessServicePrincipal(service, op, "service"),
		})
	}
	if len(jsonRoleArns) > 0 {
		notes = append(notes, "detected RoleArn values inside inline JSON args")
	}

	return out, notes
}

func guessServicePrincipal(service string, op string, roleKind string) string {
	service = strings.TrimSpace(service)
	op = strings.TrimSpace(op)
	roleKind = strings.TrimSpace(roleKind)
	if service == "" {
		return ""
	}
	if roleKind == "ec2" {
		return "ec2.amazonaws.com"
	}

	// Known exceptions where the principal is not simply "{service}.amazonaws.com".
	switch service {
	case "ecs":
		if roleKind == "task" || roleKind == "execution" || op == "register-task-definition" {
			return "ecs-tasks.amazonaws.com"
		}
		return "ecs.amazonaws.com"
	case "states":
		return "states.amazonaws.com"
	case "lambda":
		return "lambda.amazonaws.com"
	case "batch":
		return "batch.amazonaws.com"
	case "events":
		return "events.amazonaws.com"
	case "scheduler":
		return "scheduler.amazonaws.com"
	case "pipes":
		return "pipes.amazonaws.com"
	}

	return service + ".amazonaws.com"
}

type aggregatedRoleRequirement struct {
	servicePrincipals []string
	managedPolicyArns []string
	policyReason      string
}

func aggregateRoleRequirements(reqs []roleRequirement) map[string]aggregatedRoleRequirement {
	agg := map[string]aggregatedRoleRequirement{}
	for _, r := range reqs {
		roleName := strings.TrimSpace(r.roleName)
		principal := strings.TrimSpace(r.servicePrincipal)
		if roleName == "" || principal == "" {
			continue
		}
		a := agg[roleName]
		a.servicePrincipals = append(a.servicePrincipals, principal)
		for _, p := range r.managedPolicyArns {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			a.managedPolicyArns = append(a.managedPolicyArns, p)
		}
		if a.policyReason == "" {
			a.policyReason = strings.TrimSpace(r.policyReason)
		}
		agg[roleName] = a
	}

	for k, v := range agg {
		v.servicePrincipals = normalizeNonEmpty(v.servicePrincipals)
		v.managedPolicyArns = normalizeNonEmpty(v.managedPolicyArns)
		agg[k] = v
	}
	return agg
}

func assumeRolePolicyDocumentForPrincipals(servicePrincipals []string) string {
	servicePrincipals = normalizeNonEmpty(servicePrincipals)
	if len(servicePrincipals) == 0 {
		return ""
	}
	principal := any(servicePrincipals[0])
	if len(servicePrincipals) > 1 {
		arr := make([]any, 0, len(servicePrincipals))
		for _, p := range servicePrincipals {
			arr = append(arr, p)
		}
		principal = arr
	}
	policy := map[string]any{
		"Version": "2012-10-17",
		"Statement": []any{
			map[string]any{
				"Effect":    "Allow",
				"Principal": map[string]any{"Service": principal},
				"Action":    "sts:AssumeRole",
			},
		},
	}
	b, err := json.Marshal(policy)
	if err != nil {
		return ""
	}
	return string(b)
}

func normalizeNonEmpty(items []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(items))
	for _, s := range items {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func extractRoleArnsFromInlineJSONArgs(args []string) []string {
	jsonFlags := map[string]bool{
		"--cli-input-json":              true,
		"--target":                      true,
		"--targets":                     true,
		"--definition":                  true,
		"--source":                      true,
		"--destination":                 true,
		"--logging-configuration":       true,
		"--ip-permissions":              true,
		"--policy-document":             true,
		"--assume-role-policy-document": true,
		"--parameters":                  true,
		"--container-definitions":       true,
		"--network-configuration":       true,
	}

	var out []string
	seen := map[string]bool{}
	for i := 0; i < len(args); i++ {
		a := strings.TrimSpace(args[i])
		if !strings.HasPrefix(a, "--") {
			continue
		}
		flag := a
		value := ""
		if strings.Contains(a, "=") {
			parts := strings.SplitN(a, "=", 2)
			flag = strings.TrimSpace(parts[0])
			if len(parts) == 2 {
				value = strings.TrimSpace(parts[1])
			}
		} else if i+1 < len(args) {
			value = strings.TrimSpace(args[i+1])
		}
		if !jsonFlags[flag] {
			continue
		}
		if !(strings.HasPrefix(value, "{") || strings.HasPrefix(value, "[")) {
			continue
		}

		var v any
		if err := json.Unmarshal([]byte(value), &v); err != nil {
			continue
		}
		for _, arn := range findRoleArnsInJSON(v) {
			arn = strings.TrimSpace(arn)
			if arn == "" || seen[arn] {
				continue
			}
			seen[arn] = true
			out = append(out, arn)
		}
	}
	return out
}

func ensureRoleForServices(ctx context.Context, opts ExecOptions, out *[]Command, state *enrichState, roleName string, servicePrincipals []string) {
	roleName = strings.TrimSpace(roleName)
	servicePrincipals = normalizeNonEmpty(servicePrincipals)
	if roleName == "" || len(servicePrincipals) == 0 {
		return
	}

	// Merge existing trust principals to avoid clobbering shared roles.
	if state != nil {
		if existing, ok := state.roleTrustCache[roleName]; ok {
			servicePrincipals = normalizeNonEmpty(append(servicePrincipals, existing...))
		} else {
			if existing, err := getExistingRoleTrustPrincipals(ctx, opts, roleName); err == nil {
				state.roleTrustCache[roleName] = existing
				servicePrincipals = normalizeNonEmpty(append(servicePrincipals, existing...))
			}
		}
	} else {
		if existing, err := getExistingRoleTrustPrincipals(ctx, opts, roleName); err == nil {
			servicePrincipals = normalizeNonEmpty(append(servicePrincipals, existing...))
		}
	}

	assumeDoc := assumeRolePolicyDocumentForPrincipals(servicePrincipals)
	if assumeDoc == "" {
		return
	}

	if state == nil {
		*out = append(*out, Command{Args: []string{"iam", "create-role", "--role-name", roleName, "--assume-role-policy-document", assumeDoc}, Reason: "Ensure service execution role exists"})
		*out = append(*out, Command{Args: []string{"iam", "update-assume-role-policy", "--role-name", roleName, "--policy-document", assumeDoc}, Reason: "Ensure role trust policy allows required services"})
		return
	}

	if !state.roleCreated[roleName] {
		*out = append(*out, Command{Args: []string{"iam", "create-role", "--role-name", roleName, "--assume-role-policy-document", assumeDoc}, Reason: "Ensure service execution role exists"})
		state.roleCreated[roleName] = true
	}

	trustKey := roleName + "|" + strings.Join(servicePrincipals, ",")
	if !state.roleTrustSet[trustKey] {
		*out = append(*out, Command{Args: []string{"iam", "update-assume-role-policy", "--role-name", roleName, "--policy-document", assumeDoc}, Reason: "Ensure role trust policy allows required services"})
		state.roleTrustSet[trustKey] = true
	}
}

func getExistingRoleTrustPrincipals(ctx context.Context, opts ExecOptions, roleName string) ([]string, error) {
	roleName = strings.TrimSpace(roleName)
	if roleName == "" {
		return nil, nil
	}
	args := []string{"iam", "get-role", "--role-name", roleName, "--output", "json"}
	awsArgs := append(append([]string{}, args...), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
	out, err := runAWSCommandStreaming(ctx, awsArgs, nil, io.Discard)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Role struct {
			AssumeRolePolicyDocument any `json:"AssumeRolePolicyDocument"`
		} `json:"Role"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return nil, err
	}
	return findServicePrincipalsInAssumeRolePolicy(resp.Role.AssumeRolePolicyDocument), nil
}

func findServicePrincipalsInAssumeRolePolicy(v any) []string {
	var out []string
	switch t := v.(type) {
	case map[string]any:
		if p, ok := t["Principal"].(map[string]any); ok {
			if svc, ok := p["Service"]; ok {
				switch s := svc.(type) {
				case string:
					out = append(out, strings.TrimSpace(s))
				case []any:
					for _, it := range s {
						if ss, ok := it.(string); ok {
							out = append(out, strings.TrimSpace(ss))
						}
					}
				}
			}
		}
		for _, vv := range t {
			out = append(out, findServicePrincipalsInAssumeRolePolicy(vv)...)
		}
	case []any:
		for _, vv := range t {
			out = append(out, findServicePrincipalsInAssumeRolePolicy(vv)...)
		}
	}
	return normalizeNonEmpty(out)
}

func findRoleArnsInJSON(v any) []string {
	var out []string
	switch t := v.(type) {
	case map[string]any:
		for k, vv := range t {
			kl := strings.ToLower(strings.TrimSpace(k))
			if strings.Contains(kl, "rolearn") {
				if s, ok := vv.(string); ok {
					ss := strings.TrimSpace(s)
					if strings.HasPrefix(ss, "arn:") && strings.Contains(ss, ":role/") {
						out = append(out, ss)
					}
				}
			}
			out = append(out, findRoleArnsInJSON(vv)...)
		}
	case []any:
		for _, vv := range t {
			out = append(out, findRoleArnsInJSON(vv)...)
		}
	}
	return out
}

func ensureRoleHasManagedPolicy(out *[]Command, state *enrichState, roleName string, policyArn string, reason string) {
	roleName = strings.TrimSpace(roleName)
	policyArn = strings.TrimSpace(policyArn)
	if roleName == "" || policyArn == "" {
		return
	}
	key := roleName + "|" + policyArn
	if state != nil && state.rolePolicyAttached[key] {
		return
	}
	*out = append(*out, Command{Args: []string{"iam", "attach-role-policy", "--role-name", roleName, "--policy-arn", policyArn}, Reason: reason})
	if state != nil {
		state.rolePolicyAttached[key] = true
	}
}

func roleNameFromFlag(args []string, flag string) string {
	v := strings.TrimSpace(flagValue(args, flag))
	if v == "" {
		return ""
	}
	if parsed := roleNameFromArn(v); parsed != "" {
		return parsed
	}
	return v
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
		if strings.HasPrefix(a, flag+"=") {
			return true
		}
	}
	return false
}

func wantsDeleteEverythingRelated(question string) bool {
	q := strings.ToLower(strings.TrimSpace(question))
	if q == "" {
		return false
	}
	return strings.Contains(q, "everything related") ||
		strings.Contains(q, "everything associated") ||
		strings.Contains(q, "everything tied") ||
		strings.Contains(q, "delete everything") ||
		strings.Contains(q, "delete all related")
}

func expandDeleteRole(ctx context.Context, opts ExecOptions, roleName string) ([]Command, []string) {
	var out []Command
	var notes []string

	attached, err := listAttachedRolePolicies(ctx, opts, roleName)
	if err != nil {
		notes = append(notes, fmt.Sprintf("failed to preflight attached policies for role %s: %v", roleName, err))
	}
	inline, err2 := listInlineRolePolicies(ctx, opts, roleName)
	if err2 != nil {
		notes = append(notes, fmt.Sprintf("failed to preflight inline policies for role %s: %v", roleName, err2))
	}
	profiles, err3 := listInstanceProfilesForRole(ctx, opts, roleName)
	if err3 != nil {
		notes = append(notes, fmt.Sprintf("failed to preflight instance profiles for role %s: %v", roleName, err3))
	}
	boundary := ""
	if b, err := getRolePermissionsBoundary(ctx, opts, roleName); err == nil {
		boundary = b
	}

	for _, arn := range attached {
		out = append(out, Command{Args: []string{"iam", "detach-role-policy", "--role-name", roleName, "--policy-arn", arn}, Reason: "Detach managed policy before deleting role"})
	}
	for _, name := range inline {
		out = append(out, Command{Args: []string{"iam", "delete-role-policy", "--role-name", roleName, "--policy-name", name}, Reason: "Delete inline policy before deleting role"})
	}
	for _, ip := range profiles {
		out = append(out, Command{Args: []string{"iam", "remove-role-from-instance-profile", "--instance-profile-name", ip, "--role-name", roleName}, Reason: "Remove role from instance profile before deleting role"})
	}
	if boundary != "" {
		out = append(out, Command{Args: []string{"iam", "delete-role-permissions-boundary", "--role-name", roleName}, Reason: "Remove permissions boundary before deleting role"})
	}

	out = append(out, Command{Args: []string{"iam", "delete-role", "--role-name", roleName}, Reason: "Delete IAM role"})

	// If we couldn't preflight anything, we still return the delete-role command so the plan remains actionable.
	return out, notes
}

var roleArnRe = regexp.MustCompile(`(?i):role/([^/]+)$`)

func roleNameFromArn(arn string) string {
	arn = strings.TrimSpace(arn)
	if arn == "" {
		return ""
	}
	m := roleArnRe.FindStringSubmatch(arn)
	if len(m) != 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

func expandDeleteLambdaFunction(ctx context.Context, opts ExecOptions, functionName string, deleteEverythingRelated bool) ([]Command, []string) {
	var out []Command
	var notes []string

	conf, err := getFunctionConfiguration(ctx, opts, functionName)
	if err != nil {
		notes = append(notes, fmt.Sprintf("failed to preflight lambda configuration for %s: %v", functionName, err))
	}

	mappings, err := listEventSourceMappingUUIDs(ctx, opts, functionName)
	if err != nil {
		notes = append(notes, fmt.Sprintf("failed to preflight event source mappings for %s: %v", functionName, err))
	}
	for _, uuid := range mappings {
		out = append(out, Command{Args: []string{"lambda", "delete-event-source-mapping", "--uuid", uuid}, Reason: "Remove event source mapping before deleting function"})
	}

	if hasURL, _ := hasFunctionURLConfig(ctx, opts, functionName); hasURL {
		out = append(out, Command{Args: []string{"lambda", "delete-function-url-config", "--function-name", functionName}, Reason: "Delete function URL config"})
	}

	out = append(out, Command{Args: []string{"lambda", "delete-function", "--function-name", functionName}, Reason: "Delete Lambda function"})
	out = append(out, Command{Args: []string{"logs", "delete-log-group", "--log-group-name", "/aws/lambda/" + functionName}, Reason: "Delete Lambda CloudWatch log group"})

	if deleteEverythingRelated {
		roleName := roleNameFromArn(conf.Role)
		if roleName != "" {
			steps, stepNotes := expandDeleteRole(ctx, opts, roleName)
			if len(stepNotes) > 0 {
				notes = append(notes, stepNotes...)
			}
			out = append(out, steps...)
		}

		for _, sg := range conf.VpcConfig.SecurityGroupIds {
			sg = strings.TrimSpace(sg)
			if sg == "" {
				continue
			}
			steps, stepNotes := expandDeleteSecurityGroup(ctx, opts, sg)
			if len(stepNotes) > 0 {
				notes = append(notes, stepNotes...)
			}
			if len(steps) > 0 {
				out = append(out, steps...)
			} else {
				out = append(out, Command{Args: []string{"ec2", "delete-security-group", "--group-id", sg}, Reason: "Delete security group associated with Lambda VPC config"})
			}
		}
	}

	if len(conf.VpcConfig.SecurityGroupIds) > 0 || len(conf.VpcConfig.SubnetIds) > 0 {
		notes = append(notes, fmt.Sprintf("lambda %s is/was VPC-attached; ENIs may delay SG deletion", functionName))
	}

	return out, notes
}

type lambdaFunctionConfiguration struct {
	Role      string `json:"Role"`
	VpcConfig struct {
		SecurityGroupIds []string `json:"SecurityGroupIds"`
		SubnetIds        []string `json:"SubnetIds"`
	} `json:"VpcConfig"`
}

func getFunctionConfiguration(ctx context.Context, opts ExecOptions, functionName string) (lambdaFunctionConfiguration, error) {
	args := []string{"lambda", "get-function-configuration", "--function-name", functionName, "--output", "json"}
	awsArgs := append(append([]string{}, args...), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
	out, err := runAWSCommandStreaming(ctx, awsArgs, nil, io.Discard)
	if err != nil {
		return lambdaFunctionConfiguration{}, err
	}
	var resp lambdaFunctionConfiguration
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return lambdaFunctionConfiguration{}, err
	}
	return resp, nil
}

func listEventSourceMappingUUIDs(ctx context.Context, opts ExecOptions, functionName string) ([]string, error) {
	marker := ""
	var uuids []string
	for {
		args := []string{"lambda", "list-event-source-mappings", "--function-name", functionName, "--output", "json"}
		if marker != "" {
			args = append(args, "--marker", marker)
		}
		awsArgs := append(append([]string{}, args...), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
		out, err := runAWSCommandStreaming(ctx, awsArgs, nil, io.Discard)
		if err != nil {
			return nil, err
		}
		var resp struct {
			EventSourceMappings []struct {
				UUID string `json:"UUID"`
			} `json:"EventSourceMappings"`
			NextMarker string `json:"NextMarker"`
		}
		if err := json.Unmarshal([]byte(out), &resp); err != nil {
			return nil, err
		}
		for _, m := range resp.EventSourceMappings {
			u := strings.TrimSpace(m.UUID)
			if u != "" {
				uuids = append(uuids, u)
			}
		}
		if strings.TrimSpace(resp.NextMarker) == "" {
			break
		}
		marker = strings.TrimSpace(resp.NextMarker)
	}
	return uuids, nil
}

func hasFunctionURLConfig(ctx context.Context, opts ExecOptions, functionName string) (bool, error) {
	args := []string{"lambda", "get-function-url-config", "--function-name", functionName, "--output", "json"}
	awsArgs := append(append([]string{}, args...), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
	_, err := runAWSCommandStreaming(ctx, awsArgs, nil, io.Discard)
	if err == nil {
		return true, nil
	}
	// If missing, AWS CLI returns ResourceNotFoundException; just treat as false.
	return false, nil
}

func expandDeletePolicy(ctx context.Context, opts ExecOptions, policyArn string) ([]Command, []string) {
	var out []Command
	var notes []string

	roles, users, groups, err := listEntitiesForPolicy(ctx, opts, policyArn)
	if err != nil {
		notes = append(notes, fmt.Sprintf("failed to preflight entities for policy %s: %v", policyArn, err))
	}
	for _, r := range roles {
		out = append(out, Command{Args: []string{"iam", "detach-role-policy", "--role-name", r, "--policy-arn", policyArn}, Reason: "Detach policy from role before deleting policy"})
	}
	for _, u := range users {
		out = append(out, Command{Args: []string{"iam", "detach-user-policy", "--user-name", u, "--policy-arn", policyArn}, Reason: "Detach policy from user before deleting policy"})
	}
	for _, g := range groups {
		out = append(out, Command{Args: []string{"iam", "detach-group-policy", "--group-name", g, "--policy-arn", policyArn}, Reason: "Detach policy from group before deleting policy"})
	}

	versions, err := listPolicyNonDefaultVersions(ctx, opts, policyArn)
	if err != nil {
		notes = append(notes, fmt.Sprintf("failed to preflight policy versions for %s: %v", policyArn, err))
	}
	for _, v := range versions {
		out = append(out, Command{Args: []string{"iam", "delete-policy-version", "--policy-arn", policyArn, "--version-id", v}, Reason: "Delete non-default policy version before deleting policy"})
	}

	out = append(out, Command{Args: []string{"iam", "delete-policy", "--policy-arn", policyArn}, Reason: "Delete IAM managed policy"})
	return out, notes
}

func listEntitiesForPolicy(ctx context.Context, opts ExecOptions, policyArn string) ([]string, []string, []string, error) {
	args := []string{"iam", "list-entities-for-policy", "--policy-arn", policyArn, "--output", "json"}
	awsArgs := append(append([]string{}, args...), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
	out, err := runAWSCommandStreaming(ctx, awsArgs, nil, io.Discard)
	if err != nil {
		return nil, nil, nil, err
	}
	var resp struct {
		PolicyRoles []struct {
			RoleName string `json:"RoleName"`
		} `json:"PolicyRoles"`
		PolicyUsers []struct {
			UserName string `json:"UserName"`
		} `json:"PolicyUsers"`
		PolicyGroups []struct {
			GroupName string `json:"GroupName"`
		} `json:"PolicyGroups"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return nil, nil, nil, err
	}

	roles := make([]string, 0, len(resp.PolicyRoles))
	users := make([]string, 0, len(resp.PolicyUsers))
	groups := make([]string, 0, len(resp.PolicyGroups))
	for _, r := range resp.PolicyRoles {
		name := strings.TrimSpace(r.RoleName)
		if name != "" {
			roles = append(roles, name)
		}
	}
	for _, u := range resp.PolicyUsers {
		name := strings.TrimSpace(u.UserName)
		if name != "" {
			users = append(users, name)
		}
	}
	for _, g := range resp.PolicyGroups {
		name := strings.TrimSpace(g.GroupName)
		if name != "" {
			groups = append(groups, name)
		}
	}

	return roles, users, groups, nil
}

func listPolicyNonDefaultVersions(ctx context.Context, opts ExecOptions, policyArn string) ([]string, error) {
	args := []string{"iam", "list-policy-versions", "--policy-arn", policyArn, "--output", "json"}
	awsArgs := append(append([]string{}, args...), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
	out, err := runAWSCommandStreaming(ctx, awsArgs, nil, io.Discard)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Versions []struct {
			VersionId string `json:"VersionId"`
			IsDefault bool   `json:"IsDefaultVersion"`
		} `json:"Versions"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return nil, err
	}

	var outVersions []string
	for _, v := range resp.Versions {
		if v.IsDefault {
			continue
		}
		id := strings.TrimSpace(v.VersionId)
		if id != "" {
			outVersions = append(outVersions, id)
		}
	}
	return outVersions, nil
}

func listAttachedRolePolicies(ctx context.Context, opts ExecOptions, roleName string) ([]string, error) {
	marker := ""
	var arns []string
	for {
		args := []string{"iam", "list-attached-role-policies", "--role-name", roleName, "--output", "json"}
		if marker != "" {
			args = append(args, "--marker", marker)
		}
		awsArgs := append(append([]string{}, args...), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
		out, err := runAWSCommandStreaming(ctx, awsArgs, nil, io.Discard)
		if err != nil {
			return nil, err
		}
		var resp struct {
			AttachedPolicies []struct {
				PolicyArn string `json:"PolicyArn"`
			} `json:"AttachedPolicies"`
			IsTruncated bool   `json:"IsTruncated"`
			Marker      string `json:"Marker"`
		}
		if err := json.Unmarshal([]byte(out), &resp); err != nil {
			return nil, err
		}
		for _, ap := range resp.AttachedPolicies {
			arn := strings.TrimSpace(ap.PolicyArn)
			if arn != "" {
				arns = append(arns, arn)
			}
		}
		if !resp.IsTruncated || strings.TrimSpace(resp.Marker) == "" {
			break
		}
		marker = strings.TrimSpace(resp.Marker)
	}
	return arns, nil
}

func listInlineRolePolicies(ctx context.Context, opts ExecOptions, roleName string) ([]string, error) {
	marker := ""
	var names []string
	for {
		args := []string{"iam", "list-role-policies", "--role-name", roleName, "--output", "json"}
		if marker != "" {
			args = append(args, "--marker", marker)
		}
		awsArgs := append(append([]string{}, args...), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
		out, err := runAWSCommandStreaming(ctx, awsArgs, nil, io.Discard)
		if err != nil {
			return nil, err
		}
		var resp struct {
			PolicyNames []string `json:"PolicyNames"`
			IsTruncated bool     `json:"IsTruncated"`
			Marker      string   `json:"Marker"`
		}
		if err := json.Unmarshal([]byte(out), &resp); err != nil {
			return nil, err
		}
		for _, n := range resp.PolicyNames {
			n = strings.TrimSpace(n)
			if n != "" {
				names = append(names, n)
			}
		}
		if !resp.IsTruncated || strings.TrimSpace(resp.Marker) == "" {
			break
		}
		marker = strings.TrimSpace(resp.Marker)
	}
	return names, nil
}

func listInstanceProfilesForRole(ctx context.Context, opts ExecOptions, roleName string) ([]string, error) {
	marker := ""
	var names []string
	for {
		args := []string{"iam", "list-instance-profiles-for-role", "--role-name", roleName, "--output", "json"}
		if marker != "" {
			args = append(args, "--marker", marker)
		}
		awsArgs := append(append([]string{}, args...), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
		out, err := runAWSCommandStreaming(ctx, awsArgs, nil, io.Discard)
		if err != nil {
			return nil, err
		}
		var resp struct {
			InstanceProfiles []struct {
				InstanceProfileName string `json:"InstanceProfileName"`
			} `json:"InstanceProfiles"`
			IsTruncated bool   `json:"IsTruncated"`
			Marker      string `json:"Marker"`
		}
		if err := json.Unmarshal([]byte(out), &resp); err != nil {
			return nil, err
		}
		for _, ip := range resp.InstanceProfiles {
			n := strings.TrimSpace(ip.InstanceProfileName)
			if n != "" {
				names = append(names, n)
			}
		}
		if !resp.IsTruncated || strings.TrimSpace(resp.Marker) == "" {
			break
		}
		marker = strings.TrimSpace(resp.Marker)
	}
	return names, nil
}

func getRolePermissionsBoundary(ctx context.Context, opts ExecOptions, roleName string) (string, error) {
	args := []string{"iam", "get-role", "--role-name", roleName, "--output", "json"}
	awsArgs := append(append([]string{}, args...), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
	out, err := runAWSCommandStreaming(ctx, awsArgs, nil, io.Discard)
	if err != nil {
		return "", err
	}
	var resp struct {
		Role struct {
			PermissionsBoundary *struct {
				PermissionsBoundaryArn string `json:"PermissionsBoundaryArn"`
			} `json:"PermissionsBoundary"`
		} `json:"Role"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return "", err
	}
	if resp.Role.PermissionsBoundary == nil {
		return "", nil
	}
	return strings.TrimSpace(resp.Role.PermissionsBoundary.PermissionsBoundaryArn), nil
}
