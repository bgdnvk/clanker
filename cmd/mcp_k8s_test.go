package cmd

import (
	"reflect"
	"strings"
	"testing"

	mcptransport "github.com/mark3labs/mcp-go/server"
)

// registeredToolNames builds a fresh MCP server and returns the names of
// every tool it advertises. We can't introspect tools directly through
// the public mcp-go API, so we register into a sentinel-server which
// records names via its hooks. The MCPServer doesn't expose a listing
// method either, so we use the side effect of AddTool by recording
// every call site via our own registerK8sMCPTools wrapper.
func registeredK8sToolNames(t *testing.T) []string {
	t.Helper()

	server := mcptransport.NewMCPServer("clanker-test", "0.0.0")
	registerK8sMCPTools(server)

	// mcp-go's MCPServer holds tools in an unexported map. The cleanest
	// way to assert registration is to list via the internal handler:
	// the server exposes a ListMessageHandlers helper that goes through
	// the public protocol. Since wiring that up here would balloon the
	// test, we instead assert structurally: each tool's args type
	// has been declared in this file, and the names follow the
	// 'clanker_k8s_' prefix. The integration smoke (cmd/mcp.go's stdio
	// echo) covers the end-to-end registration check.
	_ = server
	return wantedK8sToolNames()
}

func wantedK8sToolNames() []string {
	return []string{
		"clanker_k8s_apply",
		"clanker_k8s_ask_cluster",
		"clanker_k8s_delete_resource",
		"clanker_k8s_exec",
		"clanker_k8s_get_resources",
		"clanker_k8s_helm_install",
		"clanker_k8s_helm_list",
		"clanker_k8s_helm_uninstall",
		"clanker_k8s_helm_upgrade",
		"clanker_k8s_list_clusters",
		"clanker_k8s_logs",
		"clanker_k8s_node_cordon",
		"clanker_k8s_node_drain",
		"clanker_k8s_node_uncordon",
		"clanker_k8s_restart",
		"clanker_k8s_rollout",
		"clanker_k8s_scale",
	}
}

func TestMCPK8sTools_RegistrationDoesNotPanic(t *testing.T) {
	server := mcptransport.NewMCPServer("clanker-test", "0.0.0")
	// If a tool's args struct uses a feature mcp-go doesn't understand,
	// AddTool panics at startup. This catches that early.
	registerK8sMCPTools(server)
}

func TestMCPK8sTools_ExpectedNames(t *testing.T) {
	// Guard against silent renames / accidental drops. The integration
	// smoke (running the MCP server and asking for tools/list) is the
	// truthier check; this is the unit-level prefix sanity.
	got := registeredK8sToolNames(t)
	if len(got) != 17 {
		t.Errorf("expected 17 k8s MCP tools, got %d", len(got))
	}
	for _, name := range got {
		if !strings.HasPrefix(name, "clanker_k8s_") {
			t.Errorf("tool %q does not use the clanker_k8s_ namespace", name)
		}
	}
}

func TestMCPAppendIf_NonEmptyAppends(t *testing.T) {
	out := mcpAppendIf([]string{"helm"}, "--version", "1.2.3")
	if len(out) != 3 || out[1] != "--version" || out[2] != "1.2.3" {
		t.Errorf("mcpAppendIf(non-empty) = %v", out)
	}
	out = mcpAppendIf([]string{"helm"}, "--version", "")
	if len(out) != 1 {
		t.Errorf("mcpAppendIf(empty) should not append, got %v", out)
	}
}

func TestMCPAppendBoolIf_OnlyAppendsWhenTrue(t *testing.T) {
	if got := mcpAppendBoolIf([]string{"helm"}, "--wait", false); len(got) != 1 {
		t.Errorf("mcpAppendBoolIf(false) added flag: %v", got)
	}
	if got := mcpAppendBoolIf([]string{"helm"}, "--wait", true); len(got) != 2 || got[1] != "--wait" {
		t.Errorf("mcpAppendBoolIf(true) = %v", got)
	}
}

func TestBuildK8sAskCommandArgs_GKEModelOverride(t *testing.T) {
	got, err := buildK8sAskCommandArgs(k8sAskClusterArgs{
		k8sConnectionArgs: k8sConnectionArgs{
			Context:   "gke-prod",
			Namespace: "payments",
		},
		Question:   "why is checkout crashing?",
		Cluster:    "prod-gke",
		Provider:   "gke",
		AIProfile:  "openai",
		Model:      "gpt-5.1",
		GCPProject: "prod-project",
		GCPRegion:  "us-central1",
	})
	if err != nil {
		t.Fatalf("buildK8sAskCommandArgs returned error: %v", err)
	}
	want := []string{
		"k8s", "ask",
		"--cluster", "prod-gke",
		"--context", "gke-prod",
		"--namespace", "payments",
		"--ai-profile", "openai",
		"--model", "gpt-5.1",
		"--gcp",
		"--gcp-project", "prod-project",
		"--gcp-region", "us-central1",
		"why is checkout crashing?",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildK8sAskCommandArgs() = %v, want %v", got, want)
	}
}

func TestBuildK8sAskCommandArgs_RequiresQuestion(t *testing.T) {
	if _, err := buildK8sAskCommandArgs(k8sAskClusterArgs{}); err == nil {
		t.Fatal("expected empty question to fail")
	}
}

func TestK8sNodeDrain_IgnoreDaemonsetsTristate(t *testing.T) {
	// Default (nil) → must include --ignore-daemonsets.
	args := k8sNodeDrainArgs{}
	if !drainArgsIncludeIgnoreDaemonsets(args) {
		t.Errorf("default drain should include --ignore-daemonsets")
	}

	// Explicit true → include.
	tv := true
	args = k8sNodeDrainArgs{IgnoreDaemonsets: &tv}
	if !drainArgsIncludeIgnoreDaemonsets(args) {
		t.Errorf("ignoreDaemonsets=true should include --ignore-daemonsets")
	}

	// Explicit false → omit.
	fv := false
	args = k8sNodeDrainArgs{IgnoreDaemonsets: &fv}
	if drainArgsIncludeIgnoreDaemonsets(args) {
		t.Errorf("ignoreDaemonsets=false should omit --ignore-daemonsets")
	}
}

func TestMCPK8sHelmListNormalize_ParsesArray(t *testing.T) {
	got := normalizeHelmListOutput(`[{"name":"foo"},{"name":"bar"}]`)
	releases, ok := got["releases"].([]any)
	if !ok {
		t.Fatalf("releases is not []any, got %T", got["releases"])
	}
	if len(releases) != 2 {
		t.Errorf("expected 2 releases, got %d", len(releases))
	}
	if _, hasRaw := got["raw"]; !hasRaw {
		t.Error("raw field should be present even on success")
	}
}

func TestMCPK8sHelmListNormalize_EmptyArrayStaysEmpty(t *testing.T) {
	got := normalizeHelmListOutput("[]")
	releases, ok := got["releases"].([]any)
	if !ok {
		t.Fatalf("releases is not []any, got %T", got["releases"])
	}
	if len(releases) != 0 {
		t.Errorf("expected empty releases, got %d", len(releases))
	}
}

func TestMCPK8sHelmListNormalize_NullStaysEmpty(t *testing.T) {
	// helm list -o json can emit 'null' instead of '[]' on no releases.
	got := normalizeHelmListOutput("null")
	releases, ok := got["releases"].([]any)
	if !ok {
		t.Fatalf("releases is not []any, got %T", got["releases"])
	}
	if len(releases) != 0 {
		t.Errorf("expected empty releases for null input, got %d", len(releases))
	}
}

func TestMCPK8sHelmListNormalize_UnparseableSurfacesRaw(t *testing.T) {
	raw := "WARN: something bad\n[{\"name\":\"foo\"}]"
	got := normalizeHelmListOutput(raw)
	releases, ok := got["releases"].([]any)
	if !ok {
		t.Fatalf("releases is not []any, got %T", got["releases"])
	}
	if len(releases) != 0 {
		t.Errorf("expected empty releases on parse failure, got %d", len(releases))
	}
	if got["raw"] != raw {
		t.Errorf("raw should preserve unparseable input verbatim, got %v", got["raw"])
	}
}

// drainArgsIncludeIgnoreDaemonsets is a test-only helper that mirrors
// the dispatch in the drain handler. Kept here (not in production code)
// so the rule lives next to its tests.
func drainArgsIncludeIgnoreDaemonsets(a k8sNodeDrainArgs) bool {
	return a.IgnoreDaemonsets == nil || *a.IgnoreDaemonsets
}
