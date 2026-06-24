package sre

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	tfclient "github.com/bgdnvk/clanker/internal/terraform"
	"github.com/spf13/viper"
)

const (
	DefaultImage          = "ghcr.io/bgdnvk/clanker:latest"
	DefaultAgentName      = "clanker-sre"
	DefaultIngestTokenEnv = "CLANKER_CEREBRO_INGEST_TOKEN"
	DefaultInterval       = 10 * time.Second
)

type ToolStatus struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Path      string `json:"path,omitempty"`
}

type ProviderStatus struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Detail    string `json:"detail,omitempty"`
}

type CapabilityStatus struct {
	Available bool     `json:"available"`
	Detail    string   `json:"detail,omitempty"`
	Signals   []string `json:"signals,omitempty"`
}

type Discovery struct {
	GeneratedAt       string           `json:"generatedAt"`
	Hostname          string           `json:"hostname"`
	OS                string           `json:"os"`
	Arch              string           `json:"arch"`
	ConfigFile        string           `json:"configFile,omitempty"`
	Tools             []ToolStatus     `json:"tools"`
	Providers         []ProviderStatus `json:"providers"`
	Local             CapabilityStatus `json:"local"`
	Docker            CapabilityStatus `json:"docker"`
	Kubernetes        CapabilityStatus `json:"kubernetes"`
	OTel              CapabilityStatus `json:"otel"`
	Databases         CapabilityStatus `json:"databases"`
	CICD              CapabilityStatus `json:"cicd"`
	Terraform         CapabilityStatus `json:"terraform"`
	RecommendedTarget string           `json:"recommendedTarget"`
	Notes             []string         `json:"notes,omitempty"`
}

type PlanOptions struct {
	Target         string
	Image          string
	Name           string
	BackendURL     string
	IngestTokenEnv string
	Provider       string
	DeployID       string
	Interval       time.Duration
}

type InstallFile struct {
	Path    string `json:"path"`
	Mode    string `json:"mode"`
	Content string `json:"content"`
}

type InstallPlan struct {
	Target    string        `json:"target"`
	Summary   string        `json:"summary"`
	Available bool          `json:"available"`
	Warnings  []string      `json:"warnings,omitempty"`
	Commands  []string      `json:"commands,omitempty"`
	Files     []InstallFile `json:"files,omitempty"`
	NextSteps []string      `json:"nextSteps,omitempty"`
	Discovery Discovery     `json:"discovery"`
}

type RunOptions struct {
	Target      string
	AgentID     string
	AgentName   string
	BackendURL  string
	IngestToken string
	Interval    time.Duration
	Once        bool
	Writer      io.Writer
	Provider    string
	DeployID    string
}

func Discover(ctx context.Context) Discovery {
	_ = ctx
	hostname, _ := os.Hostname()
	tools := detectTools([]string{"docker", "kubectl", "helm", "otelcol", "otelcol-contrib", "prometheus", "loki", "aws", "gcloud", "az", "doctl", "hcloud", "vercel", "railway", "flyctl", "fly", "tccli", "sentry-cli", "sentry", "verda", "terraform", "tofu", "pulumi", "cdk", "crossplane", "git"})
	toolMap := make(map[string]ToolStatus, len(tools))
	for _, tool := range tools {
		toolMap[tool.Name] = tool
	}

	discovery := Discovery{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Hostname:    hostname,
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		ConfigFile:  viper.ConfigFileUsed(),
		Tools:       tools,
		Providers:   detectProviders(toolMap),
		Local: CapabilityStatus{
			Available: true,
			Detail:    "local process runtime available",
			Signals:   []string{"host", "process", "filesystem", "local-clanker-config"},
		},
		Docker:     detectDocker(toolMap),
		Kubernetes: detectKubernetes(toolMap),
		OTel:       detectOTel(toolMap),
		Databases:  detectDatabases(),
		CICD:       detectCICD(toolMap),
		Terraform:  detectTerraform(toolMap),
	}

	if discovery.Docker.Available {
		discovery.RecommendedTarget = "docker"
	} else {
		discovery.RecommendedTarget = "local"
		discovery.Notes = append(discovery.Notes, "docker was not detected; local foreground mode is the safest fallback")
	}
	if !discovery.Kubernetes.Available {
		discovery.Notes = append(discovery.Notes, "kubernetes was not detected; helm/k8s install paths stay disabled unless requested")
	}
	if !discovery.OTel.Available {
		discovery.Notes = append(discovery.Notes, "otel was not detected; the SRE bot will use Clanker/provider signals only")
	}
	return discovery
}

func BuildPlan(discovery Discovery, opts PlanOptions) InstallPlan {
	target := normalizeTarget(opts.Target)
	if target == "auto" {
		target = normalizeTarget(discovery.RecommendedTarget)
	}
	if target == "" {
		target = "docker"
	}
	image := strings.TrimSpace(opts.Image)
	if image == "" {
		image = DefaultImage
	}
	name := sanitizeName(opts.Name)
	if name == "" {
		name = DefaultAgentName
	}
	tokenEnv := strings.TrimSpace(opts.IngestTokenEnv)
	if tokenEnv == "" {
		tokenEnv = DefaultIngestTokenEnv
	}
	interval := opts.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	backendURL := strings.TrimSpace(opts.BackendURL)
	if backendURL == "" {
		backendURL = firstNonEmpty(viper.GetString("sre.cerebro_url"), os.Getenv("CLANKER_CEREBRO_URL"), os.Getenv("CLANKER_CLOUD_API_BASE_URL"), viper.GetString("backend.url"))
	}
	provider := strings.ToLower(strings.TrimSpace(firstNonEmpty(opts.Provider, os.Getenv("CLANKER_SRE_PROVIDER"), viper.GetString("sre.provider"))))
	deployID := strings.TrimSpace(firstNonEmpty(opts.DeployID, os.Getenv("CLANKER_SRE_DEPLOY_ID"), viper.GetString("sre.deploy_id")))

	plan := InstallPlan{Target: target, Discovery: discovery}
	switch target {
	case "aws":
		plan = awsCloudVMPlan(discovery, image, name, backendURL, tokenEnv, provider, deployID, interval)
	case "gcp":
		plan = gcpCloudVMPlan(discovery, image, name, backendURL, tokenEnv, provider, deployID, interval)
	case "docker":
		plan = dockerPlan(discovery, image, name, backendURL, tokenEnv, provider, deployID, interval)
	case "local":
		plan = localPlan(discovery, name, backendURL, tokenEnv, provider, deployID, interval)
	case "launchd":
		plan = launchdPlan(discovery, name, backendURL, tokenEnv, provider, deployID, interval)
	case "systemd":
		plan = systemdPlan(discovery, name, backendURL, tokenEnv, provider, deployID, interval)
	case "k8s", "kubernetes":
		plan = k8sPlan(discovery, image, name, backendURL, tokenEnv, provider, deployID, interval)
	case "cloud-vm":
		plan = cloudVMPlan(discovery, image, name, backendURL, tokenEnv, provider, deployID, interval)
	default:
		plan = dockerPlan(discovery, image, name, backendURL, tokenEnv, provider, deployID, interval)
		plan.Warnings = append(plan.Warnings, "unknown target "+target+"; using docker plan")
	}
	plan.Target = target
	plan.Discovery = discovery
	return plan
}

func ApplyPlan(plan InstallPlan, baseDir string) error {
	if strings.TrimSpace(baseDir) == "" {
		dir, err := DefaultStateDir()
		if err != nil {
			return err
		}
		baseDir = filepath.Join(dir, "install")
	}
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return err
	}
	for _, file := range plan.Files {
		name := filepath.Base(file.Path)
		if strings.TrimSpace(name) == "" || name == "." || name == string(filepath.Separator) {
			continue
		}
		mode := os.FileMode(0644)
		if file.Mode == "0755" || file.Mode == "755" {
			mode = 0755
		}
		if err := os.WriteFile(filepath.Join(baseDir, name), []byte(file.Content), mode); err != nil {
			return err
		}
	}
	config := map[string]any{
		"target":      plan.Target,
		"generatedAt": time.Now().UTC().Format(time.RFC3339),
		"commands":    plan.Commands,
		"warnings":    plan.Warnings,
	}
	payload, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(baseDir, "sre-install.json"), append(payload, '\n'), 0644)
}

func Run(ctx context.Context, opts RunOptions) error {
	writer := opts.Writer
	if writer == nil {
		writer = os.Stdout
	}
	interval := opts.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	agentID := strings.TrimSpace(firstNonEmpty(opts.AgentID, os.Getenv("CLANKER_SRE_AGENT_ID")))
	deployID := strings.TrimSpace(firstNonEmpty(opts.DeployID, viper.GetString("sre.deploy_id"), os.Getenv("CLANKER_SRE_DEPLOY_ID")))
	if agentID == "" {
		agentID = firstNonEmpty(deployID, defaultAgentID())
	}
	agentName := strings.TrimSpace(opts.AgentName)
	if agentName == "" {
		agentName = DefaultAgentName
	}

	for {
		discovery := Discover(ctx)
		observations := CollectObservations(ctx, discovery)
		if err := PostHeartbeat(ctx, discovery, observations, opts, agentID, agentName); err != nil {
			fmt.Fprintf(writer, "[sre] heartbeat skipped: %v\n", err)
		} else {
			fmt.Fprintf(writer, "[sre] heartbeat sent for %s (%s)\n", agentName, agentID)
		}
		if opts.Once {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

func PostHeartbeat(ctx context.Context, discovery Discovery, observations map[string]any, opts RunOptions, agentID string, agentName string) error {
	baseURL := NormalizeAPIBaseURL(firstNonEmpty(opts.BackendURL, viper.GetString("sre.cerebro_url"), os.Getenv("CLANKER_CEREBRO_URL"), os.Getenv("CLANKER_CLOUD_API_BASE_URL"), viper.GetString("backend.url")))
	if baseURL == "" {
		baseURL = discoverLocalCloudBaseURL(ctx)
	}
	if baseURL == "" {
		return fmt.Errorf("no Cerebro URL configured")
	}
	token := strings.TrimSpace(firstNonEmpty(opts.IngestToken, viper.GetString("sre.ingest_token"), os.Getenv(DefaultIngestTokenEnv)))

	// Detect provider from RunOptions or discovery
	deployID := strings.TrimSpace(firstNonEmpty(opts.DeployID, viper.GetString("sre.deploy_id"), os.Getenv("CLANKER_SRE_DEPLOY_ID")))
	provider := strings.ToLower(strings.TrimSpace(firstNonEmpty(opts.Provider, viper.GetString("sre.provider"), os.Getenv("CLANKER_SRE_PROVIDER"))))
	if provider == "" {
		// Infer primary provider from discovery
		if len(discovery.Providers) > 0 {
			provider = discovery.Providers[0].Name
		}
	}
	if provider == "" {
		provider = "local"
	}

	payload := map[string]any{
		"runId":             agentID,
		"type":              "agent.running",
		"source":            "clanker-sre",
		"title":             "Clanker SRE heartbeat",
		"message":           fmt.Sprintf("%s heartbeat from %s (%d findings)", agentName, discovery.Hostname, len(BuildFindings(discovery, observations))),
		"agentId":           agentID,
		"agentName":         agentName,
		"deployId":          deployID,
		"provider":          provider,
		"target":            normalizeTarget(opts.Target),
		"recommendedTarget": discovery.RecommendedTarget,
		"discovery":         discovery,
		"observations":      observations,
		"findings":          BuildFindings(discovery, observations),
		"category":          "sre.discovery",
		"brainKind":         "state",
		"brainStatus":       "active",
		"brainTitle":        fmt.Sprintf("%s SRE heartbeat from %s", strings.ToUpper(provider), discovery.Hostname),
		"brainTags":         []string{"sre", "heartbeat", provider, normalizeTarget(opts.Target)},
	}

	_, _, _ = flushQueuedHeartbeats(ctx, baseURL, token)
	if err := postHeartbeatPayload(ctx, baseURL, token, payload); err != nil {
		if queueErr := enqueueHeartbeat(payload); queueErr != nil {
			return fmt.Errorf("%w (queue failed: %v)", err, queueErr)
		}
		return err
	}
	_, _, _ = flushQueuedHeartbeats(ctx, baseURL, token)
	return nil
}

func postHeartbeatPayload(ctx context.Context, baseURL string, token string, payload map[string]any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/cerebro/hooks/sre", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 12 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("Cerebro ingest returned %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return nil
}

func NormalizeAPIBaseURL(value string) string {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	if value == "" {
		return ""
	}
	if strings.HasSuffix(value, "/api") {
		return value
	}
	return value + "/api"
}

func DefaultStateDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".clanker", "sre"), nil
}

func FormatPlanText(plan InstallPlan) string {
	var out strings.Builder
	out.WriteString("Clanker SRE install plan\n")
	out.WriteString("Target: " + plan.Target + "\n")
	out.WriteString("Summary: " + plan.Summary + "\n")
	if len(plan.Warnings) > 0 {
		out.WriteString("\nWarnings:\n")
		for _, warning := range plan.Warnings {
			out.WriteString("- " + warning + "\n")
		}
	}
	if len(plan.Commands) > 0 {
		out.WriteString("\nCommands:\n")
		for _, command := range plan.Commands {
			out.WriteString(command + "\n")
		}
	}
	if len(plan.Files) > 0 {
		out.WriteString("\nFiles generated by --apply:\n")
		for _, file := range plan.Files {
			out.WriteString("- " + file.Path + "\n")
		}
	}
	if len(plan.NextSteps) > 0 {
		out.WriteString("\nNext steps:\n")
		for _, step := range plan.NextSteps {
			out.WriteString("- " + step + "\n")
		}
	}
	return out.String()
}

func CollectObservations(ctx context.Context, discovery Discovery) map[string]any {
	observations := map[string]any{
		"generatedAt": time.Now().UTC().Format(time.RFC3339),
		"host": map[string]any{
			"hostname": discovery.Hostname,
			"os":       discovery.OS,
			"arch":     discovery.Arch,
			"cpus":     runtime.NumCPU(),
		},
	}

	if stateDir, err := DefaultStateDir(); err == nil {
		host := observations["host"].(map[string]any)
		host["stateDir"] = stateDir
	}

	if metrics := collectHostMetrics(ctx); len(metrics) > 0 {
		observations["metrics"] = metrics
	}

	if logs := collectHostLogs(ctx); len(logs) > 0 {
		observations["logs"] = logs
	}

	if discovery.Docker.Available {
		docker := map[string]any{}
		if output, err := runCommandOutput(ctx, 2*time.Second, "docker", "ps", "--format", "{{.Names}}|{{.Image}}|{{.Status}}"); err == nil {
			docker["containers"] = splitLinesLimited(output, 40)
		}
		if output, err := runCommandOutput(ctx, 2*time.Second, "docker", "stats", "--no-stream", "--format", "{{.Name}}|{{.CPUPerc}}|{{.MemUsage}}|{{.NetIO}}|{{.BlockIO}}"); err == nil {
			docker["stats"] = splitLinesLimited(output, 40)
		}
		if output, err := runCommandOutput(ctx, 2*time.Second, "docker", "logs", "--tail", "80", DefaultAgentName); err == nil {
			docker["sreLogs"] = splitLinesLimited(output, 80)
		}
		if len(docker) > 0 {
			observations["docker"] = docker
		}
	}

	if discovery.Kubernetes.Available {
		k8s := map[string]any{}
		if output, err := runCommandOutput(ctx, 2*time.Second, "kubectl", "config", "get-contexts", "-o", "name"); err == nil {
			k8s["contexts"] = splitLinesLimited(output, 20)
		}
		if output, err := runCommandOutput(ctx, 1500*time.Millisecond, "kubectl", "config", "current-context"); err == nil {
			k8s["currentContext"] = strings.TrimSpace(output)
		}
		if output, err := runCommandOutput(ctx, 2*time.Second, "kubectl", "get", "nodes", "-o", "name"); err == nil {
			k8s["nodes"] = splitLinesLimited(output, 30)
		}
		if output, err := runCommandOutput(ctx, 2*time.Second, "kubectl", "top", "nodes", "--no-headers"); err == nil {
			k8s["nodeMetrics"] = splitLinesLimited(output, 30)
		}
		if output, err := runCommandOutput(ctx, 2*time.Second, "kubectl", "top", "pods", "-A", "--no-headers"); err == nil {
			k8s["podMetrics"] = splitLinesLimited(output, 80)
		}
		if output, err := runCommandOutput(ctx, 2*time.Second, "kubectl", "logs", "-n", "clanker", "-l", "app="+DefaultAgentName, "--tail=80"); err == nil {
			k8s["sreLogs"] = splitLinesLimited(output, 80)
		}
		if len(k8s) > 0 {
			observations["kubernetes"] = k8s
		}
	}

	if discovery.OTel.Available {
		observations["otel"] = map[string]any{
			"signals": discovery.OTel.Signals,
		}
	}

	if discovery.Databases.Available {
		observations["databases"] = map[string]any{
			"detail":  discovery.Databases.Detail,
			"signals": discovery.Databases.Signals,
		}
	}

	if discovery.CICD.Available {
		cicd := map[string]any{
			"signals": discovery.CICD.Signals,
		}
		if output, err := runCommandOutput(ctx, 1500*time.Millisecond, "git", "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
			cicd["gitBranch"] = strings.TrimSpace(output)
		}
		if output, err := runCommandOutput(ctx, 1500*time.Millisecond, "git", "rev-parse", "--short", "HEAD"); err == nil {
			cicd["gitCommit"] = strings.TrimSpace(output)
		}
		if output, err := runCommandOutput(ctx, 1500*time.Millisecond, "git", "status", "--short"); err == nil {
			cicd["gitStatus"] = splitLinesLimited(output, 40)
		}
		observations["cicd"] = cicd
	}

	if discovery.Terraform.Available {
		terraform := map[string]any{
			"signals": discovery.Terraform.Signals,
		}
		binary := terraformSREBinary()
		if output, err := runCommandOutput(ctx, 1500*time.Millisecond, binary, "workspace", "show"); err == nil {
			terraform["workspace"] = strings.TrimSpace(output)
		}
		workspace := terraformWorkspaceForSRE()
		if workspace != "" {
			client, err := tfclient.NewClientWithTool(workspace, binary)
			if err != nil {
				terraform["analysisError"] = err.Error()
			} else if report, err := client.Analyze(ctx, tfclient.AnalysisOptions{
				Tool:           binary,
				CheckDrift:     terraformSREDriftEnabled(),
				MaxOutputLines: 20,
			}); err == nil {
				terraform["analysis"] = report
			} else {
				terraform["analysisError"] = err.Error()
			}
		}
		observations["terraform"] = terraform
	}

	accounts := map[string]any{}
	if hasProvider(discovery.Providers, "aws") {
		if output, err := runCommandOutput(ctx, 2*time.Second, "aws", "sts", "get-caller-identity", "--output", "json"); err == nil {
			var payload map[string]any
			if json.Unmarshal([]byte(output), &payload) == nil {
				accounts["aws"] = payload
			}
		}
	}
	if hasProvider(discovery.Providers, "gcp") {
		if output, err := runCommandOutput(ctx, 1500*time.Millisecond, "gcloud", "config", "get-value", "project", "--quiet"); err == nil {
			accounts["gcp"] = map[string]any{"project": strings.TrimSpace(output)}
		}
	}
	if hasProvider(discovery.Providers, "azure") {
		if output, err := runCommandOutput(ctx, 2*time.Second, "az", "account", "show", "--query", "{id:id,name:name,tenantId:tenantId}", "-o", "json"); err == nil {
			var payload map[string]any
			if json.Unmarshal([]byte(output), &payload) == nil {
				accounts["azure"] = payload
			}
		}
	}
	if hasProvider(discovery.Providers, "digitalocean") {
		if output, err := runCommandOutput(ctx, 2*time.Second, "doctl", "account", "get", "--output", "json"); err == nil {
			accounts["digitalocean"] = splitLinesLimited(output, 1)
		}
	}
	if hasProvider(discovery.Providers, "cloudflare") {
		if output, err := runCommandOutput(ctx, 2*time.Second, "sh", "-lc", "printf '%s' \"$CLOUDFLARE_API_TOKEN$CF_API_TOKEN\" | wc -c"); err == nil {
			accounts["cloudflare"] = map[string]any{"tokenLength": strings.TrimSpace(output)}
		}
	}
	if hasProvider(discovery.Providers, "flyio") {
		accounts["flyio"] = map[string]any{
			"tokenConfigured": hasAnyEnv("FLY_API_TOKEN", "FLY_ACCESS_TOKEN") || viper.GetString("flyio.api_token") != "",
			"org":             firstNonEmpty(os.Getenv("FLY_ORG"), os.Getenv("FLY_ORG_SLUG"), viper.GetString("flyio.org_slug")),
		}
	}
	if hasProvider(discovery.Providers, "tencent") {
		accounts["tencent"] = map[string]any{
			"secretIDConfigured":  hasAnyEnv("TENCENTCLOUD_SECRET_ID", "TENCENT_SECRET_ID") || viper.GetString("tencent.secret_id") != "",
			"secretKeyConfigured": hasAnyEnv("TENCENTCLOUD_SECRET_KEY", "TENCENT_SECRET_KEY") || viper.GetString("tencent.secret_key") != "",
			"region":              firstNonEmpty(os.Getenv("TENCENT_REGION"), os.Getenv("TENCENTCLOUD_REGION"), viper.GetString("tencent.region")),
			"cliConfigured":       toolAvailable(discovery.Tools, "tccli"),
		}
	}
	if hasProvider(discovery.Providers, "supabase") {
		accounts["supabase"] = map[string]any{
			"tokenConfigured": hasAnyEnv("SUPABASE_ACCESS_TOKEN", "SUPABASE_API_TOKEN", "SUPABASE_TOKEN") || viper.GetString("supabase.api_token") != "",
		}
	}
	if hasProvider(discovery.Providers, "sentry") {
		accounts["sentry"] = map[string]any{
			"tokenConfigured": hasAnyEnv("SENTRY_AUTH_TOKEN") || viper.GetString("sentry.auth_token") != "",
			"org":             firstNonEmpty(os.Getenv("SENTRY_ORG"), viper.GetString("sentry.org")),
			"host":            firstNonEmpty(os.Getenv("SENTRY_HOST"), viper.GetString("sentry.host")),
			"cliConfigured":   toolAvailable(discovery.Tools, "sentry-cli", "sentry"),
		}
	}
	if len(accounts) > 0 {
		observations["providerAccounts"] = accounts
	}

	// --- deep provider signals ---
	if hasProvider(discovery.Providers, "aws") {
		observations["aws"] = collectAWSSignals(ctx)
	}
	if hasProvider(discovery.Providers, "gcp") {
		observations["gcp"] = collectGCPSignals(ctx)
	}
	if hasProvider(discovery.Providers, "azure") {
		observations["azure"] = collectAzureSignals(ctx)
	}
	if hasProvider(discovery.Providers, "digitalocean") {
		observations["digitalocean"] = collectDOSignals(ctx)
	}
	if hasProvider(discovery.Providers, "hetzner") {
		observations["hetzner"] = collectHetznerSignals(ctx)
	}

	// --- extra k8s warnings ---
	if discovery.Kubernetes.Available {
		observations["kubernetesWarnings"] = collectK8sWarnings(ctx)
	}

	// --- extra docker signals ---
	if discovery.Docker.Available {
		observations["dockerExtended"] = collectDockerExtended(ctx)
	}

	// --- extra host signals ---
	observations["hostExtended"] = collectHostExtended(ctx)

	return observations
}

func BuildFindings(discovery Discovery, observations map[string]any) []string {
	findings := []string{}
	if discovery.Docker.Available {
		if docker, ok := observations["docker"].(map[string]any); ok {
			if containers, ok := docker["containers"].([]string); ok {
				findings = append(findings, fmt.Sprintf("docker detected with %d running containers sampled", len(containers)))
			}
		}
	} else {
		findings = append(findings, "docker runtime not detected")
	}
	if discovery.Kubernetes.Available {
		findings = append(findings, "kubernetes control-plane access detected")
		if k8s, ok := observations["kubernetes"].(map[string]any); ok {
			if nodeMetrics, ok := k8s["nodeMetrics"].([]string); ok && len(nodeMetrics) > 0 {
				findings = append(findings, fmt.Sprintf("kubernetes metrics sampled for %d nodes", len(nodeMetrics)))
			}
		}
	}
	if discovery.OTel.Available {
		findings = append(findings, "otel signals detected")
	}
	if discovery.Databases.Available {
		findings = append(findings, "database connectivity signals detected")
	}
	if discovery.CICD.Available {
		findings = append(findings, "ci/cd signals detected")
	}
	if discovery.Terraform.Available {
		findings = append(findings, "iac workspace signals detected")
		if tf, ok := observations["terraform"].(map[string]any); ok {
			if analysis, ok := tf["analysis"].(tfclient.AnalysisReport); ok && analysis.Drift != nil && analysis.Drift.HasChanges {
				findings = append(findings, "ALERT: terraform/opentofu drift or pending plan changes detected")
			}
		}
	}
	if logs, ok := observations["logs"].(map[string]any); ok {
		if journal, ok := logs["journal"].([]string); ok && len(journal) > 0 {
			findings = append(findings, fmt.Sprintf("host journal sampled (%d lines)", len(journal)))
		}
	}

	// --- AWS urgent signals ---
	if aws, ok := observations["aws"].(map[string]any); ok {
		if alarms, ok := aws["cloudwatchAlarms"]; ok {
			if list, ok := alarms.([]any); ok && len(list) > 0 {
				findings = append(findings, fmt.Sprintf("ALERT: %d CloudWatch alarms in ALARM state", len(list)))
			}
		}
		if ctErrors, ok := aws["cloudtrailRecentErrors"]; ok {
			if list, ok := ctErrors.([]any); ok && len(list) > 0 {
				findings = append(findings, fmt.Sprintf("AWS CloudTrail: %d recent error events", len(list)))
			}
		}
		if noMFA, ok := aws["iamUsersWithoutMFA"]; ok {
			if list, ok := noMFA.([]string); ok && len(list) > 0 {
				findings = append(findings, fmt.Sprintf("SECURITY: %d IAM users without MFA", len(list)))
			}
		}
		if fns, ok := aws["lambdaFunctions"]; ok {
			if list, ok := fns.([]any); ok && len(list) > 0 {
				findings = append(findings, fmt.Sprintf("AWS Lambda: %d functions tracked", len(list)))
			}
		}
		if rdsList, ok := aws["rdsInstances"]; ok {
			if list, ok := rdsList.([]any); ok && len(list) > 0 {
				findings = append(findings, fmt.Sprintf("AWS RDS: %d instances tracked", len(list)))
			}
		}
	}

	// --- Azure urgent signals ---
	if az, ok := observations["azure"].(map[string]any); ok {
		if fired, ok := az["monitorFiredAlerts"]; ok {
			if list, ok := fired.([]any); ok && len(list) > 0 {
				findings = append(findings, fmt.Sprintf("ALERT: %d Azure Monitor alerts fired", len(list)))
			}
		}
		if fails, ok := az["activityLogFailures"]; ok {
			if list, ok := fails.([]any); ok && len(list) > 0 {
				findings = append(findings, fmt.Sprintf("Azure: %d failed activity log operations (last 1h)", len(list)))
			}
		}
	}

	// --- GCP urgent signals ---
	if gcp, ok := observations["gcp"].(map[string]any); ok {
		if errLogs, ok := gcp["recentErrorLogs"]; ok {
			if list, ok := errLogs.([]any); ok && len(list) > 0 {
				findings = append(findings, fmt.Sprintf("GCP: %d recent ERROR/CRITICAL log entries", len(list)))
			}
		}
		if errReport, ok := gcp["errorReportingEvents"]; ok {
			if list, ok := errReport.([]any); ok && len(list) > 0 {
				findings = append(findings, fmt.Sprintf("GCP Error Reporting: %d events", len(list)))
			}
		}
	}

	// --- Kubernetes urgent signals ---
	if k8sW, ok := observations["kubernetesWarnings"].(map[string]any); ok {
		if unhealthy, ok := k8sW["unhealthyPods"].([]string); ok && len(unhealthy) > 0 {
			findings = append(findings, fmt.Sprintf("ALERT: %d unhealthy pods (CrashLoop/OOMKilled/Error)", len(unhealthy)))
		}
		if nodeIssues, ok := k8sW["nodeIssues"].([]string); ok && len(nodeIssues) > 0 {
			findings = append(findings, fmt.Sprintf("ALERT: %d nodes with pressure/not-ready conditions", len(nodeIssues)))
		}
		if degraded, ok := k8sW["degradedDeployments"].([]string); ok && len(degraded) > 0 {
			findings = append(findings, fmt.Sprintf("k8s: %d deployments with unavailable replicas", len(degraded)))
		}
		if atMax, ok := k8sW["hpaAtMax"].([]string); ok && len(atMax) > 0 {
			findings = append(findings, fmt.Sprintf("k8s: %d HPAs at max replicas (scaling ceiling hit)", len(atMax)))
		}
		if pendingPVC, ok := k8sW["pendingPVCs"].([]string); ok && len(pendingPVC) > 0 {
			findings = append(findings, fmt.Sprintf("k8s: %d PVCs stuck in Pending", len(pendingPVC)))
		}
		if failedJobs, ok := k8sW["failedJobs"].([]string); ok && len(failedJobs) > 0 {
			findings = append(findings, fmt.Sprintf("k8s: %d failed jobs detected", len(failedJobs)))
		}
		if noEP, ok := k8sW["servicesWithNoEndpoints"].([]string); ok && len(noEP) > 0 {
			findings = append(findings, fmt.Sprintf("k8s: %d services with no endpoints (broken selectors)", len(noEP)))
		}
		if highRestart, ok := k8sW["highRestartPods"].([]string); ok && len(highRestart) > 0 {
			findings = append(findings, fmt.Sprintf("k8s: %d pods with restart count > 5", len(highRestart)))
		}
		if warnEvents, ok := k8sW["warningEvents"].([]string); ok && len(warnEvents) > 0 {
			findings = append(findings, fmt.Sprintf("k8s: %d warning events sampled", len(warnEvents)))
		}
	}

	// --- host extended ---
	if hostExt, ok := observations["hostExtended"].(map[string]any); ok {
		if failed, ok := hostExt["failedSystemdServices"].([]string); ok && len(failed) > 0 {
			findings = append(findings, fmt.Sprintf("ALERT: %d systemd services in failed state", len(failed)))
		}
	}

	for _, provider := range discovery.Providers {
		if provider.Available {
			findings = append(findings, strings.ToLower(provider.Name)+" provider context detected")
		}
	}
	return findings
}

func collectHostMetrics(ctx context.Context) map[string]any {
	metrics := map[string]any{}
	if output, err := runCommandOutput(ctx, 1500*time.Millisecond, "uptime"); err == nil {
		metrics["uptime"] = strings.TrimSpace(output)
	}
	if output, err := runCommandOutput(ctx, 1500*time.Millisecond, "df", "-h"); err == nil {
		metrics["disk"] = splitLinesLimited(output, 30)
	}
	if output, err := runCommandOutput(ctx, 1500*time.Millisecond, "sh", "-lc", "vm_stat 2>/dev/null | head -n 20 || free -h 2>/dev/null || true"); err == nil {
		metrics["memory"] = splitLinesLimited(output, 30)
	}
	return metrics
}

func collectHostLogs(ctx context.Context) map[string]any {
	logs := map[string]any{}
	if output, err := runCommandOutput(ctx, 2*time.Second, "journalctl", "-n", "120", "--no-pager", "--output=short-iso"); err == nil {
		logs["journal"] = splitLinesLimited(output, 120)
	}
	if output, err := runCommandOutput(ctx, 1500*time.Millisecond, "sh", "-lc", "tail -n 80 /var/log/system.log 2>/dev/null || tail -n 80 /var/log/syslog 2>/dev/null || true"); err == nil {
		if tail := splitLinesLimited(output, 80); len(tail) > 0 {
			logs["system"] = tail
		}
	}
	return logs
}

func detectTools(names []string) []ToolStatus {
	tools := make([]ToolStatus, 0, len(names))
	for _, name := range names {
		path, err := exec.LookPath(name)
		tools = append(tools, ToolStatus{Name: name, Available: err == nil, Path: path})
	}
	return tools
}

func toolAvailable(tools []ToolStatus, names ...string) bool {
	wanted := map[string]bool{}
	for _, name := range names {
		name = strings.TrimSpace(strings.ToLower(name))
		if name != "" {
			wanted[name] = true
		}
	}
	for _, tool := range tools {
		if wanted[strings.TrimSpace(strings.ToLower(tool.Name))] && tool.Available {
			return true
		}
	}
	return false
}

func hasProvider(providers []ProviderStatus, id string) bool {
	id = strings.TrimSpace(strings.ToLower(id))
	if id == "" {
		return false
	}
	for _, provider := range providers {
		if strings.ToLower(strings.TrimSpace(provider.ID)) == id && provider.Available {
			return true
		}
	}
	return false
}

func runCommandOutput(parent context.Context, timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	command := exec.CommandContext(ctx, name, args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if stderr.Len() > 0 {
			return "", fmt.Errorf("%w: %s", err, truncateText(stderr.String(), 220))
		}
		return "", err
	}
	return strings.TrimSpace(stdout.String()), nil
}

func splitLinesLimited(value string, limit int) []string {
	if limit <= 0 {
		return []string{}
	}
	lines := []string{}
	for _, line := range strings.Split(value, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		lines = append(lines, trimmed)
		if len(lines) >= limit {
			break
		}
	}
	return lines
}

func truncateText(value string, max int) string {
	value = strings.TrimSpace(value)
	if max <= 0 || len(value) <= max {
		return value
	}
	return strings.TrimSpace(value[:max]) + "..."
}

func detectProviders(tools map[string]ToolStatus) []ProviderStatus {
	providers := []ProviderStatus{
		provider("aws", "AWS", tools["aws"].Available || hasAnyEnv("AWS_PROFILE", "AWS_ACCESS_KEY_ID") || viper.IsSet("infra.aws"), "aws cli/config/env detected"),
		provider("gcp", "GCP", tools["gcloud"].Available || hasAnyEnv("GOOGLE_CLOUD_PROJECT", "GCLOUD_PROJECT") || viper.GetString("infra.gcp.project_id") != "", "gcloud/config/env detected"),
		provider("azure", "Azure", tools["az"].Available || hasAnyEnv("AZURE_SUBSCRIPTION_ID") || viper.GetString("infra.azure.subscription_id") != "", "az/config/env detected"),
		provider("cloudflare", "Cloudflare", hasAnyEnv("CLOUDFLARE_API_TOKEN", "CF_API_TOKEN") || viper.GetString("cloudflare.api_token") != "", "cloudflare token/config detected"),
		provider("digitalocean", "DigitalOcean", tools["doctl"].Available || hasAnyEnv("DO_API_TOKEN", "DIGITALOCEAN_ACCESS_TOKEN") || viper.GetString("digitalocean.api_token") != "", "doctl/token/config detected"),
		provider("hetzner", "Hetzner", tools["hcloud"].Available || hasAnyEnv("HCLOUD_TOKEN", "HETZNER_API_TOKEN") || viper.GetString("hetzner.api_token") != "", "hcloud/token/config detected"),
		provider("vercel", "Vercel", tools["vercel"].Available || hasAnyEnv("VERCEL_TOKEN") || viper.GetString("vercel.token") != "", "vercel cli/token/config detected"),
		provider("railway", "Railway", tools["railway"].Available || hasAnyEnv("RAILWAY_TOKEN") || viper.GetString("railway.token") != "", "railway cli/token/config detected"),
		provider("flyio", "Fly.io", tools["flyctl"].Available || tools["fly"].Available || hasAnyEnv("FLY_API_TOKEN", "FLY_ACCESS_TOKEN") || viper.GetString("flyio.api_token") != "", "fly cli/token/config detected"),
		provider("verda", "Verda", tools["verda"].Available || hasAnyEnv("VERDA_CLIENT_ID", "VERDA_CLIENT_SECRET", "VERDA_PROJECT_ID") || viper.GetString("verda.client_id") != "", "verda cli/config/env detected"),
		provider("tencent", "Tencent Cloud", tools["tccli"].Available || hasAnyEnv("TENCENTCLOUD_SECRET_ID", "TENCENT_SECRET_ID") || viper.GetString("tencent.secret_id") != "", "tccli/config/env detected"),
		provider("supabase", "Supabase", hasAnyEnv("SUPABASE_ACCESS_TOKEN", "SUPABASE_API_TOKEN", "SUPABASE_TOKEN") || viper.GetString("supabase.api_token") != "", "supabase token/config detected"),
		provider("sentry", "Sentry", tools["sentry-cli"].Available || tools["sentry"].Available || hasAnyEnv("SENTRY_AUTH_TOKEN") || viper.GetString("sentry.auth_token") != "", "sentry cli/token/config detected"),
	}
	return providers
}

func provider(id string, name string, available bool, detail string) ProviderStatus {
	if !available {
		detail = "not detected"
	}
	return ProviderStatus{ID: id, Name: name, Available: available, Detail: detail}
}

func detectDocker(tools map[string]ToolStatus) CapabilityStatus {
	if tools["docker"].Available {
		return CapabilityStatus{Available: true, Detail: "docker cli detected", Signals: []string{"container-runtime"}}
	}
	return CapabilityStatus{Available: false, Detail: "docker cli not detected"}
}

func detectKubernetes(tools map[string]ToolStatus) CapabilityStatus {
	signals := []string{}
	if tools["kubectl"].Available {
		signals = append(signals, "kubectl")
	}
	if tools["helm"].Available {
		signals = append(signals, "helm")
	}
	if path := kubeconfigPath(); path != "" {
		signals = append(signals, "kubeconfig:"+path)
	}
	if len(signals) == 0 {
		return CapabilityStatus{Available: false, Detail: "no kubectl, helm, or kubeconfig detected"}
	}
	return CapabilityStatus{Available: true, Detail: "kubernetes signals detected", Signals: signals}
}

func detectOTel(tools map[string]ToolStatus) CapabilityStatus {
	signals := []string{}
	if tools["otelcol"].Available {
		signals = append(signals, "otelcol")
	}
	if tools["otelcol-contrib"].Available {
		signals = append(signals, "otelcol-contrib")
	}
	if hasAnyEnv("OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_SERVICE_NAME") {
		signals = append(signals, "otel-env")
	}
	if len(signals) == 0 {
		return CapabilityStatus{Available: false, Detail: "otel collector/env not detected"}
	}
	return CapabilityStatus{Available: true, Detail: "otel signals detected", Signals: signals}
}

func detectDatabases() CapabilityStatus {
	if viper.IsSet("databases.connections") || viper.GetString("databases.default_connection") != "" || hasAnyEnv("DATABASE_URL", "POSTGRES_URL", "MYSQL_DSN") {
		return CapabilityStatus{Available: true, Detail: "database config/env detected", Signals: []string{"databases"}}
	}
	return CapabilityStatus{Available: false, Detail: "no database config detected"}
}

func detectCICD(tools map[string]ToolStatus) CapabilityStatus {
	if tools["git"].Available || viper.IsSet("github.repos") || viper.GetString("github.default_repo") != "" || hasAnyEnv("GITHUB_TOKEN", "GH_TOKEN") {
		return CapabilityStatus{Available: true, Detail: "git/github signals detected", Signals: []string{"git", "github"}}
	}
	return CapabilityStatus{Available: false, Detail: "no ci/cd signals detected"}
}

func detectTerraform(tools map[string]ToolStatus) CapabilityStatus {
	signals := []string{}
	for _, tool := range []string{"terraform", "tofu", "pulumi", "cdk", "crossplane"} {
		if tools[tool].Available {
			signals = append(signals, tool)
		}
	}
	if viper.IsSet("terraform.workspaces") || viper.GetString("terraform.default_workspace") != "" || viper.GetString("terraform.workspace") != "" {
		signals = append(signals, "terraform-config")
	}
	if len(signals) > 0 {
		return CapabilityStatus{Available: true, Detail: "iac cli/config detected", Signals: dedupeStrings(signals)}
	}
	return CapabilityStatus{Available: false, Detail: "iac tooling not detected"}
}

func terraformSREBinary() string {
	if tool := strings.TrimSpace(os.Getenv("CLANKER_TERRAFORM_TOOL")); tool != "" {
		if strings.EqualFold(tool, "opentofu") {
			return "tofu"
		}
		return tool
	}
	if _, err := exec.LookPath("terraform"); err == nil {
		return "terraform"
	}
	if _, err := exec.LookPath("tofu"); err == nil {
		return "tofu"
	}
	return "terraform"
}

func terraformSREDriftEnabled() bool {
	return envTruthy("CLANKER_TERRAFORM_DRIFT") || viper.GetBool("terraform.sre_drift")
}

func terraformWorkspaceForSRE() string {
	for _, value := range []string{
		os.Getenv("CLANKER_TERRAFORM_WORKSPACE"),
		viper.GetString("terraform.workspace"),
		viper.GetString("terraform.default_workspace"),
	} {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	cwd, err := os.Getwd()
	if err == nil && directoryHasTerraformFiles(cwd) {
		return cwd
	}
	return ""
}

func directoryHasTerraformFiles(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".tf") || strings.HasSuffix(name, ".tf.json") {
			return true
		}
	}
	return false
}

func envTruthy(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

func dedupeStrings(values []string) []string {
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

func sreRunArgs(target string, interval time.Duration, provider string, deployID string) []string {
	args := []string{"sre", "run", "--sre", "--target", target, "--interval", interval.String()}
	if strings.TrimSpace(provider) != "" {
		args = append(args, "--provider", strings.TrimSpace(provider))
	}
	if strings.TrimSpace(deployID) != "" {
		args = append(args, "--deploy-id", strings.TrimSpace(deployID))
	}
	return args
}

func sreRunShell(target string, interval time.Duration, provider string, deployID string) string {
	args := sreRunArgs(target, interval, provider, deployID)
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, fmt.Sprintf("%q", arg))
	}
	return strings.Join(quoted, " ")
}

func sreRunJSONArgs(target string, interval time.Duration, provider string, deployID string) string {
	data, _ := json.Marshal(sreRunArgs(target, interval, provider, deployID))
	return string(data)
}

func sreDockerEnvArgs(backendURL string, tokenEnv string, provider string, deployID string) string {
	parts := []string{fmt.Sprintf("-e CLANKER_CEREBRO_URL=%q", backendURL), fmt.Sprintf("-e %s=\"$%s\"", tokenEnv, tokenEnv)}
	if strings.TrimSpace(provider) != "" {
		parts = append(parts, fmt.Sprintf("-e CLANKER_SRE_PROVIDER=%q", strings.TrimSpace(provider)))
	}
	if strings.TrimSpace(deployID) != "" {
		parts = append(parts, fmt.Sprintf("-e CLANKER_SRE_DEPLOY_ID=%q", strings.TrimSpace(deployID)))
	}
	return strings.Join(parts, " ")
}

func sreShellEnvPrefix(backendURL string, tokenEnv string, provider string, deployID string) string {
	parts := []string{fmt.Sprintf("CLANKER_CEREBRO_URL=%q", backendURL), fmt.Sprintf("%s=\"$%s\"", tokenEnv, tokenEnv)}
	if strings.TrimSpace(provider) != "" {
		parts = append(parts, fmt.Sprintf("CLANKER_SRE_PROVIDER=%q", strings.TrimSpace(provider)))
	}
	if strings.TrimSpace(deployID) != "" {
		parts = append(parts, fmt.Sprintf("CLANKER_SRE_DEPLOY_ID=%q", strings.TrimSpace(deployID)))
	}
	return strings.Join(parts, " ")
}

func dockerPlan(discovery Discovery, image string, name string, backendURL string, tokenEnv string, provider string, deployID string, interval time.Duration) InstallPlan {
	available := discovery.Docker.Available
	warnings := []string{}
	if !available {
		warnings = append(warnings, "docker is the default SRE runtime but was not detected on this machine")
	}
	if backendURL == "" {
		warnings = append(warnings, "no Cerebro URL configured; set --cerebro-url or CLANKER_CEREBRO_URL before running")
	}
	commands := []string{
		fmt.Sprintf("docker pull %s", image),
		fmt.Sprintf("docker rm -f %s 2>/dev/null || true", name),
		fmt.Sprintf("docker run -d --name %s --restart unless-stopped %s -v $HOME/.clanker:/root/.clanker:ro -v $HOME/.aws:/root/.aws:ro -v $HOME/.kube:/root/.kube:ro %s %s", name, sreDockerEnvArgs(backendURL, tokenEnv, provider, deployID), image, sreRunShell("docker", interval, provider, deployID)),
	}
	composeEnv := fmt.Sprintf("      CLANKER_CEREBRO_URL: %q\n      %s: ${%s}\n", backendURL, tokenEnv, tokenEnv)
	if strings.TrimSpace(provider) != "" {
		composeEnv += fmt.Sprintf("      CLANKER_SRE_PROVIDER: %q\n", strings.TrimSpace(provider))
	}
	if strings.TrimSpace(deployID) != "" {
		composeEnv += fmt.Sprintf("      CLANKER_SRE_DEPLOY_ID: %q\n", strings.TrimSpace(deployID))
	}
	compose := fmt.Sprintf("services:\n  %s:\n    image: %s\n    restart: unless-stopped\n    environment:\n%s    volumes:\n      - ${HOME}/.clanker:/root/.clanker:ro\n      - ${HOME}/.aws:/root/.aws:ro\n      - ${HOME}/.kube:/root/.kube:ro\n    command: %s\n", name, image, composeEnv, sreRunJSONArgs("docker", interval, provider, deployID))
	return InstallPlan{Target: "docker", Summary: "Run Clanker SRE as a small Docker container", Available: available, Warnings: warnings, Commands: commands, Files: []InstallFile{{Path: "docker-compose.sre.yml", Mode: "0644", Content: compose}}, NextSteps: []string{"export " + tokenEnv + "=...", "run the docker command or docker compose -f docker-compose.sre.yml up -d"}}
}

func localPlan(discovery Discovery, name string, backendURL string, tokenEnv string, provider string, deployID string, interval time.Duration) InstallPlan {
	warnings := []string{}
	if backendURL == "" {
		warnings = append(warnings, "no Cerebro URL configured; local run will auto-detect a desktop backend if one is running")
	}
	command := fmt.Sprintf("%s clanker %s", sreShellEnvPrefix(backendURL, tokenEnv, provider, deployID), sreRunShell("local", interval, provider, deployID))
	script := "#!/usr/bin/env sh\nset -eu\nexec " + command + "\n"
	return InstallPlan{Target: "local", Summary: "Run Clanker SRE in the foreground on this machine", Available: true, Warnings: warnings, Commands: []string{command}, Files: []InstallFile{{Path: name + ".sh", Mode: "0755", Content: script}}, NextSteps: []string{"run the generated script or foreground command"}, Discovery: discovery}
}

func launchdPlan(discovery Discovery, name string, backendURL string, tokenEnv string, provider string, deployID string, interval time.Duration) InstallPlan {
	available := runtime.GOOS == "darwin"
	warnings := []string{}
	if !available {
		warnings = append(warnings, "launchd target is only available on macOS")
	}
	args := sreRunArgs("launchd", interval, provider, deployID)
	argXML := "    <string>clanker</string>"
	for _, arg := range args {
		argXML += fmt.Sprintf("<string>%s</string>", arg)
	}
	envXML := fmt.Sprintf("<key>CLANKER_CEREBRO_URL</key><string>%s</string><key>%s</key><string>$%s</string>", backendURL, tokenEnv, tokenEnv)
	if strings.TrimSpace(provider) != "" {
		envXML += fmt.Sprintf("<key>CLANKER_SRE_PROVIDER</key><string>%s</string>", strings.TrimSpace(provider))
	}
	if strings.TrimSpace(deployID) != "" {
		envXML += fmt.Sprintf("<key>CLANKER_SRE_DEPLOY_ID</key><string>%s</string>", strings.TrimSpace(deployID))
	}
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>ai.clanker.%s</string>
  <key>ProgramArguments</key>
  <array>
%s
  </array>
  <key>EnvironmentVariables</key>
  <dict>%s</dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
</dict>
</plist>
`, name, argXML, envXML)
	return InstallPlan{Target: "launchd", Summary: "Install Clanker SRE as a macOS launchd service", Available: available, Warnings: warnings, Commands: []string{"launchctl load ~/Library/LaunchAgents/ai.clanker." + name + ".plist"}, Files: []InstallFile{{Path: "ai.clanker." + name + ".plist", Mode: "0644", Content: plist}}, NextSteps: []string{"copy plist into ~/Library/LaunchAgents", "load it with launchctl"}, Discovery: discovery}
}

func systemdPlan(discovery Discovery, name string, backendURL string, tokenEnv string, provider string, deployID string, interval time.Duration) InstallPlan {
	available := runtime.GOOS == "linux"
	warnings := []string{}
	if !available {
		warnings = append(warnings, "systemd target is only available on Linux")
	}
	envLines := fmt.Sprintf("Environment=CLANKER_CEREBRO_URL=%s\nEnvironment=%s=${%s}\n", backendURL, tokenEnv, tokenEnv)
	if strings.TrimSpace(provider) != "" {
		envLines += fmt.Sprintf("Environment=CLANKER_SRE_PROVIDER=%s\n", strings.TrimSpace(provider))
	}
	if strings.TrimSpace(deployID) != "" {
		envLines += fmt.Sprintf("Environment=CLANKER_SRE_DEPLOY_ID=%s\n", strings.TrimSpace(deployID))
	}
	unit := fmt.Sprintf(`[Unit]
Description=Clanker SRE
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
%sExecStart=/usr/bin/env clanker %s
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
`, envLines, sreRunShell("systemd", interval, provider, deployID))
	return InstallPlan{Target: "systemd", Summary: "Install Clanker SRE as a Linux systemd service", Available: available, Warnings: warnings, Commands: []string{"sudo cp " + name + ".service /etc/systemd/system/", "sudo systemctl daemon-reload", "sudo systemctl enable --now " + name}, Files: []InstallFile{{Path: name + ".service", Mode: "0644", Content: unit}}, NextSteps: []string{"copy the unit to /etc/systemd/system", "enable the service"}, Discovery: discovery}
}

func k8sPlan(discovery Discovery, image string, name string, backendURL string, tokenEnv string, provider string, deployID string, interval time.Duration) InstallPlan {
	available := discovery.Kubernetes.Available
	warnings := []string{}
	if !available {
		warnings = append(warnings, "kubernetes was not detected; use this target only when kubeconfig is available")
	}
	extraEnv := ""
	if strings.TrimSpace(provider) != "" {
		extraEnv += fmt.Sprintf("            - name: CLANKER_SRE_PROVIDER\n              value: %q\n", strings.TrimSpace(provider))
	}
	if strings.TrimSpace(deployID) != "" {
		extraEnv += fmt.Sprintf("            - name: CLANKER_SRE_DEPLOY_ID\n              value: %q\n", strings.TrimSpace(deployID))
	}
	manifest := fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: %s
  namespace: clanker
spec:
  replicas: 1
  selector:
    matchLabels:
      app: %s
  template:
    metadata:
      labels:
        app: %s
    spec:
      containers:
        - name: sre
          image: %s
					args: %s
          env:
            - name: CLANKER_CEREBRO_URL
              value: %q
            - name: %s
              valueFrom:
                secretKeyRef:
                  name: clanker-sre
                  key: ingest-token
%s`, name, name, name, image, sreRunJSONArgs("k8s", interval, provider, deployID), backendURL, tokenEnv, extraEnv)
	return InstallPlan{Target: "k8s", Summary: "Run Clanker SRE in an existing Kubernetes cluster", Available: available, Warnings: warnings, Commands: []string{"kubectl create namespace clanker --dry-run=client -o yaml | kubectl apply -f -", "kubectl -n clanker create secret generic clanker-sre --from-literal=ingest-token=\"$" + tokenEnv + "\" --dry-run=client -o yaml | kubectl apply -f -", "kubectl apply -f clanker-sre.yaml"}, Files: []InstallFile{{Path: "clanker-sre.yaml", Mode: "0644", Content: manifest}}, NextSteps: []string{"create the ingest token secret", "apply clanker-sre.yaml"}, Discovery: discovery}
}

func cloudVMPlan(discovery Discovery, image string, name string, backendURL string, tokenEnv string, provider string, deployID string, interval time.Duration) InstallPlan {
	cloudInit := fmt.Sprintf(`#cloud-config
packages:
  - docker.io
runcmd:
  - [ sh, -lc, 'docker pull %s' ]
  - [ sh, -lc, 'docker rm -f %s 2>/dev/null || true' ]
  - [ sh, -lc, 'docker run -d --name %s --restart unless-stopped %s %s %s' ]
`, image, name, name, sreDockerEnvArgs(backendURL, tokenEnv, provider, deployID), image, sreRunShell("cloud-vm", interval, provider, deployID))
	return InstallPlan{Target: "cloud-vm", Summary: "Run Clanker SRE on a minimal VM in a user-owned provider", Available: true, Warnings: []string{"cloud-vm target generates bootstrap assets only; provision the VM with your chosen provider"}, Commands: []string{"use clanker-sre-cloud-init.yaml as cloud-init user data on a small VM"}, Files: []InstallFile{{Path: "clanker-sre-cloud-init.yaml", Mode: "0644", Content: cloudInit}}, NextSteps: []string{"create the smallest suitable VM", "attach cloud-init user data", "verify heartbeat in Cerebro"}, Discovery: discovery}
}

func awsCloudVMPlan(discovery Discovery, image string, name string, backendURL string, tokenEnv string, provider string, deployID string, interval time.Duration) InstallPlan {
	if strings.TrimSpace(provider) == "" {
		provider = "aws"
	}
	warnings := []string{}
	if backendURL == "" {
		warnings = append(warnings, "no public Cerebro URL configured; AWS SRE needs the backend-managed relay URL or another public ingest URL")
	}
	policy := `{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "ClankerSREReadOnlyInventory",
      "Effect": "Allow",
      "Action": [
        "apigateway:GET",
        "cloudtrail:LookupEvents",
        "cloudwatch:Describe*",
        "cloudwatch:Get*",
        "cloudwatch:List*",
        "dynamodb:Describe*",
        "dynamodb:List*",
        "ec2:Describe*",
        "ecs:Describe*",
        "ecs:List*",
        "eks:Describe*",
        "eks:List*",
        "elasticache:Describe*",
        "elasticache:List*",
        "iam:GenerateCredentialReport",
        "iam:Get*",
        "iam:List*",
        "lambda:Get*",
        "lambda:List*",
        "logs:Describe*",
        "logs:FilterLogEvents",
        "logs:Get*",
        "logs:List*",
        "rds:Describe*",
        "rds:ListTagsForResource",
        "resourcegroupstaggingapi:GetResources",
        "route53:Get*",
        "route53:List*",
        "s3:GetBucketLocation",
        "s3:GetBucketTagging",
        "s3:ListAllMyBuckets",
        "s3:ListBucket",
        "sqs:Get*",
        "sqs:List*",
        "states:Describe*",
        "states:List*",
        "sts:GetCallerIdentity",
        "tag:GetResources"
      ],
      "Resource": "*"
    }
  ]
}
`
	userData := fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail

export AWS_REGION="${AWS_REGION:-${AWS_DEFAULT_REGION:-us-east-1}}"
export AWS_DEFAULT_REGION="$AWS_REGION"
export CLANKER_CEREBRO_URL=%q
export %s="${%s:?set %s before rendering this user-data}"
export CLANKER_SRE_PROVIDER=%q
export CLANKER_SRE_DEPLOY_ID=%q

install -d -m 0755 /opt/clanker-sre
cat >/opt/clanker-sre/env <<EOF
AWS_REGION=$AWS_REGION
AWS_DEFAULT_REGION=$AWS_DEFAULT_REGION
CLANKER_CEREBRO_URL=$CLANKER_CEREBRO_URL
%s=${%s}
CLANKER_SRE_PROVIDER=$CLANKER_SRE_PROVIDER
CLANKER_SRE_DEPLOY_ID=$CLANKER_SRE_DEPLOY_ID
EOF
chmod 0600 /opt/clanker-sre/env

if command -v dnf >/dev/null 2>&1; then
  dnf install -y docker
  systemctl enable --now docker
elif command -v apt-get >/dev/null 2>&1; then
  apt-get update
  apt-get install -y docker.io
  systemctl enable --now docker
else
  echo "install docker manually on this image" >&2
  exit 1
fi

docker pull %s
docker rm -f %s 2>/dev/null || true
docker run -d --name %s --restart unless-stopped --env-file /opt/clanker-sre/env %s %s
`, backendURL, tokenEnv, tokenEnv, tokenEnv, "aws", deployID, tokenEnv, tokenEnv, image, name, name, image, sreRunShell("cloud-vm", interval, provider, deployID))
	commands := []string{
		"attach arn:aws:iam::aws:policy/ReadOnlyAccess to the clanker-sre-observer runtime role for broad AWS read coverage",
		"aws iam create-policy --policy-name clanker-sre-observer-collector-extras --policy-document file://aws-sre-readonly-policy.json || true",
		"create or reuse an EC2 instance profile/role, attach ReadOnlyAccess plus the collector extras policy, then launch a small VM with aws-sre-user-data.sh rendered with env vars",
	}
	return InstallPlan{Target: "aws", Summary: "Deploy Clanker SRE to AWS on a read-only EC2 observer VM", Available: true, Warnings: warnings, Commands: commands, Files: []InstallFile{{Path: "aws-sre-readonly-policy.json", Mode: "0644", Content: policy}, {Path: "aws-sre-user-data.sh", Mode: "0755", Content: userData}}, NextSteps: []string{"ensure CLANKER_CEREBRO_URL points at the backend-managed AWS relay or another public ingest URL", "attach AWS managed ReadOnlyAccess and the collector extras policy to the VM instance profile", "launch the VM with rendered aws-sre-user-data.sh", "confirm a heartbeat for the deploy ID in Cerebro"}, Discovery: discovery}
}

func gcpCloudVMPlan(discovery Discovery, image string, name string, backendURL string, tokenEnv string, provider string, deployID string, interval time.Duration) InstallPlan {
	if strings.TrimSpace(provider) == "" {
		provider = "gcp"
	}
	warnings := []string{}
	if backendURL == "" {
		warnings = append(warnings, "no public Cerebro URL configured; GCP SRE needs the backend-managed relay URL or another public ingest URL")
	}
	startup := fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail

PROJECT_ID="${GOOGLE_CLOUD_PROJECT:-${GCLOUD_PROJECT:-}}"
if [ -z "$PROJECT_ID" ]; then
  PROJECT_ID="$(curl -fsS -H 'Metadata-Flavor: Google' http://metadata.google.internal/computeMetadata/v1/project/project-id || true)"
fi
if [ -z "$PROJECT_ID" ]; then
  echo "GOOGLE_CLOUD_PROJECT is required" >&2
  exit 1
fi

export GOOGLE_CLOUD_PROJECT="$PROJECT_ID"
export GCLOUD_PROJECT="$PROJECT_ID"
export CLOUDSDK_CORE_PROJECT="$PROJECT_ID"
export CLANKER_CEREBRO_URL=%q
export %s="${%s:?set %s before rendering this startup script}"
export CLANKER_SRE_PROVIDER=%q
export CLANKER_SRE_DEPLOY_ID=%q

install -d -m 0755 /opt/clanker-sre
cat >/opt/clanker-sre/env <<EOF
GOOGLE_CLOUD_PROJECT=$GOOGLE_CLOUD_PROJECT
GCLOUD_PROJECT=$GCLOUD_PROJECT
CLOUDSDK_CORE_PROJECT=$CLOUDSDK_CORE_PROJECT
CLANKER_CEREBRO_URL=$CLANKER_CEREBRO_URL
%s=${%s}
CLANKER_SRE_PROVIDER=$CLANKER_SRE_PROVIDER
CLANKER_SRE_DEPLOY_ID=$CLANKER_SRE_DEPLOY_ID
EOF
chmod 0600 /opt/clanker-sre/env

apt-get update
apt-get install -y docker.io
systemctl enable --now docker

docker pull %s
docker rm -f %s 2>/dev/null || true
docker run -d --name %s --restart unless-stopped --env-file /opt/clanker-sre/env %s %s
`, backendURL, tokenEnv, tokenEnv, tokenEnv, "gcp", deployID, tokenEnv, tokenEnv, image, name, name, image, sreRunShell("cloud-vm", interval, provider, deployID))
	iam := `roles/viewer
roles/monitoring.viewer
roles/logging.viewer
roles/errorreporting.viewer
roles/cloudasset.viewer
roles/bigquery.metadataViewer
roles/browser
`
	commands := []string{
		"gcloud iam service-accounts create clanker-sre-observer --display-name='Clanker SRE Observer' || true",
		"grant the roles listed in gcp-sre-project-roles.txt to the service account at project scope",
		"create a small Compute Engine VM with that service account and gcp-sre-startup.sh as metadata startup-script",
	}
	return InstallPlan{Target: "gcp", Summary: "Deploy Clanker SRE to GCP on a read-only Compute Engine observer VM", Available: true, Warnings: warnings, Commands: commands, Files: []InstallFile{{Path: "gcp-sre-project-roles.txt", Mode: "0644", Content: iam}, {Path: "gcp-sre-startup.sh", Mode: "0755", Content: startup}}, NextSteps: []string{"ensure CLANKER_CEREBRO_URL points at the backend-managed GCP relay or another public ingest URL", "grant the listed read-only roles to the VM service account", "launch the VM with rendered gcp-sre-startup.sh", "confirm a heartbeat for the deploy ID in Cerebro"}, Discovery: discovery}
}

func normalizeTarget(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "", "default":
		return "docker"
	case "kubernetes":
		return "k8s"
	default:
		return value
	}
}

func sanitizeName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else if r == '_' || r == ' ' || r == '.' {
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func defaultAgentID() string {
	host, _ := os.Hostname()
	if host == "" {
		host = runtime.GOOS + "-" + runtime.GOARCH
	}
	return "sre-" + sanitizeName(host)
}

func kubeconfigPath() string {
	candidates := []string{os.Getenv("KUBECONFIG"), viper.GetString("kubernetes.kubeconfig")}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".kube", "config"))
	}
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if strings.Contains(candidate, string(os.PathListSeparator)) {
			for _, part := range filepath.SplitList(candidate) {
				if fileExists(part) {
					return part
				}
			}
			continue
		}
		if fileExists(candidate) {
			return candidate
		}
	}
	return ""
}

func fileExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func hasAnyEnv(keys ...string) bool {
	for _, key := range keys {
		if strings.TrimSpace(os.Getenv(key)) != "" {
			return true
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func discoverLocalCloudBaseURL(ctx context.Context) string {
	ports := []string{"8080", "8081", "8082", "8083", "8084"}
	client := &http.Client{Timeout: 500 * time.Millisecond}
	for _, port := range ports {
		base := "http://127.0.0.1:" + port + "/api"
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/health", nil)
		if err != nil {
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		_ = resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return base
		}
	}
	return ""
}

func SortDiscovery(discovery *Discovery) {
	if discovery == nil {
		return
	}
	sort.SliceStable(discovery.Tools, func(i, j int) bool { return discovery.Tools[i].Name < discovery.Tools[j].Name })
	sort.SliceStable(discovery.Providers, func(i, j int) bool { return discovery.Providers[i].ID < discovery.Providers[j].ID })
}
