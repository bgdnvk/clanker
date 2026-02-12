package networking

import (
	"strings"
	"testing"
)

func TestGKEIngressAnnotations(t *testing.T) {
	tests := []struct {
		name        string
		opts        GKEIngressOptions
		wantKeys    []string
		wantValues  map[string]string
		notWantKeys []string
	}{
		{
			name: "External with static IP",
			opts: GKEIngressOptions{
				StaticIPName: "my-static-ip",
				Internal:     false,
			},
			wantKeys:   []string{GKEAnnotationStaticIP},
			wantValues: map[string]string{GKEAnnotationStaticIP: "my-static-ip"},
		},
		{
			name: "Internal with regional static IP",
			opts: GKEIngressOptions{
				StaticIPName: "my-regional-ip",
				Internal:     true,
			},
			wantKeys:   []string{GKEAnnotationStaticIPRegional},
			wantValues: map[string]string{GKEAnnotationStaticIPRegional: "my-regional-ip"},
		},
		{
			name: "With managed certificate",
			opts: GKEIngressOptions{
				ManagedCert: "my-cert",
			},
			wantKeys:   []string{"networking.gke.io/managed-certificates"},
			wantValues: map[string]string{"networking.gke.io/managed-certificates": "my-cert"},
		},
		{
			name: "Disable HTTP",
			opts: GKEIngressOptions{
				AllowHTTP: false,
			},
			wantKeys:   []string{GKEAnnotationAllowHTTP},
			wantValues: map[string]string{GKEAnnotationAllowHTTP: "false"},
		},
		{
			name: "Allow HTTP",
			opts: GKEIngressOptions{
				AllowHTTP: true,
			},
			notWantKeys: []string{GKEAnnotationAllowHTTP},
		},
		{
			name: "With backend config",
			opts: GKEIngressOptions{
				BackendConfig: "my-backend-config",
			},
			wantKeys: []string{GKEAnnotationBackendConfig},
		},
		{
			name: "With pre-shared cert",
			opts: GKEIngressOptions{
				PreSharedCert: "my-pre-shared-cert",
			},
			wantKeys:   []string{GKEAnnotationPreSharedCert},
			wantValues: map[string]string{GKEAnnotationPreSharedCert: "my-pre-shared-cert"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			annotations := GKEIngressAnnotations(tt.opts)

			for _, key := range tt.wantKeys {
				if _, ok := annotations[key]; !ok {
					t.Errorf("expected annotation %s to be present", key)
				}
			}

			for key, wantValue := range tt.wantValues {
				if gotValue, ok := annotations[key]; !ok {
					t.Errorf("expected annotation %s to be present", key)
				} else if gotValue != wantValue {
					t.Errorf("annotation %s = %s, want %s", key, gotValue, wantValue)
				}
			}

			for _, key := range tt.notWantKeys {
				if _, ok := annotations[key]; ok {
					t.Errorf("did not expect annotation %s to be present", key)
				}
			}
		})
	}
}

func TestGKEIngressClassName(t *testing.T) {
	tests := []struct {
		name     string
		internal bool
		want     string
	}{
		{
			name:     "External ingress",
			internal: false,
			want:     GKEIngressClass,
		},
		{
			name:     "Internal ingress",
			internal: true,
			want:     GKEIngressClassInternal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GKEIngressClassName(tt.internal)
			if got != tt.want {
				t.Errorf("GKEIngressClassName(%v) = %s, want %s", tt.internal, got, tt.want)
			}
		})
	}
}

func TestGKELoadBalancerAnnotations(t *testing.T) {
	tests := []struct {
		name       string
		opts       GKELoadBalancerOptions
		wantKeys   []string
		wantValues map[string]string
	}{
		{
			name: "Internal load balancer",
			opts: GKELoadBalancerOptions{
				Internal:   true,
				Subnetwork: "my-subnet",
			},
			wantKeys: []string{GKELBAnnotationType, GKELBAnnotationSubnetwork},
			wantValues: map[string]string{
				GKELBAnnotationType:       GKELBTypeInternal,
				GKELBAnnotationSubnetwork: "my-subnet",
			},
		},
		{
			name: "External with premium tier",
			opts: GKELoadBalancerOptions{
				Internal:    false,
				NetworkTier: GKENetworkTierPremium,
			},
			wantKeys:   []string{GKELBAnnotationNetworkTier},
			wantValues: map[string]string{GKELBAnnotationNetworkTier: GKENetworkTierPremium},
		},
		{
			name: "With NEG enabled",
			opts: GKELoadBalancerOptions{
				EnableNEG: true,
			},
			wantKeys: []string{GKEAnnotationNEG},
		},
		{
			name: "With NEG specific ports",
			opts: GKELoadBalancerOptions{
				EnableNEG: true,
				NEGPorts:  []int{80, 443},
			},
			wantKeys: []string{GKEAnnotationNEG},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			annotations := GKELoadBalancerAnnotations(tt.opts)

			for _, key := range tt.wantKeys {
				if _, ok := annotations[key]; !ok {
					t.Errorf("expected annotation %s to be present", key)
				}
			}

			for key, wantValue := range tt.wantValues {
				if gotValue, ok := annotations[key]; !ok {
					t.Errorf("expected annotation %s to be present", key)
				} else if gotValue != wantValue {
					t.Errorf("annotation %s = %s, want %s", key, gotValue, wantValue)
				}
			}
		})
	}
}

func TestGKENEGAnnotation(t *testing.T) {
	tests := []struct {
		name  string
		ports []int
		want  string
	}{
		{
			name:  "No ports - ingress mode",
			ports: nil,
			want:  GKENEGTypeIngress,
		},
		{
			name:  "Empty ports - ingress mode",
			ports: []int{},
			want:  GKENEGTypeIngress,
		},
		{
			name:  "Single port",
			ports: []int{80},
			want:  `{"exposed_ports": {"80": {}}}`,
		},
		{
			name:  "Multiple ports",
			ports: []int{80, 443},
			want:  `{"exposed_ports": {"80": {}, "443": {}}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GKENEGAnnotation(tt.ports...)
			if got != tt.want {
				t.Errorf("GKENEGAnnotation(%v) = %s, want %s", tt.ports, got, tt.want)
			}
		})
	}
}

func TestGKEBackendConfigAnnotation(t *testing.T) {
	got := GKEBackendConfigAnnotation("my-config")
	want := `{"default": "my-config"}`

	if got != want {
		t.Errorf("GKEBackendConfigAnnotation() = %s, want %s", got, want)
	}
}

func TestGKEBackendConfigAnnotationWithPorts(t *testing.T) {
	tests := []struct {
		name    string
		configs map[int]string
		want    string
	}{
		{
			name:    "Empty configs",
			configs: map[int]string{},
			want:    "",
		},
		{
			name:    "Single port config",
			configs: map[int]string{80: "http-config"},
			want:    `{"ports": {"80": "http-config"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GKEBackendConfigAnnotationWithPorts(tt.configs)
			if got != tt.want {
				t.Errorf("GKEBackendConfigAnnotationWithPorts() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestGKENetworkingRecommendation(t *testing.T) {
	tests := []struct {
		name           string
		useCase        string
		wantService    string
		wantIngress    string
		wantNEG        bool
	}{
		{
			name:           "Internal service",
			useCase:        "internal backend api",
			wantService:    string(ServiceTypeClusterIP),
			wantIngress:    GKEIngressClassInternal,
			wantNEG:        true,
		},
		{
			name:           "Public web service",
			useCase:        "public web application",
			wantService:    string(ServiceTypeLoadBalancer),
			wantIngress:    GKEIngressClass,
			wantNEG:        true,
		},
		{
			name:           "Microservice",
			useCase:        "microservice in service mesh",
			wantService:    string(ServiceTypeClusterIP),
			wantIngress:    "",
			wantNEG:        false,
		},
		{
			name:           "WebSocket service",
			useCase:        "websocket streaming",
			wantService:    string(ServiceTypeLoadBalancer),
			wantIngress:    GKEIngressClass,
			wantNEG:        true,
		},
		{
			name:           "Default case",
			useCase:        "general application",
			wantService:    string(ServiceTypeClusterIP),
			wantIngress:    GKEIngressClass,
			wantNEG:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := GKENetworkingRecommendation(tt.useCase)

			if rec.ServiceType != tt.wantService {
				t.Errorf("ServiceType = %s, want %s", rec.ServiceType, tt.wantService)
			}

			if rec.IngressClass != tt.wantIngress {
				t.Errorf("IngressClass = %s, want %s", rec.IngressClass, tt.wantIngress)
			}

			if rec.UseNEG != tt.wantNEG {
				t.Errorf("UseNEG = %v, want %v", rec.UseNEG, tt.wantNEG)
			}

			if rec.Reason == "" {
				t.Error("Reason should not be empty")
			}

			if len(rec.Considerations) == 0 {
				t.Error("Considerations should not be empty")
			}
		})
	}
}

func TestGKEManagedCertificateManifest(t *testing.T) {
	manifest := GKEManagedCertificateManifest("my-cert", "default", []string{"example.com", "www.example.com"})

	expectedStrings := []string{
		"apiVersion: networking.gke.io/v1",
		"kind: ManagedCertificate",
		"name: my-cert",
		"namespace: default",
		"domains:",
		"- example.com",
		"- www.example.com",
	}

	for _, expected := range expectedStrings {
		if !strings.Contains(manifest, expected) {
			t.Errorf("manifest missing expected string: %s\nGot:\n%s", expected, manifest)
		}
	}
}

func TestGKEBackendConfigManifest(t *testing.T) {
	opts := GKEBackendConfigOptions{
		TimeoutSec:                   30,
		ConnectionDrainingTimeoutSec: 300,
		SessionAffinity:              "GENERATED_COOKIE",
		SessionAffinityTTL:           3600,
		EnableCDN:                    true,
		CDNCachePolicy:               "CACHE_ALL_STATIC",
		HealthCheckPath:              "/healthz",
		HealthCheckPort:              8080,
		SecurityPolicy:               "my-armor-policy",
	}

	manifest := GKEBackendConfigManifest("my-backend-config", "default", opts)

	expectedStrings := []string{
		"apiVersion: cloud.google.com/v1",
		"kind: BackendConfig",
		"name: my-backend-config",
		"namespace: default",
		"timeoutSec: 30",
		"connectionDraining:",
		"drainingTimeoutSec: 300",
		"sessionAffinity:",
		"affinityType: \"GENERATED_COOKIE\"",
		"affinityCookieTtlSec: 3600",
		"cdn:",
		"enabled: true",
		"CACHE_ALL_STATIC",
		"healthCheck:",
		"requestPath: /healthz",
		"port: 8080",
		"securityPolicy:",
		"my-armor-policy",
	}

	for _, expected := range expectedStrings {
		if !strings.Contains(manifest, expected) {
			t.Errorf("manifest missing expected string: %s\nGot:\n%s", expected, manifest)
		}
	}
}

func TestIsGKEIngress(t *testing.T) {
	tests := []struct {
		ingressClass string
		want         bool
	}{
		{GKEIngressClass, true},
		{GKEIngressClassInternal, true},
		{"nginx", false},
		{"alb", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.ingressClass, func(t *testing.T) {
			got := IsGKEIngress(tt.ingressClass)
			if got != tt.want {
				t.Errorf("IsGKEIngress(%q) = %v, want %v", tt.ingressClass, got, tt.want)
			}
		})
	}
}

func TestIsGKEAnnotation(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		{"cloud.google.com/neg", true},
		{"cloud.google.com/load-balancer-type", true},
		{"networking.gke.io/managed-certificates", true},
		{"ingress.gcp.kubernetes.io/pre-shared-cert", true},
		{"kubernetes.io/ingress.global-static-ip-name", true},
		{"service.beta.kubernetes.io/aws-load-balancer-type", false},
		{"nginx.ingress.kubernetes.io/rewrite-target", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got := IsGKEAnnotation(tt.key)
			if got != tt.want {
				t.Errorf("IsGKEAnnotation(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

func TestGKENetworkingNotes(t *testing.T) {
	notes := GKENetworkingNotes()

	if len(notes) == 0 {
		t.Error("expected at least one networking note")
	}

	notesText := strings.Join(notes, " ")

	expectedTopics := []string{
		"NEG",
		"ingress",
		"ManagedCertificate",
		"BackendConfig",
		"Premium",
		"Standard",
	}

	for _, topic := range expectedTopics {
		if !strings.Contains(notesText, topic) {
			t.Errorf("networking notes should mention %s", topic)
		}
	}
}

func TestGKENetworkingConstants(t *testing.T) {
	// Verify GKE constants are defined correctly
	if GKEIngressClass != "gce" {
		t.Errorf("GKEIngressClass = %s, want gce", GKEIngressClass)
	}

	if GKEIngressClassInternal != "gce-internal" {
		t.Errorf("GKEIngressClassInternal = %s, want gce-internal", GKEIngressClassInternal)
	}

	if GKEAnnotationNEG != "cloud.google.com/neg" {
		t.Errorf("GKEAnnotationNEG = %s, want cloud.google.com/neg", GKEAnnotationNEG)
	}

	if GKELBTypeInternal != "Internal" {
		t.Errorf("GKELBTypeInternal = %s, want Internal", GKELBTypeInternal)
	}

	if GKENetworkTierPremium != "Premium" {
		t.Errorf("GKENetworkTierPremium = %s, want Premium", GKENetworkTierPremium)
	}
}

func TestEKSNetworkingConstants(t *testing.T) {
	// Verify EKS constants for comparison
	if EKSAnnotationLBType != "service.beta.kubernetes.io/aws-load-balancer-type" {
		t.Errorf("EKSAnnotationLBType = %s, want service.beta.kubernetes.io/aws-load-balancer-type", EKSAnnotationLBType)
	}
}
