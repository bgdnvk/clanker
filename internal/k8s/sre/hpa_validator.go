package sre

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// HPAValidator inspects HorizontalPodAutoscalers and KEDA ScaledObjects for
// configuration smells that surface as silent reliability issues at
// scale-out time. Read-only.
type HPAValidator struct {
	client K8sClient
	debug  bool
}

func NewHPAValidator(client K8sClient, debug bool) *HPAValidator {
	return &HPAValidator{client: client, debug: debug}
}

// HPAFinding is one configuration issue on an HPA or KEDA ScaledObject.
// Severity follows the same vocabulary as the rest of the sre package.
type HPAFinding struct {
	Severity  IssueSeverity `json:"severity"`
	Resource  string        `json:"resource"` // "hpa" or "scaledobject"
	Namespace string        `json:"namespace"`
	Name      string        `json:"name"`
	Issue     string        `json:"issue"`
	Detail    string        `json:"detail,omitempty"`
}

// HPAValidationReport is the aggregate output.
type HPAValidationReport struct {
	GeneratedAt          time.Time    `json:"generatedAt"`
	HPAsScanned          int          `json:"hpasScanned"`
	ScaledObjectsScanned int          `json:"scaledObjectsScanned"`
	KEDAInstalled        bool         `json:"kedaInstalled"`
	Findings             []HPAFinding `json:"findings,omitempty"`
}

// Validate scans HPAs cluster-wide plus KEDA ScaledObjects (if KEDA's CRDs
// are installed) and returns a report of configuration smells.
func (v *HPAValidator) Validate(ctx context.Context) (*HPAValidationReport, error) {
	report := &HPAValidationReport{GeneratedAt: time.Now().UTC()}

	hpas, err := v.listHPAs(ctx)
	if err != nil {
		return nil, fmt.Errorf("list HPAs: %w", err)
	}
	report.HPAsScanned = len(hpas)
	for _, h := range hpas {
		report.Findings = append(report.Findings, v.validateHPA(h)...)
	}

	keda, err := v.kedaInstalled(ctx)
	if err == nil && keda {
		report.KEDAInstalled = true
		sos, err := v.listScaledObjects(ctx)
		if err == nil {
			report.ScaledObjectsScanned = len(sos)
			for _, s := range sos {
				report.Findings = append(report.Findings, v.validateScaledObject(s)...)
			}
		} else if v.debug {
			fmt.Println("[hpa-validator] keda detected but scaledobjects fetch failed:", err)
		}
	}

	return report, nil
}

// hpaSpec is the subset of the HPA v2 schema the validator inspects.
type hpaSpec struct {
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Spec struct {
		MinReplicas    *int `json:"minReplicas"`
		MaxReplicas    int  `json:"maxReplicas"`
		ScaleTargetRef struct {
			Kind string `json:"kind"`
			Name string `json:"name"`
		} `json:"scaleTargetRef"`
		Metrics []struct {
			Type     string `json:"type"`
			Resource *struct {
				Name   string `json:"name"`
				Target struct {
					Type               string `json:"type"`
					AverageUtilization *int   `json:"averageUtilization"`
					AverageValue       string `json:"averageValue"`
					Value              string `json:"value"`
				} `json:"target"`
			} `json:"resource,omitempty"`
			ContainerResource *struct {
				Name string `json:"name"`
			} `json:"containerResource,omitempty"`
			External *struct {
				Metric struct {
					Name string `json:"name"`
				} `json:"metric"`
			} `json:"external,omitempty"`
		} `json:"metrics"`
		Behavior *struct {
			ScaleUp   *struct{} `json:"scaleUp,omitempty"`
			ScaleDown *struct{} `json:"scaleDown,omitempty"`
		} `json:"behavior,omitempty"`
	} `json:"spec"`
}

func (v *HPAValidator) listHPAs(ctx context.Context) ([]hpaSpec, error) {
	raw, err := v.client.RunJSON(ctx, "get", "hpa", "-A", "-o", "json")
	if err != nil {
		return nil, err
	}
	var list struct {
		Items []hpaSpec `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("parse hpa list: %w", err)
	}
	return list.Items, nil
}

func (v *HPAValidator) validateHPA(h hpaSpec) []HPAFinding {
	var out []HPAFinding
	emit := func(sev IssueSeverity, issue, detail string) {
		out = append(out, HPAFinding{
			Severity:  sev,
			Resource:  "hpa",
			Namespace: h.Metadata.Namespace,
			Name:      h.Metadata.Name,
			Issue:     issue,
			Detail:    detail,
		})
	}

	// minReplicas absent → defaults to 1, which is usually fine but not
	// what users assume on stateful workloads.
	if h.Spec.MinReplicas == nil {
		emit(SeverityInfo, "minReplicas not set",
			"defaults to 1; set explicitly so the autoscaler floor is auditable")
	} else {
		min := *h.Spec.MinReplicas
		if min < 1 {
			emit(SeverityCritical, "minReplicas < 1",
				fmt.Sprintf("minReplicas=%d permits scaling to zero — only valid with KEDA fronting", min))
		}
		if min > h.Spec.MaxReplicas {
			emit(SeverityCritical, "minReplicas > maxReplicas",
				fmt.Sprintf("min=%d > max=%d; HPA will permanently report errors", min, h.Spec.MaxReplicas))
		}
		if min == h.Spec.MaxReplicas && h.Spec.MaxReplicas > 0 {
			emit(SeverityWarning, "min == max replicas",
				"autoscaling has no headroom — the HPA is effectively a static replica count")
		}
	}

	if h.Spec.MaxReplicas <= 0 {
		emit(SeverityCritical, "maxReplicas not set",
			"HPA cannot scale up; set maxReplicas explicitly")
	}

	if len(h.Spec.Metrics) == 0 {
		emit(SeverityCritical, "no metrics configured",
			"HPA must declare at least one metric to drive scaling decisions")
	}

	for i, m := range h.Spec.Metrics {
		switch m.Type {
		case "Resource", "ContainerResource":
			if m.Resource == nil && m.ContainerResource == nil {
				emit(SeverityWarning, fmt.Sprintf("metric[%d] type=%s with no spec", i, m.Type),
					"resource block is required when type is Resource/ContainerResource")
				continue
			}
			if m.Resource != nil {
				targ := m.Resource.Target
				if targ.Type == "Utilization" && targ.AverageUtilization == nil {
					emit(SeverityWarning, fmt.Sprintf("metric[%d] %s utilisation with no target", i, m.Resource.Name),
						"averageUtilization required when target.type is Utilization")
				}
				if targ.Type == "AverageValue" && strings.TrimSpace(targ.AverageValue) == "" {
					emit(SeverityWarning, fmt.Sprintf("metric[%d] %s average-value with no target", i, m.Resource.Name),
						"averageValue required when target.type is AverageValue")
				}
			}
		case "External":
			if m.External == nil || m.External.Metric.Name == "" {
				emit(SeverityWarning, fmt.Sprintf("metric[%d] external metric without a name", i),
					"external.metric.name required for external metric sources")
			}
		case "":
			emit(SeverityWarning, fmt.Sprintf("metric[%d] type missing", i),
				"each metric entry must specify a type (Resource / ContainerResource / External / Pods / Object)")
		}
	}

	// Empty scaleTargetRef.Name = misconfigured.
	if strings.TrimSpace(h.Spec.ScaleTargetRef.Name) == "" {
		emit(SeverityCritical, "scaleTargetRef.name empty",
			"HPA has no workload to scale")
	}

	return out
}

// scaledObjectSpec is the KEDA v1 subset we inspect.
type scaledObjectSpec struct {
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Spec struct {
		ScaleTargetRef struct {
			Name string `json:"name"`
		} `json:"scaleTargetRef"`
		MinReplicaCount  *int `json:"minReplicaCount"`
		MaxReplicaCount  *int `json:"maxReplicaCount"`
		IdleReplicaCount *int `json:"idleReplicaCount"`
		PollingInterval  *int `json:"pollingInterval"`
		CooldownPeriod   *int `json:"cooldownPeriod"`
		Triggers         []struct {
			Type     string            `json:"type"`
			Metadata map[string]string `json:"metadata"`
		} `json:"triggers"`
	} `json:"spec"`
}

func (v *HPAValidator) kedaInstalled(ctx context.Context) (bool, error) {
	out, err := v.client.Run(ctx, "api-resources", "--api-group=keda.sh", "-o", "name")
	if err != nil {
		return false, nil // treat as "not installed" — same convention as Karpenter detector.
	}
	return strings.Contains(out, "scaledobjects"), nil
}

func (v *HPAValidator) listScaledObjects(ctx context.Context) ([]scaledObjectSpec, error) {
	raw, err := v.client.RunJSON(ctx, "get", "scaledobjects.keda.sh", "-A", "-o", "json")
	if err != nil {
		return nil, err
	}
	var list struct {
		Items []scaledObjectSpec `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("parse scaledobjects: %w", err)
	}
	return list.Items, nil
}

func (v *HPAValidator) validateScaledObject(s scaledObjectSpec) []HPAFinding {
	var out []HPAFinding
	emit := func(sev IssueSeverity, issue, detail string) {
		out = append(out, HPAFinding{
			Severity:  sev,
			Resource:  "scaledobject",
			Namespace: s.Metadata.Namespace,
			Name:      s.Metadata.Name,
			Issue:     issue,
			Detail:    detail,
		})
	}

	if strings.TrimSpace(s.Spec.ScaleTargetRef.Name) == "" {
		emit(SeverityCritical, "scaleTargetRef.name empty",
			"ScaledObject has no workload to scale")
	}

	if len(s.Spec.Triggers) == 0 {
		emit(SeverityCritical, "no triggers configured",
			"ScaledObject must declare at least one trigger to drive scaling")
	}
	for i, t := range s.Spec.Triggers {
		if strings.TrimSpace(t.Type) == "" {
			emit(SeverityWarning, fmt.Sprintf("trigger[%d] type missing", i),
				"every trigger must specify a type (e.g. cron, prometheus, kafka)")
		}
	}

	if s.Spec.MinReplicaCount != nil && s.Spec.MaxReplicaCount != nil {
		if *s.Spec.MinReplicaCount > *s.Spec.MaxReplicaCount {
			emit(SeverityCritical, "min > max",
				fmt.Sprintf("min=%d > max=%d; KEDA will permanently log validation errors",
					*s.Spec.MinReplicaCount, *s.Spec.MaxReplicaCount))
		}
	}

	// idleReplicaCount must be strictly less than minReplicaCount when both
	// are set; else KEDA logs a continuous validation error.
	if s.Spec.IdleReplicaCount != nil {
		idle := *s.Spec.IdleReplicaCount
		if idle < 0 {
			emit(SeverityCritical, "idleReplicaCount < 0",
				"idleReplicaCount must be 0 or a positive integer")
		}
		if s.Spec.MinReplicaCount != nil && idle >= *s.Spec.MinReplicaCount {
			emit(SeverityWarning, "idleReplicaCount >= minReplicaCount",
				fmt.Sprintf("idle=%d ≥ min=%d; KEDA requires idle < min for scale-to-idle to engage",
					idle, *s.Spec.MinReplicaCount))
		}
	}

	if s.Spec.PollingInterval != nil && *s.Spec.PollingInterval < 5 {
		emit(SeverityInfo, "pollingInterval < 5s",
			fmt.Sprintf("polling=%ds is unusually aggressive — verify the trigger backend can keep up",
				*s.Spec.PollingInterval))
	}

	return out
}
