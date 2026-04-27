package cost

import (
	"strings"
	"testing"
	"time"
)

func TestFormatTagAudit_NilReport(t *testing.T) {
	f := NewFormatter("table", false)
	got, err := f.FormatTagAudit(nil)
	if err != nil {
		t.Fatalf("FormatTagAudit(nil): %v", err)
	}
	if !strings.Contains(got, "No tag data available") {
		t.Errorf("expected empty-data message, got %q", got)
	}
}

func TestFormatTagAudit_EmptyEntries(t *testing.T) {
	f := NewFormatter("table", false)
	got, err := f.FormatTagAudit(&TagAuditReport{
		Period: CostPeriod{StartDate: time.Now().AddDate(0, 0, -7), EndDate: time.Now()},
	})
	if err != nil {
		t.Fatalf("FormatTagAudit empty: %v", err)
	}
	if !strings.Contains(got, "No tag data available") {
		t.Errorf("expected empty-data message, got %q", got)
	}
}

func TestFormatTagAudit_PartialUntaggedFlagsHighOnes(t *testing.T) {
	f := NewFormatter("table", false)
	report := &TagAuditReport{
		Entries: []TagAuditEntry{
			{TagKey: "Environment", TotalCost: 100, UntaggedCost: 10, UntaggedPct: 10, TaggedValues: 3, UnsupportedNum: 0},
			{TagKey: "Owner", TotalCost: 200, UntaggedCost: 150, UntaggedPct: 75, TaggedValues: 2, UnsupportedNum: 0},
			{TagKey: "Project", TotalCost: 50, UntaggedCost: 50, UntaggedPct: 100, TaggedValues: 0, UnsupportedNum: 0},
		},
	}

	got, err := f.FormatTagAudit(report)
	if err != nil {
		t.Fatalf("FormatTagAudit: %v", err)
	}

	// Owner (75%) and Project (100%) should be flagged with the warning
	// marker; Environment (10%) should not.
	envLine := lineContaining(got, "Environment")
	ownerLine := lineContaining(got, "Owner")
	projectLine := lineContaining(got, "Project")

	if envLine == "" || ownerLine == "" || projectLine == "" {
		t.Fatalf("missing rows in output:\n%s", got)
	}

	if strings.Contains(envLine, "⚠") {
		t.Errorf("Environment (10%% untagged) should NOT have warning marker: %q", envLine)
	}
	if !strings.Contains(ownerLine, "⚠") {
		t.Errorf("Owner (75%% untagged) should have warning marker: %q", ownerLine)
	}
	if !strings.Contains(projectLine, "⚠") {
		t.Errorf("Project (100%% untagged) should have warning marker: %q", projectLine)
	}
}

func TestFormatTagAudit_UnsupportedFooter(t *testing.T) {
	f := NewFormatter("table", false)
	withUnsupported := &TagAuditReport{
		Entries: []TagAuditEntry{
			{TagKey: "Environment", TotalCost: 100, UnsupportedNum: 2},
		},
	}
	got, err := f.FormatTagAudit(withUnsupported)
	if err != nil {
		t.Fatalf("FormatTagAudit: %v", err)
	}
	if !strings.Contains(got, "2 configured provider(s) do not support tag-based cost queries") {
		t.Errorf("expected unsupported footer, got: %s", got)
	}

	noUnsupported := &TagAuditReport{
		Entries: []TagAuditEntry{
			{TagKey: "Environment", TotalCost: 100, UnsupportedNum: 0},
		},
	}
	got2, _ := f.FormatTagAudit(noUnsupported)
	if strings.Contains(got2, "do not support tag-based cost queries") {
		t.Errorf("should not emit footer when UnsupportedNum=0, got: %s", got2)
	}
}

func TestFormatTagAudit_JSONFormatRoundtrips(t *testing.T) {
	f := NewFormatter("json", false)
	report := &TagAuditReport{
		Entries: []TagAuditEntry{{TagKey: "Environment", TotalCost: 100, UntaggedPct: 10}},
	}
	got, err := f.FormatTagAudit(report)
	if err != nil {
		t.Fatalf("FormatTagAudit json: %v", err)
	}
	if !strings.Contains(got, `"tagKey": "Environment"`) {
		t.Errorf("expected JSON output with tagKey field, got: %s", got)
	}
}

func lineContaining(s, want string) string {
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, want) {
			return line
		}
	}
	return ""
}
