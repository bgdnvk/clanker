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
	playbooks := r.All()
	if len(playbooks) == 0 {
		t.Skip("no built-in playbooks registered; list table check requires at least one")
	}

	var buf bytes.Buffer
	if err := listPlaybooks(&buf, r); err != nil {
		t.Fatalf("listPlaybooks: %v", err)
	}
	out := buf.String()
	// Sanity: header rendered, every registered playbook's ID + title
	// surfaced. The exact tabwriter spacing isn't load-bearing, so we
	// match on substrings rather than full lines.
	if !strings.Contains(out, "Available playbooks") {
		t.Errorf("expected 'Available playbooks' header, got:\n%s", out)
	}
	if !strings.Contains(out, "ID") || !strings.Contains(out, "TITLE") {
		t.Errorf("expected table header columns, got:\n%s", out)
	}
	for _, p := range playbooks {
		if !strings.Contains(out, p.ID()) {
			t.Errorf("rendered list missing playbook ID %q\n%s", p.ID(), out)
		}
		if !strings.Contains(out, p.Title()) {
			t.Errorf("rendered list missing playbook title %q\n%s", p.Title(), out)
		}
	}
}
