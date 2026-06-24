package cmd

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	cfclient "github.com/bgdnvk/clanker/internal/cloudflare"
	"github.com/bgdnvk/clanker/internal/resourcedb"
	vercelapi "github.com/bgdnvk/clanker/internal/vercel"
	"github.com/spf13/cobra"
)

const (
	defaultSecurityScanQuestion     = "Analyze the current infrastructure for public or reachable APIs, internet-facing surfaces, credential leaks, auth gaps, exploitable misconfigurations, weak HTTP security headers, unsafe CORS, cookie/session flag gaps, risky HTTP methods, exposed documentation or sensitive paths, TLS posture, host/header trust, cache policy, API rate-limit/resource controls, agent/tool misuse, MCP and agent supply-chain exposure, prompt-injection paths, identity abuse, and plausible attack paths. Prioritize externally reachable services, agentic control planes, and concrete attack vectors."
	securityScanResultMarker        = "::clanker-security-result::"
	maxSecurityProbeEndpoints       = 24
	maxSecurityAttackVectors        = 18
	securityProbeTimeout            = 3 * time.Second
	runtimeSecurityBearerTokenEnv   = "CLANKER_RUNTIME_SECURITY_BEARER_TOKEN"
	runtimeSecurityBasicUsernameEnv = "CLANKER_RUNTIME_SECURITY_BASIC_USERNAME"
	runtimeSecurityBasicPasswordEnv = "CLANKER_RUNTIME_SECURITY_BASIC_PASSWORD"
	runtimeSecurityCookieEnv        = "CLANKER_RUNTIME_SECURITY_COOKIE"
	runtimeSecurityHeadersEnv       = "CLANKER_RUNTIME_SECURITY_HEADERS_JSON"
)

type securityRuntimeAuthPack struct {
	BearerToken string
	Username    string
	Password    string
	Cookie      string
	Headers     map[string]string
}

func (a securityRuntimeAuthPack) HasAuth() bool {
	return strings.TrimSpace(a.BearerToken) != "" || (strings.TrimSpace(a.Username) != "" && strings.TrimSpace(a.Password) != "") || strings.TrimSpace(a.Cookie) != "" || len(a.Headers) > 0
}

type securityScanSummary struct {
	TotalResources     int    `json:"totalResources"`
	CandidateEndpoints int    `json:"candidateEndpoints"`
	ReachableEndpoints int    `json:"reachableEndpoints"`
	CriticalFindings   int    `json:"criticalFindings"`
	HighFindings       int    `json:"highFindings"`
	CredentialRisks    int    `json:"credentialRisks"`
	AgenticRisks       int    `json:"agenticRisks"`
	MCPRisks           int    `json:"mcpRisks"`
	SupplyChainRisks   int    `json:"supplyChainRisks"`
	PrivilegeRisks     int    `json:"privilegeRisks"`
	WebPostureRisks    int    `json:"webPostureRisks"`
	SensitivePathRisks int    `json:"sensitivePathRisks"`
	CORSRisks          int    `json:"corsRisks"`
	TLSRisks           int    `json:"tlsRisks"`
	AttackVectorCount  int    `json:"attackVectorCount"`
	AuthSignals        int    `json:"authSignals"`
	PrimaryFocus       string `json:"primaryFocus,omitempty"`
}

type securityFinding struct {
	ID              string   `json:"id"`
	Severity        string   `json:"severity"`
	Category        string   `json:"category"`
	Title           string   `json:"title"`
	Summary         string   `json:"summary"`
	Confidence      string   `json:"confidence,omitempty"`
	BlastRadius     string   `json:"blastRadius,omitempty"`
	ResourceID      string   `json:"resourceId,omitempty"`
	ResourceName    string   `json:"resourceName,omitempty"`
	ResourceType    string   `json:"resourceType,omitempty"`
	Provider        string   `json:"provider,omitempty"`
	Region          string   `json:"region,omitempty"`
	Endpoint        string   `json:"endpoint,omitempty"`
	Reachable       bool     `json:"reachable,omitempty"`
	StatusCode      int      `json:"statusCode,omitempty"`
	RequiresAuth    bool     `json:"requiresAuth,omitempty"`
	Frameworks      []string `json:"frameworks,omitempty"`
	Threats         []string `json:"threats,omitempty"`
	AttackerView    string   `json:"attackerView,omitempty"`
	DefenderView    string   `json:"defenderView,omitempty"`
	Evidence        []string `json:"evidence,omitempty"`
	Questions       []string `json:"questions,omitempty"`
	Containment     []string `json:"containment,omitempty"`
	Remediation     []string `json:"remediation,omitempty"`
	Verification    []string `json:"verification,omitempty"`
	RegressionTests []string `json:"regressionTests,omitempty"`
	Priority        string   `json:"priority,omitempty"`
	Owner           string   `json:"owner,omitempty"`
	Status          string   `json:"status,omitempty"`
}

type securityAttackVector struct {
	ID               string   `json:"id"`
	Severity         string   `json:"severity"`
	Title            string   `json:"title"`
	Summary          string   `json:"summary"`
	KillChainStage   string   `json:"killChainStage,omitempty"`
	Exploitability   string   `json:"exploitability,omitempty"`
	Confidence       string   `json:"confidence,omitempty"`
	LikelyImpact     string   `json:"likelyImpact,omitempty"`
	BlastRadius      string   `json:"blastRadius,omitempty"`
	ResourceIDs      []string `json:"resourceIds,omitempty"`
	EntryPoints      []string `json:"entryPoints,omitempty"`
	Prerequisites    []string `json:"prerequisites,omitempty"`
	Steps            []string `json:"steps,omitempty"`
	ImmediateActions []string `json:"immediateActions,omitempty"`
	ValidationChecks []string `json:"validationChecks,omitempty"`
	DetectionSignals []string `json:"detectionSignals,omitempty"`
	RequiresAuth     bool     `json:"requiresAuth,omitempty"`
	AuthKinds        []string `json:"authKinds,omitempty"`
	Frameworks       []string `json:"frameworks,omitempty"`
	Threats          []string `json:"threats,omitempty"`
	AttackerView     string   `json:"attackerView,omitempty"`
	DefenderView     string   `json:"defenderView,omitempty"`
	RegressionTests  []string `json:"regressionTests,omitempty"`
	Evidence         []string `json:"evidence,omitempty"`
}

type securitySubagentRun struct {
	Name    string   `json:"name"`
	Status  string   `json:"status"`
	Summary string   `json:"summary"`
	Details []string `json:"details,omitempty"`
}

type securityScanResult struct {
	Query         string                 `json:"query"`
	GeneratedAt   string                 `json:"generatedAt"`
	Summary       securityScanSummary    `json:"summary"`
	Findings      []securityFinding      `json:"findings"`
	AttackVectors []securityAttackVector `json:"attackVectors"`
	Subagents     []securitySubagentRun  `json:"subagents,omitempty"`
	Warnings      []string               `json:"warnings,omitempty"`
}

type securitySurfaceCandidate struct {
	ResourceID    string
	ResourceName  string
	ResourceType  string
	Provider      string
	Region        string
	Endpoint      string
	Source        string
	LikelyPublic  bool
	LikelyPrivate bool
}

type securityProbeObservation struct {
	Endpoint          string
	ResourceID        string
	Scheme            string
	Port              string
	StatusCode        int
	Reachable         bool
	RequiresAuth      bool
	Authenticated     bool
	AllowsCORS        bool
	Server            string
	Banner            string
	TLSEnabled        bool
	TLSVersion        string
	ContentType       string
	Location          string
	Method            string
	InterestingPaths  []string
	MissingHeaders    []string
	WeakHeaders       []string
	CookieIssues      []string
	CORSIssues        []string
	TLSIssues         []string
	MethodIssues      []string
	HeaderTrustIssues []string
	CacheIssues       []string
	RateLimitIssues   []string
	Notes             []string
}

type securityScanContext struct {
	Question    string
	Estate      deepResearchEstateSnapshot
	Candidates  []securitySurfaceCandidate
	Probes      []securityProbeObservation
	AuthPack    securityRuntimeAuthPack
	Options     deepResearchRunOptions
	ProbeLimit  int
	ProbeTarget int
}

var securityCmd = &cobra.Command{
	Use:   "security [question]",
	Short: "Run a staged infrastructure security scan across the current estate",
	Long: `Run a staged security scan across the current infrastructure estate.

The scan inventories internet-facing surfaces, safely probes likely APIs,
checks for exploitable misconfigurations, secret leaks, HTTP header, CORS,
cookie, TLS, risky-method, sensitive-path, host/header trust, cache-policy,
and API resource-control posture, agent/tool misuse,
MCP and supply-chain risk, and builds attack vectors that an operator can
validate manually.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		question := defaultSecurityScanQuestion
		if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
			question = strings.TrimSpace(args[0])
		}

		profile, _ := cmd.Flags().GetString("profile")
		gcpProject, _ := cmd.Flags().GetString("gcp-project")
		azureSubscriptionID, _ := cmd.Flags().GetString("azure-subscription")
		workspace, _ := cmd.Flags().GetString("workspace")

		estate, warnings := loadDeepResearchEstateSnapshot()
		authPack, authWarnings := loadSecurityRuntimeAuthPack()
		warnings = append(warnings, authWarnings...)

		fmt.Printf("[security] starting security scan query=%q resources=%d\n", question, len(estate.Resources))

		ctx := securityScanContext{
			Question: question,
			Estate:   estate,
			AuthPack: authPack,
			Options: deepResearchRunOptions{
				Profile:             strings.TrimSpace(profile),
				GCPProject:          strings.TrimSpace(gcpProject),
				AzureSubscriptionID: strings.TrimSpace(azureSubscriptionID),
				TerraformWorkspace:  strings.TrimSpace(workspace),
			},
			ProbeLimit: maxSecurityProbeEndpoints,
		}

		result, scanWarnings := runSecurityScan(context.Background(), ctx)
		result.Warnings = uniqueNonEmptyStrings(append(warnings, scanWarnings...))

		payload, err := json.Marshal(result)
		if err != nil {
			return fmt.Errorf("marshal security result: %w", err)
		}

		fmt.Printf("%s%s\n", securityScanResultMarker, string(payload))
		return nil
	},
}

func init() {
	rootCmd.AddCommand(securityCmd)

	securityCmd.Flags().String("profile", "", "AWS profile to use for provider-side security helpers")
	securityCmd.Flags().String("gcp-project", "", "GCP project ID to use for provider-side security helpers")
	securityCmd.Flags().String("azure-subscription", "", "Azure subscription ID to use for provider-side security helpers")
	securityCmd.Flags().String("workspace", "", "Terraform workspace to use for provider-side security helpers")
}

func loadSecurityRuntimeAuthPack() (securityRuntimeAuthPack, []string) {
	pack := securityRuntimeAuthPack{
		BearerToken: strings.TrimSpace(os.Getenv(runtimeSecurityBearerTokenEnv)),
		Username:    strings.TrimSpace(os.Getenv(runtimeSecurityBasicUsernameEnv)),
		Password:    strings.TrimSpace(os.Getenv(runtimeSecurityBasicPasswordEnv)),
		Cookie:      strings.TrimSpace(os.Getenv(runtimeSecurityCookieEnv)),
		Headers:     map[string]string{},
	}

	warnings := []string{}
	if raw := strings.TrimSpace(os.Getenv(runtimeSecurityHeadersEnv)); raw != "" {
		if err := json.Unmarshal([]byte(raw), &pack.Headers); err != nil {
			warnings = append(warnings, fmt.Sprintf("Failed to decode security headers JSON: %v", err))
			pack.Headers = map[string]string{}
		}
	}
	for key, value := range pack.Headers {
		trimmedKey := strings.TrimSpace(key)
		trimmedValue := strings.TrimSpace(value)
		if trimmedKey == "" || trimmedValue == "" {
			delete(pack.Headers, key)
			continue
		}
		if strings.EqualFold(trimmedKey, "Host") || strings.EqualFold(trimmedKey, "Content-Length") {
			delete(pack.Headers, key)
			continue
		}
		if trimmedKey != key || trimmedValue != value {
			delete(pack.Headers, key)
			pack.Headers[trimmedKey] = trimmedValue
		}
	}
	if len(pack.Headers) == 0 {
		pack.Headers = nil
	}
	return pack, warnings
}

func runSecurityScan(ctx context.Context, scanCtx securityScanContext) (securityScanResult, []string) {
	warnings := []string{}
	runs := make([]securitySubagentRun, 0, 6)

	fmt.Printf("[security][surface-mapper] starting\n")
	candidates, candidateWarnings := buildSecuritySurfaceCandidates(scanCtx.Estate.Resources)
	scanCtx.Candidates = candidates
	scanCtx.ProbeTarget = minInt(len(candidates), scanCtx.ProbeLimit)
	runs = append(runs, securitySubagentRun{
		Name:    "surface-mapper",
		Status:  ternaryStatus(len(candidates) > 0, "ok", "warning"),
		Summary: fmt.Sprintf("Mapped %d candidate HTTP surfaces across %d resources.", len(candidates), len(scanCtx.Estate.Resources)),
		Details: summarizeSecurityCandidates(candidates, 6),
	})
	fmt.Printf("[security][surface-mapper] mapped %d candidate HTTP surfaces across %d resources\n", len(candidates), len(scanCtx.Estate.Resources))
	warnings = append(warnings, candidateWarnings...)

	fmt.Printf("[security][provider-enrichment] starting\n")
	providerCandidates, providerFindings, providerRun, providerWarnings := runSecurityProviderEnrichment(ctx, scanCtx)
	scanCtx.Candidates = mergeSecuritySurfaceCandidates(scanCtx.Candidates, providerCandidates)
	scanCtx.ProbeTarget = minInt(len(scanCtx.Candidates), scanCtx.ProbeLimit)
	runs = append(runs, providerRun)
	warnings = append(warnings, providerWarnings...)
	fmt.Printf("[security][provider-enrichment] %s\n", providerRun.Summary)

	providerScoutSubagents := buildSecurityLiveProviderSubagents(scanCtx)
	providerScoutCandidates, providerScoutFindings, providerScoutRuns, providerScoutWarnings := executeSecurityProviderSubagentBatch(ctx, providerScoutSubagents)
	if len(providerScoutSubagents) == 0 {
		runs = append(runs, securitySubagentRun{
			Name:    "live-provider-scouts",
			Status:  "warning",
			Summary: "No live provider security scouts were available for the current estate and credentials.",
		})
	} else {
		scanCtx.Candidates = mergeSecuritySurfaceCandidates(scanCtx.Candidates, providerScoutCandidates)
		scanCtx.ProbeTarget = minInt(len(scanCtx.Candidates), scanCtx.ProbeLimit)
		runs = append(runs, providerScoutRuns...)
		warnings = append(warnings, providerScoutWarnings...)
		fmt.Printf("[security][live-provider-scouts] collected %d live provider findings and %d endpoint candidates\n", len(providerScoutFindings), len(providerScoutCandidates))
	}

	fmt.Printf("[security][reachability-scout] starting\n")
	probes, reachRun, reachWarnings := runSecurityReachabilityScout(ctx, scanCtx)
	scanCtx.Probes = probes
	runs = append(runs, reachRun)
	warnings = append(warnings, reachWarnings...)
	fmt.Printf("[security][reachability-scout] %s\n", reachRun.Summary)

	var (
		findingsMu       sync.Mutex
		allFindings      = append(append([]securityFinding{}, providerFindings...), providerScoutFindings...)
		analysisRuns     []securitySubagentRun
		analysisWarnings []string
	)
	analysisSubagents := []struct {
		name string
		run  func() ([]securityFinding, securitySubagentRun, []string)
	}{
		{
			name: "network-policy-analyst",
			run: func() ([]securityFinding, securitySubagentRun, []string) {
				findings := buildSecurityNetworkPolicyFindings(scanCtx.Estate.Resources)
				summary := fmt.Sprintf("Flagged %d network policy and open-ingress findings.", len(findings))
				if len(findings) == 0 {
					summary = "No world-open ingress or obvious network-policy drift was inferred from the estate snapshot."
				}
				return findings, securitySubagentRun{Name: "network-policy-analyst", Status: "ok", Summary: summary}, nil
			},
		},
		{
			name: "misconfig-analyst",
			run: func() ([]securityFinding, securitySubagentRun, []string) {
				findings := buildSecurityMisconfigurationFindings(scanCtx.Estate.Resources, scanCtx.Probes)
				summary := fmt.Sprintf("Flagged %d exploitable misconfiguration findings.", len(findings))
				if len(findings) == 0 {
					summary = "No obvious exploitable misconfigurations were inferred from the current snapshot."
				}
				return findings, securitySubagentRun{Name: "misconfig-analyst", Status: "ok", Summary: summary}, nil
			},
		},
		{
			name: "secret-leak-analyst",
			run: func() ([]securityFinding, securitySubagentRun, []string) {
				findings := buildSecuritySecretLeakFindings(scanCtx.Estate.Resources)
				summary := fmt.Sprintf("Flagged %d credential or secret exposure risks.", len(findings))
				if len(findings) == 0 {
					summary = "No obvious plaintext secret exposure was found in the estate snapshot."
				}
				return findings, securitySubagentRun{Name: "secret-leak-analyst", Status: "ok", Summary: summary}, nil
			},
		},
		{
			name: "iam-policy-analyst",
			run: func() ([]securityFinding, securitySubagentRun, []string) {
				findings := buildSecurityIAMPolicyFindings(scanCtx.Estate.Resources)
				summary := fmt.Sprintf("Flagged %d privileged IAM policy findings.", len(findings))
				if len(findings) == 0 {
					summary = "No high-risk IAM policy names or obvious role-chaining paths were inferred from the estate snapshot."
				}
				return findings, securitySubagentRun{Name: "iam-policy-analyst", Status: "ok", Summary: summary}, nil
			},
		},
		{
			name: "identity-analyst",
			run: func() ([]securityFinding, securitySubagentRun, []string) {
				findings := buildSecurityIdentityFindings(scanCtx.Estate.Resources)
				summary := fmt.Sprintf("Flagged %d identity or privilege pivot risks.", len(findings))
				if len(findings) == 0 {
					summary = "No clear public-to-identity pivot path was inferred from the estate snapshot."
				}
				return findings, securitySubagentRun{Name: "identity-analyst", Status: "ok", Summary: summary}, nil
			},
		},
		{
			name: "agentic-surface-analyst",
			run: func() ([]securityFinding, securitySubagentRun, []string) {
				findings := buildSecurityAgenticSurfaceFindings(scanCtx.Estate.Resources)
				summary := fmt.Sprintf("Flagged %d agent/tool, prompt-injection, or agent identity risks.", len(findings))
				if len(findings) == 0 {
					summary = "No obvious agent/tool execution, prompt-injection, or agent identity risks were inferred from the estate snapshot."
				}
				return findings, securitySubagentRun{Name: "agentic-surface-analyst", Status: "ok", Summary: summary}, nil
			},
		},
		{
			name: "mcp-tooling-analyst",
			run: func() ([]securityFinding, securitySubagentRun, []string) {
				findings := buildSecurityMCPToolingFindings(scanCtx.Estate.Resources)
				summary := fmt.Sprintf("Flagged %d MCP, tool gateway, or inter-agent trust findings.", len(findings))
				if len(findings) == 0 {
					summary = "No obvious MCP, tool gateway, or cross-agent trust findings were inferred from the estate snapshot."
				}
				return findings, securitySubagentRun{Name: "mcp-tooling-analyst", Status: "ok", Summary: summary}, nil
			},
		},
		{
			name: "agent-supply-chain-analyst",
			run: func() ([]securityFinding, securitySubagentRun, []string) {
				findings := buildSecurityAgentSupplyChainFindings(scanCtx.Estate.Resources)
				summary := fmt.Sprintf("Flagged %d agent supply-chain or dependency integrity findings.", len(findings))
				if len(findings) == 0 {
					summary = "No obvious agent skill, MCP server, model, image, or workflow integrity gaps were inferred from the estate snapshot."
				}
				return findings, securitySubagentRun{Name: "agent-supply-chain-analyst", Status: "ok", Summary: summary}, nil
			},
		},
		{
			name: "storage-hygiene-analyst",
			run: func() ([]securityFinding, securitySubagentRun, []string) {
				findings := buildSecurityStorageExposureFindings(scanCtx.Estate.Resources)
				summary := fmt.Sprintf("Flagged %d storage exposure or encryption findings.", len(findings))
				if len(findings) == 0 {
					summary = "No obvious public bucket or disabled encryption signals were inferred from the estate snapshot."
				}
				return findings, securitySubagentRun{Name: "storage-hygiene-analyst", Status: "ok", Summary: summary}, nil
			},
		},
		{
			name: "detective-control-analyst",
			run: func() ([]securityFinding, securitySubagentRun, []string) {
				findings := buildSecurityDetectiveControlFindings(scanCtx.Estate.Resources)
				summary := fmt.Sprintf("Flagged %d logging, alerting, WAF, or detector coverage findings.", len(findings))
				if len(findings) == 0 {
					summary = "No obvious disabled logging, alerting, WAF, or detector signals were inferred from the estate snapshot."
				}
				return findings, securitySubagentRun{Name: "detective-control-analyst", Status: "ok", Summary: summary}, nil
			},
		},
		{
			name: "surface-exposure-analyst",
			run: func() ([]securityFinding, securitySubagentRun, []string) {
				findings := buildSecurityReachabilityFindings(scanCtx.Candidates, scanCtx.Probes, scanCtx.AuthPack)
				summary := fmt.Sprintf("Turned live probing into %d surface findings.", len(findings))
				if len(findings) == 0 {
					summary = "No reachable API surfaces were confirmed from the current candidate list."
				}
				return findings, securitySubagentRun{Name: "surface-exposure-analyst", Status: "ok", Summary: summary}, nil
			},
		},
		{
			name: "web-posture-analyst",
			run: func() ([]securityFinding, securitySubagentRun, []string) {
				findings := buildSecurityHTTPPostureFindings(scanCtx.Candidates, scanCtx.Probes)
				summary := fmt.Sprintf("Flagged %d HTTP header, CORS, TLS, method, cookie, sensitive-path, header-trust, cache, or API resource-control findings.", len(findings))
				if len(findings) == 0 {
					summary = "No HTTP header, CORS, TLS, risky-method, cookie, sensitive-path, header-trust, cache, or API resource-control gaps were confirmed by live probes."
				}
				return findings, securitySubagentRun{Name: "web-posture-analyst", Status: "ok", Summary: summary}, nil
			},
		},
	}

	var waitGroup sync.WaitGroup
	for _, subagent := range analysisSubagents {
		current := subagent
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			fmt.Printf("[security][%s] starting\n", current.name)
			findings, run, runWarnings := current.run()
			fmt.Printf("[security][%s] %s\n", current.name, run.Summary)
			findingsMu.Lock()
			allFindings = append(allFindings, findings...)
			analysisRuns = append(analysisRuns, run)
			analysisWarnings = append(analysisWarnings, runWarnings...)
			findingsMu.Unlock()
		}()
	}
	waitGroup.Wait()
	runs = append(runs, analysisRuns...)
	warnings = append(warnings, analysisWarnings...)

	allFindings = dedupeSecurityFindings(allFindings)

	fmt.Printf("[security][remediation-planner] starting\n")
	allFindings = enrichSecurityFindingsWithRemediation(allFindings)
	allFindings = sortAndCapSecurityFindings(allFindings)
	remediationSummary := fmt.Sprintf("Prepared containment and remediation guidance for %d prioritized findings.", len(allFindings))
	if len(allFindings) == 0 {
		remediationSummary = "No findings were available for remediation planning."
	}
	runs = append(runs, securitySubagentRun{
		Name:    "remediation-planner",
		Status:  ternaryStatus(len(allFindings) > 0, "ok", "warning"),
		Summary: remediationSummary,
		Details: summarizeSecurityRemediationTargets(allFindings, 4),
	})
	fmt.Printf("[security][remediation-planner] %s\n", remediationSummary)

	fmt.Printf("[security][pentesting-agent] starting\n")
	vectors := buildSecurityAttackVectors(allFindings, scanCtx.Probes, scanCtx.AuthPack)
	pentestSummary := fmt.Sprintf("Built %d executable attack vectors from %d prioritized findings.", len(vectors), len(allFindings))
	if len(vectors) == 0 {
		pentestSummary = "No attack vectors were built because the scan found no viable entry points."
	}
	runs = append(runs, securitySubagentRun{
		Name:    "pentesting-agent",
		Status:  ternaryStatus(len(vectors) > 0, "ok", "warning"),
		Summary: pentestSummary,
		Details: summarizeSecurityVectors(vectors, 4),
	})
	fmt.Printf("[security][pentesting-agent] %s\n", pentestSummary)

	sort.Slice(runs, func(i, j int) bool {
		return runs[i].Name < runs[j].Name
	})

	return securityScanResult{
		Query:         scanCtx.Question,
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
		Summary:       buildSecurityScanSummary(scanCtx.Estate, scanCtx.Candidates, scanCtx.Probes, allFindings, vectors),
		Findings:      allFindings,
		AttackVectors: vectors,
		Subagents:     runs,
	}, uniqueNonEmptyStrings(warnings)
}

func runSecurityProviderEnrichment(ctx context.Context, scanCtx securityScanContext) ([]securitySurfaceCandidate, []securityFinding, securitySubagentRun, []string) {
	details := []string{}
	warnings := []string{}
	candidates := []securitySurfaceCandidate{}
	findings := []securityFinding{}
	relevantProviders := []string{}

	if securityEstateHasProvider(scanCtx.Estate.Resources, "cloudflare") {
		relevantProviders = append(relevantProviders, "cloudflare")
		providerCandidates, providerFindings, providerDetails, providerWarnings := enrichSecurityWithCloudflare(ctx, scanCtx.Estate.Resources, scanCtx.Candidates)
		candidates = append(candidates, providerCandidates...)
		findings = append(findings, providerFindings...)
		details = append(details, providerDetails...)
		warnings = append(warnings, providerWarnings...)
	}

	if securityEstateHasProvider(scanCtx.Estate.Resources, "vercel") {
		relevantProviders = append(relevantProviders, "vercel")
		providerCandidates, providerFindings, providerDetails, providerWarnings := enrichSecurityWithVercel(ctx, scanCtx.Estate.Resources)
		candidates = append(candidates, providerCandidates...)
		findings = append(findings, providerFindings...)
		details = append(details, providerDetails...)
		warnings = append(warnings, providerWarnings...)
	}

	if len(relevantProviders) == 0 {
		return nil, nil, securitySubagentRun{
			Name:    "provider-enrichment",
			Status:  "ok",
			Summary: "No Cloudflare or Vercel edge resources required provider-native enrichment.",
		}, nil
	}

	status := "ok"
	if len(warnings) > 0 && len(candidates) == 0 && len(findings) == 0 {
		status = "warning"
	}

	return candidates, findings, securitySubagentRun{
		Name:    "provider-enrichment",
		Status:  status,
		Summary: fmt.Sprintf("Provider-native enrichment added %d endpoints and %d findings across %s.", len(candidates), len(findings), strings.Join(relevantProviders, ", ")),
		Details: uniqueNonEmptyStrings(details),
	}, uniqueNonEmptyStrings(warnings)
}

func enrichSecurityWithCloudflare(ctx context.Context, resources []deepResearchResource, existingCandidates []securitySurfaceCandidate) ([]securitySurfaceCandidate, []securityFinding, []string, []string) {
	token := strings.TrimSpace(cfclient.ResolveAPIToken())
	if token == "" {
		warning := "Cloudflare resources were detected, but no Cloudflare API token was available for provider-native enrichment."
		return nil, nil, []string{warning}, []string{warning}
	}

	client, err := cfclient.NewClient(strings.TrimSpace(cfclient.ResolveAccountID()), token, false)
	if err != nil {
		warning := fmt.Sprintf("Cloudflare enrichment setup failed: %v", err)
		return nil, nil, []string{warning}, []string{warning}
	}

	zones, err := client.ListZones(ctx)
	if err != nil {
		warning := fmt.Sprintf("Cloudflare zone listing failed: %v", err)
		return nil, nil, []string{warning}, []string{warning}
	}

	selectedZones := selectSecurityCloudflareZones(zones, resources, existingCandidates)
	candidates := []securitySurfaceCandidate{}
	findings := []securityFinding{}
	details := []string{}
	warnings := []string{}

	for _, zone := range selectedZones {
		recordsOut, err := client.RunAPIWithContext(ctx, "GET", fmt.Sprintf("/zones/%s/dns_records?per_page=100", url.PathEscape(zone.ID)), "")
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("Cloudflare DNS lookup failed for zone %s: %v", zone.Name, err))
			continue
		}
		var recordsResponse struct {
			Success bool `json:"success"`
			Result  []struct {
				ID      string `json:"id"`
				Type    string `json:"type"`
				Name    string `json:"name"`
				Content string `json:"content"`
				Proxied bool   `json:"proxied"`
			} `json:"result"`
		}
		if err := json.Unmarshal([]byte(recordsOut), &recordsResponse); err != nil {
			warnings = append(warnings, fmt.Sprintf("Cloudflare DNS response parse failed for zone %s: %v", zone.Name, err))
			continue
		}

		activeDirectOrigins := 0
		candidateCount := 0
		for _, record := range recordsResponse.Result {
			recordType := strings.ToUpper(strings.TrimSpace(record.Type))
			if record.Name == "" || (recordType != "A" && recordType != "AAAA" && recordType != "CNAME") {
				continue
			}
			for _, endpoint := range normalizeSecurityEndpoints(record.Name, "") {
				candidateCount++
				candidates = append(candidates, securitySurfaceCandidate{
					ResourceName: record.Name,
					ResourceType: "cloudflare-dns-record",
					Provider:     "cloudflare",
					Region:       "global",
					Endpoint:     endpoint,
					Source:       fmt.Sprintf("cloudflare-dns:%s", zone.Name),
					LikelyPublic: true,
				})
			}
			if !record.Proxied {
				activeDirectOrigins++
				findings = append(findings, securityFinding{
					ID:           buildDeepResearchFindingID("security-cloudflare-direct-origin", zone.ID+"|"+record.ID),
					Severity:     "high",
					Category:     "misconfiguration",
					Title:        fmt.Sprintf("Cloudflare record %s bypasses edge proxying", record.Name),
					Summary:      "A Cloudflare DNS record is configured as DNS-only, which can expose the origin directly instead of forcing traffic through edge protections.",
					ResourceName: record.Name,
					ResourceType: "cloudflare-dns-record",
					Provider:     "cloudflare",
					Region:       "global",
					Endpoint:     firstSecurityEndpointForHost(record.Name),
					Evidence: []string{
						fmt.Sprintf("Zone: %s", zone.Name),
						fmt.Sprintf("Record type: %s", recordType),
						fmt.Sprintf("Origin target: %s", strings.TrimSpace(record.Content)),
						"Cloudflare reports proxied=false for this record.",
					},
					Questions: buildSecurityQuestions("misconfiguration", record.Name, firstSecurityEndpointForHost(record.Name), false),
				})
			}
		}

		firewallOut, err := client.RunAPIWithContext(ctx, "GET", fmt.Sprintf("/zones/%s/firewall/rules", url.PathEscape(zone.ID)), "")
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("Cloudflare firewall rule lookup failed for zone %s: %v", zone.Name, err))
			details = append(details, fmt.Sprintf("Cloudflare zone %s: %d candidate endpoints from DNS, firewall rules unavailable.", zone.Name, candidateCount))
			continue
		}
		var firewallResponse struct {
			Success bool `json:"success"`
			Result  []struct {
				Paused bool `json:"paused"`
			} `json:"result"`
		}
		if err := json.Unmarshal([]byte(firewallOut), &firewallResponse); err != nil {
			warnings = append(warnings, fmt.Sprintf("Cloudflare firewall response parse failed for zone %s: %v", zone.Name, err))
			continue
		}

		activeFirewallRules := 0
		for _, rule := range firewallResponse.Result {
			if !rule.Paused {
				activeFirewallRules++
			}
		}
		if activeFirewallRules == 0 {
			findings = append(findings, securityFinding{
				ID:           buildDeepResearchFindingID("security-cloudflare-no-firewall", zone.ID),
				Severity:     "medium",
				Category:     "misconfiguration",
				Title:        fmt.Sprintf("Cloudflare zone %s has no active firewall rules", zone.Name),
				Summary:      "The zone exposes public DNS records but Cloudflare reported no active firewall rules. That reduces edge filtering depth for internet-facing traffic.",
				ResourceName: zone.Name,
				ResourceType: "cloudflare-zone",
				Provider:     "cloudflare",
				Region:       "global",
				Evidence: []string{
					fmt.Sprintf("Zone: %s", zone.Name),
					fmt.Sprintf("Active firewall rules: %d", activeFirewallRules),
				},
				Questions: buildSecurityQuestions("misconfiguration", zone.Name, "", false),
			})
		}

		details = append(details, fmt.Sprintf("Cloudflare zone %s: %d candidate endpoints, %d DNS-only origin records, %d active firewall rules.", zone.Name, candidateCount, activeDirectOrigins, activeFirewallRules))
	}

	return candidates, findings, details, warnings
}

func enrichSecurityWithVercel(ctx context.Context, resources []deepResearchResource) ([]securitySurfaceCandidate, []securityFinding, []string, []string) {
	token := strings.TrimSpace(vercelapi.ResolveAPIToken())
	if token == "" {
		warning := "Vercel resources were detected, but no Vercel API token was available for provider-native enrichment."
		return nil, nil, []string{warning}, []string{warning}
	}

	client, err := vercelapi.NewClient(token, strings.TrimSpace(vercelapi.ResolveTeamID()), false)
	if err != nil {
		warning := fmt.Sprintf("Vercel enrichment setup failed: %v", err)
		return nil, nil, []string{warning}, []string{warning}
	}

	projectsOut, err := client.RunAPIWithContext(ctx, "GET", "/v9/projects?limit=100", "")
	if err != nil {
		warning := fmt.Sprintf("Vercel project listing failed: %v", err)
		return nil, nil, []string{warning}, []string{warning}
	}
	var projectResponse struct {
		Projects []vercelapi.Project `json:"projects"`
	}
	if err := json.Unmarshal([]byte(projectsOut), &projectResponse); err != nil {
		warning := fmt.Sprintf("Vercel project response parse failed: %v", err)
		return nil, nil, []string{warning}, []string{warning}
	}

	domainsOut, err := client.RunAPIWithContext(ctx, "GET", "/v5/domains?limit=100", "")
	if err != nil {
		warning := fmt.Sprintf("Vercel domain listing failed: %v", err)
		return nil, nil, []string{warning}, []string{warning}
	}
	var domainResponse struct {
		Domains []vercelapi.Domain `json:"domains"`
	}
	if err := json.Unmarshal([]byte(domainsOut), &domainResponse); err != nil {
		warning := fmt.Sprintf("Vercel domain response parse failed: %v", err)
		return nil, nil, []string{warning}, []string{warning}
	}

	aliasesOut, err := client.RunAPIWithContext(ctx, "GET", "/v4/aliases?limit=50", "")
	if err != nil {
		warning := fmt.Sprintf("Vercel alias listing failed: %v", err)
		return nil, nil, []string{warning}, []string{warning}
	}
	var aliasResponse struct {
		Aliases []vercelapi.Alias `json:"aliases"`
	}
	if err := json.Unmarshal([]byte(aliasesOut), &aliasResponse); err != nil {
		warning := fmt.Sprintf("Vercel alias response parse failed: %v", err)
		return nil, nil, []string{warning}, []string{warning}
	}

	candidates := []securitySurfaceCandidate{}
	findings := []securityFinding{}
	details := []string{}
	warnings := []string{}

	verifiedDomains := 0
	for _, domain := range domainResponse.Domains {
		if strings.TrimSpace(domain.Name) == "" {
			continue
		}
		for _, endpoint := range normalizeSecurityEndpoints(domain.Name, "") {
			candidates = append(candidates, securitySurfaceCandidate{
				ResourceName: domain.Name,
				ResourceType: "vercel-domain",
				Provider:     "vercel",
				Region:       "global",
				Endpoint:     endpoint,
				Source:       "vercel-domains",
				LikelyPublic: true,
			})
		}
		if domain.Verified {
			verifiedDomains++
			continue
		}
		findings = append(findings, securityFinding{
			ID:           buildDeepResearchFindingID("security-vercel-unverified-domain", domain.Name),
			Severity:     "medium",
			Category:     "misconfiguration",
			Title:        fmt.Sprintf("Vercel custom domain %s is unverified", domain.Name),
			Summary:      "An unverified Vercel custom domain can indicate takeover risk, stale delegation, or routing drift that deserves immediate review.",
			ResourceName: domain.Name,
			ResourceType: "vercel-domain",
			Provider:     "vercel",
			Region:       "global",
			Endpoint:     firstSecurityEndpointForHost(domain.Name),
			Evidence: []string{
				fmt.Sprintf("Project ID: %s", strings.TrimSpace(domain.ProjectID)),
				"Vercel reported verified=false for this domain.",
			},
			Questions: buildSecurityQuestions("misconfiguration", domain.Name, firstSecurityEndpointForHost(domain.Name), false),
		})
	}

	aliasCount := 0
	for _, alias := range aliasResponse.Aliases {
		if strings.TrimSpace(alias.Alias) == "" {
			continue
		}
		aliasCount++
		for _, endpoint := range normalizeSecurityEndpoints(alias.Alias, "") {
			candidates = append(candidates, securitySurfaceCandidate{
				ResourceName: alias.Alias,
				ResourceType: "vercel-alias",
				Provider:     "vercel",
				Region:       "global",
				Endpoint:     endpoint,
				Source:       "vercel-aliases",
				LikelyPublic: true,
			})
		}
	}

	for _, project := range selectSecurityVercelProjects(projectResponse.Projects, resources) {
		if strings.TrimSpace(project.ID) == "" {
			continue
		}
		envOut, err := client.RunAPIWithContext(ctx, "GET", fmt.Sprintf("/v10/projects/%s/env", url.PathEscape(project.ID)), "")
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("Vercel env listing failed for project %s: %v", project.Name, err))
			continue
		}
		var envResponse struct {
			Envs []vercelapi.EnvVar `json:"envs"`
		}
		if err := json.Unmarshal([]byte(envOut), &envResponse); err != nil {
			warnings = append(warnings, fmt.Sprintf("Vercel env response parse failed for project %s: %v", project.Name, err))
			continue
		}
		plainSecretKeys := []string{}
		for _, envVar := range envResponse.Envs {
			if !resourcedb.IsSecretKey(envVar.Key) {
				continue
			}
			if !strings.EqualFold(strings.TrimSpace(envVar.Type), "plain") {
				continue
			}
			plainSecretKeys = append(plainSecretKeys, strings.TrimSpace(envVar.Key))
		}
		plainSecretKeys = uniqueNonEmptyStrings(plainSecretKeys)
		if len(plainSecretKeys) == 0 {
			continue
		}
		previewKeys := plainSecretKeys
		if len(previewKeys) > 5 {
			previewKeys = previewKeys[:5]
		}
		findings = append(findings, securityFinding{
			ID:           buildDeepResearchFindingID("security-vercel-plain-secret-env", project.ID),
			Severity:     "high",
			Category:     "misconfiguration",
			Title:        fmt.Sprintf("Vercel project %s stores secret-like env vars as plain values", project.Name),
			Summary:      "Vercel reported secret-like environment variable keys with type=plain. Even without reading values, that is a strong credential hygiene warning.",
			ResourceName: project.Name,
			ResourceType: "vercel-project",
			Provider:     "vercel",
			Region:       "global",
			Evidence: []string{
				fmt.Sprintf("Project ID: %s", project.ID),
				fmt.Sprintf("Plain secret-like keys: %s", strings.Join(previewKeys, ", ")),
			},
			Questions: buildSecurityQuestions("misconfiguration", project.Name, "", false),
		})
	}

	details = append(details, fmt.Sprintf("Vercel: %d domains reviewed, %d verified, %d aliases added as public candidates.", len(domainResponse.Domains), verifiedDomains, aliasCount))

	return candidates, findings, details, warnings
}

func securityEstateHasProvider(resources []deepResearchResource, provider string) bool {
	for _, resource := range resources {
		if strings.EqualFold(inferDeepResearchProvider(resource), provider) {
			return true
		}
	}
	return false
}

func collectSecurityEstateHosts(resources []deepResearchResource, candidates []securitySurfaceCandidate) []string {
	hosts := []string{}
	for _, candidate := range candidates {
		if parsed, err := url.Parse(candidate.Endpoint); err == nil {
			if hostname := strings.TrimSpace(parsed.Hostname()); hostname != "" {
				hosts = append(hosts, hostname)
			}
		}
	}
	for _, resource := range resources {
		hosts = append(hosts, flattenSecurityAttrStrings(resource.Attributes["domains"])...)
		hosts = append(hosts, flattenSecurityAttrStrings(resource.Attributes["endpoints"])...)
		hosts = append(hosts, deepResearchFirstNonEmptyAttr(resource.Attributes, "domain", "dnsName", "hostname", "publicDns", "publicDNS", "host"))
		if strings.Contains(resource.Name, ".") {
			hosts = append(hosts, resource.Name)
		}
	}
	result := []string{}
	for _, host := range uniqueNonEmptyStrings(hosts) {
		if parsed, err := url.Parse(host); err == nil && parsed.Hostname() != "" {
			result = append(result, parsed.Hostname())
			continue
		}
		result = append(result, strings.TrimSpace(host))
	}
	return uniqueNonEmptyStrings(result)
}

func selectSecurityCloudflareZones(zones []cfclient.Zone, resources []deepResearchResource, existingCandidates []securitySurfaceCandidate) []cfclient.Zone {
	if len(zones) == 0 {
		return nil
	}

	hosts := collectSecurityEstateHosts(resources, existingCandidates)
	selected := []cfclient.Zone{}
	seen := map[string]struct{}{}
	for _, zone := range zones {
		for _, host := range hosts {
			normalizedHost := strings.ToLower(strings.TrimSpace(host))
			normalizedZone := strings.ToLower(strings.TrimSpace(zone.Name))
			if normalizedHost == normalizedZone || strings.HasSuffix(normalizedHost, "."+normalizedZone) {
				if _, ok := seen[zone.ID]; ok {
					break
				}
				seen[zone.ID] = struct{}{}
				selected = append(selected, zone)
				break
			}
		}
	}
	if len(selected) == 0 {
		selected = append(selected, zones...)
	}
	if len(selected) > 4 {
		selected = selected[:4]
	}
	return selected
}

func selectSecurityVercelProjects(projects []vercelapi.Project, resources []deepResearchResource) []vercelapi.Project {
	if len(projects) == 0 {
		return nil
	}

	relevantNames := map[string]struct{}{}
	for _, resource := range resources {
		if !strings.EqualFold(inferDeepResearchProvider(resource), "vercel") {
			continue
		}
		if trimmed := strings.ToLower(strings.TrimSpace(resource.Name)); trimmed != "" {
			relevantNames[trimmed] = struct{}{}
		}
	}

	selected := []vercelapi.Project{}
	for _, project := range projects {
		if _, ok := relevantNames[strings.ToLower(strings.TrimSpace(project.Name))]; ok {
			selected = append(selected, project)
		}
	}
	if len(selected) == 0 {
		selected = append(selected, projects...)
	}
	if len(selected) > 4 {
		selected = selected[:4]
	}
	return selected
}

func mergeSecuritySurfaceCandidates(existing []securitySurfaceCandidate, additional []securitySurfaceCandidate) []securitySurfaceCandidate {
	if len(additional) == 0 {
		return existing
	}
	merged := append([]securitySurfaceCandidate{}, existing...)
	seen := map[string]struct{}{}
	for _, candidate := range merged {
		key := strings.ToLower(strings.TrimSpace(candidate.Provider + "|" + candidate.ResourceName + "|" + candidate.Endpoint))
		seen[key] = struct{}{}
	}
	for _, candidate := range additional {
		key := strings.ToLower(strings.TrimSpace(candidate.Provider + "|" + candidate.ResourceName + "|" + candidate.Endpoint))
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		merged = append(merged, candidate)
	}
	return merged
}

func firstSecurityEndpointForHost(host string) string {
	endpoints := normalizeSecurityEndpoints(host, "")
	if len(endpoints) == 0 {
		return ""
	}
	return endpoints[0]
}

func buildSecuritySurfaceCandidates(resources []deepResearchResource) ([]securitySurfaceCandidate, []string) {
	keys := []string{"url", "urls", "endpoint", "endpoints", "publicUrl", "publicURL", "functionUrl", "functionURL", "invokeUrl", "invokeURL", "apiUrl", "apiURL", "domain", "domains", "dnsName", "dns_name", "hostname", "host", "publicIp", "publicIpAddress", "ipAddress", "natIP", "loadBalancerDns", "loadBalancerDNS", "websiteUrl", "websiteURL"}
	seen := map[string]struct{}{}
	candidates := make([]securitySurfaceCandidate, 0, 24)
	warnings := []string{}

	for _, resource := range resources {
		port := deepResearchFirstNonEmptyAttr(resource.Attributes, "port", "publicPort", "listenerPort")
		if strings.TrimSpace(port) == "" {
			port = inferSecurityDefaultPort(resource)
		}
		collectedValues := make([]string, 0, 8)
		for _, key := range keys {
			collectedValues = append(collectedValues, flattenSecurityAttrStrings(resource.Attributes[key])...)
		}
		if host := deepResearchFirstNonEmptyAttr(resource.Attributes, "publicIp", "publicIpAddress", "ipAddress", "natIP"); host != "" {
			collectedValues = append(collectedValues, host)
		}
		if publicDNS := deepResearchFirstNonEmptyAttr(resource.Attributes, "publicDns", "publicDNS", "dnsName", "hostname"); publicDNS != "" {
			collectedValues = append(collectedValues, publicDNS)
		}

		for _, raw := range uniqueNonEmptyStrings(collectedValues) {
			for _, endpoint := range normalizeSecurityEndpoints(raw, port) {
				candidate := securitySurfaceCandidate{
					ResourceID:    resource.ID,
					ResourceName:  resource.Name,
					ResourceType:  resource.Type,
					Provider:      inferDeepResearchProvider(resource),
					Region:        resource.Region,
					Endpoint:      endpoint,
					Source:        raw,
					LikelyPublic:  securityEndpointLooksPublic(endpoint),
					LikelyPrivate: securityEndpointLooksPrivate(endpoint),
				}
				key := strings.ToLower(strings.TrimSpace(candidate.ResourceID + "|" + candidate.Endpoint))
				if _, ok := seen[key]; ok {
					continue
				}
				seen[key] = struct{}{}
				candidates = append(candidates, candidate)
			}
		}
	}

	if len(candidates) > maxSecurityProbeEndpoints {
		warnings = append(warnings, fmt.Sprintf("Probe set trimmed from %d to %d endpoints to keep the scan bounded.", len(candidates), maxSecurityProbeEndpoints))
	}

	sort.Slice(candidates, func(i, j int) bool {
		leftPublic := 0
		if candidates[i].LikelyPublic {
			leftPublic = 1
		}
		rightPublic := 0
		if candidates[j].LikelyPublic {
			rightPublic = 1
		}
		if leftPublic != rightPublic {
			return leftPublic > rightPublic
		}
		return candidates[i].Endpoint < candidates[j].Endpoint
	})

	return candidates, warnings
}

func inferSecurityDefaultPort(resource deepResearchResource) string {
	lowerType := strings.ToLower(strings.TrimSpace(resource.Type))
	lowerName := strings.ToLower(strings.TrimSpace(resource.Name))
	engine := strings.ToLower(strings.TrimSpace(deepResearchFirstNonEmptyAttr(resource.Attributes, "engine", "databaseEngine", "dbEngine")))
	signals := strings.Join([]string{lowerType, lowerName, engine}, " ")
	switch {
	case strings.Contains(signals, "postgres"):
		return "5432"
	case strings.Contains(signals, "mysql") || strings.Contains(signals, "mariadb"):
		return "3306"
	case strings.Contains(signals, "redis"):
		return "6379"
	case strings.Contains(signals, "mongodb") || strings.Contains(signals, "mongo"):
		return "27017"
	case strings.Contains(signals, "elasticsearch") || strings.Contains(signals, "opensearch"):
		return "9200"
	default:
		return ""
	}
}

func flattenSecurityAttrStrings(value interface{}) []string {
	result := []string{}
	switch typed := value.(type) {
	case string:
		if trimmed := strings.TrimSpace(typed); trimmed != "" {
			result = append(result, trimmed)
		}
	case []string:
		for _, item := range typed {
			result = append(result, flattenSecurityAttrStrings(item)...)
		}
	case []interface{}:
		for _, item := range typed {
			result = append(result, flattenSecurityAttrStrings(item)...)
		}
	case map[string]interface{}:
		for _, key := range []string{"url", "endpoint", "domain", "hostname", "host", "value"} {
			result = append(result, flattenSecurityAttrStrings(typed[key])...)
		}
	}
	return uniqueNonEmptyStrings(result)
}

func normalizeSecurityEndpoints(raw string, port string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || strings.HasPrefix(trimmed, "/") {
		return nil
	}
	trimmed = strings.Trim(trimmed, ",; ")
	if strings.Contains(trimmed, "*") {
		return nil
	}
	if strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://") {
		return []string{trimmed}
	}
	if strings.HasPrefix(trimmed, "//") {
		return []string{"https:" + trimmed, "http:" + trimmed}
	}
	lowerTrimmed := strings.ToLower(trimmed)
	if strings.HasPrefix(lowerTrimmed, "tcp://") || strings.HasPrefix(lowerTrimmed, "tls://") {
		parsed, err := url.Parse(trimmed)
		if err != nil || strings.TrimSpace(parsed.Host) == "" {
			return nil
		}
		return []string{strings.ToLower(parsed.Scheme) + "://" + strings.TrimSpace(parsed.Host)}
	}
	if strings.HasPrefix(lowerTrimmed, "udp://") {
		return nil
	}

	host, effectivePort := securityNormalizeEndpointHostPort(trimmed, port)
	if host == "" {
		return nil
	}

	if !strings.Contains(host, ".") && net.ParseIP(strings.Trim(host, "[]")) == nil {
		return nil
	}
	if effectivePort != "" && !securityPortLikelyHTTP(effectivePort) {
		return []string{"tcp://" + host}
	}

	return []string{"https://" + host, "http://" + host}
}

func securityNormalizeEndpointHostPort(raw string, fallbackPort string) (string, string) {
	parsed, err := url.Parse("//" + strings.TrimSpace(raw))
	if err != nil {
		return "", ""
	}
	host := strings.TrimSpace(parsed.Hostname())
	if host == "" {
		return "", ""
	}
	effectivePort := strings.TrimSpace(parsed.Port())
	if effectivePort == "" {
		effectivePort = strings.TrimSpace(fallbackPort)
	}
	if effectivePort == "" {
		return host, ""
	}
	return net.JoinHostPort(host, effectivePort), effectivePort
}

func securityPortLikelyHTTP(port string) bool {
	switch strings.TrimSpace(port) {
	case "80", "81", "88", "443", "444", "3000", "3001", "3002", "4173", "5000", "5001", "5173", "5174", "5601", "8000", "8008", "8080", "8081", "8088", "8443", "8888", "9000", "9090", "9091", "9200", "9443":
		return true
	default:
		return false
	}
}

func securityExtractEndpointPort(raw string) string {
	parsed, err := url.Parse("//" + strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(parsed.Port())
}

func securityEndpointScheme(endpoint string) string {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(parsed.Scheme))
}

func classifySecurityProbeError(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		return "timeouts", true
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	switch {
	case message == "":
		return "", false
	case strings.Contains(message, "context deadline exceeded") || strings.Contains(message, "i/o timeout"):
		return "timeouts", true
	case strings.Contains(message, "connection refused"):
		return "refused connections", true
	case strings.Contains(message, "no such host"):
		return "dns misses", true
	case strings.Contains(message, "server gave http response to https client") || strings.Contains(message, "tls:") || strings.Contains(message, "malformed http response") || strings.Contains(message, "eof"):
		return "protocol mismatches", true
	case strings.Contains(message, "dial tcp"):
		return "dial failures", true
	default:
		return "", false
	}
}

func countSecurityProbeFailures(counts map[string]int) int {
	total := 0
	for _, count := range counts {
		total += count
	}
	return total
}

func formatSecurityProbeFailureCounts(counts map[string]int) string {
	if len(counts) == 0 {
		return ""
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, counts[key]))
	}
	return strings.Join(parts, ", ")
}

func securityEndpointLooksPrivate(endpoint string) bool {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	hostname := strings.TrimSpace(parsed.Hostname())
	if hostname == "" {
		return false
	}
	if ip := net.ParseIP(hostname); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
	}
	lower := strings.ToLower(hostname)
	return strings.Contains(lower, ".internal") || strings.Contains(lower, ".local") || strings.HasSuffix(lower, ".cluster.local")
}

func securityEndpointLooksPublic(endpoint string) bool {
	return !securityEndpointLooksPrivate(endpoint)
}

func summarizeSecurityCandidates(candidates []securitySurfaceCandidate, limit int) []string {
	lines := make([]string, 0, limit)
	for _, candidate := range candidates {
		if len(lines) >= limit {
			break
		}
		visibility := "private"
		if candidate.LikelyPublic {
			visibility = "public"
		}
		lines = append(lines, fmt.Sprintf("%s -> %s (%s)", deepResearchResourceLabel(deepResearchResource{ID: candidate.ResourceID, Name: candidate.ResourceName, Type: candidate.ResourceType}), candidate.Endpoint, visibility))
	}
	return lines
}

func runSecurityReachabilityScout(ctx context.Context, scanCtx securityScanContext) ([]securityProbeObservation, securitySubagentRun, []string) {
	candidates := scanCtx.Candidates
	if len(candidates) == 0 {
		return nil, securitySubagentRun{Name: "reachability-scout", Status: "warning", Summary: "No candidate network surfaces were available to probe."}, nil
	}
	if len(candidates) > scanCtx.ProbeLimit {
		candidates = candidates[:scanCtx.ProbeLimit]
	}

	observations := make([]securityProbeObservation, 0, len(candidates))
	warnings := []string{}
	suppressedFailures := map[string]int{}
	attempted := len(candidates)
	for _, candidate := range candidates {
		obs, err := probeSecurityEndpoint(ctx, candidate, scanCtx.AuthPack)
		if err != nil {
			if label, suppress := classifySecurityProbeError(err); suppress {
				suppressedFailures[label]++
				continue
			}
			warnings = append(warnings, fmt.Sprintf("Probe failed for %s: %v", candidate.Endpoint, err))
			continue
		}
		observations = append(observations, obs)
	}

	reachableCount := 0
	authCount := 0
	httpCount := 0
	socketCount := 0
	for _, obs := range observations {
		if obs.Reachable {
			reachableCount++
		}
		if obs.RequiresAuth || obs.Authenticated {
			authCount++
		}
		if obs.StatusCode > 0 {
			httpCount++
		} else if obs.Reachable {
			socketCount++
		}
	}
	details := []string{
		fmt.Sprintf("Attempted %d of %d candidate endpoints.", attempted, len(scanCtx.Candidates)),
		fmt.Sprintf("HTTP responses collected: %d", httpCount),
		fmt.Sprintf("TCP/TLS socket handshakes collected: %d", socketCount),
		fmt.Sprintf("Reachable endpoints: %d", reachableCount),
		fmt.Sprintf("Auth-gated or auth-unlocked surfaces: %d", authCount),
	}
	if suppressedCount := countSecurityProbeFailures(suppressedFailures); suppressedCount > 0 {
		details = append(details, fmt.Sprintf("Suppressed %d expected probe misses (%s).", suppressedCount, formatSecurityProbeFailureCounts(suppressedFailures)))
	}
	if len(warnings) > 0 {
		details = append(details, fmt.Sprintf("Unexpected probe errors surfaced as warnings: %d", len(warnings)))
	}
	return observations, securitySubagentRun{
		Name:    "reachability-scout",
		Status:  ternaryStatus(reachableCount > 0, "ok", "warning"),
		Summary: fmt.Sprintf("Probed %d endpoints; %d reachable, %d auth-signaled.", attempted, reachableCount, authCount),
		Details: details,
	}, warnings
}

func probeSecurityEndpoint(parent context.Context, candidate securitySurfaceCandidate, authPack securityRuntimeAuthPack) (securityProbeObservation, error) {
	if scheme := securityEndpointScheme(candidate.Endpoint); scheme == "tcp" || scheme == "tls" {
		return probeSecuritySocket(parent, candidate)
	}

	ctx, cancel := context.WithTimeout(parent, securityProbeTimeout)
	defer cancel()

	client := &http.Client{
		Timeout: securityProbeTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	observation := securityProbeObservation{
		Endpoint:   candidate.Endpoint,
		ResourceID: candidate.ResourceID,
		Scheme:     securityEndpointScheme(candidate.Endpoint),
		Port:       securityExtractEndpointPort(strings.TrimPrefix(strings.TrimPrefix(candidate.Endpoint, "https://"), "http://")),
		Method:     "HEAD",
	}

	result, err := doSecurityHTTPProbeDetailed(ctx, client, candidate.Endpoint, "HEAD", nil)
	status, headers, location := result.StatusCode, result.Headers, result.Location
	if err != nil || status == 405 || status == 400 {
		result, err = doSecurityHTTPProbeDetailed(ctx, client, candidate.Endpoint, "GET", nil)
		status, headers, location = result.StatusCode, result.Headers, result.Location
		observation.Method = "GET"
	}
	if err != nil {
		return observation, err
	}
	observation.StatusCode = status
	observation.Reachable = status > 0
	observation.Location = location
	observation.RequiresAuth = status == http.StatusUnauthorized || status == http.StatusForbidden || strings.TrimSpace(headers.Get("WWW-Authenticate")) != ""
	observation.AllowsCORS = strings.TrimSpace(headers.Get("Access-Control-Allow-Origin")) != ""
	observation.Server = strings.TrimSpace(headers.Get("Server"))
	observation.ContentType = strings.TrimSpace(headers.Get("Content-Type"))
	if strings.TrimSpace(result.TLSVersion) != "" {
		observation.TLSEnabled = true
		observation.TLSVersion = strings.TrimSpace(result.TLSVersion)
	}
	observation.MissingHeaders, observation.WeakHeaders, observation.CookieIssues, observation.TLSIssues = analyzeSecurityHTTPPosture(candidate.Endpoint, headers, observation)
	observation.CORSIssues = probeSecurityCORS(ctx, client, candidate.Endpoint, observation.RequiresAuth)
	observation.MethodIssues = probeSecurityRiskyMethods(ctx, client, candidate.Endpoint, observation.RequiresAuth)
	observation.HeaderTrustIssues = probeSecurityHeaderTrust(ctx, client, candidate.Endpoint, observation.StatusCode)
	observation.CacheIssues = analyzeSecurityCachePolicy(headers, observation)

	authNotes := []string{}
	if authPack.HasAuth() && observation.RequiresAuth {
		authHeaders := buildSecurityAuthHeaders(authPack)
		authResult, authErr := doSecurityHTTPProbeDetailed(ctx, client, candidate.Endpoint, "GET", authHeaders)
		authStatus := authResult.StatusCode
		authRespHeaders := authResult.Headers
		if authErr == nil && authStatus > 0 {
			if authStatus != http.StatusUnauthorized && authStatus != http.StatusForbidden {
				observation.Authenticated = true
				authNotes = append(authNotes, fmt.Sprintf("User-supplied auth changed the response to %d.", authStatus))
				if observation.ContentType == "" {
					observation.ContentType = strings.TrimSpace(authRespHeaders.Get("Content-Type"))
				}
				authObservation := observation
				authObservation.StatusCode = authStatus
				authObservation.Authenticated = true
				authObservation.ContentType = coalesceSecurityName(strings.TrimSpace(authRespHeaders.Get("Content-Type")), observation.ContentType)
				observation.CacheIssues = uniqueNonEmptyStrings(append(observation.CacheIssues, analyzeSecurityCachePolicy(authRespHeaders, authObservation)...))
				observation.RateLimitIssues = uniqueNonEmptyStrings(append(observation.RateLimitIssues, analyzeSecurityRateLimitControls(candidate.Endpoint, authRespHeaders, authObservation)...))
			}
		}
	}

	if observation.Reachable {
		pathHeaders := map[string]string(nil)
		if observation.Authenticated {
			pathHeaders = buildSecurityAuthHeaders(authPack)
		}
		observation.InterestingPaths = discoverInterestingSecurityPaths(ctx, client, candidate.Endpoint, pathHeaders)
	}

	observation.RateLimitIssues = uniqueNonEmptyStrings(append(observation.RateLimitIssues, analyzeSecurityRateLimitControls(candidate.Endpoint, headers, observation)...))
	observation.Notes = uniqueNonEmptyStrings(append(buildSecurityProbeNotes(observation), authNotes...))

	return observation, nil
}

func probeSecuritySocket(parent context.Context, candidate securitySurfaceCandidate) (securityProbeObservation, error) {
	ctx, cancel := context.WithTimeout(parent, securityProbeTimeout)
	defer cancel()

	parsed, err := url.Parse(strings.TrimSpace(candidate.Endpoint))
	if err != nil {
		return securityProbeObservation{Endpoint: candidate.Endpoint, ResourceID: candidate.ResourceID}, err
	}

	observation := securityProbeObservation{
		Endpoint:   candidate.Endpoint,
		ResourceID: candidate.ResourceID,
		Scheme:     strings.ToLower(strings.TrimSpace(parsed.Scheme)),
		Port:       strings.TrimSpace(parsed.Port()),
		Method:     "TCP connect",
	}
	if strings.TrimSpace(parsed.Host) == "" {
		return observation, fmt.Errorf("missing socket host")
	}

	dialer := &net.Dialer{Timeout: securityProbeTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", parsed.Host)
	if err != nil {
		return observation, err
	}
	observation.Reachable = true
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		_ = tcpConn.SetLinger(0)
	}
	_ = conn.SetReadDeadline(time.Now().Add(350 * time.Millisecond))
	banner := make([]byte, 256)
	if n, readErr := conn.Read(banner); n > 0 {
		observation.Banner = securitySanitizeProbeBanner(banner[:n])
	} else if readErr == nil {
		observation.Banner = ""
	}
	_ = conn.Close()

	if securityShouldAttemptTLS(observation.Scheme, observation.Port) {
		if tlsVersion, tlsErr := securityProbeTLSVersion(ctx, dialer, parsed.Hostname(), parsed.Host); tlsErr == nil {
			observation.TLSEnabled = true
			observation.TLSVersion = tlsVersion
		}
	}

	observation.Notes = buildSecurityProbeNotes(observation)
	return observation, nil
}

func securityShouldAttemptTLS(scheme string, port string) bool {
	if strings.EqualFold(strings.TrimSpace(scheme), "tls") {
		return true
	}
	switch strings.TrimSpace(port) {
	case "443", "465", "636", "853", "993", "995", "8443", "9443", "5671":
		return true
	default:
		return false
	}
}

func securityProbeTLSVersion(ctx context.Context, dialer *net.Dialer, hostname string, address string) (string, error) {
	config := &tls.Config{InsecureSkipVerify: true}
	if net.ParseIP(strings.TrimSpace(hostname)) == nil {
		config.ServerName = strings.TrimSpace(hostname)
	}
	conn, err := tls.DialWithDialer(dialer, "tcp", address, config)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	return securityTLSVersionLabel(conn.ConnectionState().Version), nil
}

func securityTLSVersionLabel(version uint16) string {
	switch version {
	case tls.VersionTLS10:
		return "TLS1.0"
	case tls.VersionTLS11:
		return "TLS1.1"
	case tls.VersionTLS12:
		return "TLS1.2"
	case tls.VersionTLS13:
		return "TLS1.3"
	default:
		return fmt.Sprintf("0x%x", version)
	}
}

func securitySanitizeProbeBanner(data []byte) string {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return ""
	}
	runes := make([]rune, 0, len(trimmed))
	for _, r := range trimmed {
		if r == '\n' || r == '\r' || r == '\t' || (r >= 32 && r < 127) {
			runes = append(runes, r)
		}
	}
	sanitized := strings.TrimSpace(string(runes))
	if len(sanitized) > 160 {
		sanitized = sanitized[:160]
	}
	return sanitized
}

type securityHTTPProbeResult struct {
	StatusCode int
	Headers    http.Header
	Location   string
	TLSVersion string
}

func doSecurityHTTPProbeDetailed(ctx context.Context, client *http.Client, endpoint string, method string, headers map[string]string) (securityHTTPProbeResult, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
	if err != nil {
		return securityHTTPProbeResult{}, err
	}
	req.Header.Set("User-Agent", "clanker-security-scan/1.0")
	req.Header.Set("Accept", "application/json, text/plain;q=0.9, */*;q=0.8")
	for key, value := range headers {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(key), "Host") {
			req.Host = strings.TrimSpace(value)
			continue
		}
		req.Header.Set(key, value)
	}
	resp, err := client.Do(req)
	if err != nil {
		return securityHTTPProbeResult{}, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 2048))
	result := securityHTTPProbeResult{
		StatusCode: resp.StatusCode,
		Headers:    resp.Header.Clone(),
		Location:   strings.TrimSpace(resp.Header.Get("Location")),
	}
	if resp.TLS != nil {
		result.TLSVersion = securityTLSVersionLabel(resp.TLS.Version)
	}
	return result, nil
}

func doSecurityHTTPProbe(ctx context.Context, client *http.Client, endpoint string, method string, headers map[string]string) (int, http.Header, string, error) {
	result, err := doSecurityHTTPProbeDetailed(ctx, client, endpoint, method, headers)
	if err != nil {
		return 0, nil, "", err
	}
	return result.StatusCode, result.Headers, result.Location, nil
}

func analyzeSecurityHTTPPosture(endpoint string, headers http.Header, observation securityProbeObservation) ([]string, []string, []string, []string) {
	missingHeaders := []string{}
	weakHeaders := []string{}
	cookieIssues := []string{}
	tlsIssues := []string{}

	scheme := strings.ToLower(strings.TrimSpace(securityEndpointScheme(endpoint)))
	contentType := strings.ToLower(strings.TrimSpace(observation.ContentType))
	browserSurface := contentType == "" || strings.Contains(contentType, "text/html")
	if scheme == "https" {
		if strings.TrimSpace(headers.Get("Strict-Transport-Security")) == "" {
			missingHeaders = append(missingHeaders, "Strict-Transport-Security is missing on an HTTPS surface")
		}
	} else if scheme == "http" && observation.StatusCode > 0 {
		location := strings.ToLower(strings.TrimSpace(observation.Location))
		if !strings.HasPrefix(location, "https://") {
			tlsIssues = append(tlsIssues, "Plain HTTP endpoint is reachable without an observed HTTPS redirect")
		}
	}
	if strings.EqualFold(observation.TLSVersion, "TLS1.0") || strings.EqualFold(observation.TLSVersion, "TLS1.1") {
		tlsIssues = append(tlsIssues, fmt.Sprintf("Weak TLS protocol negotiated: %s", observation.TLSVersion))
	}
	if strings.TrimSpace(headers.Get("X-Content-Type-Options")) == "" {
		missingHeaders = append(missingHeaders, "X-Content-Type-Options is missing")
	} else if !strings.EqualFold(strings.TrimSpace(headers.Get("X-Content-Type-Options")), "nosniff") {
		weakHeaders = append(weakHeaders, fmt.Sprintf("X-Content-Type-Options is not nosniff: %s", strings.TrimSpace(headers.Get("X-Content-Type-Options"))))
	}
	if browserSurface {
		csp := strings.TrimSpace(headers.Get("Content-Security-Policy"))
		xfo := strings.TrimSpace(headers.Get("X-Frame-Options"))
		if csp == "" {
			missingHeaders = append(missingHeaders, "Content-Security-Policy is missing on a browser-facing response")
		}
		if xfo == "" && !strings.Contains(strings.ToLower(csp), "frame-ancestors") {
			missingHeaders = append(missingHeaders, "No X-Frame-Options or CSP frame-ancestors clickjacking control was observed")
		}
	}
	if strings.TrimSpace(headers.Get("Referrer-Policy")) == "" {
		missingHeaders = append(missingHeaders, "Referrer-Policy is missing")
	}
	for _, cookie := range headers.Values("Set-Cookie") {
		trimmed := strings.TrimSpace(cookie)
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)
		name := strings.TrimSpace(strings.SplitN(trimmed, "=", 2)[0])
		if name == "" {
			name = "cookie"
		}
		if !strings.Contains(lower, "secure") && scheme == "https" {
			cookieIssues = append(cookieIssues, fmt.Sprintf("%s cookie is missing Secure", name))
		}
		if !strings.Contains(lower, "httponly") {
			cookieIssues = append(cookieIssues, fmt.Sprintf("%s cookie is missing HttpOnly", name))
		}
		if !strings.Contains(lower, "samesite=") {
			cookieIssues = append(cookieIssues, fmt.Sprintf("%s cookie is missing SameSite", name))
		}
	}
	return uniqueNonEmptyStrings(missingHeaders), uniqueNonEmptyStrings(weakHeaders), uniqueNonEmptyStrings(cookieIssues), uniqueNonEmptyStrings(tlsIssues)
}

func probeSecurityCORS(ctx context.Context, client *http.Client, endpoint string, requiresAuth bool) []string {
	headers := map[string]string{
		"Origin":                         "https://clanker-security.invalid",
		"Access-Control-Request-Method":  "GET",
		"Access-Control-Request-Headers": "Authorization, Content-Type",
	}
	status, respHeaders, _, err := doSecurityHTTPProbe(ctx, client, endpoint, "OPTIONS", headers)
	if err != nil || status == 0 {
		return nil
	}
	return analyzeSecurityCORSHeaders(respHeaders, requiresAuth)
}

func analyzeSecurityCORSHeaders(headers http.Header, requiresAuth bool) []string {
	issues := []string{}
	allowOrigin := strings.TrimSpace(headers.Get("Access-Control-Allow-Origin"))
	allowCredentials := strings.EqualFold(strings.TrimSpace(headers.Get("Access-Control-Allow-Credentials")), "true")
	if allowOrigin == "" {
		return nil
	}
	allowOriginLower := strings.ToLower(allowOrigin)
	if allowOrigin == "*" {
		issues = append(issues, "Access-Control-Allow-Origin permits every origin")
	}
	if allowOriginLower == "https://clanker-security.invalid" {
		issues = append(issues, "Access-Control-Allow-Origin reflects an untrusted test origin")
	}
	if allowCredentials {
		issues = append(issues, "Access-Control-Allow-Credentials is true")
	}
	if allowCredentials && (allowOrigin == "*" || allowOriginLower == "https://clanker-security.invalid" || requiresAuth) {
		issues = append(issues, "Credentialed CORS can expose authenticated browser context if sensitive routes share this policy")
	}
	return uniqueNonEmptyStrings(issues)
}

func probeSecurityRiskyMethods(ctx context.Context, client *http.Client, endpoint string, requiresAuth bool) []string {
	status, headers, _, err := doSecurityHTTPProbe(ctx, client, endpoint, "OPTIONS", nil)
	if err != nil || status == 0 {
		return nil
	}
	values := []string{headers.Get("Allow"), headers.Get("Access-Control-Allow-Methods")}
	risky := []string{}
	for _, value := range values {
		lower := strings.ToUpper(value)
		for _, method := range []string{"PUT", "PATCH", "DELETE", "TRACE", "CONNECT"} {
			if strings.Contains(lower, method) {
				risky = append(risky, method)
			}
		}
	}
	risky = uniqueNonEmptyStrings(risky)
	if len(risky) == 0 {
		return nil
	}
	prefix := "Risky HTTP methods advertised"
	if !requiresAuth {
		prefix = "Risky HTTP methods advertised before an auth challenge"
	}
	return []string{fmt.Sprintf("%s: %s", prefix, strings.Join(risky, ", "))}
}

func probeSecurityHeaderTrust(ctx context.Context, client *http.Client, endpoint string, baselineStatus int) []string {
	injectedHost := "clanker-security.invalid"
	issues := []string{}

	hostHeaders := map[string]string{
		"Host":              injectedHost,
		"X-Forwarded-Host":  injectedHost,
		"X-Forwarded-Proto": "http",
	}
	status, _, location, err := doSecurityHTTPProbe(ctx, client, endpoint, "GET", hostHeaders)
	if err == nil && status > 0 {
		if securityLocationUsesHost(location, injectedHost) {
			issues = append(issues, fmt.Sprintf("Host/X-Forwarded-Host probe redirected to attacker-controlled host: %s", location))
		}
		if securityStatusBypassedAuth(baselineStatus, status) {
			issues = append(issues, fmt.Sprintf("Host/X-Forwarded-Host probe changed an auth challenge into HTTP %d", status))
		}
	}

	rewriteHeaders := map[string]string{
		"X-Forwarded-For": "127.0.0.1",
		"X-Original-URL":  "/admin",
		"X-Rewrite-URL":   "/admin",
	}
	status, _, _, err = doSecurityHTTPProbe(ctx, client, endpoint, "GET", rewriteHeaders)
	if err == nil && status > 0 && securityStatusBypassedAuth(baselineStatus, status) {
		issues = append(issues, fmt.Sprintf("Proxy rewrite header probe changed an auth challenge into HTTP %d", status))
	}

	return uniqueNonEmptyStrings(issues)
}

func securityLocationUsesHost(location string, host string) bool {
	location = strings.TrimSpace(location)
	host = strings.ToLower(strings.TrimSpace(host))
	if location == "" || host == "" {
		return false
	}
	parsed, err := url.Parse(location)
	if err == nil && strings.EqualFold(strings.TrimSpace(parsed.Hostname()), host) {
		return true
	}
	lower := strings.ToLower(location)
	return strings.Contains(lower, "//"+host) || strings.HasPrefix(lower, host+"/") || lower == host
}

func securityStatusBypassedAuth(baselineStatus int, probeStatus int) bool {
	if baselineStatus != http.StatusUnauthorized && baselineStatus != http.StatusForbidden {
		return false
	}
	return probeStatus >= 200 && probeStatus < 400
}

func analyzeSecurityCachePolicy(headers http.Header, observation securityProbeObservation) []string {
	cacheControl := strings.ToLower(strings.TrimSpace(headers.Get("Cache-Control")))
	pragma := strings.ToLower(strings.TrimSpace(headers.Get("Pragma")))
	setCookie := len(headers.Values("Set-Cookie")) > 0
	authLike := observation.RequiresAuth || observation.Authenticated || setCookie
	if !authLike {
		return nil
	}

	issues := []string{}
	if cacheControl == "" && !strings.Contains(pragma, "no-cache") {
		issues = append(issues, "Auth, session, or cookie-like response did not set Cache-Control")
	}
	if strings.Contains(cacheControl, "public") {
		issues = append(issues, fmt.Sprintf("Cache-Control permits public caching on auth/session-like content: %s", headers.Get("Cache-Control")))
	}
	if setCookie && !securityCacheControlIsSessionSafe(cacheControl) && !strings.Contains(pragma, "no-cache") {
		issues = append(issues, "Set-Cookie response lacks private, no-store, or no-cache cache directives")
	}
	return uniqueNonEmptyStrings(issues)
}

func securityCacheControlIsSessionSafe(cacheControl string) bool {
	cacheControl = strings.ToLower(strings.TrimSpace(cacheControl))
	if cacheControl == "" {
		return false
	}
	return strings.Contains(cacheControl, "no-store") || strings.Contains(cacheControl, "private") || strings.Contains(cacheControl, "no-cache")
}

func analyzeSecurityRateLimitControls(endpoint string, headers http.Header, observation securityProbeObservation) []string {
	if observation.StatusCode < 200 || observation.StatusCode >= 400 {
		return nil
	}
	if observation.RequiresAuth && !observation.Authenticated {
		return nil
	}
	if !securityLooksAPILike(endpoint, observation) {
		return nil
	}
	if securityHasRateLimitSignal(headers) {
		return nil
	}
	responseKind := "public API-like response"
	if observation.Authenticated {
		responseKind = "authenticated API-like response"
	}
	return []string{fmt.Sprintf("No RateLimit, X-RateLimit, or Retry-After headers observed on %s", responseKind)}
}

func securityLooksAPILike(endpoint string, observation securityProbeObservation) bool {
	contentType := strings.ToLower(strings.TrimSpace(observation.ContentType))
	for _, marker := range []string{"json", "graphql", "grpc", "protobuf", "problem+", "ndjson", "event-stream"} {
		if strings.Contains(contentType, marker) {
			return true
		}
	}
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err == nil {
		path := strings.ToLower(strings.TrimSpace(parsed.Path))
		for _, marker := range []string{"/api", "/graphql", ".json", "openapi", "swagger"} {
			if strings.Contains(path, marker) {
				return true
			}
		}
	}
	for _, path := range observation.InterestingPaths {
		lower := strings.ToLower(path)
		if strings.Contains(lower, "/graphql") || strings.Contains(lower, "openapi") || strings.Contains(lower, "swagger") || strings.Contains(lower, "api-docs") {
			return true
		}
	}
	return false
}

func securityHasRateLimitSignal(headers http.Header) bool {
	for _, header := range []string{
		"RateLimit-Limit",
		"RateLimit-Remaining",
		"RateLimit-Reset",
		"X-RateLimit-Limit",
		"X-RateLimit-Remaining",
		"X-RateLimit-Reset",
		"Retry-After",
		"X-Rate-Limit-Limit",
		"X-Rate-Limit-Remaining",
	} {
		if strings.TrimSpace(headers.Get(header)) != "" {
			return true
		}
	}
	return false
}

func buildSecurityAuthHeaders(pack securityRuntimeAuthPack) map[string]string {
	headers := map[string]string{}
	for key, value := range pack.Headers {
		headers[key] = value
	}
	if strings.TrimSpace(pack.BearerToken) != "" {
		headers["Authorization"] = "Bearer " + strings.TrimSpace(pack.BearerToken)
	} else if strings.TrimSpace(pack.Username) != "" && strings.TrimSpace(pack.Password) != "" {
		encoded := base64.StdEncoding.EncodeToString([]byte(strings.TrimSpace(pack.Username) + ":" + strings.TrimSpace(pack.Password)))
		headers["Authorization"] = "Basic " + encoded
	}
	if strings.TrimSpace(pack.Cookie) != "" {
		headers["Cookie"] = strings.TrimSpace(pack.Cookie)
	}
	return headers
}

func buildSecurityProbeNotes(observation securityProbeObservation) []string {
	notes := []string{}
	if observation.Reachable && observation.StatusCode == 0 {
		portLabel := strings.TrimSpace(observation.Port)
		if portLabel == "" {
			portLabel = "unknown"
		}
		notes = append(notes, fmt.Sprintf("TCP connect succeeded on port %s.", portLabel))
	}
	if observation.StatusCode > 0 {
		notes = append(notes, fmt.Sprintf("HTTP %d via %s", observation.StatusCode, observation.Method))
	}
	if observation.Server != "" {
		notes = append(notes, fmt.Sprintf("Server banner: %s", observation.Server))
	}
	if observation.Banner != "" {
		notes = append(notes, fmt.Sprintf("Service banner: %s", observation.Banner))
	}
	if observation.TLSEnabled {
		notes = append(notes, fmt.Sprintf("Direct TLS handshake succeeded (%s).", coalesceSecurityName(observation.TLSVersion, "TLS detected")))
	}
	if observation.ContentType != "" {
		notes = append(notes, fmt.Sprintf("Content-Type: %s", observation.ContentType))
	}
	if observation.Location != "" {
		notes = append(notes, fmt.Sprintf("Redirects to %s", observation.Location))
	}
	if observation.AllowsCORS {
		notes = append(notes, "CORS headers are present.")
	}
	for _, issue := range observation.CORSIssues {
		notes = append(notes, "CORS issue: "+issue)
	}
	for _, issue := range observation.MissingHeaders {
		notes = append(notes, "Missing header: "+issue)
	}
	for _, issue := range observation.WeakHeaders {
		notes = append(notes, "Weak header: "+issue)
	}
	for _, issue := range observation.CookieIssues {
		notes = append(notes, "Cookie issue: "+issue)
	}
	for _, issue := range observation.TLSIssues {
		notes = append(notes, "TLS issue: "+issue)
	}
	for _, issue := range observation.MethodIssues {
		notes = append(notes, "Method issue: "+issue)
	}
	for _, issue := range observation.HeaderTrustIssues {
		notes = append(notes, "Header trust issue: "+issue)
	}
	for _, issue := range observation.CacheIssues {
		notes = append(notes, "Cache policy issue: "+issue)
	}
	for _, issue := range observation.RateLimitIssues {
		notes = append(notes, "API resource-control issue: "+issue)
	}
	if observation.RequiresAuth {
		notes = append(notes, "The endpoint signaled authentication or authorization checks.")
	}
	return notes
}

func discoverInterestingSecurityPaths(ctx context.Context, client *http.Client, endpoint string, headers map[string]string) []string {
	commonPaths := []string{
		"/health",
		"/healthz",
		"/ready",
		"/live",
		"/metrics",
		"/debug/vars",
		"/server-status",
		"/admin",
		"/graphql",
		"/openapi.json",
		"/swagger.json",
		"/swagger/index.html",
		"/api-docs",
		"/docs",
		"/.well-known/openid-configuration",
		"/.env",
		"/.git/config",
		"/config.json",
		"/actuator/env",
		"/actuator/heapdump",
	}
	baseURL, err := url.Parse(endpoint)
	if err != nil {
		return nil
	}
	baseURL.Path = ""
	baseURL.RawQuery = ""
	baseURL.Fragment = ""
	results := make([]string, 0, 8)
	for _, path := range commonPaths {
		checkURL := *baseURL
		checkURL.Path = path
		status, _, _, err := doSecurityHTTPProbe(ctx, client, checkURL.String(), "GET", headers)
		if err != nil {
			continue
		}
		if status >= 200 && status < 400 {
			results = append(results, fmt.Sprintf("%s (%d)", path, status))
		}
		if len(results) >= 8 {
			break
		}
	}
	return results
}

func buildSecurityReachabilityFindings(candidates []securitySurfaceCandidate, probes []securityProbeObservation, authPack securityRuntimeAuthPack) []securityFinding {
	candidateByEndpoint := map[string]securitySurfaceCandidate{}
	for _, candidate := range candidates {
		candidateByEndpoint[candidate.Endpoint] = candidate
	}
	findings := make([]securityFinding, 0, len(probes))
	for _, probe := range probes {
		candidate, ok := candidateByEndpoint[probe.Endpoint]
		if !ok || !probe.Reachable {
			continue
		}
		resourceLabel := candidate.ResourceName
		if strings.TrimSpace(resourceLabel) == "" {
			resourceLabel = candidate.ResourceID
		}
		evidence := append([]string{}, probe.Notes...)
		for _, interestingPath := range probe.InterestingPaths {
			evidence = append(evidence, fmt.Sprintf("Interesting path reachable: %s", interestingPath))
		}
		if probe.StatusCode == 0 {
			findings = append(findings, securityFinding{
				ID:           buildDeepResearchFindingID("security-socket-surface", candidate.ResourceID+"|"+probe.Endpoint),
				Severity:     securitySeverityForSurface(candidate.ResourceType, candidate.LikelyPublic),
				Category:     "reachable-surface",
				Title:        fmt.Sprintf("%s exposes a reachable TCP service", resourceLabel),
				Summary:      fmt.Sprintf("The socket %s accepted a TCP connection. Treat it as a live externally reachable service until intended clients, auth requirements, and network guards are verified.", probe.Endpoint),
				ResourceID:   candidate.ResourceID,
				ResourceName: candidate.ResourceName,
				ResourceType: candidate.ResourceType,
				Provider:     candidate.Provider,
				Region:       candidate.Region,
				Endpoint:     probe.Endpoint,
				Reachable:    true,
				Evidence:     evidence,
				Questions:    buildSecurityQuestions("reachable-surface", candidate.ResourceName, probe.Endpoint, false),
			})
			continue
		}
		severity := "medium"
		category := "reachable-surface"
		summary := fmt.Sprintf("%s responded from %s with HTTP %d.", resourceLabel, probe.Endpoint, probe.StatusCode)
		title := fmt.Sprintf("%s is network reachable", resourceLabel)
		if !probe.RequiresAuth {
			category = "public-surface"
			severity = securitySeverityForSurface(candidate.ResourceType, candidate.LikelyPublic)
			title = fmt.Sprintf("%s exposes an unauthenticated reachable surface", resourceLabel)
			summary = fmt.Sprintf("The endpoint %s answered without a clear auth challenge. That is a high-signal entry point for reconnaissance and control-plane mapping.", probe.Endpoint)
		} else if probe.Authenticated {
			category = "authenticated-surface"
			severity = "high"
			title = fmt.Sprintf("User-supplied auth unlocks %s", resourceLabel)
			summary = fmt.Sprintf("The endpoint %s moved past its initial auth gate when the provided auth pack was applied. That makes it a strong authenticated review target.", probe.Endpoint)
		}
		findings = append(findings, securityFinding{
			ID:           buildDeepResearchFindingID("security-surface", candidate.ResourceID+"|"+probe.Endpoint),
			Severity:     severity,
			Category:     category,
			Title:        title,
			Summary:      summary,
			ResourceID:   candidate.ResourceID,
			ResourceName: candidate.ResourceName,
			ResourceType: candidate.ResourceType,
			Provider:     candidate.Provider,
			Region:       candidate.Region,
			Endpoint:     probe.Endpoint,
			Reachable:    true,
			StatusCode:   probe.StatusCode,
			RequiresAuth: probe.RequiresAuth,
			Evidence:     evidence,
			Questions:    buildSecurityQuestions(category, candidate.ResourceName, probe.Endpoint, authPack.HasAuth()),
		})
	}
	return findings
}

func buildSecurityHTTPPostureFindings(candidates []securitySurfaceCandidate, probes []securityProbeObservation) []securityFinding {
	candidateByEndpoint := map[string]securitySurfaceCandidate{}
	for _, candidate := range candidates {
		candidateByEndpoint[candidate.Endpoint] = candidate
	}
	findings := []securityFinding{}
	for _, probe := range probes {
		candidate, ok := candidateByEndpoint[probe.Endpoint]
		if !ok || !probe.Reachable || probe.StatusCode == 0 {
			continue
		}
		resourceLabel := coalesceSecurityName(candidate.ResourceName, candidate.ResourceID, probe.Endpoint, "this endpoint")
		baseEvidence := []string{
			fmt.Sprintf("Endpoint: %s", probe.Endpoint),
			fmt.Sprintf("HTTP status: %d", probe.StatusCode),
		}
		if strings.TrimSpace(probe.ContentType) != "" {
			baseEvidence = append(baseEvidence, fmt.Sprintf("Content-Type: %s", probe.ContentType))
		}

		headerEvidence := uniqueNonEmptyStrings(append(append([]string{}, probe.MissingHeaders...), probe.WeakHeaders...))
		headerEvidence = append(headerEvidence, probe.CookieIssues...)
		if len(headerEvidence) > 0 {
			severity := "medium"
			if len(probe.CookieIssues) > 0 && (probe.RequiresAuth || probe.Authenticated) {
				severity = "high"
			}
			findings = append(findings, securityFinding{
				ID:           buildDeepResearchFindingID("security-http-hardening", candidate.ResourceID+"|"+probe.Endpoint),
				Severity:     severity,
				Category:     "http-hardening",
				Title:        fmt.Sprintf("%s has HTTP response hardening gaps", resourceLabel),
				Summary:      "The endpoint responded to normal HTTP probes but is missing or weakening browser, content-sniffing, referrer, session cookie, or transport-hardening controls. These issues often become useful when chained with XSS, clickjacking, credential theft, or information disclosure.",
				Confidence:   "high",
				BlastRadius:  fmt.Sprintf("%s and any browser, API client, or session context that trusts it.", resourceLabel),
				ResourceID:   candidate.ResourceID,
				ResourceName: candidate.ResourceName,
				ResourceType: candidate.ResourceType,
				Provider:     candidate.Provider,
				Region:       candidate.Region,
				Endpoint:     probe.Endpoint,
				Reachable:    true,
				StatusCode:   probe.StatusCode,
				Frameworks:   []string{"OWASP WSTG-CONF-14 HTTP Security Header Misconfigurations", "OWASP A05 Security Misconfiguration"},
				Threats:      []string{"clickjacking", "content sniffing", "session cookie theft", "browser hardening gap"},
				Evidence:     uniqueNonEmptyStrings(append(baseEvidence, headerEvidence...)),
				Questions:    buildSecurityQuestions("http-hardening", candidate.ResourceName, probe.Endpoint, probe.Authenticated),
			})
		}

		if len(probe.CORSIssues) > 0 {
			severity := "high"
			if !probe.RequiresAuth && !probe.Authenticated {
				severity = "medium"
			}
			findings = append(findings, securityFinding{
				ID:           buildDeepResearchFindingID("security-cors", candidate.ResourceID+"|"+probe.Endpoint),
				Severity:     severity,
				Category:     "cors-misconfiguration",
				Title:        fmt.Sprintf("%s has unsafe CORS behavior", resourceLabel),
				Summary:      "The endpoint responded to an untrusted Origin probe with permissive or credential-relevant CORS behavior. If sensitive data or authenticated routes share this policy, browser-based exfiltration becomes plausible.",
				Confidence:   "high",
				BlastRadius:  fmt.Sprintf("Browser clients, authenticated sessions, and any API responses reachable from %s.", resourceLabel),
				ResourceID:   candidate.ResourceID,
				ResourceName: candidate.ResourceName,
				ResourceType: candidate.ResourceType,
				Provider:     candidate.Provider,
				Region:       candidate.Region,
				Endpoint:     probe.Endpoint,
				Reachable:    true,
				StatusCode:   probe.StatusCode,
				RequiresAuth: probe.RequiresAuth,
				Frameworks:   []string{"OWASP WSTG-CLNT-07 Cross Origin Resource Sharing", "OWASP A05 Security Misconfiguration"},
				Threats:      []string{"CORS data exfiltration", "credentialed cross-origin request", "origin reflection"},
				Evidence:     uniqueNonEmptyStrings(append(baseEvidence, probe.CORSIssues...)),
				Questions:    buildSecurityQuestions("cors-misconfiguration", candidate.ResourceName, probe.Endpoint, probe.Authenticated),
			})
		}

		if len(probe.TLSIssues) > 0 {
			findings = append(findings, securityFinding{
				ID:           buildDeepResearchFindingID("security-tls-posture", candidate.ResourceID+"|"+probe.Endpoint),
				Severity:     ternaryString(strings.Contains(strings.Join(probe.TLSIssues, " "), "TLS1.0") || strings.Contains(strings.Join(probe.TLSIssues, " "), "TLS1.1"), "high", "medium"),
				Category:     "tls-posture",
				Title:        fmt.Sprintf("%s has weak transport posture", resourceLabel),
				Summary:      "The endpoint's transport behavior leaves room for downgrade, interception, or cleartext access. Confirm whether the service should force HTTPS with modern TLS and HSTS.",
				Confidence:   "high",
				BlastRadius:  fmt.Sprintf("Traffic, sessions, tokens, and API data exchanged with %s.", resourceLabel),
				ResourceID:   candidate.ResourceID,
				ResourceName: candidate.ResourceName,
				ResourceType: candidate.ResourceType,
				Provider:     candidate.Provider,
				Region:       candidate.Region,
				Endpoint:     probe.Endpoint,
				Reachable:    true,
				StatusCode:   probe.StatusCode,
				Frameworks:   []string{"OWASP WSTG-CRYP Transport Layer Protection", "OWASP A02 Cryptographic Failures"},
				Threats:      []string{"cleartext transport", "TLS downgrade", "session interception"},
				Evidence:     uniqueNonEmptyStrings(append(baseEvidence, probe.TLSIssues...)),
				Questions:    buildSecurityQuestions("tls-posture", candidate.ResourceName, probe.Endpoint, probe.Authenticated),
			})
		}

		if len(probe.MethodIssues) > 0 {
			findings = append(findings, securityFinding{
				ID:           buildDeepResearchFindingID("security-risky-methods", candidate.ResourceID+"|"+probe.Endpoint),
				Severity:     ternaryString(probe.RequiresAuth || probe.Authenticated, "medium", "high"),
				Category:     "risky-methods",
				Title:        fmt.Sprintf("%s advertises risky HTTP methods", resourceLabel),
				Summary:      "The endpoint advertises write-capable or tunneling HTTP methods during a read-only OPTIONS probe. These methods should be intentional, authenticated, and constrained to specific routes.",
				Confidence:   "medium",
				BlastRadius:  fmt.Sprintf("Routes on %s that share the same method policy.", resourceLabel),
				ResourceID:   candidate.ResourceID,
				ResourceName: candidate.ResourceName,
				ResourceType: candidate.ResourceType,
				Provider:     candidate.Provider,
				Region:       candidate.Region,
				Endpoint:     probe.Endpoint,
				Reachable:    true,
				StatusCode:   probe.StatusCode,
				RequiresAuth: probe.RequiresAuth,
				Frameworks:   []string{"OWASP WSTG-CONF Web Server Configuration", "OWASP A05 Security Misconfiguration"},
				Threats:      []string{"unexpected write method", "verb tampering", "route mutation"},
				Evidence:     uniqueNonEmptyStrings(append(baseEvidence, probe.MethodIssues...)),
				Questions:    buildSecurityQuestions("risky-methods", candidate.ResourceName, probe.Endpoint, probe.Authenticated),
			})
		}

		if len(probe.HeaderTrustIssues) > 0 {
			findings = append(findings, securityFinding{
				ID:           buildDeepResearchFindingID("security-header-trust", candidate.ResourceID+"|"+probe.Endpoint),
				Severity:     "high",
				Category:     "header-trust",
				Title:        fmt.Sprintf("%s trusts spoofable host or proxy headers", resourceLabel),
				Summary:      "Host, forwarded-host, or proxy rewrite probes changed routing, redirects, or the auth boundary. This can enable host-header injection, password-reset poisoning, cache poisoning, direct-origin confusion, or edge/auth bypass depending on where the header is trusted.",
				Confidence:   "high",
				BlastRadius:  fmt.Sprintf("Routing, redirects, authentication decisions, and cache behavior on %s and any upstream proxy that trusts it.", resourceLabel),
				ResourceID:   candidate.ResourceID,
				ResourceName: candidate.ResourceName,
				ResourceType: candidate.ResourceType,
				Provider:     candidate.Provider,
				Region:       candidate.Region,
				Endpoint:     probe.Endpoint,
				Reachable:    true,
				StatusCode:   probe.StatusCode,
				RequiresAuth: probe.RequiresAuth,
				Frameworks:   []string{"OWASP WSTG-INPV-17 Host Header Injection", "OWASP A05 Security Misconfiguration"},
				Threats:      []string{"host header injection", "proxy header trust", "web cache poisoning", "auth bypass"},
				Evidence:     uniqueNonEmptyStrings(append(baseEvidence, probe.HeaderTrustIssues...)),
				Questions:    buildSecurityQuestions("header-trust", candidate.ResourceName, probe.Endpoint, probe.Authenticated),
			})
		}

		if len(probe.CacheIssues) > 0 {
			severity := "medium"
			if probe.RequiresAuth || probe.Authenticated {
				severity = "high"
			}
			findings = append(findings, securityFinding{
				ID:           buildDeepResearchFindingID("security-cache-policy", candidate.ResourceID+"|"+probe.Endpoint),
				Severity:     severity,
				Category:     "cache-policy",
				Title:        fmt.Sprintf("%s has unsafe cache policy for session-like content", resourceLabel),
				Summary:      "Cookie, auth, or session-like responses were served without cache directives that clearly prevent shared-cache retention. That can turn otherwise valid responses into replayable or cross-user exposure when CDNs, proxies, browsers, or service workers cache the content.",
				Confidence:   "medium",
				BlastRadius:  fmt.Sprintf("Session-bearing responses, browser caches, CDN/proxy layers, and API clients that consume %s.", resourceLabel),
				ResourceID:   candidate.ResourceID,
				ResourceName: candidate.ResourceName,
				ResourceType: candidate.ResourceType,
				Provider:     candidate.Provider,
				Region:       candidate.Region,
				Endpoint:     probe.Endpoint,
				Reachable:    true,
				StatusCode:   probe.StatusCode,
				RequiresAuth: probe.RequiresAuth,
				Frameworks:   []string{"OWASP WSTG-CONF Cache Control", "OWASP A05 Security Misconfiguration"},
				Threats:      []string{"shared cache data exposure", "session response replay", "browser cache leakage"},
				Evidence:     uniqueNonEmptyStrings(append(baseEvidence, probe.CacheIssues...)),
				Questions:    buildSecurityQuestions("cache-policy", candidate.ResourceName, probe.Endpoint, probe.Authenticated),
			})
		}

		if len(probe.RateLimitIssues) > 0 {
			severity := "medium"
			if strings.Contains(strings.ToLower(strings.Join(append(probe.InterestingPaths, probe.Endpoint), " ")), "graphql") {
				severity = "high"
			}
			findings = append(findings, securityFinding{
				ID:           buildDeepResearchFindingID("security-api-resource-controls", candidate.ResourceID+"|"+probe.Endpoint),
				Severity:     severity,
				Category:     "api-resource-controls",
				Title:        fmt.Sprintf("%s lacks visible API abuse controls", resourceLabel),
				Summary:      "The endpoint looks API-like and answered a bounded probe without visible rate-limit or backoff signals. This is not proof that rate limiting is absent, but it is a strong validation target for brute force, scraping, expensive GraphQL/query patterns, third-party API cost amplification, and denial-of-wallet/resource-consumption risks.",
				Confidence:   "medium",
				BlastRadius:  fmt.Sprintf("API routes, expensive handlers, backing services, third-party quotas, and cloud spend reachable through %s.", resourceLabel),
				ResourceID:   candidate.ResourceID,
				ResourceName: candidate.ResourceName,
				ResourceType: candidate.ResourceType,
				Provider:     candidate.Provider,
				Region:       candidate.Region,
				Endpoint:     probe.Endpoint,
				Reachable:    true,
				StatusCode:   probe.StatusCode,
				RequiresAuth: probe.RequiresAuth,
				Frameworks:   []string{"OWASP API4:2023 Unrestricted Resource Consumption", "OWASP API Security Top 10"},
				Threats:      []string{"resource exhaustion", "credential stuffing", "scraping", "cloud cost amplification"},
				Evidence:     uniqueNonEmptyStrings(append(baseEvidence, probe.RateLimitIssues...)),
				Questions:    buildSecurityQuestions("api-resource-controls", candidate.ResourceName, probe.Endpoint, probe.Authenticated),
			})
		}

		sensitivePaths := filterSensitiveSecurityPaths(probe.InterestingPaths)
		if len(sensitivePaths) > 0 {
			severity := "high"
			if onlyLowSignalSecurityPaths(sensitivePaths) {
				severity = "medium"
			}
			findings = append(findings, securityFinding{
				ID:           buildDeepResearchFindingID("security-sensitive-paths", candidate.ResourceID+"|"+probe.Endpoint),
				Severity:     severity,
				Category:     "sensitive-path",
				Title:        fmt.Sprintf("%s exposes discovery or sensitive paths", resourceLabel),
				Summary:      "Forced-browsing probes found paths that commonly reveal admin surfaces, debug state, API schemas, metrics, configuration, or source metadata. Even read-only exposure can accelerate exploitation and auth bypass testing.",
				Confidence:   "high",
				BlastRadius:  fmt.Sprintf("Reconnaissance and forced-browsing surface under %s.", resourceLabel),
				ResourceID:   candidate.ResourceID,
				ResourceName: candidate.ResourceName,
				ResourceType: candidate.ResourceType,
				Provider:     candidate.Provider,
				Region:       candidate.Region,
				Endpoint:     probe.Endpoint,
				Reachable:    true,
				StatusCode:   probe.StatusCode,
				Frameworks:   []string{"OWASP WSTG-INFO Information Gathering", "OWASP WSTG-ATHZ-02 Authorization Bypass", "OWASP A01 Broken Access Control"},
				Threats:      []string{"forced browsing", "API schema exposure", "debug endpoint exposure", "information disclosure"},
				Evidence:     uniqueNonEmptyStrings(append(baseEvidence, sensitivePaths...)),
				Questions:    buildSecurityQuestions("sensitive-path", candidate.ResourceName, probe.Endpoint, probe.Authenticated),
			})
		}
	}
	return dedupeSecurityFindings(findings)
}

func filterSensitiveSecurityPaths(paths []string) []string {
	filtered := []string{}
	for _, entry := range paths {
		path := securityPathFromInterestingPath(entry)
		if path == "" {
			continue
		}
		if securityInterestingPathIsSensitive(path) {
			filtered = append(filtered, "Reachable path: "+entry)
		}
	}
	return uniqueNonEmptyStrings(filtered)
}

func securityPathFromInterestingPath(entry string) string {
	trimmed := strings.TrimSpace(entry)
	if trimmed == "" {
		return ""
	}
	if idx := strings.Index(trimmed, " "); idx > 0 {
		trimmed = strings.TrimSpace(trimmed[:idx])
	}
	return trimmed
}

func securityInterestingPathIsSensitive(path string) bool {
	lower := strings.ToLower(strings.TrimSpace(path))
	if lower == "" || lower == "/health" || lower == "/healthz" || lower == "/ready" || lower == "/live" {
		return false
	}
	for _, marker := range []string{"admin", "graphql", "openapi", "swagger", "api-docs", "docs", "well-known", "metrics", "debug", "server-status", ".env", ".git", "config", "actuator"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func onlyLowSignalSecurityPaths(paths []string) bool {
	if len(paths) == 0 {
		return true
	}
	for _, entry := range paths {
		lower := strings.ToLower(entry)
		if strings.Contains(lower, ".env") || strings.Contains(lower, ".git") || strings.Contains(lower, "actuator/env") || strings.Contains(lower, "heapdump") || strings.Contains(lower, "debug") || strings.Contains(lower, "server-status") || strings.Contains(lower, "admin") || strings.Contains(lower, "metrics") {
			return false
		}
	}
	return true
}

func buildSecurityMisconfigurationFindings(resources []deepResearchResource, probes []securityProbeObservation) []securityFinding {
	findings := make([]securityFinding, 0, 8)
	probeByResource := map[string][]securityProbeObservation{}
	for _, probe := range probes {
		probeByResource[probe.ResourceID] = append(probeByResource[probe.ResourceID], probe)
	}
	for _, resource := range resources {
		publicAddress := deepResearchFirstNonEmptyAttr(resource.Attributes, "publicIp", "publicIpAddress", "natIP", "ipAddress")
		publiclyAccessible, hasPublicFlag := deepResearchBoolAttr(resource.Attributes, "publiclyAccessible")
		if !publiclyAccessible {
			publiclyAccessible, hasPublicFlag = deepResearchBoolAttr(resource.Attributes, "publicAccess")
		}
		if (isDeepResearchDatabaseType(resource.Type) || strings.Contains(strings.ToLower(resource.Type), "redis")) && (publicAddress != "" || (hasPublicFlag && publiclyAccessible)) {
			endpoint := ""
			if matches := probeByResource[resource.ID]; len(matches) > 0 {
				endpoint = matches[0].Endpoint
			}
			findings = append(findings, securityFinding{
				ID:           buildDeepResearchFindingID("security-public-database", resource.ID),
				Severity:     "critical",
				Category:     "misconfiguration",
				Title:        fmt.Sprintf("%s looks internet-reachable", deepResearchResourceLabel(resource)),
				Summary:      "A database-like resource appears to have public reachability. That is one of the fastest paths from recon to compromise.",
				ResourceID:   resource.ID,
				ResourceName: resource.Name,
				ResourceType: resource.Type,
				Provider:     inferDeepResearchProvider(resource),
				Region:       resource.Region,
				Endpoint:     endpoint,
				Evidence: uniqueNonEmptyStrings([]string{
					fmt.Sprintf("Public address: %s", publicAddress),
					ternaryString(hasPublicFlag && publiclyAccessible, "The resource is explicitly marked publicly accessible.", ""),
				}),
				Questions: buildSecurityQuestions("public-database", resource.Name, endpoint, false),
			})
		}
		if backupRetention, ok := deepResearchIntAttr(resource.Attributes, "backupRetentionPeriod"); ok && backupRetention == 0 {
			findings = append(findings, securityFinding{
				ID:           buildDeepResearchFindingID("security-backups-disabled", resource.ID),
				Severity:     "high",
				Category:     "misconfiguration",
				Title:        fmt.Sprintf("%s has no backup retention", deepResearchResourceLabel(resource)),
				Summary:      "Zero backup retention weakens the recovery path after a successful intrusion or operator mistake.",
				ResourceID:   resource.ID,
				ResourceName: resource.Name,
				ResourceType: resource.Type,
				Provider:     inferDeepResearchProvider(resource),
				Region:       resource.Region,
				Evidence:     []string{fmt.Sprintf("backupRetentionPeriod=%d", backupRetention)},
				Questions:    buildSecurityQuestions("backup-gap", resource.Name, "", false),
			})
		}
		if publicAddress != "" && isDeepResearchComputeLikeType(resource.Type) {
			findings = append(findings, securityFinding{
				ID:           buildDeepResearchFindingID("security-public-compute", resource.ID),
				Severity:     "high",
				Category:     "misconfiguration",
				Title:        fmt.Sprintf("%s has a public network edge", deepResearchResourceLabel(resource)),
				Summary:      "A compute or runtime resource advertises a public address. Even when intentional, it deserves a surface and control-plane review.",
				ResourceID:   resource.ID,
				ResourceName: resource.Name,
				ResourceType: resource.Type,
				Provider:     inferDeepResearchProvider(resource),
				Region:       resource.Region,
				Evidence:     []string{fmt.Sprintf("Public address: %s", publicAddress)},
				Questions:    buildSecurityQuestions("public-compute", resource.Name, publicAddress, false),
			})
		}
	}
	return findings
}

func buildSecuritySecretLeakFindings(resources []deepResearchResource) []securityFinding {
	findings := make([]securityFinding, 0, 6)
	for _, resource := range resources {
		leakedKeys := []string{}
		for key, value := range resource.Attributes {
			if !resourcedb.IsSecretKey(key) {
				continue
			}
			text := strings.TrimSpace(fmt.Sprint(value))
			if text == "" || text == "<nil>" || strings.EqualFold(text, "null") {
				continue
			}
			leakedKeys = append(leakedKeys, key)
		}
		if len(leakedKeys) == 0 {
			continue
		}
		sort.Strings(leakedKeys)
		findings = append(findings, securityFinding{
			ID:           buildDeepResearchFindingID("security-secret-leak", resource.ID),
			Severity:     "critical",
			Category:     "credential-leak",
			Title:        fmt.Sprintf("%s carries secret-like material in its snapshot", deepResearchResourceLabel(resource)),
			Summary:      "The infrastructure snapshot contains secret-like attribute keys for this resource. That is a direct credential hygiene risk and a possible replay path.",
			ResourceID:   resource.ID,
			ResourceName: resource.Name,
			ResourceType: resource.Type,
			Provider:     inferDeepResearchProvider(resource),
			Region:       resource.Region,
			Evidence:     []string{fmt.Sprintf("Sensitive attribute keys: %s", strings.Join(leakedKeys, ", "))},
			Questions:    buildSecurityQuestions("secret-leak", resource.Name, "", false),
		})
	}
	return findings
}

func buildSecurityIdentityFindings(resources []deepResearchResource) []securityFinding {
	findings := make([]securityFinding, 0, 6)
	for _, resource := range resources {
		publicAddress := deepResearchFirstNonEmptyAttr(resource.Attributes, "publicIp", "publicIpAddress", "natIP", "ipAddress")
		if publicAddress == "" {
			continue
		}
		if strings.TrimSpace(resource.IAMRole) != "" {
			findings = append(findings, securityFinding{
				ID:           buildDeepResearchFindingID("security-public-iam-role", resource.ID),
				Severity:     "high",
				Category:     "identity-pivot",
				Title:        fmt.Sprintf("%s has both public reachability and an attached IAM role", deepResearchResourceLabel(resource)),
				Summary:      "Public network reachability plus an attached cloud role is a classic post-exploitation pivot path from runtime compromise into the control plane.",
				ResourceID:   resource.ID,
				ResourceName: resource.Name,
				ResourceType: resource.Type,
				Provider:     inferDeepResearchProvider(resource),
				Region:       resource.Region,
				Evidence:     []string{fmt.Sprintf("IAM role: %s", resource.IAMRole), fmt.Sprintf("Public address: %s", publicAddress)},
				Questions:    buildSecurityQuestions("identity-pivot", resource.Name, publicAddress, false),
			})
		}
		if len(resource.CanInvokeResources) > 0 {
			findings = append(findings, securityFinding{
				ID:           buildDeepResearchFindingID("security-public-invoke-path", resource.ID),
				Severity:     "high",
				Category:     "identity-pivot",
				Title:        fmt.Sprintf("%s can invoke internal resources from a public edge", deepResearchResourceLabel(resource)),
				Summary:      "The snapshot indicates this public-facing resource can invoke other resources. That broadens the likely blast radius of a runtime foothold.",
				ResourceID:   resource.ID,
				ResourceName: resource.Name,
				ResourceType: resource.Type,
				Provider:     inferDeepResearchProvider(resource),
				Region:       resource.Region,
				Evidence: []string{
					fmt.Sprintf("Public address: %s", publicAddress),
					fmt.Sprintf("Can invoke %d resources", len(resource.CanInvokeResources)),
				},
				Questions: buildSecurityQuestions("invoke-pivot", resource.Name, publicAddress, false),
			})
		}
	}
	return findings
}

func buildSecurityAttackVectors(findings []securityFinding, probes []securityProbeObservation, authPack securityRuntimeAuthPack) []securityAttackVector {
	probeByEndpoint := map[string]securityProbeObservation{}
	for _, probe := range probes {
		key := strings.TrimSpace(probe.Endpoint)
		if key == "" {
			continue
		}
		probeByEndpoint[key] = probe
	}

	vectors := append([]securityAttackVector{}, buildSecurityAttackChainVectors(findings, probeByEndpoint, authPack)...)
	for _, finding := range findings {
		vector, ok := buildSecurityAttackVector(finding, probeByEndpoint[strings.TrimSpace(finding.Endpoint)], authPack)
		if !ok {
			continue
		}
		vectors = append(vectors, vector)
	}

	return enrichSecurityAttackVectors(sortAndCapSecurityAttackVectors(dedupeSecurityAttackVectors(vectors)))
}

func buildSecurityAttackChainVectors(findings []securityFinding, probeByEndpoint map[string]securityProbeObservation, authPack securityRuntimeAuthPack) []securityAttackVector {
	byResource := map[string][]securityFinding{}
	for _, finding := range findings {
		resourceID := strings.TrimSpace(finding.ResourceID)
		if resourceID == "" {
			continue
		}
		byResource[resourceID] = append(byResource[resourceID], finding)
	}

	vectors := []securityAttackVector{}
	for resourceID, group := range byResource {
		surface, hasSurface := pickBestSecurityFindingByCategory(group, "public-surface", "reachable-surface", "authenticated-surface")
		identity, hasIdentity := pickBestSecurityFindingByCategory(group, "identity-pivot")
		if !hasSurface || !hasIdentity {
			continue
		}

		probe := probeByEndpoint[strings.TrimSpace(surface.Endpoint)]
		severity := maxSecuritySeverity(surface.Severity, identity.Severity)
		if strings.EqualFold(strings.TrimSpace(surface.Category), "public-surface") {
			severity = maxSecuritySeverity(severity, "critical")
		}
		exploitability := "high"
		if surface.RequiresAuth && !strings.EqualFold(strings.TrimSpace(surface.Category), "public-surface") {
			exploitability = "medium"
		}
		confidence := "high"
		if !surface.Reachable && strings.TrimSpace(surface.Endpoint) == "" {
			confidence = "medium"
		}

		resourceLabel := coalesceSecurityName(surface.ResourceName, identity.ResourceName, resourceID, "this runtime")
		evidence := uniqueNonEmptyStrings(append(append([]string{}, surface.Evidence...), identity.Evidence...))
		if strings.TrimSpace(probe.Server) != "" {
			evidence = append(evidence, fmt.Sprintf("Observed server banner: %s", strings.TrimSpace(probe.Server)))
		}
		if strings.TrimSpace(probe.Banner) != "" {
			evidence = append(evidence, fmt.Sprintf("Observed service banner: %s", strings.TrimSpace(probe.Banner)))
		}
		if probe.TLSEnabled {
			evidence = append(evidence, fmt.Sprintf("Direct TLS handshake succeeded: %s", coalesceSecurityName(probe.TLSVersion, "TLS detected")))
		}

		vectors = append(vectors, securityAttackVector{
			ID:             buildDeepResearchFindingID("vector-public-runtime-pivot", resourceID),
			Severity:       severity,
			Title:          fmt.Sprintf("Initial access on %s followed by identity pivot", resourceLabel),
			Summary:        "The same workload exposes a live external surface and also carries downstream identity or invoke privileges. Treat initial access here as the first step in a wider control-plane pivot.",
			KillChainStage: "initial-access -> lateral-movement",
			Exploitability: exploitability,
			Confidence:     confidence,
			LikelyImpact:   "Privilege escalation from an internet-accessible runtime into cloud APIs, adjacent services, or sensitive data paths.",
			BlastRadius:    fmt.Sprintf("%s plus any cloud permissions or downstream services its runtime identity can reach.", resourceLabel),
			ResourceIDs:    []string{resourceID},
			EntryPoints:    uniqueNonEmptyStrings([]string{surface.Endpoint, identity.Endpoint}),
			Prerequisites: uniqueNonEmptyStrings([]string{
				ternaryString(surface.Endpoint != "", "Reachability to the externally exposed workload", "A confirmed path to the workload"),
				"A safe read-only method to inspect runtime identity, metadata, or invoke paths",
			}),
			Steps: []string{
				"Confirm the public or authenticated edge with the least invasive request that proves the surface exists.",
				"Inspect workload identity, metadata access, and any invoke paths exposed to the runtime using read-only validation only.",
				"Enumerate the exact cloud actions and downstream services reachable from that identity before attempting any deeper pivot.",
				"Contain public reachability and narrow role permissions before validating any post-exploitation assumption beyond read-only checks.",
			},
			ImmediateActions: []string{
				"Restrict the public edge to the intended front door or operator IP ranges immediately.",
				"Narrow or detach unused runtime roles and invoke permissions until the workload is reviewed.",
				"Review recent cloud audit logs for activity from this runtime identity or direct-origin traffic.",
			},
			ValidationChecks: []string{
				"Verify which routes on the workload respond anonymously versus with valid operator auth.",
				"Confirm whether metadata or workload identity endpoints are reachable from the same runtime context.",
				"List the highest-risk downstream resources reachable through the attached role or invoke path.",
			},
			DetectionSignals: []string{
				"Direct-origin hits or repeated route enumeration against the workload edge.",
				"Metadata-service or workload-identity access from unusual processes or time windows.",
				"STS, token exchange, or downstream invoke activity originating from the compromised runtime.",
			},
			RequiresAuth: surface.RequiresAuth,
			AuthKinds:    securityAuthKinds(authPack),
			Evidence:     evidence,
		})
	}

	return vectors
}

func buildSecurityAttackVector(finding securityFinding, probe securityProbeObservation, authPack securityRuntimeAuthPack) (securityAttackVector, bool) {
	resourceID := strings.TrimSpace(finding.ResourceID)
	resourceIDs := []string{}
	if resourceID != "" {
		resourceIDs = append(resourceIDs, resourceID)
	}
	entryPoints := uniqueNonEmptyStrings([]string{finding.Endpoint})
	evidence := uniqueNonEmptyStrings(append([]string{}, finding.Evidence...))
	authKinds := securityAuthKinds(authPack)
	if finding.RequiresAuth && len(authKinds) == 0 {
		authKinds = []string{"application-auth"}
	}
	resourceLabel := coalesceSecurityName(finding.ResourceName, finding.ResourceID, "this target")
	blastRadius := inferSecurityBlastRadius(finding)
	switch finding.Category {
	case "public-surface", "reachable-surface", "authenticated-surface":
		if scheme := coalesceSecurityName(strings.TrimSpace(probe.Scheme), securityEndpointScheme(finding.Endpoint)); scheme == "tcp" || scheme == "tls" {
			return securityAttackVector{
				ID:             buildDeepResearchFindingID("vector-socket-recon", finding.ID),
				Severity:       finding.Severity,
				Title:          fmt.Sprintf("Live socket exposure path for %s", resourceLabel),
				Summary:        "Treat this as a controlled network-exposure plan: confirm the open port, fingerprint the protocol with read-only handshakes only, and decide whether the service should ever be reachable from the internet.",
				KillChainStage: "initial-access",
				Exploitability: ternaryString(finding.RequiresAuth, "medium", "high"),
				Confidence:     ternaryString(probe.Reachable, "high", "medium"),
				LikelyImpact:   "Protocol fingerprinting, exposed control-plane or data-plane access, and confirmation of internet-reachable admin or data services.",
				BlastRadius:    blastRadius,
				ResourceIDs:    resourceIDs,
				EntryPoints:    entryPoints,
				Prerequisites: uniqueNonEmptyStrings([]string{
					"Network reachability to the target host and port",
					ternaryString(probe.TLSEnabled, "A read-only TLS handshake to validate certificate and protocol posture", "A single safe TCP connection with no auth or state-changing commands"),
				}),
				Steps: []string{
					fmt.Sprintf("Confirm that %s accepts a TCP connection from the expected test vantage point.", finding.Endpoint),
					"Fingerprint the exposed protocol with the least invasive read-only handshake possible and stop before any state-changing command.",
					"Record banner data, TLS metadata, and port ownership to determine whether the service is admin-only, data-plane, or public product traffic.",
					"Validate whether the port should be private-only, VPN-gated, or fronted by a dedicated edge instead of being directly internet reachable.",
				},
				ImmediateActions: []string{
					"Restrict the port to approved CIDRs, private networks, or bastion paths immediately if public reachability is not intentional.",
					"Disable direct exposure for data stores, caches, or control-plane services and move access behind private networking.",
					"Require TLS, client auth, or protocol-native authentication where the service must remain reachable.",
				},
				ValidationChecks: []string{
					"Confirm which source networks can complete the TCP or TLS handshake.",
					"Verify that only the intended protocol responds and that banner leakage is minimized.",
					"Re-test after remediation to confirm the port is no longer publicly reachable or is strongly gated.",
				},
				DetectionSignals: []string{
					"Repeated TCP handshakes or TLS hellos from unfamiliar source IPs.",
					"Connection bursts to admin, database, cache, or orchestration ports.",
					"Protocol negotiation or login attempts outside expected operator maintenance windows.",
				},
				RequiresAuth: finding.RequiresAuth,
				AuthKinds:    authKinds,
				Evidence:     evidence,
			}, true
		}
		steps := []string{
			fmt.Sprintf("Fingerprint %s with safe GET, HEAD, and OPTIONS requests.", finding.Endpoint),
			"Enumerate health, version, OpenAPI, Swagger, docs, and well-known auth paths without mutating state.",
			"Compare unauthenticated and authenticated responses to map where the auth boundary actually changes behavior.",
			"Identify direct-origin reachability, redirects, CORS policy, and response banners that widen the recon surface.",
		}
		if len(probe.InterestingPaths) > 0 {
			steps = append(steps, fmt.Sprintf("Review already reachable supporting paths: %s.", strings.Join(probe.InterestingPaths, ", ")))
		}
		exploitability := "high"
		if finding.RequiresAuth && !strings.EqualFold(strings.TrimSpace(finding.Category), "public-surface") {
			exploitability = "medium"
		}
		confidence := "medium"
		if finding.Reachable || probe.Reachable || len(probe.InterestingPaths) > 0 {
			confidence = "high"
		}
		return securityAttackVector{
			ID:             buildDeepResearchFindingID("vector-api-recon", finding.ID),
			Severity:       finding.Severity,
			Title:          fmt.Sprintf("Live surface exploitation path for %s", resourceLabel),
			Summary:        "Treat this as a controlled initial-access plan: enumerate the live edge, measure the real auth boundary, and identify the narrowest path that could yield exposed data or operator-only behavior.",
			KillChainStage: "initial-access",
			Exploitability: exploitability,
			Confidence:     confidence,
			LikelyImpact:   "Endpoint discovery, auth-boundary weaknesses, hidden route inventory, and low-friction data exposure checks.",
			BlastRadius:    blastRadius,
			ResourceIDs:    resourceIDs,
			EntryPoints:    entryPoints,
			Prerequisites:  uniqueNonEmptyStrings([]string{"Network reachability to the target endpoint", ternaryString(finding.RequiresAuth, "Credentials or headers that are valid for this surface", "")}),
			Steps:          steps,
			ImmediateActions: uniqueNonEmptyStrings([]string{
				"Restrict direct-origin reachability to the intended front door or operator network ranges.",
				"Temporarily gate documentation, schema, version, and well-known auth paths that do not need to be public.",
				ternaryString(finding.RequiresAuth, "Reduce token scope and review which identities are allowed to reach this surface.", "Move every non-public route behind explicit auth or deny rules."),
			}),
			ValidationChecks: []string{
				"Re-run anonymous and authenticated reads against the same route set and diff the behavior.",
				"Confirm whether only the intended methods, origins, and redirects remain reachable.",
				"Validate whether direct-origin traffic bypasses edge controls, WAF, or access policies.",
			},
			DetectionSignals: []string{
				"Bursts of HEAD/OPTIONS/GET requests to health, docs, version, or schema paths.",
				"Unexpected 200 responses on routes that should challenge with 401 or 403.",
				"Direct-origin traffic that bypasses the intended edge, CDN, or gateway.",
			},
			RequiresAuth: finding.RequiresAuth,
			AuthKinds:    authKinds,
			Evidence:     evidence,
		}, true
	case "misconfiguration":
		if securityFindingIsBackupGap(finding) {
			return securityAttackVector{}, false
		}
		if securityFindingIsDatabaseExposure(finding) {
			return securityAttackVector{
				ID:             buildDeepResearchFindingID("vector-public-datastore", finding.ID),
				Severity:       maxSecuritySeverity(finding.Severity, "critical"),
				Title:          fmt.Sprintf("Direct data-store exposure against %s", resourceLabel),
				Summary:        "This looks like a database-like service with public reachability. Move from banner confirmation to public-ingress containment before any deeper authentication testing.",
				KillChainStage: "initial-access",
				Exploitability: "critical",
				Confidence:     ternaryString(strings.TrimSpace(finding.Endpoint) != "", "high", "medium"),
				LikelyImpact:   "Data theft, destructive writes, replica abuse, or credential harvesting from an internet-exposed data service.",
				BlastRadius:    blastRadius,
				ResourceIDs:    resourceIDs,
				EntryPoints:    entryPoints,
				Prerequisites: []string{
					"Network path to the exposed service",
					"A non-destructive banner or auth check that confirms the database type",
				},
				Steps: []string{
					"Confirm the exposed port, protocol, TLS posture, and whether the service challenges for auth.",
					"Capture only version or auth banners; do not run write queries or schema-changing commands during validation.",
					"Determine whether the service is directly internet-reachable or exposed through a misconfigured edge path.",
					"Contain public ingress before attempting any deeper authentication or enumeration sequence.",
				},
				ImmediateActions: []string{
					"Remove public ingress and disable public accessibility immediately.",
					"Rotate credentials and review replicas, read-only endpoints, and dependent applications for exposure.",
					"Audit recent network and auth logs for signs of internet-origin probing or access.",
				},
				ValidationChecks: []string{
					"Confirm direct public connections fail after the ingress change.",
					"Verify the application still reaches the store through approved private paths only.",
					"Review whether any public DNS, load balancer, or security group path still resolves to the service.",
				},
				DetectionSignals: []string{
					"Unexpected connection attempts from internet IPs or generic scanners.",
					"Repeated auth failures or version/banner grabs on the exposed database port.",
					"Public network or load-balancer changes that reopen ingress after containment.",
				},
				Evidence: evidence,
			}, true
		}
		if securityFindingIsPublicCompute(finding) {
			return securityAttackVector{
				ID:             buildDeepResearchFindingID("vector-public-runtime", finding.ID),
				Severity:       finding.Severity,
				Title:          fmt.Sprintf("Public runtime foothold against %s", resourceLabel),
				Summary:        "Treat the public host or workload as an initial foothold risk. Confirm the exposed services, then inspect identity, metadata, and outbound trust edges without mutating state.",
				KillChainStage: "initial-access",
				Exploitability: "high",
				Confidence:     ternaryString(strings.TrimSpace(finding.Endpoint) != "" || len(evidence) > 0, "medium", "low"),
				LikelyImpact:   "Initial access, metadata harvest, local secret discovery, and possible control-plane pivot from a public runtime.",
				BlastRadius:    blastRadius,
				ResourceIDs:    resourceIDs,
				EntryPoints:    entryPoints,
				Prerequisites: []string{
					"Internet reachability to the public host or runtime edge",
					"A safe read-only path to identify exposed protocols and metadata protections",
				},
				Steps: []string{
					"Enumerate only safe network banners and HTTP paths exposed by the runtime.",
					"Confirm whether metadata, admin, debug, or sidecar services are reachable from the same host.",
					"Map local secrets, workload identity, and outbound trust edges without changing system state.",
					"Reduce or remove direct public ingress before any validation deeper than banner and auth checks.",
				},
				ImmediateActions: []string{
					"Narrow inbound rules to the intended front doors or operator IPs.",
					"Review metadata protections and strip unused instance or workload roles.",
					"Check for admin, debug, or sidecar services that should never be internet-accessible.",
				},
				ValidationChecks: []string{
					"Verify the host is not directly reachable except through approved edges.",
					"Confirm metadata and admin ports are blocked or strongly authenticated.",
					"Inspect outbound API calls from the runtime for unnecessary control-plane access.",
				},
				DetectionSignals: []string{
					"Direct connections to host IPs instead of the intended edge DNS.",
					"Requests to metadata, debug, or admin routes from unusual sources.",
					"New outbound calls from the host to cloud control-plane APIs.",
				},
				Evidence: evidence,
			}, true
		}
		steps := []string{
			"Validate the public edge with the least invasive probe that still confirms exposure.",
			"Confirm whether TLS, auth, firewalling, and source restrictions are actually in place.",
			"Check whether the exposed resource can be reached directly or only through an intended front door.",
			"Review rollback and containment options before any deeper validation.",
		}
		return securityAttackVector{
			ID:             buildDeepResearchFindingID("vector-misconfig", finding.ID),
			Severity:       finding.Severity,
			Title:          fmt.Sprintf("Misconfiguration validation for %s", resourceLabel),
			Summary:        "Treat the finding as a manual exploitation plan: confirm the exposure, verify the missing control, and prepare containment before any risky validation.",
			KillChainStage: "initial-access",
			Exploitability: ternaryString(strings.TrimSpace(finding.Endpoint) != "", "high", "medium"),
			Confidence:     ternaryString(len(evidence) > 0, "medium", "low"),
			LikelyImpact:   "Confirms whether a public or weakly protected control can be abused by an external actor.",
			BlastRadius:    blastRadius,
			ResourceIDs:    resourceIDs,
			EntryPoints:    entryPoints,
			Prerequisites:  uniqueNonEmptyStrings([]string{"Operator approval for exposure validation", ternaryString(finding.Endpoint != "", "Reachability to the suspected public endpoint", "")}),
			Steps:          steps,
			ImmediateActions: []string{
				"Close the exposure with the smallest safe network, identity, or policy change first.",
				"Record rollback steps before changing anything beyond a read-only validation.",
			},
			ValidationChecks: []string{
				"Confirm the missing control exists or fails exactly where expected.",
				"Verify the intended front door still works after containment.",
			},
			DetectionSignals: []string{
				"Configuration changes that reopen public access after containment.",
				"Unexpected direct-origin traffic against the exposed control path.",
			},
			Evidence: evidence,
		}, true
	case "credential-leak":
		return securityAttackVector{
			ID:             buildDeepResearchFindingID("vector-credential-replay", finding.ID),
			Severity:       finding.Severity,
			Title:          fmt.Sprintf("Credential replay chain for %s", resourceLabel),
			Summary:        "Treat leaked material as compromised. Validate scope with read-only calls, enumerate where it works, and prepare rotation and revocation immediately.",
			KillChainStage: "credential-access",
			Exploitability: "critical",
			Confidence:     "high",
			LikelyImpact:   "Direct access to cloud control plane, runtime data, or CI/CD systems depending on the leaked credential scope.",
			BlastRadius:    blastRadius,
			ResourceIDs:    resourceIDs,
			Prerequisites:  []string{"A safe read-only validation path", "A revocation and rotation plan before replay testing"},
			Steps: []string{
				"Map each leaked secret-like key to the service or trust boundary it likely controls.",
				"Use a single read-only call to confirm whether the credential is live and what identity it resolves to.",
				"Enumerate directly reachable services and blast radius before any further use.",
				"Rotate, revoke, and invalidate all matching credentials once scope is confirmed.",
			},
			ImmediateActions: []string{
				"Rotate and revoke the leaked credential material before broader investigation widens the blast radius.",
				"Search CI, secrets stores, local configs, and snapshots for the same material or reused variants.",
				"Review audit logs for successful or failed use of the credential from unexpected locations.",
			},
			ValidationChecks: []string{
				"Use exactly one read-only identity or metadata call to prove whether the credential is still live.",
				"Enumerate the minimum set of services and permissions reachable with the leaked material.",
				"Verify the old credential fails after rotation and revocation complete.",
			},
			DetectionSignals: []string{
				"New API calls or token use from unknown IPs, regions, or automation identities.",
				"STS or identity-resolution requests for credentials that should be dormant.",
				"Follow-on privilege use across CI/CD, runtime, or control-plane services after the first replay.",
			},
			Evidence: evidence,
		}, true
	case "identity-pivot":
		return securityAttackVector{
			ID:             buildDeepResearchFindingID("vector-identity-pivot", finding.ID),
			Severity:       finding.Severity,
			Title:          fmt.Sprintf("Public-edge privilege pivot through %s", resourceLabel),
			Summary:        "Assume an attacker lands on the public workload, then validate what cloud privileges and internal invocation paths become reachable from there.",
			KillChainStage: "lateral-movement",
			Exploitability: ternaryString(strings.TrimSpace(finding.Endpoint) != "", "high", "medium"),
			Confidence:     "high",
			LikelyImpact:   "Lateral movement from public runtime into the cloud control plane or adjacent services.",
			BlastRadius:    blastRadius,
			ResourceIDs:    resourceIDs,
			EntryPoints:    entryPoints,
			Prerequisites:  []string{"Runtime inventory for the public workload", "Role and invoke-path inventory"},
			Steps: []string{
				"Fingerprint the public workload and confirm what runtime surface is actually exposed.",
				"Inspect metadata, workload identity, and attached role behavior using only safe validation.",
				"Enumerate which downstream services the role or invoke path can reach.",
				"Contain the role and network path before testing any deeper pivot assumptions.",
			},
			ImmediateActions: []string{
				"Reduce the attached role to the minimum permissions needed for production traffic.",
				"Remove unused invoke permissions and review trust relationships tied to the runtime.",
				"Inspect audit logs for recent role use, token exchange, or unexpected downstream calls.",
			},
			ValidationChecks: []string{
				"Confirm exactly which cloud actions the runtime identity can still perform.",
				"Verify downstream services reject requests once unused invoke edges are removed.",
				"Check whether metadata protections block unauthenticated token retrieval paths.",
			},
			DetectionSignals: []string{
				"STS, metadata, or token exchange requests from the public runtime.",
				"Unexpected calls from the workload into control-plane APIs or adjacent services.",
				"Privilege or invoke-path changes that silently re-expand the runtime blast radius.",
			},
			Evidence: evidence,
		}, true
	case "agentic-risk", "mcp-tooling", "agent-supply-chain":
		stage := "execution"
		title := fmt.Sprintf("Agentic misuse path through %s", resourceLabel)
		summary := "Validate how untrusted input, agent goals, tool selection, and privileged actions interact before the agent can turn context into side effects."
		likelyImpact := "Prompt injection or supply-chain manipulation can steer the agent into data exposure, tool misuse, code execution, or privileged non-human identity abuse."
		prerequisites := []string{
			"An input path that can influence the agent or its retrieved context",
			"A tool, skill, MCP server, model, or downstream API the agent can invoke",
		}
		steps := []string{
			"Anchor the original user goal and list which inputs are instructions versus untrusted content.",
			"Inventory every tool, MCP server, skill, model, file path, credential, and downstream API reachable during this run.",
			"Run a bounded prompt-injection or tool-poisoning simulation that asks for an out-of-scope read, write, shell, deploy, or data-export action.",
			"Confirm the policy layer outside the model blocks or approval-gates the action and records the decision.",
		}
		actions := []string{
			"Disable unnecessary tools and narrow file, network, credential, and cloud permissions immediately.",
			"Require human approval for write, deploy, identity, shell, data-export, or destructive actions.",
			"Log tool calls with goal, parameters, actor identity, data touched, and policy decision.",
		}
		validation := []string{
			"Verify malicious instructions in web, document, issue, email, or RAG content cannot change the agent goal.",
			"Confirm tool descriptors and schemas are pinned or signed and alert on unexpected changes.",
			"Prove the same out-of-scope action fails both with and without valid user credentials.",
		}
		detection := []string{
			"Unexpected goal drift, new tool sequences, or tool parameters unrelated to the original request.",
			"Agent access to secrets, source code, shell, identity APIs, or export endpoints outside normal baselines.",
			"New or changed MCP servers, skills, plugins, models, or tool schemas before sensitive actions.",
		}

		switch finding.Category {
		case "mcp-tooling":
			stage = "initial-access -> tool-pivot"
			title = fmt.Sprintf("Tool poisoning or cross-agent trust path through %s", resourceLabel)
			summary = "Treat MCP servers, tool schemas, and peer agents as active trust boundaries: a changed descriptor or remote instruction can steer the agent into unsafe tool calls."
			prerequisites = append(prerequisites, "A tool descriptor, MCP server, or peer-agent message that can influence tool selection")
			steps[2] = "Run a bounded tool-poisoning or peer-agent deviation simulation that requests a sensitive action outside the original task."
		case "agent-supply-chain":
			stage = "supply-chain -> execution"
			title = fmt.Sprintf("Agent dependency integrity path through %s", resourceLabel)
			summary = "Treat skills, plugins, models, MCP servers, and images as privileged dependencies whose executable behavior can diverge from declared purpose."
			prerequisites = []string{
				"A third-party or unpinned component loaded by the agent runtime",
				"Agent access to credentials, files, shell, network, or downstream APIs inherited by that component",
			}
			steps[1] = "Compare each installed component's manifest, natural-language instructions, and executable code for behavioral drift."
			steps[2] = "Run a bounded behavioral-integrity check that looks for file reads, credential access, network egress, shell execution, and tool chaining not declared by the component."
			actions[0] = "Freeze or disable unpinned and unreviewed agent dependencies until provenance and behavior are verified."
			validation[1] = "Confirm component versions are pinned by digest or commit and installed only from approved sources."
		}

		return securityAttackVector{
			ID:               buildDeepResearchFindingID("vector-agentic-risk", finding.ID),
			Severity:         finding.Severity,
			Title:            title,
			Summary:          summary,
			KillChainStage:   stage,
			Exploitability:   ternaryString(strings.TrimSpace(finding.Endpoint) != "" || finding.Reachable, "high", "medium"),
			Confidence:       ternaryString(len(evidence) > 0, "medium", "low"),
			LikelyImpact:     likelyImpact,
			BlastRadius:      blastRadius,
			ResourceIDs:      resourceIDs,
			EntryPoints:      entryPoints,
			Prerequisites:    uniqueNonEmptyStrings(prerequisites),
			Steps:            steps,
			ImmediateActions: actions,
			ValidationChecks: validation,
			DetectionSignals: detection,
			RequiresAuth:     finding.RequiresAuth,
			AuthKinds:        authKinds,
			Frameworks:       finding.Frameworks,
			Threats:          finding.Threats,
			Evidence:         evidence,
		}, true
	case "http-hardening", "cors-misconfiguration", "tls-posture", "sensitive-path", "risky-methods", "header-trust", "cache-policy", "api-resource-controls":
		stage := "initial-access -> reconnaissance"
		title := fmt.Sprintf("Curl-driven web exploitation path through %s", resourceLabel)
		summary := "Use read-only HTTP probes to validate whether normal web hardening gaps can become an attacker-friendly path for reconnaissance, auth-boundary testing, session abuse, or browser-based data exposure."
		impact := "Information disclosure, browser-side abuse, session weakening, forced browsing, or route mutation that can be chained with a public edge or weak authorization."
		prerequisites := []string{
			"Network reachability to the endpoint",
			"Permission to run read-only HEAD, GET, OPTIONS, and preflight-style requests",
		}
		steps := []string{
			"Capture baseline HEAD and GET responses with status, redirects, server, content type, cookies, and security headers.",
			"Run OPTIONS and CORS preflight probes from an untrusted Origin and record allowed methods, origins, credentials, and exposed headers.",
			"Forced-browse only low-impact discovery paths such as docs, OpenAPI, metrics, debug, admin, config, and well-known endpoints.",
			"Compare anonymous behavior with authenticated behavior if a scoped test credential is available.",
		}
		actions := []string{
			"Gate sensitive paths and docs behind explicit authentication or operator-only networks.",
			"Apply strict CORS, cookie, header, method, and TLS baselines at the edge and origin.",
			"Review access logs for route enumeration, OPTIONS bursts, and direct-origin probing.",
		}
		validation := []string{
			"Re-run the exact HEAD, GET, OPTIONS, and Origin probes and confirm risky behavior is gone.",
			"Verify sensitive paths return 401, 403, 404, or an approved public response anonymously.",
			"Confirm deployment tests fail when headers, CORS, cookies, TLS, or method policy regresses.",
		}
		detection := []string{
			"Spikes of HEAD, OPTIONS, and GET requests across docs, schema, admin, metrics, and debug paths.",
			"Requests with unusual Origin headers, Access-Control-Request-* headers, or write method probes.",
			"Direct-origin traffic that bypasses the expected gateway, CDN, WAF, or access policy.",
		}
		switch finding.Category {
		case "cors-misconfiguration":
			stage = "browser-abuse -> data-exposure"
			title = fmt.Sprintf("Browser CORS exfiltration path through %s", resourceLabel)
			summary = "Validate whether an attacker-controlled origin can make the browser read sensitive API responses or use credentialed context across origins."
			impact = "Authenticated data exposure, token-assisted reads, or API response exfiltration through victim browser sessions."
			steps[1] = "Send preflight and simple requests with attacker-controlled Origin values and compare Access-Control-Allow-* behavior on sensitive and non-sensitive routes."
			actions[1] = "Replace wildcard or reflected origins with an explicit allowlist and disable credentials unless a route demonstrably needs them."
		case "sensitive-path":
			stage = "reconnaissance -> auth-boundary-testing"
			title = fmt.Sprintf("Forced-browsing discovery path through %s", resourceLabel)
			summary = "Validate which discovered docs, schema, metrics, debug, admin, config, or source metadata paths are reachable anonymously and whether they reveal exploitation instructions."
			impact = "Accelerated route discovery, auth-bypass targeting, debug data exposure, or leaked operational details."
			steps[2] = "Review each reachable discovery path for secrets, internal hostnames, API schemas, admin verbs, stack traces, version data, or debug state."
			actions[0] = "Block, remove, or authenticate exposed docs, debug, admin, metrics, config, and source metadata paths."
		case "tls-posture":
			stage = "network-position -> session-interception"
			title = fmt.Sprintf("Transport downgrade path through %s", resourceLabel)
			summary = "Validate whether cleartext or weak TLS behavior can expose sessions, tokens, API data, or downgrade opportunities."
			impact = "Interception or manipulation of traffic, cookies, tokens, and API responses on paths that should be protected by modern TLS."
			steps[0] = "Confirm HTTP-to-HTTPS redirect behavior, HSTS presence, negotiated TLS version, and whether cleartext paths remain usable."
			actions[1] = "Force HTTPS, enable HSTS, disable TLS 1.0/1.1, and keep only modern cipher/protocol policy at the edge."
		case "risky-methods":
			stage = "reconnaissance -> route-mutation"
			title = fmt.Sprintf("HTTP verb tampering path through %s", resourceLabel)
			summary = "Validate whether write-capable or tunneling methods are advertised or accepted where only read methods should exist."
			impact = "Unexpected mutation, upload, delete, or proxy/tunnel behavior on routes that operators assume are read-only."
			steps[1] = "Use OPTIONS and harmless method probes to confirm which verbs are advertised or accepted before and after authentication."
			actions[1] = "Deny unused HTTP methods at the edge and origin, then allow write methods only on routes that require explicit authorization."
		case "header-trust":
			stage = "initial-access -> edge-confusion"
			title = fmt.Sprintf("Host and proxy header trust path through %s", resourceLabel)
			summary = "Validate whether spoofable Host, forwarded-host, original-url, or rewrite headers can alter redirects, routing, cache keys, tenant selection, or auth decisions."
			impact = "Host-header injection, password-reset poisoning, cache poisoning, route confusion, tenant confusion, or auth bypass through misplaced proxy trust."
			steps[1] = "Replay hostile Host, X-Forwarded-Host, X-Original-URL, X-Rewrite-URL, and X-Forwarded-For probes and compare redirects, status codes, and auth challenges against baseline."
			actions[1] = "Normalize or strip proxy headers at the first trusted ingress and reject untrusted host values before application routing."
			validation[0] = "Re-run hostile host and proxy-header probes and confirm redirects, routing, and auth decisions match the safe baseline."
			detection[1] = "Requests with unexpected Host, X-Forwarded-Host, X-Original-URL, X-Rewrite-URL, or private-loopback forwarding values."
		case "cache-policy":
			stage = "browser-abuse -> data-exposure"
			title = fmt.Sprintf("Shared-cache exposure path through %s", resourceLabel)
			summary = "Validate whether cookie, auth, or tenant-specific responses can be retained and replayed by browsers, CDNs, reverse proxies, or service workers."
			impact = "Cross-user data exposure, stale authenticated response replay, session-state leakage, or CDN/proxy cache poisoning."
			steps[0] = "Capture baseline HEAD and GET responses with Cache-Control, Vary, Set-Cookie, Authorization-dependent behavior, and CDN cache diagnostics where available."
			actions[1] = "Apply no-store or private cache policy to session-like responses and purge shared caches that may have retained sensitive content."
			validation[0] = "Confirm cookie/auth-like responses include approved no-store, private, or Vary behavior at both edge and origin."
			detection[0] = "Cache hits, CDN diagnostics, or service-worker reads for routes that carry cookies, tokens, tenant state, or user data."
		case "api-resource-controls":
			stage = "resource-abuse -> denial-of-wallet"
			title = fmt.Sprintf("API resource-consumption path through %s", resourceLabel)
			summary = "Validate whether cheap repeated requests, expensive queries, brute-force attempts, scraping, uploads, or third-party-cost paths are constrained before expensive work begins."
			impact = "Resource exhaustion, denial of service, denial of wallet, credential stuffing, data scraping, or downstream quota burn."
			prerequisites = append(prerequisites, "Permission to run bounded low-volume quota, payload-size, and query-complexity checks")
			steps[1] = "Run bounded rate, burst, payload-size, pagination, query-depth, and operation-count checks against API-like routes without destructive inputs."
			actions[1] = "Enforce server-side throttles, quotas, query-cost limits, payload limits, and consistent backoff before downstream work starts."
			validation[0] = "Confirm throttles, query limits, upload limits, and backoff responses activate under bounded abuse tests while normal clients remain healthy."
			detection[0] = "Repeated expensive API calls, high-cardinality client identities, query-depth spikes, upload bursts, and quota/backoff events."
		}
		return securityAttackVector{
			ID:               buildDeepResearchFindingID("vector-web-posture", finding.ID),
			Severity:         finding.Severity,
			Title:            title,
			Summary:          summary,
			KillChainStage:   stage,
			Exploitability:   ternaryString(strings.TrimSpace(finding.Endpoint) != "" || finding.Reachable, "high", "medium"),
			Confidence:       ternaryString(len(evidence) > 0, "high", "medium"),
			LikelyImpact:     impact,
			BlastRadius:      blastRadius,
			ResourceIDs:      resourceIDs,
			EntryPoints:      entryPoints,
			Prerequisites:    uniqueNonEmptyStrings(prerequisites),
			Steps:            steps,
			ImmediateActions: actions,
			ValidationChecks: validation,
			DetectionSignals: detection,
			RequiresAuth:     finding.RequiresAuth,
			AuthKinds:        authKinds,
			Frameworks:       finding.Frameworks,
			Threats:          finding.Threats,
			Evidence:         evidence,
		}, true
	default:
		return securityAttackVector{}, false
	}
}

func pickBestSecurityFindingByCategory(findings []securityFinding, categories ...string) (securityFinding, bool) {
	allowed := map[string]struct{}{}
	for _, category := range categories {
		allowed[strings.ToLower(strings.TrimSpace(category))] = struct{}{}
	}

	best := securityFinding{}
	matched := false
	for _, finding := range findings {
		if _, ok := allowed[strings.ToLower(strings.TrimSpace(finding.Category))]; !ok {
			continue
		}
		if !matched || securitySeverityRank(finding.Severity) > securitySeverityRank(best.Severity) {
			best = finding
			matched = true
			continue
		}
		if matched && securitySeverityRank(finding.Severity) == securitySeverityRank(best.Severity) && len(finding.Evidence) > len(best.Evidence) {
			best = finding
		}
	}
	return best, matched
}

func inferSecurityBlastRadius(finding securityFinding) string {
	resourceLabel := coalesceSecurityName(finding.ResourceName, finding.ResourceID, "the affected resource")
	resourceType := strings.ToLower(strings.TrimSpace(finding.ResourceType))
	switch {
	case strings.EqualFold(strings.TrimSpace(finding.Category), "credential-leak"):
		return "Any control-plane or data-plane scope reachable with the leaked credential material."
	case isDeepResearchDatabaseType(finding.ResourceType) || strings.Contains(resourceType, "redis"):
		return fmt.Sprintf("Data confidentiality, data integrity, and any applications or replicas that depend on %s.", resourceLabel)
	case isDeepResearchComputeLikeType(finding.ResourceType):
		return fmt.Sprintf("%s, its runtime secrets, attached identity, and downstream services it can reach.", resourceLabel)
	default:
		return fmt.Sprintf("%s and the directly connected services that trust it.", resourceLabel)
	}
}

func securityFindingIsBackupGap(finding securityFinding) bool {
	lowerTitle := strings.ToLower(strings.TrimSpace(finding.Title))
	lowerSummary := strings.ToLower(strings.TrimSpace(finding.Summary))
	if strings.Contains(lowerTitle, "backup") || strings.Contains(lowerSummary, "backup retention") {
		return true
	}
	for _, evidence := range finding.Evidence {
		if strings.Contains(strings.ToLower(strings.TrimSpace(evidence)), "backupretentionperiod=0") {
			return true
		}
	}
	return false
}

func securityFindingIsDatabaseExposure(finding securityFinding) bool {
	resourceType := strings.ToLower(strings.TrimSpace(finding.ResourceType))
	if isDeepResearchDatabaseType(finding.ResourceType) || strings.Contains(resourceType, "redis") {
		return true
	}
	lowerTitle := strings.ToLower(strings.TrimSpace(finding.Title))
	lowerSummary := strings.ToLower(strings.TrimSpace(finding.Summary))
	return strings.Contains(lowerTitle, "database") || strings.Contains(lowerSummary, "database-like resource")
}

func securityFindingIsPublicCompute(finding securityFinding) bool {
	if isDeepResearchComputeLikeType(finding.ResourceType) {
		return true
	}
	lowerTitle := strings.ToLower(strings.TrimSpace(finding.Title))
	lowerSummary := strings.ToLower(strings.TrimSpace(finding.Summary))
	return strings.Contains(lowerTitle, "public network edge") || strings.Contains(lowerSummary, "compute or runtime resource")
}

func dedupeSecurityAttackVectors(vectors []securityAttackVector) []securityAttackVector {
	seen := map[string]securityAttackVector{}
	for _, vector := range vectors {
		id := strings.TrimSpace(vector.ID)
		if id == "" {
			id = strings.TrimSpace(vector.Title + "|" + strings.Join(vector.ResourceIDs, ",") + "|" + strings.Join(vector.EntryPoints, ","))
		}
		existing, ok := seen[id]
		if !ok || securityAttackVectorScore(vector) > securityAttackVectorScore(existing) {
			vector.ID = id
			seen[id] = vector
		}
	}
	result := make([]securityAttackVector, 0, len(seen))
	for _, vector := range seen {
		result = append(result, vector)
	}
	return result
}

func sortAndCapSecurityAttackVectors(vectors []securityAttackVector) []securityAttackVector {
	sort.Slice(vectors, func(i, j int) bool {
		leftScore := securityAttackVectorScore(vectors[i])
		rightScore := securityAttackVectorScore(vectors[j])
		if leftScore != rightScore {
			return leftScore > rightScore
		}
		return vectors[i].Title < vectors[j].Title
	})
	if len(vectors) > maxSecurityAttackVectors {
		return vectors[:maxSecurityAttackVectors]
	}
	return vectors
}

func securityAttackVectorScore(vector securityAttackVector) int {
	return (securitySeverityRank(vector.Severity) * 100) + (securitySeverityRank(vector.Exploitability) * 10) + securitySeverityRank(vector.Confidence)
}

func maxSecuritySeverity(left string, right string) string {
	if securitySeverityRank(right) > securitySeverityRank(left) {
		return right
	}
	return left
}

func buildSecurityScanSummary(estate deepResearchEstateSnapshot, candidates []securitySurfaceCandidate, probes []securityProbeObservation, findings []securityFinding, vectors []securityAttackVector) securityScanSummary {
	summary := securityScanSummary{
		TotalResources:     len(estate.Resources),
		CandidateEndpoints: len(candidates),
		AttackVectorCount:  len(vectors),
		PrimaryFocus:       "internet exposure",
	}
	for _, probe := range probes {
		if probe.Reachable {
			summary.ReachableEndpoints++
		}
		if probe.RequiresAuth || probe.Authenticated {
			summary.AuthSignals++
		}
	}
	for _, finding := range findings {
		category := strings.ToLower(strings.TrimSpace(finding.Category))
		switch strings.ToLower(strings.TrimSpace(finding.Severity)) {
		case "critical":
			summary.CriticalFindings++
		case "high":
			summary.HighFindings++
		}
		if category == "credential-leak" {
			summary.CredentialRisks++
		}
		if category == "agentic-risk" {
			summary.AgenticRisks++
		}
		if category == "mcp-tooling" {
			summary.MCPRisks++
		}
		if category == "agent-supply-chain" {
			summary.SupplyChainRisks++
		}
		if category == "identity-pivot" || category == "invoke-pivot" {
			summary.PrivilegeRisks++
		}
		if category == "http-hardening" || category == "risky-methods" || category == "header-trust" || category == "cache-policy" || category == "api-resource-controls" {
			summary.WebPostureRisks++
		}
		if category == "sensitive-path" {
			summary.SensitivePathRisks++
		}
		if category == "cors-misconfiguration" {
			summary.CORSRisks++
		}
		if category == "tls-posture" {
			summary.TLSRisks++
		}
	}
	if summary.CredentialRisks > 0 {
		summary.PrimaryFocus = "credential exposure"
	} else if summary.SensitivePathRisks > 0 || summary.CORSRisks > 0 || summary.WebPostureRisks > 0 {
		summary.PrimaryFocus = "web attack surface"
	} else if summary.AgenticRisks > 0 || summary.MCPRisks > 0 {
		summary.PrimaryFocus = "agentic attack paths"
	} else if summary.CriticalFindings > 0 {
		summary.PrimaryFocus = "critical exploit paths"
	} else if summary.ReachableEndpoints > 0 {
		summary.PrimaryFocus = "reachable API surface"
	}
	return summary
}

func dedupeSecurityFindings(findings []securityFinding) []securityFinding {
	seen := map[string]securityFinding{}
	for _, finding := range findings {
		id := strings.TrimSpace(finding.ID)
		if id == "" {
			id = strings.TrimSpace(finding.Category + "|" + finding.ResourceID + "|" + finding.Endpoint + "|" + finding.Title)
		}
		existing, ok := seen[id]
		if !ok || securitySeverityRank(finding.Severity) > securitySeverityRank(existing.Severity) {
			finding.ID = id
			seen[id] = finding
		}
	}
	result := make([]securityFinding, 0, len(seen))
	for _, finding := range seen {
		result = append(result, finding)
	}
	return result
}

func sortAndCapSecurityFindings(findings []securityFinding) []securityFinding {
	sort.Slice(findings, func(i, j int) bool {
		leftPriority := securityPriorityRank(findings[i].Priority)
		rightPriority := securityPriorityRank(findings[j].Priority)
		if leftPriority != rightPriority {
			return leftPriority > rightPriority
		}
		leftRank := securitySeverityRank(findings[i].Severity)
		rightRank := securitySeverityRank(findings[j].Severity)
		if leftRank != rightRank {
			return leftRank > rightRank
		}
		leftHighSignal := 0
		if findings[i].Reachable {
			leftHighSignal++
		}
		if findings[i].Endpoint != "" {
			leftHighSignal++
		}
		rightHighSignal := 0
		if findings[j].Reachable {
			rightHighSignal++
		}
		if findings[j].Endpoint != "" {
			rightHighSignal++
		}
		if leftHighSignal != rightHighSignal {
			return leftHighSignal > rightHighSignal
		}
		return findings[i].Title < findings[j].Title
	})
	if len(findings) > 32 {
		return findings[:32]
	}
	return findings
}

func summarizeSecurityVectors(vectors []securityAttackVector, limit int) []string {
	lines := make([]string, 0, limit)
	for _, vector := range vectors {
		if len(lines) >= limit {
			break
		}
		lines = append(lines, fmt.Sprintf("%s (%s)", vector.Title, vector.Severity))
	}
	return lines
}

func summarizeSecurityRemediationTargets(findings []securityFinding, limit int) []string {
	lines := make([]string, 0, limit)
	for _, finding := range findings {
		if len(lines) >= limit {
			break
		}
		firstStep := ""
		if len(finding.Containment) > 0 {
			firstStep = finding.Containment[0]
		} else if len(finding.Remediation) > 0 {
			firstStep = finding.Remediation[0]
		}
		if strings.TrimSpace(firstStep) == "" {
			lines = append(lines, fmt.Sprintf("[%s][%s] %s", coalesceSecurityName(strings.ToUpper(strings.TrimSpace(finding.Priority)), "P?"), coalesceSecurityName(strings.TrimSpace(finding.Owner), "owner"), finding.Title))
			continue
		}
		lines = append(lines, fmt.Sprintf("[%s][%s] %s -> %s", coalesceSecurityName(strings.ToUpper(strings.TrimSpace(finding.Priority)), "P?"), coalesceSecurityName(strings.TrimSpace(finding.Owner), "owner"), finding.Title, firstStep))
	}
	return lines
}

func securityPriorityRank(priority string) int {
	switch strings.ToLower(strings.TrimSpace(priority)) {
	case "p0":
		return 4
	case "p1":
		return 3
	case "p2":
		return 2
	case "p3":
		return 1
	default:
		return 0
	}
}

func securitySeverityRank(severity string) int {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case "critical":
		return 4
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	default:
		return 0
	}
}

func securitySeverityForSurface(resourceType string, likelyPublic bool) string {
	if isDeepResearchDatabaseType(resourceType) {
		return "critical"
	}
	if likelyPublic && (isDeepResearchComputeLikeType(resourceType) || isDeepResearchNetworkType(resourceType)) {
		return "high"
	}
	if likelyPublic {
		return "high"
	}
	return "medium"
}

func isDeepResearchComputeLikeType(resourceType string) bool {
	lower := strings.ToLower(strings.TrimSpace(resourceType))
	keywords := []string{"vm", "instance", "server", "compute", "container", "service", "lambda", "function", "run", "app", "worker", "droplet", "ecs", "eks", "gke", "aks"}
	for _, keyword := range keywords {
		if strings.Contains(lower, keyword) {
			return true
		}
	}
	return false
}

func buildSecurityQuestions(category string, resourceName string, endpoint string, authProvided bool) []string {
	resourceLabel := coalesceSecurityName(resourceName, endpoint, "this target")
	switch category {
	case "public-surface", "reachable-surface", "authenticated-surface":
		if scheme := securityEndpointScheme(endpoint); scheme == "tcp" || scheme == "tls" {
			return []string{
				fmt.Sprintf("Which clients, CIDRs, or networks should ever be allowed to reach %s on this port?", resourceLabel),
				"Is this socket supposed to be internet reachable, or should it only exist on private networking or through a bastion path?",
				"What is the safest read-only handshake or banner check that proves the auth boundary without changing service state?",
			}
		}
		questions := []string{
			fmt.Sprintf("Which routes on %s should be public versus operator-only?", resourceLabel),
			"Does the current surface expose version, schema, or documentation endpoints that should be gated?",
			"What is the smallest set of safe validation requests to confirm the auth boundary?",
		}
		if authProvided {
			questions = append(questions, "Which low-risk read-only checks should we run with the supplied auth to measure privilege scope?")
		}
		return questions
	case "public-database", "backup-gap", "public-compute", "misconfiguration":
		return []string{
			fmt.Sprintf("Is %s intentionally exposed, or is this accidental reachability?", resourceLabel),
			"Which compensating controls actually sit in front of it today?",
			"What containment step closes the gap fastest without breaking production?",
		}
	case "secret-leak", "credential-leak":
		return []string{
			fmt.Sprintf("Where else could the leaked material attached to %s be replayed?", resourceLabel),
			"Which rotation and revocation steps must happen before deeper validation?",
			"What read-only call proves whether the credential is still live?",
		}
	case "identity-pivot", "invoke-pivot":
		return []string{
			fmt.Sprintf("If %s were compromised, which internal resources would be reachable next?", resourceLabel),
			"How much of that path is role-driven versus network-driven?",
			"Which privilege or invoke edge should be narrowed first?",
		}
	case "agentic-risk":
		return []string{
			fmt.Sprintf("Which untrusted inputs can influence %s before it selects tools or actions?", resourceLabel),
			"Which tools, files, credentials, and downstream APIs can the agent reach during one run?",
			"Which high-impact actions are enforced outside the model through policy, approval, or deny rules?",
		}
	case "mcp-tooling":
		return []string{
			fmt.Sprintf("Which MCP servers, tools, schemas, or peer agents does %s trust today?", resourceLabel),
			"Are tool descriptors, dynamic tool changes, and sampling flows authenticated, logged, and pinned to known provenance?",
			"Can users see and interrupt every sensitive tool call before it executes?",
		}
	case "agent-supply-chain":
		return []string{
			fmt.Sprintf("Which skills, plugins, models, MCP servers, or images are installed into %s?", resourceLabel),
			"Are those components pinned, reviewed, signed, and behaviorally checked against their declared purpose?",
			"Which credentials, files, or shell commands would a compromised component inherit?",
		}
	case "http-hardening":
		return []string{
			fmt.Sprintf("Which missing headers or cookie flags on %s can realistically be chained with browser, API, or session behavior?", resourceLabel),
			"Is this response browser-facing, API-only, or shared by both clients?",
			"Where should the security header baseline be enforced: app, ingress, CDN, or WAF?",
		}
	case "cors-misconfiguration":
		return []string{
			fmt.Sprintf("Which sensitive routes on %s share the observed CORS policy?", resourceLabel),
			"Can an untrusted Origin read authenticated or token-bearing responses?",
			"Which exact origins need cross-origin access in production?",
		}
	case "tls-posture":
		return []string{
			fmt.Sprintf("Should %s ever be reachable over cleartext HTTP or weak TLS?", resourceLabel),
			"Where is TLS terminated and where should HSTS be enforced?",
			"Which clients still require legacy protocol support, if any?",
		}
	case "sensitive-path":
		return []string{
			fmt.Sprintf("Which discovered paths under %s are intended to be public?", resourceLabel),
			"Do docs, schemas, metrics, debug, admin, or config paths reveal secrets, internal hosts, privileged verbs, or implementation details?",
			"Which paths should return 401, 403, or 404 from the public internet?",
		}
	case "risky-methods":
		return []string{
			fmt.Sprintf("Which routes on %s actually need PUT, PATCH, DELETE, TRACE, or CONNECT?", resourceLabel),
			"Are write-capable methods blocked before authentication and authorization?",
			"Can method override headers or proxies re-enable denied verbs?",
		}
	case "header-trust":
		return []string{
			fmt.Sprintf("Which proxy, ingress, CDN, or framework layer is allowed to trust Host and X-Forwarded-* headers for %s?", resourceLabel),
			"Can spoofed host, forwarded-host, original-url, or rewrite headers affect redirects, reset links, tenant routing, or auth decisions?",
			"Where should trusted proxy header normalization happen before traffic reaches application code?",
		}
	case "cache-policy":
		return []string{
			fmt.Sprintf("Which responses from %s can carry cookies, tokens, user data, or tenant-specific content?", resourceLabel),
			"Can any CDN, reverse proxy, browser, or service worker cache those responses across users?",
			"Where should no-store, private, and vary behavior be enforced for session-like responses?",
		}
	case "api-resource-controls":
		return []string{
			fmt.Sprintf("Which expensive operations, GraphQL queries, auth attempts, exports, or third-party calls sit behind %s?", resourceLabel),
			"Which rate, quota, payload-size, query-depth, and backoff controls are enforced server-side today?",
			"Do logs and response headers give operators enough signal to distinguish legitimate clients from abuse?",
		}
	default:
		return []string{
			fmt.Sprintf("What is the smallest safe validation step for %s?", resourceLabel),
			"What evidence would raise or lower the exploitability of this finding?",
		}
	}
}

func enrichSecurityFindingsWithRemediation(findings []securityFinding) []securityFinding {
	enriched := make([]securityFinding, 0, len(findings))
	for _, finding := range findings {
		containment, remediation, verification := buildSecurityFindingRemediation(finding)
		priority, owner, status := buildSecurityFindingTriage(finding)
		attackerView, defenderView := buildSecurityFindingPerspective(finding)
		finding.Containment = uniqueNonEmptyStrings(containment)
		finding.Remediation = uniqueNonEmptyStrings(remediation)
		finding.Verification = uniqueNonEmptyStrings(verification)
		if strings.TrimSpace(finding.AttackerView) == "" {
			finding.AttackerView = attackerView
		}
		if strings.TrimSpace(finding.DefenderView) == "" {
			finding.DefenderView = defenderView
		}
		if len(finding.RegressionTests) == 0 {
			finding.RegressionTests = uniqueNonEmptyStrings(buildSecurityFindingRegressionTests(finding))
		}
		finding.Priority = priority
		finding.Owner = owner
		finding.Status = status
		enriched = append(enriched, finding)
	}
	return enriched
}

func enrichSecurityAttackVectors(vectors []securityAttackVector) []securityAttackVector {
	enriched := make([]securityAttackVector, 0, len(vectors))
	for _, vector := range vectors {
		attackerView, defenderView := buildSecurityAttackVectorPerspective(vector)
		if strings.TrimSpace(vector.AttackerView) == "" {
			vector.AttackerView = attackerView
		}
		if strings.TrimSpace(vector.DefenderView) == "" {
			vector.DefenderView = defenderView
		}
		if len(vector.RegressionTests) == 0 {
			vector.RegressionTests = uniqueNonEmptyStrings(buildSecurityAttackVectorRegressionTests(vector))
		}
		enriched = append(enriched, vector)
	}
	return enriched
}

func buildSecurityFindingPerspective(finding securityFinding) (string, string) {
	resourceLabel := coalesceSecurityName(finding.ResourceName, finding.Endpoint, finding.ResourceID, "this target")
	category := strings.ToLower(strings.TrimSpace(finding.Category))
	switch {
	case category == "public-surface" || category == "reachable-surface" || category == "authenticated-surface":
		return fmt.Sprintf("An attacker treats %s as an entry point for route discovery, direct-origin bypass, auth-boundary probing, and low-noise enumeration before chaining into adjacent services.", resourceLabel),
			"Defenders should prove which routes must be public, close direct-origin bypasses, and keep anonymous/authenticated response diffs under continuous regression tests."
	case category == "credential-leak":
		return fmt.Sprintf("An attacker tries to replay the material tied to %s across cloud APIs, CI/CD systems, source hosts, and downstream services before rotation completes.", resourceLabel),
			"Defenders should revoke first, then enumerate every place the same material or a derived token could still exist and verify old credentials fail."
	case category == "identity-pivot" || category == "invoke-pivot":
		return fmt.Sprintf("An attacker who lands on %s uses attached roles, trust relationships, metadata, and invoke paths to move from runtime access into the control plane.", resourceLabel),
			"Defenders should narrow runtime identity, remove unused invoke edges, and alert on token exchange or downstream calls that do not match workload intent."
	case category == "agentic-risk":
		return fmt.Sprintf("An attacker feeds %s malicious web, document, issue, email, or RAG content to steer goals, tool choice, parameters, or data export without needing direct shell access.", resourceLabel),
			"Defenders should separate untrusted content from instructions, enforce tool policy outside the model, and require approval for sensitive side effects."
	case category == "mcp-tooling":
		return fmt.Sprintf("An attacker targets %s through poisoned tool descriptions, changed MCP schemas, peer-agent messages, sampling flows, or tool-name collisions.", resourceLabel),
			"Defenders should authenticate tool servers, pin schemas, surface every sensitive tool call to users, and block descriptor drift before execution."
	case category == "agent-supply-chain":
		return fmt.Sprintf("An attacker compromises %s by changing a skill, plugin, model, MCP server, image, or prompt dependency that inherits the agent runtime's permissions.", resourceLabel),
			"Defenders should maintain an agent dependency bill of materials, pin versions by digest or commit, and run behavioral checks before loading components."
	case category == "http-hardening":
		return fmt.Sprintf("An attacker chains weak response headers or cookie flags on %s with XSS, clickjacking, content sniffing, referrer leaks, or session theft.", resourceLabel),
			"Defenders should enforce a route-aware security header and cookie baseline at the edge and keep it under deployment regression tests."
	case category == "cors-misconfiguration":
		return fmt.Sprintf("An attacker hosts a malicious origin and tries to make a victim browser read responses from %s using permissive or reflected CORS.", resourceLabel),
			"Defenders should allow only explicit trusted origins, disable credentials by default, and test sensitive routes with hostile Origin values."
	case category == "tls-posture":
		return fmt.Sprintf("An attacker with network position targets %s for cleartext access, weak protocol negotiation, or downgrade-friendly behavior.", resourceLabel),
			"Defenders should force HTTPS, enable HSTS where applicable, and disable legacy TLS protocol support."
	case category == "sensitive-path":
		return fmt.Sprintf("An attacker forced-browses %s for schemas, docs, metrics, debug, admin, config, and source metadata that accelerate exploitation.", resourceLabel),
			"Defenders should remove, authenticate, or operator-gate discovery and debug paths and monitor route enumeration."
	case category == "risky-methods":
		return fmt.Sprintf("An attacker probes %s for write-capable or tunneling HTTP verbs that can mutate resources or bypass route assumptions.", resourceLabel),
			"Defenders should deny unused methods at the edge and enforce per-route authorization before any state-changing verb is accepted."
	case category == "header-trust":
		return fmt.Sprintf("An attacker spoofs Host, X-Forwarded-Host, X-Original-URL, or rewrite headers against %s to poison redirects, caches, tenant routing, or auth decisions.", resourceLabel),
			"Defenders should normalize trusted proxy headers at the first ingress, reject untrusted values, and regression-test redirects and auth gates with hostile header values."
	case category == "cache-policy":
		return fmt.Sprintf("An attacker looks for session-like responses from %s that can be retained by browsers, CDNs, shared proxies, or service workers and replayed across users.", resourceLabel),
			"Defenders should mark session-bound content no-store or private, vary on authorization/cookie state when caching is intentional, and verify cache layers honor the policy."
	case category == "api-resource-controls":
		return fmt.Sprintf("An attacker drives cheap repeated requests, expensive queries, credential stuffing, scraping, or denial-of-wallet behavior against %s.", resourceLabel),
			"Defenders should enforce quotas, rate limits, payload/query complexity limits, and backoff server-side, then expose enough signals for detection and client behavior."
	case securityFindingIsDatabaseExposure(finding):
		return fmt.Sprintf("An attacker prioritizes %s for banner grabs, weak-auth checks, data exfiltration, destructive writes, and credential reuse against replicas or dependent services.", resourceLabel),
			"Defenders should remove public ingress first, rotate exposed credentials, and validate private-only reachability from a known external vantage point."
	case securityFindingIsPublicCompute(finding):
		return fmt.Sprintf("An attacker looks at %s as a public foothold for service fingerprinting, metadata access, local secret discovery, and runtime identity abuse.", resourceLabel),
			"Defenders should restrict ingress, harden metadata access, and baseline process, network, and identity activity from the workload."
	case securityFindingIsBackupGap(finding):
		return fmt.Sprintf("An attacker benefits from %s having weak recovery because destructive actions, ransomware, or accidental data loss become harder to reverse.", resourceLabel),
			"Defenders should create restore evidence, assign recovery ownership, and make backup drift a deploy-blocking regression."
	default:
		return fmt.Sprintf("An attacker looks for the shortest path from %s to data access, privilege escalation, or durable control.", resourceLabel),
			"Defenders should validate the risky behavior with the smallest read-only test, close the highest-blast-radius path first, and keep an automated regression for the control."
	}
}

func buildSecurityFindingRegressionTests(finding securityFinding) []string {
	resourceLabel := coalesceSecurityName(finding.ResourceName, finding.Endpoint, finding.ResourceID, "this target")
	category := strings.ToLower(strings.TrimSpace(finding.Category))
	switch {
	case category == "public-surface" || category == "reachable-surface" || category == "authenticated-surface":
		return []string{
			fmt.Sprintf("From an external vantage point, assert %s only serves the documented public route set.", resourceLabel),
			"Diff anonymous and authenticated HEAD/GET/OPTIONS responses and fail the test when private behavior leaks anonymously.",
			"Assert direct-origin access cannot bypass the intended edge, CDN, gateway, or access policy.",
		}
	case category == "credential-leak":
		return []string{
			"Run secret scanning across code, CI logs, state exports, snapshots, and local config before release.",
			"Assert the revoked credential fails one read-only identity check after rotation.",
			"Assert equivalent newly issued credentials carry only the minimum required permissions.",
		}
	case category == "identity-pivot" || category == "invoke-pivot":
		return []string{
			fmt.Sprintf("Run a policy simulation for %s and fail if non-required control-plane actions are allowed.", resourceLabel),
			"Assert unused invoke paths and trust relationships remain absent after deployment.",
			"Assert token exchange and metadata access are logged with workload identity and source context.",
		}
	case category == "agentic-risk":
		return []string{
			"Replay prompt-injection fixtures from web pages, uploaded documents, emails, issues, and RAG records and assert the agent keeps the original goal.",
			"Assert out-of-scope shell, file, deploy, identity, and data-export tool calls are blocked or require approval outside the model.",
			"Assert every high-impact tool call logs goal, actor, parameters, data touched, and policy decision.",
		}
	case category == "mcp-tooling":
		return []string{
			"Inject a changed or malicious tool descriptor in test and assert the runtime blocks it before tool selection.",
			"Assert MCP server identity, schema version, and requested permissions are authenticated and pinned before each session.",
			"Assert users can see, pause, and reject sensitive tool invocations before execution.",
		}
	case category == "agent-supply-chain":
		return []string{
			"Assert every skill, plugin, MCP server, model, image, and prompt dependency is pinned to an approved digest, commit, or version.",
			"Run a behavioral integrity test that fails on undeclared file reads, credential access, shell execution, network egress, or tool chaining.",
			"Assert dependency install and update events are logged with provenance and requested permissions.",
		}
	case category == "http-hardening":
		return []string{
			fmt.Sprintf("Run a HEAD/GET regression against %s and assert required security headers and cookie flags are present.", resourceLabel),
			"Assert browser-facing responses include CSP plus clickjacking protection and API responses include nosniff/referrer policy where applicable.",
			"Fail deployment when session cookies miss Secure, HttpOnly, or SameSite on HTTPS routes.",
		}
	case category == "cors-misconfiguration":
		return []string{
			fmt.Sprintf("Send an OPTIONS preflight to %s with an untrusted Origin and fail if it is reflected or wildcarded for sensitive routes.", resourceLabel),
			"Assert credentialed CORS is disabled unless the route and origin are explicitly allowlisted.",
			"Assert sensitive authenticated responses cannot be read from hostile origins.",
		}
	case category == "tls-posture":
		return []string{
			fmt.Sprintf("Assert %s redirects cleartext HTTP to HTTPS or is unreachable over HTTP.", resourceLabel),
			"Assert TLS 1.0 and TLS 1.1 handshakes fail.",
			"Assert HSTS is present on HTTPS browser-facing routes where safe for the domain.",
		}
	case category == "sensitive-path":
		return []string{
			fmt.Sprintf("Forced-browse the sensitive path fixture list against %s and fail if unapproved paths return 2xx or 3xx anonymously.", resourceLabel),
			"Assert docs, schema, metrics, debug, admin, config, and source metadata paths require auth or are absent.",
			"Assert route enumeration is logged with source, path, status, and edge/origin context.",
		}
	case category == "risky-methods":
		return []string{
			fmt.Sprintf("Run OPTIONS against %s and fail if unused write or tunneling verbs are advertised.", resourceLabel),
			"Assert PUT, PATCH, DELETE, TRACE, and CONNECT are denied before route-specific authorization.",
			"Assert method override headers cannot bypass the edge or application method policy.",
		}
	case category == "header-trust":
		return []string{
			fmt.Sprintf("Send hostile Host, X-Forwarded-Host, X-Original-URL, and X-Rewrite-URL probes to %s and fail if redirects, routing, or auth gates change unexpectedly.", resourceLabel),
			"Assert password-reset links, tenant routing, absolute URLs, and cache keys are built from trusted configured origins rather than request headers.",
			"Assert only the first trusted ingress can set proxy headers and all downstream services receive normalized values.",
		}
	case category == "cache-policy":
		return []string{
			fmt.Sprintf("Run HEAD/GET checks against session-like responses from %s and fail if Cache-Control lacks no-store, private, or another approved directive.", resourceLabel),
			"Assert Set-Cookie, Authorization, and tenant-specific responses are not stored by CDN, reverse-proxy, browser, or service-worker caches unless explicitly approved.",
			"Assert Vary behavior prevents cross-user reuse when authenticated API responses are intentionally cacheable.",
		}
	case category == "api-resource-controls":
		return []string{
			fmt.Sprintf("Run bounded rate, quota, payload-size, and query-complexity checks against %s and fail when abuse controls are absent or unaudited.", resourceLabel),
			"Assert GraphQL or search endpoints enforce query depth, cost, pagination, upload size, and operation-count limits.",
			"Assert brute-force, scraping, and repeated expensive requests produce throttling/backoff telemetry without degrading normal clients.",
		}
	case securityFindingIsDatabaseExposure(finding):
		return []string{
			fmt.Sprintf("Assert %s rejects direct public TCP/TLS connections from an external test source.", resourceLabel),
			"Assert application traffic still reaches the data store only through approved private network paths.",
			"Assert old credentials tied to the exposure window no longer authenticate.",
		}
	case securityFindingIsPublicCompute(finding):
		return []string{
			fmt.Sprintf("Assert only approved source ranges can reach public services on %s.", resourceLabel),
			"Assert metadata and workload identity endpoints are unavailable to unauthenticated application paths.",
			"Assert no admin, debug, database, cache, or orchestration ports are exposed publicly.",
		}
	case securityFindingIsBackupGap(finding):
		return []string{
			fmt.Sprintf("Run a restore test for %s and fail if recovery exceeds the documented window.", resourceLabel),
			"Assert backup retention, encryption, and ownership settings remain present after each deployment.",
		}
	default:
		return []string{
			fmt.Sprintf("Add a read-only regression that proves the risky behavior on %s is closed.", resourceLabel),
			"Fail deployment when the same exposure, privilege, or data path reappears.",
		}
	}
}

func buildSecurityAttackVectorPerspective(vector securityAttackVector) (string, string) {
	text := strings.ToLower(strings.Join(uniqueNonEmptyStrings(append(append([]string{vector.Title, vector.Summary, vector.KillChainStage}, vector.Threats...), vector.EntryPoints...)), " "))
	switch {
	case strings.Contains(text, "prompt") || strings.Contains(text, "agent") || strings.Contains(text, "tool") || strings.Contains(text, "mcp"):
		return "Introduce untrusted instructions or a changed tool boundary, then wait for the agent to convert context into a privileged side effect.",
			"Keep instructions, data, tools, and permissions as separate policy objects; block sensitive actions outside the model and log every decision."
	case strings.Contains(text, "supply"):
		return "Alter a dependency that the runtime trusts so malicious behavior executes with the agent or workload's inherited permissions.",
			"Pin provenance, compare declared metadata with executable behavior, and quarantine dependency drift before runtime load."
	case strings.Contains(text, "cors") || strings.Contains(text, "origin"):
		return "Use a hostile origin to test whether victim browsers can read API responses or send credentialed cross-origin requests.",
			"Constrain origins route-by-route, avoid credentialed CORS by default, and regression-test hostile Origin values."
	case strings.Contains(text, "forced") || strings.Contains(text, "schema") || strings.Contains(text, "debug") || strings.Contains(text, "metrics"):
		return "Forced-browse predictable paths to collect schemas, docs, debug state, metrics, and admin clues before attempting auth bypass.",
			"Remove or authenticate discovery paths and alert on route-enumeration patterns against public edges."
	case strings.Contains(text, "tls") || strings.Contains(text, "transport") || strings.Contains(text, "https"):
		return "Look for cleartext, weak TLS, or downgrade-friendly behavior that exposes traffic or session material.",
			"Force HTTPS, enable HSTS where applicable, and keep modern TLS policy under deployment tests."
	case strings.Contains(text, "method") || strings.Contains(text, "verb"):
		return "Probe HTTP verbs to find write-capable or tunneling behavior that route owners did not intend to expose.",
			"Deny unused methods at the edge and verify application authorization before any state-changing verb."
	case strings.Contains(text, "host") || strings.Contains(text, "forwarded") || strings.Contains(text, "rewrite"):
		return "Spoof host and proxy headers to see whether redirects, tenants, cache keys, or auth decisions trust attacker-controlled request metadata.",
			"Normalize trusted proxy headers at the first ingress, reject untrusted host values, and regression-test redirects and auth gates with hostile headers."
	case strings.Contains(text, "cache"):
		return "Look for session-like responses that can be stored and replayed by browsers, CDNs, shared proxies, or service workers.",
			"Mark session-bound content no-store or private, vary on auth state when caching is intentional, and prove cache layers honor the policy."
	case strings.Contains(text, "rate") || strings.Contains(text, "quota") || strings.Contains(text, "resource-consumption") || strings.Contains(text, "resource control") || strings.Contains(text, "denial-of-wallet") || strings.Contains(text, "graphql"):
		return "Drive cheap repeated requests, expensive queries, brute-force attempts, scraping, or quota-burning calls until a server-side limit appears.",
			"Enforce rate, quota, payload, query-complexity, and backoff controls before expensive downstream work starts."
	case strings.Contains(text, "header") || strings.Contains(text, "cookie") || strings.Contains(text, "clickjacking"):
		return "Chain missing headers or weak cookie flags with browser execution, clickjacking, sniffing, or session-theft opportunities.",
			"Apply a route-aware response-header and cookie baseline at ingress and application layers."
	case strings.Contains(text, "credential") || strings.Contains(text, "secret"):
		return "Replay the exposed material quickly across identity, source, CI/CD, and cloud APIs to find the broadest surviving scope.",
			"Revoke first, enumerate blast radius second, and verify old material is dead from multiple control planes."
	case strings.Contains(text, "identity") || strings.Contains(text, "privilege") || strings.Contains(text, "pivot"):
		return "Use initial access to harvest runtime identity, then chain allowed actions into adjacent services or the control plane.",
			"Narrow non-human identities to workload intent and alert on token exchange, metadata, or invoke activity outside baseline."
	case strings.Contains(text, "database") || strings.Contains(text, "data-store"):
		return "Confirm database reachability, fingerprint auth posture, and look for fast data access or destructive write paths.",
			"Remove public reachability before deeper testing and prove private-only access plus credential rotation."
	default:
		return "Start with the lowest-friction entry point, validate reachable behavior, and chain toward data, identity, or control-plane impact.",
			"Close the highest-blast-radius control first, then preserve an automated test that proves the same path stays closed."
	}
}

func buildSecurityAttackVectorRegressionTests(vector securityAttackVector) []string {
	text := strings.ToLower(strings.Join(uniqueNonEmptyStrings(append(append([]string{vector.Title, vector.Summary, vector.KillChainStage}, vector.Threats...), vector.EntryPoints...)), " "))
	switch {
	case strings.Contains(text, "prompt") || strings.Contains(text, "agent") || strings.Contains(text, "tool") || strings.Contains(text, "mcp"):
		return []string{
			"Replay an indirect prompt-injection fixture and assert the original task, approved tools, and allowed parameters do not change.",
			"Inject a malicious or changed tool descriptor and assert execution is blocked before any sensitive action.",
			"Assert all write, shell, deploy, identity, data-export, and destructive actions require policy approval outside the model.",
		}
	case strings.Contains(text, "supply"):
		return []string{
			"Assert every runtime dependency is pinned to an approved digest, commit, or version.",
			"Run behavioral integrity tests that fail on undeclared file, credential, shell, network, or tool-chain access.",
		}
	case strings.Contains(text, "cors") || strings.Contains(text, "origin"):
		return []string{
			"Replay hostile Origin and preflight requests and fail if untrusted origins are reflected or wildcarded on sensitive routes.",
			"Assert credentialed CORS is disabled unless route and origin are explicitly allowlisted.",
		}
	case strings.Contains(text, "forced") || strings.Contains(text, "schema") || strings.Contains(text, "debug") || strings.Contains(text, "metrics"):
		return []string{
			"Forced-browse the approved sensitive-path fixture list and fail when docs, schemas, debug, metrics, admin, config, or source paths are public.",
			"Assert route-enumeration attempts are logged with source, path, status, and edge/origin context.",
		}
	case strings.Contains(text, "tls") || strings.Contains(text, "transport") || strings.Contains(text, "https"):
		return []string{
			"Assert cleartext HTTP either redirects to HTTPS or is unreachable.",
			"Assert TLS 1.0 and TLS 1.1 handshakes fail and HSTS is present where safe.",
		}
	case strings.Contains(text, "method") || strings.Contains(text, "verb"):
		return []string{
			"Run OPTIONS and harmless method probes and fail if unused write or tunneling methods are advertised or accepted.",
			"Assert method override headers cannot bypass edge or application method policy.",
		}
	case strings.Contains(text, "host") || strings.Contains(text, "forwarded") || strings.Contains(text, "rewrite"):
		return []string{
			"Replay hostile Host, X-Forwarded-Host, X-Original-URL, and X-Rewrite-URL probes and fail when redirects, routing, or auth gates change.",
			"Assert absolute URLs, tenant routing, and cache keys are derived from trusted configured origins or normalized ingress metadata.",
		}
	case strings.Contains(text, "cache"):
		return []string{
			"Run cache-policy checks against cookie, auth, and tenant-specific responses and fail when approved no-store, private, or Vary behavior disappears.",
			"Assert CDN, reverse-proxy, browser, and service-worker caches cannot replay user-specific responses across sessions.",
		}
	case strings.Contains(text, "rate") || strings.Contains(text, "quota") || strings.Contains(text, "resource-consumption") || strings.Contains(text, "resource control") || strings.Contains(text, "denial-of-wallet") || strings.Contains(text, "graphql"):
		return []string{
			"Run bounded rate, quota, payload-size, and query-complexity checks and fail when abuse controls are absent or unaudited.",
			"Assert brute-force, scraping, and repeated expensive requests create throttling/backoff telemetry without degrading normal clients.",
		}
	case strings.Contains(text, "header") || strings.Contains(text, "cookie") || strings.Contains(text, "clickjacking"):
		return []string{
			"Run HEAD/GET response-baseline tests and fail when required security headers or cookie flags disappear.",
			"Assert browser-facing routes keep CSP plus clickjacking controls and session cookies keep Secure, HttpOnly, and SameSite.",
		}
	case strings.Contains(text, "credential") || strings.Contains(text, "secret"):
		return []string{
			"Assert secret scanning finds no matching material in code, config, state, logs, or snapshots.",
			"Assert the old credential fails a read-only identity check after rotation.",
		}
	case strings.Contains(text, "identity") || strings.Contains(text, "privilege") || strings.Contains(text, "pivot"):
		return []string{
			"Run policy simulation and fail when the runtime identity can perform non-required high-risk actions.",
			"Assert token exchange, metadata access, and downstream invokes are logged with workload identity context.",
		}
	case strings.Contains(text, "database") || strings.Contains(text, "data-store"):
		return []string{
			"Assert direct public connections to the data store fail from an external vantage point.",
			"Assert the application can still reach the store only through approved private network paths.",
		}
	default:
		return []string{
			"Automate the smallest read-only proof that this attack path is closed.",
			"Fail the release if the same endpoint, permission, or data path reappears.",
		}
	}
}

func buildSecurityFindingTriage(finding securityFinding) (string, string, string) {
	priority := "p2"
	category := strings.ToLower(strings.TrimSpace(finding.Category))

	switch {
	case category == "credential-leak":
		priority = "p0"
	case securityFindingIsDatabaseExposure(finding):
		priority = "p0"
	case category == "public-surface" && (finding.Reachable || finding.StatusCode > 0):
		priority = "p0"
	case category == "identity-pivot" || category == "invoke-pivot":
		priority = "p1"
	case category == "agentic-risk" || category == "mcp-tooling":
		priority = "p1"
		if securitySeverityRank(finding.Severity) >= 4 || finding.Reachable || finding.Endpoint != "" {
			priority = "p0"
		}
	case category == "agent-supply-chain":
		priority = "p2"
		if securitySeverityRank(finding.Severity) >= 3 {
			priority = "p1"
		}
	case category == "cors-misconfiguration" || category == "sensitive-path" || category == "risky-methods" || category == "header-trust":
		priority = "p1"
		if securitySeverityRank(finding.Severity) >= 4 {
			priority = "p0"
		}
	case category == "api-resource-controls":
		priority = "p2"
		if securitySeverityRank(finding.Severity) >= 3 {
			priority = "p1"
		}
	case category == "http-hardening" || category == "tls-posture" || category == "cache-policy":
		priority = "p2"
		if securitySeverityRank(finding.Severity) >= 3 {
			priority = "p1"
		}
	case securityFindingIsPublicCompute(finding):
		priority = "p1"
	case category == "authenticated-surface" || category == "reachable-surface":
		priority = ternaryString(finding.Reachable || finding.StatusCode > 0, "p1", "p2")
	case securityFindingIsBackupGap(finding):
		priority = "p2"
	default:
		switch securitySeverityRank(finding.Severity) {
		case 4, 3:
			priority = "p1"
		case 2:
			priority = "p2"
		default:
			priority = "p3"
		}
	}

	owner := inferSecurityFindingOwner(finding)
	status := "monitor"
	switch priority {
	case "p0":
		status = "contain-now"
	case "p1":
		status = "fix-next"
	case "p2":
		status = "review-soon"
	}

	return priority, owner, status
}

func inferSecurityFindingOwner(finding securityFinding) string {
	category := strings.ToLower(strings.TrimSpace(finding.Category))
	provider := strings.ToLower(strings.TrimSpace(finding.Provider))
	resourceType := strings.ToLower(strings.TrimSpace(finding.ResourceType))

	switch category {
	case "credential-leak", "identity-pivot", "invoke-pivot":
		return "identity"
	case "agentic-risk", "mcp-tooling", "agent-supply-chain":
		return "agent-platform"
	case "tls-posture", "cors-misconfiguration", "risky-methods", "http-hardening", "header-trust", "cache-policy":
		return "edge"
	case "api-resource-controls":
		return "application"
	case "sensitive-path":
		return "application"
	}

	if securityFindingIsDatabaseExposure(finding) {
		return "data-platform"
	}
	if provider == "cloudflare" || provider == "vercel" || strings.Contains(resourceType, "gateway") || strings.Contains(resourceType, "load balancer") || strings.Contains(resourceType, "ingress") || strings.Contains(resourceType, "cdn") {
		return "edge"
	}
	if securityFindingIsPublicCompute(finding) || securityFindingIsBackupGap(finding) || isDeepResearchComputeLikeType(finding.ResourceType) {
		return "platform"
	}
	return "application"
}

func buildSecurityFindingRemediation(finding securityFinding) ([]string, []string, []string) {
	resourceLabel := coalesceSecurityName(finding.ResourceName, finding.Endpoint, "this target")
	switch strings.ToLower(strings.TrimSpace(finding.Category)) {
	case "public-surface", "reachable-surface", "authenticated-surface":
		containment := []string{
			"Restrict direct-origin access to the intended edge service or approved operator IP ranges.",
			"Gate documentation, schema, version, and well-known auth endpoints that do not need to be internet-facing.",
		}
		if finding.RequiresAuth {
			containment = append(containment, "Reduce token or session scope for this surface until intended routes are confirmed.")
		} else {
			containment = append(containment, "Put every non-public route behind explicit auth or a deny rule immediately.")
		}
		return containment,
			[]string{
				fmt.Sprintf("Document the exact routes on %s that must stay public and close everything else.", resourceLabel),
				"Tighten CORS, allowed methods, redirects, and cache behavior to the minimum policy the product needs.",
				"Move operator-only diagnostics and admin routes behind a separate authenticated control plane.",
			},
			[]string{
				"Re-run anonymous and authenticated HEAD/GET/OPTIONS checks and confirm only intended paths respond.",
				"Verify edge, WAF, and origin logs show traffic only through approved routes after containment.",
				"Confirm docs, schema, and version endpoints challenge or deny from the public internet when they should not be public.",
			}
	case "misconfiguration":
		if securityFindingIsDatabaseExposure(finding) {
			return []string{
					"Remove public ingress and disable public accessibility immediately.",
					"Rotate any credentials that may have been exposed while the data store was internet-reachable.",
					"Review audit and connection logs for recent internet-origin access attempts.",
				}, []string{
					fmt.Sprintf("Place %s on private networks only and force all access through approved application paths.", resourceLabel),
					"Enforce TLS, strong auth, and the narrowest possible source restrictions on every remaining path.",
					"Remove stale public DNS, load-balancer targets, or security-group rules that can reopen the same exposure.",
				}, []string{
					"Confirm direct public connections fail from an external vantage point after the ingress change.",
					"Verify application traffic still reaches the store through approved private paths only.",
					"Check that rotated credentials invalidate any old auth material tied to the exposure window.",
				}
		}
		if securityFindingIsBackupGap(finding) {
			return []string{
					"Take an out-of-band snapshot immediately before more changes or deeper testing.",
					"Document the current recovery gap so responders do not assume a rollback path exists.",
				}, []string{
					"Enable backup retention, snapshot cadence, and restore ownership for this resource.",
					"Add a restore test and evidence collection step to the service’s normal operational review.",
				}, []string{
					"Prove that a restore from retained backups succeeds inside the expected recovery window.",
					"Verify the retention setting and restore runbook stay present after the next deployment cycle.",
				}
		}
		return []string{
				"Close the public exposure with the smallest safe network, identity, or policy change first.",
				"Record rollback steps before any change that goes beyond read-only validation.",
			}, []string{
				fmt.Sprintf("Remove the accidental exposure on %s and move the control behind the intended front door.", resourceLabel),
				"Add policy or IaC guardrails so the same configuration drift cannot reopen silently.",
			}, []string{
				"Verify the exposed path is no longer reachable from the public internet.",
				"Confirm the intended production flow still works after the closing control is applied.",
			}
	case "credential-leak":
		return []string{
				"Rotate and revoke the leaked credential material before deeper investigation widens the blast radius.",
				"Search CI, local configs, snapshots, and secret stores for the same material or reused variants.",
				"Review recent audit logs for successful or failed use of the credential from unexpected sources.",
			}, []string{
				fmt.Sprintf("Move any secret-like material tied to %s into an approved secret manager or runtime injection path.", resourceLabel),
				"Remove the secret from infrastructure state, snapshots, and configuration surfaces that should never carry it.",
				"Reduce the credential’s permissions so a future leak has materially less blast radius.",
			}, []string{
				"Confirm the old credential no longer authenticates after rotation and revocation.",
				"Verify no remaining snapshot, config export, or state file still contains the secret-like key.",
				"Check audit logs for follow-on use after the rotation window closes.",
			}
	case "identity-pivot":
		fallthrough
	case "invoke-pivot":
		return []string{
				"Reduce the attached role and invoke paths to the minimum permissions required for production traffic.",
				"Review recent audit logs for token exchange, role use, or downstream calls from this runtime.",
			}, []string{
				fmt.Sprintf("Split broad privileges on %s into narrowly scoped identities aligned to one workload responsibility each.", resourceLabel),
				"Apply resource-level conditions or trust boundaries so the runtime cannot pivot into adjacent services by default.",
				"Remove unused service-to-service invoke edges and replace them with explicit allowlists.",
			}, []string{
				"Confirm the runtime can no longer perform the high-risk cloud actions or downstream invokes that created the pivot path.",
				"Verify production traffic still succeeds with the narrowed role and trust policy in place.",
				"Check that metadata or workload identity protections block opportunistic token retrieval paths.",
			}
	case "agentic-risk":
		return []string{
				"Disable or approval-gate high-impact tools for this agent until untrusted input paths are mapped.",
				"Separate untrusted retrieved content from instructions and tool parameters before the next run.",
				"Log and review recent agent tool calls for unusual file, shell, deploy, identity, or data access.",
			}, []string{
				fmt.Sprintf("Bind %s to a declared task scope and validate every tool call against that scope outside the model.", resourceLabel),
				"Apply least-agency: remove unnecessary tools, narrow file and network access, and require human approval for writes, deploys, identity changes, and data export.",
				"Add prompt-injection and context-poisoning tests using web, document, issue, email, and RAG payloads that the agent can encounter.",
			}, []string{
				"Confirm malicious instructions in retrieved content cannot change the goal, tool choice, or action parameters.",
				"Verify high-impact tool calls are blocked or require approval when they deviate from the original task.",
				"Review agent activity logs for goal state, selected tools, parameters, and approval outcomes.",
			}
	case "mcp-tooling":
		return []string{
				"Pin or disable unknown MCP/tool servers and require explicit approval before new tools appear in the agent runtime.",
				"Block sensitive tool calls until tool descriptors, schemas, and peer-agent identities are verified.",
			}, []string{
				fmt.Sprintf("Inventory every MCP server, tool schema, sampling flow, and peer-agent trust path used by %s.", resourceLabel),
				"Authenticate remote tool endpoints, sign or pin tool definitions, and alert on descriptor changes or tool-name collisions.",
				"Expose tool invocations and remote-agent instructions in the UI with interrupt and rollback paths for sensitive actions.",
			}, []string{
				"Verify a malicious or changed tool descriptor cannot redirect the agent into credential, file, shell, or exfiltration actions.",
				"Confirm peer-agent or MCP server identity is validated before a session begins and rechecked on changes.",
				"Re-run a tool-poisoning simulation and confirm the policy engine blocks the unsafe call.",
			}
	case "agent-supply-chain":
		return []string{
				"Freeze the current skill, plugin, model, MCP server, and image versions until provenance is reviewed.",
				"Disable components that are unpinned, recently changed, or able to reach credentials, files, shell, or network egress.",
			}, []string{
				fmt.Sprintf("Create a bill of materials for agent dependencies loaded by %s, including skills, prompts, scripts, models, MCP servers, and images.", resourceLabel),
				"Pin versions by digest or commit, require trusted sources, and compare each component's declared metadata against executable behavior.",
				"Run static and behavioral checks before installation and again whenever a third-party component updates.",
			}, []string{
				"Confirm the agent can only install or load allowlisted components from approved registries.",
				"Verify no component can silently exfiltrate files, credentials, code, or tool outputs outside the intended boundary.",
				"Check audit logs for component changes, install events, and newly requested permissions.",
			}
	case "http-hardening":
		return []string{
				"Apply missing security headers and cookie flags at the edge or origin before relying on browser clients.",
				"Review routes setting cookies and treat missing Secure, HttpOnly, or SameSite as session-hardening work.",
			}, []string{
				fmt.Sprintf("Define the response-header baseline for %s by route type: browser page, API response, redirect, and static asset.", resourceLabel),
				"Enforce HSTS on HTTPS surfaces where safe, X-Content-Type-Options, Referrer-Policy, CSP or frame-ancestors for browser routes, and strict session cookie flags.",
				"Move the baseline into ingress/CDN/WAF or framework middleware so app teams cannot drift route-by-route.",
			}, []string{
				"Re-run HEAD and GET probes and confirm the missing or weak header/cookie evidence is gone.",
				"Verify browser-facing pages keep CSP and clickjacking controls while API clients continue to function.",
				"Add deployment tests that fail when required headers or cookie flags disappear.",
			}
	case "cors-misconfiguration":
		return []string{
				"Disable wildcard, reflected, or credentialed CORS behavior on sensitive routes until intended origins are documented.",
				"Review access logs for requests with unusual Origin or Access-Control-Request-* headers.",
			}, []string{
				fmt.Sprintf("Create an explicit allowed-origin list for %s and apply it only to routes that need browser cross-origin access.", resourceLabel),
				"Disable Access-Control-Allow-Credentials unless the route requires it and has route-specific origin validation.",
				"Make CORS decisions before sensitive data is produced and keep preflight behavior consistent with actual route authorization.",
			}, []string{
				"Replay hostile Origin and preflight probes and confirm they no longer receive readable or credentialed access.",
				"Verify approved origins still work for required product flows.",
				"Assert sensitive authenticated responses cannot be read from untrusted origins.",
			}
	case "tls-posture":
		return []string{
				"Force HTTPS or close cleartext HTTP reachability where the endpoint carries sessions, tokens, or sensitive data.",
				"Disable TLS 1.0 and TLS 1.1 at the first terminating edge that accepts public traffic.",
			}, []string{
				fmt.Sprintf("Document where TLS terminates for %s and apply a modern protocol/cipher policy there.", resourceLabel),
				"Enable HSTS on browser-facing HTTPS routes once redirect and subdomain behavior is safe.",
				"Ensure internal origin traffic is either private, encrypted, or covered by explicit compensating controls.",
			}, []string{
				"Confirm HTTP redirects to HTTPS or fails closed from an external vantage point.",
				"Verify TLS 1.0 and TLS 1.1 handshakes fail and TLS 1.2/1.3 remain available.",
				"Confirm HSTS appears on HTTPS browser-facing responses where intended.",
			}
	case "sensitive-path":
		return []string{
				"Block or authenticate exposed docs, schemas, metrics, debug, admin, config, and source metadata paths.",
				"Review the discovered path responses for secrets, internal hosts, privileged verbs, stack traces, or implementation details.",
			}, []string{
				fmt.Sprintf("Define the public route inventory for %s and move every operator-only route behind auth or a private network.", resourceLabel),
				"Serve API documentation and schemas from an authenticated developer portal when they describe non-public behavior.",
				"Disable production debug, actuator, server-status, source metadata, and raw config endpoints unless explicitly approved.",
			}, []string{
				"Forced-browse the sensitive-path list and confirm unapproved paths return 401, 403, 404, or an approved public response.",
				"Verify route enumeration is logged with source IP, path, status, edge, and origin context.",
				"Confirm product-required public docs still expose only intended public API behavior.",
			}
	case "risky-methods":
		return []string{
				"Deny unused write-capable or tunneling HTTP methods at the edge before traffic reaches the application.",
				"Confirm state-changing methods require route-specific authentication and authorization.",
			}, []string{
				fmt.Sprintf("Document allowed methods for each route family on %s and reject everything else.", resourceLabel),
				"Disable TRACE and CONNECT on public application surfaces unless there is a tightly scoped operational need.",
				"Review method override headers and proxy behavior so denied verbs cannot be reintroduced upstream.",
			}, []string{
				"Re-run OPTIONS and harmless method probes and confirm unused methods are no longer advertised or accepted.",
				"Verify legitimate write routes still require proper authorization and CSRF/session controls where browser-facing.",
				"Assert method override headers cannot bypass edge or app policy.",
			}
	case "header-trust":
		return []string{
				"Reject hostile Host, X-Forwarded-Host, X-Original-URL, and X-Rewrite-URL values at the first trusted edge.",
				"Disable header-driven routing, redirects, reset-link generation, and auth decisions until trusted proxy normalization is confirmed.",
			}, []string{
				fmt.Sprintf("Configure %s to derive canonical origins and tenants from trusted configuration, not raw request headers.", resourceLabel),
				"Allow proxy headers only from known ingress/CDN/load-balancer hops and strip or overwrite them everywhere else.",
				"Separate public app routes from operator/admin routes so rewrite headers cannot cross the control-plane boundary.",
			}, []string{
				"Replay hostile Host and X-Forwarded-Host probes and confirm redirects never point at attacker-controlled hosts.",
				"Replay X-Original-URL and X-Rewrite-URL probes and confirm auth challenges and routing do not weaken.",
				"Verify edge and application logs include normalized host, original host, and proxy source context for suspicious requests.",
			}
	case "cache-policy":
		return []string{
				"Apply no-store or private Cache-Control to session-like responses that carry cookies, auth state, user data, or tenant-specific data.",
				"Purge CDN, reverse-proxy, and browser-relevant caches for routes that may have stored session-like content.",
			}, []string{
				fmt.Sprintf("Define route-level cache policy for %s and enforce it consistently at app, CDN, and ingress layers.", resourceLabel),
				"Use Vary on Authorization, Cookie, Origin, or tenant state when authenticated API caching is explicitly approved.",
				"Prevent service workers and client caches from retaining sensitive API responses unless there is a documented product requirement.",
			}, []string{
				"Re-run HEAD/GET probes and confirm cookie/auth-like responses include approved Cache-Control behavior.",
				"Confirm CDN/reverse-proxy cache diagnostics show misses or private handling for session-like responses.",
				"Assert public cacheable assets still cache correctly without sharing user-specific content.",
			}
	case "api-resource-controls":
		return []string{
				"Add or tighten server-side throttles on authentication, search, export, GraphQL, upload, and third-party-cost paths.",
				"Log repeated expensive requests, high-cardinality clients, and quota failures before raising limits.",
			}, []string{
				fmt.Sprintf("Define abuse budgets for %s by route: rate, burst, payload size, query depth/cost, pagination, and operation count.", resourceLabel),
				"Enforce limits before expensive downstream work starts and return consistent backoff or quota responses.",
				"Expose enough rate-limit or retry-after telemetry for operators and well-behaved clients without leaking sensitive policy internals.",
			}, []string{
				"Run bounded abuse tests that prove throttles, query limits, upload limits, and backoff responses activate before resource exhaustion.",
				"Confirm legitimate clients still succeed within documented quotas.",
				"Verify abuse-control events appear in logs, metrics, and alerting with source, route, actor, and limit context.",
			}
	default:
		return []string{
				"Contain the exposed control with the smallest safe change first.",
			}, []string{
				fmt.Sprintf("Define the intended access policy for %s and align configuration to that policy.", resourceLabel),
			}, []string{
				"Re-run the smallest read-only validation step and confirm the risky behavior is gone.",
			}
	}
}

func securityAuthKinds(pack securityRuntimeAuthPack) []string {
	kinds := []string{}
	if strings.TrimSpace(pack.BearerToken) != "" {
		kinds = append(kinds, "bearer")
	}
	if strings.TrimSpace(pack.Username) != "" && strings.TrimSpace(pack.Password) != "" {
		kinds = append(kinds, "basic")
	}
	if strings.TrimSpace(pack.Cookie) != "" {
		kinds = append(kinds, "cookie")
	}
	if len(pack.Headers) > 0 {
		kinds = append(kinds, "custom-headers")
	}
	return uniqueNonEmptyStrings(kinds)
}

func coalesceSecurityName(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return "target"
}

func ternaryStatus(condition bool, trueValue string, falseValue string) string {
	if condition {
		return trueValue
	}
	return falseValue
}

func ternaryString(condition bool, trueValue string, falseValue string) string {
	if condition {
		return trueValue
	}
	return falseValue
}

func minInt(left int, right int) int {
	if left < right {
		return left
	}
	return right
}
