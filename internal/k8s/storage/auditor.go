package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Auditor inspects PersistentVolume + PersistentVolumeClaim posture for
// waste signals: orphaned PVCs (paying storage for nothing), orphaned PVs
// (Released or never-used Available), and stuck (Pending) PVCs.
//
// Read-only — only kubectl get/list is invoked.
type Auditor struct {
	client K8sClient
	pvm    *PVManager
	pvcm   *PVCManager
	debug  bool
}

func NewAuditor(client K8sClient, debug bool) *Auditor {
	return &Auditor{
		client: client,
		pvm:    NewPVManager(client, debug),
		pvcm:   NewPVCManager(client, debug),
		debug:  debug,
	}
}

// AuditFinding is one waste signal.
type AuditFinding struct {
	Kind      string `json:"kind"` // "pv" or "pvc"
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	Issue     string `json:"issue"`
	Detail    string `json:"detail,omitempty"`
	Capacity  string `json:"capacity,omitempty"`
}

// AuditReport rolls up the audit results.
type AuditReport struct {
	GeneratedAt  time.Time      `json:"generatedAt"`
	PVsScanned   int            `json:"pvsScanned"`
	PVCsScanned  int            `json:"pvcsScanned"`
	PodsScanned  int            `json:"podsScanned"`
	OrphanedPVCs int            `json:"orphanedPvcs"`
	PendingPVCs  int            `json:"pendingPvcs"`
	OrphanedPVs  int            `json:"orphanedPvs"`
	Findings     []AuditFinding `json:"findings,omitempty"`
}

// Audit lists PVs, PVCs, and pods cluster-wide and produces a list of waste
// findings. Pods are scanned to determine which PVCs are referenced by at
// least one workload — PVCs not referenced by any pod are flagged as
// orphaned (paying storage for nothing).
func (a *Auditor) Audit(ctx context.Context) (*AuditReport, error) {
	report := &AuditReport{GeneratedAt: time.Now().UTC()}

	pvs, err := a.pvm.ListPVs(ctx, QueryOptions{})
	if err != nil {
		return nil, fmt.Errorf("list PVs: %w", err)
	}
	report.PVsScanned = len(pvs)

	pvcs, err := a.pvcm.ListPVCs(ctx, "", QueryOptions{AllNamespaces: true})
	if err != nil {
		return nil, fmt.Errorf("list PVCs: %w", err)
	}
	report.PVCsScanned = len(pvcs)

	usedPVCs, podsScanned, err := a.collectPVCsInUseByPods(ctx)
	if err != nil {
		// Pod listing failure shouldn't block the entire audit; we just
		// can't detect orphaned PVCs without it.
		if a.debug {
			fmt.Println("[storage-audit] pod scan failed, orphaned-PVC detection disabled:", err)
		}
	}
	report.PodsScanned = podsScanned

	for _, pvc := range pvcs {
		report.Findings = append(report.Findings, a.checkPVC(pvc, usedPVCs)...)
	}
	for _, pv := range pvs {
		report.Findings = append(report.Findings, a.checkPV(pv)...)
	}

	for _, f := range report.Findings {
		switch {
		case f.Kind == "pvc" && strings.Contains(f.Issue, "not referenced"):
			report.OrphanedPVCs++
		case f.Kind == "pvc" && strings.Contains(f.Issue, "Pending"):
			report.PendingPVCs++
		case f.Kind == "pv":
			report.OrphanedPVs++
		}
	}

	sort.SliceStable(report.Findings, func(i, j int) bool {
		if report.Findings[i].Kind != report.Findings[j].Kind {
			return report.Findings[i].Kind < report.Findings[j].Kind
		}
		if report.Findings[i].Namespace != report.Findings[j].Namespace {
			return report.Findings[i].Namespace < report.Findings[j].Namespace
		}
		return report.Findings[i].Name < report.Findings[j].Name
	})

	return report, nil
}

func (a *Auditor) checkPVC(pvc PVCInfo, usedPVCs map[string]bool) []AuditFinding {
	var out []AuditFinding
	key := pvc.Namespace + "/" + pvc.Name

	switch strings.ToLower(pvc.Status) {
	case "pending":
		out = append(out, AuditFinding{
			Kind:      "pvc",
			Namespace: pvc.Namespace,
			Name:      pvc.Name,
			Issue:     "PVC stuck Pending",
			Detail:    fmt.Sprintf("requested %s, no PV has bound — provisioner may be missing or the storage class is wrong", pvc.RequestedStorage),
			Capacity:  pvc.RequestedStorage,
		})
	case "lost":
		out = append(out, AuditFinding{
			Kind:      "pvc",
			Namespace: pvc.Namespace,
			Name:      pvc.Name,
			Issue:     "PVC Lost",
			Detail:    "underlying PV was deleted; data is gone but the PVC reference still exists",
			Capacity:  pvc.RequestedStorage,
		})
	}

	// Bound PVCs not referenced by any pod = orphaned. Only check when
	// the pod scan ran (usedPVCs is nil if it failed).
	if usedPVCs != nil && strings.EqualFold(pvc.Status, "Bound") && !usedPVCs[key] {
		out = append(out, AuditFinding{
			Kind:      "pvc",
			Namespace: pvc.Namespace,
			Name:      pvc.Name,
			Issue:     "PVC not referenced by any pod",
			Detail:    fmt.Sprintf("bound to PV %q but no pod mounts it — likely orphaned spend", pvc.Volume),
			Capacity:  pvc.Capacity,
		})
	}
	return out
}

func (a *Auditor) checkPV(pv PVInfo) []AuditFinding {
	var out []AuditFinding
	switch strings.ToLower(pv.Status) {
	case "released":
		out = append(out, AuditFinding{
			Kind:     "pv",
			Name:     pv.Name,
			Issue:    "PV Released and not reclaimed",
			Detail:   fmt.Sprintf("reclaim policy %q — Retain leaks unless cleaned up by hand", pv.ReclaimPolicy),
			Capacity: pv.Capacity,
		})
	case "available":
		out = append(out, AuditFinding{
			Kind:     "pv",
			Name:     pv.Name,
			Issue:    "PV Available (never bound)",
			Detail:   "static PV with no PVC claim — verify it's still intentional",
			Capacity: pv.Capacity,
		})
	case "failed":
		out = append(out, AuditFinding{
			Kind:     "pv",
			Name:     pv.Name,
			Issue:    "PV Failed",
			Detail:   "PV moved to Failed state — provisioner or storage backend is unhealthy",
			Capacity: pv.Capacity,
		})
	}
	return out
}

// collectPVCsInUseByPods walks every pod cluster-wide and records which
// PVCs they reference. Returns a map keyed by "namespace/pvc-name".
func (a *Auditor) collectPVCsInUseByPods(ctx context.Context) (map[string]bool, int, error) {
	out, err := a.client.Run(ctx, "get", "pods", "-A", "-o", "json")
	if err != nil {
		return nil, 0, err
	}
	raw := []byte(out)
	var list struct {
		Items []struct {
			Metadata struct {
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Spec struct {
				Volumes []struct {
					PersistentVolumeClaim *struct {
						ClaimName string `json:"claimName"`
					} `json:"persistentVolumeClaim,omitempty"`
				} `json:"volumes"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, 0, fmt.Errorf("parse pod list: %w", err)
	}

	used := make(map[string]bool)
	for _, p := range list.Items {
		for _, v := range p.Spec.Volumes {
			if v.PersistentVolumeClaim == nil {
				continue
			}
			used[p.Metadata.Namespace+"/"+v.PersistentVolumeClaim.ClaimName] = true
		}
	}
	return used, len(list.Items), nil
}
