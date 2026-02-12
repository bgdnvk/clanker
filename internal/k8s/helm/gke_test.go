package helm

import (
	"strings"
	"testing"
)

func TestGKEArtifactRegistryURL(t *testing.T) {
	tests := []struct {
		region  string
		project string
		repo    string
		want    string
	}{
		{
			region:  "us-central1",
			project: "my-project",
			repo:    "helm-charts",
			want:    "oci://us-central1-docker.pkg.dev/my-project/helm-charts",
		},
		{
			region:  "europe-west1",
			project: "test-project",
			repo:    "charts",
			want:    "oci://europe-west1-docker.pkg.dev/test-project/charts",
		},
	}

	for _, tt := range tests {
		t.Run(tt.region+"/"+tt.project, func(t *testing.T) {
			got := GKEArtifactRegistryURL(tt.region, tt.project, tt.repo)
			if got != tt.want {
				t.Errorf("GKEArtifactRegistryURL() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestGKEArtifactRegistryAuthHints(t *testing.T) {
	hints := GKEArtifactRegistryAuthHints()

	if len(hints) == 0 {
		t.Error("expected at least one auth hint")
	}

	hintsText := strings.Join(hints, " ")

	expectedTopics := []string{
		"gcloud",
		"credential",
		"helm registry login",
		"Workload Identity",
	}

	for _, topic := range expectedTopics {
		if !strings.Contains(hintsText, topic) {
			t.Errorf("auth hints should mention %s", topic)
		}
	}
}

func TestGKERecommendedRepos(t *testing.T) {
	repos := GKERecommendedRepos()

	if len(repos) == 0 {
		t.Error("expected at least one recommended repo")
	}

	for _, repo := range repos {
		if repo.Name == "" {
			t.Error("repo name should not be empty")
		}
		if repo.URL == "" {
			t.Error("repo URL should not be empty")
		}
		if !strings.HasPrefix(repo.URL, "https://") {
			t.Errorf("repo URL should start with https://, got %s", repo.URL)
		}
	}
}

func TestGetGKEChartRecommendations(t *testing.T) {
	tests := []struct {
		name                 string
		useCase              string
		wantChartNameContains string
	}{
		{
			name:                 "Config Connector",
			useCase:              "manage gcp resources",
			wantChartNameContains: "Config Connector",
		},
		{
			name:                 "Ingress",
			useCase:              "ingress with tls",
			wantChartNameContains: "cert-manager",
		},
		{
			name:                 "Monitoring",
			useCase:              "prometheus monitoring",
			wantChartNameContains: "Prometheus",
		},
		{
			name:                 "Service Mesh",
			useCase:              "istio service mesh",
			wantChartNameContains: "Istio",
		},
		{
			name:                 "Secrets",
			useCase:              "secrets management",
			wantChartNameContains: "Secrets",
		},
		{
			name:                 "CI/CD",
			useCase:              "argocd deployment",
			wantChartNameContains: "Argo",
		},
		{
			name:                 "Database",
			useCase:              "postgresql database",
			wantChartNameContains: "PG",
		},
		{
			name:                 "Default",
			useCase:              "general application",
			wantChartNameContains: "nginx",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recommendations := GetGKEChartRecommendations(tt.useCase)

			if len(recommendations) == 0 {
				t.Error("expected at least one chart recommendation")
				return
			}

			found := false
			for _, rec := range recommendations {
				if strings.Contains(rec.Name, tt.wantChartNameContains) {
					found = true
					break
				}
			}

			if !found {
				t.Errorf("expected recommendation containing %s for use case %s", tt.wantChartNameContains, tt.useCase)
			}

			// Verify all recommendations have required fields
			for _, rec := range recommendations {
				if rec.Name == "" {
					t.Error("recommendation name should not be empty")
				}
				if rec.Description == "" {
					t.Error("recommendation description should not be empty")
				}
			}
		})
	}
}

func TestGKEHelmNotes(t *testing.T) {
	notes := GKEHelmNotes()

	if len(notes) == 0 {
		t.Error("expected at least one helm note")
	}

	notesText := strings.Join(notes, " ")

	expectedTopics := []string{
		"Artifact Registry",
		"Workload Identity",
		"Config Connector",
		"Managed Prometheus",
	}

	for _, topic := range expectedTopics {
		if !strings.Contains(notesText, topic) {
			t.Errorf("helm notes should mention %s", topic)
		}
	}
}

func TestGetGKEArtifactRegistryCommand(t *testing.T) {
	cmd := GetGKEArtifactRegistryCommand("us-central1")

	if !strings.Contains(cmd, "gcloud auth configure-docker") {
		t.Error("expected gcloud auth configure-docker command")
	}

	if !strings.Contains(cmd, "us-central1-docker.pkg.dev") {
		t.Error("expected region-specific registry URL")
	}
}

func TestGetGKEHelmLoginCommand(t *testing.T) {
	cmd := GetGKEHelmLoginCommand("europe-west1")

	if !strings.Contains(cmd, "gcloud auth print-access-token") {
		t.Error("expected gcloud auth command")
	}

	if !strings.Contains(cmd, "helm registry login") {
		t.Error("expected helm registry login command")
	}

	if !strings.Contains(cmd, "europe-west1-docker.pkg.dev") {
		t.Error("expected region-specific registry URL")
	}
}

func TestIsGKEArtifactRegistryURL(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"oci://us-central1-docker.pkg.dev/my-project/helm-charts", true},
		{"oci://europe-west1-docker.pkg.dev/test/repo", true},
		{"https://charts.example.com", false},
		{"oci://ghcr.io/myorg/mychart", false},
		{"us-central1-docker.pkg.dev/project/repo", false}, // Missing oci:// prefix
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			got := IsGKEArtifactRegistryURL(tt.url)
			if got != tt.want {
				t.Errorf("IsGKEArtifactRegistryURL(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

func TestParseGKEArtifactRegistryURL(t *testing.T) {
	tests := []struct {
		url         string
		wantRegion  string
		wantProject string
		wantRepo    string
		wantErr     bool
	}{
		{
			url:         "oci://us-central1-docker.pkg.dev/my-project/helm-charts",
			wantRegion:  "us-central1",
			wantProject: "my-project",
			wantRepo:    "helm-charts",
			wantErr:     false,
		},
		{
			url:         "oci://europe-west1-docker.pkg.dev/test-project/charts",
			wantRegion:  "europe-west1",
			wantProject: "test-project",
			wantRepo:    "charts",
			wantErr:     false,
		},
		{
			url:     "https://charts.example.com",
			wantErr: true,
		},
		{
			url:     "oci://ghcr.io/org/chart",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			region, project, repo, err := ParseGKEArtifactRegistryURL(tt.url)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if region != tt.wantRegion {
				t.Errorf("region = %s, want %s", region, tt.wantRegion)
			}

			if project != tt.wantProject {
				t.Errorf("project = %s, want %s", project, tt.wantProject)
			}

			if repo != tt.wantRepo {
				t.Errorf("repo = %s, want %s", repo, tt.wantRepo)
			}
		})
	}
}

func TestGKEChartSourceRecommendation(t *testing.T) {
	tests := []struct {
		name              string
		useCase           string
		wantSourceContains string
	}{
		{
			name:              "Enterprise",
			useCase:           "enterprise production deployment",
			wantSourceContains: "Artifact Registry",
		},
		{
			name:              "Internal",
			useCase:           "internal organization charts",
			wantSourceContains: "Artifact Registry",
		},
		{
			name:              "Development",
			useCase:           "development testing",
			wantSourceContains: "Public",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := GKEChartSourceRecommendation(tt.useCase)

			if !strings.Contains(rec.Source, tt.wantSourceContains) {
				t.Errorf("source = %s, want containing %s", rec.Source, tt.wantSourceContains)
			}

			if rec.Reason == "" {
				t.Error("recommendation should have a reason")
			}

			if rec.Setup == "" {
				t.Error("recommendation should have setup instructions")
			}

			if len(rec.Advantages) == 0 {
				t.Error("recommendation should have advantages")
			}
		})
	}
}

func TestEKSHelmComparison(t *testing.T) {
	comparison := EKSHelmComparison()

	if len(comparison) == 0 {
		t.Error("expected comparison entries")
	}

	// Verify GKE entries
	gkeKeys := []string{"gke_registry", "gke_auth", "gke_gcp_charts"}
	for _, key := range gkeKeys {
		if _, ok := comparison[key]; !ok {
			t.Errorf("expected comparison key %s", key)
		}
	}

	// Verify EKS entries
	eksKeys := []string{"eks_registry", "eks_auth", "eks_aws_charts"}
	for _, key := range eksKeys {
		if _, ok := comparison[key]; !ok {
			t.Errorf("expected comparison key %s", key)
		}
	}

	// Verify specific values
	if comparison["gke_registry"] != "Artifact Registry (OCI)" {
		t.Errorf("gke_registry = %s, want Artifact Registry (OCI)", comparison["gke_registry"])
	}

	if comparison["eks_registry"] != "ECR (OCI)" {
		t.Errorf("eks_registry = %s, want ECR (OCI)", comparison["eks_registry"])
	}
}

func TestGKERepoConstants(t *testing.T) {
	if GKERepoConfigConnector != "https://charts.config-connector.cloud.google.com" {
		t.Errorf("GKERepoConfigConnector = %s, want https://charts.config-connector.cloud.google.com", GKERepoConfigConnector)
	}

	if !strings.HasPrefix(GKERepoGoogleCloud, "https://") {
		t.Errorf("GKERepoGoogleCloud should start with https://, got %s", GKERepoGoogleCloud)
	}
}

func TestGKEArtifactRegistryURLFormat(t *testing.T) {
	if GKEArtifactRegistryURLFormat != "oci://%s-docker.pkg.dev/%s/%s" {
		t.Errorf("GKEArtifactRegistryURLFormat = %s, want oci://%%s-docker.pkg.dev/%%s/%%s", GKEArtifactRegistryURLFormat)
	}
}

func TestGKEChartRecommendationStruct(t *testing.T) {
	rec := GKEChartRecommendation{
		Name:        "Test Chart",
		Chart:       "test/chart",
		Repo:        "https://test.example.com",
		Description: "Test description",
		Notes:       []string{"Note 1", "Note 2"},
		Values:      []string{"--set key=value"},
	}

	if rec.Name != "Test Chart" {
		t.Errorf("expected name 'Test Chart', got %s", rec.Name)
	}

	if len(rec.Notes) != 2 {
		t.Errorf("expected 2 notes, got %d", len(rec.Notes))
	}

	if len(rec.Values) != 1 {
		t.Errorf("expected 1 value, got %d", len(rec.Values))
	}
}

func TestGKEChartSourceRecStruct(t *testing.T) {
	rec := GKEChartSourceRec{
		Source:     "Artifact Registry",
		Reason:     "Test reason",
		Setup:      "Test setup",
		AuthMethod: "Workload Identity",
		Advantages: []string{"Advantage 1"},
	}

	if rec.Source != "Artifact Registry" {
		t.Errorf("expected source 'Artifact Registry', got %s", rec.Source)
	}

	if rec.AuthMethod != "Workload Identity" {
		t.Errorf("expected auth method 'Workload Identity', got %s", rec.AuthMethod)
	}
}
