package k8s

import "testing"

func TestDetectCloudProviderFromContext(t *testing.T) {
	tests := []struct {
		name        string
		contextName string
		want        CloudProvider
	}{
		{
			name:        "GKE context pattern",
			contextName: "gke_my-project_us-central1_my-cluster",
			want:        CloudProviderGCP,
		},
		{
			name:        "GKE context pattern with underscores in cluster",
			contextName: "gke_my-project_us-central1-a_my-cluster-name",
			want:        CloudProviderGCP,
		},
		{
			name:        "EKS ARN context pattern",
			contextName: "arn:aws:eks:us-east-1:123456789012:cluster/my-cluster",
			want:        CloudProviderAWS,
		},
		{
			name:        "AKS context pattern",
			contextName: "aks-my-cluster",
			want:        CloudProviderAzure,
		},
		{
			name:        "Azure context pattern",
			contextName: "azure-my-cluster",
			want:        CloudProviderAzure,
		},
		{
			name:        "Generic EKS context",
			contextName: "my-eks-cluster",
			want:        CloudProviderAWS,
		},
		{
			name:        "Generic GKE context",
			contextName: "my-gke-cluster",
			want:        CloudProviderGCP,
		},
		{
			name:        "Generic AWS context",
			contextName: "aws-prod-cluster",
			want:        CloudProviderAWS,
		},
		{
			name:        "Generic GCP context",
			contextName: "gcp-prod-cluster",
			want:        CloudProviderGCP,
		},
		{
			name:        "Unknown context",
			contextName: "minikube",
			want:        CloudProviderUnknown,
		},
		{
			name:        "Empty context",
			contextName: "",
			want:        CloudProviderUnknown,
		},
		{
			name:        "Kind cluster",
			contextName: "kind-my-cluster",
			want:        CloudProviderUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectCloudProviderFromContext(tt.contextName)
			if got != tt.want {
				t.Errorf("DetectCloudProviderFromContext(%q) = %v, want %v", tt.contextName, got, tt.want)
			}
		})
	}
}

func TestDetectCloudProviderFromClusterName(t *testing.T) {
	tests := []struct {
		name        string
		clusterName string
		want        CloudProvider
	}{
		{
			name:        "GKE in cluster name",
			clusterName: "prod-gke-cluster",
			want:        CloudProviderGCP,
		},
		{
			name:        "EKS in cluster name",
			clusterName: "prod-eks-cluster",
			want:        CloudProviderAWS,
		},
		{
			name:        "AKS in cluster name",
			clusterName: "prod-aks-cluster",
			want:        CloudProviderAzure,
		},
		{
			name:        "Generic cluster name",
			clusterName: "production-cluster",
			want:        CloudProviderUnknown,
		},
		{
			name:        "Empty cluster name",
			clusterName: "",
			want:        CloudProviderUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectCloudProviderFromClusterName(tt.clusterName)
			if got != tt.want {
				t.Errorf("DetectCloudProviderFromClusterName(%q) = %v, want %v", tt.clusterName, got, tt.want)
			}
		})
	}
}

func TestDetectCloudProviderFromQuery(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  CloudProvider
	}{
		{
			name:  "GKE mention",
			query: "show pods on my GKE cluster",
			want:  CloudProviderGCP,
		},
		{
			name:  "EKS mention",
			query: "list deployments on EKS",
			want:  CloudProviderAWS,
		},
		{
			name:  "Artifact Registry mention",
			query: "install chart from artifact registry",
			want:  CloudProviderGCP,
		},
		{
			name:  "ECR mention",
			query: "pull image from ECR",
			want:  CloudProviderAWS,
		},
		{
			name:  "GCP project mention",
			query: "check cloud.google.com annotations",
			want:  CloudProviderGCP,
		},
		{
			name:  "AWS account mention",
			query: "check eks.amazonaws.com annotations",
			want:  CloudProviderAWS,
		},
		{
			name:  "Preemptible mention",
			query: "schedule on preemptible nodes",
			want:  CloudProviderGCP,
		},
		{
			name:  "Spot instance mention",
			query: "use spot instance for workers",
			want:  CloudProviderAWS,
		},
		{
			name:  "PD storage mention",
			query: "create pd-ssd storage class",
			want:  CloudProviderGCP,
		},
		{
			name:  "EBS mention",
			query: "create EBS volume",
			want:  CloudProviderAWS,
		},
		{
			name:  "Workload Identity mention",
			query: "configure workload identity",
			want:  CloudProviderGCP,
		},
		{
			name:  "IRSA mention",
			query: "setup IRSA for pod",
			want:  CloudProviderAWS,
		},
		{
			name:  "Generic query",
			query: "show all pods in default namespace",
			want:  CloudProviderUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectCloudProviderFromQuery(tt.query)
			if got != tt.want {
				t.Errorf("DetectCloudProviderFromQuery(%q) = %v, want %v", tt.query, got, tt.want)
			}
		})
	}
}

func TestDetectCloudProvider(t *testing.T) {
	tests := []struct {
		name        string
		contextName string
		clusterName string
		want        CloudProvider
	}{
		{
			name:        "Context takes precedence",
			contextName: "gke_project_region_cluster",
			clusterName: "eks-cluster",
			want:        CloudProviderGCP,
		},
		{
			name:        "Cluster name fallback",
			contextName: "my-context",
			clusterName: "my-gke-cluster",
			want:        CloudProviderGCP,
		},
		{
			name:        "Both unknown",
			contextName: "minikube",
			clusterName: "local-cluster",
			want:        CloudProviderUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectCloudProvider(tt.contextName, tt.clusterName)
			if got != tt.want {
				t.Errorf("DetectCloudProvider(%q, %q) = %v, want %v", tt.contextName, tt.clusterName, got, tt.want)
			}
		})
	}
}

func TestParseGKEContextInfo(t *testing.T) {
	tests := []struct {
		name        string
		contextName string
		wantProject string
		wantRegion  string
		wantCluster string
		wantOk      bool
	}{
		{
			name:        "Valid GKE context",
			contextName: "gke_my-project_us-central1_my-cluster",
			wantProject: "my-project",
			wantRegion:  "us-central1",
			wantCluster: "my-cluster",
			wantOk:      true,
		},
		{
			name:        "GKE context with zone",
			contextName: "gke_my-project_us-central1-a_my-cluster",
			wantProject: "my-project",
			wantRegion:  "us-central1-a",
			wantCluster: "my-cluster",
			wantOk:      true,
		},
		{
			name:        "GKE context with underscores in cluster",
			contextName: "gke_my-project_us-central1_my_cluster_name",
			wantProject: "my-project",
			wantRegion:  "us-central1",
			wantCluster: "my_cluster_name",
			wantOk:      true,
		},
		{
			name:        "Not a GKE context",
			contextName: "arn:aws:eks:us-east-1:123456789012:cluster/my-cluster",
			wantProject: "",
			wantRegion:  "",
			wantCluster: "",
			wantOk:      false,
		},
		{
			name:        "Invalid GKE context format",
			contextName: "gke_project",
			wantProject: "",
			wantRegion:  "",
			wantCluster: "",
			wantOk:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotProject, gotRegion, gotCluster, gotOk := ParseGKEContextInfo(tt.contextName)
			if gotOk != tt.wantOk {
				t.Errorf("ParseGKEContextInfo(%q) ok = %v, want %v", tt.contextName, gotOk, tt.wantOk)
			}
			if gotOk && tt.wantOk {
				if gotProject != tt.wantProject {
					t.Errorf("ParseGKEContextInfo(%q) project = %v, want %v", tt.contextName, gotProject, tt.wantProject)
				}
				if gotRegion != tt.wantRegion {
					t.Errorf("ParseGKEContextInfo(%q) region = %v, want %v", tt.contextName, gotRegion, tt.wantRegion)
				}
				if gotCluster != tt.wantCluster {
					t.Errorf("ParseGKEContextInfo(%q) cluster = %v, want %v", tt.contextName, gotCluster, tt.wantCluster)
				}
			}
		})
	}
}

func TestParseEKSContextInfo(t *testing.T) {
	tests := []struct {
		name        string
		contextName string
		wantRegion  string
		wantAccount string
		wantCluster string
		wantOk      bool
	}{
		{
			name:        "Valid EKS ARN context",
			contextName: "arn:aws:eks:us-east-1:123456789012:cluster/my-cluster",
			wantRegion:  "us-east-1",
			wantAccount: "123456789012",
			wantCluster: "my-cluster",
			wantOk:      true,
		},
		{
			name:        "EKS ARN with different region",
			contextName: "arn:aws:eks:eu-west-1:987654321098:cluster/prod-cluster",
			wantRegion:  "eu-west-1",
			wantAccount: "987654321098",
			wantCluster: "prod-cluster",
			wantOk:      true,
		},
		{
			name:        "Not an EKS ARN",
			contextName: "gke_my-project_us-central1_my-cluster",
			wantRegion:  "",
			wantAccount: "",
			wantCluster: "",
			wantOk:      false,
		},
		{
			name:        "Invalid ARN format",
			contextName: "arn:aws:eks",
			wantRegion:  "",
			wantAccount: "",
			wantCluster: "",
			wantOk:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotRegion, gotAccount, gotCluster, gotOk := ParseEKSContextInfo(tt.contextName)
			if gotOk != tt.wantOk {
				t.Errorf("ParseEKSContextInfo(%q) ok = %v, want %v", tt.contextName, gotOk, tt.wantOk)
			}
			if gotOk && tt.wantOk {
				if gotRegion != tt.wantRegion {
					t.Errorf("ParseEKSContextInfo(%q) region = %v, want %v", tt.contextName, gotRegion, tt.wantRegion)
				}
				if gotAccount != tt.wantAccount {
					t.Errorf("ParseEKSContextInfo(%q) account = %v, want %v", tt.contextName, gotAccount, tt.wantAccount)
				}
				if gotCluster != tt.wantCluster {
					t.Errorf("ParseEKSContextInfo(%q) cluster = %v, want %v", tt.contextName, gotCluster, tt.wantCluster)
				}
			}
		})
	}
}

func TestCloudProviderConstants(t *testing.T) {
	// Verify the cloud provider constants are defined correctly
	if CloudProviderUnknown != "" {
		t.Errorf("CloudProviderUnknown should be empty string, got %q", CloudProviderUnknown)
	}

	if CloudProviderAWS != "aws" {
		t.Errorf("CloudProviderAWS should be 'aws', got %q", CloudProviderAWS)
	}

	if CloudProviderGCP != "gcp" {
		t.Errorf("CloudProviderGCP should be 'gcp', got %q", CloudProviderGCP)
	}

	if CloudProviderAzure != "azure" {
		t.Errorf("CloudProviderAzure should be 'azure', got %q", CloudProviderAzure)
	}
}
