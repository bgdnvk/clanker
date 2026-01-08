package helm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ReleaseManager handles Helm release operations
type ReleaseManager struct {
	client HelmClient
	debug  bool
}

// NewReleaseManager creates a new release manager
func NewReleaseManager(client HelmClient, debug bool) *ReleaseManager {
	return &ReleaseManager{
		client: client,
		debug:  debug,
	}
}

// ListReleases lists Helm releases
func (m *ReleaseManager) ListReleases(ctx context.Context, namespace string, opts QueryOptions) ([]ReleaseInfo, error) {
	args := []string{"list", "-o", "json"}

	if opts.AllNamespaces {
		args = append(args, "--all-namespaces")
	}

	var output string
	var err error

	if opts.AllNamespaces {
		output, err = m.client.Run(ctx, args...)
	} else {
		output, err = m.client.RunWithNamespace(ctx, namespace, args...)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to list releases: %w", err)
	}

	return m.parseReleaseList([]byte(output))
}

// GetRelease gets details of a specific release
func (m *ReleaseManager) GetRelease(ctx context.Context, name, namespace string) (*ReleaseInfo, error) {
	output, err := m.client.RunWithNamespace(ctx, namespace, "status", name, "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("failed to get release %s: %w", name, err)
	}

	return m.parseReleaseStatus([]byte(output))
}

// GetReleaseHistory gets the revision history of a release
func (m *ReleaseManager) GetReleaseHistory(ctx context.Context, name, namespace string) ([]ReleaseHistoryEntry, error) {
	output, err := m.client.RunWithNamespace(ctx, namespace, "history", name, "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("failed to get release history for %s: %w", name, err)
	}

	return m.parseReleaseHistory([]byte(output))
}

// GetReleaseValues gets the values of a release
func (m *ReleaseManager) GetReleaseValues(ctx context.Context, name, namespace string) (string, error) {
	output, err := m.client.RunWithNamespace(ctx, namespace, "get", "values", name)
	if err != nil {
		return "", fmt.Errorf("failed to get values for %s: %w", name, err)
	}

	return output, nil
}

// InstallReleasePlan creates a plan for installing a Helm chart
func (m *ReleaseManager) InstallReleasePlan(opts InstallOptions) *HelmPlan {
	args := []string{"install", opts.ReleaseName, opts.Chart}

	if opts.Namespace != "" {
		args = append(args, "-n", opts.Namespace)
	}

	if opts.CreateNamespace {
		args = append(args, "--create-namespace")
	}

	if opts.Version != "" {
		args = append(args, "--version", opts.Version)
	}

	for _, f := range opts.ValuesFiles {
		args = append(args, "-f", f)
	}

	for _, s := range opts.Set {
		args = append(args, "--set", s)
	}

	if opts.Wait {
		args = append(args, "--wait")
	}

	if opts.Timeout > 0 {
		args = append(args, "--timeout", opts.Timeout.String())
	}

	if opts.DryRun {
		args = append(args, "--dry-run")
	}

	if opts.Description != "" {
		args = append(args, "--description", opts.Description)
	}

	notes := []string{
		fmt.Sprintf("Installing chart %s as release %s", opts.Chart, opts.ReleaseName),
	}

	if opts.CreateNamespace {
		notes = append(notes, fmt.Sprintf("Namespace %s will be created if it does not exist", opts.Namespace))
	}

	if opts.Wait {
		notes = append(notes, "Will wait for all resources to be ready")
	}

	return &HelmPlan{
		Version:   1,
		CreatedAt: time.Now(),
		Summary:   fmt.Sprintf("Install Helm release %s from chart %s", opts.ReleaseName, opts.Chart),
		Steps: []HelmStep{
			{
				ID:          "install-release",
				Description: fmt.Sprintf("Install release %s", opts.ReleaseName),
				Command:     "helm",
				Args:        args,
				Reason:      fmt.Sprintf("Install chart %s in namespace %s", opts.Chart, opts.Namespace),
			},
		},
		Notes: notes,
	}
}

// UpgradeReleasePlan creates a plan for upgrading a Helm release
func (m *ReleaseManager) UpgradeReleasePlan(opts UpgradeOptions) *HelmPlan {
	args := []string{"upgrade", opts.ReleaseName, opts.Chart}

	if opts.Namespace != "" {
		args = append(args, "-n", opts.Namespace)
	}

	if opts.Version != "" {
		args = append(args, "--version", opts.Version)
	}

	for _, f := range opts.ValuesFiles {
		args = append(args, "-f", f)
	}

	for _, s := range opts.Set {
		args = append(args, "--set", s)
	}

	if opts.Wait {
		args = append(args, "--wait")
	}

	if opts.Timeout > 0 {
		args = append(args, "--timeout", opts.Timeout.String())
	}

	if opts.DryRun {
		args = append(args, "--dry-run")
	}

	if opts.ReuseValues {
		args = append(args, "--reuse-values")
	}

	if opts.ResetValues {
		args = append(args, "--reset-values")
	}

	if opts.Force {
		args = append(args, "--force")
	}

	if opts.Install {
		args = append(args, "--install")
	}

	if opts.Description != "" {
		args = append(args, "--description", opts.Description)
	}

	notes := []string{
		fmt.Sprintf("Upgrading release %s with chart %s", opts.ReleaseName, opts.Chart),
	}

	if opts.Install {
		notes = append(notes, "Will install if release does not exist")
	}

	if opts.ReuseValues {
		notes = append(notes, "Reusing existing values")
	}

	return &HelmPlan{
		Version:   1,
		CreatedAt: time.Now(),
		Summary:   fmt.Sprintf("Upgrade Helm release %s", opts.ReleaseName),
		Steps: []HelmStep{
			{
				ID:          "upgrade-release",
				Description: fmt.Sprintf("Upgrade release %s", opts.ReleaseName),
				Command:     "helm",
				Args:        args,
				Reason:      fmt.Sprintf("Upgrade release %s to latest chart version", opts.ReleaseName),
			},
		},
		Notes: notes,
	}
}

// RollbackReleasePlan creates a plan for rolling back a Helm release
func (m *ReleaseManager) RollbackReleasePlan(opts RollbackOptions) *HelmPlan {
	args := []string{"rollback", opts.ReleaseName}

	if opts.Revision > 0 {
		args = append(args, fmt.Sprintf("%d", opts.Revision))
	}

	if opts.Namespace != "" {
		args = append(args, "-n", opts.Namespace)
	}

	if opts.Wait {
		args = append(args, "--wait")
	}

	if opts.Timeout > 0 {
		args = append(args, "--timeout", opts.Timeout.String())
	}

	if opts.DryRun {
		args = append(args, "--dry-run")
	}

	if opts.Force {
		args = append(args, "--force")
	}

	revisionStr := "previous revision"
	if opts.Revision > 0 {
		revisionStr = fmt.Sprintf("revision %d", opts.Revision)
	}

	return &HelmPlan{
		Version:   1,
		CreatedAt: time.Now(),
		Summary:   fmt.Sprintf("Rollback Helm release %s to %s", opts.ReleaseName, revisionStr),
		Steps: []HelmStep{
			{
				ID:          "rollback-release",
				Description: fmt.Sprintf("Rollback release %s to %s", opts.ReleaseName, revisionStr),
				Command:     "helm",
				Args:        args,
				Reason:      fmt.Sprintf("Restore release %s to a previous state", opts.ReleaseName),
			},
		},
		Notes: []string{
			fmt.Sprintf("Rolling back %s to %s", opts.ReleaseName, revisionStr),
			"Use 'helm history' to view available revisions",
		},
	}
}

// UninstallReleasePlan creates a plan for uninstalling a Helm release
func (m *ReleaseManager) UninstallReleasePlan(opts UninstallOptions) *HelmPlan {
	args := []string{"uninstall", opts.ReleaseName}

	if opts.Namespace != "" {
		args = append(args, "-n", opts.Namespace)
	}

	if opts.KeepHistory {
		args = append(args, "--keep-history")
	}

	if opts.DryRun {
		args = append(args, "--dry-run")
	}

	if opts.Wait {
		args = append(args, "--wait")
	}

	if opts.Timeout > 0 {
		args = append(args, "--timeout", opts.Timeout.String())
	}

	if opts.Description != "" {
		args = append(args, "--description", opts.Description)
	}

	notes := []string{
		fmt.Sprintf("Uninstalling release %s from namespace %s", opts.ReleaseName, opts.Namespace),
	}

	if opts.KeepHistory {
		notes = append(notes, "Release history will be preserved for rollback")
	} else {
		notes = append(notes, "Release history will be deleted permanently")
	}

	return &HelmPlan{
		Version:   1,
		CreatedAt: time.Now(),
		Summary:   fmt.Sprintf("Uninstall Helm release %s", opts.ReleaseName),
		Steps: []HelmStep{
			{
				ID:          "uninstall-release",
				Description: fmt.Sprintf("Uninstall release %s", opts.ReleaseName),
				Command:     "helm",
				Args:        args,
				Reason:      fmt.Sprintf("Remove release %s and its resources", opts.ReleaseName),
			},
		},
		Notes: notes,
	}
}

// parseReleaseList parses a JSON list of releases
func (m *ReleaseManager) parseReleaseList(data []byte) ([]ReleaseInfo, error) {
	var rawList []json.RawMessage

	if err := json.Unmarshal(data, &rawList); err != nil {
		// Try parsing as empty result
		if strings.TrimSpace(string(data)) == "[]" || strings.TrimSpace(string(data)) == "" {
			return []ReleaseInfo{}, nil
		}
		return nil, fmt.Errorf("failed to parse release list: %w", err)
	}

	releases := make([]ReleaseInfo, 0, len(rawList))
	for _, item := range rawList {
		release, err := m.parseReleaseJSON(item)
		if err != nil {
			if m.debug {
				fmt.Printf("[helm] failed to parse release: %v\n", err)
			}
			continue
		}
		releases = append(releases, *release)
	}

	return releases, nil
}

// parseReleaseJSON parses a single release JSON
func (m *ReleaseManager) parseReleaseJSON(data []byte) (*ReleaseInfo, error) {
	var raw struct {
		Name       string `json:"name"`
		Namespace  string `json:"namespace"`
		Revision   string `json:"revision"`
		Status     string `json:"status"`
		Chart      string `json:"chart"`
		AppVersion string `json:"app_version"`
		Updated    string `json:"updated"`
	}

	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse release JSON: %w", err)
	}

	// Parse revision
	var revision int
	fmt.Sscanf(raw.Revision, "%d", &revision)

	// Parse chart name and version
	chartName := raw.Chart
	chartVersion := ""
	if idx := strings.LastIndex(raw.Chart, "-"); idx > 0 {
		chartName = raw.Chart[:idx]
		chartVersion = raw.Chart[idx+1:]
	}

	// Parse updated time
	updated, _ := time.Parse("2006-01-02 15:04:05.999999999 -0700 MST", raw.Updated)

	return &ReleaseInfo{
		Name:         raw.Name,
		Namespace:    raw.Namespace,
		Revision:     revision,
		Status:       raw.Status,
		Chart:        chartName,
		ChartVersion: chartVersion,
		AppVersion:   raw.AppVersion,
		Updated:      updated,
	}, nil
}

// parseReleaseStatus parses release status JSON
func (m *ReleaseManager) parseReleaseStatus(data []byte) (*ReleaseInfo, error) {
	var raw struct {
		Name string `json:"name"`
		Info struct {
			Status      string `json:"status"`
			Description string `json:"description"`
			Notes       string `json:"notes"`
		} `json:"info"`
		Namespace string `json:"namespace"`
		Version   int    `json:"version"`
		Chart     struct {
			Metadata struct {
				Name       string `json:"name"`
				Version    string `json:"version"`
				AppVersion string `json:"appVersion"`
			} `json:"metadata"`
		} `json:"chart"`
	}

	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse release status: %w", err)
	}

	return &ReleaseInfo{
		Name:         raw.Name,
		Namespace:    raw.Namespace,
		Revision:     raw.Version,
		Status:       raw.Info.Status,
		Chart:        raw.Chart.Metadata.Name,
		ChartVersion: raw.Chart.Metadata.Version,
		AppVersion:   raw.Chart.Metadata.AppVersion,
		Description:  raw.Info.Description,
		Notes:        raw.Info.Notes,
	}, nil
}

// parseReleaseHistory parses release history JSON
func (m *ReleaseManager) parseReleaseHistory(data []byte) ([]ReleaseHistoryEntry, error) {
	var rawList []struct {
		Revision    int    `json:"revision"`
		Updated     string `json:"updated"`
		Status      string `json:"status"`
		Chart       string `json:"chart"`
		AppVersion  string `json:"app_version"`
		Description string `json:"description"`
	}

	if err := json.Unmarshal(data, &rawList); err != nil {
		return nil, fmt.Errorf("failed to parse release history: %w", err)
	}

	history := make([]ReleaseHistoryEntry, 0, len(rawList))
	for _, raw := range rawList {
		updated, _ := time.Parse("2006-01-02 15:04:05.999999999 -0700 MST", raw.Updated)
		history = append(history, ReleaseHistoryEntry{
			Revision:    raw.Revision,
			Updated:     updated,
			Status:      raw.Status,
			Chart:       raw.Chart,
			AppVersion:  raw.AppVersion,
			Description: raw.Description,
		})
	}

	return history, nil
}
