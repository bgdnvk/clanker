package cmd

import (
	"strings"
	"testing"
)

func TestK8sCreateAKSCmd_Wiring(t *testing.T) {
	if k8sCreateAKSCmd.Use == "" {
		t.Fatal("k8sCreateAKSCmd.Use is empty")
	}
	if k8sCreateAKSCmd.RunE == nil {
		t.Fatal("k8sCreateAKSCmd.RunE is nil")
	}

	for _, name := range []string{
		"azure-subscription", "azure-resource-group", "azure-region",
		"nodes", "node-type", "version", "plan", "apply",
	} {
		if k8sCreateAKSCmd.Flags().Lookup(name) == nil {
			t.Errorf("k8s create aks is missing --%s flag", name)
		}
	}
}

func TestK8sCreateAKSCmd_RequiresExactlyOneArg(t *testing.T) {
	if err := k8sCreateAKSCmd.Args(k8sCreateAKSCmd, nil); err == nil {
		t.Error("expected error when cluster name omitted")
	}
	if err := k8sCreateAKSCmd.Args(k8sCreateAKSCmd, []string{"my-cluster", "extra"}); err == nil {
		t.Error("expected error when too many args passed")
	}
	if err := k8sCreateAKSCmd.Args(k8sCreateAKSCmd, []string{"my-cluster"}); err != nil {
		t.Errorf("expected single-arg call to be valid, got %v", err)
	}
}

func TestK8sCreateAKSCmd_RegisteredUnderCreate(t *testing.T) {
	for _, c := range k8sCreateCmd.Commands() {
		if strings.SplitN(c.Use, " ", 2)[0] == "aks" {
			return
		}
	}
	t.Fatal("k8s create aks not registered under k8s create")
}

func TestK8sCreateAKSCmd_ResourceGroupRequired(t *testing.T) {
	// Cobra marks 'azure-resource-group' as required via annotations; the
	// flag should report annotation "cobra_annotation_bash_completion_one_required_flag"
	// = "true". Verify by inspecting the annotations directly.
	f := k8sCreateAKSCmd.Flags().Lookup("azure-resource-group")
	if f == nil {
		t.Fatal("--azure-resource-group flag missing")
	}
	v, ok := f.Annotations["cobra_annotation_bash_completion_one_required_flag"]
	if !ok || len(v) == 0 || v[0] != "true" {
		t.Errorf("--azure-resource-group is not marked required: annotations=%v", f.Annotations)
	}
}
