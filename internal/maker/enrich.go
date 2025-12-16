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

	for _, cmd := range plan.Commands {
		args := normalizeArgs(cmd.Args)
		deleteEverythingRelated := wantsDeleteEverythingRelated(plan.Question)

		if len(args) >= 2 && args[0] == "lambda" && args[1] == "delete-function" {
			fn := flagValue(args, "--function-name")
			if fn != "" {
				steps, stepNotes := expandDeleteLambdaFunction(ctx, opts, fn, deleteEverythingRelated)
				if len(stepNotes) > 0 {
					notes = append(notes, stepNotes...)
				}
				if len(steps) > 0 {
					expanded = append(expanded, steps...)
					continue
				}
			}
		}

		if len(args) >= 2 && args[0] == "iam" && args[1] == "delete-role" {
			roleName := flagValue(args, "--role-name")
			if roleName == "" {
				expanded = append(expanded, cmd)
				continue
			}

			steps, stepNotes := expandDeleteRole(ctx, opts, roleName)
			if len(stepNotes) > 0 {
				notes = append(notes, stepNotes...)
			}
			if len(steps) > 0 {
				expanded = append(expanded, steps...)
				continue
			}
		}

		if len(args) >= 2 && args[0] == "iam" && args[1] == "delete-policy" {
			policyArn := flagValue(args, "--policy-arn")
			if policyArn != "" {
				steps, stepNotes := expandDeletePolicy(ctx, opts, policyArn)
				if len(stepNotes) > 0 {
					notes = append(notes, stepNotes...)
				}
				if len(steps) > 0 {
					expanded = append(expanded, steps...)
					continue
				}
			}
		}

		if len(args) >= 2 && args[0] == "ec2" && args[1] == "delete-security-group" {
			groupID := flagValue(args, "--group-id")
			if groupID != "" {
				steps, stepNotes := expandDeleteSecurityGroup(ctx, opts, groupID)
				if len(stepNotes) > 0 {
					notes = append(notes, stepNotes...)
				}
				if len(steps) > 0 {
					expanded = append(expanded, steps...)
					continue
				}
			}
		}

		expanded = append(expanded, cmd)
	}

	if len(expanded) > 0 {
		plan.Commands = expanded
	}
	if len(notes) > 0 {
		plan.Notes = append(plan.Notes, notes...)
	}

	return nil
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
