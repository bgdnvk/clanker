package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bgdnvk/clanker/internal/k8s"
	"github.com/mark3labs/mcp-go/mcp"
	mcptransport "github.com/mark3labs/mcp-go/server"
	"github.com/spf13/viper"
)

// k8sConnectionArgs is embedded by every K8s tool's args struct so the
// caller can override the kubeconfig and context without us having to
// repeat the doc strings.
type k8sConnectionArgs struct {
	Kubeconfig string `json:"kubeconfig,omitempty" jsonschema:"description=Path to kubeconfig file (defaults to ~/.kube/config)"`
	Context    string `json:"context,omitempty" jsonschema:"description=kubectl context to use (defaults to the active context)"`
	Namespace  string `json:"namespace,omitempty" jsonschema:"description=Kubernetes namespace (defaults to 'default')"`
}

type k8sListClustersArgs struct{}

type k8sGetResourcesArgs struct {
	Cluster    string `json:"cluster,omitempty" jsonschema:"description=Cluster name (uses current kubeconfig context if omitted)"`
	Kubeconfig string `json:"kubeconfig,omitempty" jsonschema:"description=Path to kubeconfig file"`
}

type k8sScaleArgs struct {
	k8sConnectionArgs
	Kind     string `json:"kind" jsonschema:"description=Workload kind: deployment, statefulset, or replicaset,required"`
	Name     string `json:"name" jsonschema:"description=Workload name,required"`
	Replicas int    `json:"replicas" jsonschema:"description=Target replica count (>= 0),required"`
}

type k8sRestartArgs struct {
	k8sConnectionArgs
	Kind string `json:"kind" jsonschema:"description=Workload kind: deployment, statefulset, or daemonset,required"`
	Name string `json:"name" jsonschema:"description=Workload name,required"`
}

type k8sRolloutArgs struct {
	k8sConnectionArgs
	Action     string `json:"action" jsonschema:"description=Rollout action: status, undo, or history,required"`
	Kind       string `json:"kind,omitempty" jsonschema:"description=Workload kind (default: deployment)"`
	Name       string `json:"name" jsonschema:"description=Workload name,required"`
	ToRevision int    `json:"toRevision,omitempty" jsonschema:"description=Revision to roll back to (undo only; 0 = previous)"`
}

type k8sApplyArgs struct {
	k8sConnectionArgs
	Manifest string `json:"manifest" jsonschema:"description=Kubernetes manifest YAML,required"`
}

type k8sDeleteResourceArgs struct {
	k8sConnectionArgs
	Kind           string `json:"kind" jsonschema:"description=Resource kind (pod, deployment, service, configmap, ...),required"`
	Name           string `json:"name" jsonschema:"description=Resource name,required"`
	IgnoreNotFound bool   `json:"ignoreNotFound,omitempty" jsonschema:"description=Treat missing resource as success"`
	Force          bool   `json:"force,omitempty" jsonschema:"description=Force immediate deletion"`
	GracePeriod    int    `json:"gracePeriod,omitempty" jsonschema:"description=Grace period in seconds (-1 = kubectl default; default -1)"`
}

type k8sExecArgs struct {
	k8sConnectionArgs
	Pod       string   `json:"pod" jsonschema:"description=Pod name,required"`
	Container string   `json:"container,omitempty" jsonschema:"description=Container name (if pod has multiple)"`
	Command   []string `json:"command" jsonschema:"description=Command and args to exec (one-shot; output is buffered),required"`
}

type k8sLogsArgs struct {
	k8sConnectionArgs
	Pod       string `json:"pod" jsonschema:"description=Pod name,required"`
	Container string `json:"container,omitempty" jsonschema:"description=Container name"`
	TailLines int    `json:"tailLines,omitempty" jsonschema:"description=Lines from the end (default 200)"`
	Since     string `json:"since,omitempty" jsonschema:"description=Show logs since duration (e.g., 1h, 30m)"`
	Previous  bool   `json:"previous,omitempty" jsonschema:"description=Pull logs from the previously terminated container"`
}

type k8sHelmInstallArgs struct {
	k8sConnectionArgs
	Release         string   `json:"release" jsonschema:"description=Helm release name,required"`
	Chart           string   `json:"chart" jsonschema:"description=Chart reference (repo/name or path or oci://...),required"`
	Version         string   `json:"version,omitempty" jsonschema:"description=Chart version"`
	CreateNamespace bool     `json:"createNamespace,omitempty" jsonschema:"description=Create the namespace if it does not exist"`
	Wait            bool     `json:"wait,omitempty" jsonschema:"description=Wait until all resources are ready"`
	Timeout         string   `json:"timeout,omitempty" jsonschema:"description=Time to wait (e.g., 5m0s)"`
	DryRun          bool     `json:"dryRun,omitempty" jsonschema:"description=Simulate without installing"`
	Set             []string `json:"set,omitempty" jsonschema:"description=Set values (key=val, repeatable)"`
	ValuesFiles     []string `json:"valuesFiles,omitempty" jsonschema:"description=Paths to values YAML files"`
}

type k8sHelmUpgradeArgs struct {
	k8sConnectionArgs
	Release     string   `json:"release" jsonschema:"description=Helm release name,required"`
	Chart       string   `json:"chart" jsonschema:"description=Chart reference,required"`
	Install     bool     `json:"install,omitempty" jsonschema:"description=Install if the release does not exist (helm upgrade --install)"`
	Version     string   `json:"version,omitempty" jsonschema:"description=Chart version"`
	Wait        bool     `json:"wait,omitempty" jsonschema:"description=Wait until all resources are ready"`
	Timeout     string   `json:"timeout,omitempty" jsonschema:"description=Time to wait (e.g., 5m0s)"`
	DryRun      bool     `json:"dryRun,omitempty" jsonschema:"description=Simulate without upgrading"`
	ReuseValues bool     `json:"reuseValues,omitempty" jsonschema:"description=Reuse values from the last release"`
	ResetValues bool     `json:"resetValues,omitempty" jsonschema:"description=Reset values to chart defaults"`
	Force       bool     `json:"force,omitempty" jsonschema:"description=Force resource updates through delete/recreate"`
	Set         []string `json:"set,omitempty" jsonschema:"description=Set values (key=val, repeatable)"`
	ValuesFiles []string `json:"valuesFiles,omitempty" jsonschema:"description=Paths to values YAML files"`
}

type k8sHelmListArgs struct {
	k8sConnectionArgs
	AllNamespaces bool `json:"allNamespaces,omitempty" jsonschema:"description=List releases in every namespace"`
}

type k8sHelmUninstallArgs struct {
	k8sConnectionArgs
	Release     string `json:"release" jsonschema:"description=Release name,required"`
	KeepHistory bool   `json:"keepHistory,omitempty" jsonschema:"description=Retain release history"`
	DryRun      bool   `json:"dryRun,omitempty" jsonschema:"description=Simulate without uninstalling"`
	Timeout     string `json:"timeout,omitempty" jsonschema:"description=Time to wait (e.g., 5m0s)"`
}

type k8sNodeArgs struct {
	Kubeconfig string `json:"kubeconfig,omitempty" jsonschema:"description=Path to kubeconfig file"`
	Context    string `json:"context,omitempty" jsonschema:"description=kubectl context"`
	Node       string `json:"node" jsonschema:"description=Node name,required"`
}

type k8sNodeDrainArgs struct {
	k8sNodeArgs
	Force              bool   `json:"force,omitempty" jsonschema:"description=Force eviction of pods not managed by a controller"`
	IgnoreDaemonsets   *bool  `json:"ignoreDaemonsets,omitempty" jsonschema:"description=Ignore DaemonSet-managed pods (defaults to true; pass false to require none exist)"`
	DeleteEmptyDirData bool   `json:"deleteEmptyDirData,omitempty" jsonschema:"description=Allow eviction of pods using emptyDir volumes"`
	GracePeriod        int    `json:"gracePeriod,omitempty" jsonschema:"description=Grace period in seconds (-1 = pod default; default -1)"`
	Timeout            string `json:"timeout,omitempty" jsonschema:"description=Time to wait before giving up"`
}

// buildMCPK8sClient resolves a *k8s.Client from MCP-supplied connection
// args, falling back to ~/.kube/config and the active context if the
// caller didn't override anything.
func buildMCPK8sClient(conn k8sConnectionArgs) *k8s.Client {
	debug := viper.GetBool("debug")
	kubeconfig := strings.TrimSpace(conn.Kubeconfig)
	if kubeconfig == "" {
		kubeconfig = getKubeconfigPath()
	}
	client := k8s.NewClient(kubeconfig, strings.TrimSpace(conn.Context), debug)
	if strings.TrimSpace(conn.Namespace) != "" {
		client.SetNamespace(conn.Namespace)
	}
	return client
}

// nsForCall returns the namespace to pass into Client.RunWithNamespace.
// Empty means "use the client default" (which buildMCPK8sClient already
// aligned with conn.Namespace).
func nsForCall(conn k8sConnectionArgs) string {
	return strings.TrimSpace(conn.Namespace)
}

// mcpAppendIf adds (flag, value) when value is non-empty. Kept local to
// this file so we don't depend on helpers that live on unmerged feature
// branches.
func mcpAppendIf(args []string, flag, value string) []string {
	if value == "" {
		return args
	}
	return append(args, flag, value)
}

func mcpAppendBoolIf(args []string, flag string, v bool) []string {
	if !v {
		return args
	}
	return append(args, flag)
}

// registerK8sMCPTools adds every native Kubernetes tool to the MCP server.
// Called from newClankerMCPServer so the registration order remains
// auditable in one place.
func registerK8sMCPTools(server *mcptransport.MCPServer) {
	// ---- Discovery / read-only ----

	server.AddTool(
		mcp.NewTool(
			"clanker_k8s_list_clusters",
			mcp.WithDescription("List all Kubernetes clusters discoverable across the configured providers (EKS, GKE, AKS, kubeadm, and existing kubeconfig contexts)."),
			mcp.WithInputSchema[k8sListClustersArgs](),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, _ k8sListClustersArgs) (*mcp.CallToolResult, error) {
			providerCtx := getK8sAgentWithAvailableProviders()
			discovered, errs := listDiscoveredK8sClusters(ctx, providerCtx)
			out := map[string]any{
				"clusters": discovered,
			}
			if len(errs) > 0 {
				providerErrors := make(map[string]string, len(errs))
				for k, v := range errs {
					providerErrors[string(k)] = v.Error()
				}
				out["providerErrors"] = providerErrors
			}
			return mcp.NewToolResultJSON(out)
		}),
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_k8s_get_resources",
			mcp.WithDescription("Fetch all Kubernetes resources (nodes, pods, services, PVs, ConfigMaps, ingresses) on the active or named cluster. Returns the same shape as 'clanker k8s resources'."),
			mcp.WithInputSchema[k8sGetResourcesArgs](),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args k8sGetResourcesArgs) (*mcp.CallToolResult, error) {
			debug := viper.GetBool("debug")
			kubeconfig := strings.TrimSpace(args.Kubeconfig)
			if kubeconfig == "" {
				kubeconfig = getKubeconfigPath()
			}
			if args.Cluster == "" {
				resources, err := getResourcesFromContext(ctx, "", kubeconfig, "", debug)
				if err != nil {
					return mcp.NewToolResultError(err.Error()), nil
				}
				return mcp.NewToolResultJSON(resources)
			}
			providerCtx := getK8sAgentWithAvailableProviders()
			resources, err := getNamedClusterResources(ctx, providerCtx, args.Cluster, debug)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultJSON(resources)
		}),
	)

	// ---- Mutations ----

	server.AddTool(
		mcp.NewTool(
			"clanker_k8s_scale",
			mcp.WithDescription("Scale a Deployment, StatefulSet, or ReplicaSet to a target replica count. Equivalent to 'kubectl scale <kind>/<name> --replicas N'."),
			mcp.WithInputSchema[k8sScaleArgs](),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args k8sScaleArgs) (*mcp.CallToolResult, error) {
			if args.Replicas < 0 {
				return mcp.NewToolResultError("replicas must be >= 0"), nil
			}
			client := buildMCPK8sClient(args.k8sConnectionArgs)
			output, err := client.Scale(ctx, args.Kind, args.Name, nsForCall(args.k8sConnectionArgs), args.Replicas)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultJSON(map[string]any{"output": output})
		}),
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_k8s_restart",
			mcp.WithDescription("Trigger a rolling restart of a Deployment, StatefulSet, or DaemonSet via 'kubectl rollout restart'."),
			mcp.WithInputSchema[k8sRestartArgs](),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args k8sRestartArgs) (*mcp.CallToolResult, error) {
			client := buildMCPK8sClient(args.k8sConnectionArgs)
			output, err := client.Rollout(ctx, "restart", args.Kind, args.Name, nsForCall(args.k8sConnectionArgs))
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultJSON(map[string]any{"output": output})
		}),
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_k8s_rollout",
			mcp.WithDescription("Inspect or undo a rollout. action=status returns the rollout status; action=history returns revision history; action=undo rolls back to the previous revision (or toRevision if specified)."),
			mcp.WithInputSchema[k8sRolloutArgs](),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args k8sRolloutArgs) (*mcp.CallToolResult, error) {
			action := strings.ToLower(strings.TrimSpace(args.Action))
			switch action {
			case "status", "history", "undo":
			default:
				return mcp.NewToolResultError(fmt.Sprintf("unsupported rollout action %q (use status, history, or undo)", args.Action)), nil
			}
			kind := strings.TrimSpace(args.Kind)
			if kind == "" {
				kind = "deployment"
			}
			client := buildMCPK8sClient(args.k8sConnectionArgs)
			ns := nsForCall(args.k8sConnectionArgs)
			if action == "undo" && args.ToRevision > 0 {
				output, err := client.RunWithNamespace(ctx, ns, "rollout", "undo", kind, args.Name, "--to-revision", fmt.Sprintf("%d", args.ToRevision))
				if err != nil {
					return mcp.NewToolResultError(err.Error()), nil
				}
				return mcp.NewToolResultJSON(map[string]any{"output": output})
			}
			output, err := client.Rollout(ctx, action, kind, args.Name, ns)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultJSON(map[string]any{"output": output})
		}),
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_k8s_apply",
			mcp.WithDescription("Apply a Kubernetes manifest to the active context. Server-side dry-run lands in a follow-up once Client.ApplyDryRunServer is merged."),
			mcp.WithInputSchema[k8sApplyArgs](),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args k8sApplyArgs) (*mcp.CallToolResult, error) {
			if strings.TrimSpace(args.Manifest) == "" {
				return mcp.NewToolResultError("manifest is required"), nil
			}
			client := buildMCPK8sClient(args.k8sConnectionArgs)
			output, err := client.Apply(ctx, args.Manifest, nsForCall(args.k8sConnectionArgs))
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultJSON(map[string]any{"output": output})
		}),
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_k8s_delete_resource",
			mcp.WithDescription("Delete a Kubernetes resource on the active context. Use --force/grace-period=0 for stuck pods; ignoreNotFound makes the call idempotent."),
			mcp.WithInputSchema[k8sDeleteResourceArgs](),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args k8sDeleteResourceArgs) (*mcp.CallToolResult, error) {
			client := buildMCPK8sClient(args.k8sConnectionArgs)
			ns := nsForCall(args.k8sConnectionArgs)
			deleteArgs := []string{"delete", args.Kind, args.Name}
			if args.IgnoreNotFound {
				deleteArgs = append(deleteArgs, "--ignore-not-found")
			}
			if args.Force {
				deleteArgs = append(deleteArgs, "--force")
			}
			if args.GracePeriod >= 0 {
				deleteArgs = append(deleteArgs, fmt.Sprintf("--grace-period=%d", args.GracePeriod))
			}
			output, err := client.RunWithNamespace(ctx, ns, deleteArgs...)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultJSON(map[string]any{"output": output})
		}),
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_k8s_exec",
			mcp.WithDescription("Execute a one-shot command in a pod. Output is buffered; interactive sessions land on a WebSocket endpoint in the cloud backend (Phase 2)."),
			mcp.WithInputSchema[k8sExecArgs](),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args k8sExecArgs) (*mcp.CallToolResult, error) {
			if len(args.Command) == 0 {
				return mcp.NewToolResultError("command is required"), nil
			}
			client := buildMCPK8sClient(args.k8sConnectionArgs)
			ns := nsForCall(args.k8sConnectionArgs)
			kubectlArgs := []string{"exec", args.Pod}
			if strings.TrimSpace(args.Container) != "" {
				kubectlArgs = append(kubectlArgs, "-c", args.Container)
			}
			kubectlArgs = append(kubectlArgs, "--")
			kubectlArgs = append(kubectlArgs, args.Command...)
			output, err := client.RunWithNamespace(ctx, ns, kubectlArgs...)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultJSON(map[string]any{"output": output})
		}),
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_k8s_logs",
			mcp.WithDescription("Fetch logs from a pod (one-shot, buffered). For streaming logs use the cloud backend's SSE endpoint."),
			mcp.WithInputSchema[k8sLogsArgs](),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args k8sLogsArgs) (*mcp.CallToolResult, error) {
			client := buildMCPK8sClient(args.k8sConnectionArgs)
			tail := args.TailLines
			if tail == 0 {
				tail = 200
			}
			output, err := client.Logs(ctx, args.Pod, nsForCall(args.k8sConnectionArgs), k8s.LogOptions{
				Container: args.Container,
				Follow:    false,
				Previous:  args.Previous,
				TailLines: tail,
				Since:     args.Since,
			})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultJSON(map[string]any{"output": output})
		}),
	)

	// ---- Helm ----

	server.AddTool(
		mcp.NewTool(
			"clanker_k8s_helm_install",
			mcp.WithDescription("Install a Helm chart as a new release on the active context."),
			mcp.WithInputSchema[k8sHelmInstallArgs](),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args k8sHelmInstallArgs) (*mcp.CallToolResult, error) {
			client := buildMCPK8sClient(args.k8sConnectionArgs)
			helmArgs := []string{"install", args.Release, args.Chart}
			helmArgs = mcpAppendIf(helmArgs, "--version", args.Version)
			helmArgs = mcpAppendBoolIf(helmArgs, "--create-namespace", args.CreateNamespace)
			helmArgs = mcpAppendBoolIf(helmArgs, "--wait", args.Wait)
			helmArgs = mcpAppendIf(helmArgs, "--timeout", args.Timeout)
			helmArgs = mcpAppendBoolIf(helmArgs, "--dry-run", args.DryRun)
			for _, f := range args.ValuesFiles {
				helmArgs = append(helmArgs, "-f", f)
			}
			for _, s := range args.Set {
				helmArgs = append(helmArgs, "--set", s)
			}
			output, err := client.RunHelm(ctx, helmArgs...)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultJSON(map[string]any{"output": output})
		}),
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_k8s_helm_upgrade",
			mcp.WithDescription("Upgrade an existing Helm release. Set install=true for the upgrade-or-install behaviour of 'helm upgrade --install'."),
			mcp.WithInputSchema[k8sHelmUpgradeArgs](),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args k8sHelmUpgradeArgs) (*mcp.CallToolResult, error) {
			client := buildMCPK8sClient(args.k8sConnectionArgs)
			helmArgs := []string{"upgrade", args.Release, args.Chart}
			helmArgs = mcpAppendBoolIf(helmArgs, "--install", args.Install)
			helmArgs = mcpAppendIf(helmArgs, "--version", args.Version)
			helmArgs = mcpAppendBoolIf(helmArgs, "--wait", args.Wait)
			helmArgs = mcpAppendIf(helmArgs, "--timeout", args.Timeout)
			helmArgs = mcpAppendBoolIf(helmArgs, "--dry-run", args.DryRun)
			helmArgs = mcpAppendBoolIf(helmArgs, "--reuse-values", args.ReuseValues)
			helmArgs = mcpAppendBoolIf(helmArgs, "--reset-values", args.ResetValues)
			helmArgs = mcpAppendBoolIf(helmArgs, "--force", args.Force)
			for _, f := range args.ValuesFiles {
				helmArgs = append(helmArgs, "-f", f)
			}
			for _, s := range args.Set {
				helmArgs = append(helmArgs, "--set", s)
			}
			output, err := client.RunHelm(ctx, helmArgs...)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultJSON(map[string]any{"output": output})
		}),
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_k8s_helm_list",
			mcp.WithDescription("List Helm releases on the active context. Pass allNamespaces=true to span every namespace."),
			mcp.WithInputSchema[k8sHelmListArgs](),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args k8sHelmListArgs) (*mcp.CallToolResult, error) {
			// AllNamespaces flips the client's namespace to "all" so
			// buildHelmArgs skips '-n' injection.
			conn := args.k8sConnectionArgs
			if args.AllNamespaces {
				conn.Namespace = "all"
			}
			client := buildMCPK8sClient(conn)
			helmArgs := []string{"list", "-o", "json"}
			if args.AllNamespaces {
				helmArgs = append(helmArgs, "-A")
			}
			output, err := client.RunHelm(ctx, helmArgs...)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			// helm list -o json gives us a parseable array; surface
			// both the raw string (for clients that just want it) and
			// the parsed form (for downstream LLM use).
			var parsed any
			if uerr := json.Unmarshal([]byte(output), &parsed); uerr == nil {
				return mcp.NewToolResultJSON(map[string]any{"releases": parsed})
			}
			return mcp.NewToolResultJSON(map[string]any{"output": output})
		}),
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_k8s_helm_uninstall",
			mcp.WithDescription("Uninstall a Helm release (removes all resources created by the release)."),
			mcp.WithInputSchema[k8sHelmUninstallArgs](),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args k8sHelmUninstallArgs) (*mcp.CallToolResult, error) {
			client := buildMCPK8sClient(args.k8sConnectionArgs)
			helmArgs := []string{"uninstall", args.Release}
			helmArgs = mcpAppendBoolIf(helmArgs, "--keep-history", args.KeepHistory)
			helmArgs = mcpAppendBoolIf(helmArgs, "--dry-run", args.DryRun)
			helmArgs = mcpAppendIf(helmArgs, "--timeout", args.Timeout)
			output, err := client.RunHelm(ctx, helmArgs...)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultJSON(map[string]any{"output": output})
		}),
	)

	// ---- Node lifecycle ----

	server.AddTool(
		mcp.NewTool(
			"clanker_k8s_node_cordon",
			mcp.WithDescription("Mark a node as unschedulable. Existing pods are left in place — use clanker_k8s_node_drain to evict them."),
			mcp.WithInputSchema[k8sNodeArgs](),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args k8sNodeArgs) (*mcp.CallToolResult, error) {
			client := newMCPNodeClient(args)
			output, err := client.Run(ctx, "cordon", args.Node)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultJSON(map[string]any{"output": output})
		}),
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_k8s_node_uncordon",
			mcp.WithDescription("Mark a node as schedulable."),
			mcp.WithInputSchema[k8sNodeArgs](),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args k8sNodeArgs) (*mcp.CallToolResult, error) {
			client := newMCPNodeClient(args)
			output, err := client.Run(ctx, "uncordon", args.Node)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultJSON(map[string]any{"output": output})
		}),
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_k8s_node_drain",
			mcp.WithDescription("Cordon a node and evict its pods so it can be safely removed or rebooted. Mirrors 'kubectl drain' flags."),
			mcp.WithInputSchema[k8sNodeDrainArgs](),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args k8sNodeDrainArgs) (*mcp.CallToolResult, error) {
			client := newMCPNodeClient(args.k8sNodeArgs)
			drainArgs := []string{"drain", args.Node}
			if args.Force {
				drainArgs = append(drainArgs, "--force")
			}
			// IgnoreDaemonsets defaults to true to match kubectl's safety
			// net; passing false explicitly makes drain refuse to run if
			// any DaemonSet-managed pod is present.
			if args.IgnoreDaemonsets == nil || *args.IgnoreDaemonsets {
				drainArgs = append(drainArgs, "--ignore-daemonsets")
			}
			if args.DeleteEmptyDirData {
				drainArgs = append(drainArgs, "--delete-emptydir-data")
			}
			if args.GracePeriod >= 0 {
				drainArgs = append(drainArgs, fmt.Sprintf("--grace-period=%d", args.GracePeriod))
			}
			if strings.TrimSpace(args.Timeout) != "" {
				drainArgs = append(drainArgs, "--timeout", args.Timeout)
			}
			output, err := client.Run(ctx, drainArgs...)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultJSON(map[string]any{"output": output})
		}),
	)
}

// newMCPNodeClient mirrors buildMCPK8sClient but pins the client to the
// "all" pseudo-namespace so buildArgs doesn't inject '-n' on
// cluster-scoped node operations.
func newMCPNodeClient(args k8sNodeArgs) *k8s.Client {
	debug := viper.GetBool("debug")
	kubeconfig := strings.TrimSpace(args.Kubeconfig)
	if kubeconfig == "" {
		kubeconfig = getKubeconfigPath()
	}
	client := k8s.NewClient(kubeconfig, strings.TrimSpace(args.Context), debug)
	client.SetNamespace("all")
	return client
}
