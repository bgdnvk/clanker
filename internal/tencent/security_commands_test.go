package tencent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestSecurityScansRegistry guards two invariants the rest of the
// command surface relies on: every registered scan has a name + run
// function, and the dashboard's documented set of 10 scans is present.
// If a future PR drops or renames a scan, this fails fast so the
// downstream clanker-cloud panel doesn't ship with a dead button.
func TestSecurityScansRegistry(t *testing.T) {
	t.Parallel()

	wantNames := map[string]bool{
		"public-exposure":   true,
		"clb-exposure":      true,
		"db-exposure":       true,
		"idle-eips":         true,
		"unencrypted-cbs":   true,
		"cert-expiry":       true,
		"cam-hygiene":       true,
		"waf-coverage":      true,
		"antiddos-coverage": true,
		"audit-coverage":    true,
	}

	seen := map[string]bool{}
	for _, s := range securityScans {
		if s.name == "" {
			t.Error("scan with empty name in registry")
		}
		if s.run == nil {
			t.Errorf("scan %q has nil run func", s.name)
		}
		if seen[s.name] {
			t.Errorf("scan %q registered twice", s.name)
		}
		seen[s.name] = true
		if !wantNames[s.name] {
			t.Errorf("scan %q not in expected dashboard set", s.name)
		}
	}
	for name := range wantNames {
		if !seen[name] {
			t.Errorf("expected scan %q not registered", name)
		}
	}
}

// TestBuildSecurityCmd verifies the cobra subtree carries one child per
// scan plus the `all` fan-out command, and that cert-expiry advertises
// the --days flag.
func TestBuildSecurityCmd(t *testing.T) {
	t.Parallel()
	region := ""
	cmd := buildSecurityCmd(&region)
	if cmd.Use != "security" {
		t.Errorf("Use = %q, want %q", cmd.Use, "security")
	}

	children := map[string]*cobra.Command{}
	for _, sub := range cmd.Commands() {
		children[sub.Use] = sub
	}

	for _, scan := range securityScans {
		if _, ok := children[scan.name]; !ok {
			t.Errorf("subcommand %q missing", scan.name)
		}
	}
	if _, ok := children["all"]; !ok {
		t.Fatal(`subcommand "all" missing`)
	}
	if children["cert-expiry"].Flag("days") == nil {
		t.Error(`cert-expiry should advertise --days flag`)
	}
}

// TestRunAllSecurityScans_CapturesPerScanErrors confirms the fan-out
// path returns a wrapped envelope where individual failures are
// surfaced in the per-scan `error` field rather than aborting the
// bundle. Passes a fake registry through the parameter so the test
// is race-clean alongside the other parallel tests.
func TestRunAllSecurityScans_CapturesPerScanErrors(t *testing.T) {
	t.Parallel()

	fake := []securityScan{
		{
			name: "ok-scan",
			run: func(_ context.Context, _ *Client, _ string, _ int) (string, error) {
				return `{"items":[1,2,3]}`, nil
			},
		},
		{
			name: "failing-scan",
			run: func(_ context.Context, _ *Client, _ string, _ int) (string, error) {
				return "", errors.New("permission denied")
			},
		},
		{
			name: "bad-json-scan",
			run: func(_ context.Context, _ *Client, _ string, _ int) (string, error) {
				return "not json", nil
			},
		},
	}

	var buf strings.Builder
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	if err := runAllSecurityScans(context.Background(), nil, "ap-singapore", 30, fake, cmd); err != nil {
		t.Fatal(err)
	}

	var got struct {
		Region string          `json:"region"`
		Scans  []allScanResult `json:"scans"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &got); err != nil {
		t.Fatalf("envelope is not valid JSON: %v", err)
	}
	if got.Region != "ap-singapore" {
		t.Errorf("Region = %q, want ap-singapore", got.Region)
	}
	if len(got.Scans) != 3 {
		t.Fatalf("len(Scans) = %d, want 3", len(got.Scans))
	}

	byName := map[string]allScanResult{}
	for _, s := range got.Scans {
		byName[s.Name] = s
	}
	if byName["ok-scan"].Error != "" {
		t.Errorf("ok-scan should not have an error; got %q", byName["ok-scan"].Error)
	}
	if len(byName["ok-scan"].Data) == 0 {
		t.Error("ok-scan should carry a Data payload")
	}
	if byName["failing-scan"].Error == "" {
		t.Error("failing-scan should surface its error")
	}
	if byName["bad-json-scan"].Error == "" {
		t.Error("bad-json-scan should be flagged as non-JSON")
	}
}
