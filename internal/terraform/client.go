package terraform

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/viper"
)

type Client struct {
	workspace string
	path      string
}

func NewClient(workspace string) (*Client, error) {
	// Get workspace configuration
	workspaces := viper.GetStringMap("terraform.workspaces")
	if len(workspaces) == 0 {
		return nil, fmt.Errorf("no terraform workspaces configured")
	}

	// Use default workspace if none specified
	if workspace == "" {
		workspace = viper.GetString("terraform.default_workspace")
		if workspace == "" {
			workspace = "dev"
		}
	}

	// Get workspace configuration
	workspaceData, exists := workspaces[workspace]
	if !exists {
		return nil, fmt.Errorf("terraform workspace '%s' not found in configuration", workspace)
	}

	config := workspaceData.(map[string]interface{})
	path, ok := config["path"].(string)
	if !ok {
		return nil, fmt.Errorf("terraform workspace '%s' has no path configured", workspace)
	}

	return &Client{
		workspace: workspace,
		path:      path,
	}, nil
}

func (c *Client) GetRelevantContext(ctx context.Context, question string) (string, error) {
	var context strings.Builder

	// Get workspace info
	workspaceInfo, err := c.getWorkspaceInfo(ctx)
	if err == nil {
		context.WriteString("Terraform Workspace Info:\n")
		context.WriteString(workspaceInfo)
		context.WriteString("\n\n")
	}

	// Get state info
	stateInfo, err := c.getStateInfo(ctx)
	if err == nil {
		context.WriteString("Terraform State:\n")
		context.WriteString(stateInfo)
		context.WriteString("\n\n")
	}

	// Get plan info if question is about changes/plan
	questionLower := strings.ToLower(question)
	if strings.Contains(questionLower, "plan") || strings.Contains(questionLower, "change") || strings.Contains(questionLower, "diff") {
		planInfo, err := c.getPlanInfo(ctx)
		if err == nil {
			context.WriteString("Terraform Plan:\n")
			context.WriteString(planInfo)
			context.WriteString("\n\n")
		}
	}

	// Get outputs if question is about outputs or infrastructure details
	if strings.Contains(questionLower, "output") || strings.Contains(questionLower, "infrastructure") || strings.Contains(questionLower, "resource") {
		outputInfo, err := c.getOutputInfo(ctx)
		if err == nil && outputInfo != "" {
			context.WriteString("Terraform Outputs:\n")
			context.WriteString(outputInfo)
			context.WriteString("\n\n")
		}
	}

	return context.String(), nil
}

func (c *Client) getWorkspaceInfo(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "terraform", "workspace", "show")
	cmd.Dir = c.path

	output, err := cmd.Output()
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("Current workspace: %s\nConfigured path: %s", strings.TrimSpace(string(output)), c.path), nil
}

func (c *Client) getStateInfo(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "terraform", "state", "list")
	cmd.Dir = c.path

	output, err := cmd.Output()
	if err != nil {
		return "", err
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) == 0 || (len(lines) == 1 && lines[0] == "") {
		return "No resources in state", nil
	}

	var info strings.Builder
	info.WriteString(fmt.Sprintf("Total resources: %d\n", len(lines)))

	// Group resources by type
	resourceTypes := make(map[string]int)
	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.Split(line, ".")
		if len(parts) > 0 {
			resourceTypes[parts[0]]++
		}
	}

	info.WriteString("Resource types:\n")
	for resourceType, count := range resourceTypes {
		info.WriteString(fmt.Sprintf("  %s: %d\n", resourceType, count))
	}

	return info.String(), nil
}

func (c *Client) getPlanInfo(ctx context.Context) (string, error) {
	// Check if terraform plan file exists
	planFile := filepath.Join(c.path, "tfplan")
	if _, err := os.Stat(planFile); os.IsNotExist(err) {
		// Run terraform plan
		cmd := exec.CommandContext(ctx, "terraform", "plan", "-no-color", "-compact-warnings")
		cmd.Dir = c.path

		output, err := cmd.Output()
		if err != nil {
			return "", err
		}

		// Return summary of plan
		lines := strings.Split(string(output), "\n")
		var summary strings.Builder
		for _, line := range lines {
			if strings.Contains(line, "Plan:") || strings.Contains(line, "No changes") {
				summary.WriteString(line)
				summary.WriteString("\n")
			}
		}

		return summary.String(), nil
	}

	// Show existing plan
	cmd := exec.CommandContext(ctx, "terraform", "show", "-no-color", planFile)
	cmd.Dir = c.path

	output, err := cmd.Output()
	if err != nil {
		return "", err
	}

	// Return first few lines of plan
	lines := strings.Split(string(output), "\n")
	maxLines := 20
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		lines = append(lines, "... (truncated)")
	}

	return strings.Join(lines, "\n"), nil
}

func (c *Client) getOutputInfo(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "terraform", "output", "-json")
	cmd.Dir = c.path

	output, err := cmd.Output()
	if err != nil {
		// Try non-JSON output as fallback
		cmd = exec.CommandContext(ctx, "terraform", "output")
		cmd.Dir = c.path
		output, err = cmd.Output()
		if err != nil {
			return "", err
		}
	}

	outputStr := strings.TrimSpace(string(output))
	if outputStr == "" || outputStr == "{}" {
		return "No outputs defined", nil
	}

	return outputStr, nil
}

func (c *Client) GetTerraformOutputs(ctx context.Context) (map[string]interface{}, error) {
	cmd := exec.CommandContext(ctx, "terraform", "output", "-json")
	cmd.Dir = c.path

	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var outputs map[string]interface{}
	if err := json.Unmarshal(output, &outputs); err != nil {
		return nil, fmt.Errorf("failed to parse terraform outputs: %w", err)
	}

	// Extract just the values from terraform output format
	result := make(map[string]interface{})
	for key, value := range outputs {
		if valueMap, ok := value.(map[string]interface{}); ok {
			if val, exists := valueMap["value"]; exists {
				result[key] = val
			}
		}
	}

	return result, nil
}
