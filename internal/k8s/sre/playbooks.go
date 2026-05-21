package sre

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// PlaybookStep is one step in a generated remediation plan. It carries
// the kubectl/helm/clanker invocation the executor will run plus
// metadata for the approval UI (why this step exists, how risky it is,
// whether it can be auto-applied).
//
// The shape is deliberately close to sre.RemediationStep — we could
// have re-used that type, but playbooks emit ORDERED multi-step plans
// where a later step depends on an earlier one's effect, while
// RemediationStep is currently used for one-shot human-readable
// recommendations. Keeping the playbook step distinct avoids
// retrofitting the diagnostics types with sequencing concerns.
type PlaybookStep struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	Command     string   `json:"command"`            // "kubectl" | "helm" | "clanker"
	Args        []string `json:"args"`               // pre-shellquoted argv
	Reason      string   `json:"reason,omitempty"`   // human-facing "why this"
	Risk        string   `json:"risk,omitempty"`     // low | medium | high
	Mutating    bool     `json:"mutating,omitempty"` // false = read-only / diagnostic
	// RequiresApproval forces a per-step confirmation even when the
	// caller asked for auto-apply. Used for irreversible steps
	// (delete, drain) so 'auto' mode still pauses on the dangerous ones.
	RequiresApproval bool `json:"requiresApproval,omitempty"`
}

// PlaybookPlan is the ordered list of steps a playbook emits, plus
// some metadata the runner / UI uses to render and audit. The Plan
// itself does NOT execute — that's the caller's job. This keeps
// playbooks pure functions of (context, k8s state) → steps.
type PlaybookPlan struct {
	PlaybookID  string         `json:"playbookId"`
	Title       string         `json:"title"`
	Target      string         `json:"target"` // 'pod/foo@ns' or similar
	Summary     string         `json:"summary"`
	Steps       []PlaybookStep `json:"steps"`
	GeneratedAt time.Time      `json:"generatedAt"`
	// Notes is free-form text the runner can render verbatim above
	// the step list — used to surface context the LLM diagnosed
	// (e.g., "container OOMKilled 3 times in the last 10m").
	Notes []string `json:"notes,omitempty"`
}

// PlaybookInput is the request shape that drives a playbook. Not every
// playbook uses every field; each playbook documents what it needs.
type PlaybookInput struct {
	Namespace string `json:"namespace,omitempty"`
	// Name is the target resource the playbook should operate on
	// (a pod for crashloop, a deployment for stuck rollout, etc.).
	// Optional for cluster-wide playbooks.
	Name string `json:"name,omitempty"`
	// Cluster / Context: optional kubectl context override. The
	// playbook itself doesn't switch context — it emits steps that
	// honour whichever context the runner sets.
	Context string `json:"context,omitempty"`
	Cluster string `json:"cluster,omitempty"`
}

// Playbook is a deterministic plan generator. Each playbook reads the
// current cluster state via the supplied K8sClient and returns a
// PlaybookPlan that the caller (CLI / backend / agent) executes —
// usually with per-step approval gates.
type Playbook interface {
	// ID is the stable identifier used to invoke the playbook
	// (e.g., 'crashloop-recovery'). Kebab-case.
	ID() string
	// Title is the human-readable name shown in the UI.
	Title() string
	// Description summarises when and why to use this playbook.
	Description() string
	// Plan inspects the cluster state and returns an ordered set of
	// remediation steps. Returning an empty Steps slice (with a
	// non-empty Summary) signals "nothing to do" — the runner
	// should surface the summary as a no-op outcome.
	Plan(ctx context.Context, client K8sClient, input PlaybookInput) (*PlaybookPlan, error)
}

// PlaybookRegistry maps IDs to playbooks. Used by the CLI 'fix'
// subcommand and the backend /api/k8s/playbook/run handler.
type PlaybookRegistry struct {
	playbooks map[string]Playbook
}

// NewPlaybookRegistry returns a registry pre-populated with every
// built-in playbook this package ships. Callers can add their own
// playbooks via Register before invoking Get/List.
func NewPlaybookRegistry() *PlaybookRegistry {
	r := &PlaybookRegistry{playbooks: make(map[string]Playbook)}
	for _, p := range builtinPlaybooks() {
		r.Register(p)
	}
	return r
}

// Register adds a playbook to the registry. Overwrites any existing
// playbook with the same ID — callers that need to compose registries
// (e.g., a test that injects a mock for one of the built-ins) should
// register after NewPlaybookRegistry.
func (r *PlaybookRegistry) Register(p Playbook) {
	r.playbooks[p.ID()] = p
}

// Get returns the playbook by ID, or an error listing the known IDs
// if no match. The error message lists the valid IDs alphabetically
// so the CLI / API surface gets a stable hint.
func (r *PlaybookRegistry) Get(id string) (Playbook, error) {
	if p, ok := r.playbooks[id]; ok {
		return p, nil
	}
	known := r.IDs()
	return nil, fmt.Errorf("unknown playbook %q (known: %s)", id, strings.Join(known, ", "))
}

// IDs returns every registered playbook ID in deterministic
// (alphabetical) order — exposed so CLI help and the
// /api/k8s/playbook/list endpoint can render a stable list.
func (r *PlaybookRegistry) IDs() []string {
	ids := make([]string, 0, len(r.playbooks))
	for id := range r.playbooks {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// All returns every registered playbook, sorted by ID. Useful for
// rendering a 'clanker k8s fix' help table with descriptions.
func (r *PlaybookRegistry) All() []Playbook {
	ids := r.IDs()
	out := make([]Playbook, 0, len(ids))
	for _, id := range ids {
		out = append(out, r.playbooks[id])
	}
	return out
}

// builtinPlaybooks returns the standard set shipped with this package.
// Adding a new playbook to the library = adding a constructor here
// (and writing a focused test).
func builtinPlaybooks() []Playbook {
	return []Playbook{
		&crashLoopRecoveryPlaybook{},
		&stuckRolloutPlaybook{},
		&pendingPodSchedulingPlaybook{},
	}
}

// =====================================================================
// crashLoopRecoveryPlaybook
// =====================================================================
//
// Common pattern: a pod stuck in CrashLoopBackOff because the
// container exits non-zero on startup. Recovery is a sequence:
//   1. Capture recent logs from the previous container (so the user /
//      LLM has data to diagnose with).
//   2. Describe the pod (events, container statuses, restart count).
//   3. Delete the pod, letting its controller (ReplicaSet, StatefulSet)
//      recreate it — sometimes the failure was a transient kube state
//      issue and a fresh pod recovers on its own.
//
// This playbook deliberately does NOT mutate the underlying workload
// (no scale-down, no image rollback). That's a separate playbook
// (stuck-rollout) that the user explicitly asks for. We stop after
// the pod delete and let the user observe whether the new pod is
// healthy — agents can chain into stuck-rollout if it isn't.

type crashLoopRecoveryPlaybook struct{}

func (p *crashLoopRecoveryPlaybook) ID() string    { return "crashloop-recovery" }
func (p *crashLoopRecoveryPlaybook) Title() string { return "CrashLoopBackOff recovery" }
func (p *crashLoopRecoveryPlaybook) Description() string {
	return "Capture previous-container logs + events, then delete the crashing pod so its controller recreates it. Stops at the delete; chain stuck-rollout if the new pod is unhealthy too."
}

func (p *crashLoopRecoveryPlaybook) Plan(ctx context.Context, _ K8sClient, input PlaybookInput) (*PlaybookPlan, error) {
	if input.Name == "" {
		return nil, fmt.Errorf("crashloop-recovery: pod name is required (use the --name flag)")
	}
	ns := input.Namespace
	if ns == "" {
		ns = "default"
	}
	pod := input.Name
	target := fmt.Sprintf("pod/%s@%s", pod, ns)

	steps := []PlaybookStep{
		{
			ID:          "logs-previous",
			Description: fmt.Sprintf("Capture last 200 lines from the previous %s container", pod),
			Command:     "kubectl",
			Args:        kubectlArgs(input, "logs", pod, "--previous", "--tail=200"),
			Reason:      "The current container is restarting, so 'kubectl logs' alone would only show the post-restart state. --previous catches the crash output.",
			Risk:        "low",
			Mutating:    false,
		},
		{
			ID:          "describe",
			Description: fmt.Sprintf("Describe %s for events + container statuses", pod),
			Command:     "kubectl",
			Args:        kubectlArgs(input, "describe", "pod", pod),
			Reason:      "Surfaces the kubelet-reported exit code, last termination reason (OOMKilled, Error, …), and any image-pull / probe events leading up to the crash.",
			Risk:        "low",
			Mutating:    false,
		},
		{
			ID:               "delete-pod",
			Description:      fmt.Sprintf("Delete %s so its controller recreates a fresh pod", pod),
			Command:          "kubectl",
			Args:             kubectlArgs(input, "delete", "pod", pod),
			Reason:           "Transient kube state (e.g., a stuck volume attach, a flapping probe) often clears on pod restart. The owning controller (ReplicaSet, StatefulSet) brings a new pod up automatically.",
			Risk:             "medium",
			Mutating:         true,
			RequiresApproval: true,
		},
	}

	return &PlaybookPlan{
		PlaybookID:  p.ID(),
		Title:       p.Title(),
		Target:      target,
		Summary:     fmt.Sprintf("Diagnose and recycle %s. Stops before re-applying workload changes — chain stuck-rollout if the recreated pod also fails.", target),
		Steps:       steps,
		GeneratedAt: time.Now().UTC(),
		Notes: []string{
			"This plan captures diagnostics first, then deletes the pod. If you're operating without LLM assistance, scan the 'logs-previous' output for OOMKilled / image-pull errors before approving the delete.",
		},
	}, nil
}

// =====================================================================
// stuckRolloutPlaybook
// =====================================================================
//
// A deployment rollout stuck in progress (image-pull failure, failing
// readiness probe on the new pod spec, missing ConfigMap) usually has
// only one safe recovery: roll back to the last known-good revision.
// This playbook captures the rollout state + history so the user
// can see WHY it's stuck, then offers the undo step.

type stuckRolloutPlaybook struct{}

func (p *stuckRolloutPlaybook) ID() string    { return "stuck-rollout" }
func (p *stuckRolloutPlaybook) Title() string { return "Stuck deployment rollout" }
func (p *stuckRolloutPlaybook) Description() string {
	return "Inspect the rollout status + revision history of a deployment, then roll back to the previous revision. Use when a rollout has been stalled for minutes with one or more pods stuck Pending / ImagePullBackOff / CrashLoopBackOff."
}

func (p *stuckRolloutPlaybook) Plan(ctx context.Context, _ K8sClient, input PlaybookInput) (*PlaybookPlan, error) {
	if input.Name == "" {
		return nil, fmt.Errorf("stuck-rollout: deployment name is required (use the --name flag)")
	}
	ns := input.Namespace
	if ns == "" {
		ns = "default"
	}
	dep := input.Name
	target := fmt.Sprintf("deployment/%s@%s", dep, ns)

	steps := []PlaybookStep{
		{
			ID:          "rollout-status",
			Description: fmt.Sprintf("Read current rollout status of %s", dep),
			Command:     "kubectl",
			Args:        kubectlArgs(input, "rollout", "status", "deployment/"+dep, "--watch=false"),
			Reason:      "Shows where the rollout is stuck (waiting on ReplicaSet scale-up, failing probes, etc).",
			Risk:        "low",
			Mutating:    false,
		},
		{
			ID:          "rollout-history",
			Description: fmt.Sprintf("List the revision history of %s", dep),
			Command:     "kubectl",
			Args:        kubectlArgs(input, "rollout", "history", "deployment/"+dep),
			Reason:      "We need the previous revision number to know what we're rolling back TO.",
			Risk:        "low",
			Mutating:    false,
		},
		{
			ID:          "describe",
			Description: fmt.Sprintf("Describe %s for events + condition reasons", dep),
			Command:     "kubectl",
			Args:        kubectlArgs(input, "describe", "deployment/"+dep),
			Reason:      "Surfaces ProgressDeadlineExceeded / ReplicaFailure conditions and the kubelet events behind them.",
			Risk:        "low",
			Mutating:    false,
		},
		{
			ID:               "rollout-undo",
			Description:      fmt.Sprintf("Roll %s back to the previous revision", dep),
			Command:          "kubectl",
			Args:             kubectlArgs(input, "rollout", "undo", "deployment/"+dep),
			Reason:           "If the current revision is broken, rolling back to the previous one restores the known-good state. The deployment's revisionHistoryLimit must be >= 1 (default is 10).",
			Risk:             "medium",
			Mutating:         true,
			RequiresApproval: true,
		},
	}

	return &PlaybookPlan{
		PlaybookID:  p.ID(),
		Title:       p.Title(),
		Target:      target,
		Summary:     fmt.Sprintf("Inspect and roll back the stuck rollout of %s. Stops after undo — chain crashloop-recovery if the rolled-back pods still fail.", target),
		Steps:       steps,
		GeneratedAt: time.Now().UTC(),
		Notes: []string{
			"Roll back is destructive: any config changes baked into the current revision (env vars, image tag, args) will revert. Verify the rollout history shows the right previous revision before approving.",
		},
	}, nil
}

// =====================================================================
// pendingPodSchedulingPlaybook
// =====================================================================
//
// A pod stuck in Pending is almost always a scheduler problem: no
// node has enough CPU/memory, no node matches the pod's tolerations
// / nodeSelector / affinity, or PV bindings are blocking. The right
// remediation depends on the cluster shape (Karpenter? fixed
// node-pool? cluster-autoscaler?), so this playbook is diagnostic-
// only: surface the data the user / LLM needs to choose between
// 'add capacity', 'relax constraints', and 'fix the volume binding'.
// No mutating step.

type pendingPodSchedulingPlaybook struct{}

func (p *pendingPodSchedulingPlaybook) ID() string    { return "pending-pod-scheduling" }
func (p *pendingPodSchedulingPlaybook) Title() string { return "Diagnose pending pod scheduling" }
func (p *pendingPodSchedulingPlaybook) Description() string {
	return "Surface why a pod is stuck Pending: scheduler events, node capacity, taints/tolerations. Diagnostic-only — the right fix (add nodes / scale HPA / relax selectors / fix PVC binding) depends on the cluster topology, so this playbook stops at the data-gathering step."
}

func (p *pendingPodSchedulingPlaybook) Plan(ctx context.Context, _ K8sClient, input PlaybookInput) (*PlaybookPlan, error) {
	if input.Name == "" {
		return nil, fmt.Errorf("pending-pod-scheduling: pod name is required (use the --name flag)")
	}
	ns := input.Namespace
	if ns == "" {
		ns = "default"
	}
	pod := input.Name
	target := fmt.Sprintf("pod/%s@%s", pod, ns)

	steps := []PlaybookStep{
		{
			ID:          "describe-pod",
			Description: fmt.Sprintf("Describe %s — scheduler events go here", pod),
			Command:     "kubectl",
			Args:        kubectlArgs(input, "describe", "pod", pod),
			Reason:      "The Events section will show FailedScheduling messages with the exact reason (insufficient cpu/memory, no matching node, PVC not bound, …).",
			Risk:        "low",
			Mutating:    false,
		},
		{
			ID:          "get-nodes",
			Description: "List nodes with their allocatable capacity",
			Command:     "kubectl",
			// No --namespace: nodes are cluster-scoped. We still want
			// --context if the caller set one.
			Args:     kubectlArgsClusterScoped(input, "get", "nodes", "-o", "wide"),
			Reason:   "Shows which nodes exist and roughly how full they are. Empty cluster + Karpenter would show one or two nodes; full cluster + fixed pool shows everyone Ready.",
			Risk:     "low",
			Mutating: false,
		},
		{
			ID:          "top-nodes",
			Description: "Show per-node CPU/memory pressure",
			Command:     "kubectl",
			Args:        kubectlArgsClusterScoped(input, "top", "nodes"),
			Reason:      "Confirms whether the cluster is genuinely out of capacity vs. has free room but a constraint mismatch.",
			Risk:        "low",
			Mutating:    false,
		},
		{
			ID:          "events",
			Description: fmt.Sprintf("Tail recent events for %s", pod),
			Command:     "kubectl",
			Args:        kubectlArgs(input, "get", "events", "--field-selector", "involvedObject.name="+pod, "--sort-by=.lastTimestamp"),
			Reason:      "Picks up scheduler retries that may have surfaced different reasons over time.",
			Risk:        "low",
			Mutating:    false,
		},
	}

	return &PlaybookPlan{
		PlaybookID:  p.ID(),
		Title:       p.Title(),
		Target:      target,
		Summary:     fmt.Sprintf("Gather scheduler diagnostics for %s. Read the describe + events output to identify the remediation: add node capacity, relax selectors, fix PVC binding, or scale the HPA.", target),
		Steps:       steps,
		GeneratedAt: time.Now().UTC(),
		Notes: []string{
			"This playbook is intentionally read-only. The mutation that fixes a Pending pod (scale node pool, edit tolerations, create the missing PVC) depends on the cluster topology — chain into 'clanker k8s helm install' for an HPA, 'clanker k8s apply' for a config change, or your cloud's node-pool API.",
		},
	}, nil
}

// kubectlArgsClusterScoped is kubectlArgs minus the namespace flag.
// Cluster-scoped resources (nodes, PVs, ClusterRoles) reject -n.
func kubectlArgsClusterScoped(input PlaybookInput, args ...string) []string {
	out := make([]string, 0, len(args)+2)
	if input.Context != "" {
		out = append(out, "--context", input.Context)
	}
	out = append(out, args...)
	return out
}

// kubectlArgs prepends '-n <ns>' / '--context <ctx>' (when set) to the
// caller-supplied kubectl args. Keeps each step's Args list self-
// contained so the executor doesn't have to thread context separately.
func kubectlArgs(input PlaybookInput, args ...string) []string {
	out := make([]string, 0, len(args)+4)
	if input.Namespace != "" {
		out = append(out, "-n", input.Namespace)
	}
	if input.Context != "" {
		out = append(out, "--context", input.Context)
	}
	out = append(out, args...)
	return out
}
