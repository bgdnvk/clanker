package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"github.com/bgdnvk/clanker/internal/verda"
)

// VerdaInstantProviderOptions configures a VerdaInstantProvider.
type VerdaInstantProviderOptions struct {
	Client          *verda.Client
	DefaultLocation string
	DefaultSSHKeyID string
	SSHKeyPath      string
	Debug           bool
}

// VerdaInstantProvider provisions Verda Cloud "Instant Clusters" with the
// Kubernetes orchestrator preinstalled. Scale() is intentionally not supported
// — Verda Instant Clusters are fixed-size per cluster_type.
type VerdaInstantProvider struct {
	client          *verda.Client
	defaultLocation string
	defaultSSHKey   string
	sshKeyPath      string
	debug           bool
}

// NewVerdaInstantProvider constructs a provider. The caller must pass a
// configured *verda.Client (nil clients return an error on every method).
func NewVerdaInstantProvider(opts VerdaInstantProviderOptions) *VerdaInstantProvider {
	return &VerdaInstantProvider{
		client:          opts.Client,
		defaultLocation: opts.DefaultLocation,
		defaultSSHKey:   opts.DefaultSSHKeyID,
		sshKeyPath:      opts.SSHKeyPath,
		debug:           opts.Debug,
	}
}

// Type returns the cluster type identifier.
func (p *VerdaInstantProvider) Type() ClusterType { return ClusterTypeVerdaInstant }

func (p *VerdaInstantProvider) requireClient() error {
	if p.client == nil {
		return fmt.Errorf("verda client not configured — set verda.client_id/client_secret in ~/.clanker.yaml or run `verda auth login`")
	}
	return nil
}

// Create provisions a Verda Instant Cluster and polls until it reaches a
// terminal state. The cluster's orchestrator is selected via the `image` —
// we prefer a kubernetes-labelled image from /v1/images/cluster; if none
// exists we fall back to the first available cluster image and expect the
// caller to rely on a startup script to install k8s.
func (p *VerdaInstantProvider) Create(ctx context.Context, opts CreateOptions) (*ClusterInfo, error) {
	if err := p.requireClient(); err != nil {
		return nil, err
	}

	clusterType := strings.TrimSpace(opts.WorkerType)
	if clusterType == "" {
		return nil, &ErrInvalidConfiguration{Message: "verda-instant requires WorkerType set to a Verda cluster_type (e.g. \"8H100\")"}
	}

	location := strings.TrimSpace(opts.Region)
	if location == "" {
		location = p.defaultLocation
	}
	if location == "" {
		return nil, &ErrInvalidConfiguration{Message: "verda-instant requires Region (Verda location_code, e.g. FIN-01)"}
	}

	sshKey := strings.TrimSpace(opts.KeyPairName)
	if sshKey == "" {
		sshKey = p.defaultSSHKey
	}
	if sshKey == "" {
		return nil, &ErrInvalidConfiguration{Message: "verda-instant requires an ssh_key_id — set verda.default_ssh_key_id or pass KeyPairName"}
	}

	image, err := p.pickKubernetesClusterImage(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve cluster image: %w", err)
	}

	// Verda requires a shared_volume on every cluster create. Use a default SFS
	// sized for working-set storage; caller can resize after provisioning.
	shared := verda.SharedVolumeDto{
		Name: opts.Name + "-sfs",
		Size: 100,
	}

	body := verda.DeployClusterRequest{
		ClusterType:  clusterType,
		Image:        image,
		SSHKeyIDs:    []string{sshKey},
		Hostname:     opts.Name,
		Description:  fmt.Sprintf("clanker-managed verda-instant cluster (%s)", opts.Name),
		LocationCode: location,
		SharedVolume: &shared,
		Contract:     verda.ContractPayAsYouGo,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal cluster request: %w", err)
	}

	DebugLog(p.debug, "verda", "creating cluster %s (type=%s, location=%s, image=%s)", opts.Name, clusterType, location, image)
	resp, err := p.client.RunAPIWithContext(ctx, http.MethodPost, "/v1/clusters", string(payload))
	if err != nil {
		return nil, fmt.Errorf("POST /v1/clusters: %w", err)
	}

	// The API returns `{"id":"..."}` on 202. Polling requires the ID.
	var accepted struct {
		ID string `json:"id"`
	}
	if jerr := json.Unmarshal([]byte(resp), &accepted); jerr != nil || accepted.ID == "" {
		// Some Verda endpoints wrap the ID differently; fall back to listing + matching hostname.
		if id, lerr := p.findClusterIDByHostname(ctx, opts.Name); lerr == nil && id != "" {
			accepted.ID = id
		} else {
			return nil, fmt.Errorf("could not determine created cluster ID (resp=%s)", resp)
		}
	}

	createTimeout := opts.CreateTimeout
	if createTimeout == 0 {
		createTimeout = 30 * time.Minute
	}
	cluster, err := p.client.WaitClusterRunning(ctx, accepted.ID, verda.PollOptions{Interval: 30 * time.Second, Max: createTimeout})
	if err != nil {
		return nil, err
	}
	if cluster == nil {
		return nil, fmt.Errorf("cluster %s did not return detail payload", accepted.ID)
	}

	return toClusterInfo(cluster), nil
}

// Delete issues the `discontinue` cluster action.
func (p *VerdaInstantProvider) Delete(ctx context.Context, clusterName string) error {
	if err := p.requireClient(); err != nil {
		return err
	}
	id, err := p.resolveClusterID(ctx, clusterName)
	if err != nil {
		return err
	}

	body, err := json.Marshal(map[string]interface{}{
		"action": "discontinue",
		"id":     id,
	})
	if err != nil {
		return err
	}
	DebugLog(p.debug, "verda", "discontinuing cluster %s (id=%s)", clusterName, id)
	_, err = p.client.RunAPIWithContext(ctx, http.MethodPut, "/v1/clusters", string(body))
	return err
}

// Scale is not supported — Verda Instant Clusters are fixed-size per type.
func (p *VerdaInstantProvider) Scale(ctx context.Context, clusterName string, opts ScaleOptions) error {
	return fmt.Errorf("scale not supported for verda-instant — pick a larger cluster_type and recreate")
}

// GetKubeconfig SCPs `/root/.kube/config` off the first worker node and
// rewrites the `server:` URL to the node's public IP so kubectl can reach it
// from outside Verda's private network.
func (p *VerdaInstantProvider) GetKubeconfig(ctx context.Context, clusterName string) (string, error) {
	if err := p.requireClient(); err != nil {
		return "", err
	}
	id, err := p.resolveClusterID(ctx, clusterName)
	if err != nil {
		return "", err
	}

	body, err := p.client.RunAPIWithContext(ctx, http.MethodGet, "/v1/clusters/"+id, "")
	if err != nil {
		return "", err
	}
	var cluster verda.Cluster
	if err := json.Unmarshal([]byte(body), &cluster); err != nil {
		return "", fmt.Errorf("decode cluster: %w", err)
	}

	host := firstPublicIP(cluster)
	if host == "" {
		return "", fmt.Errorf("cluster %s has no reachable public IP yet", clusterName)
	}

	if p.sshKeyPath == "" {
		return "", fmt.Errorf("ssh_key_path not configured — set verda.ssh_key_path in ~/.clanker.yaml")
	}

	kubeconfig, err := p.fetchRemoteKubeconfig(ctx, host)
	if err != nil {
		return "", err
	}

	rewritten, err := rewriteKubeconfigServer(kubeconfig, host)
	if err != nil {
		return "", err
	}
	return rewritten, nil
}

// Health reports node readiness based on the cluster's worker-node status list.
func (p *VerdaInstantProvider) Health(ctx context.Context, clusterName string) (*HealthStatus, error) {
	if err := p.requireClient(); err != nil {
		return nil, err
	}
	id, err := p.resolveClusterID(ctx, clusterName)
	if err != nil {
		return nil, err
	}

	body, err := p.client.RunAPIWithContext(ctx, http.MethodGet, "/v1/clusters/"+id, "")
	if err != nil {
		return nil, err
	}
	var cluster verda.Cluster
	if err := json.Unmarshal([]byte(body), &cluster); err != nil {
		return nil, fmt.Errorf("decode cluster: %w", err)
	}

	status := &HealthStatus{
		Components:  make(map[string]string),
		NodeStatus:  make(map[string]string),
		LastChecked: time.Now(),
	}
	ready := 0
	for _, n := range cluster.WorkerNodes {
		name := n.Hostname
		if name == "" {
			name = n.ID
		}
		status.NodeStatus[name] = n.Status
		if n.Status == verda.StatusRunning {
			ready++
		}
	}
	total := len(cluster.WorkerNodes)
	status.Healthy = total > 0 && ready == total && cluster.Status == verda.StatusRunning
	if status.Healthy {
		status.Message = fmt.Sprintf("cluster running, %d/%d nodes ready", ready, total)
	} else {
		status.Message = fmt.Sprintf("cluster status=%s, %d/%d nodes ready", cluster.Status, ready, total)
	}
	return status, nil
}

// ListClusters returns every Verda cluster visible to the current credentials.
func (p *VerdaInstantProvider) ListClusters(ctx context.Context) ([]ClusterInfo, error) {
	if err := p.requireClient(); err != nil {
		return nil, err
	}
	body, err := p.client.RunAPIWithContext(ctx, http.MethodGet, "/v1/clusters", "")
	if err != nil {
		return nil, err
	}
	var list []verda.Cluster
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		return nil, fmt.Errorf("decode cluster list: %w", err)
	}
	out := make([]ClusterInfo, 0, len(list))
	for i := range list {
		out = append(out, *toClusterInfo(&list[i]))
	}
	return out, nil
}

// GetCluster fetches a single cluster by hostname (or ID).
func (p *VerdaInstantProvider) GetCluster(ctx context.Context, clusterName string) (*ClusterInfo, error) {
	if err := p.requireClient(); err != nil {
		return nil, err
	}
	id, err := p.resolveClusterID(ctx, clusterName)
	if err != nil {
		return nil, err
	}
	body, err := p.client.RunAPIWithContext(ctx, http.MethodGet, "/v1/clusters/"+id, "")
	if err != nil {
		return nil, err
	}
	var cluster verda.Cluster
	if err := json.Unmarshal([]byte(body), &cluster); err != nil {
		return nil, fmt.Errorf("decode cluster: %w", err)
	}
	return toClusterInfo(&cluster), nil
}

// resolveClusterID maps a hostname or raw ID to a cluster ID. Verda's API keys
// everything by UUID, but users specify clusters by name in clanker commands.
func (p *VerdaInstantProvider) resolveClusterID(ctx context.Context, nameOrID string) (string, error) {
	// Treat the input as a UUID if it looks like one. Verda UUIDs are
	// lowercase per the spec, so normalise in case a user pasted an
	// uppercase variant from a UI.
	lowered := strings.ToLower(strings.TrimSpace(nameOrID))
	if looksLikeUUID(lowered) {
		return lowered, nil
	}
	return p.findClusterIDByHostname(ctx, nameOrID)
}

func (p *VerdaInstantProvider) findClusterIDByHostname(ctx context.Context, hostname string) (string, error) {
	body, err := p.client.RunAPIWithContext(ctx, http.MethodGet, "/v1/clusters", "")
	if err != nil {
		return "", err
	}
	var list []verda.Cluster
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		return "", fmt.Errorf("decode cluster list: %w", err)
	}
	for _, c := range list {
		if strings.EqualFold(c.Hostname, hostname) {
			return c.ID, nil
		}
	}
	return "", &ErrClusterNotFound{ClusterName: hostname}
}

// pickKubernetesClusterImage selects a cluster OS image labelled as Kubernetes.
func (p *VerdaInstantProvider) pickKubernetesClusterImage(ctx context.Context) (string, error) {
	body, err := p.client.RunAPIWithContext(ctx, http.MethodGet, "/v1/images/cluster", "")
	if err != nil {
		return "", err
	}
	var images []verda.Image
	if err := json.Unmarshal([]byte(body), &images); err != nil {
		return "", fmt.Errorf("decode cluster images: %w", err)
	}
	// Prefer an image whose name/category hints at Kubernetes.
	for _, img := range images {
		joined := strings.ToLower(img.Name + " " + img.Category + " " + img.ImageType + " " + strings.Join(img.Details, " "))
		if strings.Contains(joined, "kubernetes") || strings.Contains(joined, "k8s") {
			return pickImageID(img), nil
		}
	}
	// Fall back to the default image — cluster will come up without k8s baked
	// in; caller is expected to configure a startup script.
	for _, img := range images {
		if img.IsDefault {
			return pickImageID(img), nil
		}
	}
	if len(images) > 0 {
		return pickImageID(images[0]), nil
	}
	return "", fmt.Errorf("no cluster images available from Verda")
}

func pickImageID(img verda.Image) string {
	if img.ID != "" {
		return img.ID
	}
	return img.ImageType
}

func (p *VerdaInstantProvider) fetchRemoteKubeconfig(ctx context.Context, host string) (string, error) {
	// Try root first, then ubuntu — Verda cluster nodes expose kubeconfig in
	// both locations depending on the OS image.
	candidates := []string{
		"root@" + host + ":/root/.kube/config",
		"ubuntu@" + host + ":/home/ubuntu/.kube/config",
	}
	var lastErr error
	for _, target := range candidates {
		out, err := p.scpRead(ctx, target)
		if err == nil && strings.TrimSpace(out) != "" {
			return out, nil
		}
		lastErr = err
	}
	return "", fmt.Errorf("failed to read kubeconfig from %s: %w", host, lastErr)
}

func (p *VerdaInstantProvider) scpRead(ctx context.Context, target string) (string, error) {
	// We invoke `ssh <host> cat <path>` since `scp - <remote:path>` has
	// inconsistent support across OpenSSH versions.
	host, path := splitSCPTarget(target)
	if host == "" || path == "" {
		return "", fmt.Errorf("invalid scp target %s", target)
	}
	// Belt-and-braces: reject any path containing shell metacharacters in case
	// the candidate list ever becomes user-supplied. `ssh` concatenates the
	// remote args into a single string before passing to the login shell, so
	// anything non-literal here is an injection vector.
	if strings.ContainsAny(path, ";&|`$><\n\r") {
		return "", fmt.Errorf("refusing to scp path with shell metacharacters: %q", path)
	}

	args := []string{
		"-i", p.sshKeyPath,
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=10",
		"-o", "BatchMode=yes",
		host,
		"--",
		"cat",
		path,
	}
	DebugLog(p.debug, "ssh", "%s", strings.Join(args, " "))

	cmd := exec.CommandContext(ctx, "ssh", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ssh %s: %w, stderr=%s", host, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func splitSCPTarget(target string) (host, path string) {
	i := strings.Index(target, ":")
	if i <= 0 {
		return "", ""
	}
	return target[:i], target[i+1:]
}

// rewriteKubeconfigServer updates the `server:` URL to use the supplied host
// so kubectl can reach the cluster from outside Verda's private network.
func rewriteKubeconfigServer(kubeconfig, publicHost string) (string, error) {
	var sb strings.Builder
	for _, line := range strings.Split(kubeconfig, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "server:") {
			u, err := url.Parse(strings.TrimSpace(strings.TrimPrefix(trimmed, "server:")))
			if err == nil && u.Host != "" {
				newHost := publicHost
				if port := u.Port(); port != "" {
					newHost = publicHost + ":" + port
				} else {
					newHost = publicHost + ":6443"
				}
				u.Host = newHost
				indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
				sb.WriteString(indent + "server: " + u.String() + "\n")
				continue
			}
		}
		sb.WriteString(line + "\n")
	}
	out := sb.String()
	if !strings.Contains(out, "server:") {
		return "", fmt.Errorf("kubeconfig has no server entry to rewrite")
	}
	return out, nil
}

func firstPublicIP(c verda.Cluster) string {
	for _, n := range c.WorkerNodes {
		if n.PublicIP != "" {
			return n.PublicIP
		}
	}
	return c.IP
}

func toClusterInfo(c *verda.Cluster) *ClusterInfo {
	if c == nil {
		return nil
	}
	info := &ClusterInfo{
		Name:              c.Hostname,
		Type:              ClusterTypeVerdaInstant,
		Status:            c.Status,
		Endpoint:          firstPublicIP(*c),
		Region:            c.Location,
		KubernetesVersion: "",
	}
	if t, err := time.Parse(time.RFC3339, c.CreatedAt); err == nil {
		info.CreatedAt = t
	}
	for _, n := range c.WorkerNodes {
		role := "worker"
		// The first worker node is conventionally the control-plane jump host.
		if len(info.ControlPlaneNodes) == 0 {
			role = "control-plane"
		}
		node := NodeInfo{
			Name:       n.Hostname,
			Role:       role,
			Status:     n.Status,
			InternalIP: n.PrivateIP,
			ExternalIP: n.PublicIP,
			InstanceID: n.ID,
		}
		if role == "control-plane" {
			info.ControlPlaneNodes = append(info.ControlPlaneNodes, node)
		} else {
			info.WorkerNodes = append(info.WorkerNodes, node)
		}
	}
	return info
}

func looksLikeUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') && !(r >= 'A' && r <= 'F') {
				return false
			}
		}
	}
	return true
}
