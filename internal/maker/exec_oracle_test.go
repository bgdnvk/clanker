package maker

import "testing"

func TestValidateOracleCommand(t *testing.T) {
	if err := validateOracleCommand([]string{"oci", "compute", "instance", "list", "--compartment-id", "ocid1.compartment.oc1..x"}, false); err != nil {
		t.Fatalf("valid list command rejected: %v", err)
	}
	if err := validateOracleCommand([]string{"aws", "ec2", "describe-instances"}, false); err == nil {
		t.Fatal("expected non-oci command to be rejected")
	}
	if err := validateOracleCommand([]string{"oci", "compute", "instance", "terminate", "--instance-id", "ocid1.instance.oc1..x"}, false); err == nil {
		t.Fatal("expected destructive command to be rejected without destroyer")
	}
	if err := validateOracleCommand([]string{"oci", "compute", "instance", "terminate", "--instance-id", "ocid1.instance.oc1..x"}, true); err != nil {
		t.Fatalf("destructive command with destroyer rejected: %v", err)
	}
	if err := validateOracleCommand([]string{"oci", "compute", "instance", "list", "&&", "echo", "bad"}, true); err == nil {
		t.Fatal("expected shell operator to be rejected")
	}
}
