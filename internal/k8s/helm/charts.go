package helm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ChartManager handles Helm chart and repository operations
type ChartManager struct {
	client HelmClient
	debug  bool
}

// NewChartManager creates a new chart manager
func NewChartManager(client HelmClient, debug bool) *ChartManager {
	return &ChartManager{
		client: client,
		debug:  debug,
	}
}

// SearchCharts searches for charts in configured repositories
func (m *ChartManager) SearchCharts(ctx context.Context, keyword string) ([]ChartInfo, error) {
	args := []string{"search", "repo", keyword, "-o", "json"}

	output, err := m.client.Run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to search charts: %w", err)
	}

	return m.parseChartList([]byte(output))
}

// ShowChart shows detailed information about a chart
func (m *ChartManager) ShowChart(ctx context.Context, chart string) (string, error) {
	output, err := m.client.Run(ctx, "show", "chart", chart)
	if err != nil {
		return "", fmt.Errorf("failed to show chart %s: %w", chart, err)
	}

	return output, nil
}

// ShowChartValues shows the default values for a chart
func (m *ChartManager) ShowChartValues(ctx context.Context, chart string) (string, error) {
	output, err := m.client.Run(ctx, "show", "values", chart)
	if err != nil {
		return "", fmt.Errorf("failed to show chart values for %s: %w", chart, err)
	}

	return output, nil
}

// ShowChartReadme shows the README for a chart
func (m *ChartManager) ShowChartReadme(ctx context.Context, chart string) (string, error) {
	output, err := m.client.Run(ctx, "show", "readme", chart)
	if err != nil {
		return "", fmt.Errorf("failed to show chart readme for %s: %w", chart, err)
	}

	return output, nil
}

// ListRepos lists configured Helm repositories
func (m *ChartManager) ListRepos(ctx context.Context) ([]RepoInfo, error) {
	output, err := m.client.Run(ctx, "repo", "list", "-o", "json")
	if err != nil {
		// No repos configured returns error
		if strings.Contains(err.Error(), "no repositories") {
			return []RepoInfo{}, nil
		}
		return nil, fmt.Errorf("failed to list repos: %w", err)
	}

	return m.parseRepoList([]byte(output))
}

// AddRepoPlan creates a plan for adding a Helm repository
func (m *ChartManager) AddRepoPlan(opts AddRepoOptions) *HelmPlan {
	args := []string{"repo", "add", opts.Name, opts.URL}

	if opts.Username != "" {
		args = append(args, "--username", opts.Username)
	}

	if opts.Password != "" {
		args = append(args, "--password", opts.Password)
	}

	if opts.CAFile != "" {
		args = append(args, "--ca-file", opts.CAFile)
	}

	if opts.CertFile != "" {
		args = append(args, "--cert-file", opts.CertFile)
	}

	if opts.KeyFile != "" {
		args = append(args, "--key-file", opts.KeyFile)
	}

	if opts.Insecure {
		args = append(args, "--insecure-skip-tls-verify")
	}

	return &HelmPlan{
		Version:   1,
		CreatedAt: time.Now(),
		Summary:   fmt.Sprintf("Add Helm repository %s", opts.Name),
		Steps: []HelmStep{
			{
				ID:          "add-repo",
				Description: fmt.Sprintf("Add repository %s", opts.Name),
				Command:     "helm",
				Args:        args,
				Reason:      fmt.Sprintf("Add %s repository from %s", opts.Name, opts.URL),
			},
			{
				ID:          "update-repos",
				Description: "Update repository index",
				Command:     "helm",
				Args:        []string{"repo", "update"},
				Reason:      "Fetch latest chart index from the new repository",
			},
		},
		Notes: []string{
			fmt.Sprintf("Adding repository %s from %s", opts.Name, opts.URL),
			"Repository index will be updated after adding",
		},
	}
}

// UpdateReposPlan creates a plan for updating Helm repositories
func (m *ChartManager) UpdateReposPlan() *HelmPlan {
	return &HelmPlan{
		Version:   1,
		CreatedAt: time.Now(),
		Summary:   "Update Helm repositories",
		Steps: []HelmStep{
			{
				ID:          "update-repos",
				Description: "Update all repository indexes",
				Command:     "helm",
				Args:        []string{"repo", "update"},
				Reason:      "Fetch latest chart indexes from all configured repositories",
			},
		},
		Notes: []string{
			"Updates the local cache of all configured repositories",
			"Run this before installing or upgrading charts to get latest versions",
		},
	}
}

// RemoveRepoPlan creates a plan for removing a Helm repository
func (m *ChartManager) RemoveRepoPlan(name string) *HelmPlan {
	return &HelmPlan{
		Version:   1,
		CreatedAt: time.Now(),
		Summary:   fmt.Sprintf("Remove Helm repository %s", name),
		Steps: []HelmStep{
			{
				ID:          "remove-repo",
				Description: fmt.Sprintf("Remove repository %s", name),
				Command:     "helm",
				Args:        []string{"repo", "remove", name},
				Reason:      fmt.Sprintf("Remove the %s repository from local configuration", name),
			},
		},
		Notes: []string{
			fmt.Sprintf("Removing repository %s", name),
			"Charts from this repository will no longer be available for installation",
		},
	}
}

// parseChartList parses a JSON list of charts
func (m *ChartManager) parseChartList(data []byte) ([]ChartInfo, error) {
	var rawList []struct {
		Name        string `json:"name"`
		Version     string `json:"version"`
		AppVersion  string `json:"app_version"`
		Description string `json:"description"`
	}

	if err := json.Unmarshal(data, &rawList); err != nil {
		// Empty result
		if strings.TrimSpace(string(data)) == "[]" || strings.TrimSpace(string(data)) == "" {
			return []ChartInfo{}, nil
		}
		return nil, fmt.Errorf("failed to parse chart list: %w", err)
	}

	charts := make([]ChartInfo, 0, len(rawList))
	for _, raw := range rawList {
		charts = append(charts, ChartInfo{
			Name:        raw.Name,
			Version:     raw.Version,
			AppVersion:  raw.AppVersion,
			Description: raw.Description,
		})
	}

	return charts, nil
}

// parseRepoList parses a JSON list of repos
func (m *ChartManager) parseRepoList(data []byte) ([]RepoInfo, error) {
	var rawList []struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	}

	if err := json.Unmarshal(data, &rawList); err != nil {
		// Empty result
		if strings.TrimSpace(string(data)) == "[]" || strings.TrimSpace(string(data)) == "" {
			return []RepoInfo{}, nil
		}
		return nil, fmt.Errorf("failed to parse repo list: %w", err)
	}

	repos := make([]RepoInfo, 0, len(rawList))
	for _, raw := range rawList {
		repos = append(repos, RepoInfo{
			Name: raw.Name,
			URL:  raw.URL,
		})
	}

	return repos, nil
}
