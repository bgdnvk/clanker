package deploy

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bgdnvk/clanker/internal/maker"
)

func ApplyOpenClawPlanAutofix(plan *maker.Plan, profile *RepoProfile, deep *DeepAnalysis, logf func(string, ...any)) *maker.Plan {
	if plan == nil || len(plan.Commands) == 0 {
		return plan
	}
	if !IsOpenClawRepo(profile, deep) {
		return plan
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}

	proxyFixed := fixOpenClawDOAppPlatformProxy(plan)
	if proxyFixed > 0 {
		logf("[deploy] openclaw autofix: normalized DigitalOcean App Platform HTTPS proxy flow (%d change(s))", proxyFixed)
	}

	if ensureOpenClawDOHTTPSOriginNote(plan) {
		logf("[deploy] openclaw autofix: documented post-apply allowedOrigins HTTPS patch")
	}

	if bindNarrativeFixed := normalizeOpenClawDOBindSettingNarrative(plan); bindNarrativeFixed > 0 {
		logf("[deploy] openclaw autofix: normalized %d OpenClaw bind-setting note/reason text", bindNarrativeFixed)
	}

	prunedAppsGet := pruneOpenClawDOAppsGet(plan)
	if prunedAppsGet > 0 {
		logf("[deploy] openclaw autofix: removed %d redundant App Platform lookup command(s)", prunedAppsGet)
	}

	prunedChecks := pruneOpenClawDOVerificationCommands(plan)
	if prunedChecks > 0 {
		logf("[deploy] openclaw autofix: removed %d verification-only command(s)", prunedChecks)
	}

	removed := pruneOpenClawExactDuplicates(plan)
	if removed > 0 {
		logf("[deploy] openclaw autofix: removed %d exact duplicate command(s)", removed)
	}

	// Prune semantic SSM duplicates (multiple onboarding / start / health check variants).
	ssmRemoved := pruneOpenClawSemanticSSMDuplicates(plan)
	if ssmRemoved > 0 {
		logf("[deploy] openclaw autofix: removed %d redundant SSM command(s)", ssmRemoved)
	}

	// Prune SSM document cycles (create-doc→send→wait→delete→create-doc-v2...).
	docCycleRemoved := pruneSSMDocumentCycles(plan)
	if docCycleRemoved > 0 {
		logf("[deploy] openclaw autofix: removed %d SSM document-cycle command(s)", docCycleRemoved)
	}

	hasCloudFrontCreate := false
	hasCloudFrontWait := false
	hasCloudFrontIDProduce := false
	hasCloudFrontDomainProduce := false
	hasHTTPSProduce := false
	cloudFrontCreateIdx := -1

	for i := range plan.Commands {
		cmd := &plan.Commands[i]
		if len(cmd.Args) >= 2 {
			svc := strings.ToLower(strings.TrimSpace(cmd.Args[0]))
			op := strings.ToLower(strings.TrimSpace(cmd.Args[1]))
			if svc == "cloudfront" && (op == "create-distribution" || op == "create-distribution-with-tags") {
				hasCloudFrontCreate = true
				if cloudFrontCreateIdx < 0 {
					cloudFrontCreateIdx = i
				}
			}
			if svc == "cloudfront" && op == "wait" && len(cmd.Args) >= 3 && strings.EqualFold(strings.TrimSpace(cmd.Args[2]), "distribution-deployed") {
				hasCloudFrontWait = true
			}
		}

		for k, v := range cmd.Produces {
			ku := strings.ToUpper(strings.TrimSpace(k))
			sv := strings.TrimSpace(v)
			svLower := strings.ToLower(sv)
			switch ku {
			case "CLOUDFRONT_ID", "CF_DISTRIBUTION_ID":
				hasCloudFrontIDProduce = true
			case "CLOUDFRONT_DOMAIN":
				hasCloudFrontDomainProduce = true
			case "HTTPS_URL":
				if strings.HasPrefix(svLower, "https://") {
					hasHTTPSProduce = true
				}
			}
		}
	}

	if hasCloudFrontCreate && cloudFrontCreateIdx >= 0 {
		cmd := &plan.Commands[cloudFrontCreateIdx]
		if cmd.Produces == nil {
			cmd.Produces = map[string]string{}
		}
		if !hasCloudFrontIDProduce {
			cmd.Produces["CLOUDFRONT_ID"] = "$.Distribution.Id"
			hasCloudFrontIDProduce = true
			logf("[deploy] openclaw autofix: added CLOUDFRONT_ID produce mapping")
		}
		if !hasCloudFrontDomainProduce {
			cmd.Produces["CLOUDFRONT_DOMAIN"] = "$.Distribution.DomainName"
			hasCloudFrontDomainProduce = true
			logf("[deploy] openclaw autofix: added CLOUDFRONT_DOMAIN produce mapping")
		}
		if !hasHTTPSProduce {
			cmd.Produces["HTTPS_URL"] = "https://<CLOUDFRONT_DOMAIN>"
			hasHTTPSProduce = true
			logf("[deploy] openclaw autofix: added HTTPS_URL produce mapping")
		}
	}

	if hasCloudFrontCreate && !hasCloudFrontWait && hasCloudFrontIDProduce {
		plan.Commands = append(plan.Commands, maker.Command{
			Args:   []string{"cloudfront", "wait", "distribution-deployed", "--id", "<CLOUDFRONT_ID>"},
			Reason: "Wait for CloudFront distribution deployment to complete before reporting pairing URL",
		})
		logf("[deploy] openclaw autofix: appended missing cloudfront wait distribution-deployed")
	}

	if hasCloudFrontCreate {
		return plan
	}

	logf("[deploy] openclaw autofix: skipped cloudfront patching because create-distribution step is missing")
	return plan
}

const openClawDOHTTPSOriginNote = "Post-apply: patch gateway.controlUi.allowedOrigins with HTTPS_URL on the droplet; the DigitalOcean executor performs this automatically over SSH after the App Platform HTTPS URL is known."
const openClawDOBindSettingNote = "OPENCLAW_GATEWAY_BIND=lan allows the gateway to accept external connections from the App Platform proxy."

func ensureOpenClawDOHTTPSOriginNote(plan *maker.Plan) bool {
	if plan == nil || !strings.EqualFold(strings.TrimSpace(plan.Provider), "digitalocean") {
		return false
	}
	hasDropletCreate := false
	hasAppsCreate := false
	for _, cmd := range plan.Commands {
		if len(cmd.Args) >= 3 && strings.EqualFold(cmd.Args[0], "compute") && strings.EqualFold(cmd.Args[1], "droplet") && strings.EqualFold(cmd.Args[2], "create") {
			hasDropletCreate = true
		}
		if len(cmd.Args) >= 2 && strings.EqualFold(cmd.Args[0], "apps") && strings.EqualFold(cmd.Args[1], "create") {
			hasAppsCreate = true
		}
	}
	if !hasDropletCreate || !hasAppsCreate {
		return false
	}
	for _, note := range plan.Notes {
		if strings.EqualFold(strings.TrimSpace(note), openClawDOHTTPSOriginNote) {
			return false
		}
	}
	plan.Notes = append(plan.Notes, openClawDOHTTPSOriginNote)
	return true
}

func normalizeOpenClawDOBindSettingNarrative(plan *maker.Plan) int {
	if plan == nil || !strings.EqualFold(strings.TrimSpace(plan.Provider), "digitalocean") {
		return 0
	}
	changes := 0
	hasContradictoryBindNote := false
	hasCanonicalBindNote := false
	filteredNotes := make([]string, 0, len(plan.Notes)+1)
	for _, note := range plan.Notes {
		trimmed := strings.TrimSpace(note)
		lower := strings.ToLower(trimmed)
		switch {
		case strings.EqualFold(trimmed, openClawDOBindSettingNote):
			hasCanonicalBindNote = true
			filteredNotes = append(filteredNotes, trimmed)
		case strings.Contains(lower, "openclaw_gateway_bind=0.0.0.0") || strings.Contains(lower, "binds to all interfaces"):
			hasContradictoryBindNote = true
			changes++
		default:
			filteredNotes = append(filteredNotes, note)
		}
	}
	if hasContradictoryBindNote && !hasCanonicalBindNote {
		filteredNotes = append(filteredNotes, openClawDOBindSettingNote)
		changes++
	}
	if len(filteredNotes) != len(plan.Notes) || hasContradictoryBindNote {
		plan.Notes = filteredNotes
	}

	for i := range plan.Commands {
		args := plan.Commands[i].Args
		if len(args) < 3 || !strings.EqualFold(strings.TrimSpace(args[0]), "compute") || !strings.EqualFold(strings.TrimSpace(args[1]), "droplet") || !strings.EqualFold(strings.TrimSpace(args[2]), "create") {
			continue
		}
		reason := plan.Commands[i].Reason
		updated := strings.ReplaceAll(reason, "OPENCLAW_GATEWAY_BIND=0.0.0.0", "OPENCLAW_GATEWAY_BIND=lan")
		if updated != reason {
			plan.Commands[i].Reason = updated
			changes++
		}
	}
	return changes
}

const openClawDOProxyBuildContext = "__CLANKER_OPENCLAW_DO_PROXY__"
const openClawDOProxyPlatform = "linux/amd64"

func fixOpenClawDOAppPlatformProxy(plan *maker.Plan) int {
	if plan == nil || len(plan.Commands) == 0 {
		return 0
	}
	if !strings.EqualFold(strings.TrimSpace(plan.Provider), "digitalocean") {
		return 0
	}

	appsIdx := -1
	registryName := ""
	region := "nyc1"
	changes := 0

	for i := range plan.Commands {
		args := plan.Commands[i].Args
		if len(args) >= 3 && strings.EqualFold(args[0], "compute") && strings.EqualFold(args[1], "droplet") && strings.EqualFold(args[2], "create") {
			if v := strings.TrimSpace(flagValueLocal(args, "--region")); v != "" {
				region = v
			}
		}
		if len(args) >= 2 && strings.EqualFold(args[0], "registry") && strings.EqualFold(args[1], "create") && len(args) >= 3 {
			if registryName == "" {
				registryName = strings.TrimSpace(args[2])
			}
		}
		if len(args) >= 2 && strings.EqualFold(args[0], "apps") && strings.EqualFold(args[1], "create") {
			appsIdx = i
		}
	}
	if appsIdx < 0 {
		return 0
	}

	appName, repositoryName, nextRegistryName, specChanged := normalizeOpenClawDOAppSpec(&plan.Commands[appsIdx], registryName, region)
	if specChanged {
		changes++
	}
	if nextRegistryName != "" {
		registryName = nextRegistryName
	}
	if repositoryName == "" {
		repositoryName = "https-proxy"
	}
	if registryName == "" {
		registryName = deriveOpenClawDORegistryName(appName)
	}

	imageRef := fmt.Sprintf("registry.digitalocean.com/<REGISTRY_NAME>/%s:latest", repositoryName)

	hasRegistryCreate := false
	hasRegistryLogin := false
	hasDockerBuild := false
	hasDockerPush := false
	filtered := make([]maker.Command, 0, len(plan.Commands))
	for i := range plan.Commands {
		args := plan.Commands[i].Args
		if len(args) >= 2 && strings.EqualFold(args[0], "docker") && strings.EqualFold(args[1], "tag") {
			changes++
			continue
		}
		if len(args) >= 2 && strings.EqualFold(args[0], "registry") && strings.EqualFold(args[1], "create") {
			hasRegistryCreate = true
			if len(args) >= 3 && strings.TrimSpace(args[2]) == "" {
				plan.Commands[i].Args[2] = registryName
				changes++
			}
			ensureRegistryCreateProduces(&plan.Commands[i])
		}
		if len(args) >= 2 && strings.EqualFold(args[0], "registry") && strings.EqualFold(args[1], "login") {
			hasRegistryLogin = true
		}
		if len(args) >= 2 && strings.EqualFold(args[0], "docker") && strings.EqualFold(args[1], "build") {
			hasDockerBuild = true
			if normalizeOpenClawProxyDockerBuild(&plan.Commands[i], imageRef) {
				changes++
			}
		}
		if len(args) >= 2 && strings.EqualFold(args[0], "docker") && strings.EqualFold(args[1], "push") {
			hasDockerPush = true
			if normalizeOpenClawProxyDockerPush(&plan.Commands[i], imageRef) {
				changes++
			}
		}
		filtered = append(filtered, plan.Commands[i])
	}
	if len(filtered) != len(plan.Commands) {
		plan.Commands = filtered
	}

	insertAt := len(plan.Commands)
	for i := range plan.Commands {
		args := plan.Commands[i].Args
		if len(args) >= 2 && strings.EqualFold(args[0], "apps") && strings.EqualFold(args[1], "create") {
			insertAt = i
			break
		}
	}
	missing := make([]maker.Command, 0, 4)
	if !hasRegistryCreate {
		missing = append(missing, maker.Command{
			Args:     []string{"registry", "create", registryName, "--subscription-tier", "basic", "--region", region, "--output", "json"},
			Reason:   "Create DigitalOcean Container Registry for the App Platform HTTPS proxy image",
			Produces: map[string]string{"REGISTRY_NAME": "name"},
		})
		changes++
	}
	if !hasRegistryLogin {
		missing = append(missing, maker.Command{
			Args:   []string{"registry", "login"},
			Reason: "Authenticate Docker CLI with DigitalOcean Container Registry for proxy image push",
		})
		changes++
	}
	if !hasDockerBuild {
		missing = append(missing, maker.Command{
			Args:     []string{"docker", "build", "--platform", openClawDOProxyPlatform, "-t", imageRef, openClawDOProxyBuildContext},
			Reason:   "Build the small HTTPS proxy image used by App Platform to front the OpenClaw droplet",
			Produces: map[string]string{"IMAGE_URI": imageRef},
		})
		changes++
	}
	if !hasDockerPush {
		missing = append(missing, maker.Command{
			Args:   []string{"docker", "push", imageRef},
			Reason: "Push the App Platform HTTPS proxy image to DigitalOcean Container Registry",
		})
		changes++
	}
	if len(missing) > 0 {
		updated := make([]maker.Command, 0, len(plan.Commands)+len(missing))
		updated = append(updated, plan.Commands[:insertAt]...)
		updated = append(updated, missing...)
		updated = append(updated, plan.Commands[insertAt:]...)
		plan.Commands = updated
	}

	return changes
}

func pruneOpenClawDOAppsGet(plan *maker.Plan) int {
	if plan == nil || len(plan.Commands) == 0 {
		return 0
	}
	if !strings.EqualFold(strings.TrimSpace(plan.Provider), "digitalocean") {
		return 0
	}
	hasCreateIngress := false
	for _, cmd := range plan.Commands {
		args := cmd.Args
		if len(args) < 2 || !strings.EqualFold(args[0], "apps") || !strings.EqualFold(args[1], "create") {
			continue
		}
		for key, value := range cmd.Produces {
			ku := strings.ToUpper(strings.TrimSpace(key))
			vv := strings.TrimSpace(value)
			if (ku == "APP_URL" || ku == "HTTPS_URL") && strings.EqualFold(vv, "default_ingress") {
				hasCreateIngress = true
				break
			}
		}
	}
	if !hasCreateIngress {
		return 0
	}
	kept := make([]maker.Command, 0, len(plan.Commands))
	removed := 0
	for _, cmd := range plan.Commands {
		args := cmd.Args
		if len(args) >= 2 && strings.EqualFold(args[0], "apps") && strings.EqualFold(args[1], "get") {
			removed++
			continue
		}
		kept = append(kept, cmd)
	}
	if removed > 0 {
		plan.Commands = kept
	}
	return removed
}

func pruneOpenClawDOVerificationCommands(plan *maker.Plan) int {
	if plan == nil || len(plan.Commands) == 0 {
		return 0
	}
	if !strings.EqualFold(strings.TrimSpace(plan.Provider), "digitalocean") {
		return 0
	}
	kept := make([]maker.Command, 0, len(plan.Commands))
	removed := 0
	for _, cmd := range plan.Commands {
		args := cmd.Args
		if len(args) >= 2 && strings.EqualFold(args[0], "apps") && strings.EqualFold(args[1], "list-deployments") {
			removed++
			continue
		}
		if len(args) >= 3 && strings.EqualFold(args[0], "compute") && strings.EqualFold(args[1], "droplet") && strings.EqualFold(args[2], "get") {
			removed++
			continue
		}
		kept = append(kept, cmd)
	}
	if removed > 0 {
		plan.Commands = kept
	}
	return removed
}

func normalizeOpenClawDOAppSpec(cmd *maker.Command, registryName, region string) (string, string, string, bool) {
	if cmd == nil {
		return "", "", registryName, false
	}
	specRaw, specIdx := commandFlagValueLocal(cmd.Args, "--spec")
	if specIdx < 0 {
		return "", "", registryName, false
	}
	var spec map[string]any
	if err := json.Unmarshal([]byte(specRaw), &spec); err != nil {
		return "", "", registryName, false
	}
	changed := false
	appName := stringMapValue(spec, "name")
	if appName == "" {
		appName = "openclaw-proxy"
		spec["name"] = appName
		changed = true
	}
	if strings.TrimSpace(stringMapValue(spec, "region")) == "" {
		if region == "" {
			region = "nyc"
		}
		spec["region"] = mapDORegionToAppPlatform(region)
		changed = true
	}
	registryOut := registryName
	if registryOut == "" {
		registryOut = deriveOpenClawDORegistryName(appName)
	}
	services, ok := spec["services"].([]any)
	if !ok || len(services) == 0 {
		services = []any{map[string]any{}}
		spec["services"] = services
		changed = true
	}
	service, ok := services[0].(map[string]any)
	if !ok {
		service = map[string]any{}
		services[0] = service
		changed = true
	}
	serviceName := stringMapValue(service, "name")
	if serviceName == "" {
		serviceName = "https-proxy"
		service["name"] = serviceName
		changed = true
	}
	if num, ok := service["http_port"].(float64); !ok || int(num) != 8080 {
		service["http_port"] = 8080
		changed = true
	}
	image, ok := service["image"].(map[string]any)
	if !ok {
		image = map[string]any{}
		service["image"] = image
		changed = true
	}
	if _, exists := service["source_dir"]; exists {
		delete(service, "source_dir")
		changed = true
	}
	if stringMapValue(image, "registry_type") != "DOCR" {
		image["registry_type"] = "DOCR"
		changed = true
	}
	if stringMapValue(image, "registry") != "<REGISTRY_NAME>" {
		image["registry"] = "<REGISTRY_NAME>"
		changed = true
	}
	repositoryName := openClawDOProxyRepositoryName
	if normalizedRepo := normalizeOpenClawDORepositoryName(stringMapValue(image, "repository"), registryOut); normalizedRepo != "" && normalizedRepo != repositoryName {
		changed = true
	}
	if stringMapValue(image, "repository") != repositoryName {
		image["repository"] = repositoryName
		changed = true
	}
	if stringMapValue(image, "tag") == "" {
		image["tag"] = "latest"
		changed = true
	}
	if ensureOpenClawProxyHealthCheck(service) {
		changed = true
	}
	if ensureOpenClawProxyEnv(service) {
		changed = true
	}
	if ensureOpenClawProxyProduces(cmd) {
		changed = true
	}
	if changed {
		encoded, err := json.Marshal(spec)
		if err == nil {
			cmd.Args[specIdx] = string(encoded)
		}
	}
	return appName, repositoryName, registryOut, changed
}

func ensureOpenClawProxyHealthCheck(service map[string]any) bool {
	changed := false
	healthCheck, ok := service["health_check"].(map[string]any)
	if !ok {
		healthCheck = map[string]any{}
		service["health_check"] = healthCheck
		changed = true
	}
	if stringMapValue(healthCheck, "http_path") != "/healthz" {
		healthCheck["http_path"] = "/healthz"
		changed = true
	}
	if intMapValue(healthCheck, "initial_delay_seconds") != 30 {
		healthCheck["initial_delay_seconds"] = 30
		changed = true
	}
	if intMapValue(healthCheck, "period_seconds") != 15 {
		healthCheck["period_seconds"] = 15
		changed = true
	}
	return changed
}

func intMapValue(m map[string]any, key string) int {
	if m == nil {
		return 0
	}
	v, ok := m[key]
	if !ok {
		return 0
	}
	switch x := v.(type) {
	case int:
		return x
	case int32:
		return int(x)
	case int64:
		return int(x)
	case float64:
		return int(x)
	default:
		return 0
	}
}

func ensureOpenClawProxyEnv(service map[string]any) bool {
	changed := false
	envs, ok := service["envs"].([]any)
	if !ok {
		envs = []any{}
	}
	hasUpstream := false
	for i := range envs {
		entry, ok := envs[i].(map[string]any)
		if !ok {
			continue
		}
		if strings.EqualFold(stringMapValue(entry, "key"), "UPSTREAM_URL") {
			hasUpstream = true
			if stringMapValue(entry, "value") != "http://<DROPLET_IP>:18789" {
				entry["value"] = "http://<DROPLET_IP>:18789"
				changed = true
			}
			if stringMapValue(entry, "type") == "" {
				entry["type"] = "GENERAL"
				changed = true
			}
			if stringMapValue(entry, "scope") == "" {
				entry["scope"] = "RUN_TIME"
				changed = true
			}
		}
	}
	if !hasUpstream {
		envs = append(envs, map[string]any{"key": "UPSTREAM_URL", "value": "http://<DROPLET_IP>:18789", "type": "GENERAL", "scope": "RUN_TIME"})
		changed = true
	}
	service["envs"] = envs
	return changed
}

func ensureOpenClawProxyProduces(cmd *maker.Command) bool {
	if cmd == nil {
		return false
	}
	changed := false
	if cmd.Produces == nil {
		cmd.Produces = map[string]string{}
	}
	for key, want := range map[string]string{"APP_ID": "id", "APP_URL": "default_ingress", "HTTPS_URL": "default_ingress"} {
		if strings.TrimSpace(cmd.Produces[key]) != want {
			cmd.Produces[key] = want
			changed = true
		}
	}
	return changed
}

func ensureRegistryCreateProduces(cmd *maker.Command) bool {
	if cmd == nil {
		return false
	}
	if cmd.Produces == nil {
		cmd.Produces = map[string]string{"REGISTRY_NAME": "name"}
		return true
	}
	if strings.TrimSpace(cmd.Produces["REGISTRY_NAME"]) == "name" {
		return false
	}
	cmd.Produces["REGISTRY_NAME"] = "name"
	return true
}

func normalizeOpenClawProxyDockerBuild(cmd *maker.Command, imageRef string) bool {
	if cmd == nil {
		return false
	}
	want := []string{"docker", "build", "--platform", openClawDOProxyPlatform, "-t", imageRef, openClawDOProxyBuildContext}
	if strings.Join(cmd.Args, "\x1f") == strings.Join(want, "\x1f") {
		return false
	}
	cmd.Args = want
	if cmd.Produces == nil {
		cmd.Produces = map[string]string{"IMAGE_URI": imageRef}
	} else {
		cmd.Produces["IMAGE_URI"] = imageRef
	}
	return true
}

func normalizeOpenClawProxyDockerPush(cmd *maker.Command, imageRef string) bool {
	if cmd == nil {
		return false
	}
	want := []string{"docker", "push", imageRef}
	if strings.Join(cmd.Args, "\x1f") == strings.Join(want, "\x1f") {
		return false
	}
	cmd.Args = want
	return true
}

func deriveOpenClawDORegistryName(appName string) string {
	name := strings.TrimSpace(strings.ToLower(appName))
	name = strings.TrimSuffix(name, "-proxy")
	name = strings.TrimSuffix(name, "-")
	if name == "" {
		name = "openclaw"
	}
	return name + "-registry"
}

const openClawDOProxyRepositoryName = "https-proxy"

func normalizeOpenClawDORepositoryName(repositoryName string, registryName string) string {
	repositoryName = strings.TrimSpace(repositoryName)
	repositoryName = strings.TrimPrefix(repositoryName, "registry.digitalocean.com/<REGISTRY_NAME>/")
	repositoryName = strings.TrimPrefix(repositoryName, "registry.digitalocean.com/")
	if registryName != "" {
		repositoryName = strings.TrimPrefix(repositoryName, registryName+"/")
	}
	repositoryName = strings.TrimPrefix(repositoryName, "<REGISTRY_NAME>/")
	repositoryName = strings.TrimPrefix(repositoryName, "/")
	return strings.TrimSpace(repositoryName)
}

func commandFlagValueLocal(args []string, flagName string) (string, int) {
	for i := 0; i < len(args); i++ {
		trimmed := strings.TrimSpace(args[i])
		if trimmed == flagName && i+1 < len(args) {
			return args[i+1], i + 1
		}
		if strings.HasPrefix(trimmed, flagName+"=") {
			return strings.TrimPrefix(trimmed, flagName+"="), i
		}
	}
	return "", -1
}

func flagValueLocal(args []string, flagName string) string {
	value, _ := commandFlagValueLocal(args, flagName)
	return value
}

func stringMapValue(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, _ := m[key]
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func mapDORegionToAppPlatform(region string) string {
	region = strings.TrimSpace(strings.ToLower(region))
	if region == "" {
		return "nyc"
	}
	if len(region) >= 3 {
		return region[:3]
	}
	return region
}

func pruneOpenClawExactDuplicates(plan *maker.Plan) int {
	if plan == nil || len(plan.Commands) == 0 {
		return 0
	}
	seen := make(map[string]struct{}, len(plan.Commands))
	filtered := make([]maker.Command, 0, len(plan.Commands))
	removed := 0
	for _, cmd := range plan.Commands {
		sig := openClawCommandSignature(cmd.Args)
		if sig == "" {
			filtered = append(filtered, cmd)
			continue
		}
		if _, ok := seen[sig]; ok {
			removed++
			continue
		}
		seen[sig] = struct{}{}
		filtered = append(filtered, cmd)
	}
	if removed > 0 {
		plan.Commands = filtered
	}
	return removed
}

func openClawCommandSignature(args []string) string {
	if len(args) == 0 {
		return ""
	}
	clean := make([]string, 0, len(args))
	for _, raw := range args {
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		clean = append(clean, v)
	}
	if len(clean) == 0 {
		return ""
	}
	return strings.Join(clean, "\x1f")
}

// pruneOpenClawSemanticSSMDuplicates collapses SSM send-command steps that
// repeat the same intent (onboarding, env-setup, gateway-start, diagnostics).
// For each intent category we keep only the LAST occurrence (the most refined
// version the LLM produced). Non-SSM commands and uncategorised SSM commands
// are never removed.
func pruneOpenClawSemanticSSMDuplicates(plan *maker.Plan) int {
	if plan == nil || len(plan.Commands) == 0 {
		return 0
	}

	type tagged struct {
		cmd      maker.Command
		idx      int
		category string // empty = keep unconditionally
	}

	items := make([]tagged, len(plan.Commands))
	for i, cmd := range plan.Commands {
		items[i] = tagged{cmd: cmd, idx: i, category: classifySSMIntent(cmd.Args)}
	}

	// Find the last index of each non-empty category.
	lastOfCategory := map[string]int{}
	for _, t := range items {
		if t.category != "" {
			lastOfCategory[t.category] = t.idx
		}
	}

	filtered := make([]maker.Command, 0, len(plan.Commands))
	removed := 0
	for _, t := range items {
		if t.category == "" {
			filtered = append(filtered, t.cmd)
			continue
		}
		// Keep only the last of each category.
		if t.idx == lastOfCategory[t.category] {
			filtered = append(filtered, t.cmd)
		} else {
			removed++
		}
	}
	if removed > 0 {
		plan.Commands = filtered
	}
	return removed
}

// classifySSMIntent returns a semantic category for SSM send-command steps.
// Returns "" for non-SSM commands or unrecognised SSM commands.
func classifySSMIntent(args []string) string {
	if len(args) < 4 {
		return ""
	}
	svc := strings.ToLower(strings.TrimSpace(args[0]))
	op := strings.ToLower(strings.TrimSpace(args[1]))
	if svc != "ssm" || op != "send-command" {
		return ""
	}
	script := extractSSMScriptFromArgs(args)
	return classifySSMScript(script)
}

// classifySSMScript returns a semantic category for a shell script
// embedded in an SSM command or document. OpenClaw-specific patterns first,
// then delegates to classifySSMScriptGeneric for project-agnostic fallback.
func classifySSMScript(script string) string {
	if script == "" {
		return ""
	}
	l := strings.ToLower(script)

	// OpenClaw-specific patterns
	hasOnboard := strings.Contains(l, "docker-setup.sh") || strings.Contains(l, "openclaw-cli onboard") || strings.Contains(l, "openclaw-cli\" onboard")
	hasStart := strings.Contains(l, "docker compose up") || strings.Contains(l, "docker-compose up") || (strings.Contains(l, "docker run") && strings.Contains(l, "openclaw"))
	hasConfigOrigins := strings.Contains(l, "openclaw.json") && strings.Contains(l, "allowedorigins")
	hasListInvocations := strings.Contains(l, "list-command-invocations")

	// OpenClaw onboard/start combos take highest priority
	switch {
	case hasOnboard && !hasStart:
		return "ssm-onboard"
	case hasOnboard && hasStart:
		return "ssm-onboard-and-start"
	case hasConfigOrigins && !hasStart && !hasOnboard:
		return "ssm-config-origins"
	case hasListInvocations:
		return "ssm-list-invocations"
	}

	// Fallback to generic classifier (handles clone, start, stop, env, ecr, diag)
	return classifySSMScriptGeneric(script)
}

// extractSSMScriptFromArgs extracts the flattened shell script from
// ssm send-command --parameters. LLM output is non-deterministic so we
// handle every observed format:
//
//	{"commands":["cmd1","cmd2"]}           JSON object
//	commands=["cmd1","cmd2"]               SSM shorthand
//	commands = ["cmd1","cmd2"]             shorthand with spaces
//	["cmd1","cmd2"]                        bare JSON array
//	'{"commands":["cmd1"]}'                outer single-quotes
//	commands=['cmd1','cmd2']               single-quoted array items
//	cmd1                                   bare string (single command)
func extractSSMScriptFromArgs(args []string) string {
	for i := 0; i < len(args); i++ {
		a := strings.TrimSpace(args[i])
		var params string
		if a == "--parameters" && i+1 < len(args) {
			params = strings.TrimSpace(args[i+1])
		} else if strings.HasPrefix(a, "--parameters=") {
			params = strings.TrimSpace(strings.TrimPrefix(a, "--parameters="))
		} else if strings.HasPrefix(a, "--parameters") && strings.Contains(a, "=") {
			params = strings.TrimSpace(a[strings.Index(a, "=")+1:])
		} else {
			continue
		}
		// Strip outer single/double quotes the LLM sometimes wraps around the whole value
		params = strings.TrimSpace(params)
		if len(params) >= 2 {
			if (params[0] == '\'' && params[len(params)-1] == '\'') ||
				(params[0] == '"' && params[len(params)-1] == '"') {
				inner := params[1 : len(params)-1]
				// Only strip if inner looks like valid content
				if strings.Contains(inner, "commands") || strings.HasPrefix(strings.TrimSpace(inner), "[") || strings.HasPrefix(strings.TrimSpace(inner), "{") {
					params = inner
				}
			}
		}

		if cmds := tryExtractCommands(params); len(cmds) > 0 {
			return strings.Join(cmds, "\n")
		}
		return ""
	}
	return ""
}

// tryExtractCommands attempts multiple parsing strategies to extract the
// commands array from an SSM --parameters value.
func tryExtractCommands(params string) []string {
	params = strings.TrimSpace(params)
	if params == "" {
		return nil
	}

	// 1) JSON object: {"commands":["cmd1","cmd2"]}
	var obj struct {
		Commands []string `json:"commands"`
	}
	if json.Unmarshal([]byte(params), &obj) == nil && len(obj.Commands) > 0 {
		return obj.Commands
	}

	// 2) Bare JSON array: ["cmd1","cmd2"]
	var arr []string
	if json.Unmarshal([]byte(params), &arr) == nil && len(arr) > 0 {
		return arr
	}

	// 3) SSM shorthand: commands=["cmd1","cmd2"] or commands = [...]
	if idx := strings.Index(strings.ToLower(params), "commands"); idx >= 0 {
		rest := params[idx+len("commands"):]
		rest = strings.TrimLeft(rest, " \t")
		if len(rest) > 0 && rest[0] == '=' {
			rest = strings.TrimSpace(rest[1:])
			// Try JSON array parse
			var cmds []string
			if json.Unmarshal([]byte(rest), &cmds) == nil && len(cmds) > 0 {
				return cmds
			}
			// Single-quoted items: ['cmd1','cmd2'] → replace ' with " and retry
			if strings.HasPrefix(rest, "[") {
				normalized := singleToDoubleQuotes(rest)
				if json.Unmarshal([]byte(normalized), &cmds) == nil && len(cmds) > 0 {
					return cmds
				}
			}
			// Bare unquoted single value: commands=echo hello
			if !strings.HasPrefix(rest, "[") && !strings.HasPrefix(rest, "{") && rest != "" {
				return []string{rest}
			}
		}
	}

	// 4) Single-quoted JSON object: try replacing single quotes
	if strings.Contains(params, "'") {
		normalized := singleToDoubleQuotes(params)
		if json.Unmarshal([]byte(normalized), &obj) == nil && len(obj.Commands) > 0 {
			return obj.Commands
		}
	}

	// 5) Bare string: treat entire params as a single command (last resort)
	if !strings.HasPrefix(params, "{") && !strings.HasPrefix(params, "[") && !strings.HasPrefix(strings.ToLower(params), "commands") {
		return []string{params}
	}

	return nil
}

// singleToDoubleQuotes naively swaps outer single quotes to double quotes
// in a JSON-like string. Only swaps quotes that appear to delimit string
// values (after [, before ], around : etc.), not quotes inside shell commands.
func singleToDoubleQuotes(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inDouble := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch == '"' {
			inDouble = !inDouble
			b.WriteByte(ch)
		} else if ch == '\'' && !inDouble {
			b.WriteByte('"')
		} else {
			b.WriteByte(ch)
		}
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// SSM document-cycle dedup
// ---------------------------------------------------------------------------

// pruneSSMDocumentCycles collapses repeated SSM document-based execution
// cycles where the LLM creates, runs, then recreates numbered versions
// of the same document (onboard→delete→onboard-v2→delete→start-v3...).
// We classify each doc's content by intent and keep only the LAST cycle.
func pruneSSMDocumentCycles(plan *maker.Plan) int {
	if plan == nil || len(plan.Commands) < 2 {
		return 0
	}

	// Phase 1: index all ssm create-document commands
	type docEntry struct {
		createIdx int
		name      string
		intent    string
	}
	docs := map[string]*docEntry{}
	var docOrder []string
	for i, cmd := range plan.Commands {
		if !isSSMOp(cmd.Args, "create-document") {
			continue
		}
		name := getSSMArgValue(cmd.Args, "--name")
		if name == "" {
			continue
		}
		content := getSSMArgValue(cmd.Args, "--content")
		script := extractScriptFromDocContent(content)
		intent := classifySSMScript(script)
		docs[name] = &docEntry{createIdx: i, name: name, intent: intent}
		docOrder = append(docOrder, name)
	}
	if len(docs) < 2 {
		return 0
	}

	// Phase 2: find last doc name for each intent category
	intentLast := map[string]string{}
	for _, name := range docOrder {
		d := docs[name]
		if d.intent != "" {
			intentLast[d.intent] = name
		}
	}

	// Phase 3: mark earlier cycles for removal
	removedDocNames := map[string]bool{}
	for _, name := range docOrder {
		d := docs[name]
		if d.intent != "" && intentLast[d.intent] != name {
			removedDocNames[name] = true
		}
	}
	if len(removedDocNames) == 0 {
		return 0
	}

	// Track produced placeholder keys from removed send-commands
	removedProducedKeys := map[string]bool{}
	drop := map[int]bool{}

	for i, cmd := range plan.Commands {
		if len(cmd.Args) < 2 {
			continue
		}
		svc := strings.ToLower(strings.TrimSpace(cmd.Args[0]))
		op := strings.ToLower(strings.TrimSpace(cmd.Args[1]))
		if svc != "ssm" {
			continue
		}

		switch op {
		case "create-document":
			name := getSSMArgValue(cmd.Args, "--name")
			if removedDocNames[name] {
				drop[i] = true
			}
		case "send-command":
			name := getSSMArgValue(cmd.Args, "--document-name")
			if removedDocNames[name] {
				drop[i] = true
				for k := range cmd.Produces {
					removedProducedKeys[k] = true
				}
			}
		case "delete-document":
			name := getSSMArgValue(cmd.Args, "--name")
			if removedDocNames[name] {
				drop[i] = true
			}
		case "wait":
			// ssm wait command-executed --command-id <PLACEHOLDER>
			if referencesRemovedKey(cmd.Args, removedProducedKeys) {
				drop[i] = true
			}
		case "get-command-invocation":
			if referencesRemovedKey(cmd.Args, removedProducedKeys) {
				drop[i] = true
			}
		}
	}

	if len(drop) == 0 {
		return 0
	}

	filtered := make([]maker.Command, 0, len(plan.Commands)-len(drop))
	for i, cmd := range plan.Commands {
		if !drop[i] {
			filtered = append(filtered, cmd)
		}
	}
	plan.Commands = filtered
	return len(drop)
}

// isSSMOp checks if a command is ssm <operation>.
func isSSMOp(args []string, operation string) bool {
	if len(args) < 2 {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(args[0]), "ssm") &&
		strings.EqualFold(strings.TrimSpace(args[1]), operation)
}

// getSSMArgValue returns the value following a CLI flag (--name, --content, etc.).
func getSSMArgValue(args []string, flag string) string {
	for i := 0; i < len(args)-1; i++ {
		if strings.TrimSpace(args[i]) == flag {
			return strings.TrimSpace(args[i+1])
		}
	}
	return ""
}

// extractScriptFromDocContent parses an SSM document JSON and returns
// the flattened runCommand array from the first aws:runShellScript step.
func extractScriptFromDocContent(content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	var doc struct {
		MainSteps []struct {
			Inputs struct {
				RunCommand []string `json:"runCommand"`
			} `json:"inputs"`
		} `json:"mainSteps"`
	}
	if json.Unmarshal([]byte(content), &doc) == nil {
		for _, step := range doc.MainSteps {
			if len(step.Inputs.RunCommand) > 0 {
				return strings.Join(step.Inputs.RunCommand, "\n")
			}
		}
	}
	return ""
}

// referencesRemovedKey checks if any arg contains a <PLACEHOLDER> for a removed key.
func referencesRemovedKey(args []string, removedKeys map[string]bool) bool {
	for _, a := range args {
		for k := range removedKeys {
			if strings.Contains(a, "<"+k+">") {
				return true
			}
		}
	}
	return false
}
