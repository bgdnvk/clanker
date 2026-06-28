package terraform

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
)

const (
	defaultMaxOutputLines = 80
	stalePlanAge          = 24 * time.Hour
	staleLocalStateAge    = 30 * 24 * time.Hour
)

type AnalysisOptions struct {
	Tool           string `json:"tool,omitempty"`
	CheckDrift     bool   `json:"checkDrift,omitempty"`
	IncludePlan    bool   `json:"includePlan,omitempty"`
	MaxOutputLines int    `json:"maxOutputLines,omitempty"`
}

type AnalysisReport struct {
	Workspace       string            `json:"workspace"`
	Path            string            `json:"path"`
	Tool            string            `json:"tool"`
	ToolPath        string            `json:"toolPath,omitempty"`
	Mode            string            `json:"mode"`
	Remote          bool              `json:"remote"`
	Files           []string          `json:"files"`
	Backends        []string          `json:"backends,omitempty"`
	ProviderSources []string          `json:"providerSources,omitempty"`
	Modules         []string          `json:"modules,omitempty"`
	State           *StateSummary     `json:"state,omitempty"`
	Drift           *DriftReport      `json:"drift,omitempty"`
	StaleArtifacts  []StaleArtifact   `json:"staleArtifacts,omitempty"`
	Alternatives    []AlternativeTool `json:"alternatives"`
	Warnings        []string          `json:"warnings,omitempty"`
	Recommendations []string          `json:"recommendations,omitempty"`
}

type StateSummary struct {
	ResourceCount int            `json:"resourceCount"`
	ResourceTypes map[string]int `json:"resourceTypes,omitempty"`
	Sample        []string       `json:"sample,omitempty"`
}

type DriftReport struct {
	Checked    bool     `json:"checked"`
	HasChanges bool     `json:"hasChanges"`
	ExitCode   int      `json:"exitCode"`
	Command    string   `json:"command"`
	Summary    []string `json:"summary,omitempty"`
	Output     []string `json:"output,omitempty"`
	Error      string   `json:"error,omitempty"`
}

type StaleArtifact struct {
	Path           string `json:"path"`
	Kind           string `json:"kind"`
	Age            string `json:"age"`
	ObservedAt     string `json:"observedAt"`
	Recommendation string `json:"recommendation"`
}

type AlternativeTool struct {
	Name         string `json:"name"`
	Binary       string `json:"binary,omitempty"`
	Category     string `json:"category"`
	Providers    string `json:"providers"`
	DriftCommand string `json:"driftCommand,omitempty"`
	DocsURL      string `json:"docsUrl"`
	Detected     bool   `json:"detected"`
}

type workspaceMetadata struct {
	files           []string
	backends        []string
	providerSources []string
	modules         []string
	resourceTypes   []string
	remote          bool
}

var (
	backendRe        = regexp.MustCompile(`(?m)\bbackend\s+"([^"]+)"`)
	cloudBlockRe     = regexp.MustCompile(`(?m)\bcloud\s*\{`)
	providerSourceRe = regexp.MustCompile(`(?m)\bsource\s*=\s*"([^"]+)"`)
	moduleRe         = regexp.MustCompile(`(?m)\bmodule\s+"([^"]+)"`)
	resourceRe       = regexp.MustCompile(`(?m)\b(?:resource|data)\s+"([^"]+)"\s+"([^"]+)"`)
	ansiRe           = regexp.MustCompile(`\x1b\[[0-9;]*m`)
)

func (c *Client) Analyze(ctx context.Context, opts AnalysisOptions) (AnalysisReport, error) {
	if c == nil {
		return AnalysisReport{}, fmt.Errorf("terraform client is nil")
	}
	binary := c.binary
	if override := resolveTerraformBinary(opts.Tool); strings.TrimSpace(opts.Tool) != "" {
		binary = override
	}
	if strings.TrimSpace(binary) == "" {
		binary = "terraform"
	}

	maxLines := opts.MaxOutputLines
	if maxLines <= 0 {
		maxLines = defaultMaxOutputLines
	}

	report := AnalysisReport{
		Workspace:    c.workspace,
		Path:         c.path,
		Tool:         displayToolName(binary),
		Alternatives: DetectAlternatives(),
	}
	if path, err := exec.LookPath(binary); err == nil {
		report.ToolPath = path
	} else {
		report.Warnings = append(report.Warnings, fmt.Sprintf("%s binary not found on PATH", binary))
	}

	info, err := os.Stat(c.path)
	if err != nil {
		return report, fmt.Errorf("terraform workspace path is not readable: %w", err)
	}
	if !info.IsDir() {
		return report, fmt.Errorf("terraform workspace path is not a directory: %s", c.path)
	}

	metadata := scanWorkspace(c.path)
	report.Files = metadata.files
	report.Backends = metadata.backends
	report.ProviderSources = metadata.providerSources
	report.Modules = metadata.modules
	report.Remote = metadata.remote
	report.Mode = workspaceMode(metadata)
	report.StaleArtifacts = detectStaleArtifacts(c.path)

	if len(report.Files) == 0 {
		report.Warnings = append(report.Warnings, "no .tf or .tf.json files found in workspace")
	}
	if len(report.Backends) == 0 && len(report.Files) > 0 {
		report.Recommendations = append(report.Recommendations, "Declare an explicit backend before team or agent workflows depend on this workspace.")
	}
	if len(report.ProviderSources) == 0 && len(metadata.resourceTypes) > 0 {
		report.ProviderSources = inferredProviderSources(metadata.resourceTypes)
	}
	if len(report.ProviderSources) > 0 {
		report.Recommendations = append(report.Recommendations, fmt.Sprintf("Provider families detected: %s.", strings.Join(report.ProviderSources, ", ")))
	}
	if report.Remote {
		report.Recommendations = append(report.Recommendations, "Remote state/backend detected; run drift checks from an environment with the matching cloud credentials and backend access.")
	}

	report.State = c.stateSummary(ctx, binary, maxLines, &report)
	if opts.IncludePlan {
		report.Drift = c.normalPlan(ctx, binary, maxLines)
	} else if opts.CheckDrift {
		report.Drift = c.refreshOnlyDrift(ctx, binary, maxLines)
	}
	if report.Drift != nil && report.Drift.Checked && report.Drift.HasChanges {
		report.Recommendations = append(report.Recommendations, "Drift or pending state refresh detected; reconcile code, imports, or remote objects before applying new changes.")
	}
	if len(report.StaleArtifacts) > 0 {
		report.Recommendations = append(report.Recommendations, "Review stale local state or saved plan artifacts; remove obsolete files after confirming the active backend.")
	}
	report.Warnings = dedupeSorted(report.Warnings)
	report.Recommendations = dedupePreserveOrder(report.Recommendations)
	return report, nil
}

func (c *Client) stateSummary(ctx context.Context, binary string, maxLines int, report *AnalysisReport) *StateSummary {
	output, err := runTerraformCommand(ctx, c.path, binary, 8*time.Second, "state", "list")
	if err != nil {
		if report != nil {
			report.Warnings = append(report.Warnings, fmt.Sprintf("state list unavailable: %v", err))
		}
		return nil
	}
	resources := nonEmptyLines(output)
	if len(resources) == 0 {
		return &StateSummary{ResourceCount: 0}
	}
	types := make(map[string]int)
	for _, resource := range resources {
		if resourceType := resourceTypeFromAddress(resource); resourceType != "" {
			types[resourceType]++
		}
	}
	return &StateSummary{
		ResourceCount: len(resources),
		ResourceTypes: types,
		Sample:        limitStrings(resources, maxLines),
	}
}

func (c *Client) refreshOnlyDrift(ctx context.Context, binary string, maxLines int) *DriftReport {
	args := []string{"plan", "-refresh-only", "-detailed-exitcode", "-no-color", "-compact-warnings", "-input=false"}
	return c.planReport(ctx, binary, args, maxLines)
}

func (c *Client) normalPlan(ctx context.Context, binary string, maxLines int) *DriftReport {
	args := []string{"plan", "-detailed-exitcode", "-no-color", "-compact-warnings", "-input=false"}
	return c.planReport(ctx, binary, args, maxLines)
}

func (c *Client) planReport(ctx context.Context, binary string, args []string, maxLines int) *DriftReport {
	output, exitCode, err := runTerraformCommandDetailed(ctx, c.path, binary, 90*time.Second, args...)
	report := &DriftReport{
		Checked:  true,
		ExitCode: exitCode,
		Command:  binary + " " + strings.Join(args, " "),
		Output:   limitStrings(nonEmptyLines(output), maxLines),
		Summary:  summarizePlanOutput(output),
	}
	if exitCode == 2 {
		report.HasChanges = true
	}
	if err != nil && exitCode != 2 {
		report.Error = err.Error()
	}
	return report
}

func runTerraformCommand(ctx context.Context, dir string, binary string, timeout time.Duration, args ...string) (string, error) {
	output, _, err := runTerraformCommandDetailed(ctx, dir, binary, timeout, args...)
	return output, err
}

func runTerraformCommandDetailed(ctx context.Context, dir string, binary string, timeout time.Duration, args ...string) (string, int, error) {
	runCtx := ctx
	cancel := func() {}
	if timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()

	cmd := exec.CommandContext(runCtx, binary, args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	text := stripANSI(strings.TrimSpace(string(output)))
	exitCode := 0
	if err != nil {
		exitCode = 1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
				exitCode = status.ExitStatus()
			}
		}
		if runCtx.Err() != nil {
			return text, exitCode, runCtx.Err()
		}
		return text, exitCode, fmt.Errorf("%s %s failed: %w%s", binary, strings.Join(args, " "), err, commandOutputSuffix(text))
	}
	return text, exitCode, nil
}

func commandOutputSuffix(output string) string {
	if strings.TrimSpace(output) == "" {
		return ""
	}
	return "\nOutput: " + strings.TrimSpace(output)
}

func stripANSI(value string) string {
	return ansiRe.ReplaceAllString(value, "")
}

func scanWorkspace(root string) workspaceMetadata {
	metadata := workspaceMetadata{}
	seenBackends := map[string]bool{}
	seenSources := map[string]bool{}
	seenModules := map[string]bool{}
	seenResourceTypes := map[string]bool{}
	rootFS, rootErr := os.OpenRoot(root)
	if rootErr != nil {
		return metadata
	}
	defer rootFS.Close()
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".terraform", "node_modules", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if !isTerraformFile(path) {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil
		}
		metadata.files = append(metadata.files, rel)
		data, readErr := readWorkspaceFile(rootFS, rel)
		if readErr != nil {
			return nil
		}
		text := string(data)
		for _, match := range backendRe.FindAllStringSubmatch(text, -1) {
			addSeen(match[1], seenBackends, &metadata.backends)
		}
		if cloudBlockRe.MatchString(text) {
			addSeen("hcp_terraform", seenBackends, &metadata.backends)
		}
		for _, match := range providerSourceRe.FindAllStringSubmatch(text, -1) {
			source := strings.TrimSpace(match[1])
			if looksLikeProviderSource(source) {
				addSeen(source, seenSources, &metadata.providerSources)
			}
		}
		for _, match := range moduleRe.FindAllStringSubmatch(text, -1) {
			addSeen(match[1], seenModules, &metadata.modules)
		}
		for _, match := range resourceRe.FindAllStringSubmatch(text, -1) {
			addSeen(match[1], seenResourceTypes, &metadata.resourceTypes)
		}
		return nil
	})
	sort.Strings(metadata.files)
	sort.Strings(metadata.backends)
	sort.Strings(metadata.providerSources)
	sort.Strings(metadata.modules)
	sort.Strings(metadata.resourceTypes)
	metadata.remote = hasRemoteBackend(metadata.backends)
	return metadata
}

func readWorkspaceFile(rootFS *os.Root, rel string) ([]byte, error) {
	file, err := rootFS.Open(rel)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(file)
}

func isTerraformFile(path string) bool {
	return strings.HasSuffix(path, ".tf") || strings.HasSuffix(path, ".tf.json")
}

func addSeen(value string, seen map[string]bool, values *[]string) {
	value = strings.TrimSpace(value)
	if value == "" || seen[value] {
		return
	}
	seen[value] = true
	*values = append(*values, value)
}

func looksLikeProviderSource(source string) bool {
	if source == "" || strings.HasPrefix(source, ".") || strings.HasPrefix(source, "/") {
		return false
	}
	if strings.Contains(source, "://") {
		return false
	}
	parts := strings.Split(source, "/")
	return len(parts) == 2 || len(parts) == 3
}

func inferredProviderSources(resourceTypes []string) []string {
	providers := map[string]bool{}
	for _, resourceType := range resourceTypes {
		if idx := strings.Index(resourceType, "_"); idx > 0 {
			providers[resourceType[:idx]] = true
		}
	}
	values := make([]string, 0, len(providers))
	for provider := range providers {
		values = append(values, provider)
	}
	sort.Strings(values)
	return values
}

func hasRemoteBackend(backends []string) bool {
	for _, backend := range backends {
		switch strings.ToLower(backend) {
		case "azurerm", "consul", "cos", "gcs", "hcp_terraform", "http", "kubernetes", "oss", "pg", "remote", "s3":
			return true
		}
	}
	return false
}

func workspaceMode(metadata workspaceMetadata) string {
	if len(metadata.files) == 0 {
		return "unconfigured"
	}
	if metadata.remote {
		return "remote-state"
	}
	if len(metadata.backends) > 0 {
		return "local-or-custom-backend"
	}
	return "local-state"
}

func detectStaleArtifacts(root string) []StaleArtifact {
	now := time.Now()
	candidates := []struct {
		path           string
		kind           string
		threshold      time.Duration
		recommendation string
	}{
		{filepath.Join(root, "tfplan"), "saved-plan", stalePlanAge, "Regenerate saved plans before applying; plan files can become stale quickly."},
		{filepath.Join(root, "terraform.tfstate"), "local-state", staleLocalStateAge, "Confirm whether this local state is still authoritative or move state to a remote backend."},
	}
	stale := []StaleArtifact{}
	for _, candidate := range candidates {
		info, err := os.Stat(candidate.path)
		if err != nil || info.IsDir() {
			continue
		}
		age := now.Sub(info.ModTime())
		if age < candidate.threshold {
			continue
		}
		rel, _ := filepath.Rel(root, candidate.path)
		stale = append(stale, StaleArtifact{
			Path:           rel,
			Kind:           candidate.kind,
			Age:            age.Round(time.Second).String(),
			ObservedAt:     info.ModTime().UTC().Format(time.RFC3339),
			Recommendation: candidate.recommendation,
		})
	}
	return stale
}

func resourceTypeFromAddress(address string) string {
	address = strings.TrimSpace(address)
	if address == "" {
		return ""
	}
	parts := strings.Split(address, ".")
	for i := 0; i < len(parts)-1; i++ {
		part := parts[i]
		if part == "data" && i+1 < len(parts) {
			return "data." + parts[i+1]
		}
		if strings.Contains(part, "_") && !strings.Contains(part, "[") {
			return part
		}
	}
	return ""
}

func summarizePlanOutput(output string) []string {
	lines := nonEmptyLines(output)
	summary := []string{}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, "Plan:") ||
			strings.Contains(trimmed, "No changes") ||
			strings.Contains(trimmed, "No changes.") ||
			strings.Contains(trimmed, "Objects have changed outside of Terraform") ||
			strings.Contains(trimmed, "Objects have changed outside of OpenTofu") ||
			strings.Contains(trimmed, "Your infrastructure matches the configuration") {
			summary = append(summary, trimmed)
		}
	}
	return dedupePreserveOrder(summary)
}

func nonEmptyLines(output string) []string {
	raw := strings.Split(strings.TrimSpace(output), "\n")
	lines := make([]string, 0, len(raw))
	for _, line := range raw {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func limitStrings(values []string, max int) []string {
	if max <= 0 || len(values) <= max {
		return values
	}
	limited := append([]string{}, values[:max]...)
	limited = append(limited, fmt.Sprintf("... truncated %d more line(s)", len(values)-max))
	return limited
}

func displayToolName(binary string) string {
	switch strings.TrimSpace(binary) {
	case "tofu":
		return "OpenTofu"
	default:
		return "Terraform"
	}
}

func DetectAlternatives() []AlternativeTool {
	alternatives := []AlternativeTool{
		{
			Name:         "Terraform",
			Binary:       "terraform",
			Category:     "HCL infrastructure as code",
			Providers:    "Multi-cloud via Terraform providers",
			DriftCommand: "terraform plan -refresh-only -detailed-exitcode",
			DocsURL:      "https://developer.hashicorp.com/terraform/cli/commands/plan",
		},
		{
			Name:         "OpenTofu",
			Binary:       "tofu",
			Category:     "Open-source Terraform-compatible HCL",
			Providers:    "Multi-cloud via OpenTofu/Terraform-compatible providers",
			DriftCommand: "tofu plan -refresh-only -detailed-exitcode",
			DocsURL:      "https://opentofu.org/docs/cli/commands/plan/",
		},
		{
			Name:         "Pulumi",
			Binary:       "pulumi",
			Category:     "Programming-language infrastructure as code",
			Providers:    "AWS, Azure, Google Cloud, Kubernetes, Cloudflare, and more",
			DriftCommand: "pulumi refresh --preview-only",
			DocsURL:      "https://www.pulumi.com/docs/iac/operations/stack-management/drift/",
		},
		{
			Name:         "Crossplane",
			Binary:       "kubectl",
			Category:     "Kubernetes control-plane infrastructure",
			Providers:    "External services through Crossplane providers",
			DriftCommand: "kubectl get managed",
			DocsURL:      "https://docs.crossplane.io/latest/packages/providers/",
		},
		{
			Name:         "AWS CDK",
			Binary:       "cdk",
			Category:     "Code-first AWS infrastructure",
			Providers:    "AWS via CloudFormation",
			DriftCommand: "cdk diff",
			DocsURL:      "https://docs.aws.amazon.com/cdk/v2/guide/ref-cli-cmd-diff.html",
		},
		{
			Name:         "AWS CloudFormation",
			Binary:       "aws",
			Category:     "Native AWS declarative infrastructure",
			Providers:    "AWS",
			DriftCommand: "aws cloudformation detect-stack-drift",
			DocsURL:      "https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/using-cfn-stack-drift.html",
		},
		{
			Name:         "Azure Bicep",
			Binary:       "az",
			Category:     "Native Azure declarative infrastructure",
			Providers:    "Azure",
			DriftCommand: "az deployment group what-if",
			DocsURL:      "https://learn.microsoft.com/en-us/azure/azure-resource-manager/bicep/deploy-what-if",
		},
		{
			Name:         "Google Cloud Infrastructure Manager",
			Binary:       "gcloud",
			Category:     "Managed Terraform deployments on Google Cloud",
			Providers:    "Google Cloud",
			DriftCommand: "gcloud infra-manager deployments describe",
			DocsURL:      "https://docs.cloud.google.com/infrastructure-manager/docs",
		},
	}
	for i := range alternatives {
		if alternatives[i].Binary == "" {
			continue
		}
		_, err := exec.LookPath(alternatives[i].Binary)
		alternatives[i].Detected = err == nil
	}
	return alternatives
}

func dedupeSorted(values []string) []string {
	values = dedupePreserveOrder(values)
	sort.Strings(values)
	return values
}

func dedupePreserveOrder(values []string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}
