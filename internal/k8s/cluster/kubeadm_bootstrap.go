package cluster

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// KubernetesVersion is the default Kubernetes version to install
const KubernetesVersion = "1.29"

// BootstrapConfig contains configuration for bootstrapping a node
type BootstrapConfig struct {
	KubernetesVersion string
	PodCIDR           string
	ServiceCIDR       string
	ClusterName       string
	ControlPlaneIP    string
	IsControlPlane    bool
	JoinToken         string
	CACertHash        string
	CNI               string // calico or flannel
}

// DefaultBootstrapConfig returns sensible defaults
func DefaultBootstrapConfig() BootstrapConfig {
	return BootstrapConfig{
		KubernetesVersion: KubernetesVersion,
		PodCIDR:           "192.168.0.0/16",
		ServiceCIDR:       "10.96.0.0/12",
		CNI:               "calico",
	}
}

// BootstrapNode installs prerequisites and kubeadm on a node
func BootstrapNode(ctx context.Context, ssh *SSHClient, config BootstrapConfig) error {
	// Step 1: Configure kernel modules
	if _, err := ssh.RunSudoScript(ctx, kernelModulesScript()); err != nil {
		return fmt.Errorf("failed to configure kernel modules: %w", err)
	}

	// Step 2: Configure sysctl
	if _, err := ssh.RunSudoScript(ctx, sysctlScript()); err != nil {
		return fmt.Errorf("failed to configure sysctl: %w", err)
	}

	// Step 3: Install containerd
	if _, err := ssh.RunSudoScript(ctx, containerdInstallScript()); err != nil {
		return fmt.Errorf("failed to install containerd: %w", err)
	}

	// Step 4: Install kubeadm, kubelet, kubectl
	if _, err := ssh.RunSudoScript(ctx, kubeadmInstallScript(config.KubernetesVersion)); err != nil {
		return fmt.Errorf("failed to install kubeadm: %w", err)
	}

	return nil
}

// InitializeControlPlane runs kubeadm init on the control plane
func InitializeControlPlane(ctx context.Context, ssh *SSHClient, config BootstrapConfig) (*KubeadmInitOutput, error) {
	script := kubeadmInitScript(config)

	output, err := ssh.RunSudoScript(ctx, script)
	if err != nil {
		return nil, fmt.Errorf("kubeadm init failed: %w", err)
	}

	// Parse the join command from output
	initOutput := parseKubeadmInitOutput(output)

	// Setup kubectl for the user
	if _, err := ssh.RunScript(ctx, kubectlSetupScript()); err != nil {
		return nil, fmt.Errorf("failed to setup kubectl: %w", err)
	}

	return initOutput, nil
}

// InstallCNI installs the CNI plugin on the control plane
func InstallCNI(ctx context.Context, ssh *SSHClient, cni string) error {
	var script string

	switch strings.ToLower(cni) {
	case "calico":
		script = calicoInstallScript()
	case "flannel":
		script = flannelInstallScript()
	default:
		return fmt.Errorf("unsupported CNI: %s", cni)
	}

	if _, err := ssh.RunScript(ctx, script); err != nil {
		return fmt.Errorf("failed to install CNI %s: %w", cni, err)
	}

	return nil
}

// JoinWorker runs kubeadm join on a worker node
func JoinWorker(ctx context.Context, ssh *SSHClient, config BootstrapConfig) error {
	script := kubeadmJoinScript(config)

	if _, err := ssh.RunSudoScript(ctx, script); err != nil {
		return fmt.Errorf("kubeadm join failed: %w", err)
	}

	return nil
}

// KubeadmInitOutput contains parsed output from kubeadm init
type KubeadmInitOutput struct {
	JoinCommand string
	Token       string
	CACertHash  string
}

// GetJoinToken retrieves a new join token from the control plane
func GetJoinToken(ctx context.Context, ssh *SSHClient) (*KubeadmInitOutput, error) {
	// Create a new token
	tokenOutput, err := ssh.RunSudo(ctx, "kubeadm token create --print-join-command")
	if err != nil {
		return nil, fmt.Errorf("failed to create join token: %w", err)
	}

	return parseJoinCommand(strings.TrimSpace(tokenOutput)), nil
}

// WaitForNodeReady waits for all nodes to be ready
func WaitForNodeReady(ctx context.Context, ssh *SSHClient, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		output, err := ssh.Run(ctx, "kubectl get nodes -o jsonpath='{.items[*].status.conditions[?(@.type==\"Ready\")].status}'")
		if err == nil {
			statuses := strings.Fields(output)
			allReady := true
			for _, status := range statuses {
				if status != "True" {
					allReady = false
					break
				}
			}
			if allReady && len(statuses) > 0 {
				return nil
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Second):
		}
	}

	return fmt.Errorf("timeout waiting for nodes to be ready")
}

// Script generators

func kernelModulesScript() string {
	return `
cat <<EOF | tee /etc/modules-load.d/k8s.conf
overlay
br_netfilter
EOF

modprobe overlay
modprobe br_netfilter
`
}

func sysctlScript() string {
	return `
cat <<EOF | tee /etc/sysctl.d/k8s.conf
net.bridge.bridge-nf-call-iptables  = 1
net.bridge.bridge-nf-call-ip6tables = 1
net.ipv4.ip_forward                 = 1
EOF

sysctl --system
`
}

func containerdInstallScript() string {
	return `
# Install containerd
apt-get update
apt-get install -y ca-certificates curl gnupg

# Add Docker GPG key
install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/ubuntu/gpg | gpg --dearmor -o /etc/apt/keyrings/docker.gpg
chmod a+r /etc/apt/keyrings/docker.gpg

# Add Docker repo
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo "$VERSION_CODENAME") stable" | tee /etc/apt/sources.list.d/docker.list > /dev/null

apt-get update
apt-get install -y containerd.io

# Configure containerd
mkdir -p /etc/containerd
containerd config default | tee /etc/containerd/config.toml

# Enable SystemdCgroup
sed -i 's/SystemdCgroup = false/SystemdCgroup = true/g' /etc/containerd/config.toml

systemctl restart containerd
systemctl enable containerd
`
}

func kubeadmInstallScript(version string) string {
	return fmt.Sprintf(`
# Disable swap
swapoff -a
sed -i '/ swap / s/^\(.*\)$/#\1/g' /etc/fstab

# Install kubeadm, kubelet, kubectl
apt-get update
apt-get install -y apt-transport-https ca-certificates curl gpg

# Add Kubernetes GPG key
curl -fsSL https://pkgs.k8s.io/core:/stable:/v%s/deb/Release.key | gpg --dearmor -o /etc/apt/keyrings/kubernetes-apt-keyring.gpg

# Add Kubernetes repo
echo "deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] https://pkgs.k8s.io/core:/stable:/v%s/deb/ /" | tee /etc/apt/sources.list.d/kubernetes.list

apt-get update
apt-get install -y kubelet kubeadm kubectl
apt-mark hold kubelet kubeadm kubectl

systemctl enable kubelet
`, version, version)
}

func kubeadmInitScript(config BootstrapConfig) string {
	// Get the public IP dynamically for TLS SAN
	return fmt.Sprintf(`
# Get public IP for TLS certificate SAN
PUBLIC_IP=$(curl -s http://169.254.169.254/latest/meta-data/public-ipv4 || echo "")
PRIVATE_IP=$(curl -s http://169.254.169.254/latest/meta-data/local-ipv4 || hostname -I | awk '{print $1}')

kubeadm init \
  --pod-network-cidr=%s \
  --service-cidr=%s \
  --kubernetes-version=v%s.0 \
  --apiserver-cert-extra-sans=${PUBLIC_IP},${PRIVATE_IP} \
  --upload-certs

# Print the join command for easy parsing
echo "=== JOIN COMMAND ==="
kubeadm token create --print-join-command
`, config.PodCIDR, config.ServiceCIDR, config.KubernetesVersion)
}

func kubectlSetupScript() string {
	return `
mkdir -p $HOME/.kube
sudo cp -f /etc/kubernetes/admin.conf $HOME/.kube/config
sudo chown $(id -u):$(id -g) $HOME/.kube/config
`
}

func kubeadmJoinScript(config BootstrapConfig) string {
	return fmt.Sprintf(`
kubeadm join %s:6443 \
  --token %s \
  --discovery-token-ca-cert-hash %s
`, config.ControlPlaneIP, config.JoinToken, config.CACertHash)
}

func calicoInstallScript() string {
	return `
kubectl apply -f https://raw.githubusercontent.com/projectcalico/calico/v3.27.0/manifests/calico.yaml
`
}

func flannelInstallScript() string {
	return `
kubectl apply -f https://github.com/flannel-io/flannel/releases/latest/download/kube-flannel.yml
`
}

// parseKubeadmInitOutput parses the output of kubeadm init
func parseKubeadmInitOutput(output string) *KubeadmInitOutput {
	result := &KubeadmInitOutput{}

	lines := strings.Split(output, "\n")
	foundMarker := false

	for i, line := range lines {
		// Look for our marker first
		if strings.Contains(line, "=== JOIN COMMAND ===") {
			foundMarker = true
			continue
		}

		// After marker, look for the join command
		if foundMarker && strings.Contains(line, "kubeadm join") {
			joinCmd := strings.TrimSpace(line)
			result = parseJoinCommand(joinCmd)
			break
		}

		// Also handle the original kubeadm init output format
		if !foundMarker && strings.Contains(line, "kubeadm join") {
			// Join command spans multiple lines
			joinCmd := strings.TrimSpace(line)
			// Remove backslash continuation
			joinCmd = strings.TrimSuffix(joinCmd, "\\")
			joinCmd = strings.TrimSpace(joinCmd)

			for j := i + 1; j < len(lines) && j <= i+3; j++ {
				nextLine := strings.TrimSpace(lines[j])
				nextLine = strings.TrimSuffix(nextLine, "\\")
				nextLine = strings.TrimSpace(nextLine)
				if nextLine != "" && !strings.HasPrefix(nextLine, "#") {
					joinCmd += " " + nextLine
				}
			}
			result = parseJoinCommand(joinCmd)
			break
		}
	}

	return result
}

// parseJoinCommand extracts token and hash from a join command
func parseJoinCommand(joinCmd string) *KubeadmInitOutput {
	result := &KubeadmInitOutput{
		JoinCommand: joinCmd,
	}

	parts := strings.Fields(joinCmd)
	for i, part := range parts {
		if part == "--token" && i+1 < len(parts) {
			result.Token = parts[i+1]
		}
		if part == "--discovery-token-ca-cert-hash" && i+1 < len(parts) {
			result.CACertHash = parts[i+1]
		}
	}

	return result
}

// GetKubeconfig retrieves the kubeconfig from the control plane
func GetKubeconfig(ctx context.Context, ssh *SSHClient) ([]byte, error) {
	return ssh.DownloadBytes(ctx, "/etc/kubernetes/admin.conf")
}

// ResetNode resets a node (removes kubeadm configuration)
func ResetNode(ctx context.Context, ssh *SSHClient) error {
	_, err := ssh.RunSudo(ctx, "kubeadm reset -f")
	return err
}
