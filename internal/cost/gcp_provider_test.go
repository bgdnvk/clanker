package cost

import "testing"

func TestBigQueryBillingTableRef(t *testing.T) {
	got, err := bigQueryBillingTableRef("my-project", "billing_export", "gcp_billing_export_v1")
	if err != nil {
		t.Fatalf("bigQueryBillingTableRef: %v", err)
	}
	if got != "`my-project.billing_export.gcp_billing_export_v1`" {
		t.Fatalf("table ref = %q", got)
	}

	if _, err := bigQueryBillingTableRef("my-project", "billing;DROP", "table"); err == nil {
		t.Fatal("expected invalid dataset error")
	}
	if _, err := bigQueryBillingTableRef("my-project", "billing", "table`x"); err == nil {
		t.Fatal("expected invalid table error")
	}
}

func TestBigQueryStringLiteral(t *testing.T) {
	if got := bigQueryStringLiteral("env'prod"); got != "env''prod" {
		t.Fatalf("escaped literal = %q", got)
	}
}
