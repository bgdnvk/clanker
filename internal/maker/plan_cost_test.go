package maker

import "testing"

func TestEstimatePlanCost_NilPlan(t *testing.T) {
	out := EstimatePlanCost(nil)
	if out == nil || out.MonthlyUSD != 0 || len(out.Items) != 0 {
		t.Errorf("nil plan should return empty estimate, got %+v", out)
	}
}

func TestEstimatePlanCost_AWSEC2RunInstances(t *testing.T) {
	plan := &Plan{
		Commands: []Command{
			{Args: []string{"ec2", "run-instances", "--instance-type", "m5.xlarge", "--count", "2", "--image-id", "ami-1"}},
		},
	}
	out := EstimatePlanCost(plan)
	if len(out.Items) != 1 {
		t.Fatalf("expected 1 item, got %+v", out.Items)
	}
	item := out.Items[0]
	if item.Provider != "aws" || item.Resource != "ec2" || item.Family != "m5.xlarge" {
		t.Errorf("provider/resource/family = %s/%s/%s, want aws/ec2/m5.xlarge", item.Provider, item.Resource, item.Family)
	}
	if item.Count != 2 {
		t.Errorf("count = %d, want 2", item.Count)
	}
	if !item.PriceKnown {
		t.Error("m5.xlarge should be priced")
	}
	// 0.192/hr × 730 × 2 = 280.32
	if delta := out.MonthlyUSD - 280.32; delta < -0.01 || delta > 0.01 {
		t.Errorf("MonthlyUSD = %v, want ~280.32", out.MonthlyUSD)
	}
}

func TestEstimatePlanCost_RDSCreateDB(t *testing.T) {
	plan := &Plan{
		Commands: []Command{
			{Args: []string{"rds", "create-db-instance", "--db-instance-class", "db.t3.medium", "--engine", "postgres"}},
		},
	}
	out := EstimatePlanCost(plan)
	if len(out.Items) != 1 || out.Items[0].Family != "db.t3.medium" {
		t.Fatalf("RDS not classified, got %+v", out.Items)
	}
	// 0.068/hr × 730 = 49.64
	if delta := out.MonthlyUSD - 49.64; delta < -0.01 || delta > 0.01 {
		t.Errorf("MonthlyUSD = %v, want ~49.64", out.MonthlyUSD)
	}
}

func TestEstimatePlanCost_LambdaIsMetered(t *testing.T) {
	plan := &Plan{
		Commands: []Command{
			{Args: []string{"lambda", "create-function", "--function-name", "f"}},
		},
	}
	out := EstimatePlanCost(plan)
	if len(out.Items) != 1 || out.Items[0].PriceKnown {
		t.Errorf("lambda should be metered (PriceKnown=false), got %+v", out.Items)
	}
	if out.MonthlyUSD != 0 {
		t.Errorf("metered item should not contribute fixed cost, got MonthlyUSD=%v", out.MonthlyUSD)
	}
	if out.UnknownPriceItems != 1 {
		t.Errorf("UnknownPriceItems = %d, want 1", out.UnknownPriceItems)
	}
}

func TestEstimatePlanCost_HetznerCreate(t *testing.T) {
	plan := &Plan{
		Commands: []Command{
			{Args: []string{"hcloud", "server", "create", "--type", "cx21", "--image", "ubuntu-24.04"}},
		},
	}
	out := EstimatePlanCost(plan)
	if len(out.Items) != 1 || out.Items[0].Provider != "hetzner" {
		t.Fatalf("Hetzner not classified, got %+v", out.Items)
	}
	if !out.Items[0].PriceKnown {
		t.Error("cx21 should be priced")
	}
}

func TestEstimatePlanCost_DigitalOceanDroplet(t *testing.T) {
	plan := &Plan{
		Commands: []Command{
			{Args: []string{"doctl", "compute", "droplet", "create", "web", "--size", "s-2vcpu-4gb", "--image", "ubuntu-24-04-x64"}},
		},
	}
	out := EstimatePlanCost(plan)
	if len(out.Items) != 1 || out.Items[0].Family != "s-2vcpu-4gb" {
		t.Fatalf("droplet not classified, got %+v", out.Items)
	}
}

func TestEstimatePlanCost_GCPMachineTypeFlagEqualsForm(t *testing.T) {
	plan := &Plan{
		Commands: []Command{
			{Args: []string{"gcloud", "compute", "instances", "create", "vm-1", "--machine-type=e2-small"}},
		},
	}
	out := EstimatePlanCost(plan)
	if len(out.Items) != 1 || out.Items[0].Family != "e2-small" {
		t.Fatalf("e2-small not parsed (--key=value form), got %+v", out.Items)
	}
}

func TestEstimatePlanCost_UnknownInstanceTypeIsCounted(t *testing.T) {
	plan := &Plan{
		Commands: []Command{
			{Args: []string{"ec2", "run-instances", "--instance-type", "made-up.huge"}},
		},
	}
	out := EstimatePlanCost(plan)
	if len(out.Items) != 1 {
		t.Fatalf("unknown instance type should still record an item, got %+v", out.Items)
	}
	if out.Items[0].PriceKnown {
		t.Error("made-up.huge should not be PriceKnown")
	}
	if out.UnknownPriceItems != 1 {
		t.Errorf("UnknownPriceItems = %d, want 1", out.UnknownPriceItems)
	}
	if out.MonthlyUSD != 0 {
		t.Errorf("unknown price → 0 cost, got %v", out.MonthlyUSD)
	}
	// A note should be added.
	foundNote := false
	for _, n := range out.Notes {
		if n != "" {
			foundNote = true
			break
		}
	}
	if !foundNote {
		t.Errorf("expected notes about unknown prices, got %+v", out.Notes)
	}
}

func TestEstimatePlanCost_UnrecognisedCommandsCounted(t *testing.T) {
	plan := &Plan{
		Commands: []Command{
			{Args: []string{"iam", "create-role", "--role-name", "r1"}},
			{Args: []string{"ec2", "describe-instances"}},
			{Args: []string{"ec2", "run-instances", "--instance-type", "t3.micro"}},
		},
	}
	out := EstimatePlanCost(plan)
	if out.UnestimatedCommands != 2 {
		t.Errorf("UnestimatedCommands = %d, want 2", out.UnestimatedCommands)
	}
	if len(out.Items) != 1 {
		t.Errorf("only the run-instances command should produce an item, got %+v", out.Items)
	}
}

func TestEstimatePlanCost_SortedByMonthlyDesc(t *testing.T) {
	plan := &Plan{
		Commands: []Command{
			{Args: []string{"ec2", "run-instances", "--instance-type", "t3.micro"}},
			{Args: []string{"ec2", "run-instances", "--instance-type", "m5.xlarge"}},
			{Args: []string{"ec2", "run-instances", "--instance-type", "t3.small"}},
		},
	}
	out := EstimatePlanCost(plan)
	if len(out.Items) != 3 {
		t.Fatalf("expected 3 items, got %+v", out.Items)
	}
	if out.Items[0].Family != "m5.xlarge" {
		t.Errorf("highest cost should sort first; got order: %v / %v / %v",
			out.Items[0].Family, out.Items[1].Family, out.Items[2].Family)
	}
}

func TestPlanCostFlagValue(t *testing.T) {
	cases := []struct {
		args []string
		name string
		want string
	}{
		{[]string{"--instance-type", "m5.xlarge"}, "--instance-type", "m5.xlarge"},
		{[]string{"--instance-type=m5.xlarge"}, "--instance-type", "m5.xlarge"},
		{[]string{"--other", "x"}, "--instance-type", ""},
		{[]string{"--instance-type"}, "--instance-type", ""}, // missing value
	}
	for _, c := range cases {
		if got := planCostFlagValue(c.args, c.name); got != c.want {
			t.Errorf("flagValue(%v, %q) = %q, want %q", c.args, c.name, got, c.want)
		}
	}
}

func TestPlanCostIntFlag_Defaults(t *testing.T) {
	if v := planCostIntFlag([]string{}, "--count", 1); v != 1 {
		t.Errorf("missing flag should use default, got %d", v)
	}
	if v := planCostIntFlag([]string{"--count", "0"}, "--count", 1); v != 1 {
		t.Errorf("zero value should use default, got %d", v)
	}
	if v := planCostIntFlag([]string{"--count", "abc"}, "--count", 1); v != 1 {
		t.Errorf("non-numeric should use default, got %d", v)
	}
	if v := planCostIntFlag([]string{"--count", "5"}, "--count", 1); v != 5 {
		t.Errorf("normal parse, got %d", v)
	}
}
