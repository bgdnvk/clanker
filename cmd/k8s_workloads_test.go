package cmd

import (
	"strings"
	"testing"
)

func TestResolveKindFromTable_ScalableAliases(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"deployment", "deployment"},
		{"deployments", "deployment"},
		{"Deploy", "deployment"},
		{"sts", "statefulset"},
		{"StatefulSet", "statefulset"},
		{"replicaset", "replicaset"},
		{"rs", "replicaset"},
	}
	for _, c := range cases {
		got, err := resolveKindFromTable(scalableKinds, c.raw, "scale")
		if err != nil {
			t.Fatalf("resolveKindFromTable(%q) returned error: %v", c.raw, err)
		}
		if got != c.want {
			t.Errorf("resolveKindFromTable(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}

func TestResolveKindFromTable_RejectsUnsupported(t *testing.T) {
	_, err := resolveKindFromTable(scalableKinds, "pod", "scale")
	if err == nil {
		t.Fatal("expected error for unsupported kind 'pod' under scale, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported kind") {
		t.Errorf("expected 'unsupported kind' message, got %v", err)
	}
	// Sanity: error message lists at least one valid canonical kind.
	if !strings.Contains(err.Error(), "deployment") {
		t.Errorf("expected error to list 'deployment' as valid, got %v", err)
	}
}

func TestResolveKindFromTable_RestartTable(t *testing.T) {
	// deployments, statefulsets, daemonsets all restart-able
	for _, raw := range []string{"deployment", "sts", "statefulset", "daemonset", "ds"} {
		if _, err := resolveKindFromTable(restartableKinds, raw, "restart"); err != nil {
			t.Errorf("restartableKinds rejected %q: %v", raw, err)
		}
	}
	// replicaset is NOT restartable via `kubectl rollout restart`.
	if _, err := resolveKindFromTable(restartableKinds, "rs", "restart"); err == nil {
		t.Errorf("expected restartableKinds to reject 'rs' (replicaset has no rollout restart)")
	}
}

func TestResolveKindFromTable_TrimsAndCaseFolds(t *testing.T) {
	got, err := resolveKindFromTable(scalableKinds, "  DEPLOYMENT  ", "scale")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "deployment" {
		t.Errorf("got %q, want deployment", got)
	}
}

func TestK8sScaleCmd_Wiring(t *testing.T) {
	if k8sScaleCmd.Use == "" {
		t.Fatal("k8sScaleCmd.Use is empty")
	}
	if k8sScaleCmd.RunE == nil {
		t.Fatal("k8sScaleCmd.RunE is nil")
	}
	// --replicas should be a required flag — calling without it must error.
	flag := k8sScaleCmd.Flags().Lookup("replicas")
	if flag == nil {
		t.Fatal("expected --replicas flag on k8s scale")
	}
}

func TestK8sRolloutCmd_HasSubcommands(t *testing.T) {
	subs := k8sRolloutCmd.Commands()
	got := make(map[string]bool, len(subs))
	for _, c := range subs {
		got[strings.SplitN(c.Use, " ", 2)[0]] = true
	}
	for _, want := range []string{"status", "undo", "history"} {
		if !got[want] {
			t.Errorf("k8s rollout is missing subcommand %q (have: %v)", want, got)
		}
	}
}

func TestK8sRmCmd_HasDestructiveFlags(t *testing.T) {
	for _, name := range []string{"force", "ignore-not-found", "grace-period", "namespace"} {
		if k8sRmCmd.Flags().Lookup(name) == nil {
			t.Errorf("k8s rm is missing --%s flag", name)
		}
	}
}

func TestK8sRestartCmd_HasNoClusterFlag(t *testing.T) {
	// The --cluster flag used to be wired but was a silent no-op (the
	// EKS/GKE/AKS kubeconfig auto-update flow only lives on `k8s ask`).
	// Guard against it sneaking back without an implementation behind it.
	if k8sRestartCmd.Flags().Lookup("cluster") != nil {
		t.Error("k8s restart should not advertise --cluster until the kubeconfig auto-update path is wired up")
	}
	if k8sScaleCmd.Flags().Lookup("cluster") != nil {
		t.Error("k8s scale should not advertise --cluster until the kubeconfig auto-update path is wired up")
	}
}

func TestK8sWorkloadCmds_RegisteredOnRoot(t *testing.T) {
	parents := map[string]bool{}
	for _, c := range k8sCmd.Commands() {
		parents[strings.SplitN(c.Use, " ", 2)[0]] = true
	}
	for _, want := range []string{"scale", "restart", "rollout", "rm"} {
		if !parents[want] {
			t.Errorf("k8s root is missing subcommand %q (have: %v)", want, parents)
		}
	}
}
