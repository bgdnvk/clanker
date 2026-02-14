package helm

import (
	"fmt"
	"strings"
)

// GKE Artifact Registry constants
const (
	// GKEArtifactRegistryURLFormat is the OCI format for GKE Artifact Registry
	// Format: oci://{region}-docker.pkg.dev/{project}/{repository}
	GKEArtifactRegistryURLFormat = "oci://%s-docker.pkg.dev/%s/%s"
)

// GKE recommended repositories
const (
	// GKERepoConfigConnector is the Config Connector chart repository
	GKERepoConfigConnector = "https://charts.config-connector.cloud.google.com"
	// GKERepoGoogleCloud is the Google Cloud charts repository
	GKERepoGoogleCloud = "https://googlecloudplatform.github.io/gke-managed-certs"
)

// GKEArtifactRegistryURL builds an OCI URL for GKE Artifact Registry
func GKEArtifactRegistryURL(region, project, repo string) string {
	return fmt.Sprintf(GKEArtifactRegistryURLFormat, region, project, repo)
}

// GKEArtifactRegistryAuthHints returns authentication guidance for Artifact Registry
func GKEArtifactRegistryAuthHints() []string {
	return []string{
		"Configure Docker credential helper for Artifact Registry:",
		"  gcloud auth configure-docker REGION-docker.pkg.dev",
		"",
		"For OCI registry access with Helm:",
		"  gcloud auth application-default login",
		"  gcloud auth print-access-token | helm registry login -u oauth2accesstoken --password-stdin REGION-docker.pkg.dev",
		"",
		"For service account authentication:",
		"  gcloud auth activate-service-account --key-file=KEY_FILE",
		"  gcloud auth print-access-token | helm registry login -u oauth2accesstoken --password-stdin REGION-docker.pkg.dev",
		"",
		"Workload Identity (recommended for GKE):",
		"  - Annotate K8s SA with GCP SA: iam.gke.io/gcp-service-account=GSA@PROJECT.iam.gserviceaccount.com",
		"  - Bind K8s SA to GCP SA with Workload Identity User role",
		"  - Pods automatically get credentials via Workload Identity",
	}
}

// GKERecommendedRepos returns GKE-specific recommended Helm repositories
func GKERecommendedRepos() []RepoInfo {
	return []RepoInfo{
		{
			Name: "config-connector",
			URL:  GKERepoConfigConnector,
		},
		{
			Name: "gke-managed-certs",
			URL:  GKERepoGoogleCloud,
		},
	}
}

// GKEChartRecommendation represents a recommended chart for a use case
type GKEChartRecommendation struct {
	Name        string   `json:"name"`
	Chart       string   `json:"chart"`
	Repo        string   `json:"repo,omitempty"`
	Description string   `json:"description"`
	Notes       []string `json:"notes,omitempty"`
	Values      []string `json:"values,omitempty"`
}

// GetGKEChartRecommendations returns GKE-optimized chart suggestions for common use cases
func GetGKEChartRecommendations(useCase string) []GKEChartRecommendation {
	useCaseLower := strings.ToLower(useCase)

	// Config Connector for managing GCP resources
	if containsAny(useCaseLower, []string{"gcp", "google cloud", "config connector", "infrastructure"}) {
		return []GKEChartRecommendation{
			{
				Name:        "Config Connector",
				Chart:       "configconnector.cloud.google.com/configconnector",
				Repo:        GKERepoConfigConnector,
				Description: "Manage GCP resources via Kubernetes Custom Resources",
				Notes: []string{
					"Requires Workload Identity for authentication",
					"Supports 200+ GCP resource types",
					"Enables GitOps for GCP infrastructure",
				},
				Values: []string{
					"--set global.name=configconnector.cloud.google.com",
				},
			},
		}
	}

	// Ingress and certificates
	if containsAny(useCaseLower, []string{"ingress", "certificate", "tls", "ssl", "https"}) {
		return []GKEChartRecommendation{
			{
				Name:        "cert-manager",
				Chart:       "jetstack/cert-manager",
				Description: "Certificate management controller for Kubernetes",
				Notes: []string{
					"Works with GKE Ingress for automatic TLS",
					"Supports Let's Encrypt and Google CA Service",
					"Install CRDs separately: --set installCRDs=true",
				},
				Values: []string{
					"--set installCRDs=true",
					"--set global.leaderElection.namespace=cert-manager",
				},
			},
			{
				Name:        "external-dns",
				Chart:       "bitnami/external-dns",
				Description: "Synchronize Ingress/Service resources with Cloud DNS",
				Notes: []string{
					"Automatically creates DNS records for Ingress/Service",
					"Supports Cloud DNS with Workload Identity",
					"Configure provider: --set provider=google",
				},
				Values: []string{
					"--set provider=google",
					"--set google.project=PROJECT_ID",
				},
			},
		}
	}

	// Monitoring and observability
	if containsAny(useCaseLower, []string{"monitoring", "metrics", "prometheus", "observability", "grafana"}) {
		return []GKEChartRecommendation{
			{
				Name:        "Prometheus Stack (with GKE integration)",
				Chart:       "prometheus-community/kube-prometheus-stack",
				Description: "Complete monitoring stack with Prometheus and Grafana",
				Notes: []string{
					"Consider GKE Managed Prometheus for reduced operational overhead",
					"Use PodMonitoring CRD with GKE Managed Prometheus",
					"Integrates with Cloud Monitoring for unified observability",
				},
				Values: []string{
					"--set prometheus.prometheusSpec.serviceMonitorSelectorNilUsesHelmValues=false",
				},
			},
			{
				Name:        "OpenTelemetry Collector",
				Chart:       "open-telemetry/opentelemetry-collector",
				Description: "Vendor-agnostic telemetry data collection",
				Notes: []string{
					"Export to Cloud Trace and Cloud Monitoring",
					"Supports Workload Identity for authentication",
					"Configure googlecloud exporter for GCP integration",
				},
			},
		}
	}

	// Service mesh
	if containsAny(useCaseLower, []string{"service mesh", "istio", "anthos"}) {
		return []GKEChartRecommendation{
			{
				Name:        "Istio (via istioctl)",
				Chart:       "N/A - Use istioctl or Anthos Service Mesh",
				Description: "Service mesh for GKE",
				Notes: []string{
					"Prefer Anthos Service Mesh for managed Istio on GKE",
					"ASM provides managed control plane and automatic updates",
					"Enable ASM via: gcloud container fleet mesh enable",
					"For manual Istio: istioctl install --set profile=default",
				},
			},
		}
	}

	// Secrets management
	if containsAny(useCaseLower, []string{"secrets", "vault", "secret manager"}) {
		return []GKEChartRecommendation{
			{
				Name:        "External Secrets Operator",
				Chart:       "external-secrets/external-secrets",
				Description: "Sync secrets from GCP Secret Manager to Kubernetes",
				Notes: []string{
					"Supports GCP Secret Manager as backend",
					"Uses Workload Identity for authentication",
					"Create SecretStore CRD pointing to Secret Manager",
				},
				Values: []string{
					"--set installCRDs=true",
				},
			},
			{
				Name:        "HashiCorp Vault",
				Chart:       "hashicorp/vault",
				Description: "Secrets management with Vault",
				Notes: []string{
					"Consider GCP Secret Manager for simpler setup",
					"Vault supports GCP auth method for GKE",
					"Use Workload Identity with Vault GCP auth",
				},
			},
		}
	}

	// CI/CD
	if containsAny(useCaseLower, []string{"ci", "cd", "cicd", "pipeline", "argocd", "flux"}) {
		return []GKEChartRecommendation{
			{
				Name:        "Argo CD",
				Chart:       "argo/argo-cd",
				Description: "GitOps continuous delivery for Kubernetes",
				Notes: []string{
					"Supports Artifact Registry as OCI chart source",
					"Use Workload Identity for GCP authentication",
					"Configure repo credentials for private repos",
				},
				Values: []string{
					"--set server.service.type=ClusterIP",
				},
			},
			{
				Name:        "Tekton",
				Chart:       "N/A - Use kubectl apply",
				Description: "Cloud-native CI/CD pipelines",
				Notes: []string{
					"Install via: kubectl apply -f https://storage.googleapis.com/tekton-releases/pipeline/latest/release.yaml",
					"Native GKE integration available",
					"Consider Cloud Build for managed CI/CD",
				},
			},
		}
	}

	// Database
	if containsAny(useCaseLower, []string{"database", "postgres", "mysql", "redis", "mongodb"}) {
		return []GKEChartRecommendation{
			{
				Name:        "CloudNativePG",
				Chart:       "cloudnative-pg/cloudnative-pg",
				Description: "Cloud-native PostgreSQL operator",
				Notes: []string{
					"Consider Cloud SQL for managed PostgreSQL",
					"Use Cloud SQL Auth Proxy for Cloud SQL connectivity",
					"Supports backup to Cloud Storage",
				},
			},
			{
				Name:        "Redis",
				Chart:       "bitnami/redis",
				Description: "Redis in-memory data store",
				Notes: []string{
					"Consider Memorystore for managed Redis",
					"Use premium-rwo storage class for performance",
					"Configure persistence with GKE storage classes",
				},
				Values: []string{
					"--set global.storageClass=premium-rwo",
				},
			},
		}
	}

	// Default recommendations
	return []GKEChartRecommendation{
		{
			Name:        "nginx-ingress",
			Chart:       "ingress-nginx/ingress-nginx",
			Description: "Ingress controller (use native GKE Ingress for Google integration)",
			Notes: []string{
				"GKE native Ingress (gce class) is recommended for Google integration",
				"nginx-ingress useful for advanced configuration needs",
				"Consider Container-native load balancing with NEG",
			},
		},
	}
}

// GKEHelmNotes returns important notes about using Helm with GKE
func GKEHelmNotes() []string {
	return []string{
		"GKE Artifact Registry supports OCI Helm charts",
		"Use Workload Identity for secure registry authentication",
		"Config Connector enables managing GCP resources via Kubernetes",
		"Consider managed services (Cloud SQL, Memorystore) over self-managed charts",
		"GKE Managed Prometheus can replace self-hosted Prometheus",
		"Anthos Service Mesh provides managed Istio with auto-updates",
		"External Secrets Operator integrates with GCP Secret Manager",
	}
}

// GetGKEArtifactRegistryCommand returns the gcloud command to configure Artifact Registry
func GetGKEArtifactRegistryCommand(region string) string {
	return fmt.Sprintf("gcloud auth configure-docker %s-docker.pkg.dev", region)
}

// GetGKEHelmLoginCommand returns the command to login to Artifact Registry with Helm
func GetGKEHelmLoginCommand(region string) string {
	return fmt.Sprintf(
		"gcloud auth print-access-token | helm registry login -u oauth2accesstoken --password-stdin %s-docker.pkg.dev",
		region)
}

// IsGKEArtifactRegistryURL checks if a URL is a GKE Artifact Registry URL
func IsGKEArtifactRegistryURL(url string) bool {
	return strings.Contains(url, "-docker.pkg.dev") && strings.HasPrefix(url, "oci://")
}

// ParseGKEArtifactRegistryURL parses a GKE Artifact Registry URL into components
func ParseGKEArtifactRegistryURL(url string) (region, project, repo string, err error) {
	if !IsGKEArtifactRegistryURL(url) {
		return "", "", "", fmt.Errorf("not a valid GKE Artifact Registry URL: %s", url)
	}

	// Remove oci:// prefix
	trimmed := strings.TrimPrefix(url, "oci://")

	// Split by /
	parts := strings.SplitN(trimmed, "/", 3)
	if len(parts) < 3 {
		return "", "", "", fmt.Errorf("invalid Artifact Registry URL format: %s", url)
	}

	// Extract region from hostname (e.g., us-central1-docker.pkg.dev)
	host := parts[0]
	region = strings.TrimSuffix(host, "-docker.pkg.dev")

	project = parts[1]
	repo = parts[2]

	return region, project, repo, nil
}

// GKEChartSourceRecommendation returns recommendations for chart sources on GKE
func GKEChartSourceRecommendation(useCase string) GKEChartSourceRec {
	useCaseLower := strings.ToLower(useCase)

	// Enterprise/production
	if containsAny(useCaseLower, []string{"enterprise", "production", "secure", "compliance"}) {
		return GKEChartSourceRec{
			Source:     "Artifact Registry",
			Reason:     "Private OCI registry with IAM integration and vulnerability scanning",
			Setup:      "Create Artifact Registry repo, push charts as OCI artifacts",
			AuthMethod: "Workload Identity",
			Advantages: []string{
				"IAM-based access control",
				"Vulnerability scanning for container images",
				"Audit logging",
				"Regional and multi-regional options",
			},
		}
	}

	// Internal/private
	if containsAny(useCaseLower, []string{"internal", "private", "organization"}) {
		return GKEChartSourceRec{
			Source:     "Artifact Registry",
			Reason:     "Private registry for organization-internal charts",
			Setup:      "Create Artifact Registry repo with appropriate IAM",
			AuthMethod: "Workload Identity or Service Account",
			Advantages: []string{
				"Private by default",
				"Integration with Cloud Build",
				"Supports OCI and traditional Helm repos",
			},
		}
	}

	// Default for development
	return GKEChartSourceRec{
		Source:     "Public Helm repositories",
		Reason:     "Easy access to community charts for development",
		Setup:      "Use helm repo add for public repositories",
		AuthMethod: "None required",
		Advantages: []string{
			"Quick setup",
			"Access to popular charts (bitnami, grafana, etc.)",
			"No additional GCP costs",
		},
	}
}

// GKEChartSourceRec represents a chart source recommendation
type GKEChartSourceRec struct {
	Source     string   `json:"source"`
	Reason     string   `json:"reason"`
	Setup      string   `json:"setup"`
	AuthMethod string   `json:"authMethod"`
	Advantages []string `json:"advantages"`
}

// EKSHelmComparison returns comparison notes between GKE and EKS Helm usage
func EKSHelmComparison() map[string]string {
	return map[string]string{
		"gke_registry":     "Artifact Registry (OCI)",
		"eks_registry":     "ECR (OCI)",
		"gke_auth":         "Workload Identity + gcloud",
		"eks_auth":         "IRSA + aws ecr get-login-password",
		"gke_gcp_charts":   "Config Connector for GCP resources",
		"eks_aws_charts":   "AWS Controllers for Kubernetes (ACK)",
		"gke_service_mesh": "Anthos Service Mesh (managed Istio)",
		"eks_service_mesh": "App Mesh or self-managed Istio",
	}
}

// containsAny checks if the string contains any of the patterns
func containsAny(s string, patterns []string) bool {
	for _, p := range patterns {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}
