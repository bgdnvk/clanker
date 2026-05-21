package cmd

import (
	"strings"
	"testing"
)

func TestK8sNodeCmd_HasAllSubcommands(t *testing.T) {
	want := []string{"cordon", "uncordon", "drain"}
	got := make(map[string]bool, len(k8sNodeCmd.Commands()))
	for _, c := range k8sNodeCmd.Commands() {
		got[strings.SplitN(c.Use, " ", 2)[0]] = true
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("k8s node missing subcommand %q (have: %v)", name, got)
		}
	}
}

func TestK8sNodeDrain_HasKubectlFlags(t *testing.T) {
	for _, name := range []string{
		"force", "ignore-daemonsets", "delete-emptydir-data",
		"grace-period", "timeout", "pod-selector", "disable-eviction",
	} {
		if k8sNodeDrainCmd.Flags().Lookup(name) == nil {
			t.Errorf("k8s node drain is missing --%s flag", name)
		}
	}
}

func TestK8sNodeCordon_RequiresOneArg(t *testing.T) {
	if err := k8sNodeCordonCmd.Args(k8sNodeCordonCmd, nil); err == nil {
		t.Error("expected error when no node arg passed to cordon")
	}
	if err := k8sNodeCordonCmd.Args(k8sNodeCordonCmd, []string{"my-node"}); err != nil {
		t.Errorf("cordon with one arg should be valid: %v", err)
	}
}

func TestK8sNodeCmd_RegisteredOnK8sRoot(t *testing.T) {
	for _, c := range k8sCmd.Commands() {
		if strings.SplitN(c.Use, " ", 2)[0] == "node" {
			return
		}
	}
	t.Fatal("k8s node not registered on k8sCmd")
}
