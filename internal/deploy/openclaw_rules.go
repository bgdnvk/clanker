package deploy

import (
	"regexp"
	"sort"
	"strings"
)

const (
	openClawDOImageBuildCommand   = "docker build -t openclaw:local ."
	openClawDOGatewayComposeCmd   = "docker compose up -d openclaw-gateway"
	openClawDORequiredPortsText   = "22, 18789, 18790"
	openClawDORequiredPortsCIDR   = "22/tcp, 18789/tcp, and 18790/tcp"
	openClawDORequiredBindSetting = "OPENCLAW_GATEWAY_BIND=lan"
)

var (
	openClawGitCloneSoftFailRe    = regexp.MustCompile(`(?im)(^\s*git\s+clone[^\n]*?)\s*\|\|[^\n]*(\s*)$`)
	openClawDockerSetupSoftFailRe = regexp.MustCompile(`(?im)(^\s*\./docker-setup\.sh[^\n]*?)\s*\|\|[^\n]*(\s*)$`)
	openClawCloneURLRe            = regexp.MustCompile(`(?im)^\s*git\s+clone(?:\s+--[^\s]+)*\s+(https?://\S+)`)
	openClawEnvLineRe             = regexp.MustCompile(`(?m)^\s*([A-Z0-9_]+)=(.*)$`)
)

type openClawDOBootstrapSpec struct {
	RepoURL          string
	GatewaySecretKey string
	IncludeAnthropic bool
	IncludeOpenAI    bool
	IncludeGemini    bool
	IncludeDiscord   bool
	IncludeTelegram  bool
}

type openClawDORuntimeSpec struct {
	Bootstrap              openClawDOBootstrapSpec
	HasBootstrap           bool
	HasDockerBuild         bool
	HasComposeUp           bool
	HasDockerRun           bool
	HasOnboarding          bool
	HasHomeExport          bool
	HasBindSetting         bool
	HasGatewaySecret       bool
	HasProviderKey         bool
	LeaksDOAccessToken     bool
	HasComposeBuild        bool
	HasLeakedDoctlFlags    bool
	HasGeneratedGateway    bool
	HasDummySecrets        bool
	HasCloneSoftFail       bool
	HasDockerSetupSoftFail bool
}

type doFirewallRuleSpec struct {
	Protocol string
	Ports    string
	Address  string
}

type doFirewallSpec struct {
	Inbound  []doFirewallRuleSpec
	Outbound []doFirewallRuleSpec
}

func writeLines(b *strings.Builder, lines ...string) {
	if b == nil {
		return
	}
	for _, line := range lines {
		b.WriteString(line)
		if !strings.HasSuffix(line, "\n") {
			b.WriteString("\n")
		}
	}
}

func openClawDODropletPromptNotes() []string {
	return []string{
		"- Build the OpenClaw runtime image directly on the droplet. Do NOT use DOCR for the OpenClaw runtime image.",
		"- The HTTPS front door should be a separate App Platform web service on a DigitalOcean-owned ondigitalocean.app hostname.",
		"- DOCR is allowed only for the tiny App Platform HTTPS proxy image. If building that proxy locally, use docker build context __CLANKER_OPENCLAW_DO_PROXY__.",
		"- DigitalOcean allows only one registry per account/team. Reuse that registry when it already exists, but choose a fresh repository name for the proxy image on each deploy.",
		"- Build the App Platform proxy image for linux/amd64 so App Platform can run it reliably.",
		"- The App Platform proxy should be a web service on port 8080 with env UPSTREAM_URL=http://<DROPLET_IP>:18789 and should produce APP_ID plus HTTPS_URL/APP_URL from DefaultIngress.",
		"- The plan must include both Droplet infrastructure commands and App Platform app creation for the HTTPS proxy.",
		"- Use compute ssh-key import, not compute ssh-key list. Each deploy should use a fresh deployment-scoped SSH key so SSH_KEY_ID comes from the current import step.",
		"- Do NOT use --tag-names on compute firewall create for this flow. Create the firewall rules first, then attach it with compute firewall add-droplets after the droplet exists.",
		"- If using the DigitalOcean Docker 1-click image, open host UFW ports 18789/tcp and 18790/tcp before starting the app.",
		"- After the App Platform app is live, patch gateway.controlUi.allowedOrigins to include the App Platform HTTPS URL, not the droplet public IP.",
		"- Build-on-droplet should provision swap before 'docker build -t openclaw:local .' so the TypeScript build does not get OOM-killed.",
		"- CRITICAL: OpenClaw's docker-compose.yml uses 'image: openclaw:local' (NOT build:).",
		"  The user-data must run '" + openClawDOImageBuildCommand + "' to build the image FIRST,",
		"  then '" + openClawDOGatewayComposeCmd + "' which references that local image.",
		"  Do NOT use 'docker compose build' — it will find nothing to build.",
		"- CRITICAL: Do NOT run 'cloud-init status --wait' inside user-data. User-data already runs inside cloud-init, so that causes a deadlock.",
		"- CRITICAL: Set 'export HOME=/root' before running docker-setup.sh (it uses $HOME internally).",
		"- CRITICAL: Do NOT write DIGITALOCEAN_ACCESS_TOKEN into the OpenClaw .env file.",
		"- CRITICAL: Do NOT use shell fallbacks like 'git clone ... || ...' in user-data. Repository checkout must fail fast.",
		"- .env MUST include " + openClawDORequiredBindSetting + " (required for gateway to accept external connections).",
	}
}

func openClawDODeploymentRequirementLines() []string {
	return []string{
		"- Build the OpenClaw runtime image directly on the droplet. Do NOT use DOCR for the OpenClaw runtime image.",
		"- Create a separate App Platform web service to provide managed HTTPS on an ondigitalocean.app hostname.",
		"- DOCR is allowed only for the tiny App Platform HTTPS proxy image that forwards to the droplet.",
		"- DigitalOcean allows only one registry per account/team. Reuse that registry when required, but use a fresh repository name for the proxy image on each deploy.",
		"- Use compute ssh-key import, not compute ssh-key list. Each deploy should use a fresh deployment-scoped SSH key so SSH_KEY_ID comes from the current import step.",
		"- Build the App Platform proxy image for linux/amd64.",
		"- The App Platform proxy must forward to http://<DROPLET_IP>:18789, listen on port 8080, and produce APP_ID and HTTPS_URL/APP_URL from the default ingress URL.",
		"- Do NOT use --tag-names on compute firewall create. Attach the firewall explicitly with compute firewall add-droplets after compute droplet create.",
		"- Create Cloud Firewall BEFORE OR AFTER creating the Droplet (both work, but before is cleaner).",
		"- If the DigitalOcean Docker 1-click image is used, user-data must also open UFW for 18789/tcp and 18790/tcp because the host firewall blocks them by default.",
		"- After App Platform is live, patch gateway.controlUi.allowedOrigins with the App Platform HTTPS URL so the Control UI runs in a secure browser context.",
		"- User-data should create and enable swap before the local Docker build so the OpenClaw image build can finish on smaller droplets.",
		"- The Droplet user-data script runs at first boot — it must clone the repo, write .env, build with '" + openClawDOImageBuildCommand + "', run onboarding with 'export HOME=/root', and docker compose up.",
		"- Open only " + openClawDORequiredPortsCIDR + " on the droplet firewall; the browser-facing HTTPS endpoint comes from App Platform, not droplet ports 80/443.",
		"- CRITICAL: OpenClaw's docker-compose.yml uses 'image: openclaw:local' — there is no 'build:' in compose. You MUST run '" + openClawDOImageBuildCommand + "' before '" + openClawDOGatewayComposeCmd + "'.",
		"- CRITICAL: Do NOT include 'cloud-init status --wait' in user-data. That waits on itself and hangs forever.",
		"- DO does NOT have IAM roles; app secrets go directly into the .env file written by user-data. Do NOT inject DIGITALOCEAN_ACCESS_TOKEN into that .env file.",
		"- Reserved IP is optional. Do not require it by default because account quota may block deployment.",
	}
}

func openClawDOSkeletonLines() []string {
	return []string{
		"- User-data script should: clone the repo, write .env, " + openClawDOImageBuildCommand + ", docker compose up.",
		"- Add an App Platform web service as the managed HTTPS front door, backed by a tiny proxy image stored in DOCR.",
		"- If the account already has a DOCR registry, reuse that registry and switch only the proxy repository name for this deploy.",
		"- Use compute ssh-key import rather than compute ssh-key list so the deployment gets a fresh SSH key instead of reusing an existing account key.",
		"- Create the firewall without --tag-names and attach it later with compute firewall add-droplets once the droplet ID exists.",
		"- Droplet firewall ports should be " + openClawDORequiredPortsText + " only. Do NOT add 80/443 on the droplet because App Platform owns the public HTTPS endpoint.",
		"- Do NOT write DIGITALOCEAN_ACCESS_TOKEN into the OpenClaw .env file.",
		"- Do NOT use shell fallbacks like 'git clone ... || ...' in user-data; clone failure must fail the deployment.",
	}
}

func openClawDOUserDataRepairLines() []string {
	return []string{
		"- OpenClaw on DigitalOcean builds directly on the droplet. Do NOT switch the OpenClaw runtime itself to DOCR/local push flows.",
		"- Use App Platform as the managed HTTPS front door and patch OpenClaw allowedOrigins to the resulting HTTPS URL.",
		"- DOCR is allowed only for the tiny App Platform HTTPS proxy image.",
		"- If the account already has a DOCR registry, keep that registry and rotate only the repository name used for the proxy image.",
		"- On the Docker 1-click image, open host UFW ports 18789/tcp and 18790/tcp before starting the gateway.",
		"- After the App Platform app is live, patch gateway.controlUi.allowedOrigins to include the App Platform HTTPS URL.",
		"- Create and enable swap before the local docker build so the OpenClaw TypeScript build does not get SIGKILL/OOM-killed.",
		"- Do NOT use shell fallbacks like 'git clone ... || ...' or './docker-setup.sh || ...'. Fail fast on bootstrap errors.",
		"- Keep outer doctl flags outside the script. The user-data line must be just '" + openClawDOGatewayComposeCmd + "', not '--wait' or '--output json'.",
	}
}

func hasOpenClawCloneSoftFail(script string) bool {
	return openClawGitCloneSoftFailRe.MatchString(script)
}

func stripOpenClawCloneSoftFail(script string) (string, bool) {
	fixed := openClawGitCloneSoftFailRe.ReplaceAllString(script, `$1$2`)
	return fixed, fixed != script
}

func hasOpenClawDockerSetupSoftFail(script string) bool {
	return openClawDockerSetupSoftFailRe.MatchString(script)
}

func stripOpenClawDockerSetupSoftFail(script string) (string, bool) {
	fixed := openClawDockerSetupSoftFailRe.ReplaceAllString(script, `$1$2`)
	return fixed, fixed != script
}

func inferOpenClawDOBootstrapSpec(script string) (openClawDOBootstrapSpec, bool) {
	lower := strings.ToLower(script)
	if !strings.Contains(lower, "openclaw") && !strings.Contains(lower, "docker-setup.sh") {
		return openClawDOBootstrapSpec{}, false
	}

	spec := openClawDOBootstrapSpec{
		RepoURL:          "https://github.com/openclaw/openclaw",
		GatewaySecretKey: "OPENCLAW_GATEWAY_TOKEN",
	}
	if m := openClawCloneURLRe.FindStringSubmatch(script); len(m) == 2 {
		spec.RepoURL = strings.TrimSpace(m[1])
	}

	for _, match := range openClawEnvLineRe.FindAllStringSubmatch(script, -1) {
		if len(match) != 3 {
			continue
		}
		key := strings.ToUpper(strings.TrimSpace(match[1]))
		switch key {
		case "OPENCLAW_GATEWAY_PASSWORD":
			spec.GatewaySecretKey = "OPENCLAW_GATEWAY_PASSWORD"
		case "OPENCLAW_GATEWAY_TOKEN":
			if spec.GatewaySecretKey == "" {
				spec.GatewaySecretKey = "OPENCLAW_GATEWAY_TOKEN"
			}
		case "ANTHROPIC_API_KEY":
			spec.IncludeAnthropic = true
		case "OPENAI_API_KEY":
			spec.IncludeOpenAI = true
		case "GEMINI_API_KEY":
			spec.IncludeGemini = true
		case "DISCORD_BOT_TOKEN":
			spec.IncludeDiscord = true
		case "TELEGRAM_BOT_TOKEN":
			spec.IncludeTelegram = true
		}
	}

	if !spec.IncludeAnthropic && !spec.IncludeOpenAI && !spec.IncludeGemini {
		return openClawDOBootstrapSpec{}, false
	}
	if spec.GatewaySecretKey == "" {
		return openClawDOBootstrapSpec{}, false
	}
	return spec, true
}

func inferOpenClawDORuntimeSpec(script string) (openClawDORuntimeSpec, bool) {
	lower := strings.ToLower(script)
	if !strings.Contains(lower, "openclaw") && !strings.Contains(lower, "docker-setup.sh") {
		return openClawDORuntimeSpec{}, false
	}

	bootstrap, hasBootstrap := inferOpenClawDOBootstrapSpec(script)
	return openClawDORuntimeSpec{
		Bootstrap:              bootstrap,
		HasBootstrap:           hasBootstrap,
		HasDockerBuild:         strings.Contains(lower, strings.ToLower(openClawDOImageBuildCommand)),
		HasComposeUp:           strings.Contains(lower, "docker compose up") || strings.Contains(lower, "docker-compose up"),
		HasDockerRun:           strings.Contains(lower, "docker run") && strings.Contains(lower, "openclaw"),
		HasOnboarding:          strings.Contains(lower, "docker-setup.sh") || strings.Contains(lower, "openclaw-cli onboard") || strings.Contains(lower, "openclaw-cli\" onboard"),
		HasHomeExport:          strings.Contains(lower, "export home=/root") || strings.Contains(lower, "home=/root"),
		HasBindSetting:         strings.Contains(lower, strings.ToLower(openClawDORequiredBindSetting)),
		HasGatewaySecret:       strings.Contains(lower, "openclaw_gateway_token") || strings.Contains(lower, "openclaw_gateway_password"),
		HasProviderKey:         strings.Contains(lower, "anthropic_api_key") || strings.Contains(lower, "openai_api_key") || strings.Contains(lower, "gemini_api_key"),
		LeaksDOAccessToken:     strings.Contains(lower, "digitalocean_access_token="),
		HasComposeBuild:        strings.Contains(lower, "docker compose build") || strings.Contains(lower, "docker-compose build"),
		HasLeakedDoctlFlags:    strings.Contains(lower, "docker compose up -d openclaw-gateway --wait") || strings.Contains(lower, "docker-compose up -d openclaw-gateway --wait") || strings.Contains(lower, "docker compose up -d openclaw-gateway --output") || strings.Contains(lower, "docker-compose up -d openclaw-gateway --output"),
		HasGeneratedGateway:    strings.Contains(lower, "openssl rand") || strings.Contains(lower, "gateway_token=$(") || strings.Contains(lower, "openclaw_gateway_token=${gateway_token}") || strings.Contains(lower, "openclaw_gateway_token=$gateway_token"),
		HasDummySecrets:        strings.Contains(lower, "placeholder_replace_me") || strings.Contains(lower, "changeme") || strings.Contains(lower, "replace_me"),
		HasCloneSoftFail:       hasOpenClawCloneSoftFail(script),
		HasDockerSetupSoftFail: hasOpenClawDockerSetupSoftFail(script),
	}, true
}

func hasOpenClawDORuntimeScript(script string) bool {
	_, ok := inferOpenClawDORuntimeSpec(script)
	return ok
}

func renderOpenClawDOBootstrapScript(spec openClawDOBootstrapSpec) string {
	if strings.TrimSpace(spec.RepoURL) == "" {
		spec.RepoURL = "https://github.com/openclaw/openclaw"
	}
	if strings.TrimSpace(spec.GatewaySecretKey) == "" {
		spec.GatewaySecretKey = "OPENCLAW_GATEWAY_TOKEN"
	}

	var b strings.Builder
	writeLines(&b,
		"#!/bin/bash",
		"set -euo pipefail",
		"exec > >(tee /var/log/user-data.log) 2>&1",
		"echo \"[$(date)] Starting user-data script\"",
		"",
		"while fuser /var/lib/dpkg/lock-frontend >/dev/null 2>&1; do sleep 5; done",
		"while fuser /var/lib/apt/lists/lock >/dev/null 2>&1; do sleep 5; done",
		"",
		"apt-get update",
		"apt-get install -y git docker-compose-plugin",
		"if command -v ufw >/dev/null 2>&1; then ufw allow 18789/tcp; ufw allow 18790/tcp; fi",
		"if ! swapon --show | grep -q '/swapfile'; then",
		"  fallocate -l 4G /swapfile || dd if=/dev/zero of=/swapfile bs=1M count=4096",
		"  chmod 600 /swapfile",
		"  mkswap /swapfile",
		"  swapon /swapfile",
		"  grep -q '^/swapfile ' /etc/fstab || echo '/swapfile none swap sw 0 0' >> /etc/fstab",
		"fi",
		"",
		"rm -rf /opt/openclaw",
		"git clone "+spec.RepoURL+" /opt/openclaw",
		"cd /opt/openclaw",
		"",
		"mkdir -p /opt/openclaw/data /opt/openclaw/workspace",
		"chown -R 1000:1000 /opt/openclaw/data /opt/openclaw/workspace",
		"chmod -R 755 /opt/openclaw/data /opt/openclaw/workspace",
		"",
		"cat > /opt/openclaw/.env << 'ENVEOF'",
		"OPENCLAW_CONFIG_DIR=/opt/openclaw/data",
		"OPENCLAW_WORKSPACE_DIR=/opt/openclaw/workspace",
		openClawDORequiredBindSetting,
		spec.GatewaySecretKey+"=<"+spec.GatewaySecretKey+">",
	)
	if spec.IncludeAnthropic {
		writeLines(&b, "ANTHROPIC_API_KEY=<ANTHROPIC_API_KEY>")
	}
	if spec.IncludeOpenAI {
		writeLines(&b, "OPENAI_API_KEY=<OPENAI_API_KEY>")
	}
	if spec.IncludeGemini {
		writeLines(&b, "GEMINI_API_KEY=<GEMINI_API_KEY>")
	}
	if spec.IncludeDiscord {
		writeLines(&b, "DISCORD_BOT_TOKEN=<DISCORD_BOT_TOKEN>")
	}
	if spec.IncludeTelegram {
		writeLines(&b, "TELEGRAM_BOT_TOKEN=<TELEGRAM_BOT_TOKEN>")
	}
	writeLines(&b,
		"ENVEOF",
		"",
		openClawDOImageBuildCommand,
		"",
		"export HOME=/root",
		"chmod +x docker-setup.sh",
		"./docker-setup.sh",
		openClawDOGatewayComposeCmd,
		"",
		"echo \"[$(date)] User-data script completed\"",
	)
	return b.String()
}

func normalizeOpenClawDOBootstrapScript(script string) (string, bool) {
	runtimeSpec, ok := inferOpenClawDORuntimeSpec(script)
	if !ok || !runtimeSpec.HasBootstrap {
		return script, false
	}
	canonical := renderOpenClawDOBootstrapScript(runtimeSpec.Bootstrap)
	return canonical, canonical != strings.TrimSpace(script)
}

func extractDOFirewallSpec(args []string) doFirewallSpec {
	return doFirewallSpec{
		Inbound:  extractDOFirewallRuleSpecs(args, "--inbound-rules"),
		Outbound: extractDOFirewallRuleSpecs(args, "--outbound-rules"),
	}
}

func extractDOFirewallRuleSpecs(args []string, flagName string) []doFirewallRuleSpec {
	rules := make([]doFirewallRuleSpec, 0)
	for i := 0; i < len(args); i++ {
		trimmed := strings.TrimSpace(args[i])
		var value string
		switch {
		case trimmed == flagName && i+1 < len(args):
			value = args[i+1]
			i++
		case strings.HasPrefix(trimmed, flagName+"="):
			value = strings.TrimPrefix(trimmed, flagName+"=")
		default:
			continue
		}
		for _, rawRule := range strings.Fields(value) {
			rules = append(rules, parseDOFirewallRuleSpec(rawRule))
		}
	}
	return rules
}

func parseDOFirewallRuleSpec(rule string) doFirewallRuleSpec {
	out := doFirewallRuleSpec{}
	for _, part := range strings.Split(rule, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), ":")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "protocol":
			out.Protocol = strings.ToLower(strings.TrimSpace(v))
		case "ports":
			out.Ports = strings.TrimSpace(v)
		case "address":
			out.Address = strings.TrimSpace(v)
		}
	}
	return normalizeDOFirewallRuleSpec(out)
}

func normalizeDOFirewallRuleSpec(rule doFirewallRuleSpec) doFirewallRuleSpec {
	rule.Protocol = strings.ToLower(strings.TrimSpace(rule.Protocol))
	rule.Ports = strings.TrimSpace(rule.Ports)
	rule.Address = strings.TrimSpace(rule.Address)
	if rule.Address == "" {
		rule.Address = "0.0.0.0/0"
	}
	return rule
}

func renderDOFirewallRuleSpec(rule doFirewallRuleSpec) string {
	rule = normalizeDOFirewallRuleSpec(rule)
	parts := make([]string, 0, 3)
	if rule.Protocol != "" {
		parts = append(parts, "protocol:"+rule.Protocol)
	}
	if rule.Ports != "" && rule.Protocol != "icmp" {
		parts = append(parts, "ports:"+rule.Ports)
	}
	if rule.Address != "" {
		parts = append(parts, "address:"+rule.Address)
	}
	return strings.Join(parts, ",")
}

func canonicalizeOpenClawDOFirewallArgs(args []string) ([]string, bool) {
	if len(args) < 3 || !strings.EqualFold(strings.TrimSpace(args[0]), "compute") || !strings.EqualFold(strings.TrimSpace(args[1]), "firewall") {
		return args, false
	}
	verb := strings.ToLower(strings.TrimSpace(args[2]))
	if verb != "create" && verb != "update" {
		return args, false
	}
	spec := normalizeOpenClawDOFirewallSpec(extractDOFirewallSpec(args))
	canonical := rewriteDOFirewallArgs(args, spec)
	if len(canonical) != len(args) {
		return canonical, true
	}
	for i := range canonical {
		if canonical[i] != args[i] {
			return canonical, true
		}
	}
	return args, false
}

func normalizeOpenClawDOFirewallSpec(spec doFirewallSpec) doFirewallSpec {
	inbound := make([]doFirewallRuleSpec, 0, len(spec.Inbound)+4)
	inboundSeen := map[string]bool{}
	addInbound := func(rule doFirewallRuleSpec) {
		rule = normalizeDOFirewallRuleSpec(rule)
		if rule.Protocol == "" || rule.Ports == "" {
			return
		}
		if rule.Protocol == "tcp" {
			switch rule.Ports {
			case "80", "443", "8080":
				return
			}
		}
		key := rule.Protocol + "|" + rule.Ports + "|" + rule.Address
		if inboundSeen[key] {
			return
		}
		inboundSeen[key] = true
		inbound = append(inbound, rule)
	}
	for _, required := range []string{"22", "18789", "18790"} {
		addInbound(doFirewallRuleSpec{Protocol: "tcp", Ports: required, Address: "0.0.0.0/0"})
	}
	for _, rule := range spec.Inbound {
		addInbound(rule)
	}
	sort.SliceStable(inbound, func(i, j int) bool {
		return doFirewallRuleSortKey(inbound[i], true) < doFirewallRuleSortKey(inbound[j], true)
	})

	output := make([]doFirewallRuleSpec, 0, len(spec.Outbound)+4)
	outputSeen := map[string]bool{}
	addOutbound := func(rule doFirewallRuleSpec) {
		rule = normalizeDOFirewallRuleSpec(rule)
		if rule.Protocol == "" || rule.Ports == "" {
			return
		}
		key := rule.Protocol + "|" + rule.Ports + "|" + rule.Address
		if outputSeen[key] {
			return
		}
		outputSeen[key] = true
		output = append(output, rule)
	}
	for _, proto := range []string{"tcp", "udp"} {
		addOutbound(doFirewallRuleSpec{Protocol: proto, Ports: "all", Address: "0.0.0.0/0"})
	}
	for _, rule := range spec.Outbound {
		addOutbound(rule)
	}
	sort.SliceStable(output, func(i, j int) bool {
		return doFirewallRuleSortKey(output[i], false) < doFirewallRuleSortKey(output[j], false)
	})

	return doFirewallSpec{Inbound: inbound, Outbound: output}
}

func rewriteDOFirewallArgs(args []string, spec doFirewallSpec) []string {
	cleaned := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		trimmed := strings.TrimSpace(args[i])
		switch {
		case trimmed == "--tag-names" && i+1 < len(args):
			i++
			continue
		case strings.HasPrefix(trimmed, "--tag-names="):
			continue
		case trimmed == "--inbound-rules" && i+1 < len(args):
			i++
			continue
		case strings.HasPrefix(trimmed, "--inbound-rules="):
			continue
		case trimmed == "--outbound-rules" && i+1 < len(args):
			i++
			continue
		case strings.HasPrefix(trimmed, "--outbound-rules="):
			continue
		default:
			cleaned = append(cleaned, args[i])
		}
	}
	insertAt := len(cleaned)
	for i, arg := range cleaned {
		trimmed := strings.TrimSpace(arg)
		if trimmed == "--output" || strings.HasPrefix(trimmed, "--output=") {
			insertAt = i
			break
		}
	}
	prefix := append([]string{}, cleaned[:insertAt]...)
	suffix := append([]string{}, cleaned[insertAt:]...)
	if len(spec.Inbound) > 0 {
		prefix = append(prefix, "--inbound-rules", renderDOFirewallRuleList(spec.Inbound))
	}
	if len(spec.Outbound) > 0 {
		prefix = append(prefix, "--outbound-rules", renderDOFirewallRuleList(spec.Outbound))
	}
	return append(prefix, suffix...)
}

func renderDOFirewallRuleList(rules []doFirewallRuleSpec) string {
	rendered := make([]string, 0, len(rules))
	for _, rule := range rules {
		line := renderDOFirewallRuleSpec(rule)
		if line != "" {
			rendered = append(rendered, line)
		}
	}
	return strings.Join(rendered, " ")
}

func doFirewallSpecHasInboundPort(spec doFirewallSpec, protocol string, port string) bool {
	for _, rule := range spec.Inbound {
		rule = normalizeDOFirewallRuleSpec(rule)
		if rule.Protocol == strings.ToLower(strings.TrimSpace(protocol)) && rule.Ports == strings.TrimSpace(port) {
			return true
		}
	}
	return false
}

func doFirewallSpecHasOutboundAll(spec doFirewallSpec, protocol string) bool {
	for _, rule := range spec.Outbound {
		rule = normalizeDOFirewallRuleSpec(rule)
		if rule.Protocol == strings.ToLower(strings.TrimSpace(protocol)) && (strings.EqualFold(rule.Ports, "all") || rule.Ports == "0") {
			return true
		}
	}
	return false
}

func doFirewallRuleSortKey(rule doFirewallRuleSpec, inbound bool) string {
	rule = normalizeDOFirewallRuleSpec(rule)
	if inbound {
		switch rule.Protocol + "/" + rule.Ports {
		case "tcp/22":
			return "00|" + rule.Address
		case "tcp/18789":
			return "01|" + rule.Address
		case "tcp/18790":
			return "02|" + rule.Address
		}
	} else {
		switch rule.Protocol + "/" + rule.Ports {
		case "tcp/all":
			return "00|" + rule.Address
		case "udp/all":
			return "01|" + rule.Address
		case "icmp/all":
			return "02|" + rule.Address
		}
	}
	return "99|" + rule.Protocol + "|" + rule.Ports + "|" + rule.Address
}
