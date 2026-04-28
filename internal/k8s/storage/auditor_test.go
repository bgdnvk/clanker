package storage

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// auditMock fans kubectl invocations to per-resource responses based on args.
type auditMock struct {
	pvList   string
	pvErr    error
	pvcList  string
	pvcErr   error
	podsList string
	podsErr  error
}

func (m *auditMock) Run(_ context.Context, args ...string) (string, error) {
	full := strings.Join(args, " ")
	switch {
	case strings.Contains(full, "get pv "), strings.HasSuffix(full, "get pv -o json"):
		return m.pvList, m.pvErr
	case strings.Contains(full, "get pvc"):
		return m.pvcList, m.pvcErr
	case strings.Contains(full, "get pods"):
		return m.podsList, m.podsErr
	}
	return "", nil
}

func (m *auditMock) RunWithNamespace(_ context.Context, _ string, _ ...string) (string, error) {
	return "", nil
}
func (m *auditMock) GetJSON(_ context.Context, _, _, _ string) ([]byte, error) {
	return nil, nil
}
func (m *auditMock) Describe(_ context.Context, _, _, _ string) (string, error) { return "", nil }
func (m *auditMock) Delete(_ context.Context, _, _, _ string) (string, error)   { return "", nil }
func (m *auditMock) Apply(_ context.Context, _ string) (string, error)          { return "", nil }

const (
	emptyList = `{"items": []}`

	pvBoundOnly = `{
	  "items": [{
	    "metadata": {"name": "pv-bound"},
	    "spec": {"capacity": {"storage": "10Gi"}, "persistentVolumeReclaimPolicy": "Delete"},
	    "status": {"phase": "Bound"}
	  }]
	}`

	pvAllStates = `{
	  "items": [
	    {"metadata": {"name": "pv-released"}, "spec": {"capacity": {"storage": "20Gi"}, "persistentVolumeReclaimPolicy": "Retain"}, "status": {"phase": "Released"}},
	    {"metadata": {"name": "pv-available"}, "spec": {"capacity": {"storage": "5Gi"}, "persistentVolumeReclaimPolicy": "Delete"}, "status": {"phase": "Available"}},
	    {"metadata": {"name": "pv-failed"}, "spec": {"capacity": {"storage": "1Gi"}, "persistentVolumeReclaimPolicy": "Delete"}, "status": {"phase": "Failed"}},
	    {"metadata": {"name": "pv-bound"}, "spec": {"capacity": {"storage": "10Gi"}, "persistentVolumeReclaimPolicy": "Delete"}, "status": {"phase": "Bound"}}
	  ]
	}`

	pvcBoundUsed = `{
	  "items": [{
	    "metadata": {"name": "data", "namespace": "prod"},
	    "spec": {"volumeName": "pv-bound", "resources": {"requests": {"storage": "10Gi"}}},
	    "status": {"phase": "Bound", "capacity": {"storage": "10Gi"}}
	  }]
	}`

	pvcMixed = `{
	  "items": [
	    {"metadata": {"name": "orphan", "namespace": "prod"}, "spec": {"volumeName": "pv-bound", "resources": {"requests": {"storage": "10Gi"}}}, "status": {"phase": "Bound", "capacity": {"storage": "10Gi"}}},
	    {"metadata": {"name": "stuck", "namespace": "default"}, "spec": {"resources": {"requests": {"storage": "5Gi"}}}, "status": {"phase": "Pending"}},
	    {"metadata": {"name": "gone", "namespace": "default"}, "spec": {"resources": {"requests": {"storage": "1Gi"}}}, "status": {"phase": "Lost"}}
	  ]
	}`

	podMountingOrphan = `{
	  "items": [{
	    "metadata": {"namespace": "prod"},
	    "spec": {"volumes": [{"persistentVolumeClaim": {"claimName": "data"}}]}
	  }]
	}`

	podsNoPVC = `{
	  "items": [{
	    "metadata": {"namespace": "kube-system"},
	    "spec": {"volumes": [{"emptyDir": {}}]}
	  }]
	}`
)

func TestAudit_HappyPath(t *testing.T) {
	a := NewAuditor(&auditMock{
		pvList:   pvBoundOnly,
		pvcList:  pvcBoundUsed,
		podsList: podMountingOrphan,
	}, false)

	report, err := a.Audit(context.Background())
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if report.PVsScanned != 1 || report.PVCsScanned != 1 || report.PodsScanned != 1 {
		t.Errorf("scan counts wrong: PVs=%d PVCs=%d Pods=%d", report.PVsScanned, report.PVCsScanned, report.PodsScanned)
	}
	if len(report.Findings) != 0 {
		t.Errorf("expected 0 findings, got %+v", report.Findings)
	}
}

func TestAudit_FlagsOrphanedPendingLost(t *testing.T) {
	a := NewAuditor(&auditMock{
		pvList:   pvBoundOnly,
		pvcList:  pvcMixed,
		podsList: podsNoPVC,
	}, false)

	report, err := a.Audit(context.Background())
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}

	issues := map[string]string{} // name -> issue
	for _, f := range report.Findings {
		if f.Kind == "pvc" {
			issues[f.Name] = f.Issue
		}
	}

	if !strings.Contains(issues["orphan"], "not referenced") {
		t.Errorf("expected orphan PVC flagged, got %+v", report.Findings)
	}
	if !strings.Contains(issues["stuck"], "Pending") {
		t.Errorf("expected stuck PVC flagged Pending, got %+v", report.Findings)
	}
	if !strings.Contains(issues["gone"], "Lost") {
		t.Errorf("expected gone PVC flagged Lost, got %+v", report.Findings)
	}

	if report.OrphanedPVCs != 1 {
		t.Errorf("OrphanedPVCs = %d, want 1", report.OrphanedPVCs)
	}
	if report.PendingPVCs != 1 {
		t.Errorf("PendingPVCs = %d, want 1", report.PendingPVCs)
	}
}

func TestAudit_FlagsAllPVStates(t *testing.T) {
	a := NewAuditor(&auditMock{
		pvList:   pvAllStates,
		pvcList:  emptyList,
		podsList: emptyList,
	}, false)

	report, err := a.Audit(context.Background())
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}

	issues := map[string]string{}
	for _, f := range report.Findings {
		if f.Kind == "pv" {
			issues[f.Name] = f.Issue
		}
	}

	if !strings.Contains(issues["pv-released"], "Released") {
		t.Errorf("expected Released PV flagged, got %+v", report.Findings)
	}
	if !strings.Contains(issues["pv-available"], "Available") {
		t.Errorf("expected Available PV flagged, got %+v", report.Findings)
	}
	if !strings.Contains(issues["pv-failed"], "Failed") {
		t.Errorf("expected Failed PV flagged, got %+v", report.Findings)
	}
	if _, ok := issues["pv-bound"]; ok {
		t.Errorf("Bound PV should not be flagged, got %+v", report.Findings)
	}

	if report.UnusedPVs != 3 {
		t.Errorf("UnusedPVs = %d, want 3", report.UnusedPVs)
	}
}

func TestAudit_PodScanFailureToleratesOrphans(t *testing.T) {
	// Pod listing fails — orphaned-PVC detection should be disabled but
	// the rest of the audit (pending/lost/PVs) must still run.
	a := NewAuditor(&auditMock{
		pvList:  pvBoundOnly,
		pvcList: pvcMixed,
		podsErr: errors.New("forbidden"),
	}, false)

	report, err := a.Audit(context.Background())
	if err != nil {
		t.Fatalf("Audit should not fail when pod scan errors: %v", err)
	}

	for _, f := range report.Findings {
		if f.Kind == "pvc" && f.Name == "orphan" && strings.Contains(f.Issue, "not referenced") {
			t.Errorf("orphaned-PVC detection should be skipped when pod scan fails, got %+v", f)
		}
	}

	// Pending + Lost should still surface.
	pending := false
	lost := false
	for _, f := range report.Findings {
		if f.Kind == "pvc" && strings.Contains(f.Issue, "Pending") {
			pending = true
		}
		if f.Kind == "pvc" && strings.Contains(f.Issue, "Lost") {
			lost = true
		}
	}
	if !pending || !lost {
		t.Errorf("expected Pending+Lost findings even with pod scan failure, got %+v", report.Findings)
	}
}

func TestAudit_PVListFailureReturnsError(t *testing.T) {
	a := NewAuditor(&auditMock{
		pvErr:    errors.New("kubectl boom"),
		pvcList:  emptyList,
		podsList: emptyList,
	}, false)

	if _, err := a.Audit(context.Background()); err == nil {
		t.Error("expected error when PV list fails")
	}
}

func TestAudit_FindingsSortedDeterministic(t *testing.T) {
	a := NewAuditor(&auditMock{
		pvList: pvAllStates,
		pvcList: `{
		  "items": [
		    {"metadata": {"name": "z", "namespace": "alpha"}, "spec": {"resources": {"requests": {"storage": "1Gi"}}}, "status": {"phase": "Pending"}},
		    {"metadata": {"name": "a", "namespace": "beta"}, "spec": {"resources": {"requests": {"storage": "1Gi"}}}, "status": {"phase": "Pending"}}
		  ]
		}`,
		podsList: emptyList,
	}, false)

	r1, _ := a.Audit(context.Background())
	r2, _ := a.Audit(context.Background())

	if len(r1.Findings) != len(r2.Findings) {
		t.Fatalf("finding counts differ across runs: %d vs %d", len(r1.Findings), len(r2.Findings))
	}
	for i := range r1.Findings {
		if r1.Findings[i].Name != r2.Findings[i].Name || r1.Findings[i].Kind != r2.Findings[i].Kind {
			t.Errorf("findings[%d] differ across runs: %+v vs %+v", i, r1.Findings[i], r2.Findings[i])
		}
	}

	// Verify "pv" sorts before "pvc" alphabetically.
	if r1.Findings[0].Kind != "pv" {
		t.Errorf("first finding kind = %q, want pv (sort order broken)", r1.Findings[0].Kind)
	}
}
