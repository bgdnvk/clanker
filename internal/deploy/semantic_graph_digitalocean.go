package deploy

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/bgdnvk/clanker/internal/maker"
)

const (
	doNodeSSHKeyImport   = "do_ssh_key_import"
	doNodeFirewallCreate = "do_firewall_create"
	doNodeRegistryCreate = "do_registry_create"
	doNodeDropletCreate  = "do_droplet_create"
	doNodeRegistryLogin  = "do_registry_login"
	doNodeProxyBuild     = "do_proxy_build"
	doNodeProxyPush      = "do_proxy_push"
	doNodeFirewallAttach = "do_firewall_attach"
	doNodeAppsCreate     = "do_apps_create"
)

type digitalOceanSemanticNodeSpec struct {
	ID        string
	Kind      string
	Family    string
	Required  bool
	DependsOn []string
	Infer     func(maker.Command, *SemanticGraphNode) error
	Compile   func(SemanticGraphNode, digitalOceanSemanticCompileContext) (maker.Command, error)
}

type digitalOceanSemanticCompileContext struct {
	RegistryName string
	ProxyRegion  string
}

type digitalOceanSemanticCompiler struct{}

var openClawDOSemanticNodeSpecs = []digitalOceanSemanticNodeSpec{
	{
		ID:       "ssh_key",
		Kind:     doNodeSSHKeyImport,
		Family:   "compute ssh-key import",
		Required: true,
		Infer:    inferDOOpenClawSSHKeyNode,
		Compile:  compileDOOpenClawSSHKeyNode,
	},
	{
		ID:       "firewall",
		Kind:     doNodeFirewallCreate,
		Family:   "compute firewall create",
		Required: true,
		Infer:    inferDOOpenClawFirewallNode,
		Compile:  compileDOOpenClawFirewallNode,
	},
	{
		ID:       "registry",
		Kind:     doNodeRegistryCreate,
		Family:   "registry create",
		Required: true,
		Infer:    inferDOOpenClawRegistryNode,
		Compile:  compileDOOpenClawRegistryNode,
	},
	{
		ID:        "droplet",
		Kind:      doNodeDropletCreate,
		Family:    "compute droplet create",
		Required:  true,
		DependsOn: []string{"ssh_key"},
		Infer:     inferDOOpenClawDropletNode,
		Compile:   compileDOOpenClawDropletNode,
	},
	{
		ID:        "registry_login",
		Kind:      doNodeRegistryLogin,
		Family:    "registry login",
		Required:  true,
		DependsOn: []string{"registry"},
		Infer:     inferDOOpenClawRegistryLoginNode,
		Compile:   compileDOOpenClawRegistryLoginNode,
	},
	{
		ID:        "proxy_build",
		Kind:      doNodeProxyBuild,
		Family:    "docker build",
		Required:  true,
		DependsOn: []string{"registry_login"},
		Infer:     inferDOOpenClawProxyBuildNode,
		Compile:   compileDOOpenClawProxyBuildNode,
	},
	{
		ID:        "proxy_push",
		Kind:      doNodeProxyPush,
		Family:    "docker push",
		Required:  true,
		DependsOn: []string{"proxy_build"},
		Infer:     inferDOOpenClawProxyPushNode,
		Compile:   compileDOOpenClawProxyPushNode,
	},
	{
		ID:        "firewall_attach",
		Kind:      doNodeFirewallAttach,
		Family:    "compute firewall add-droplets",
		Required:  true,
		DependsOn: []string{"firewall", "droplet"},
		Infer:     inferDOOpenClawFirewallAttachNode,
		Compile:   compileDOOpenClawFirewallAttachNode,
	},
	{
		ID:        "app_proxy",
		Kind:      doNodeAppsCreate,
		Family:    "apps create",
		Required:  true,
		DependsOn: []string{"proxy_push", "droplet", "registry"},
		Infer:     inferDOOpenClawAppsNode,
		Compile:   compileDOOpenClawAppsNode,
	},
}

var openClawDOSemanticNodeSpecByFamily = func() map[string]digitalOceanSemanticNodeSpec {
	lookup := make(map[string]digitalOceanSemanticNodeSpec, len(openClawDOSemanticNodeSpecs))
	for _, spec := range openClawDOSemanticNodeSpecs {
		lookup[spec.Family] = spec
	}
	return lookup
}()

var openClawDOSemanticNodeSpecByKind = func() map[string]digitalOceanSemanticNodeSpec {
	lookup := make(map[string]digitalOceanSemanticNodeSpec, len(openClawDOSemanticNodeSpecs))
	for _, spec := range openClawDOSemanticNodeSpecs {
		lookup[spec.Kind] = spec
	}
	return lookup
}()

func openClawDOSemanticNodeSpecForFamily(family string) (digitalOceanSemanticNodeSpec, bool) {
	spec, ok := openClawDOSemanticNodeSpecByFamily[family]
	return spec, ok
}

func openClawDOSemanticNodeSpecForKind(kind string) (digitalOceanSemanticNodeSpec, bool) {
	spec, ok := openClawDOSemanticNodeSpecByKind[kind]
	return spec, ok
}

func (digitalOceanSemanticCompiler) Build(plan *maker.Plan, ctx RulePackContext) (*SemanticGraph, bool, error) {
	return buildDigitalOceanSemanticGraph(plan, ctx)
}

func (digitalOceanSemanticCompiler) Compile(baseline *maker.Plan, graph *SemanticGraph, ctx RulePackContext) (*maker.Plan, bool, error) {
	return compileDigitalOceanSemanticGraph(baseline, graph, ctx)
}

func buildDigitalOceanSemanticGraph(plan *maker.Plan, ctx RulePackContext) (*SemanticGraph, bool, error) {
	if plan == nil || !strings.EqualFold(strings.TrimSpace(plan.Provider), "digitalocean") {
		return nil, false, nil
	}
	if !isOpenClawDigitalOceanPlan(plan) {
		return nil, false, nil
	}

	graph := &SemanticGraph{
		Provider:     "digitalocean",
		AppKind:      "openclaw",
		RuntimeModel: "droplet-compose-app-platform-proxy",
		Nodes:        make([]SemanticGraphNode, 0, len(plan.Commands)),
	}
	nodeByKind := map[string]int{}

	for _, cmd := range plan.Commands {
		spec, ok := openClawDOSemanticNodeSpecForFamily(hydratedCommandFamily(cmd.Args))
		if !ok {
			continue
		}
		node, err := inferOpenClawDONode(spec, cmd)
		if err != nil {
			return nil, false, err
		}
		if idx, exists := nodeByKind[node.Kind]; exists {
			graph.Nodes[idx] = node
			continue
		}
		nodeByKind[node.Kind] = len(graph.Nodes)
		graph.Nodes = append(graph.Nodes, node)
	}

	for _, spec := range openClawDOSemanticNodeSpecs {
		if !spec.Required {
			continue
		}
		if _, ok := nodeByKind[spec.Kind]; !ok {
			return nil, false, nil
		}
	}

	applyDigitalOceanGraphDependencies(graph)
	return graph, true, nil
}

func inferOpenClawDONode(spec digitalOceanSemanticNodeSpec, cmd maker.Command) (SemanticGraphNode, error) {
	outputs := cloneStringMap(cmd.Produces)
	attributes := map[string]string{}
	node := SemanticGraphNode{
		ID:         spec.ID,
		Kind:       spec.Kind,
		Reason:     strings.TrimSpace(cmd.Reason),
		Outputs:    outputs,
		Attributes: attributes,
	}
	if spec.Infer != nil {
		if err := spec.Infer(cmd, &node); err != nil {
			return SemanticGraphNode{}, err
		}
	}
	return node, nil
}

func applyDigitalOceanGraphDependencies(graph *SemanticGraph) {
	if graph == nil {
		return
	}
	for i := range graph.Nodes {
		spec, ok := openClawDOSemanticNodeSpecForKind(graph.Nodes[i].Kind)
		if !ok || len(spec.DependsOn) == 0 {
			continue
		}
		graph.Nodes[i].DependsOn = append([]string{}, spec.DependsOn...)
	}
}

func compileDigitalOceanSemanticGraph(baseline *maker.Plan, graph *SemanticGraph, _ RulePackContext) (*maker.Plan, bool, error) {
	if baseline == nil || graph == nil {
		return baseline, false, nil
	}
	nodes, err := topoSortSemanticGraph(graph)
	if err != nil {
		return nil, false, err
	}
	compiled := &maker.Plan{
		Version:      baseline.Version,
		CreatedAt:    baseline.CreatedAt,
		Provider:     baseline.Provider,
		Question:     baseline.Question,
		Summary:      baseline.Summary,
		Notes:        append([]string{}, baseline.Notes...),
		Capabilities: baseline.Capabilities,
		Commands:     make([]maker.Command, 0, len(nodes)),
	}

	compileCtx := digitalOceanSemanticCompileContext{RegistryName: "<REGISTRY_NAME>", ProxyRegion: "nyc"}
	for _, node := range nodes {
		switch node.Kind {
		case doNodeRegistryCreate:
			if strings.TrimSpace(node.Name) != "" {
				compileCtx.RegistryName = node.Name
			}
		case doNodeDropletCreate:
			if node.Droplet != nil && strings.TrimSpace(node.Droplet.Region) != "" {
				compileCtx.ProxyRegion = mapDORegionToAppPlatform(node.Droplet.Region)
			} else if region := strings.TrimSpace(node.Attributes["region"]); region != "" {
				compileCtx.ProxyRegion = mapDORegionToAppPlatform(region)
			}
		}
	}

	for _, node := range nodes {
		cmd, err := compileDigitalOceanSemanticNode(node, compileCtx)
		if err != nil {
			return nil, false, err
		}
		compiled.Commands = append(compiled.Commands, cmd)
	}

	changed := !semanticPlanEquivalent(baseline, compiled)
	return compiled, changed, nil
}

func compileDigitalOceanSemanticNode(node SemanticGraphNode, compileCtx digitalOceanSemanticCompileContext) (maker.Command, error) {
	spec, ok := openClawDOSemanticNodeSpecForKind(node.Kind)
	if !ok || spec.Compile == nil {
		return maker.Command{}, fmt.Errorf("semantic graph compiler does not support node kind %q", node.Kind)
	}
	return spec.Compile(node, compileCtx)
}

func inferDOOpenClawSSHKeyNode(cmd maker.Command, node *SemanticGraphNode) error {
	node.Name = doPositionalArg(cmd.Args, 3)
	node.SSHKey = &semanticSSHKeySpec{PublicKeyFile: flagValueLocal(cmd.Args, "--public-key-file")}
	if node.Outputs == nil {
		node.Outputs = map[string]string{"SSH_KEY_ID": "id"}
	}
	return nil
}

func inferDOOpenClawFirewallNode(cmd maker.Command, node *SemanticGraphNode) error {
	node.Name = flagValueLocal(cmd.Args, "--name")
	firewallSpec := normalizeOpenClawDOFirewallSpec(extractDOFirewallSpec(cmd.Args))
	node.Firewall = &firewallSpec
	if node.Outputs == nil {
		node.Outputs = map[string]string{"FIREWALL_ID": "id"}
	}
	return nil
}

func inferDOOpenClawRegistryNode(cmd maker.Command, node *SemanticGraphNode) error {
	node.Name = doPositionalArg(cmd.Args, 2)
	node.Registry = &semanticRegistrySpec{SubscriptionTier: strings.TrimSpace(flagValueLocal(cmd.Args, "--subscription-tier"))}
	if node.Outputs == nil {
		node.Outputs = map[string]string{"REGISTRY_NAME": "name"}
	}
	return nil
}

func inferDOOpenClawDropletNode(cmd maker.Command, node *SemanticGraphNode) error {
	node.Name = doPositionalArg(cmd.Args, 3)
	node.Droplet = &semanticDropletSpec{
		Region:   strings.TrimSpace(flagValueLocal(cmd.Args, "--region")),
		Size:     strings.TrimSpace(flagValueLocal(cmd.Args, "--size")),
		Image:    strings.TrimSpace(flagValueLocal(cmd.Args, "--image")),
		SSHKeys:  strings.TrimSpace(flagValueLocal(cmd.Args, "--ssh-keys")),
		UserData: extractDoctlUserDataScript(cmd.Args),
	}
	if bootstrap, ok := inferOpenClawDOBootstrapSpec(node.Droplet.UserData); ok {
		node.Bootstrap = &bootstrap
	}
	if node.Outputs == nil {
		node.Outputs = map[string]string{"DROPLET_ID": "[0].id", "DROPLET_IP": "[0].networks.v4[0].ip_address"}
	}
	return nil
}

func inferDOOpenClawRegistryLoginNode(_ maker.Command, _ *SemanticGraphNode) error {
	return nil
}

func inferDOOpenClawProxyBuildNode(cmd maker.Command, node *SemanticGraphNode) error {
	node.ContainerImage = &semanticContainerImageSpec{
		Platform: dockerBuildPlatformArg(cmd.Args),
		Ref:      dockerBuildTagArg(cmd.Args),
		Context:  dockerBuildContextArg(cmd.Args),
	}
	if node.Outputs == nil && strings.TrimSpace(node.ContainerImage.Ref) != "" {
		node.Outputs = map[string]string{"IMAGE_URI": node.ContainerImage.Ref}
	}
	return nil
}

func inferDOOpenClawProxyPushNode(cmd maker.Command, node *SemanticGraphNode) error {
	node.ContainerImage = &semanticContainerImageSpec{Ref: dockerPushRefArg(cmd.Args)}
	return nil
}

func inferDOOpenClawFirewallAttachNode(cmd maker.Command, node *SemanticGraphNode) error {
	node.FirewallAttachment = &semanticFirewallAttachmentSpec{
		FirewallID: doPositionalArg(cmd.Args, 3),
		DropletIDs: strings.TrimSpace(flagValueLocal(cmd.Args, "--droplet-ids")),
	}
	return nil
}

func inferDOOpenClawAppsNode(cmd maker.Command, node *SemanticGraphNode) error {
	proxy, err := inferDigitalOceanAppProxy(cmd.Args)
	if err != nil {
		return err
	}
	node.AppProxy = proxy
	if node.Outputs == nil {
		node.Outputs = map[string]string{"APP_ID": "id", "APP_URL": "default_ingress", "HTTPS_URL": "default_ingress"}
	}
	return nil
}

func compileDOOpenClawSSHKeyNode(node SemanticGraphNode, _ digitalOceanSemanticCompileContext) (maker.Command, error) {
	publicKeyFile := ""
	if node.SSHKey != nil {
		publicKeyFile = node.SSHKey.PublicKeyFile
	}
	cmd := maker.Command{Args: []string{"compute", "ssh-key", "import", node.Name, "--public-key-file", publicKeyFile, "--output", "json"}, Reason: node.Reason, Produces: cloneStringMap(node.Outputs)}
	if cmd.Produces == nil {
		cmd.Produces = map[string]string{"SSH_KEY_ID": "id"}
	}
	return cmd, nil
}

func compileDOOpenClawFirewallNode(node SemanticGraphNode, _ digitalOceanSemanticCompileContext) (maker.Command, error) {
	cmd := maker.Command{Args: []string{"compute", "firewall", "create", "--name", node.Name, "--output", "json"}, Reason: node.Reason, Produces: cloneStringMap(node.Outputs)}
	if node.Firewall != nil {
		cmd.Args = []string{"compute", "firewall", "create", "--name", node.Name, "--inbound-rules", renderDOFirewallRuleList(node.Firewall.Inbound), "--outbound-rules", renderDOFirewallRuleList(node.Firewall.Outbound), "--output", "json"}
	}
	if canonical, changed := canonicalizeOpenClawDOFirewallArgs(cmd.Args); changed {
		cmd.Args = canonical
	}
	if cmd.Produces == nil {
		cmd.Produces = map[string]string{"FIREWALL_ID": "id"}
	}
	return cmd, nil
}

func compileDOOpenClawRegistryNode(node SemanticGraphNode, _ digitalOceanSemanticCompileContext) (maker.Command, error) {
	tier := ""
	if node.Registry != nil {
		tier = strings.TrimSpace(node.Registry.SubscriptionTier)
	}
	if tier == "" {
		tier = "basic"
	}
	cmd := maker.Command{Args: []string{"registry", "create", node.Name, "--subscription-tier", tier, "--output", "json"}, Reason: node.Reason, Produces: cloneStringMap(node.Outputs)}
	ensureRegistryCreateProduces(&cmd)
	return cmd, nil
}

func compileDOOpenClawDropletNode(node SemanticGraphNode, _ digitalOceanSemanticCompileContext) (maker.Command, error) {
	region := ""
	size := ""
	image := ""
	sshKeys := ""
	userData := ""
	if node.Droplet != nil {
		region = strings.TrimSpace(node.Droplet.Region)
		size = strings.TrimSpace(node.Droplet.Size)
		image = strings.TrimSpace(node.Droplet.Image)
		sshKeys = strings.TrimSpace(node.Droplet.SSHKeys)
		userData = strings.TrimSpace(node.Droplet.UserData)
	}
	if region == "" {
		region = "nyc1"
	}
	if size == "" {
		size = "s-2vcpu-4gb"
	}
	if image == "" {
		image = "docker-20-04"
	}
	if sshKeys == "" {
		sshKeys = "<SSH_KEY_ID>"
	}
	if node.Bootstrap != nil {
		userData = renderOpenClawDOBootstrapScript(*node.Bootstrap)
	}
	cmd := maker.Command{Args: []string{"compute", "droplet", "create", node.Name, "--region", region, "--size", size, "--image", image, "--ssh-keys", sshKeys, "--user-data", userData, "--wait", "--output", "json"}, Reason: node.Reason, Produces: cloneStringMap(node.Outputs)}
	if cmd.Produces == nil {
		cmd.Produces = map[string]string{"DROPLET_ID": "[0].id", "DROPLET_IP": "[0].networks.v4[0].ip_address"}
	}
	return cmd, nil
}

func compileDOOpenClawRegistryLoginNode(node SemanticGraphNode, _ digitalOceanSemanticCompileContext) (maker.Command, error) {
	return maker.Command{Args: []string{"registry", "login"}, Reason: node.Reason}, nil
}

func compileDOOpenClawProxyBuildNode(node SemanticGraphNode, _ digitalOceanSemanticCompileContext) (maker.Command, error) {
	imageRef := ""
	platform := ""
	context := ""
	if node.ContainerImage != nil {
		imageRef = strings.TrimSpace(node.ContainerImage.Ref)
		platform = strings.TrimSpace(node.ContainerImage.Platform)
		context = strings.TrimSpace(node.ContainerImage.Context)
	}
	if imageRef == "" {
		imageRef = fmt.Sprintf("registry.digitalocean.com/<REGISTRY_NAME>/%s:latest", openClawDOProxyRepositoryName)
	}
	if platform == "" {
		platform = openClawDOProxyPlatform
	}
	if context == "" {
		context = openClawDOProxyBuildContext
	}
	cmd := maker.Command{Args: []string{"docker", "build", "--platform", platform, "-t", imageRef, context}, Reason: node.Reason, Produces: cloneStringMap(node.Outputs)}
	normalizeOpenClawProxyDockerBuild(&cmd, imageRef)
	return cmd, nil
}

func compileDOOpenClawProxyPushNode(node SemanticGraphNode, _ digitalOceanSemanticCompileContext) (maker.Command, error) {
	imageRef := ""
	if node.ContainerImage != nil {
		imageRef = strings.TrimSpace(node.ContainerImage.Ref)
	}
	if imageRef == "" {
		imageRef = fmt.Sprintf("registry.digitalocean.com/<REGISTRY_NAME>/%s:latest", openClawDOProxyRepositoryName)
	}
	cmd := maker.Command{Args: []string{"docker", "push", imageRef}, Reason: node.Reason}
	normalizeOpenClawProxyDockerPush(&cmd, imageRef)
	return cmd, nil
}

func compileDOOpenClawFirewallAttachNode(node SemanticGraphNode, _ digitalOceanSemanticCompileContext) (maker.Command, error) {
	firewallID := ""
	dropletIDs := ""
	if node.FirewallAttachment != nil {
		firewallID = strings.TrimSpace(node.FirewallAttachment.FirewallID)
		dropletIDs = strings.TrimSpace(node.FirewallAttachment.DropletIDs)
	}
	if firewallID == "" {
		firewallID = "<FIREWALL_ID>"
	}
	if dropletIDs == "" {
		dropletIDs = "<DROPLET_ID>"
	}
	return maker.Command{Args: []string{"compute", "firewall", "add-droplets", firewallID, "--droplet-ids", dropletIDs}, Reason: node.Reason}, nil
}

func compileDOOpenClawAppsNode(node SemanticGraphNode, compileCtx digitalOceanSemanticCompileContext) (maker.Command, error) {
	cmd := maker.Command{Args: []string{"apps", "create", "--spec", compileDigitalOceanAppSpec(node.AppProxy, compileCtx.RegistryName, compileCtx.ProxyRegion), "--output", "json"}, Reason: node.Reason, Produces: cloneStringMap(node.Outputs)}
	normalizeOpenClawDOAppSpec(&cmd, compileCtx.RegistryName, compileCtx.ProxyRegion)
	ensureOpenClawProxyProduces(&cmd)
	return cmd, nil
}

func inferDigitalOceanAppProxy(args []string) (*digitalOceanAppPlatformProxy, error) {
	specRaw := strings.TrimSpace(flagValueLocal(args, "--spec"))
	if specRaw == "" {
		return nil, fmt.Errorf("apps create missing --spec")
	}
	var spec map[string]any
	if err := json.Unmarshal([]byte(specRaw), &spec); err != nil {
		return nil, fmt.Errorf("parse apps create spec: %w", err)
	}
	proxy := &digitalOceanAppPlatformProxy{
		AppName:          stringMapValue(spec, "name"),
		Region:           stringMapValue(spec, "region"),
		ServiceName:      "proxy",
		Registry:         "<REGISTRY_NAME>",
		Repository:       openClawDOProxyRepositoryName,
		Tag:              "latest",
		UpstreamURL:      fmt.Sprintf("http://<DROPLET_IP>:%d", 18789),
		InstanceSizeSlug: "basic-xxs",
	}
	services, _ := spec["services"].([]any)
	if len(services) > 0 {
		service, _ := services[0].(map[string]any)
		if service != nil {
			if name := stringMapValue(service, "name"); name != "" {
				proxy.ServiceName = name
			}
			if size := stringMapValue(service, "instance_size_slug"); size != "" {
				proxy.InstanceSizeSlug = size
			}
			image, _ := service["image"].(map[string]any)
			if image != nil {
				if registry := stringMapValue(image, "registry"); registry != "" {
					proxy.Registry = registry
				}
				if repo := normalizeOpenClawDORepositoryName(stringMapValue(image, "repository"), proxy.Registry); repo != "" {
					proxy.Repository = repo
				}
				if tag := stringMapValue(image, "tag"); tag != "" {
					proxy.Tag = tag
				}
			}
			envs, _ := service["envs"].([]any)
			for _, raw := range envs {
				entry, _ := raw.(map[string]any)
				if entry == nil {
					continue
				}
				if strings.EqualFold(stringMapValue(entry, "key"), "UPSTREAM_URL") {
					if value := stringMapValue(entry, "value"); value != "" {
						proxy.UpstreamURL = value
					}
				}
			}
		}
	}
	return proxy, nil
}

func compileDigitalOceanAppSpec(proxy *digitalOceanAppPlatformProxy, registryName string, proxyRegion string) string {
	if proxy == nil {
		proxy = &digitalOceanAppPlatformProxy{}
	}
	appName := strings.TrimSpace(proxy.AppName)
	if appName == "" {
		appName = "openclaw-proxy"
	}
	region := strings.TrimSpace(proxy.Region)
	if region == "" {
		region = proxyRegion
	}
	if region == "" {
		region = "nyc"
	}
	serviceName := strings.TrimSpace(proxy.ServiceName)
	if serviceName == "" {
		serviceName = "proxy"
	}
	registry := strings.TrimSpace(proxy.Registry)
	if registry == "" || registry == registryName {
		registry = "<REGISTRY_NAME>"
	}
	repository := strings.TrimSpace(proxy.Repository)
	if repository == "" {
		repository = openClawDOProxyRepositoryName
	}
	tag := strings.TrimSpace(proxy.Tag)
	if tag == "" {
		tag = "latest"
	}
	upstream := strings.TrimSpace(proxy.UpstreamURL)
	if upstream == "" {
		upstream = "http://<DROPLET_IP>:18789"
	}
	instanceSize := strings.TrimSpace(proxy.InstanceSizeSlug)
	if instanceSize == "" {
		instanceSize = "basic-xxs"
	}
	spec := map[string]any{
		"name":   appName,
		"region": region,
		"services": []map[string]any{{
			"name":               serviceName,
			"http_port":          8080,
			"instance_count":     1,
			"instance_size_slug": instanceSize,
			"image": map[string]any{
				"registry":      registry,
				"registry_type": "DOCR",
				"repository":    repository,
				"tag":           tag,
			},
			"envs":         []map[string]any{{"key": "UPSTREAM_URL", "value": upstream, "type": "GENERAL", "scope": "RUN_TIME"}},
			"health_check": map[string]any{"http_path": "/healthz", "initial_delay_seconds": 30, "period_seconds": 15},
		}},
	}
	encoded, _ := json.Marshal(spec)
	return string(encoded)
}

func topoSortSemanticGraph(graph *SemanticGraph) ([]SemanticGraphNode, error) {
	if graph == nil || len(graph.Nodes) == 0 {
		return nil, nil
	}
	nodes := append([]SemanticGraphNode{}, graph.Nodes...)
	byID := make(map[string]SemanticGraphNode, len(nodes))
	inDegree := make(map[string]int, len(nodes))
	edges := make(map[string][]string, len(nodes))
	for _, node := range nodes {
		byID[node.ID] = node
		if _, ok := inDegree[node.ID]; !ok {
			inDegree[node.ID] = 0
		}
	}
	for _, node := range nodes {
		for _, dep := range node.DependsOn {
			if _, ok := byID[dep]; !ok {
				continue
			}
			edges[dep] = append(edges[dep], node.ID)
			inDegree[node.ID]++
		}
	}
	ready := make([]string, 0, len(nodes))
	for _, node := range nodes {
		if inDegree[node.ID] == 0 {
			ready = append(ready, node.ID)
		}
	}
	sort.Strings(ready)
	ordered := make([]SemanticGraphNode, 0, len(nodes))
	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		ordered = append(ordered, byID[id])
		for _, next := range edges[id] {
			inDegree[next]--
			if inDegree[next] == 0 {
				ready = append(ready, next)
				sort.Strings(ready)
			}
		}
	}
	if len(ordered) != len(nodes) {
		return nil, fmt.Errorf("semantic graph has a dependency cycle")
	}
	return ordered, nil
}

func semanticPlanEquivalent(a *maker.Plan, b *maker.Plan) bool {
	if a == nil || b == nil {
		return a == b
	}
	if len(a.Commands) != len(b.Commands) {
		return false
	}
	for i := range a.Commands {
		if strings.Join(a.Commands[i].Args, "\x1f") != strings.Join(b.Commands[i].Args, "\x1f") {
			return false
		}
		if strings.TrimSpace(a.Commands[i].Reason) != strings.TrimSpace(b.Commands[i].Reason) {
			return false
		}
		if !stringMapEqual(a.Commands[i].Produces, b.Commands[i].Produces) {
			return false
		}
	}
	return true
}

func stringMapEqual(a map[string]string, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if strings.TrimSpace(v) != strings.TrimSpace(b[k]) {
			return false
		}
	}
	return true
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func doPositionalArg(args []string, index int) string {
	if index < 0 || index >= len(args) {
		return ""
	}
	return strings.TrimSpace(args[index])
}

func dockerBuildTagArg(args []string) string {
	for i := 0; i < len(args); i++ {
		trimmed := strings.TrimSpace(args[i])
		switch {
		case trimmed == "-t" && i+1 < len(args):
			return strings.TrimSpace(args[i+1])
		case strings.HasPrefix(trimmed, "--tag="):
			return strings.TrimSpace(strings.TrimPrefix(trimmed, "--tag="))
		}
	}
	return ""
}

func dockerBuildPlatformArg(args []string) string {
	for i := 0; i < len(args); i++ {
		trimmed := strings.TrimSpace(args[i])
		switch {
		case trimmed == "--platform" && i+1 < len(args):
			return strings.TrimSpace(args[i+1])
		case strings.HasPrefix(trimmed, "--platform="):
			return strings.TrimSpace(strings.TrimPrefix(trimmed, "--platform="))
		}
	}
	return ""
}

func dockerBuildContextArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return strings.TrimSpace(args[len(args)-1])
}

func dockerPushRefArg(args []string) string {
	if len(args) < 3 {
		return ""
	}
	return strings.TrimSpace(args[2])
}
