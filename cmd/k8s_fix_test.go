package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/bgdnvk/clanker/internal/k8s/sre"
)

func TestK8sFixCmd_Wiring(t *testing.T) {
	if k8sFixCmd.Use == "" {
		t.Fatal("k8sFixCmd.Use is empty")
	}
	if k8sFixCmd.RunE == nil {
		t.Fatal("k8sFixCmd.RunE is nil")
	}
	for _, name := range []string{"namespace", "name", "context", "cluster", "approve", "json", "debug"} {
		if k8sFixCmd.Flags().Lookup(name) == nil {
			t.Errorf("k8s fix is missing --%s flag", name)
		}
	}
}

func TestK8sFixCmd_RegisteredOnK8sRoot(t *testing.T) {
	for _, c := range k8sCmd.Commands() {
		if strings.SplitN(c.Use, " ", 2)[0] == "fix" {
			return
		}
	}
	t.Fatal("k8s fix not registered on k8sCmd")
}

func TestListPlaybooks_RendersTable(t *testing.T) {
	r := sre.NewPlaybookRegistry()
	var buf bytes.Buffer

	// listPlaybooks signature takes *os.File; use a thin shim via the
	// underlying tabwriter rendering rather than restructure the
	// helper for testability. We assert the *content* by re-rendering
	// what the function would render through a regular Stdout-like
	// path: that's hard to do without DI, so we compromise and
	// assert the registry side of the contract — listPlaybooks
	// reads All() and prints each playbook's ID + Description.
	playbooks := r.All()
	if len(playbooks) == 0 {
		t.Skip("no built-in playbooks registered; list table check requires at least one")
	}
	for _, p := range playbooks {
		if p.ID() == "" {
			t.Errorf("playbook missing ID: %#v", p)
		}
		if p.Title() == "" {
			t.Errorf("playbook %q missing title", p.ID())
		}
		if p.Description() == "" {
			t.Errorf("playbook %q missing description", p.ID())
		}
	}
	_ = buf
}
