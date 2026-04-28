package sre

import (
	"context"
	"strings"
	"testing"
)

// hpaValidatorMock fans kubectl invocations to per-target responses.
type hpaValidatorMock struct {
	hpaList            string
	hpaErr             error
	apiResourcesOutput string
	apiResourcesErr    error
	scaledObjectsList  string
	scaledObjectsErr   error
}

func (m *hpaValidatorMock) Run(_ context.Context, args ...string) (string, error) {
	full := strings.Join(args, " ")
	if strings.Contains(full, "api-resources") {
		return m.apiResourcesOutput, m.apiResourcesErr
	}
	return "", nil
}
func (m *hpaValidatorMock) RunWithNamespace(_ context.Context, _ string, _ ...string) (string, error) {
	return "", nil
}
func (m *hpaValidatorMock) RunJSON(_ context.Context, args ...string) ([]byte, error) {
	full := strings.Join(args, " ")
	switch {
	case strings.Contains(full, "scaledobjects.keda.sh"):
		return []byte(m.scaledObjectsList), m.scaledObjectsErr
	case strings.Contains(full, "get hpa"):
		return []byte(m.hpaList), m.hpaErr
	}
	return []byte(`{"items": []}`), nil
}

func TestValidate_HappyHPA(t *testing.T) {
	v := NewHPAValidator(&hpaValidatorMock{
		hpaList: `{
		  "items": [{
		    "metadata": {"name": "good", "namespace": "prod"},
		    "spec": {
		      "minReplicas": 2, "maxReplicas": 10,
		      "scaleTargetRef": {"kind": "Deployment", "name": "api"},
		      "metrics": [
		        {"type": "Resource", "resource": {"name": "cpu", "target": {"type": "Utilization", "averageUtilization": 60}}}
		      ]
		    }
		  }]
		}`,
	}, false)

	report, err := v.Validate(context.Background())
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if report.HPAsScanned != 1 {
		t.Errorf("HPAsScanned = %d, want 1", report.HPAsScanned)
	}
	if len(report.Findings) != 0 {
		t.Errorf("expected 0 findings, got %+v", report.Findings)
	}
}

func TestValidate_HPAFlagsMisconfig(t *testing.T) {
	v := NewHPAValidator(&hpaValidatorMock{
		hpaList: `{
		  "items": [{
		    "metadata": {"name": "broken", "namespace": "default"},
		    "spec": {
		      "minReplicas": 5, "maxReplicas": 3,
		      "scaleTargetRef": {"kind": "Deployment", "name": ""},
		      "metrics": []
		    }
		  }]
		}`,
	}, false)

	report, err := v.Validate(context.Background())
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}

	issues := map[string]bool{}
	for _, f := range report.Findings {
		issues[f.Issue] = true
	}

	for _, want := range []string{"minReplicas > maxReplicas", "scaleTargetRef.name empty", "no metrics configured"} {
		if !issues[want] {
			t.Errorf("expected issue %q in findings %+v", want, report.Findings)
		}
	}
}

func TestValidate_HPAMinEqualsMaxIsWarning(t *testing.T) {
	v := NewHPAValidator(&hpaValidatorMock{
		hpaList: `{
		  "items": [{
		    "metadata": {"name": "static", "namespace": "default"},
		    "spec": {
		      "minReplicas": 5, "maxReplicas": 5,
		      "scaleTargetRef": {"kind": "Deployment", "name": "x"},
		      "metrics": [{"type": "Resource", "resource": {"name": "cpu", "target": {"type": "Utilization", "averageUtilization": 80}}}]
		    }
		  }]
		}`,
	}, false)
	report, _ := v.Validate(context.Background())
	found := false
	for _, f := range report.Findings {
		if f.Issue == "min == max replicas" && f.Severity == SeverityWarning {
			found = true
		}
	}
	if !found {
		t.Errorf("expected min==max warning, got %+v", report.Findings)
	}
}

func TestValidate_HPAMissingMinReplicasIsInfo(t *testing.T) {
	v := NewHPAValidator(&hpaValidatorMock{
		hpaList: `{
		  "items": [{
		    "metadata": {"name": "implicit", "namespace": "default"},
		    "spec": {
		      "maxReplicas": 5,
		      "scaleTargetRef": {"kind": "Deployment", "name": "x"},
		      "metrics": [{"type": "Resource", "resource": {"name": "cpu", "target": {"type": "Utilization", "averageUtilization": 80}}}]
		    }
		  }]
		}`,
	}, false)
	report, _ := v.Validate(context.Background())
	found := false
	for _, f := range report.Findings {
		if strings.Contains(f.Issue, "minReplicas not set") && f.Severity == SeverityInfo {
			found = true
		}
	}
	if !found {
		t.Errorf("expected minReplicas-not-set info, got %+v", report.Findings)
	}
}

func TestValidate_HPAUtilizationWithoutTargetWarns(t *testing.T) {
	v := NewHPAValidator(&hpaValidatorMock{
		hpaList: `{
		  "items": [{
		    "metadata": {"name": "missing-util", "namespace": "default"},
		    "spec": {
		      "minReplicas": 1, "maxReplicas": 5,
		      "scaleTargetRef": {"kind": "Deployment", "name": "x"},
		      "metrics": [{"type": "Resource", "resource": {"name": "cpu", "target": {"type": "Utilization"}}}]
		    }
		  }]
		}`,
	}, false)
	report, _ := v.Validate(context.Background())
	found := false
	for _, f := range report.Findings {
		if strings.Contains(f.Issue, "utilisation with no target") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected utilisation warning, got %+v", report.Findings)
	}
}

func TestValidate_KEDANotInstalledScansHPAOnly(t *testing.T) {
	v := NewHPAValidator(&hpaValidatorMock{
		hpaList:            `{"items": []}`,
		apiResourcesOutput: "",
	}, false)
	report, _ := v.Validate(context.Background())
	if report.KEDAInstalled {
		t.Error("KEDAInstalled should be false when api-resources returns nothing")
	}
	if report.ScaledObjectsScanned != 0 {
		t.Errorf("ScaledObjectsScanned = %d, want 0", report.ScaledObjectsScanned)
	}
}

func TestValidate_KEDAInstalledScansScaledObjects(t *testing.T) {
	v := NewHPAValidator(&hpaValidatorMock{
		hpaList:            `{"items": []}`,
		apiResourcesOutput: "scaledobjects.keda.sh\n",
		scaledObjectsList: `{
		  "items": [
		    {
		      "metadata": {"name": "good", "namespace": "default"},
		      "spec": {
		        "scaleTargetRef": {"name": "api"},
		        "minReplicaCount": 1, "maxReplicaCount": 10,
		        "triggers": [{"type": "cron", "metadata": {"timezone": "UTC"}}]
		      }
		    },
		    {
		      "metadata": {"name": "broken", "namespace": "default"},
		      "spec": {
		        "scaleTargetRef": {"name": ""},
		        "minReplicaCount": 5, "maxReplicaCount": 1,
		        "triggers": []
		      }
		    },
		    {
		      "metadata": {"name": "idle-bad", "namespace": "default"},
		      "spec": {
		        "scaleTargetRef": {"name": "x"},
		        "minReplicaCount": 1, "maxReplicaCount": 5, "idleReplicaCount": 1,
		        "triggers": [{"type": "prometheus"}]
		      }
		    }
		  ]
		}`,
	}, false)
	report, _ := v.Validate(context.Background())

	if !report.KEDAInstalled {
		t.Fatal("KEDAInstalled should be true")
	}
	if report.ScaledObjectsScanned != 3 {
		t.Errorf("ScaledObjectsScanned = %d, want 3", report.ScaledObjectsScanned)
	}

	issues := map[string]bool{}
	for _, f := range report.Findings {
		if f.Resource == "scaledobject" {
			issues[f.Name+":"+f.Issue] = true
		}
	}

	for _, want := range []string{
		"broken:scaleTargetRef.name empty",
		"broken:no triggers configured",
		"broken:min > max",
		"idle-bad:idleReplicaCount >= minReplicaCount",
	} {
		if !issues[want] {
			t.Errorf("expected scaledobject issue %q in %+v", want, issues)
		}
	}
}

func TestValidate_KEDAAggressivePollingIsInfo(t *testing.T) {
	pi := 1
	min := 1
	max := 5
	_ = pi
	_ = min
	_ = max
	v := NewHPAValidator(&hpaValidatorMock{
		hpaList:            `{"items": []}`,
		apiResourcesOutput: "scaledobjects.keda.sh\n",
		scaledObjectsList: `{
		  "items": [{
		    "metadata": {"name": "fast-poll", "namespace": "default"},
		    "spec": {
		      "scaleTargetRef": {"name": "x"},
		      "minReplicaCount": 1, "maxReplicaCount": 5,
		      "pollingInterval": 1,
		      "triggers": [{"type": "cron"}]
		    }
		  }]
		}`,
	}, false)
	report, _ := v.Validate(context.Background())
	found := false
	for _, f := range report.Findings {
		if strings.Contains(f.Issue, "pollingInterval < 5s") && f.Severity == SeverityInfo {
			found = true
		}
	}
	if !found {
		t.Errorf("expected polling-interval info, got %+v", report.Findings)
	}
}
