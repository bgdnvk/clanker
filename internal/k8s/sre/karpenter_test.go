package sre

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// karpenterMock dispatches to per-arg responses so a single test can return
// different data for `api-resources`, `get nodepools.karpenter.sh ...`, and
// `get nodeclaims.karpenter.sh ...` calls.
type karpenterMock struct {
	apiResourcesOut string
	apiResourcesErr error
	nodePoolsJSON   []byte
	nodePoolsErr    error
	nodeClaimsJSON  []byte
	nodeClaimsErr   error
}

func (m *karpenterMock) Run(_ context.Context, args ...string) (string, error) {
	if len(args) > 0 && args[0] == "api-resources" {
		return m.apiResourcesOut, m.apiResourcesErr
	}
	return "", nil
}
func (m *karpenterMock) RunWithNamespace(_ context.Context, _ string, _ ...string) (string, error) {
	return "", nil
}
func (m *karpenterMock) RunJSON(_ context.Context, args ...string) ([]byte, error) {
	full := strings.Join(args, " ")
	switch {
	case strings.Contains(full, "nodepools.karpenter.sh"):
		return m.nodePoolsJSON, m.nodePoolsErr
	case strings.Contains(full, "nodeclaims.karpenter.sh"):
		return m.nodeClaimsJSON, m.nodeClaimsErr
	}
	return nil, nil
}

func TestKarpenterDetect_NotInstalled(t *testing.T) {
	d := NewKarpenterDetector(&karpenterMock{
		apiResourcesErr: errors.New("the server doesn't have a resource type \"karpenter.sh\""),
	}, false)
	got, err := d.Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect should not propagate detection errors: %v", err)
	}
	if got.Installed {
		t.Errorf("expected Installed=false when api-resources errors, got %+v", got)
	}
	if got.Notes == "" {
		t.Error("expected explanatory Notes when not installed")
	}
}

func TestKarpenterDetect_BothCRDsPresent(t *testing.T) {
	d := NewKarpenterDetector(&karpenterMock{
		apiResourcesOut: "nodepools.karpenter.sh\nnodeclaims.karpenter.sh\nec2nodeclasses.karpenter.k8s.aws\n",
	}, false)
	got, err := d.Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !got.Installed || !got.NodePoolsAvailable || !got.NodeClaimsAvailable {
		t.Errorf("expected Installed + both CRDs available, got %+v", got)
	}
	if got.APIGroup != "karpenter.sh" {
		t.Errorf("expected APIGroup karpenter.sh, got %q", got.APIGroup)
	}
}

func TestKarpenterDetect_OnlyNodePools(t *testing.T) {
	d := NewKarpenterDetector(&karpenterMock{
		apiResourcesOut: "nodepools.karpenter.sh\n",
	}, false)
	got, _ := d.Detect(context.Background())
	if !got.NodePoolsAvailable || got.NodeClaimsAvailable {
		t.Errorf("expected only NodePools available, got %+v", got)
	}
}

const samplePoolsJSON = `{
  "items": [
    {
      "metadata": {
        "name": "default",
        "creationTimestamp": "2026-04-01T00:00:00Z",
        "labels": {"team": "platform"}
      },
      "spec": {
        "limits": {"cpu": "1000", "memory": "4000Gi"},
        "weight": 10,
        "template": {
          "spec": {
            "nodeClassRef": {"name": "default-ec2"},
            "taints": [{"key": "spot", "effect": "NoSchedule"}]
          }
        },
        "disruption": {"consolidationPolicy": "WhenUnderutilized"}
      }
    },
    {
      "metadata": {"name": "gpu", "creationTimestamp": "2026-04-15T00:00:00Z"},
      "spec": {
        "weight": 100,
        "template": {"spec": {"nodeClassRef": {"name": "gpu-ec2"}}},
        "disruption": {"consolidationPolicy": "WhenEmpty"}
      }
    }
  ]
}`

func TestListNodePools_ParsesAllFields(t *testing.T) {
	d := NewKarpenterDetector(&karpenterMock{nodePoolsJSON: []byte(samplePoolsJSON)}, false)

	pools, err := d.ListNodePools(context.Background())
	if err != nil {
		t.Fatalf("ListNodePools: %v", err)
	}
	if len(pools) != 2 {
		t.Fatalf("expected 2 NodePools, got %d", len(pools))
	}

	byName := map[string]NodePoolSummary{}
	for _, p := range pools {
		byName[p.Name] = p
	}

	def := byName["default"]
	if def.NodeClass != "default-ec2" {
		t.Errorf("default NodeClass = %q, want default-ec2", def.NodeClass)
	}
	if def.Weight != 10 {
		t.Errorf("default Weight = %d, want 10", def.Weight)
	}
	if def.Disruption != "WhenUnderutilized" {
		t.Errorf("default Disruption = %q", def.Disruption)
	}
	if def.Limits["cpu"] != "1000" {
		t.Errorf("default Limits[cpu] = %q, want 1000", def.Limits["cpu"])
	}
	if len(def.Taints) != 1 || def.Taints[0] != "spot:NoSchedule" {
		t.Errorf("default Taints = %v, want [spot:NoSchedule]", def.Taints)
	}
	if def.Labels["team"] != "platform" {
		t.Errorf("default Labels[team] = %q, want platform", def.Labels["team"])
	}
	if def.Age == "" || def.CreatedAt.IsZero() {
		t.Errorf("default Age/CreatedAt should be populated, got Age=%q CreatedAt=%v", def.Age, def.CreatedAt)
	}
}

func TestListNodePools_KarpenterNotInstalled(t *testing.T) {
	// kubectl error like `the server doesn't have a resource type ...`
	d := NewKarpenterDetector(&karpenterMock{
		nodePoolsErr: errors.New("the server doesn't have a resource type \"nodepools.karpenter.sh\""),
	}, false)
	pools, err := d.ListNodePools(context.Background())
	if err != nil {
		t.Errorf("missing-CRD should return (nil, nil), got err=%v", err)
	}
	if pools != nil {
		t.Errorf("expected nil slice when Karpenter missing, got %v", pools)
	}
}

func TestListNodePools_RealError(t *testing.T) {
	// Permission denied or similar real failure must propagate.
	d := NewKarpenterDetector(&karpenterMock{
		nodePoolsErr: errors.New("Error from server (Forbidden): nodepools.karpenter.sh is forbidden"),
	}, false)
	_, err := d.ListNodePools(context.Background())
	if err == nil {
		t.Error("real kubectl error must propagate")
	}
}

const sampleClaimsJSON = `{
  "items": [
    {
      "metadata": {
        "name": "claim-abc",
        "creationTimestamp": "2026-04-20T10:00:00Z",
        "labels": {"karpenter.sh/nodepool": "default"}
      },
      "status": {
        "capacity": {"cpu": "4", "memory": "16Gi"},
        "nodeName": "ip-10-0-1-23.ec2.internal",
        "providerID": "aws:///us-east-1a/i-0abc123def",
        "conditions": [{"type": "Ready", "status": "True"}]
      }
    },
    {
      "metadata": {"name": "claim-pending", "labels": {"karpenter.sh/nodepool": "gpu"}},
      "status": {
        "providerID": "aws:///us-east-1a/i-pending",
        "conditions": [{"type": "Ready", "status": "False"}, {"type": "Launched", "status": "True"}]
      }
    }
  ]
}`

func TestListNodeClaims_ParsesAndDerivesStatus(t *testing.T) {
	d := NewKarpenterDetector(&karpenterMock{nodeClaimsJSON: []byte(sampleClaimsJSON)}, false)

	claims, err := d.ListNodeClaims(context.Background())
	if err != nil {
		t.Fatalf("ListNodeClaims: %v", err)
	}
	if len(claims) != 2 {
		t.Fatalf("expected 2 NodeClaims, got %d", len(claims))
	}

	byName := map[string]NodeClaimSummary{}
	for _, c := range claims {
		byName[c.Name] = c
	}

	abc := byName["claim-abc"]
	if abc.Status != "Ready" {
		t.Errorf("claim-abc Status = %q, want Ready", abc.Status)
	}
	if abc.NodePool != "default" {
		t.Errorf("claim-abc NodePool = %q, want default", abc.NodePool)
	}
	if abc.NodeName != "ip-10-0-1-23.ec2.internal" {
		t.Errorf("claim-abc NodeName = %q", abc.NodeName)
	}
	if abc.InstanceID != "i-0abc123def" {
		t.Errorf("claim-abc InstanceID = %q, want i-0abc123def", abc.InstanceID)
	}
	if abc.Capacity["cpu"] != "4" {
		t.Errorf("claim-abc Capacity[cpu] = %q", abc.Capacity["cpu"])
	}

	pending := byName["claim-pending"]
	if pending.Status != "Pending" {
		t.Errorf("claim-pending Status = %q, want Pending (Ready=False even though Launched=True)", pending.Status)
	}
	if pending.InstanceID != "i-pending" {
		t.Errorf("claim-pending InstanceID = %q", pending.InstanceID)
	}
}

func TestProviderInstanceID(t *testing.T) {
	cases := map[string]string{
		"aws:///us-east-1a/i-0abc123def": "i-0abc123def",
		"i-bare":                         "i-bare",
		"":                               "",
		"gce://project/zone/instance":    "instance",
	}
	for in, want := range cases {
		if got := providerInstanceID(in); got != want {
			t.Errorf("providerInstanceID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHumanAge(t *testing.T) {
	cases := []struct {
		in  string
		out string
	}{
		{"30s", "30s"},
		{"5m", "5m"},
		{"3h", "3h"},
		{"50h", "2d"},
		{"-1m", "0s"},
	}
	for _, c := range cases {
		d, err := time.ParseDuration(c.in)
		if err != nil {
			t.Fatalf("parse %q: %v", c.in, err)
		}
		if got := humanAge(d); got != c.out {
			t.Errorf("humanAge(%v) = %q, want %q", d, got, c.out)
		}
	}
}

func TestIsMissingResource(t *testing.T) {
	cases := map[string]bool{
		// Genuine "CRD not installed" signals from kubectl
		"the server doesn't have a resource type \"nodepools.karpenter.sh\"": true,
		"no matches for kind \"NodePool\" in version \"karpenter.sh/v1\"":    true,

		// Real failures must NOT be classified as missing
		"Error from server (Forbidden)": false,
		"connection refused":            false,
		"":                              false,

		// Regression: "no resources found" used to match this predicate,
		// which mis-classified an empty-but-installed Karpenter cluster as
		// "not installed". Must NOT match.
		"no resources found in default namespace": false,
	}
	for msg, want := range cases {
		var err error
		if msg != "" {
			err = errors.New(msg)
		}
		if got := isMissingResource(err); got != want {
			t.Errorf("isMissingResource(%q) = %v, want %v", msg, got, want)
		}
	}
}

// Regression: an empty-but-installed Karpenter cluster used to surface as
// "not installed" because the "no resources found" string was mis-handled.
// This test exercises the integration: the mock returns an empty items list
// (not an error), so ListNodePools should yield an empty slice, NOT nil.
func TestListNodePools_EmptyButInstalledCluster(t *testing.T) {
	d := NewKarpenterDetector(&karpenterMock{
		nodePoolsJSON: []byte(`{"items": []}`),
	}, false)
	pools, err := d.ListNodePools(context.Background())
	if err != nil {
		t.Fatalf("empty list should not error: %v", err)
	}
	if pools == nil {
		t.Fatal("empty installed cluster should return empty slice, not nil")
	}
	if len(pools) != 0 {
		t.Errorf("expected 0 pools for empty cluster, got %d", len(pools))
	}
}

func TestHumanAge_BoundaryAt24h(t *testing.T) {
	// Exactly 24h must render as 1d, not 24h, to match `kubectl get`.
	got := humanAge(24 * time.Hour)
	if got != "1d" {
		t.Errorf("humanAge(24h) = %q, want \"1d\"", got)
	}
}
