package maker

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestMaterializeDOAppSpecArgSupportsEqualsForm(t *testing.T) {
	args := []string{"apps", "create", "--spec={\"name\":\"demo\",\"services\":[]}"}
	updated, cleanup, err := materializeDOAppSpecArg(args, nil)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		t.Fatalf("materializeDOAppSpecArg returned error: %v", err)
	}
	if len(updated) != len(args) {
		t.Fatalf("expected args length to stay the same, got %d", len(updated))
	}
	if !strings.HasPrefix(updated[2], "--spec=") {
		t.Fatalf("expected --spec= form to be preserved, got %q", updated[2])
	}
	path := strings.TrimPrefix(updated[2], "--spec=")
	if path == "" || strings.HasPrefix(path, "{") {
		t.Fatalf("expected inline spec to be materialized to a file path, got %q", path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected materialized spec file to exist: %v", err)
	}
}

func TestDockerCommandNeedsIsolatedConfigOnlyForPushLikeSteps(t *testing.T) {
	if dockerCommandNeedsIsolatedConfig([]string{"build", "--platform", "linux/amd64", "."}) {
		t.Fatal("expected docker build to keep normal Docker environment")
	}
	if !dockerCommandNeedsIsolatedConfig([]string{"push", "registry.digitalocean.com/demo/proxy:latest"}) {
		t.Fatal("expected docker push to use isolated Docker config")
	}
}

func TestDoctlCommandNeedsIsolatedConfigOnlyForRegistryLogin(t *testing.T) {
	if !doctlCommandNeedsIsolatedConfig([]string{"registry", "login"}) {
		t.Fatal("expected doctl registry login to use isolated Docker config")
	}
	if doctlCommandNeedsIsolatedConfig([]string{"compute", "droplet", "create"}) {
		t.Fatal("expected non-registry doctl commands to keep normal environment")
	}
}

func TestShouldRetryFreshRegistryPushOnlyForAuthFailureAfterCreate(t *testing.T) {
	if !shouldRetryFreshRegistryPush([]string{"docker", "push", "registry.digitalocean.com/demo/proxy:latest"}, "server message: insufficient_scope: authorization failed", true) {
		t.Fatal("expected fresh-registry auth failure to trigger retry")
	}
	if shouldRetryFreshRegistryPush([]string{"docker", "push", "registry.digitalocean.com/demo/proxy:latest"}, "server message: insufficient_scope: authorization failed", false) {
		t.Fatal("expected no retry when registry was not just created")
	}
	if shouldRetryFreshRegistryPush([]string{"docker", "build", "."}, "server message: insufficient_scope: authorization failed", true) {
		t.Fatal("expected non-push docker command to skip retry")
	}
}

func TestIsDORegistryLogin(t *testing.T) {
	if !isDORegistryLogin([]string{"registry", "login"}) {
		t.Fatal("expected registry login detection to succeed")
	}
	if isDORegistryLogin([]string{"registry", "create", "demo"}) {
		t.Fatal("expected non-login registry command to be ignored")
	}
}

func TestPlanNeedsDigitalOceanRegistryPush(t *testing.T) {
	plan := &Plan{
		Provider: "digitalocean",
		Commands: []Command{
			{Args: []string{"compute", "droplet", "create", "demo"}},
			{Args: []string{"docker", "push", "registry.digitalocean.com/demo/proxy:latest"}},
		},
	}
	if !PlanNeedsDigitalOceanRegistryPush(plan) {
		t.Fatal("expected DigitalOcean plan with docker push to require DOCR prereq probe")
	}
	if PlanNeedsDigitalOceanRegistryPush(&Plan{Provider: "digitalocean", Commands: []Command{{Args: []string{"compute", "droplet", "create", "demo"}}}}) {
		t.Fatal("expected DigitalOcean plan without docker push to skip DOCR prereq probe")
	}
	if PlanNeedsDigitalOceanRegistryPush(&Plan{Provider: "aws", Commands: []Command{{Args: []string{"docker", "push", "x"}}}}) {
		t.Fatal("expected non-DigitalOcean plan to skip DOCR prereq probe")
	}
}

func TestExtractFirewallBindingsDirectFromCreateResponse(t *testing.T) {
	bindings := map[string]string{}
	outputBytes, err := json.Marshal([]map[string]any{{
		"id":   "fw-123",
		"name": "openclaw-fw",
	}})
	if err != nil {
		t.Fatalf("marshal firewall output: %v", err)
	}

	extractFirewallBindingsDirect([]string{"compute", "firewall", "create", "--name", "openclaw-fw", "--output", "json"}, string(outputBytes), bindings)

	if got := bindings["FIREWALL_ID"]; got != "fw-123" {
		t.Fatalf("expected FIREWALL_ID to be extracted, got %q", got)
	}
}

func TestExtractDODropletBindingsDirectPrefersPublicIPv4(t *testing.T) {
	bindings := map[string]string{}
	outputBytes, err := json.Marshal([]map[string]any{{
		"id": 557771067,
		"networks": map[string]any{
			"v4": []map[string]any{
				{"ip_address": "10.116.0.6", "type": "private"},
				{"ip_address": "161.35.55.219", "type": "public"},
			},
		},
	}})
	if err != nil {
		t.Fatalf("marshal droplet output: %v", err)
	}

	extractDODropletBindingsDirect([]string{"compute", "droplet", "create", "openclaw", "--output", "json"}, string(outputBytes), bindings)

	if got := bindings["DROPLET_ID"]; got != "557771067" {
		t.Fatalf("expected DROPLET_ID to be extracted, got %q", got)
	}
	if got := bindings["DROPLET_IP"]; got != "161.35.55.219" {
		t.Fatalf("expected public DROPLET_IP to be extracted, got %q", got)
	}
}
