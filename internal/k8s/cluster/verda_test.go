package cluster

import (
	"strings"
	"testing"
)

func TestLooksLikeUUID(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"valid lowercase", "4d04ce40-aed8-4bed-aa73-648e74b188c7", true},
		{"valid all zeros", "00000000-0000-0000-0000-000000000000", true},
		{"uppercase rejected", "4D04CE40-AED8-4BED-AA73-648E74B188C7", false},
		{"too short", "4d04ce40-aed8-4bed-aa73-648e74b188", false},
		{"too long", "4d04ce40-aed8-4bed-aa73-648e74b188c7aa", false},
		{"bad separator", "4d04ce40_aed8_4bed_aa73_648e74b188c7", false},
		{"non-hex chars", "zzzzzzzz-aed8-4bed-aa73-648e74b188c7", false},
		{"hostname shaped", "my-host-01", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := looksLikeUUID(tc.in); got != tc.want {
				t.Errorf("looksLikeUUID(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestSplitSCPTarget(t *testing.T) {
	cases := []struct {
		in       string
		wantHost string
		wantPath string
	}{
		{"root@1.2.3.4:/root/.kube/config", "root@1.2.3.4", "/root/.kube/config"},
		{"ubuntu@host:/home/ubuntu/.kube/config", "ubuntu@host", "/home/ubuntu/.kube/config"},
		{"bad-no-colon", "", ""},
		{":nohost-only-path", "", ""},
	}
	for _, tc := range cases {
		host, path := splitSCPTarget(tc.in)
		if host != tc.wantHost || path != tc.wantPath {
			t.Errorf("splitSCPTarget(%q) = (%q, %q), want (%q, %q)", tc.in, host, path, tc.wantHost, tc.wantPath)
		}
	}
}

func TestRewriteKubeconfigServerPreservesIndent(t *testing.T) {
	kubeconfig := `apiVersion: v1
clusters:
- cluster:
    certificate-authority-data: AAAA==
    server: https://10.0.0.1:6443
  name: verda
contexts:
- context:
    cluster: verda
    user: admin
  name: verda
current-context: verda
kind: Config
users:
- name: admin
  user:
    token: tok
`
	out, err := rewriteKubeconfigServer(kubeconfig, "1.2.3.4")
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if !strings.Contains(out, "server: https://1.2.3.4:6443") {
		t.Errorf("expected rewritten server URL to keep port 6443, got:\n%s", out)
	}
	// Original indentation (4 spaces before "server:") must be preserved so the
	// resulting document parses as valid YAML.
	if !strings.Contains(out, "    server: https://1.2.3.4:6443") {
		t.Errorf("indentation not preserved, got:\n%s", out)
	}
}

func TestRewriteKubeconfigServerKeepsCustomPort(t *testing.T) {
	kubeconfig := `apiVersion: v1
clusters:
- cluster:
    server: https://10.0.0.1:8443
  name: verda
kind: Config
`
	out, err := rewriteKubeconfigServer(kubeconfig, "1.2.3.4")
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if !strings.Contains(out, "server: https://1.2.3.4:8443") {
		t.Errorf("expected port 8443 to be preserved, got:\n%s", out)
	}
}

func TestRewriteKubeconfigServerMissingServerErrors(t *testing.T) {
	kubeconfig := `apiVersion: v1
kind: Config
users: []
`
	if _, err := rewriteKubeconfigServer(kubeconfig, "1.2.3.4"); err == nil {
		t.Fatal("expected error when kubeconfig has no server line")
	}
}

func TestVerdaInstantProviderType(t *testing.T) {
	p := NewVerdaInstantProvider(VerdaInstantProviderOptions{})
	if p.Type() != ClusterTypeVerdaInstant {
		t.Errorf("Type() = %s, want %s", p.Type(), ClusterTypeVerdaInstant)
	}
}

func TestVerdaInstantProviderRequireClient(t *testing.T) {
	p := NewVerdaInstantProvider(VerdaInstantProviderOptions{})
	if err := p.requireClient(); err == nil {
		t.Fatal("expected error when client is nil")
	}
}
