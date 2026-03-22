package deploy

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/bgdnvk/clanker/internal/maker"
)

// SemanticGraph is the provider/app/runtime graph inferred from an LLM plan
// before final executable command compilation.
type SemanticGraph struct {
	Provider     string              `json:"provider"`
	AppKind      string              `json:"app_kind,omitempty"`
	RuntimeModel string              `json:"runtime_model,omitempty"`
	Nodes        []SemanticGraphNode `json:"nodes"`
}

// SemanticGraphNode is one semantic deployment step. Node payloads are typed
// enough for provider compilation but still broad enough to preserve generic
// repo-driven planning.
type SemanticGraphNode struct {
	ID                 string                          `json:"id"`
	Kind               string                          `json:"kind"`
	Name               string                          `json:"name,omitempty"`
	Reason             string                          `json:"reason,omitempty"`
	DependsOn          []string                        `json:"depends_on,omitempty"`
	Outputs            map[string]string               `json:"outputs,omitempty"`
	Attributes         map[string]string               `json:"attributes,omitempty"`
	SSHKey             *semanticSSHKeySpec             `json:"ssh_key,omitempty"`
	Registry           *semanticRegistrySpec           `json:"registry,omitempty"`
	Droplet            *semanticDropletSpec            `json:"droplet,omitempty"`
	ContainerImage     *semanticContainerImageSpec     `json:"container_image,omitempty"`
	FirewallAttachment *semanticFirewallAttachmentSpec `json:"firewall_attachment,omitempty"`
	Firewall           *doFirewallSpec                 `json:"firewall,omitempty"`
	Bootstrap          *openClawDOBootstrapSpec        `json:"bootstrap,omitempty"`
	AppProxy           *digitalOceanAppPlatformProxy   `json:"app_proxy,omitempty"`
}

type semanticSSHKeySpec struct {
	PublicKeyFile string `json:"public_key_file,omitempty"`
}

type semanticRegistrySpec struct {
	SubscriptionTier string `json:"subscription_tier,omitempty"`
}

type semanticDropletSpec struct {
	Region   string `json:"region,omitempty"`
	Size     string `json:"size,omitempty"`
	Image    string `json:"image,omitempty"`
	SSHKeys  string `json:"ssh_keys,omitempty"`
	UserData string `json:"user_data,omitempty"`
}

type semanticContainerImageSpec struct {
	Platform string `json:"platform,omitempty"`
	Ref      string `json:"ref,omitempty"`
	Context  string `json:"context,omitempty"`
}

type semanticFirewallAttachmentSpec struct {
	FirewallID string `json:"firewall_id,omitempty"`
	DropletIDs string `json:"droplet_ids,omitempty"`
}

type semanticProviderCompiler interface {
	Build(*maker.Plan, RulePackContext) (*SemanticGraph, bool, error)
	Compile(*maker.Plan, *SemanticGraph, RulePackContext) (*maker.Plan, bool, error)
}

// digitalOceanAppPlatformProxy is the typed app-platform representation used by
// the DO compiler to emit the final --spec payload.
type digitalOceanAppPlatformProxy struct {
	AppName          string `json:"app_name,omitempty"`
	Region           string `json:"region,omitempty"`
	ServiceName      string `json:"service_name,omitempty"`
	Registry         string `json:"registry,omitempty"`
	Repository       string `json:"repository,omitempty"`
	Tag              string `json:"tag,omitempty"`
	UpstreamURL      string `json:"upstream_url,omitempty"`
	InstanceSizeSlug string `json:"instance_size_slug,omitempty"`
}

// ApplySemanticGraphCompilation infers a semantic graph from the current LLM
// plan and recompiles it into canonical executable commands when the provider
// path supports it. Unsupported paths intentionally no-op.
func ApplySemanticGraphCompilation(plan *maker.Plan, ctx RulePackContext, logf func(string, ...any)) *maker.Plan {
	if plan == nil {
		return nil
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	graph, ok, err := BuildSemanticGraph(plan, ctx)
	if err != nil {
		logf("[deploy] semantic graph skipped: %v", err)
		return plan
	}
	if !ok || graph == nil {
		return plan
	}
	compiled, changed, err := CompileSemanticGraph(plan, graph, ctx)
	if err != nil {
		logf("[deploy] semantic graph compile skipped: %v", err)
		return plan
	}
	if !changed || compiled == nil {
		return plan
	}
	encoded, _ := json.Marshal(graph)
	const semanticGraphNote = "semantic-graph: canonicalized provider commands from inferred graph"
	if !slices.Contains(compiled.Notes, semanticGraphNote) {
		compiled.Notes = append(compiled.Notes, semanticGraphNote)
	}
	logf("[deploy] semantic graph: compiled %d node(s) into canonical %s plan", len(graph.Nodes), strings.TrimSpace(graph.Provider))
	_ = encoded
	return compiled
}

// BuildSemanticGraph infers a semantic graph from a provider-specific command
// plan while preserving the LLM as the source of deploy intent.
func BuildSemanticGraph(plan *maker.Plan, ctx RulePackContext) (*SemanticGraph, bool, error) {
	if plan == nil {
		return nil, false, nil
	}
	compiler, ok := semanticProviderCompilerFor(ctx.effectivePlanProvider())
	if !ok {
		return nil, false, nil
	}
	return compiler.Build(plan, ctx)
}

// CompileSemanticGraph turns a provider semantic graph back into an executable
// plan. The baseline plan remains the source of metadata and user-facing notes.
func CompileSemanticGraph(baseline *maker.Plan, graph *SemanticGraph, ctx RulePackContext) (*maker.Plan, bool, error) {
	if baseline == nil || graph == nil {
		return baseline, false, nil
	}
	compiler, ok := semanticProviderCompilerFor(strings.ToLower(strings.TrimSpace(graph.Provider)))
	if !ok {
		return baseline, false, nil
	}
	return compiler.Compile(baseline, graph, ctx)
}

func semanticProviderCompilerFor(provider string) (semanticProviderCompiler, bool) {
	compiler, ok := semanticProviderCompilers[strings.ToLower(strings.TrimSpace(provider))]
	return compiler, ok
}

var semanticProviderCompilers = map[string]semanticProviderCompiler{
	"digitalocean": digitalOceanSemanticCompiler{},
}
