package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParsePortPair(t *testing.T) {
	cases := []struct {
		in      string
		wantErr bool
	}{
		{"8080:80", false},
		{"0:80", false},
		{":80", false},
		{"8080", true},
		{"8080:", true},
		{"", true},
	}
	for _, c := range cases {
		_, err := parsePortPair(c.in)
		if c.wantErr && err == nil {
			t.Errorf("parsePortPair(%q) expected error, got nil", c.in)
		}
		if !c.wantErr && err != nil {
			t.Errorf("parsePortPair(%q) unexpected error: %v", c.in, err)
		}
	}
}

func TestNormalizePFTarget(t *testing.T) {
	cases := []struct {
		raw  string
		kind string
		want string
	}{
		{"my-pod", "pod", "pod/my-pod"},
		{"my-pod", "", "pod/my-pod"},
		{"my-svc", "svc", "svc/my-svc"},
		{"my-svc", "service", "svc/my-svc"},
		{"my-deploy", "deploy", "deployment/my-deploy"},
		{"my-deploy", "deployment", "deployment/my-deploy"},
		{"svc/already-prefixed", "pod", "svc/already-prefixed"},
		{"deployment/foo", "svc", "deployment/foo"},
	}
	for _, c := range cases {
		got := normalizePFTarget(c.raw, c.kind)
		if got != c.want {
			t.Errorf("normalizePFTarget(%q, %q) = %q, want %q", c.raw, c.kind, got, c.want)
		}
	}
}

func TestResolveApplyManifest_FromFile(t *testing.T) {
	// Reset state from any other test.
	resetApplyFlags()
	defer resetApplyFlags()

	dir := t.TempDir()
	f := filepath.Join(dir, "m.yaml")
	if err := os.WriteFile(f, []byte("apiVersion: v1\nkind: Pod"), 0o600); err != nil {
		t.Fatal(err)
	}
	k8sApplyFile = f

	got, err := resolveApplyManifest()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "kind: Pod") {
		t.Errorf("manifest content not returned; got %q", got)
	}
}

func TestResolveApplyManifest_FromManifestFlag(t *testing.T) {
	resetApplyFlags()
	defer resetApplyFlags()
	k8sApplyManifest = "kind: ConfigMap"

	got, err := resolveApplyManifest()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "kind: ConfigMap" {
		t.Errorf("got %q", got)
	}
}

func TestResolveApplyManifest_NoneAndTooMany(t *testing.T) {
	resetApplyFlags()
	defer resetApplyFlags()

	if _, err := resolveApplyManifest(); err == nil {
		t.Error("expected error when no manifest source is set")
	}

	k8sApplyFile = "/tmp/nope"
	k8sApplyManifest = "kind: X"
	if _, err := resolveApplyManifest(); err == nil {
		t.Error("expected error when both --file and --manifest set")
	}
}

func TestK8sApplyExecPortForward_RegisteredOnK8sRoot(t *testing.T) {
	want := map[string]bool{"apply": true, "exec": true, "port-forward": true}
	for _, c := range k8sCmd.Commands() {
		name := strings.SplitN(c.Use, " ", 2)[0]
		delete(want, name)
	}
	if len(want) != 0 {
		t.Errorf("missing registered subcommands on k8s root: %v", want)
	}
}

func TestK8sExecCmd_RequiresPodAndCommand(t *testing.T) {
	if err := k8sExecCmd.Args(k8sExecCmd, []string{"my-pod"}); err == nil {
		t.Error("expected error for exec with no command")
	}
	if err := k8sExecCmd.Args(k8sExecCmd, []string{"my-pod", "env"}); err != nil {
		t.Errorf("exec with command should be valid: %v", err)
	}
}

func TestK8sPortForwardCmd_RequiresTwoArgs(t *testing.T) {
	if err := k8sPortForwardCmd.Args(k8sPortForwardCmd, []string{"my-pod"}); err == nil {
		t.Error("expected error for pf with only one arg")
	}
	if err := k8sPortForwardCmd.Args(k8sPortForwardCmd, []string{"my-pod", "8080:80"}); err != nil {
		t.Errorf("pf with two args should be valid: %v", err)
	}
}

func resetApplyFlags() {
	k8sApplyFile = ""
	k8sApplyManifest = ""
	k8sApplyStdin = false
	k8sApplyServerDry = false
}
