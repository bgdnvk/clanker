package terraform

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

type ViewReport struct {
	GeneratedAt     string            `json:"generatedAt"`
	Workspace       string            `json:"workspace"`
	Path            string            `json:"path"`
	Tool            string            `json:"tool"`
	ToolPath        string            `json:"toolPath,omitempty"`
	Status          string            `json:"status"`
	Summary         []string          `json:"summary"`
	Local           LocalView         `json:"local"`
	State           StateView         `json:"state"`
	Remote          RemoteView        `json:"remote"`
	Drift           *DriftView        `json:"drift,omitempty"`
	Alternatives    []AlternativeView `json:"alternatives"`
	Warnings        []string          `json:"warnings,omitempty"`
	Recommendations []string          `json:"recommendations,omitempty"`
}

type LocalView struct {
	Mode            string          `json:"mode"`
	FileCount       int             `json:"fileCount"`
	Files           []string        `json:"files,omitempty"`
	ProviderSources []string        `json:"providerSources,omitempty"`
	Modules         []string        `json:"modules,omitempty"`
	StaleArtifacts  []StaleArtifact `json:"staleArtifacts,omitempty"`
}

type StateView struct {
	Source           string         `json:"source"`
	Backend          string         `json:"backend"`
	Backends         []string       `json:"backends,omitempty"`
	HasRemoteBackend bool           `json:"hasRemoteBackend"`
	Availability     string         `json:"availability"`
	ResourceCount    int            `json:"resourceCount"`
	ResourceTypes    map[string]int `json:"resourceTypes,omitempty"`
	Sample           []string       `json:"sample,omitempty"`
}

type RemoteView struct {
	Enabled     bool     `json:"enabled"`
	Backends    []string `json:"backends,omitempty"`
	DriftStatus string   `json:"driftStatus"`
	HasChanges  bool     `json:"hasChanges"`
	Command     string   `json:"command,omitempty"`
	Summary     []string `json:"summary,omitempty"`
	Error       string   `json:"error,omitempty"`
}

type DriftView struct {
	Checked    bool     `json:"checked"`
	Status     string   `json:"status"`
	HasChanges bool     `json:"hasChanges"`
	ExitCode   int      `json:"exitCode"`
	Command    string   `json:"command"`
	Summary    []string `json:"summary,omitempty"`
	Output     []string `json:"output,omitempty"`
	Error      string   `json:"error,omitempty"`
}

type AlternativeView struct {
	Name         string `json:"name"`
	Binary       string `json:"binary,omitempty"`
	Category     string `json:"category"`
	Providers    string `json:"providers"`
	DriftCommand string `json:"driftCommand,omitempty"`
	DocsURL      string `json:"docsUrl"`
	Detected     bool   `json:"detected"`
	Status       string `json:"status"`
}

func BuildViewReport(workspace string, report AnalysisReport) ViewReport {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		workspace = "local"
	}

	state := buildStateView(report)
	remote := buildRemoteView(report)
	drift := buildDriftView(report.Drift)
	alternatives := buildAlternativeViews(report.Alternatives)
	status := viewStatus(report, state, drift)

	view := ViewReport{
		GeneratedAt:     time.Now().UTC().Format(time.RFC3339),
		Workspace:       workspace,
		Path:            report.Path,
		Tool:            report.Tool,
		ToolPath:        report.ToolPath,
		Status:          status,
		Local:           buildLocalView(report),
		State:           state,
		Remote:          remote,
		Drift:           drift,
		Alternatives:    alternatives,
		Warnings:        append([]string{}, report.Warnings...),
		Recommendations: append([]string{}, report.Recommendations...),
	}
	view.Summary = buildViewSummary(view)
	return view
}

func buildLocalView(report AnalysisReport) LocalView {
	return LocalView{
		Mode:            report.Mode,
		FileCount:       len(report.Files),
		Files:           append([]string{}, report.Files...),
		ProviderSources: append([]string{}, report.ProviderSources...),
		Modules:         append([]string{}, report.Modules...),
		StaleArtifacts:  append([]StaleArtifact{}, report.StaleArtifacts...),
	}
}

func buildStateView(report AnalysisReport) StateView {
	source := "none"
	backend := "none"
	if len(report.Files) > 0 {
		switch {
		case report.Remote:
			source = "remote"
		case len(report.Backends) > 0:
			source = "custom"
		default:
			source = "local"
		}
	}
	if len(report.Backends) > 0 {
		backend = strings.Join(report.Backends, ", ")
	}

	view := StateView{
		Source:           source,
		Backend:          backend,
		Backends:         append([]string{}, report.Backends...),
		HasRemoteBackend: report.Remote,
		Availability:     "unavailable",
	}
	if report.State != nil {
		view.Availability = "available"
		if report.State.ResourceCount == 0 {
			view.Availability = "empty"
		}
		view.ResourceCount = report.State.ResourceCount
		view.ResourceTypes = copyStringIntMap(report.State.ResourceTypes)
		view.Sample = append([]string{}, report.State.Sample...)
	}
	return view
}

func buildRemoteView(report AnalysisReport) RemoteView {
	view := RemoteView{
		Enabled:     report.Remote,
		Backends:    append([]string{}, report.Backends...),
		DriftStatus: "not-checked",
	}
	if report.Drift == nil {
		return view
	}
	view.HasChanges = report.Drift.HasChanges
	view.Command = report.Drift.Command
	view.Summary = append([]string{}, report.Drift.Summary...)
	view.Error = report.Drift.Error
	view.DriftStatus = driftStatus(report.Drift)
	return view
}

func buildDriftView(report *DriftReport) *DriftView {
	if report == nil {
		return nil
	}
	return &DriftView{
		Checked:    report.Checked,
		Status:     driftStatus(report),
		HasChanges: report.HasChanges,
		ExitCode:   report.ExitCode,
		Command:    report.Command,
		Summary:    append([]string{}, report.Summary...),
		Output:     append([]string{}, report.Output...),
		Error:      report.Error,
	}
}

func buildAlternativeViews(alternatives []AlternativeTool) []AlternativeView {
	views := make([]AlternativeView, 0, len(alternatives))
	for _, alternative := range alternatives {
		status := "missing"
		if alternative.Detected {
			status = "available"
		}
		views = append(views, AlternativeView{
			Name:         alternative.Name,
			Binary:       alternative.Binary,
			Category:     alternative.Category,
			Providers:    alternative.Providers,
			DriftCommand: alternative.DriftCommand,
			DocsURL:      alternative.DocsURL,
			Detected:     alternative.Detected,
			Status:       status,
		})
	}
	return views
}

func viewStatus(report AnalysisReport, state StateView, drift *DriftView) string {
	if drift != nil && drift.Status == "error" {
		return "error"
	}
	if drift != nil && drift.HasChanges {
		return "attention"
	}
	if len(report.StaleArtifacts) > 0 {
		return "attention"
	}
	if len(report.Files) > 0 && state.Availability == "unavailable" {
		return "warning"
	}
	if len(report.Warnings) > 0 {
		return "warning"
	}
	if len(report.Files) == 0 {
		return "warning"
	}
	return "ok"
}

func driftStatus(report *DriftReport) string {
	if report == nil || !report.Checked {
		return "not-checked"
	}
	if strings.TrimSpace(report.Error) != "" {
		return "error"
	}
	if report.HasChanges {
		return "changed"
	}
	return "in-sync"
}

func buildViewSummary(view ViewReport) []string {
	summary := []string{
		fmt.Sprintf("%d Terraform/OpenTofu file(s) in %s mode.", view.Local.FileCount, fallbackViewText(view.Local.Mode, "unknown")),
		fmt.Sprintf("State source is %s via %s backend.", fallbackViewText(view.State.Source, "unknown"), fallbackViewText(view.State.Backend, "none")),
	}
	if view.State.Availability == "available" || view.State.Availability == "empty" {
		summary = append(summary, fmt.Sprintf("%d resource(s) currently listed in state.", view.State.ResourceCount))
	} else {
		summary = append(summary, "State resources are unavailable from the selected environment.")
	}
	if view.Remote.Enabled {
		summary = append(summary, fmt.Sprintf("Remote backend detected; drift status is %s.", view.Remote.DriftStatus))
	} else {
		summary = append(summary, fmt.Sprintf("Local/custom state mode; drift status is %s.", view.Remote.DriftStatus))
	}
	if detected := detectedAlternativeCount(view.Alternatives); detected > 0 {
		summary = append(summary, fmt.Sprintf("%d IaC alternative tool(s) detected on PATH.", detected))
	}
	return summary
}

func detectedAlternativeCount(alternatives []AlternativeView) int {
	count := 0
	for _, alternative := range alternatives {
		if alternative.Detected {
			count++
		}
	}
	return count
}

func fallbackViewText(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func copyStringIntMap(values map[string]int) map[string]int {
	if len(values) == 0 {
		return nil
	}
	copied := make(map[string]int, len(values))
	for key, value := range values {
		copied[key] = value
	}
	return copied
}

func SortedResourceTypeLines(values map[string]int) []string {
	lines := make([]string, 0, len(values))
	for key, count := range values {
		lines = append(lines, fmt.Sprintf("%s: %d", key, count))
	}
	sort.Strings(lines)
	return lines
}
