package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/bgdnvk/clanker/internal/k8s/storage"
)

func TestPrintStorageAuditReport_Nil(t *testing.T) {
	var buf bytes.Buffer
	printStorageAuditReport(&buf, nil)
	if !strings.Contains(buf.String(), "No audit report") {
		t.Errorf("expected nil-report message, got %q", buf.String())
	}
}

func TestPrintStorageAuditReport_Clean(t *testing.T) {
	var buf bytes.Buffer
	printStorageAuditReport(&buf, &storage.AuditReport{
		PVsScanned: 5, PVCsScanned: 3, PodsScanned: 12,
	})
	out := buf.String()
	if !strings.Contains(out, "Scanned 5 PV(s), 3 PVC(s), 12 pod(s)") {
		t.Errorf("expected scan summary in header, got %q", out)
	}
	if !strings.Contains(out, "No storage issues detected") {
		t.Errorf("expected clean-bill message, got %q", out)
	}
}

func TestPrintStorageAuditReport_RendersFindings(t *testing.T) {
	var buf bytes.Buffer
	printStorageAuditReport(&buf, &storage.AuditReport{
		PVsScanned: 1, PVCsScanned: 2, PodsScanned: 4,
		OrphanedPVCs: 1, PendingPVCs: 1, OrphanedPVs: 1,
		Findings: []storage.AuditFinding{
			{Kind: "pv", Name: "pv-released", Issue: "PV Released and not reclaimed", Capacity: "20Gi"},
			{Kind: "pvc", Namespace: "prod", Name: "orphan", Issue: "PVC not referenced by any pod", Capacity: "10Gi"},
			{Kind: "pvc", Namespace: "default", Name: "stuck", Issue: "PVC stuck Pending", Capacity: "5Gi"},
		},
	})
	out := buf.String()

	for _, want := range []string{
		"3 finding(s)",
		"KIND",
		"NAMESPACE/NAME",
		"CAPACITY",
		"-/pv-released",
		"prod/orphan",
		"default/stuck",
		"PVC stuck Pending",
		"20Gi",
		"5Gi",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in output, got:\n%s", want, out)
		}
	}
}

func TestPrintStorageAuditReport_DashesForEmptyFields(t *testing.T) {
	var buf bytes.Buffer
	printStorageAuditReport(&buf, &storage.AuditReport{
		Findings: []storage.AuditFinding{
			{Kind: "pv", Name: "pv-no-cap", Issue: "PV Failed"},
		},
	})
	out := buf.String()
	// PVs have no namespace and we render "-" as namespace placeholder.
	if !strings.Contains(out, "-/pv-no-cap") {
		t.Errorf("expected '-/pv-no-cap' for PV without namespace, got:\n%s", out)
	}
}
