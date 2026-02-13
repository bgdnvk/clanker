package helm

import (
	"fmt"
	"strings"
)

// AKSACRURL builds an OCI URL for Azure Container Registry
func AKSACRURL(registryName, repo string) string {
	return fmt.Sprintf(AKSACRURLFormat, registryName, repo)
}

// AKSACRAuthHints returns authentication guidance for ACR
func AKSACRAuthHints() []string {
	return []string{
		"Attach ACR to AKS cluster (recommended, uses managed identity):",
		"  az aks update -n CLUSTER -g RESOURCE_GROUP --attach-acr ACR_NAME",
		"",
		"Login to ACR with Azure CLI:",
		"  az acr login --name ACR_NAME",
		"",
		"Login to ACR with Helm:",
		"  TOKEN=$(az acr login --name ACR_NAME --expose-token --query accessToken -o tsv)",
		"  helm registry login ACR_NAME.azurecr.io --username 00000000-0000-0000-0000-000000000000 --password $TOKEN",
		"",
		"For service principal authentication:",
		"  az acr login --name ACR_NAME --username SP_APP_ID --password SP_PASSWORD",
		"",
		"Managed Identity (for AKS):",
		"  - Attach ACR to AKS: az aks update -n CLUSTER -g RG --attach-acr ACR_NAME",
		"  - AKS nodes automatically get pull credentials",
		"  - Use Workload Identity for pod-level ACR access",
	}
}

// AKSRecommendedRepos returns AKS-specific recommended Helm repositories
func AKSRecommendedRepos() []RepoInfo {
	return []RepoInfo{
		{
			Name: "azure-marketplace",
			URL:  AKSRepoAzureMarketplace,
		},
		{
			Name: "microsoft",
			URL:  AKSRepoMicrosoft,
		},
		{
			Name: "application-gateway-kubernetes-ingress",
			URL:  "https://appgwingress.blob.core.windows.net/ingress-azure-helm-package/",
		},
		{
			Name: "aad-pod-identity",
			URL:  "https://raw.githubusercontent.com/Azure/aad-pod-identity/master/charts",
		},
	}
}

// AKSChartRecommendation represents a recommended chart for a use case
type AKSChartRecommendation struct {
	Name        string   `json:"name"`
	Chart       string   `json:"chart"`
	Repo        string   `json:"repo,omitempty"`
	Description string   `json:"description"`
	Notes       []string `json:"notes,omitempty"`
	Values      []string `json:"values,omitempty"`
}

// GetAKSChartRecommendations returns AKS-optimized chart suggestions for common use cases
func GetAKSChartRecommendations(useCase string) []AKSChartRecommendation {
	useCaseLower := strings.ToLower(useCase)

	// KEDA for event-driven autoscaling
	if containsAny(useCaseLower, []string{"autoscaling", "keda", "event-driven", "scale"}) {
		return []AKSChartRecommendation{
			{
				Name:        "KEDA",
				Chart:       "kedacore/keda",
				Description: "Event-driven autoscaling for Kubernetes",
				Notes: []string{
					"KEDA is an official AKS add-on: az aks update -n CLUSTER -g RG --enable-keda",
					"Supports Azure Queue Storage, Service Bus, Event Hubs scalers",
					"Integrates with Azure Monitor for custom metrics scaling",
					"Use Workload Identity for Azure service authentication",
				},
				Values: []string{
					"--set serviceAccount.annotations.azure\\.workload\\.identity/client-id=CLIENT_ID",
				},
			},
		}
	}

	// Secrets management with Azure Key Vault
	if containsAny(useCaseLower, []string{"secrets", "vault", "key vault", "secret"}) {
		return []AKSChartRecommendation{
			{
				Name:        "Secrets Store CSI Driver",
				Chart:       "csi-secrets-store/secrets-store-csi-driver",
				Description: "Integrate Azure Key Vault with Kubernetes secrets",
				Notes: []string{
					"AKS add-on available: az aks enable-addons -n CLUSTER -g RG --addons azure-keyvault-secrets-provider",
					"Mount Key Vault secrets as volumes or sync to K8s secrets",
					"Use Workload Identity or managed identity for Key Vault access",
					"Supports automatic secret rotation",
				},
				Values: []string{
					"--set syncSecret.enabled=true",
					"--set enableSecretRotation=true",
				},
			},
			{
				Name:        "Azure Key Vault Provider",
				Chart:       "csi-secrets-store-provider-azure/csi-secrets-store-provider-azure",
				Description: "Azure-specific provider for Secrets Store CSI Driver",
				Notes: []string{
					"Required alongside Secrets Store CSI Driver",
					"Supports secrets, keys, and certificates from Key Vault",
					"Configure SecretProviderClass CRD for Key Vault access",
				},
			},
		}
	}

	// Application Gateway Ingress Controller
	if containsAny(useCaseLower, []string{"ingress", "agic", "application gateway", "waf"}) {
		return []AKSChartRecommendation{
			{
				Name:        "Application Gateway Ingress Controller",
				Chart:       "application-gateway-kubernetes-ingress/ingress-azure",
				Repo:        "https://appgwingress.blob.core.windows.net/ingress-azure-helm-package/",
				Description: "Use Azure Application Gateway as Kubernetes ingress",
				Notes: []string{
					"AGIC is available as AKS add-on: az aks enable-addons -n CLUSTER -g RG --addons ingress-appgw --appgw-id APP_GW_ID",
					"Provides L7 load balancing with WAF protection",
					"Supports SSL termination and URL-based routing",
					"Use annotations for Application Gateway features",
				},
				Values: []string{
					"--set appgw.applicationGatewayID=/subscriptions/SUB/resourceGroups/RG/providers/Microsoft.Network/applicationGateways/APP_GW",
					"--set armAuth.type=workloadIdentity",
				},
			},
			{
				Name:        "cert-manager",
				Chart:       "jetstack/cert-manager",
				Description: "Certificate management for Kubernetes",
				Notes: []string{
					"Integrates with Azure Key Vault via External Issuer",
					"Supports Let's Encrypt with DNS-01 challenge on Azure DNS",
					"Use Workload Identity for Azure DNS authentication",
				},
				Values: []string{
					"--set installCRDs=true",
				},
			},
		}
	}

	// Service mesh
	if containsAny(useCaseLower, []string{"service mesh", "istio", "osm", "open service mesh"}) {
		return []AKSChartRecommendation{
			{
				Name:        "Open Service Mesh",
				Chart:       "N/A - Use az aks mesh enable",
				Description: "Microsoft's lightweight service mesh for AKS",
				Notes: []string{
					"Enable via: az aks mesh enable -n CLUSTER -g RG",
					"Lightweight alternative to Istio",
					"Supports mTLS, traffic policies, and observability",
					"Integrated with Azure Monitor for metrics",
				},
			},
			{
				Name:        "Istio",
				Chart:       "N/A - Use istioctl or AKS Istio add-on",
				Description: "Enterprise service mesh",
				Notes: []string{
					"AKS Istio add-on available in preview",
					"Enable via: az aks mesh enable -n CLUSTER -g RG --mesh istio",
					"Provides traffic management, security, and observability",
					"Consider OSM for simpler use cases",
				},
			},
		}
	}

	// GitOps
	if containsAny(useCaseLower, []string{"gitops", "flux", "argocd", "ci", "cd"}) {
		return []AKSChartRecommendation{
			{
				Name:        "Flux",
				Chart:       "N/A - Use az k8s-configuration flux create",
				Description: "GitOps for AKS with Azure Arc integration",
				Notes: []string{
					"Officially supported GitOps solution for AKS",
					"Enable via: az k8s-configuration flux create -n CLUSTER -g RG --name CONFIG_NAME --url REPO_URL",
					"Supports Helm releases, Kustomize, and plain manifests",
					"Integrates with Azure DevOps and GitHub",
				},
			},
			{
				Name:        "Argo CD",
				Chart:       "argo/argo-cd",
				Description: "Declarative GitOps for Kubernetes",
				Notes: []string{
					"Supports ACR as OCI chart source",
					"Use Workload Identity for Azure authentication",
					"Configure ApplicationSet for multi-cluster deployments",
				},
				Values: []string{
					"--set server.service.type=ClusterIP",
				},
			},
		}
	}

	// Monitoring
	if containsAny(useCaseLower, []string{"monitoring", "prometheus", "grafana", "metrics"}) {
		return []AKSChartRecommendation{
			{
				Name:        "Azure Monitor (Container Insights)",
				Chart:       "N/A - Use az aks enable-addons --addons monitoring",
				Description: "Azure-native monitoring for AKS",
				Notes: []string{
					"Enable via: az aks enable-addons -n CLUSTER -g RG --addons monitoring",
					"Integrated with Azure Monitor and Log Analytics",
					"Pre-built workbooks and dashboards in Azure Portal",
					"Consider for Azure-native observability stack",
				},
			},
			{
				Name:        "Prometheus Stack",
				Chart:       "prometheus-community/kube-prometheus-stack",
				Description: "Self-managed Prometheus and Grafana",
				Notes: []string{
					"Consider Azure Managed Prometheus as alternative",
					"Use Azure Managed Grafana with Prometheus data source",
					"Configure remote write to Azure Monitor workspace",
				},
				Values: []string{
					"--set prometheus.prometheusSpec.serviceMonitorSelectorNilUsesHelmValues=false",
				},
			},
		}
	}

	// Dapr
	if containsAny(useCaseLower, []string{"dapr", "microservices", "distributed"}) {
		return []AKSChartRecommendation{
			{
				Name:        "Dapr",
				Chart:       "dapr/dapr",
				Description: "Distributed Application Runtime for microservices",
				Notes: []string{
					"Dapr extension available for AKS: az k8s-extension create --extension-type Microsoft.Dapr",
					"Provides service invocation, state management, pub/sub",
					"Supports Azure services as building blocks (Cosmos DB, Service Bus, etc.)",
					"Use Workload Identity for Azure service authentication",
				},
				Values: []string{
					"--set global.mtls.enabled=true",
				},
			},
		}
	}

	// Database
	if containsAny(useCaseLower, []string{"database", "postgres", "mysql", "redis", "cosmos"}) {
		return []AKSChartRecommendation{
			{
				Name:        "CloudNativePG",
				Chart:       "cloudnative-pg/cloudnative-pg",
				Description: "Cloud-native PostgreSQL for Kubernetes",
				Notes: []string{
					"Consider Azure Database for PostgreSQL for managed service",
					"Use Azure Disk CSI for persistent storage",
					"Support backup to Azure Blob Storage",
				},
			},
			{
				Name:        "Redis",
				Chart:       "bitnami/redis",
				Description: "Redis in-memory data store",
				Notes: []string{
					"Consider Azure Cache for Redis for managed service",
					"Use managed-csi-premium storage class for performance",
					"Configure persistence with Azure Disk",
				},
				Values: []string{
					"--set global.storageClass=managed-csi-premium",
				},
			},
		}
	}

	// Default recommendations
	return []AKSChartRecommendation{
		{
			Name:        "nginx-ingress",
			Chart:       "ingress-nginx/ingress-nginx",
			Description: "Popular ingress controller (consider AGIC for Azure integration)",
			Notes: []string{
				"AGIC recommended for Azure Application Gateway features",
				"nginx-ingress useful for portability and advanced config",
				"Web App Routing add-on available for simple ingress",
			},
		},
	}
}

// AKSHelmNotes returns important notes about using Helm with AKS
func AKSHelmNotes() []string {
	return []string{
		"Azure Container Registry (ACR) supports OCI Helm charts",
		"Attach ACR to AKS for automatic pull authentication",
		"Use Workload Identity for secure registry and Azure service access",
		"Consider AKS add-ons for KEDA, AGIC, Key Vault integration",
		"Flux is the officially supported GitOps solution for AKS",
		"Azure Managed Prometheus and Grafana available for monitoring",
		"Dapr extension available for microservices development",
	}
}

// GetAKSACRLoginCommand returns the az cli command to login to ACR
func GetAKSACRLoginCommand(registryName string) string {
	return fmt.Sprintf("az acr login --name %s", registryName)
}

// GetAKSHelmLoginCommand returns the command to login to ACR with Helm
func GetAKSHelmLoginCommand(registryName string) string {
	return fmt.Sprintf(
		`TOKEN=$(az acr login --name %s --expose-token --query accessToken -o tsv) && helm registry login %s.azurecr.io --username 00000000-0000-0000-0000-000000000000 --password $TOKEN`,
		registryName, registryName)
}

// IsAKSACRURL checks if a URL is an Azure Container Registry URL
func IsAKSACRURL(url string) bool {
	return strings.Contains(url, ".azurecr.io") && strings.HasPrefix(url, "oci://")
}

// ParseAKSACRURL parses an ACR URL into components
func ParseAKSACRURL(url string) (registryName, repo string, err error) {
	if !IsAKSACRURL(url) {
		return "", "", fmt.Errorf("not a valid ACR URL: %s", url)
	}

	// Remove oci:// prefix
	trimmed := strings.TrimPrefix(url, "oci://")

	// Split by /
	parts := strings.SplitN(trimmed, "/", 2)
	if len(parts) < 2 {
		return "", "", fmt.Errorf("invalid ACR URL format: %s", url)
	}

	// Extract registry name from hostname (e.g., myregistry.azurecr.io)
	host := parts[0]
	registryName = strings.TrimSuffix(host, ".azurecr.io")
	repo = parts[1]

	return registryName, repo, nil
}

// AKSChartSourceRecommendation returns recommendations for chart sources on AKS
func AKSChartSourceRecommendation(useCase string) AKSChartSourceRec {
	useCaseLower := strings.ToLower(useCase)

	// Enterprise/production
	if containsAny(useCaseLower, []string{"enterprise", "production", "secure", "compliance"}) {
		return AKSChartSourceRec{
			Source:     "Azure Container Registry (ACR)",
			Reason:     "Private OCI registry with Azure AD integration and security scanning",
			Setup:      "Create ACR, attach to AKS cluster, push charts as OCI artifacts",
			AuthMethod: "Managed Identity or Workload Identity",
			Advantages: []string{
				"Azure AD and RBAC integration",
				"Microsoft Defender for Containers scanning",
				"Geo-replication for global deployments",
				"Content trust with signed images",
			},
		}
	}

	// Internal/private
	if containsAny(useCaseLower, []string{"internal", "private", "organization"}) {
		return AKSChartSourceRec{
			Source:     "Azure Container Registry (ACR)",
			Reason:     "Private registry for organization-internal charts",
			Setup:      "Create ACR with appropriate RBAC, attach to AKS",
			AuthMethod: "Managed Identity (attach-acr)",
			Advantages: []string{
				"Private by default",
				"Integration with Azure DevOps and GitHub Actions",
				"Supports OCI and traditional Helm repos",
				"Network integration with Private Link",
			},
		}
	}

	// Default for development
	return AKSChartSourceRec{
		Source:     "Public Helm repositories",
		Reason:     "Easy access to community charts for development",
		Setup:      "Use helm repo add for public repositories",
		AuthMethod: "None required",
		Advantages: []string{
			"Quick setup",
			"Access to popular charts (bitnami, grafana, etc.)",
			"No additional Azure costs",
		},
	}
}

// AKSChartSourceRec represents a chart source recommendation
type AKSChartSourceRec struct {
	Source     string   `json:"source"`
	Reason     string   `json:"reason"`
	Setup      string   `json:"setup"`
	AuthMethod string   `json:"authMethod"`
	Advantages []string `json:"advantages"`
}

// GKEHelmComparisonWithAKS returns comparison notes between AKS and GKE Helm usage
func GKEHelmComparisonWithAKS() map[string]string {
	return map[string]string{
		"aks_registry":     "Azure Container Registry (ACR)",
		"gke_registry":     "Artifact Registry",
		"eks_registry":     "Elastic Container Registry (ECR)",
		"aks_auth":         "Managed Identity / Workload Identity + az acr login",
		"gke_auth":         "Workload Identity + gcloud auth",
		"eks_auth":         "IRSA + aws ecr get-login-password",
		"aks_azure_charts": "Key Vault CSI, AGIC, KEDA, Dapr",
		"gke_gcp_charts":   "Config Connector, External Secrets",
		"eks_aws_charts":   "AWS Controllers for Kubernetes (ACK)",
		"aks_service_mesh": "Open Service Mesh, Istio add-on",
		"gke_service_mesh": "Anthos Service Mesh",
		"eks_service_mesh": "App Mesh",
		"aks_gitops":       "Flux (GitOps extension)",
		"gke_gitops":       "Config Sync",
		"eks_gitops":       "Flux or Argo CD",
	}
}
