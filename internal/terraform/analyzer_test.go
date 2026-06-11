package terraform

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScanWorkspaceDetectsBackendsProvidersModules(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "main.tf"), `
terraform {
  backend "s3" {}
  required_providers {
    aws = {
      source = "hashicorp/aws"
    }
  }
}

module "network" {
  source = "./modules/network"
}

resource "aws_instance" "web" {}
data "aws_iam_policy_document" "assume" {}
`)
	writeFile(t, filepath.Join(dir, "modules", "network", "main.tf"), `
resource "aws_vpc" "main" {}
`)

	metadata := scanWorkspace(dir)
	if !metadata.remote {
		t.Fatal("expected remote backend detection")
	}
	assertContains(t, metadata.backends, "s3")
	assertContains(t, metadata.providerSources, "hashicorp/aws")
	assertContains(t, metadata.modules, "network")
	assertContains(t, metadata.resourceTypes, "aws_instance")
	assertContains(t, metadata.resourceTypes, "aws_vpc")
	if len(metadata.files) != 2 {
		t.Fatalf("expected 2 terraform files, got %d: %#v", len(metadata.files), metadata.files)
	}
}

func TestDetectStaleArtifactsFlagsOldPlanAndState(t *testing.T) {
	dir := t.TempDir()
	planPath := filepath.Join(dir, "tfplan")
	statePath := filepath.Join(dir, "terraform.tfstate")
	writeFile(t, planPath, "plan")
	writeFile(t, statePath, "{}")
	old := time.Now().Add(-45 * 24 * time.Hour)
	if err := os.Chtimes(planPath, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(statePath, old, old); err != nil {
		t.Fatal(err)
	}

	stale := detectStaleArtifacts(dir)
	if len(stale) != 2 {
		t.Fatalf("expected 2 stale artifacts, got %d: %#v", len(stale), stale)
	}
	if stale[0].Path == "" || stale[0].Recommendation == "" {
		t.Fatalf("expected stale artifact path and recommendation, got %#v", stale[0])
	}
}

func TestResourceTypeFromAddress(t *testing.T) {
	tests := map[string]string{
		"aws_instance.web":                                   "aws_instance",
		"module.network.aws_vpc.main":                        "aws_vpc",
		"module.a.module.b.aws_security_group.rule[\"api\"]": "aws_security_group",
		"data.aws_iam_policy_document.assume":                "data.aws_iam_policy_document",
	}
	for address, want := range tests {
		if got := resourceTypeFromAddress(address); got != want {
			t.Errorf("resourceTypeFromAddress(%q) = %q, want %q", address, got, want)
		}
	}
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertContains(t *testing.T, values []string, want string) {
	t.Helper()
	for _, value := range values {
		if value == want {
			return
		}
	}
	t.Fatalf("expected %#v to contain %q", values, want)
}
