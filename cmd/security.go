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
	defaultSecurityScanQuestion     = "Analyze the current infrastructure for public or reachable APIs, internet-facing surfaces, credential leaks, auth gaps, exploitable misconfigurations, and plausible attack paths. Prioritize externally reachable services and concrete attack vectors."
	securityScanResultMarker        = "::clanker-security-result::"
	maxSecurityProbeEndpoints       = 24
	maxSecurityAttackVectors        = 10
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
	AttackVectorCount  int    `json:"attackVectorCount"`
	AuthSignals        int    `json:"authSignals"`
	PrimaryFocus       string `json:"primaryFocus,omitempty"`
}

type securityFinding struct {
	ID           string   `json:"id"`
	Severity     string   `json:"severity"`
	Category     string   `json:"category"`
	Title        string   `json:"title"`
	Summary      string   `json:"summary"`
	Confidence   string   `json:"confidence,omitempty"`
	BlastRadius  string   `json:"blastRadius,omitempty"`
	ResourceID   string   `json:"resourceId,omitempty"`
	ResourceName string   `json:"resourceName,omitempty"`
	ResourceType string   `json:"resourceType,omitempty"`
	Provider     string   `json:"provider,omitempty"`
	Region       string   `json:"region,omitempty"`
	Endpoint     string   `json:"endpoint,omitempty"`
	Reachable    bool     `json:"reachable,omitempty"`
	StatusCode   int      `json:"statusCode,omitempty"`
	RequiresAuth bool     `json:"requiresAuth,omitempty"`
	Evidence     []string `json:"evidence,omitempty"`
	Questions    []string `json:"questions,omitempty"`
	Containment  []string `json:"containment,omitempty"`
	Remediation  []string `json:"remediation,omitempty"`
	Verification []string `json:"verification,omitempty"`
	Priority     string   `json:"priority,omitempty"`
	Owner        string   `json:"owner,omitempty"`
	Status       string   `json:"status,omitempty"`
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
	Endpoint         string
	ResourceID       string
	Scheme           string
	Port             string
	StatusCode       int
	Reachable        bool
	RequiresAuth     bool
	Authenticated    bool
	AllowsCORS       bool
	Server           string
	Banner           string
	TLSEnabled       bool
	TLSVersion       string
	ContentType      string
	Location         string
	Method           string
	InterestingPaths []string
	Notes            []string
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
checks for exploitable misconfigurations and secret leaks, and builds attack
vectors that an operator can validate manually.`,
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

	status, headers, location, err := doSecurityHTTPProbe(ctx, client, candidate.Endpoint, "HEAD", nil)
	if err != nil || status == 405 || status == 400 {
		status, headers, location, err = doSecurityHTTPProbe(ctx, client, candidate.Endpoint, "GET", nil)
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
	observation.Notes = buildSecurityProbeNotes(observation)

	if authPack.HasAuth() && observation.RequiresAuth {
		authHeaders := buildSecurityAuthHeaders(authPack)
		authStatus, authRespHeaders, _, authErr := doSecurityHTTPProbe(ctx, client, candidate.Endpoint, "GET", authHeaders)
		if authErr == nil && authStatus > 0 {
			if authStatus != http.StatusUnauthorized && authStatus != http.StatusForbidden {
				observation.Authenticated = true
				observation.Notes = append(observation.Notes, fmt.Sprintf("User-supplied auth changed the response to %d.", authStatus))
				if observation.ContentType == "" {
					observation.ContentType = strings.TrimSpace(authRespHeaders.Get("Content-Type"))
				}
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

func doSecurityHTTPProbe(ctx context.Context, client *http.Client, endpoint string, method string, headers map[string]string) (int, http.Header, string, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
	if err != nil {
		return 0, nil, "", err
	}
	req.Header.Set("User-Agent", "clanker-security-scan/1.0")
	req.Header.Set("Accept", "application/json, text/plain;q=0.9, */*;q=0.8")
	for key, value := range headers {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
			continue
		}
		req.Header.Set(key, value)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, "", err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 2048))
	return resp.StatusCode, resp.Header.Clone(), strings.TrimSpace(resp.Header.Get("Location")), nil
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
	if observation.RequiresAuth {
		notes = append(notes, "The endpoint signaled authentication or authorization checks.")
	}
	return notes
}

func discoverInterestingSecurityPaths(ctx context.Context, client *http.Client, endpoint string, headers map[string]string) []string {
	commonPaths := []string{"/health", "/healthz", "/openapi.json", "/swagger.json", "/docs", "/swagger/index.html", "/.well-known/openid-configuration"}
	baseURL, err := url.Parse(endpoint)
	if err != nil {
		return nil
	}
	baseURL.Path = ""
	baseURL.RawQuery = ""
	baseURL.Fragment = ""
	results := make([]string, 0, 3)
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
		if len(results) >= 3 {
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

	return sortAndCapSecurityAttackVectors(dedupeSecurityAttackVectors(vectors))
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
		switch strings.ToLower(strings.TrimSpace(finding.Severity)) {
		case "critical":
			summary.CriticalFindings++
		case "high":
			summary.HighFindings++
		}
		if finding.Category == "credential-leak" {
			summary.CredentialRisks++
		}
	}
	if summary.CredentialRisks > 0 {
		summary.PrimaryFocus = "credential exposure"
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
	if len(findings) > 18 {
		return findings[:18]
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
		finding.Containment = uniqueNonEmptyStrings(containment)
		finding.Remediation = uniqueNonEmptyStrings(remediation)
		finding.Verification = uniqueNonEmptyStrings(verification)
		finding.Priority = priority
		finding.Owner = owner
		finding.Status = status
		enriched = append(enriched, finding)
	}
	return enriched
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
