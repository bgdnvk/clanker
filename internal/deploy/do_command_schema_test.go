package deploy

import (
	"strings"
	"testing"

	"github.com/bgdnvk/clanker/internal/maker"
)

func TestValidateDigitalOceanCommandSchemaRejectsHallucinatedFamilies(t *testing.T) {
	plan := &maker.Plan{
		Provider: "digitalocean",
		Question: "Deploy OpenClaw to DigitalOcean with HTTPS",
		Capabilities: &maker.PlanCapabilities{
			Provider:     "digitalocean",
			AppKind:      "openclaw",
			RuntimeModel: "droplet-compose",
		},
		Commands: []maker.Command{
			{Args: []string{"registry", "docker-login"}},
			{Args: []string{"compute", "droplet", "create", "demo", "--tag", "openclaw"}},
			{Args: []string{"apps", "get", "demo-app"}},
		},
	}

	issues, _ := validateDigitalOceanCommandSchema(plan)
	joined := strings.Join(issues, "\n")
	if !strings.Contains(joined, "registry docker-login") {
		t.Fatalf("expected registry docker-login rejection, got %q", joined)
	}
	if !strings.Contains(joined, "--tag") {
		t.Fatalf("expected droplet --tag rejection, got %q", joined)
	}
	if !strings.Contains(joined, "outside the allowed deploy schema") {
		t.Fatalf("expected strict schema rejection, got %q", joined)
	}
}

func TestValidateDigitalOceanCommandSchemaAllowsNamedRegistryCreate(t *testing.T) {
	plan := &maker.Plan{
		Provider: "digitalocean",
		Question: "Deploy OpenClaw to DigitalOcean with HTTPS",
		Capabilities: &maker.PlanCapabilities{
			Provider:     "digitalocean",
			AppKind:      "openclaw",
			RuntimeModel: "droplet-compose",
		},
		Commands: []maker.Command{
			{Args: []string{"compute", "ssh-key", "import", "openclaw-key", "--public-key-file", "./id.pub"}, Produces: map[string]string{"SSH_KEY_ID": "$.id"}},
			{Args: []string{"compute", "firewall", "create", "openclaw-fw"}, Produces: map[string]string{"FIREWALL_ID": "$.id"}},
			{Args: []string{"compute", "droplet", "create", "openclaw", "--ssh-keys", "<SSH_KEY_ID>"}, Produces: map[string]string{"DROPLET_ID": "$.id", "DROPLET_IP": "$.droplet.networks.v4[0].ip_address"}},
			{Args: []string{"registry", "create", "openclaw-a4bbb8-registry"}, Produces: map[string]string{"REGISTRY_NAME": "$.name"}},
			{Args: []string{"registry", "login"}},
			{Args: []string{"docker", "build", "-t", "registry.digitalocean.com/<REGISTRY_NAME>/proxy:latest", "."}, Produces: map[string]string{"IMAGE_TAG": "registry.digitalocean.com/<REGISTRY_NAME>/proxy:latest"}},
			{Args: []string{"docker", "push", "<IMAGE_TAG>"}},
			{Args: []string{"apps", "create", "--spec", `{"services":[{"name":"proxy","image":{"registry":"<REGISTRY_NAME>","repository":"proxy","tag":"latest"}}]}`}, Produces: map[string]string{"APP_ID": "$.id"}},
		},
	}

	issues, _ := validateDigitalOceanCommandSchema(plan)
	if len(issues) > 0 {
		t.Fatalf("expected valid named registry create to pass, got %v", issues)
	}
}

func TestValidateCommandBindingSequenceRejectsOutOfOrderPlaceholder(t *testing.T) {
	issues := ValidateCommandBindingSequence(nil, []maker.Command{
		{Args: []string{"apps", "create", "--spec", `{\"services\":[{\"envs\":[{\"key\":\"UPSTREAM\",\"value\":\"http://<DROPLET_IP>:18789\"}]}]}`}},
		{Args: []string{"compute", "droplet", "create", "demo"}, Produces: map[string]string{"DROPLET_IP": "$.droplet.networks.v4[0].ip_address"}},
	}, nil)

	if len(issues) == 0 {
		t.Fatal("expected binding-order issue")
	}
	if !strings.Contains(issues[0], "<DROPLET_IP>") {
		t.Fatalf("expected DROPLET_IP in issue, got %q", issues[0])
	}
}

func TestValidatePlanPageBoundaryUsesExistingBindings(t *testing.T) {
	current := &maker.Plan{
		Provider: "digitalocean",
		Question: "Deploy OpenClaw to DigitalOcean with HTTPS",
		Capabilities: &maker.PlanCapabilities{
			Provider:     "digitalocean",
			AppKind:      "openclaw",
			RuntimeModel: "droplet-compose",
		},
		Commands: []maker.Command{
			{Args: []string{"compute", "ssh-key", "import", "demo", "--public-key-file", "./id.pub"}, Produces: map[string]string{"SSH_KEY_ID": "$.id"}},
		},
	}
	goodPage := &PlanPage{Commands: []maker.Command{{Args: []string{"compute", "droplet", "create", "demo", "--ssh-keys", "<SSH_KEY_ID>"}, Produces: map[string]string{"DROPLET_ID": "$.id"}}}}
	if err := ValidatePlanPageBoundary(current, goodPage, nil); err != nil {
		t.Fatalf("expected good page to pass, got %v", err)
	}

	badPage := &PlanPage{Commands: []maker.Command{{Args: []string{"compute", "firewall", "add-droplets", "<FIREWALL_ID>", "--droplet-ids", "<DROPLET_ID>"}}}}
	err := ValidatePlanPageBoundary(current, badPage, nil)
	if err == nil {
		t.Fatal("expected bad page to fail")
	}
	if !strings.Contains(err.Error(), "<DROPLET_ID>") {
		t.Fatalf("expected missing DROPLET_ID in error, got %v", err)
	}
}
