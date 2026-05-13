package sre

import (
	"context"
	"strings"
	"time"
)

func collectK8sWarnings(ctx context.Context) map[string]any {
	out := map[string]any{}

	// --- Warning events ---
	if v, err := runCommandOutput(ctx, 4*time.Second,
		"kubectl", "get", "events", "--all-namespaces",
		"--field-selector", "type=Warning",
		"-o", "custom-columns=NAMESPACE:.metadata.namespace,NAME:.involvedObject.name,REASON:.reason,MESSAGE:.message,COUNT:.count",
		"--sort-by=.count",
	); err == nil {
		out["warningEvents"] = splitLinesLimited(v, 80)
	}

	// --- CrashLoopBackOff / OOMKilled / Failed pods ---
	if v, err := runCommandOutput(ctx, 4*time.Second,
		"kubectl", "get", "pods", "--all-namespaces",
		"-o", "custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name,STATUS:.status.phase,REASON:.status.reason,RESTARTS:.status.containerStatuses[0].restartCount",
	); err == nil {
		crashy := []string{}
		for _, line := range splitLinesLimited(v, 200) {
			upper := strings.ToUpper(line)
			if strings.Contains(upper, "CRASHLOOP") ||
				strings.Contains(upper, "OOMKILLED") ||
				strings.Contains(upper, "ERROR") ||
				(strings.Contains(upper, "FAILED") && !strings.Contains(upper, "STATUS")) {
				crashy = append(crashy, line)
			}
		}
		if len(crashy) > 0 {
			out["unhealthyPods"] = crashy
		}
	}

	// --- Pods with restart count > 5 ---
	if v, err := runCommandOutput(ctx, 4*time.Second,
		"kubectl", "get", "pods", "--all-namespaces",
		"-o", "custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name,RESTARTS:.status.containerStatuses[0].restartCount",
		"--sort-by=.status.containerStatuses[0].restartCount",
	); err == nil {
		highRestart := []string{}
		for _, line := range splitLinesLimited(v, 200) {
			cols := strings.Fields(line)
			if len(cols) >= 3 && cols[2] != "RESTARTS" && cols[2] != "<none>" {
				n := 0
				for _, c := range cols[2] {
					if c >= '0' && c <= '9' {
						n = n*10 + int(c-'0')
					}
				}
				if n > 5 {
					highRestart = append(highRestart, line)
				}
			}
		}
		if len(highRestart) > 0 {
			out["highRestartPods"] = highRestart
		}
	}

	// --- Node conditions (NotReady / Pressure) ---
	if v, err := runCommandOutput(ctx, 4*time.Second,
		"kubectl", "get", "nodes",
		"-o", "custom-columns=NAME:.metadata.name,STATUS:.status.conditions[-1].type,REASON:.status.conditions[-1].reason",
	); err == nil {
		notReady := []string{}
		for _, line := range splitLinesLimited(v, 50) {
			upper := strings.ToUpper(line)
			if strings.Contains(upper, "NOTREADY") ||
				strings.Contains(upper, "MEMORYPRESSURE") ||
				strings.Contains(upper, "DISKPRESSURE") ||
				strings.Contains(upper, "PIDPRESSURE") {
				notReady = append(notReady, line)
			}
		}
		if len(notReady) > 0 {
			out["nodeIssues"] = notReady
		}
	}

	// --- Deployments with unavailable replicas ---
	if v, err := runCommandOutput(ctx, 4*time.Second,
		"kubectl", "get", "deployments", "--all-namespaces",
		"-o", "custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name,DESIRED:.spec.replicas,READY:.status.readyReplicas,UNAVAILABLE:.status.unavailableReplicas",
	); err == nil {
		degraded := []string{}
		for _, line := range splitLinesLimited(v, 100) {
			if strings.Contains(line, "UNAVAILABLE") {
				continue
			}
			cols := strings.Fields(line)
			if len(cols) >= 5 && cols[4] != "<none>" && cols[4] != "0" {
				degraded = append(degraded, line)
			}
		}
		if len(degraded) > 0 {
			out["degradedDeployments"] = degraded
		}
	}

	// --- DaemonSets with misscheduled/unavailable ---
	if v, err := runCommandOutput(ctx, 3*time.Second,
		"kubectl", "get", "daemonsets", "--all-namespaces",
		"-o", "custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name,DESIRED:.status.desiredNumberScheduled,READY:.status.numberReady,UNAVAILABLE:.status.numberUnavailable",
	); err == nil {
		out["daemonSets"] = splitLinesLimited(v, 40)
	}

	// --- Failed jobs ---
	if v, err := runCommandOutput(ctx, 3*time.Second,
		"kubectl", "get", "jobs", "--all-namespaces",
		"--field-selector", "status.failed>0",
		"-o", "custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name,FAILED:.status.failed",
	); err == nil {
		if lines := splitLinesLimited(v, 40); len(lines) > 1 {
			out["failedJobs"] = lines
		}
	}

	// --- CronJobs with last schedule failed ---
	if v, err := runCommandOutput(ctx, 3*time.Second,
		"kubectl", "get", "cronjobs", "--all-namespaces",
		"-o", "custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name,SCHEDULE:.spec.schedule,LASTSCHEDULE:.status.lastScheduleTime",
	); err == nil {
		out["cronJobs"] = splitLinesLimited(v, 40)
	}

	// --- PVCs stuck in Pending ---
	if v, err := runCommandOutput(ctx, 3*time.Second,
		"kubectl", "get", "pvc", "--all-namespaces",
		"--field-selector", "status.phase=Pending",
		"-o", "custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name,STORAGE:.spec.resources.requests.storage,CLASS:.spec.storageClassName",
	); err == nil {
		if lines := splitLinesLimited(v, 40); len(lines) > 1 {
			out["pendingPVCs"] = lines
		}
	}

	// --- Services with 0 endpoints ---
	if v, err := runCommandOutput(ctx, 3*time.Second,
		"kubectl", "get", "endpoints", "--all-namespaces",
		"-o", "custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name,ENDPOINTS:.subsets",
	); err == nil {
		noEndpoints := []string{}
		for _, line := range splitLinesLimited(v, 200) {
			if strings.Contains(line, "<none>") {
				noEndpoints = append(noEndpoints, line)
			}
		}
		if len(noEndpoints) > 0 {
			out["servicesWithNoEndpoints"] = noEndpoints
		}
	}

	// --- HPAs at max replicas ---
	if v, err := runCommandOutput(ctx, 3*time.Second,
		"kubectl", "get", "hpa", "--all-namespaces",
		"-o", "custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name,CURRENT:.status.currentReplicas,MAX:.spec.maxReplicas",
	); err == nil {
		atMax := []string{}
		for _, line := range splitLinesLimited(v, 80) {
			cols := strings.Fields(line)
			if len(cols) >= 4 && cols[2] == cols[3] && cols[2] != "CURRENT" {
				atMax = append(atMax, line)
			}
		}
		if len(atMax) > 0 {
			out["hpaAtMax"] = atMax
		}
	}

	// --- cert-manager certificates expiring within 14 days ---
	if v, err := runCommandOutput(ctx, 3*time.Second,
		"kubectl", "get", "certificates", "--all-namespaces",
		"-o", "custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name,READY:.status.conditions[0].status,EXPIRY:.status.notAfter",
	); err == nil {
		out["certManagerCerts"] = splitLinesLimited(v, 40)
	}

	// --- Namespace resource quotas > 90% ---
	if v, err := runCommandOutput(ctx, 3*time.Second,
		"kubectl", "get", "resourcequota", "--all-namespaces",
		"-o", "custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name,HARD:.spec.hard,USED:.status.used",
	); err == nil {
		out["resourceQuotas"] = splitLinesLimited(v, 40)
	}

	// --- Ingress rules ---
	if v, err := runCommandOutput(ctx, 3*time.Second,
		"kubectl", "get", "ingress", "--all-namespaces",
		"-o", "custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name,CLASS:.spec.ingressClassName,HOSTS:.spec.rules[*].host",
	); err == nil {
		out["ingressRules"] = splitLinesLimited(v, 40)
	}

	// --- Network policies presence ---
	if v, err := runCommandOutput(ctx, 3*time.Second,
		"kubectl", "get", "networkpolicies", "--all-namespaces",
		"-o", "custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name",
	); err == nil {
		out["networkPolicies"] = splitLinesLimited(v, 40)
	}

	return out
}

// collectHostExtended gathers extra host-level OS and process signals.
