package maker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

func expandDeleteSecurityGroup(ctx context.Context, opts ExecOptions, groupID string) ([]Command, []string) {
	var out []Command
	var notes []string

	// Preflight: find SG rules in other groups that reference this SG, and revoke them.
	refs, err := findSecurityGroupReferences(ctx, opts, groupID)
	if err != nil {
		notes = append(notes, fmt.Sprintf("failed to preflight SG references for %s: %v", groupID, err))
	} else {
		for _, r := range refs {
			if r.Type == "ingress" {
				out = append(out, Command{Args: []string{"ec2", "revoke-security-group-ingress", "--group-id", r.OwnerGroupID, "--ip-permissions", r.PermissionJSON}, Reason: "Remove inbound SG reference blocking deletion"})
			} else {
				out = append(out, Command{Args: []string{"ec2", "revoke-security-group-egress", "--group-id", r.OwnerGroupID, "--ip-permissions", r.PermissionJSON}, Reason: "Remove outbound SG reference blocking deletion"})
			}
		}
	}

	// Preflight: show ENIs using this SG (helps identify VPC/Lambda attachments).
	out = append(out, Command{Args: []string{"ec2", "describe-network-interfaces", "--filters", fmt.Sprintf("Name=group-id,Values=%s", groupID), "--output", "json"}, Reason: "Show ENIs still using the SG (deletion will fail if any exist)"})

	out = append(out, Command{Args: []string{"ec2", "delete-security-group", "--group-id", groupID}, Reason: "Delete security group"})
	return out, notes
}

type sgReference struct {
	OwnerGroupID   string
	Type           string // ingress|egress
	PermissionJSON string
}

func findSecurityGroupReferences(ctx context.Context, opts ExecOptions, targetGroupID string) ([]sgReference, error) {
	args := []string{"ec2", "describe-security-groups", "--output", "json"}
	awsArgs := append(append([]string{}, args...), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
	out, err := runAWSCommandStreaming(ctx, awsArgs, nil, io.Discard)
	if err != nil {
		return nil, err
	}

	var resp struct {
		SecurityGroups []struct {
			GroupId             string `json:"GroupId"`
			IpPermissions       []any  `json:"IpPermissions"`
			IpPermissionsEgress []any  `json:"IpPermissionsEgress"`
		} `json:"SecurityGroups"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return nil, err
	}

	var refs []sgReference
	for _, sg := range resp.SecurityGroups {
		owner := strings.TrimSpace(sg.GroupId)
		if owner == "" {
			continue
		}
		for _, perm := range sg.IpPermissions {
			if permissionReferencesGroup(perm, targetGroupID) {
				b, _ := json.Marshal([]any{perm})
				refs = append(refs, sgReference{OwnerGroupID: owner, Type: "ingress", PermissionJSON: string(b)})
			}
		}
		for _, perm := range sg.IpPermissionsEgress {
			if permissionReferencesGroup(perm, targetGroupID) {
				b, _ := json.Marshal([]any{perm})
				refs = append(refs, sgReference{OwnerGroupID: owner, Type: "egress", PermissionJSON: string(b)})
			}
		}
	}

	return refs, nil
}

func permissionReferencesGroup(permission any, targetGroupID string) bool {
	m, ok := permission.(map[string]any)
	if !ok {
		// When unmarshalling into any, encoding/json uses map[string]any.
		// If not, we can't introspect.
		return false
	}
	pairs, ok := m["UserIdGroupPairs"].([]any)
	if !ok {
		return false
	}
	for _, p := range pairs {
		pm, ok := p.(map[string]any)
		if !ok {
			continue
		}
		gid, _ := pm["GroupId"].(string)
		if strings.TrimSpace(gid) == targetGroupID {
			return true
		}
	}
	return false
}
