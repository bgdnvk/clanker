package maker

import (
	"strings"
	"testing"
)

func TestValidateFlyioCommand_BlocksNonFlyctl(t *testing.T) {
	cases := [][]string{
		{},
		{"rm", "-rf", "/"},
		{"git", "push"},
		{"docker", "run"},
		{"FLY_API_TOKEN=oops"},
	}
	for _, args := range cases {
		if err := validateFlyioCommand(args, false); err == nil {
			t.Errorf("expected error for %v", args)
		}
	}
}

func TestValidateFlyioCommand_AcceptsFlyctlAndFlyAlias(t *testing.T) {
	if err := validateFlyioCommand([]string{"flyctl", "list", "apps"}, false); err != nil {
		t.Errorf("flyctl list apps should pass, got: %v", err)
	}
	if err := validateFlyioCommand([]string{"fly", "list", "apps"}, false); err != nil {
		t.Errorf("fly list apps should pass, got: %v", err)
	}
}

func TestValidateFlyioCommand_BlocksAuthLogout(t *testing.T) {
	if err := validateFlyioCommand([]string{"flyctl", "auth", "logout"}, true); err == nil {
		t.Error("flyctl auth logout must always be blocked")
	}
}

func TestValidateFlyioCommand_BlocksDestructiveWithoutDestroyer(t *testing.T) {
	cases := [][]string{
		{"flyctl", "machine", "destroy", "m-1"},
		{"flyctl", "apps", "destroy", "my-app"},
		{"flyctl", "volumes", "remove", "vol-1"},
		{"flyctl", "secrets", "delete", "KEY"},
	}
	for _, args := range cases {
		if err := validateFlyioCommand(args, false); err == nil {
			t.Errorf("expected destructive block for %v", args)
		}
		if err := validateFlyioCommand(args, true); err != nil {
			t.Errorf("destroyer mode should allow %v, got: %v", args, err)
		}
	}
}

func TestValidateFlyioCommand_BlocksForceFlagWithoutDestroyer(t *testing.T) {
	args := []string{"flyctl", "machine", "destroy", "m-1", "--force"}
	if err := validateFlyioCommand(args, false); err == nil {
		t.Error("--force must be blocked outside destroyer mode")
	}
	if err := validateFlyioCommand(args, true); err != nil {
		t.Errorf("--force should be allowed in destroyer mode: %v", err)
	}
}

func TestValidateFlyioCommand_BlocksShellOperators(t *testing.T) {
	cases := [][]string{
		{"flyctl", "logs", ";", "rm", "-rf", "/"},
		{"flyctl", "deploy", "|", "tee"},
		{"flyctl", "ssh", "console", "--app", "x", "&&", "evil"},
		{"flyctl", "list", "apps\nDROP TABLE"},
	}
	for _, args := range cases {
		if err := validateFlyioCommand(args, true); err == nil {
			t.Errorf("expected shell-operator block for %v", args)
		}
	}
}

func TestIsFlyioSecretsSetCommand(t *testing.T) {
	if !isFlyioSecretsSetCommand([]string{"flyctl", "secrets", "set", "X=Y"}) {
		t.Error("flyctl secrets set should match")
	}
	if !isFlyioSecretsSetCommand([]string{"fly", "secrets", "set", "X=Y"}) {
		t.Error("fly secrets set should match")
	}
	if isFlyioSecretsSetCommand([]string{"flyctl", "secrets", "unset", "X"}) {
		t.Error("flyctl secrets unset must not match")
	}
	if isFlyioSecretsSetCommand([]string{"flyctl", "deploy"}) {
		t.Error("flyctl deploy must not match")
	}
}

func TestExtractFlyioSecretValues_KeyEqualsValue(t *testing.T) {
	args := []string{"flyctl", "secrets", "set", "FOO=bar", "BAZ=qux", "--app", "myapp"}
	stdin, scrubbed := extractFlyioSecretValues(args)

	if stdin != "FOO=bar\nBAZ=qux\n" {
		t.Errorf("stdin = %q, want FOO=bar\\nBAZ=qux\\n", stdin)
	}
	wantScrubbed := []string{"flyctl", "secrets", "set", "--app", "myapp"}
	if len(scrubbed) != len(wantScrubbed) {
		t.Fatalf("scrubbed len = %d, want %d (%v)", len(scrubbed), len(wantScrubbed), scrubbed)
	}
	for i := range wantScrubbed {
		if scrubbed[i] != wantScrubbed[i] {
			t.Errorf("scrubbed[%d] = %q, want %q", i, scrubbed[i], wantScrubbed[i])
		}
	}
	for _, a := range scrubbed {
		if strings.Contains(a, "bar") || strings.Contains(a, "qux") {
			t.Errorf("scrubbed args still contain a value: %v", scrubbed)
		}
	}
}

func TestExtractFlyioSecretValues_BareKeyValue(t *testing.T) {
	args := []string{"flyctl", "secrets", "set", "FOO", "barvalue"}
	stdin, scrubbed := extractFlyioSecretValues(args)
	if stdin != "FOO=barvalue\n" {
		t.Errorf("stdin = %q, want FOO=barvalue\\n", stdin)
	}
	want := []string{"flyctl", "secrets", "set"}
	if len(scrubbed) != len(want) {
		t.Fatalf("scrubbed = %v, want %v", scrubbed, want)
	}
}

func TestExtractFlyioSecretValues_NoValues(t *testing.T) {
	args := []string{"flyctl", "secrets", "set", "--app", "myapp"}
	stdin, scrubbed := extractFlyioSecretValues(args)
	if stdin != "" {
		t.Errorf("expected empty stdin when no values to lift, got %q", stdin)
	}
	if len(scrubbed) != len(args) {
		t.Errorf("scrubbed should pass through when no values, got %v", scrubbed)
	}
}

func TestFormatFlyioArgsForLog_MasksSecretValues(t *testing.T) {
	args := []string{"flyctl", "secrets", "set", "FOO=supersecret", "BAR=alsosecret", "--app", "x"}
	log := formatFlyioArgsForLog(args)
	if strings.Contains(log, "supersecret") || strings.Contains(log, "alsosecret") {
		t.Errorf("log line leaked a secret value: %q", log)
	}
	if !strings.Contains(log, "FOO=***") || !strings.Contains(log, "BAR=***") {
		t.Errorf("log should mask with KEY=*** form: %q", log)
	}
}

func TestFormatFlyioArgsForLog_PreservesShape(t *testing.T) {
	args := []string{"flyctl", "list", "apps"}
	got := formatFlyioArgsForLog(args)
	want := "flyctl list apps"
	if got != want {
		t.Errorf("formatFlyioArgsForLog = %q, want %q", got, want)
	}
}
