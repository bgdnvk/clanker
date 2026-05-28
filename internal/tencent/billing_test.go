package tencent

import (
	"encoding/json"
	"testing"
)

// TestBillByProductReport_JSONRoundTrip pins the JSON envelope shape
// that clanker-cloud's cost.Provider parses. A reshape upstream would
// otherwise silently break the cloud integration.
func TestBillByProductReport_JSONRoundTrip(t *testing.T) {
	in := BillByProductReport{
		Month: "2026-05",
		Items: []BillByProductItem{
			{Product: "CVM", RealCost: "12.50", Cash: "10.00", Voucher: "2.50", Pct: "62.5"},
			{Product: "COS", RealCost: "7.50", Cash: "7.50", Pct: "37.5"},
		},
		Total: 20.0,
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var out BillByProductReport
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v\nwire: %s", err, raw)
	}
	if out.Month != in.Month {
		t.Errorf("month lost: got %q want %q", out.Month, in.Month)
	}
	if len(out.Items) != len(in.Items) {
		t.Fatalf("items length lost: got %d want %d", len(out.Items), len(in.Items))
	}
	if out.Items[0].Product != "CVM" || out.Items[0].RealCost != "12.50" {
		t.Errorf("first item lost: got %+v", out.Items[0])
	}
	if out.Total != 20.0 {
		t.Errorf("total lost: got %v want 20", out.Total)
	}
}

// TestBillTopResourceReport_JSONRoundTrip — same contract test for the
// top-N resource shape.
func TestBillTopResourceReport_JSONRoundTrip(t *testing.T) {
	in := BillTopResourceReport{
		Month: "2026-05",
		Top:   5,
		Items: []BillTopResource{
			{Product: "CVM", ResourceID: "ins-1", Name: "web-a", Region: "ap-singapore", PayMode: "PREPAID", Action: "renew", Cost: "12.50"},
		},
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out BillTopResourceReport
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v\nwire: %s", err, raw)
	}
	if out.Top != 5 || len(out.Items) != 1 || out.Items[0].ResourceID != "ins-1" {
		t.Errorf("round-trip lost data: got %+v", out)
	}
}
