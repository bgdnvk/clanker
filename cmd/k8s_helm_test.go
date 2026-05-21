package cmd

import (
	"strings"
	"testing"
)

func TestK8sHelmCmd_HasAllSubcommands(t *testing.T) {
	want := []string{"install", "upgrade", "list", "uninstall", "status", "history", "rollback", "values"}
	got := make(map[string]bool)
	for _, c := range k8sHelmCmd.Commands() {
		got[strings.SplitN(c.Use, " ", 2)[0]] = true
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("k8s helm is missing %q (have: %v)", name, got)
		}
	}
}

func TestK8sHelmInstall_RequiresTwoArgs(t *testing.T) {
	if k8sHelmInstallCmd.Args == nil {
		t.Fatal("k8sHelmInstallCmd.Args is nil")
	}
	if err := k8sHelmInstallCmd.Args(k8sHelmInstallCmd, []string{"only-one"}); err == nil {
		t.Error("expected error when only one arg passed to install")
	}
	if err := k8sHelmInstallCmd.Args(k8sHelmInstallCmd, []string{"rel", "chart"}); err != nil {
		t.Errorf("unexpected error for valid arg count: %v", err)
	}
}

func TestK8sHelmInstall_HasCommonFlags(t *testing.T) {
	for _, name := range []string{"namespace", "create-namespace", "version", "values", "set", "wait", "dry-run", "timeout"} {
		if k8sHelmInstallCmd.Flags().Lookup(name) == nil {
			t.Errorf("k8s helm install is missing --%s flag", name)
		}
	}
}

func TestK8sHelmUpgrade_HasUpgradeSpecificFlags(t *testing.T) {
	for _, name := range []string{"install", "reuse-values", "reset-values", "force"} {
		if k8sHelmUpgradeCmd.Flags().Lookup(name) == nil {
			t.Errorf("k8s helm upgrade is missing --%s flag", name)
		}
	}
}

func TestK8sHelmList_HasAllNamespacesShorthand(t *testing.T) {
	f := k8sHelmListCmd.Flags().ShorthandLookup("A")
	if f == nil {
		t.Fatal("k8s helm list missing -A shorthand for --all-namespaces")
	}
	if f.Name != "all-namespaces" {
		t.Errorf("expected -A to map to all-namespaces, got %s", f.Name)
	}
}

func TestK8sHelmUninstall_HasKeepHistoryAndDryRun(t *testing.T) {
	for _, name := range []string{"keep-history", "dry-run"} {
		if k8sHelmUninstallCmd.Flags().Lookup(name) == nil {
			t.Errorf("k8s helm uninstall is missing --%s flag", name)
		}
	}
}

func TestK8sHelmRollback_RequiresTwoArgs(t *testing.T) {
	if err := k8sHelmRollbackCmd.Args(k8sHelmRollbackCmd, []string{"only-rel"}); err == nil {
		t.Error("expected error when revision missing")
	}
}

func TestK8sHelmCmd_RegisteredOnK8sRoot(t *testing.T) {
	for _, c := range k8sCmd.Commands() {
		if strings.SplitN(c.Use, " ", 2)[0] == "helm" {
			return
		}
	}
	t.Fatal("k8s helm not registered on k8sCmd")
}

func TestAppendIf_BehavesLikeFlagBuilder(t *testing.T) {
	got := appendIf([]string{"helm"}, "--version", "")
	if len(got) != 1 {
		t.Errorf("appendIf with empty value added args: %v", got)
	}
	got = appendIf([]string{"helm"}, "--version", "1.2.3")
	if len(got) != 3 || got[1] != "--version" || got[2] != "1.2.3" {
		t.Errorf("appendIf(non-empty) = %v", got)
	}
}

func TestAppendBoolIf_OnlyAddsWhenTrue(t *testing.T) {
	if got := appendBoolIf([]string{"helm"}, "--wait", false); len(got) != 1 {
		t.Errorf("appendBoolIf(false) added flag: %v", got)
	}
	if got := appendBoolIf([]string{"helm"}, "--wait", true); len(got) != 2 || got[1] != "--wait" {
		t.Errorf("appendBoolIf(true) = %v", got)
	}
}
