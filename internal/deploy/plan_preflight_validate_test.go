package deploy

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bgdnvk/clanker/internal/maker"
)

func TestDeterministicValidatePlanUsesProvidedRuntimeEnvKeys(t *testing.T) {
	plan := &maker.Plan{
		Version:  1,
		Provider: "cloudflare",
		Question: "Deploy test app",
		Summary:  "Test plan",
		Commands: []maker.Command{
			{Args: []string{"pages", "deploy", "--project-name", "demo", "--token", "<API_TOKEN>"}},
		},
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("marshal plan: %v", err)
	}

	withoutEnv := DeterministicValidatePlan(string(raw), nil, nil, nil, nil)
	if withoutEnv == nil || withoutEnv.IsValid {
		t.Fatal("expected missing runtime env key to fail deterministic validation")
	}

	withEnv := DeterministicValidatePlan(string(raw), nil, nil, nil, []string{"API_TOKEN"})
	if withEnv == nil || !withEnv.IsValid {
		t.Fatalf("expected provided runtime env key to satisfy deterministic validation, got %+v", withEnv)
	}
}

func TestValidatePlanDeterministicFinalRejectsOpenClawDOProxyImageMismatch(t *testing.T) {
	plan := &maker.Plan{
		Version:  1,
		Provider: "digitalocean",
		Question: "Deploy OpenClaw to DigitalOcean with HTTPS",
		Capabilities: &maker.PlanCapabilities{
			Provider: "digitalocean",
			AppKind:  "openclaw",
		},
		Commands: []maker.Command{
			{Args: []string{"compute", "ssh-key", "import", "openclaw-key", "--public-key-file", "./id.pub"}, Produces: map[string]string{"SSH_KEY_ID": "id"}},
			{Args: []string{"compute", "firewall", "create", "--name", "openclaw-fw", "--inbound-rules", "protocol:tcp,ports:22,address:0.0.0.0/0 protocol:tcp,ports:18789,address:0.0.0.0/0 protocol:tcp,ports:18790,address:0.0.0.0/0", "--outbound-rules", "protocol:tcp,ports:all,address:0.0.0.0/0 protocol:udp,ports:all,address:0.0.0.0/0"}, Produces: map[string]string{"FIREWALL_ID": "id"}},
			{Args: []string{"compute", "droplet", "create", "openclaw", "--ssh-keys", "<SSH_KEY_ID>"}, Produces: map[string]string{"DROPLET_ID": "id", "DROPLET_IP": "ip"}},
			{Args: []string{"compute", "firewall", "add-droplets", "<FIREWALL_ID>", "--droplet-ids", "<DROPLET_ID>"}},
			{Args: []string{"registry", "create", "openclaw-registry"}, Produces: map[string]string{"REGISTRY_NAME": "name"}},
			{Args: []string{"registry", "login"}},
			{Args: []string{"docker", "build", "--platform", "linux/amd64", "-t", "registry.digitalocean.com/<REGISTRY_NAME>/<REGISTRY_NAME>/openclaw-proxy:latest", "__CLANKER_OPENCLAW_DO_PROXY__"}},
			{Args: []string{"docker", "push", "registry.digitalocean.com/<REGISTRY_NAME>/<REGISTRY_NAME>/openclaw-proxy:latest"}},
			{Args: []string{"apps", "create", "--spec", `{"name":"openclaw-proxy","services":[{"name":"proxy","http_port":8080,"image":{"registry":"<REGISTRY_NAME>","repository":"openclaw-proxy","tag":"latest"},"envs":[{"key":"UPSTREAM_URL","value":"http://<DROPLET_IP>:18789","scope":"RUN_TIME","type":"GENERAL"}]}]}`}, Produces: map[string]string{"APP_ID": "id", "HTTPS_URL": "default_ingress"}},
		},
	}

	v := ValidatePlanDeterministicFinal(plan, &RepoProfile{RepoURL: "https://github.com/openclaw/openclaw", Summary: "openclaw"}, &DeepAnalysis{}, &DockerAnalysis{PrimaryPort: 18789}, nil)
	if v == nil || v.IsValid {
		t.Fatalf("expected deterministic validation to reject mismatched OpenClaw DO proxy image refs, got %+v", v)
	}
	joined := strings.Join(v.Issues, "\n")
	if !strings.Contains(joined, "App Platform expects") {
		t.Fatalf("expected App Platform image mismatch issue, got %q", joined)
	}
}

func TestValidatePlanDeterministicFinalRejectsOpenClawDOComposeInstallMismatch(t *testing.T) {
	plan := &maker.Plan{
		Version:      1,
		Provider:     "digitalocean",
		Question:     "Deploy OpenClaw to DigitalOcean with HTTPS",
		Capabilities: &maker.PlanCapabilities{Provider: "digitalocean", AppKind: "openclaw"},
		Commands: []maker.Command{
			{Args: []string{"compute", "ssh-key", "import", "openclaw-key", "--public-key-file", "./id.pub"}, Produces: map[string]string{"SSH_KEY_ID": "id"}},
			{Args: []string{"compute", "firewall", "create", "--name", "openclaw-fw", "--inbound-rules", "protocol:tcp,ports:22,address:0.0.0.0/0 protocol:tcp,ports:18789,address:0.0.0.0/0 protocol:tcp,ports:18790,address:0.0.0.0/0", "--outbound-rules", "protocol:tcp,ports:all,address:0.0.0.0/0 protocol:udp,ports:all,address:0.0.0.0/0"}, Produces: map[string]string{"FIREWALL_ID": "id"}},
			{Args: []string{"compute", "droplet", "create", "openclaw", "--image", "docker-20-04", "--size", "s-2vcpu-4gb", "--ssh-keys", "<SSH_KEY_ID>", "--user-data", "#!/bin/bash\nset -euo pipefail\napt-get update\napt-get install -y docker-compose\ngit clone https://github.com/openclaw/openclaw /opt/openclaw\ncd /opt/openclaw\ncat > /opt/openclaw/.env << 'ENVEOF'\nOPENCLAW_CONFIG_DIR=/opt/openclaw/data\nOPENCLAW_WORKSPACE_DIR=/opt/openclaw/workspace\nOPENCLAW_GATEWAY_BIND=lan\nOPENCLAW_GATEWAY_TOKEN=<OPENCLAW_GATEWAY_TOKEN>\nANTHROPIC_API_KEY=<ANTHROPIC_API_KEY>\nENVEOF\ndocker build -t openclaw:local .\nexport HOME=/root\n./docker-setup.sh\ndocker compose up -d openclaw-gateway\n"}, Produces: map[string]string{"DROPLET_ID": "id", "DROPLET_IP": "ip"}},
			{Args: []string{"compute", "firewall", "add-droplets", "<FIREWALL_ID>", "--droplet-ids", "<DROPLET_ID>"}},
			{Args: []string{"registry", "create", "openclaw-registry"}, Produces: map[string]string{"REGISTRY_NAME": "name"}},
			{Args: []string{"registry", "login"}},
			{Args: []string{"docker", "build", "--platform", "linux/amd64", "-t", "registry.digitalocean.com/<REGISTRY_NAME>/https-proxy:latest", "__CLANKER_OPENCLAW_DO_PROXY__"}},
			{Args: []string{"docker", "push", "registry.digitalocean.com/<REGISTRY_NAME>/https-proxy:latest"}},
			{Args: []string{"apps", "create", "--spec", `{"name":"openclaw-proxy","services":[{"name":"proxy","http_port":8080,"image":{"registry":"<REGISTRY_NAME>","repository":"https-proxy","tag":"latest"},"envs":[{"key":"UPSTREAM_URL","value":"http://<DROPLET_IP>:18789","scope":"RUN_TIME","type":"GENERAL"}]}]}`}, Produces: map[string]string{"APP_ID": "id", "HTTPS_URL": "default_ingress"}},
		},
	}

	v := ValidatePlanDeterministicFinal(plan, &RepoProfile{RepoURL: "https://github.com/openclaw/openclaw", Summary: "openclaw"}, &DeepAnalysis{}, &DockerAnalysis{PrimaryPort: 18789}, nil)
	if v == nil || v.IsValid {
		t.Fatalf("expected deterministic validation to reject compose install mismatch, got %+v", v)
	}
	joined := strings.Join(v.Issues, "\n")
	if !strings.Contains(joined, "runs 'docker compose' but installs only the standalone docker-compose package") {
		t.Fatalf("expected compose install mismatch issue, got %q", joined)
	}
	if !strings.Contains(strings.Join(v.Warnings, "\n"), "git clone without explicitly installing git") {
		t.Fatalf("expected git install warning, got %+v", v.Warnings)
	}
}
