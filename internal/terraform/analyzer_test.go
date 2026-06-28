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

func TestScanWorkspaceSkipsSymlinkedTerraformFiles(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "outside.tf"), `resource "aws_db_instance" "leak" {}`)
	if err := os.Symlink(filepath.Join(outside, "outside.tf"), filepath.Join(dir, "outside.tf")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	metadata := scanWorkspace(dir)
	if len(metadata.files) != 0 {
		t.Fatalf("expected no terraform files from symlink, got %#v", metadata.files)
	}
	if len(metadata.resourceTypes) != 0 {
		t.Fatalf("expected no resource types from symlink, got %#v", metadata.resourceTypes)
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

func TestBuildViewReportDescribesLocalRemoteAndAlternatives(t *testing.T) {
	report := AnalysisReport{
		Path:            "/infra/prod",
		Tool:            "Terraform",
		Mode:            "remote-state",
		Remote:          true,
		Files:           []string{"main.tf", "network/vpc.tf"},
		Backends:        []string{"s3"},
		ProviderSources: []string{"hashicorp/aws"},
		Modules:         []string{"network"},
		State: &StateSummary{
			ResourceCount: 2,
			ResourceTypes: map[string]int{
				"aws_instance": 1,
				"aws_vpc":      1,
			},
			Sample: []string{"aws_instance.web", "aws_vpc.main"},
		},
		Drift: &DriftReport{
			Checked:    true,
			HasChanges: true,
			ExitCode:   2,
			Command:    "terraform plan -refresh-only -detailed-exitcode",
			Summary:    []string{"Plan: 0 to add, 1 to change, 0 to destroy."},
		},
		Alternatives: []AlternativeTool{
			{Name: "Terraform", Binary: "terraform", Detected: true},
			{Name: "OpenTofu", Binary: "tofu", Detected: false},
		},
	}

	view := BuildViewReport("prod", report)
	if view.Workspace != "prod" {
		t.Fatalf("workspace = %q, want prod", view.Workspace)
	}
	if view.Status != "attention" {
		t.Fatalf("status = %q, want attention", view.Status)
	}
	if view.Local.FileCount != 2 || view.State.Source != "remote" || view.State.Backend != "s3" {
		t.Fatalf("unexpected local/state view: %#v %#v", view.Local, view.State)
	}
	if view.Remote.DriftStatus != "changed" || !view.Remote.HasChanges {
		t.Fatalf("unexpected remote drift view: %#v", view.Remote)
	}
	if view.Drift == nil || view.Drift.Status != "changed" {
		t.Fatalf("unexpected drift view: %#v", view.Drift)
	}
	if len(view.Alternatives) != 2 || view.Alternatives[0].Status != "available" || view.Alternatives[1].Status != "missing" {
		t.Fatalf("unexpected alternatives: %#v", view.Alternatives)
	}
	if len(view.Summary) == 0 {
		t.Fatal("expected generated summary")
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
