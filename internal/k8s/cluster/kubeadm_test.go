package cluster

import (
	"testing"
)

func TestNewKubeadmProvider(t *testing.T) {
	provider := NewKubeadmProvider(KubeadmProviderOptions{
		AWSProfile:  "test",
		Region:      "us-west-2",
		KeyPairName: "my-key",
		Debug:       false,
	})

	if provider == nil {
		t.Fatal("NewKubeadmProvider returned nil")
	}

	if provider.Type() != ClusterTypeKubeadm {
		t.Errorf("Type() = %v, want %v", provider.Type(), ClusterTypeKubeadm)
	}

	if provider.awsProfile != "test" {
		t.Errorf("awsProfile = %s, want test", provider.awsProfile)
	}

	if provider.region != "us-west-2" {
		t.Errorf("region = %s, want us-west-2", provider.region)
	}

	if provider.keyPairName != "my-key" {
		t.Errorf("keyPairName = %s, want my-key", provider.keyPairName)
	}
}

func TestDefaultBootstrapConfig(t *testing.T) {
	config := DefaultBootstrapConfig()

	if config.KubernetesVersion == "" {
		t.Error("KubernetesVersion is empty")
	}

	if config.PodCIDR == "" {
		t.Error("PodCIDR is empty")
	}

	if config.ServiceCIDR == "" {
		t.Error("ServiceCIDR is empty")
	}

	if config.CNI == "" {
		t.Error("CNI is empty")
	}
}

func TestParseJoinCommand(t *testing.T) {
	tests := []struct {
		name        string
		joinCmd     string
		wantToken   string
		wantCAHash  string
	}{
		{
			name:       "valid join command",
			joinCmd:    "kubeadm join 10.0.0.1:6443 --token abc123.xyz789 --discovery-token-ca-cert-hash sha256:abcdef123456",
			wantToken:  "abc123.xyz789",
			wantCAHash: "sha256:abcdef123456",
		},
		{
			name:       "empty command",
			joinCmd:    "",
			wantToken:  "",
			wantCAHash: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseJoinCommand(tt.joinCmd)

			if result.Token != tt.wantToken {
				t.Errorf("Token = %s, want %s", result.Token, tt.wantToken)
			}

			if result.CACertHash != tt.wantCAHash {
				t.Errorf("CACertHash = %s, want %s", result.CACertHash, tt.wantCAHash)
			}
		})
	}
}

func TestKernelModulesScript(t *testing.T) {
	script := kernelModulesScript()

	if script == "" {
		t.Error("kernelModulesScript returned empty string")
	}

	// Check for expected content
	if !containsStr(script, "overlay") {
		t.Error("script missing overlay module")
	}

	if !containsStr(script, "br_netfilter") {
		t.Error("script missing br_netfilter module")
	}
}

func TestSysctlScript(t *testing.T) {
	script := sysctlScript()

	if script == "" {
		t.Error("sysctlScript returned empty string")
	}

	// Check for expected content
	if !containsStr(script, "net.bridge.bridge-nf-call-iptables") {
		t.Error("script missing iptables setting")
	}

	if !containsStr(script, "net.ipv4.ip_forward") {
		t.Error("script missing ip_forward setting")
	}
}

func TestContainerdInstallScript(t *testing.T) {
	script := containerdInstallScript()

	if script == "" {
		t.Error("containerdInstallScript returned empty string")
	}

	if !containsStr(script, "containerd") {
		t.Error("script missing containerd installation")
	}

	if !containsStr(script, "SystemdCgroup") {
		t.Error("script missing SystemdCgroup configuration")
	}
}

func TestKubeadmInstallScript(t *testing.T) {
	script := kubeadmInstallScript("1.29")

	if script == "" {
		t.Error("kubeadmInstallScript returned empty string")
	}

	if !containsStr(script, "kubeadm") {
		t.Error("script missing kubeadm")
	}

	if !containsStr(script, "kubelet") {
		t.Error("script missing kubelet")
	}

	if !containsStr(script, "kubectl") {
		t.Error("script missing kubectl")
	}

	if !containsStr(script, "1.29") {
		t.Error("script missing version")
	}
}

func TestCalicoInstallScript(t *testing.T) {
	script := calicoInstallScript()

	if script == "" {
		t.Error("calicoInstallScript returned empty string")
	}

	if !containsStr(script, "calico") {
		t.Error("script missing calico")
	}
}

func TestFlannelInstallScript(t *testing.T) {
	script := flannelInstallScript()

	if script == "" {
		t.Error("flannelInstallScript returned empty string")
	}

	if !containsStr(script, "flannel") {
		t.Error("script missing flannel")
	}
}

func TestKubeadmInitScript(t *testing.T) {
	config := DefaultBootstrapConfig()
	script := kubeadmInitScript(config)

	if script == "" {
		t.Error("kubeadmInitScript returned empty string")
	}

	if !containsStr(script, "kubeadm init") {
		t.Error("script missing kubeadm init")
	}

	if !containsStr(script, config.PodCIDR) {
		t.Error("script missing pod CIDR")
	}
}

func TestKubeadmJoinScript(t *testing.T) {
	config := BootstrapConfig{
		ControlPlaneIP: "10.0.0.1",
		JoinToken:      "abc123.xyz789",
		CACertHash:     "sha256:abcdef",
	}
	script := kubeadmJoinScript(config)

	if script == "" {
		t.Error("kubeadmJoinScript returned empty string")
	}

	if !containsStr(script, "kubeadm join") {
		t.Error("script missing kubeadm join")
	}

	if !containsStr(script, config.ControlPlaneIP) {
		t.Error("script missing control plane IP")
	}

	if !containsStr(script, config.JoinToken) {
		t.Error("script missing join token")
	}
}

// Helper function
func containsStr(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && (s == substr || len(s) > len(substr) && (s[:len(substr)] == substr || s[len(s)-len(substr):] == substr || findSubstr(s, substr)))
}

func findSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
