package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	ghclient "github.com/bgdnvk/clanker/internal/github"
	iamanalyzer "github.com/bgdnvk/clanker/internal/iam/analyzer"
	verdaclient "github.com/bgdnvk/clanker/internal/verda"
	"github.com/spf13/viper"
)

type securityProviderSubagent struct {
	Name string
	Run  func(context.Context) ([]securitySurfaceCandidate, []securityFinding, securitySubagentRun, []string)
}

type securityProviderContextLine struct {
	Section string
	Line    string
}

type securityAttributeSignal struct {
	Path  string
	Value string
}

var (
	securityURLPattern        = regexp.MustCompile(`https?://[^\s,]+`)
	securityIPv4Pattern       = regexp.MustCompile(`\b\d{1,3}(?:\.\d{1,3}){3}\b`)
	securityNamedFieldPattern = regexp.MustCompile(`(?i)\b(?:Function|Role|Bucket|Service|API|App|Cluster|Instance ID|DB Instance|Name)\s*:\s*([^,]+)`)
)

func canUseSecurityGitHubScout() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := ghclient.NewClient(viper.GetString("github.token"), "", "")
	status := client.GetAuthStatus(ctx)
	if !status.CLIAvailable {
		return false
	}
	_, _, err := client.ResolveRepository(ctx)
	return err == nil
}

func canUseSecurityVerdaScout() bool {
	return strings.TrimSpace(verdaclient.ResolveClientID()) != "" && strings.TrimSpace(verdaclient.ResolveClientSecret()) != ""
}

func collectSecurityProviderContext(ctx context.Context, provider string, prompt string, options deepResearchRunOptions) (deepResearchProviderContext, error) {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "github":
		return collectSecurityGitHubContext(ctx)
	case "verda":
		return collectSecurityVerdaContext(ctx, prompt, options)
	default:
		return collectDeepResearchProviderContext(ctx, provider, prompt, options)
	}
}

func collectSecurityGitHubContext(ctx context.Context) (deepResearchProviderContext, error) {
	timeoutCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	client := ghclient.NewClient(viper.GetString("github.token"), "", "")
	owner, repo, err := client.ResolveRepository(timeoutCtx)
	if err != nil {
		return deepResearchProviderContext{}, err
	}

	var out strings.Builder
	var warnings []string

	repos, err := client.ListRepositories(timeoutCtx, 100)
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("repositories: %v", err))
	} else {
		filtered := make([]ghclient.Repository, 0, 1)
		for _, item := range repos {
			if strings.EqualFold(strings.TrimSpace(item.Owner), owner) && strings.EqualFold(strings.TrimSpace(item.Name), repo) {
				filtered = append(filtered, item)
				break
			}
		}
		if len(filtered) > 0 {
			out.WriteString("GitHub Repository:\n")
			out.WriteString(ghclient.FormatRepositories(filtered))
			out.WriteString("\n")
		}
	}

	runners, err := client.ListRunners(timeoutCtx)
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("runners: %v", err))
	} else {
		out.WriteString("GitHub Runners:\n")
		out.WriteString(ghclient.FormatRunners(runners))
		out.WriteString("\n")
	}

	workflowInfo, err := client.GetRelevantContext(timeoutCtx, "workflow run status failed jobs steps artifacts logs pull requests")
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("workflows: %v", err))
	} else if strings.TrimSpace(workflowInfo) != "" {
		out.WriteString(workflowInfo)
		if !strings.HasSuffix(workflowInfo, "\n") {
			out.WriteString("\n")
		}
	}

	if len(warnings) > 0 {
		out.WriteString("GitHub Warnings:\n")
		for i, warningText := range warnings {
			if i >= 8 {
				out.WriteString("- (additional warnings omitted)\n")
				break
			}
			out.WriteString("- ")
			out.WriteString(warningText)
			out.WriteString("\n")
		}
		out.WriteString("\n")
	}

	blob := strings.TrimSpace(out.String())
	if blob == "" {
		return deepResearchProviderContext{}, fmt.Errorf("no GitHub repository context was collected")
	}

	label := owner + "/" + repo
	return deepResearchProviderContext{
		Provider: "github",
		Summary:  fmt.Sprintf("Collected GitHub live context for %s.", label),
		Details:  summarizeDeepResearchLines(blob, 4),
		Blob:     blob,
	}, nil
}

func collectSecurityVerdaContext(ctx context.Context, prompt string, options deepResearchRunOptions) (deepResearchProviderContext, error) {
	clientID := strings.TrimSpace(verdaclient.ResolveClientID())
	clientSecret := strings.TrimSpace(verdaclient.ResolveClientSecret())
	projectID := strings.TrimSpace(verdaclient.ResolveProjectID())
	if clientID == "" || clientSecret == "" {
		return deepResearchProviderContext{}, fmt.Errorf("Verda credentials are not configured")
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	client, err := verdaclient.NewClient(clientID, clientSecret, projectID, options.Debug)
	if err != nil {
		return deepResearchProviderContext{}, err
	}
	info, err := client.GetRelevantContext(timeoutCtx, prompt)
	if err != nil {
		return deepResearchProviderContext{}, err
	}

	label := projectID
	if label == "" {
		label = "default project"
	}
	return deepResearchProviderContext{
		Provider: "verda",
		Summary:  fmt.Sprintf("Collected Verda live context for %s.", label),
		Details:  summarizeDeepResearchLines(info, 4),
		Blob:     info,
	}, nil
}

func buildSecurityLiveProviderSubagents(scanCtx securityScanContext) []securityProviderSubagent {
	providerSet := map[string]struct{}{}
	for _, resource := range scanCtx.Estate.Resources {
		provider := inferDeepResearchProvider(resource)
		if provider == "" {
			continue
		}
		providerSet[provider] = struct{}{}
	}

	subagents := []securityProviderSubagent{}
	seen := map[string]struct{}{}
	appendScout := func(provider string) {
		if _, ok := seen[provider]; ok {
			return
		}
		seen[provider] = struct{}{}
		currentProvider := provider
		subagents = append(subagents, securityProviderSubagent{
			Name: currentProvider + "-security-scout",
			Run: func(ctx context.Context) ([]securitySurfaceCandidate, []securityFinding, securitySubagentRun, []string) {
				return runSecurityLiveProviderScout(ctx, currentProvider, scanCtx)
			},
		})
	}

	for _, provider := range []string{"aws", "gcp", "azure", "cloudflare", "digitalocean", "hetzner", "k8s", "supabase", "vercel", "verda"} {
		hasAccess := canRunDeepResearchProviderDrilldown(provider, scanCtx.Options)
		if provider == "verda" {
			hasAccess = canUseSecurityVerdaScout()
		}
		if shouldRunDeepResearchProvider(providerSet, provider, hasAccess) {
			appendScout(provider)
		}
	}
	if canRunDeepResearchProviderDrilldown("terraform", scanCtx.Options) || scanCtx.Estate.TerraformOK {
		appendScout("terraform")
	}
	if canUseSecurityGitHubScout() {
		appendScout("github")
	}

	return subagents
}

func executeSecurityProviderSubagentBatch(ctx context.Context, subagents []securityProviderSubagent) ([]securitySurfaceCandidate, []securityFinding, []securitySubagentRun, []string) {
	if len(subagents) == 0 {
		return nil, nil, nil, nil
	}

	var (
		waitGroup  sync.WaitGroup
		mu         sync.Mutex
		candidates []securitySurfaceCandidate
		findings   []securityFinding
		runs       []securitySubagentRun
		warnings   []string
	)

	for _, subagent := range subagents {
		current := subagent
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			fmt.Printf("[security][%s] starting\n", current.Name)
			runCandidates, runFindings, run, runWarnings := current.Run(ctx)
			fmt.Printf("[security][%s] %s\n", current.Name, run.Summary)

			mu.Lock()
			candidates = append(candidates, runCandidates...)
			findings = append(findings, runFindings...)
			runs = append(runs, run)
			warnings = append(warnings, runWarnings...)
			mu.Unlock()
		}()
	}
	waitGroup.Wait()

	sort.Slice(runs, func(i, j int) bool {
		return runs[i].Name < runs[j].Name
	})

	return mergeSecuritySurfaceCandidates(nil, candidates), dedupeSecurityFindings(findings), runs, uniqueNonEmptyStrings(warnings)
}

func runSecurityLiveProviderScout(ctx context.Context, provider string, scanCtx securityScanContext) ([]securitySurfaceCandidate, []securityFinding, securitySubagentRun, []string) {
	name := provider + "-security-scout"
	prompt := buildSecurityProviderContextPrompt(provider, scanCtx.Question)
	contextResult, err := collectSecurityProviderContext(ctx, provider, prompt, scanCtx.Options)
	if err != nil {
		summary := fmt.Sprintf("%s live security scout failed: %v", securityProviderLabel(provider), err)
		return nil, nil, securitySubagentRun{Name: name, Status: "warning", Summary: summary}, []string{summary}
	}

	lines := parseSecurityProviderContextLines(contextResult.Blob)
	candidates := buildSecurityProviderContextCandidates(provider, lines)
	findings := buildSecurityProviderContextFindings(provider, lines)

	details := append([]string{}, contextResult.Details...)
	if len(findings) > 0 {
		details = append(details, summarizeSecurityFindingHeadlines(findings, 3)...)
	}
	if len(candidates) > 0 {
		details = append(details, fmt.Sprintf("Live provider context yielded %d endpoint candidates.", len(candidates)))
	}

	summary := fmt.Sprintf("%s live security context yielded %d findings and %d endpoint candidates.", securityProviderLabel(provider), len(findings), len(candidates))
	if len(findings) == 0 && len(candidates) == 0 {
		summary = fmt.Sprintf("%s live security context added coverage but produced no high-signal findings.", securityProviderLabel(provider))
	}

	return candidates, findings, securitySubagentRun{
		Name:    name,
		Status:  "ok",
		Summary: summary,
		Details: uniqueNonEmptyStrings(details),
	}, nil
}

func buildSecurityProviderContextPrompt(provider string, question string) string {
	base := "security posture review public exposure internet-facing services unauthenticated urls privileged iam roles wildcard access admin permissions audit logs alerts public storage encryption firewalls"
	suffix := strings.TrimSpace(question)
	if suffix != "" {
		suffix = " question: " + suffix
	}

	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "aws":
		return base + " ec2 instances lambda functions rds databases s3 buckets ecs containers iam roles cloudwatch logs alarms public urls auth none" + suffix
	case "gcp":
		return base + " service accounts iam roles cloud run compute engine gcp firewall cloud firewall cloud load balancing cloud armor cloud dns cloud sql cloud storage cloud functions logs cloud monitoring alert policy api gateway" + suffix
	case "azure":
		return base + " resource groups virtual machines aks app service function app storage account key vault postgres mysql redis activity logs alert rules" + suffix
	case "cloudflare":
		return base + " cloudflare zones dns domains workers pages r2 d1 waf firewall access tunnels public hostnames" + suffix
	case "digitalocean":
		return base + " droplets kubernetes doks databases apps load balancers domains firewalls volumes floating ips" + suffix
	case "hetzner":
		return base + " servers load balancers networks firewalls floating ips primary ips certificates kubernetes" + suffix
	case "k8s":
		return base + " kubernetes k8s namespaces ingress services load balancers nodeports pods daemonsets deployments secrets cluster role binding public ip auth none" + suffix
	case "supabase":
		return base + " supabase postgres database storage buckets auth row level security rls policies anon service role secrets logs public schema" + suffix
	case "vercel":
		return base + " vercel projects deployments domains edge functions preview production" + suffix
	case "verda":
		return base + " verda instances clusters volumes ssh keys scripts container deployments job deployments public endpoints auth none firewall ingress egress" + suffix
	case "github":
		return base + " github repository repo actions workflows workflow runs jobs steps artifacts logs self-hosted runners pull requests" + suffix
	case "terraform":
		return base + " terraform state summary plan changes diff outputs resources ingress firewall security groups public access bucket encryption iam role" + suffix
	default:
		return base + suffix
	}
}

func parseSecurityProviderContextLines(blob string) []securityProviderContextLine {
	lines := []securityProviderContextLine{}
	currentSection := ""
	for _, rawLine := range strings.Split(blob, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.Trim(line, "-= ") == "" {
			continue
		}
		if strings.HasSuffix(line, ":") && !strings.HasPrefix(line, "-") {
			currentSection = strings.TrimSuffix(line, ":")
			continue
		}
		lines = append(lines, securityProviderContextLine{Section: currentSection, Line: line})
	}
	return lines
}

func buildSecurityProviderContextCandidates(provider string, lines []securityProviderContextLine) []securitySurfaceCandidate {
	if strings.EqualFold(strings.TrimSpace(provider), "github") {
		return nil
	}

	seen := map[string]struct{}{}
	candidates := []securitySurfaceCandidate{}

	for _, entry := range lines {
		for _, rawTarget := range extractSecurityProviderCandidateTargets(entry) {
			for _, endpoint := range normalizeSecurityEndpoints(rawTarget, "") {
				key := strings.ToLower(strings.TrimSpace(provider + "|" + endpoint))
				if _, ok := seen[key]; ok {
					continue
				}
				seen[key] = struct{}{}
				candidates = append(candidates, securitySurfaceCandidate{
					ResourceName:  securityProviderContextSubject(entry.Line),
					ResourceType:  securityProviderSectionResourceType(provider, entry.Section),
					Provider:      provider,
					Endpoint:      endpoint,
					Source:        fmt.Sprintf("%s live context", coalesceSecurityName(entry.Section, securityProviderLabel(provider))),
					LikelyPublic:  securityEndpointLooksPublic(endpoint),
					LikelyPrivate: securityEndpointLooksPrivate(endpoint),
				})
			}
		}
	}

	return candidates
}

func buildSecurityProviderContextFindings(provider string, lines []securityProviderContextLine) []securityFinding {
	findings := []securityFinding{}
	for _, entry := range lines {
		findings = append(findings, buildSecurityProviderLineFindings(provider, entry)...)
	}
	return dedupeSecurityFindings(findings)
}

func buildSecurityProviderLineFindings(provider string, entry securityProviderContextLine) []securityFinding {
	lineLower := strings.ToLower(strings.TrimSpace(entry.Line))
	sectionLower := strings.ToLower(strings.TrimSpace(entry.Section))
	if lineLower == "" {
		return nil
	}

	resourceName := securityProviderContextSubject(entry.Line)
	if strings.EqualFold(strings.TrimSpace(provider), "github") {
		resourceName = securityGitHubContextSubject(entry.Line)
	}
	resourceType := securityProviderSectionResourceType(provider, entry.Section)
	endpoint := ""
	if !strings.EqualFold(strings.TrimSpace(provider), "github") {
		if targets := extractSecurityProviderCandidateTargets(entry); len(targets) > 0 {
			if endpoints := normalizeSecurityEndpoints(targets[0], ""); len(endpoints) > 0 {
				endpoint = endpoints[0]
			}
		}
	}
	evidence := []string{fmt.Sprintf("%s: %s", coalesceSecurityName(entry.Section, securityProviderLabel(provider)), entry.Line)}
	label := coalesceSecurityName(resourceName, entry.Section, securityProviderLabel(provider)+" resource")
	findings := []securityFinding{}

	if strings.EqualFold(strings.TrimSpace(provider), "github") {
		if strings.Contains(sectionLower, "repository") && strings.Contains(lineLower, "(public") {
			findings = append(findings, securityFinding{
				ID:           buildDeepResearchFindingID("security-github-public-repo", provider+"|"+entry.Line),
				Severity:     "medium",
				Category:     "public-surface",
				Title:        fmt.Sprintf("%s is a public GitHub repository", label),
				Summary:      "The current repository is public. Treat workflow files, issue templates, release artifacts, and any referenced endpoints as internet-visible attack surface.",
				ResourceName: resourceName,
				ResourceType: resourceType,
				Provider:     provider,
				Evidence:     evidence,
				Questions:    buildSecurityQuestions("public-surface", resourceName, "", false),
			})
		}
		if strings.Contains(sectionLower, "runner") && !strings.Contains(lineLower, "no self-hosted runners") {
			findings = append(findings, securityFinding{
				ID:           buildDeepResearchFindingID("security-github-self-hosted-runner", provider+"|"+entry.Line),
				Severity:     "high",
				Category:     "ci-cd",
				Title:        fmt.Sprintf("%s uses a self-hosted GitHub Actions runner", label),
				Summary:      "Self-hosted runners expand the CI trust boundary into your own compute. Review network reachability, secret scoping, runner isolation, and post-job cleanup.",
				ResourceName: resourceName,
				ResourceType: resourceType,
				Provider:     provider,
				Evidence:     evidence,
				Questions:    buildSecurityQuestions("misconfiguration", resourceName, "", false),
			})
		}
	}

	if securityLineShowsNoAuth(lineLower) && endpoint != "" {
		findings = append(findings, securityFinding{
			ID:           buildDeepResearchFindingID("security-provider-no-auth", provider+"|"+entry.Section+"|"+entry.Line),
			Severity:     "high",
			Category:     "public-surface",
			Title:        fmt.Sprintf("%s exposes an unauthenticated public URL", label),
			Summary:      "Live provider context reports an explicit URL with no auth requirement. Treat that as an internet-facing surface until proven otherwise.",
			ResourceName: resourceName,
			ResourceType: resourceType,
			Provider:     provider,
			Endpoint:     endpoint,
			Evidence:     evidence,
			Questions:    buildSecurityQuestions("public-surface", resourceName, endpoint, false),
		})
	}

	if securityLineHasWorldOpenCIDR(lineLower) {
		severity := "high"
		if securityLineHasSensitivePort(lineLower) || strings.Contains(sectionLower, "firewall") || strings.Contains(sectionLower, "security group") {
			severity = "critical"
		}
		findings = append(findings, securityFinding{
			ID:           buildDeepResearchFindingID("security-provider-world-open", provider+"|"+entry.Section+"|"+entry.Line),
			Severity:     severity,
			Category:     "misconfiguration",
			Title:        fmt.Sprintf("%s allows world-open network access", label),
			Summary:      "Live provider context includes a world-open CIDR such as 0.0.0.0/0 or ::/0. That should be treated as an exposed ingress path until reviewed.",
			ResourceName: resourceName,
			ResourceType: resourceType,
			Provider:     provider,
			Endpoint:     endpoint,
			Evidence:     evidence,
			Questions:    buildSecurityQuestions("misconfiguration", resourceName, endpoint, false),
		})
	}

	if securityLineShowsPublicStorageExposure(lineLower) {
		findings = append(findings, securityFinding{
			ID:           buildDeepResearchFindingID("security-provider-storage-public", provider+"|"+entry.Section+"|"+entry.Line),
			Severity:     "critical",
			Category:     "misconfiguration",
			Title:        fmt.Sprintf("%s looks publicly readable", label),
			Summary:      "Live provider context contains a public storage exposure marker such as public-read or allUsers. That is a direct confidentiality risk.",
			ResourceName: resourceName,
			ResourceType: resourceType,
			Provider:     provider,
			Evidence:     evidence,
			Questions:    buildSecurityQuestions("misconfiguration", resourceName, endpoint, false),
		})
	}

	if securityLineShowsPrivilegedIdentity(lineLower) {
		severity := "high"
		if strings.Contains(lineLower, "administratoraccess") || strings.Contains(lineLower, "roles/owner") {
			severity = "critical"
		}
		findings = append(findings, securityFinding{
			ID:           buildDeepResearchFindingID("security-provider-identity-admin", provider+"|"+entry.Section+"|"+entry.Line),
			Severity:     severity,
			Category:     "identity-pivot",
			Title:        fmt.Sprintf("%s references an admin or wildcard identity path", label),
			Summary:      "Live provider context references a privileged role, wildcard access path, or role-chaining capability that deserves immediate review.",
			ResourceName: resourceName,
			ResourceType: resourceType,
			Provider:     provider,
			Evidence:     evidence,
			Questions:    buildSecurityQuestions("identity-pivot", resourceName, endpoint, false),
		})
	}

	if securityLineShowsDisabledControl(sectionLower, lineLower) {
		findings = append(findings, securityFinding{
			ID:           buildDeepResearchFindingID("security-provider-disabled-control", provider+"|"+entry.Section+"|"+entry.Line),
			Severity:     "medium",
			Category:     "misconfiguration",
			Title:        fmt.Sprintf("%s has a weak or disabled detective control", label),
			Summary:      "Live provider context suggests a logging, monitoring, or edge-protection control is disabled or ineffective.",
			ResourceName: resourceName,
			ResourceType: resourceType,
			Provider:     provider,
			Evidence:     evidence,
			Questions:    buildSecurityQuestions("misconfiguration", resourceName, endpoint, false),
		})
	}

	return findings
}

func buildSecurityNetworkPolicyFindings(resources []deepResearchResource) []securityFinding {
	findings := []securityFinding{}
	for _, resource := range resources {
		signals := securityResourceAttributeSignals(resource.Attributes)
		worldOpenEvidence := []string{}
		dangerous := false
		for _, signal := range signals {
			combined := strings.ToLower(strings.TrimSpace(signal.Path + "=" + signal.Value))
			if !securityAttributePathInteresting(signal.Path) && !securityLineHasWorldOpenCIDR(combined) {
				continue
			}
			if !securityLineHasWorldOpenCIDR(combined) {
				continue
			}
			worldOpenEvidence = append(worldOpenEvidence, fmt.Sprintf("%s=%s", signal.Path, signal.Value))
			if securityLineHasSensitivePort(combined) {
				dangerous = true
			}
		}
		worldOpenEvidence = uniqueNonEmptyStrings(worldOpenEvidence)
		if len(worldOpenEvidence) == 0 {
			continue
		}
		if len(worldOpenEvidence) > 5 {
			worldOpenEvidence = worldOpenEvidence[:5]
		}

		severity := "high"
		if dangerous || isDeepResearchDatabaseType(resource.Type) || strings.Contains(strings.ToLower(resource.Type), "redis") {
			severity = "critical"
		}
		endpoint := firstSecurityEndpointForHost(deepResearchFirstNonEmptyAttr(resource.Attributes, "publicIp", "publicIpAddress", "natIP", "ipAddress", "hostname", "dnsName"))
		findings = append(findings, securityFinding{
			ID:           buildDeepResearchFindingID("security-world-open-ingress", resource.ID),
			Severity:     severity,
			Category:     "misconfiguration",
			Title:        fmt.Sprintf("%s appears to allow world-open ingress", deepResearchResourceLabel(resource)),
			Summary:      "Security-shaped attributes in the estate snapshot include world-open CIDRs such as 0.0.0.0/0 or ::/0. Treat this as exposed ingress until the exact rule is verified.",
			ResourceID:   resource.ID,
			ResourceName: resource.Name,
			ResourceType: resource.Type,
			Provider:     inferDeepResearchProvider(resource),
			Region:       resource.Region,
			Endpoint:     endpoint,
			Evidence:     worldOpenEvidence,
			Questions:    buildSecurityQuestions("misconfiguration", resource.Name, endpoint, false),
		})
	}
	return findings
}

func buildSecurityIAMPolicyFindings(resources []deepResearchResource) []securityFinding {
	findings := []securityFinding{}
	for _, resource := range resources {
		publicAddress := deepResearchFirstNonEmptyAttr(resource.Attributes, "publicIp", "publicIpAddress", "natIP", "ipAddress")
		findings = append(findings, buildSecurityIAMDocumentFindings(resource, publicAddress)...)

		policies := uniqueNonEmptyStrings(resource.IAMPolicies)
		if len(policies) == 0 {
			continue
		}

		adminPolicies := []string{}
		chainPolicies := []string{}
		for _, policy := range policies {
			if securityLooksLikeJSONPolicyDocument(policy) {
				continue
			}
			lower := strings.ToLower(strings.TrimSpace(policy))
			switch {
			case strings.Contains(lower, "administratoraccess") || strings.Contains(lower, "roles/owner") || strings.EqualFold(strings.TrimSpace(policy), "owner") || strings.Contains(lower, "fullaccess") || strings.Contains(lower, "poweruser"):
				adminPolicies = append(adminPolicies, policy)
			case strings.Contains(lower, "passrole") || strings.Contains(lower, "assumerole") || strings.Contains(lower, "serviceaccountuser") || strings.Contains(lower, "tokencreator") || strings.Contains(lower, "workloadidentityuser"):
				chainPolicies = append(chainPolicies, policy)
			}
		}

		if len(adminPolicies) > 0 {
			severity := "high"
			if publicAddress != "" || strings.TrimSpace(resource.IAMRole) != "" {
				severity = "critical"
			}
			evidence := []string{}
			if strings.TrimSpace(resource.IAMRole) != "" {
				evidence = append(evidence, fmt.Sprintf("IAM role: %s", resource.IAMRole))
			}
			evidence = append(evidence, fmt.Sprintf("High-risk IAM policies: %s", strings.Join(adminPolicies, ", ")))
			if publicAddress != "" {
				evidence = append(evidence, fmt.Sprintf("Public address: %s", publicAddress))
			}
			findings = append(findings, securityFinding{
				ID:           buildDeepResearchFindingID("security-admin-policy", resource.ID),
				Severity:     severity,
				Category:     "identity-pivot",
				Title:        fmt.Sprintf("%s references admin-grade IAM access", deepResearchResourceLabel(resource)),
				Summary:      "The estate snapshot includes admin-grade or wildcard IAM policy names for this resource. That materially increases blast radius if the runtime is reached.",
				Confidence:   "medium",
				BlastRadius:  securityIAMBlastRadius(resource, adminPolicies, "admin_access", publicAddress),
				ResourceID:   resource.ID,
				ResourceName: resource.Name,
				ResourceType: resource.Type,
				Provider:     inferDeepResearchProvider(resource),
				Region:       resource.Region,
				Evidence:     uniqueNonEmptyStrings(evidence),
				Questions:    buildSecurityQuestions("identity-pivot", resource.Name, publicAddress, false),
			})
		}

		if len(chainPolicies) > 0 {
			evidence := []string{}
			if strings.TrimSpace(resource.IAMRole) != "" {
				evidence = append(evidence, fmt.Sprintf("IAM role: %s", resource.IAMRole))
			}
			evidence = append(evidence, fmt.Sprintf("Role-chaining IAM policies: %s", strings.Join(chainPolicies, ", ")))
			findings = append(findings, securityFinding{
				ID:           buildDeepResearchFindingID("security-role-chain-policy", resource.ID),
				Severity:     "high",
				Category:     "identity-pivot",
				Title:        fmt.Sprintf("%s can likely chain or delegate identity", deepResearchResourceLabel(resource)),
				Summary:      "The estate snapshot includes IAM policy names associated with assume-role, pass-role, or delegated service-account behavior. That widens post-compromise pivot paths.",
				Confidence:   "medium",
				BlastRadius:  securityIAMBlastRadius(resource, chainPolicies, "excessive_permissions", publicAddress),
				ResourceID:   resource.ID,
				ResourceName: resource.Name,
				ResourceType: resource.Type,
				Provider:     inferDeepResearchProvider(resource),
				Region:       resource.Region,
				Evidence:     uniqueNonEmptyStrings(evidence),
				Questions:    buildSecurityQuestions("identity-pivot", resource.Name, publicAddress, false),
			})
		}
	}
	return findings
}

func buildSecurityIAMDocumentFindings(resource deepResearchResource, publicAddress string) []securityFinding {
	findings := []securityFinding{}
	resourceLabel := coalesceSecurityName(strings.TrimSpace(resource.IAMRole), deepResearchResourceLabel(resource), resource.ID)
	for _, document := range securityCollectIAMPolicyDocuments(resource) {
		for _, finding := range iamanalyzer.AnalyzePermissions(resourceLabel, document) {
			findings = append(findings, securityIAMAnalyzerFinding(resource, finding, publicAddress, "policy document"))
		}
	}
	for _, document := range securityCollectIAMTrustDocuments(resource) {
		for _, finding := range iamanalyzer.AnalyzeTrustPolicy(resourceLabel, document) {
			findings = append(findings, securityIAMAnalyzerFinding(resource, finding, publicAddress, "trust policy"))
		}
	}
	return dedupeSecurityFindings(findings)
}

func securityIAMAnalyzerFinding(resource deepResearchResource, finding iamanalyzer.SecurityFinding, publicAddress string, source string) securityFinding {
	resourceLabel := deepResearchResourceLabel(resource)
	title := fmt.Sprintf("%s has risky IAM policy shape", resourceLabel)
	switch finding.Type {
	case iamanalyzer.FindingAdminAccess:
		title = fmt.Sprintf("%s has IAM permissions that enable privilege escalation", resourceLabel)
	case iamanalyzer.FindingCrossAccountTrust:
		title = fmt.Sprintf("%s has risky trust relationships", resourceLabel)
	case iamanalyzer.FindingMissingResourceScoping:
		title = fmt.Sprintf("%s has weak IAM resource scoping", resourceLabel)
	case iamanalyzer.FindingPublicS3Access:
		title = fmt.Sprintf("%s can read broadly across storage", resourceLabel)
	case iamanalyzer.FindingExcessivePermissions:
		title = fmt.Sprintf("%s has an IAM action combination that widens blast radius", resourceLabel)
	}
	evidence := []string{}
	if strings.TrimSpace(resource.IAMRole) != "" {
		evidence = append(evidence, fmt.Sprintf("IAM role: %s", resource.IAMRole))
	}
	if strings.TrimSpace(source) != "" {
		evidence = append(evidence, fmt.Sprintf("Analyzed %s", source))
	}
	if len(finding.Actions) > 0 {
		evidence = append(evidence, fmt.Sprintf("Actions: %s", strings.Join(uniqueNonEmptyStrings(finding.Actions), ", ")))
	}
	if len(finding.Resources) > 0 {
		evidence = append(evidence, fmt.Sprintf("Resources: %s", strings.Join(uniqueNonEmptyStrings(finding.Resources), ", ")))
	}
	if publicAddress != "" {
		evidence = append(evidence, fmt.Sprintf("Public address: %s", publicAddress))
	}
	return securityFinding{
		ID:           buildDeepResearchFindingID("security-iam-document", resource.ID+"|"+finding.Type+"|"+finding.Description),
		Severity:     finding.Severity,
		Category:     "identity-pivot",
		Title:        title,
		Summary:      finding.Description,
		Confidence:   "high",
		BlastRadius:  securityIAMBlastRadius(resource, finding.Actions, finding.Type, publicAddress),
		ResourceID:   resource.ID,
		ResourceName: resource.Name,
		ResourceType: resource.Type,
		Provider:     inferDeepResearchProvider(resource),
		Region:       resource.Region,
		Evidence:     uniqueNonEmptyStrings(evidence),
		Questions:    buildSecurityQuestions("identity-pivot", resource.Name, publicAddress, false),
	}
}

func securityCollectIAMPolicyDocuments(resource deepResearchResource) []string {
	documents := []string{}
	for _, policy := range uniqueNonEmptyStrings(resource.IAMPolicies) {
		if securityLooksLikeJSONPolicyDocument(policy) {
			documents = append(documents, strings.TrimSpace(policy))
		}
	}
	collectSecurityJSONDocuments("", resource.Attributes, []string{"policy"}, &documents)
	return uniqueNonEmptyStrings(documents)
}

func securityCollectIAMTrustDocuments(resource deepResearchResource) []string {
	documents := []string{}
	collectSecurityJSONDocuments("", resource.Attributes, []string{"trust", "assume"}, &documents)
	return uniqueNonEmptyStrings(documents)
}

func collectSecurityJSONDocuments(prefix string, value interface{}, markers []string, documents *[]string) {
	pathLower := strings.ToLower(strings.TrimSpace(prefix))
	switch typed := value.(type) {
	case map[string]interface{}:
		if securityPathHasMarker(pathLower, markers) {
			if document, ok := securityMarshalPolicyDocument(typed); ok {
				*documents = append(*documents, document)
			}
		}
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			next := key
			if prefix != "" {
				next = prefix + "." + key
			}
			collectSecurityJSONDocuments(next, typed[key], markers, documents)
		}
	case []interface{}:
		if securityPathHasMarker(pathLower, markers) {
			if document, ok := securityMarshalPolicyDocument(typed); ok {
				*documents = append(*documents, document)
			}
		}
		for index, item := range typed {
			next := fmt.Sprintf("%s[%d]", prefix, index)
			collectSecurityJSONDocuments(next, item, markers, documents)
		}
	case string:
		if securityPathHasMarker(pathLower, markers) && securityLooksLikeJSONPolicyDocument(typed) {
			*documents = append(*documents, strings.TrimSpace(typed))
		}
	}
}

func securityPathHasMarker(path string, markers []string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	for _, marker := range markers {
		if strings.Contains(path, strings.ToLower(strings.TrimSpace(marker))) {
			return true
		}
	}
	return false
}

func securityMarshalPolicyDocument(value interface{}) (string, bool) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", false
	}
	document := strings.TrimSpace(string(encoded))
	if !securityLooksLikeJSONPolicyDocument(document) {
		return "", false
	}
	return document, true
}

func securityLooksLikeJSONPolicyDocument(document string) bool {
	trimmed := strings.TrimSpace(document)
	if trimmed == "" || (!strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[")) {
		return false
	}
	lower := strings.ToLower(trimmed)
	return strings.Contains(lower, "\"statement\"") && (strings.Contains(lower, "\"effect\"") || strings.Contains(lower, "\"action\"") || strings.Contains(lower, "\"principal\""))
}

func securityIAMBlastRadius(resource deepResearchResource, actions []string, findingType string, publicAddress string) string {
	actionText := strings.ToLower(strings.Join(actions, " "))
	base := "The attached identity and any downstream resources it can reach."
	if strings.TrimSpace(resource.IAMRole) != "" {
		base = fmt.Sprintf("IAM role %s and any downstream resources it can reach.", resource.IAMRole)
	}
	if publicAddress != "" {
		base = fmt.Sprintf("Public runtime %s plus the permissions and downstream services its identity can reach.", publicAddress)
	}
	if strings.Contains(actionText, "iam:") || strings.Contains(strings.ToLower(findingType), "admin") {
		return base + " This includes delegated identities, role assumption paths, and control-plane changes if not contained."
	}
	if strings.Contains(actionText, "lambda:") || strings.Contains(actionText, "ecs:") || strings.Contains(actionText, "sts:") {
		return base + " This includes launched workloads, assumed roles, and invoked services reachable through the same identity path."
	}
	if strings.Contains(actionText, "s3:") || strings.Contains(actionText, "kms:") || strings.Contains(actionText, "secretsmanager:") {
		return base + " This includes broad data-access paths, keys, and stored secrets exposed by the same policy scope."
	}
	return base
}

func buildSecurityStorageExposureFindings(resources []deepResearchResource) []securityFinding {
	findings := []securityFinding{}
	for _, resource := range resources {
		if !securityResourceLooksStorage(resource) && !securityResourceLooksSnapshot(resource) && !securityResourceLooksSecretStore(resource) {
			continue
		}
		signals := securityResourceAttributeSignals(resource.Attributes)
		publicEvidence := []string{}
		encryptionEvidence := []string{}
		snapshotExposureEvidence := []string{}
		secretStoreExposureEvidence := []string{}
		for _, signal := range signals {
			lowerPath := strings.ToLower(strings.TrimSpace(signal.Path))
			lowerValue := strings.ToLower(strings.TrimSpace(signal.Value))
			if securityStorageSignalShowsPublic(lowerPath, lowerValue) {
				publicEvidence = append(publicEvidence, fmt.Sprintf("%s=%s", signal.Path, signal.Value))
			}
			if securityStorageSignalShowsEncryptionGap(lowerPath, lowerValue) {
				encryptionEvidence = append(encryptionEvidence, fmt.Sprintf("%s=%s", signal.Path, signal.Value))
			}
			if securitySnapshotSignalShowsExposure(lowerPath, lowerValue) {
				snapshotExposureEvidence = append(snapshotExposureEvidence, fmt.Sprintf("%s=%s", signal.Path, signal.Value))
			}
			if securitySecretStoreSignalShowsExposure(lowerPath, lowerValue) {
				secretStoreExposureEvidence = append(secretStoreExposureEvidence, fmt.Sprintf("%s=%s", signal.Path, signal.Value))
			}
		}

		publicEvidence = uniqueNonEmptyStrings(publicEvidence)
		encryptionEvidence = uniqueNonEmptyStrings(encryptionEvidence)
		snapshotExposureEvidence = uniqueNonEmptyStrings(snapshotExposureEvidence)
		secretStoreExposureEvidence = uniqueNonEmptyStrings(secretStoreExposureEvidence)
		if len(publicEvidence) > 5 {
			publicEvidence = publicEvidence[:5]
		}
		if len(encryptionEvidence) > 5 {
			encryptionEvidence = encryptionEvidence[:5]
		}
		if len(snapshotExposureEvidence) > 5 {
			snapshotExposureEvidence = snapshotExposureEvidence[:5]
		}
		if len(secretStoreExposureEvidence) > 5 {
			secretStoreExposureEvidence = secretStoreExposureEvidence[:5]
		}

		if len(publicEvidence) > 0 {
			findings = append(findings, securityFinding{
				ID:           buildDeepResearchFindingID("security-storage-public", resource.ID),
				Severity:     "critical",
				Category:     "misconfiguration",
				Title:        fmt.Sprintf("%s appears publicly readable", deepResearchResourceLabel(resource)),
				Summary:      "Storage-shaped attributes in the estate snapshot include public-read, allUsers, or equivalent public-access signals. Treat that as direct data exposure until verified.",
				ResourceID:   resource.ID,
				ResourceName: resource.Name,
				ResourceType: resource.Type,
				Provider:     inferDeepResearchProvider(resource),
				Region:       resource.Region,
				Evidence:     publicEvidence,
				Questions:    buildSecurityQuestions("misconfiguration", resource.Name, "", false),
			})
		}

		if len(encryptionEvidence) > 0 {
			findings = append(findings, securityFinding{
				ID:           buildDeepResearchFindingID("security-storage-encryption-gap", resource.ID),
				Severity:     "high",
				Category:     "misconfiguration",
				Title:        fmt.Sprintf("%s may lack storage encryption or managed keys", deepResearchResourceLabel(resource)),
				Summary:      "Storage-shaped attributes in the estate snapshot suggest encryption or key-management controls are disabled, absent, or downgraded.",
				ResourceID:   resource.ID,
				ResourceName: resource.Name,
				ResourceType: resource.Type,
				Provider:     inferDeepResearchProvider(resource),
				Region:       resource.Region,
				Evidence:     encryptionEvidence,
				Questions:    buildSecurityQuestions("misconfiguration", resource.Name, "", false),
			})
		}

		if len(snapshotExposureEvidence) > 0 {
			findings = append(findings, securityFinding{
				ID:           buildDeepResearchFindingID("security-snapshot-public", resource.ID),
				Severity:     "critical",
				Category:     "misconfiguration",
				Title:        fmt.Sprintf("%s looks publicly shareable or externally restorable", deepResearchResourceLabel(resource)),
				Summary:      "Snapshot-like attributes suggest this backup or image can be shared, restored, or read outside the intended trust boundary. Treat that as direct data exposure risk.",
				ResourceID:   resource.ID,
				ResourceName: resource.Name,
				ResourceType: resource.Type,
				Provider:     inferDeepResearchProvider(resource),
				Region:       resource.Region,
				Evidence:     snapshotExposureEvidence,
				Questions:    buildSecurityQuestions("misconfiguration", resource.Name, "", false),
			})
		}

		if len(secretStoreExposureEvidence) > 0 {
			findings = append(findings, securityFinding{
				ID:           buildDeepResearchFindingID("security-secret-store-exposure", resource.ID),
				Severity:     "high",
				Category:     "misconfiguration",
				Title:        fmt.Sprintf("%s may expose a secret-management surface", deepResearchResourceLabel(resource)),
				Summary:      "Secret-store attributes suggest public network access, weak auth posture, or a directly reachable management path. Review immediately before assuming stored secrets are protected.",
				ResourceID:   resource.ID,
				ResourceName: resource.Name,
				ResourceType: resource.Type,
				Provider:     inferDeepResearchProvider(resource),
				Region:       resource.Region,
				Evidence:     secretStoreExposureEvidence,
				Questions:    buildSecurityQuestions("misconfiguration", resource.Name, "", false),
			})
		}
	}
	return findings
}

func buildSecurityDetectiveControlFindings(resources []deepResearchResource) []securityFinding {
	findings := []securityFinding{}
	for _, resource := range resources {
		signals := securityResourceAttributeSignals(resource.Attributes)
		evidence := []string{}
		for _, signal := range signals {
			lowerPath := strings.ToLower(strings.TrimSpace(signal.Path))
			lowerValue := strings.ToLower(strings.TrimSpace(signal.Value))
			if securityControlSignalShowsGap(lowerPath, lowerValue) {
				evidence = append(evidence, fmt.Sprintf("%s=%s", signal.Path, signal.Value))
			}
		}
		evidence = uniqueNonEmptyStrings(evidence)
		if len(evidence) == 0 {
			continue
		}
		if len(evidence) > 5 {
			evidence = evidence[:5]
		}
		severity := "medium"
		if deepResearchFirstNonEmptyAttr(resource.Attributes, "publicIp", "publicIpAddress", "natIP", "ipAddress", "publicDns", "publicDNS", "hostname", "domain") != "" || isDeepResearchNetworkType(resource.Type) {
			severity = "high"
		}
		findings = append(findings, securityFinding{
			ID:           buildDeepResearchFindingID("security-detective-control-gap", resource.ID),
			Severity:     severity,
			Category:     "misconfiguration",
			Title:        fmt.Sprintf("%s has weak or disabled detective controls", deepResearchResourceLabel(resource)),
			Summary:      "Resource attributes indicate logging, alerting, WAF, audit, or detector coverage is disabled, missing, or explicitly weakened. That reduces the chance of catching abuse quickly.",
			ResourceID:   resource.ID,
			ResourceName: resource.Name,
			ResourceType: resource.Type,
			Provider:     inferDeepResearchProvider(resource),
			Region:       resource.Region,
			Evidence:     evidence,
			Questions:    buildSecurityQuestions("misconfiguration", resource.Name, "", false),
		})
	}
	return findings
}

func securityProviderLabel(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "aws":
		return "AWS"
	case "gcp":
		return "GCP"
	case "azure":
		return "Azure"
	case "cloudflare":
		return "Cloudflare"
	case "digitalocean":
		return "DigitalOcean"
	case "hetzner":
		return "Hetzner"
	case "k8s":
		return "Kubernetes"
	case "supabase":
		return "Supabase"
	case "vercel":
		return "Vercel"
	case "verda":
		return "Verda"
	case "github":
		return "GitHub"
	case "terraform":
		return "Terraform"
	default:
		return strings.ToUpper(strings.TrimSpace(provider))
	}
}

func summarizeSecurityFindingHeadlines(findings []securityFinding, limit int) []string {
	items := append([]securityFinding(nil), findings...)
	sort.Slice(items, func(i, j int) bool {
		left := securitySeverityRank(items[i].Severity)
		right := securitySeverityRank(items[j].Severity)
		if left != right {
			return left > right
		}
		return items[i].Title < items[j].Title
	})
	lines := []string{}
	for _, finding := range items {
		if len(lines) >= limit {
			break
		}
		lines = append(lines, fmt.Sprintf("[%s] %s", strings.ToUpper(strings.TrimSpace(finding.Severity)), finding.Title))
	}
	return lines
}

func extractSecurityProviderCandidateTargets(entry securityProviderContextLine) []string {
	targets := extractSecurityURLs(entry.Line)
	if len(targets) > 0 {
		return targets
	}
	sectionLower := strings.ToLower(strings.TrimSpace(entry.Section))
	lineLower := strings.ToLower(strings.TrimSpace(entry.Line))
	if strings.Contains(sectionLower, "load balancer") || strings.Contains(sectionLower, "forwarding") || strings.Contains(lineLower, "natip") || strings.Contains(lineLower, "public ip") || strings.Contains(lineLower, "ipaddress") {
		return extractSecurityIPAddresses(entry.Line)
	}
	return nil
}

func extractSecurityURLs(line string) []string {
	matches := securityURLPattern.FindAllString(line, -1)
	urls := make([]string, 0, len(matches))
	for _, match := range matches {
		clean := strings.TrimRight(strings.TrimSpace(match), ").,")
		if clean == "" {
			continue
		}
		urls = append(urls, clean)
	}
	return uniqueNonEmptyStrings(urls)
}

func extractSecurityIPAddresses(line string) []string {
	matches := securityIPv4Pattern.FindAllString(line, -1)
	ips := make([]string, 0, len(matches))
	for _, match := range matches {
		if ip := net.ParseIP(strings.TrimSpace(match)); ip == nil {
			continue
		}
		ips = append(ips, strings.TrimSpace(match))
	}
	return uniqueNonEmptyStrings(ips)
}

func securityProviderContextSubject(line string) string {
	if match := securityNamedFieldPattern.FindStringSubmatch(line); len(match) > 1 {
		return strings.TrimSpace(match[1])
	}
	trimmed := strings.TrimSpace(strings.TrimPrefix(line, "-"))
	if idx := strings.Index(trimmed, ","); idx > 0 {
		candidate := strings.TrimSpace(trimmed[:idx])
		if sep := strings.Index(candidate, ":"); sep > 0 {
			candidate = strings.TrimSpace(candidate[sep+1:])
		}
		return candidate
	}
	fields := strings.Fields(trimmed)
	if len(fields) > 0 {
		return strings.Trim(fields[0], ",")
	}
	return ""
}

func securityGitHubContextSubject(line string) string {
	trimmed := strings.TrimSpace(strings.TrimPrefix(line, "-"))
	if idx := strings.Index(trimmed, " ("); idx > 0 {
		return strings.TrimSpace(trimmed[:idx])
	}
	if idx := strings.Index(trimmed, ","); idx > 0 {
		return strings.TrimSpace(trimmed[:idx])
	}
	return trimmed
}

func securityProviderSectionResourceType(provider string, section string) string {
	lower := strings.ToLower(strings.TrimSpace(section))
	switch {
	case strings.Contains(lower, "repository"):
		return provider + "-repository"
	case strings.Contains(lower, "runner"):
		return provider + "-runner"
	case strings.Contains(lower, "workflow"):
		return provider + "-workflow"
	case strings.Contains(lower, "artifact"):
		return provider + "-artifact"
	case strings.Contains(lower, "firewall"):
		return provider + "-firewall-rule"
	case strings.Contains(lower, "armor") || strings.Contains(lower, "waf") || strings.Contains(lower, "security polic"):
		return provider + "-edge-policy"
	case strings.Contains(lower, "lambda"):
		return "lambda-function"
	case strings.Contains(lower, "cloud run"):
		return "cloud-run-service"
	case strings.Contains(lower, "load balancer") || strings.Contains(lower, "forwarding"):
		return provider + "-load-balancer"
	case strings.Contains(lower, "bucket") || strings.Contains(lower, "storage"):
		return provider + "-bucket"
	case strings.Contains(lower, "role") || strings.Contains(lower, "service account"):
		return provider + "-identity"
	case strings.Contains(lower, "alert"):
		return provider + "-alert-policy"
	case strings.Contains(lower, "logging") || strings.Contains(lower, "activity log"):
		return provider + "-logging"
	default:
		if lower == "" {
			return provider + "-resource"
		}
		return provider + "-" + strings.ReplaceAll(strings.ReplaceAll(lower, " ", "-"), "/", "-")
	}
}

func securityLineHasWorldOpenCIDR(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	return strings.Contains(lower, "0.0.0.0/0") || strings.Contains(lower, "::/0")
}

func securityLineHasSensitivePort(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	for _, port := range []string{"22", "3389", "5432", "3306", "6379", "27017", "9200", "9300", "5601", "6443", "2379", "10250", "389", "25"} {
		if strings.Contains(lower, port) {
			return true
		}
	}
	return false
}

func securityLineShowsNoAuth(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	return strings.Contains(lower, "auth: none") || strings.Contains(lower, "auth type: none") || strings.Contains(lower, "auth none") || strings.Contains(lower, "unauthenticated") || strings.Contains(lower, "allow unauthenticated")
}

func securityLineShowsPublicStorageExposure(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	markers := []string{"public-read", "public-read-write", "allusers", "allauthenticatedusers", "public access block disabled", "allowblobpublicaccess", "publicnetworkaccess enabled", "restorablebyuserids", "createvolumepermissions", "allprojects"}
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func securityLineShowsPrivilegedIdentity(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	markers := []string{"administratoraccess", "roles/owner", "fullaccess", "poweruser", "passrole", "assumerole", "serviceaccountuser", "tokencreator", "workloadidentityuser", "cluster-admin"}
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func securityLineShowsDisabledControl(sectionLower string, lineLower string) bool {
	if !(strings.Contains(sectionLower, "alert") || strings.Contains(sectionLower, "logging") || strings.Contains(sectionLower, "armor") || strings.Contains(sectionLower, "waf") || strings.Contains(sectionLower, "security polic") || strings.Contains(sectionLower, "activity log") || strings.Contains(sectionLower, "cloudtrail") || strings.Contains(sectionLower, "guardduty") || strings.Contains(sectionLower, "security hub") || strings.Contains(sectionLower, "config") || strings.Contains(sectionLower, "audit") || strings.Contains(sectionLower, "flow log")) {
		return false
	}
	return strings.Contains(lineLower, "disabled") || strings.Contains(lineLower, "false") || strings.Contains(lineLower, "not enabled") || lineLower == "[]" || strings.Contains(lineLower, "no ") && (strings.Contains(lineLower, "found") || strings.Contains(lineLower, "configured") || strings.Contains(lineLower, "enabled"))
}

func securityResourceAttributeSignals(attributes map[string]interface{}) []securityAttributeSignal {
	signals := []securityAttributeSignal{}
	collectSecurityAttributeSignals("", attributes, &signals)
	sort.Slice(signals, func(i, j int) bool {
		if signals[i].Path != signals[j].Path {
			return signals[i].Path < signals[j].Path
		}
		return signals[i].Value < signals[j].Value
	})
	return signals
}

func collectSecurityAttributeSignals(prefix string, value interface{}, signals *[]securityAttributeSignal) {
	switch typed := value.(type) {
	case map[string]interface{}:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			next := key
			if prefix != "" {
				next = prefix + "." + key
			}
			collectSecurityAttributeSignals(next, typed[key], signals)
		}
	case []interface{}:
		for index, item := range typed {
			next := fmt.Sprintf("%s[%d]", prefix, index)
			collectSecurityAttributeSignals(next, item, signals)
		}
	case []string:
		for index, item := range typed {
			next := fmt.Sprintf("%s[%d]", prefix, index)
			collectSecurityAttributeSignals(next, item, signals)
		}
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return
		}
		*signals = append(*signals, securityAttributeSignal{Path: prefix, Value: trimmed})
	case bool:
		*signals = append(*signals, securityAttributeSignal{Path: prefix, Value: fmt.Sprintf("%t", typed)})
	case int, int32, int64, float32, float64:
		*signals = append(*signals, securityAttributeSignal{Path: prefix, Value: fmt.Sprint(typed)})
	default:
		text := strings.TrimSpace(fmt.Sprint(typed))
		if text != "" && text != "<nil>" {
			*signals = append(*signals, securityAttributeSignal{Path: prefix, Value: text})
		}
	}
}

func securityAttributePathInteresting(path string) bool {
	lower := strings.ToLower(strings.TrimSpace(path))
	markers := []string{"security", "firewall", "ingress", "egress", "cidr", "source", "allow", "port", "listener", "public", "networksecuritygroup", "nsg", "armor", "waf"}
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func securityResourceLooksStorage(resource deepResearchResource) bool {
	lower := strings.ToLower(strings.TrimSpace(resource.Type))
	markers := []string{"bucket", "storage", "blob", "object", "r2", "spaces", "s3", "gcs"}
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func securityResourceLooksSnapshot(resource deepResearchResource) bool {
	lower := strings.ToLower(strings.TrimSpace(resource.Type))
	markers := []string{"snapshot", "image", "backup", "machineimage", "volume-backup"}
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func securityResourceLooksSecretStore(resource deepResearchResource) bool {
	lower := strings.ToLower(strings.TrimSpace(resource.Type))
	markers := []string{"secret", "keyvault", "key-vault", "secretsmanager", "secretmanager", "parameterstore", "parameter-store", "vault"}
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func securityStorageSignalShowsPublic(path string, value string) bool {
	if strings.Contains(value, "public-read") || strings.Contains(value, "public-read-write") || strings.Contains(value, "allusers") || strings.Contains(value, "allauthenticatedusers") {
		return true
	}
	if strings.Contains(path, "allowblobpublicaccess") && value == "true" {
		return true
	}
	if strings.Contains(path, "publicnetworkaccess") && value == "enabled" {
		return true
	}
	if strings.Contains(path, "public") && (value == "true" || value == "enabled") {
		return true
	}
	if strings.Contains(value, "publicaccessblock=false") || strings.Contains(value, "blockpublicacls=false") || strings.Contains(value, "blockpublicpolicy=false") {
		return true
	}
	return false
}

func securityStorageSignalShowsEncryptionGap(path string, value string) bool {
	if !(strings.Contains(path, "encrypt") || strings.Contains(path, "kms") || strings.Contains(path, "cmek") || strings.Contains(path, "key")) {
		return false
	}
	return value == "false" || value == "disabled" || value == "none" || value == "null"
}

func securitySnapshotSignalShowsExposure(path string, value string) bool {
	if !(strings.Contains(path, "snapshot") || strings.Contains(path, "image") || strings.Contains(path, "backup") || strings.Contains(path, "permission") || strings.Contains(path, "share") || strings.Contains(path, "restorable")) {
		return false
	}
	return value == "true" || value == "enabled" || value == "public" || value == "all" || strings.Contains(value, "allusers") || strings.Contains(value, "allprojects") || strings.Contains(value, "*")
}

func securitySecretStoreSignalShowsExposure(path string, value string) bool {
	if !(strings.Contains(path, "secret") || strings.Contains(path, "vault") || strings.Contains(path, "publicnetworkaccess") || strings.Contains(path, "publicaccess") || strings.Contains(path, "networkaccess")) {
		return false
	}
	if strings.Contains(path, "auth") {
		return value == "none" || value == "disabled"
	}
	return value == "true" || value == "enabled" || value == "public" || strings.Contains(value, "allow") && strings.Contains(path, "public")
}

func securityControlSignalShowsGap(path string, value string) bool {
	markers := []string{"cloudtrail", "guardduty", "securityhub", "security_hub", "config", "audit", "logging", "log", "monitor", "alert", "alarm", "waf", "armor", "securitypolicy", "security_policy", "diagnostic", "flowlog", "flow_log"}
	hasMarker := false
	for _, marker := range markers {
		if strings.Contains(path, marker) {
			hasMarker = true
			break
		}
	}
	if !hasMarker {
		return false
	}
	if value == "false" || value == "disabled" || value == "none" || value == "off" || value == "0" || value == "not_configured" {
		return true
	}
	return strings.Contains(value, "not enabled") || strings.Contains(value, "missing") || strings.Contains(value, "absent")
}
