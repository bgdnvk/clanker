package helm

import (
	"strings"
	"testing"
)

func TestAKSACRURL(t *testing.T) {
	url := AKSACRURL("myregistry", "helm/mychart")

	expectedContents := []string{
		"oci://",
		"myregistry",
		"azurecr.io",
		"helm/mychart",
	}

	for _, expected := range expectedContents {
		if !strings.Contains(url, expected) {
			t.Errorf("ACR URL should contain %s, got %s", expected, url)
		}
	}
}

func TestAKSACRAuthHints(t *testing.T) {
	hints := AKSACRAuthHints()

	if len(hints) == 0 {
		t.Error("expected at least one ACR auth hint")
	}

	hintsText := strings.Join(hints, " ")

	expectedTopics := []string{
		"az aks update",
		"attach-acr",
		"az acr login",
		"helm registry login",
		"Managed Identity",
		"Workload Identity",
	}

	for _, topic := range expectedTopics {
		if !strings.Contains(hintsText, topic) {
			t.Errorf("ACR auth hints should mention %s", topic)
		}
	}
}

func TestAKSRecommendedRepos(t *testing.T) {
	repos := AKSRecommendedRepos()

	if len(repos) == 0 {
		t.Error("expected at least one recommended repo")
	}

	// Check for expected repos
	repoNames := make(map[string]bool)
	for _, repo := range repos {
		repoNames[repo.Name] = true
		if repo.URL == "" {
			t.Errorf("repo %s should have a URL", repo.Name)
		}
	}

	expectedRepos := []string{
		"azure-marketplace",
		"microsoft",
		"application-gateway-kubernetes-ingress",
	}

	for _, expected := range expectedRepos {
		if !repoNames[expected] {
			t.Errorf("expected recommended repo %s", expected)
		}
	}
}

func TestGetAKSChartRecommendations(t *testing.T) {
	tests := []struct {
		name        string
		useCase     string
		wantChart   string
		wantCount   int
	}{
		{
			name:      "KEDA autoscaling",
			useCase:   "event-driven autoscaling",
			wantChart: "KEDA",
			wantCount: 1,
		},
		{
			name:      "Key Vault secrets",
			useCase:   "azure key vault secrets",
			wantChart: "Secrets Store CSI Driver",
			wantCount: 2,
		},
		{
			name:      "AGIC ingress",
			useCase:   "application gateway ingress",
			wantChart: "Application Gateway Ingress Controller",
			wantCount: 2,
		},
		{
			name:      "Service mesh",
			useCase:   "open service mesh",
			wantChart: "Open Service Mesh",
			wantCount: 2,
		},
		{
			name:      "GitOps",
			useCase:   "gitops flux deployment",
			wantChart: "Flux",
			wantCount: 2,
		},
		{
			name:      "Monitoring",
			useCase:   "prometheus grafana monitoring",
			wantChart: "Azure Monitor",
			wantCount: 2,
		},
		{
			name:      "Dapr",
			useCase:   "dapr microservices",
			wantChart: "Dapr",
			wantCount: 1,
		},
		{
			name:      "Database",
			useCase:   "postgres database",
			wantChart: "CloudNativePG",
			wantCount: 2,
		},
		{
			name:      "Default",
			useCase:   "general application",
			wantChart: "nginx-ingress",
			wantCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recs := GetAKSChartRecommendations(tt.useCase)

			if len(recs) < tt.wantCount {
				t.Errorf("expected at least %d recommendations, got %d", tt.wantCount, len(recs))
			}

			found := false
			for _, rec := range recs {
				if strings.Contains(rec.Name, tt.wantChart) {
					found = true
					if rec.Description == "" {
						t.Errorf("recommendation %s should have a description", rec.Name)
					}
					if len(rec.Notes) == 0 {
						t.Errorf("recommendation %s should have notes", rec.Name)
					}
					break
				}
			}

			if !found {
				t.Errorf("expected chart recommendation %s not found", tt.wantChart)
			}
		})
	}
}

func TestAKSHelmNotes(t *testing.T) {
	notes := AKSHelmNotes()

	if len(notes) == 0 {
		t.Error("expected at least one Helm note")
	}

	notesText := strings.Join(notes, " ")

	expectedTopics := []string{
		"ACR",
		"Workload Identity",
		"KEDA",
		"AGIC",
		"Flux",
		"Prometheus",
		"Dapr",
	}

	for _, topic := range expectedTopics {
		if !strings.Contains(notesText, topic) {
			t.Errorf("Helm notes should mention %s", topic)
		}
	}
}

func TestGetAKSACRLoginCommand(t *testing.T) {
	cmd := GetAKSACRLoginCommand("myregistry")

	if !strings.Contains(cmd, "az acr login") {
		t.Error("command should contain 'az acr login'")
	}

	if !strings.Contains(cmd, "myregistry") {
		t.Error("command should contain registry name")
	}
}

func TestGetAKSHelmLoginCommand(t *testing.T) {
	cmd := GetAKSHelmLoginCommand("myregistry")

	expectedContents := []string{
		"az acr login",
		"expose-token",
		"helm registry login",
		"myregistry",
		"azurecr.io",
	}

	for _, expected := range expectedContents {
		if !strings.Contains(cmd, expected) {
			t.Errorf("Helm login command should contain %s", expected)
		}
	}
}

func TestIsAKSACRURL(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"oci://myregistry.azurecr.io/helm/chart", true},
		{"oci://test.azurecr.io/repo", true},
		{"https://myregistry.azurecr.io/helm/chart", false},
		{"oci://us-central1-docker.pkg.dev/project/repo", false},
		{"oci://123456789.dkr.ecr.us-east-1.amazonaws.com/repo", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			got := IsAKSACRURL(tt.url)
			if got != tt.want {
				t.Errorf("IsAKSACRURL(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

func TestParseAKSACRURL(t *testing.T) {
	tests := []struct {
		name         string
		url          string
		wantRegistry string
		wantRepo     string
		wantErr      bool
	}{
		{
			name:         "Valid URL",
			url:          "oci://myregistry.azurecr.io/helm/mychart",
			wantRegistry: "myregistry",
			wantRepo:     "helm/mychart",
			wantErr:      false,
		},
		{
			name:         "Simple repo",
			url:          "oci://test.azurecr.io/repo",
			wantRegistry: "test",
			wantRepo:     "repo",
			wantErr:      false,
		},
		{
			name:    "Invalid URL - not ACR",
			url:     "oci://us-central1-docker.pkg.dev/project/repo",
			wantErr: true,
		},
		{
			name:    "Invalid URL - not OCI",
			url:     "https://myregistry.azurecr.io/repo",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registryName, repo, err := ParseAKSACRURL(tt.url)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if registryName != tt.wantRegistry {
				t.Errorf("registryName = %s, want %s", registryName, tt.wantRegistry)
			}

			if repo != tt.wantRepo {
				t.Errorf("repo = %s, want %s", repo, tt.wantRepo)
			}
		})
	}
}

func TestAKSChartSourceRecommendation(t *testing.T) {
	tests := []struct {
		name       string
		useCase    string
		wantSource string
	}{
		{
			name:       "Enterprise",
			useCase:    "enterprise production deployment",
			wantSource: "Azure Container Registry (ACR)",
		},
		{
			name:       "Internal",
			useCase:    "internal private organization",
			wantSource: "Azure Container Registry (ACR)",
		},
		{
			name:       "Development",
			useCase:    "general development",
			wantSource: "Public Helm repositories",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := AKSChartSourceRecommendation(tt.useCase)

			if rec.Source != tt.wantSource {
				t.Errorf("Source = %s, want %s", rec.Source, tt.wantSource)
			}

			if rec.Reason == "" {
				t.Error("recommendation should have a reason")
			}

			if rec.Setup == "" {
				t.Error("recommendation should have setup instructions")
			}

			if rec.AuthMethod == "" {
				t.Error("recommendation should have auth method")
			}

			if len(rec.Advantages) == 0 {
				t.Error("recommendation should have advantages")
			}
		})
	}
}

func TestGKEHelmComparisonWithAKS(t *testing.T) {
	comparison := GKEHelmComparisonWithAKS()

	if len(comparison) == 0 {
		t.Error("expected Helm comparison entries")
	}

	// Verify AKS entries
	aksKeys := []string{"aks_registry", "aks_auth", "aks_azure_charts", "aks_service_mesh", "aks_gitops"}
	for _, key := range aksKeys {
		if _, ok := comparison[key]; !ok {
			t.Errorf("expected comparison key %s", key)
		}
	}

	// Verify GKE entries
	gkeKeys := []string{"gke_registry", "gke_auth", "gke_gcp_charts", "gke_service_mesh", "gke_gitops"}
	for _, key := range gkeKeys {
		if _, ok := comparison[key]; !ok {
			t.Errorf("expected comparison key %s", key)
		}
	}

	// Verify EKS entries
	eksKeys := []string{"eks_registry", "eks_auth", "eks_aws_charts", "eks_service_mesh", "eks_gitops"}
	for _, key := range eksKeys {
		if _, ok := comparison[key]; !ok {
			t.Errorf("expected comparison key %s", key)
		}
	}
}

func TestAKSACRURLFormatConstant(t *testing.T) {
	if AKSACRURLFormat != "oci://%s.azurecr.io/%s" {
		t.Errorf("AKSACRURLFormat = %s, want oci://%%s.azurecr.io/%%s", AKSACRURLFormat)
	}
}

func TestAKSRepoConstants(t *testing.T) {
	if AKSRepoAzureMarketplace != "https://marketplace.azurecr.io/helm/v1/repo" {
		t.Errorf("AKSRepoAzureMarketplace = %s, want https://marketplace.azurecr.io/helm/v1/repo", AKSRepoAzureMarketplace)
	}

	if !strings.Contains(AKSRepoMicrosoft, "microsoft.github.io") {
		t.Errorf("AKSRepoMicrosoft should contain microsoft.github.io")
	}
}

func TestAKSChartRecommendationStruct(t *testing.T) {
	rec := AKSChartRecommendation{
		Name:        "Test Chart",
		Chart:       "test/chart",
		Repo:        "https://test.repo",
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

func TestAKSChartSourceRecStruct(t *testing.T) {
	rec := AKSChartSourceRec{
		Source:     "ACR",
		Reason:     "Test reason",
		Setup:      "Test setup",
		AuthMethod: "Managed Identity",
		Advantages: []string{"Advantage 1"},
	}

	if rec.Source != "ACR" {
		t.Errorf("expected source 'ACR', got %s", rec.Source)
	}

	if rec.AuthMethod != "Managed Identity" {
		t.Errorf("expected auth method 'Managed Identity', got %s", rec.AuthMethod)
	}
}
