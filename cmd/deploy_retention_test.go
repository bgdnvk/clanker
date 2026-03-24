package cmd

import (
	"strings"
	"testing"

	"github.com/bgdnvk/clanker/internal/deploy"
	"github.com/bgdnvk/clanker/internal/maker"
)

func TestEnforceStrictPlanRetentionRejectsNewStrictSchemaViolation(t *testing.T) {
	baseline := &maker.Plan{
		Provider: "digitalocean",
		Question: "Deploy OpenClaw to DigitalOcean with HTTPS",
		Capabilities: &maker.PlanCapabilities{
			Provider:     "digitalocean",
			AppKind:      "openclaw",
			RuntimeModel: "droplet-compose",
		},
		Commands: []maker.Command{
			{Args: []string{"compute", "ssh-key", "import", "openclaw-key", "--public-key-file", "./id.pub"}, Produces: map[string]string{"SSH_KEY_ID": "$.id"}},
			{Args: []string{"compute", "firewall", "create", "--name", "openclaw-fw"}, Produces: map[string]string{"FIREWALL_ID": "$.id"}},
			{Args: []string{"compute", "droplet", "create", "openclaw", "--ssh-keys", "<SSH_KEY_ID>"}, Produces: map[string]string{"DROPLET_ID": "$.id", "DROPLET_IP": "$.droplet.networks.v4[0].ip_address"}},
			{Args: []string{"registry", "create", "openclaw-registry"}, Produces: map[string]string{"REGISTRY_NAME": "$.name"}},
			{Args: []string{"registry", "login"}},
			{Args: []string{"docker", "build", "-t", "registry.digitalocean.com/<REGISTRY_NAME>/proxy:latest", "__CLANKER_OPENCLAW_DO_PROXY__"}, Produces: map[string]string{"PROXY_IMAGE_URI": "registry.digitalocean.com/<REGISTRY_NAME>/proxy:latest"}},
			{Args: []string{"docker", "push", "<PROXY_IMAGE_URI>"}},
			{Args: []string{"apps", "create", "--spec", `{"services":[{"name":"proxy","image":{"registry":"<REGISTRY_NAME>","repository":"proxy","tag":"latest"}}]}`}, Produces: map[string]string{"APP_ID": "$.id"}},
		},
	}

	candidate := &maker.Plan{
		Provider:     baseline.Provider,
		Question:     baseline.Question,
		Capabilities: baseline.Capabilities,
		Commands: append([]maker.Command{},
			baseline.Commands[0],
			baseline.Commands[1],
			baseline.Commands[2],
			baseline.Commands[3],
			baseline.Commands[4],
			baseline.Commands[5],
			maker.Command{Args: []string{"docker", "tag", "local-proxy:latest", "registry.digitalocean.com/<REGISTRY_NAME>/proxy:latest"}},
			baseline.Commands[6],
			baseline.Commands[7],
		),
	}

	err := deploy.CheckStrictPlanCandidateRegression(baseline, candidate)
	if err == nil {
		t.Fatal("expected strict retention guard to reject new schema violation")
	}
	if !strings.Contains(err.Error(), "strict deploy-schema violation") {
		t.Fatalf("expected strict schema error, got %v", err)
	}
	if !strings.Contains(err.Error(), `"docker tag"`) {
		t.Fatalf("expected docker tag family in error, got %v", err)
	}
}
