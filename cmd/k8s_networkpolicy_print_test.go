package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/bgdnvk/clanker/internal/k8s/networking"
)

func TestPrintNetworkPolicyAuditTable_Empty(t *testing.T) {
	var buf bytes.Buffer
	printNetworkPolicyAuditTable(&buf, &networking.PolicyAuditReport{})
	if got := buf.String(); !strings.Contains(got, "No namespaces audited") {
		t.Errorf("expected empty-report message, got %q", got)
	}
}

func TestPrintNetworkPolicyAuditTable_Nil(t *testing.T) {
	var buf bytes.Buffer
	printNetworkPolicyAuditTable(&buf, nil)
	if got := buf.String(); !strings.Contains(got, "No namespaces audited") {
		t.Errorf("expected empty-report message for nil, got %q", got)
	}
}

func TestPrintNetworkPolicyAuditTable_FlagsUncovered(t *testing.T) {
	report := &networking.PolicyAuditReport{
		Namespaces: []networking.NamespacePolicyAudit{
			{Namespace: "prod", PolicyCount: 2, DefaultDenyIn: true, DefaultDenyOut: true},
			{Namespace: "staging", PolicyCount: 1, DefaultDenyIn: true, DefaultDenyOut: false},
			{Namespace: "dev", PolicyCount: 0, DefaultDenyIn: false, DefaultDenyOut: false},
		},
	}

	var buf bytes.Buffer
	printNetworkPolicyAuditTable(&buf, report)
	out := buf.String()

	for _, want := range []string{"prod", "staging", "dev", "yes", "no"} {
		if !strings.Contains(out, want) {
			t.Errorf("table output missing %q\n----\n%s", want, out)
		}
	}

	// staging + dev should both appear in the uncovered footer warning.
	if !strings.Contains(out, "2 namespace(s) lack default-deny") {
		t.Errorf("expected uncovered count of 2, got: %s", out)
	}
	if !strings.Contains(out, "staging") || !strings.Contains(out, "dev") {
		t.Errorf("expected uncovered namespace list to include staging and dev, got: %s", out)
	}
	if strings.Contains(strings.SplitN(out, "lack default-deny", 2)[1], "prod") {
		t.Errorf("prod is fully covered and should not appear in the uncovered footer: %s", out)
	}
}

func TestPrintNetworkPolicyAuditTable_AllCoveredNoFooter(t *testing.T) {
	report := &networking.PolicyAuditReport{
		Namespaces: []networking.NamespacePolicyAudit{
			{Namespace: "prod", PolicyCount: 2, DefaultDenyIn: true, DefaultDenyOut: true},
		},
	}
	var buf bytes.Buffer
	printNetworkPolicyAuditTable(&buf, report)
	if strings.Contains(buf.String(), "lack default-deny") {
		t.Errorf("fully-covered report should not emit warning footer: %s", buf.String())
	}
}

func TestYesNo(t *testing.T) {
	if yesNo(true) != "yes" || yesNo(false) != "no" {
		t.Error("yesNo wrong")
	}
}
