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
