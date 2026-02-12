package storage

import (
	"strings"
	"testing"
)

func TestGKEStorageClasses(t *testing.T) {
	classes := GKEStorageClasses()

	if len(classes) == 0 {
		t.Error("expected at least one storage class")
	}

	// Find the default storage class
	foundDefault := false
	for _, sc := range classes {
		if sc.IsDefault {
			foundDefault = true
			if sc.Name != "standard" {
				t.Errorf("expected default storage class to be 'standard', got %s", sc.Name)
			}
		}
	}

	if !foundDefault {
		t.Error("expected to find a default storage class")
	}

	// Verify all classes have required fields
	for _, sc := range classes {
		if sc.Name == "" {
			t.Error("storage class name should not be empty")
		}
		if sc.DiskType == "" {
			t.Error("storage class disk type should not be empty")
		}
		if sc.ReclaimPolicy == "" {
			t.Error("storage class reclaim policy should not be empty")
		}
		if sc.VolumeBindingMode == "" {
			t.Error("storage class volume binding mode should not be empty")
		}
	}
}

func TestGKERegionalStorageClasses(t *testing.T) {
	classes := GKERegionalStorageClasses()

	if len(classes) == 0 {
		t.Error("expected at least one regional storage class")
	}

	for _, sc := range classes {
		if sc.ReplicationType != "regional-pd" {
			t.Errorf("expected regional storage class %s to have replication-type regional-pd, got %s",
				sc.Name, sc.ReplicationType)
		}
	}
}

func TestGKEStorageClassManifest(t *testing.T) {
	sc := GKEStorageClass{
		Name:                 "test-class",
		DiskType:             GKEStorageTypePDSSD,
		ReclaimPolicy:        string(ReclaimPolicyDelete),
		VolumeBindingMode:    string(VolumeBindingWaitForFirstConsumer),
		AllowVolumeExpansion: true,
		FSType:               "ext4",
		IsDefault:            false,
		ReplicationType:      "none",
	}

	manifest := GKEStorageClassManifest(sc)

	expectedStrings := []string{
		"apiVersion: storage.k8s.io/v1",
		"kind: StorageClass",
		"name: test-class",
		"provisioner: pd.csi.storage.gke.io",
		"type: pd-ssd",
		"reclaimPolicy: Delete",
		"volumeBindingMode: WaitForFirstConsumer",
		"allowVolumeExpansion: true",
	}

	for _, expected := range expectedStrings {
		if !strings.Contains(manifest, expected) {
			t.Errorf("manifest missing expected string: %s\nGot:\n%s", expected, manifest)
		}
	}
}

func TestGKEStorageClassManifestDefault(t *testing.T) {
	sc := GKEStorageClass{
		Name:                 "default-class",
		DiskType:             GKEStorageTypePDStandard,
		ReclaimPolicy:        string(ReclaimPolicyDelete),
		VolumeBindingMode:    string(VolumeBindingImmediate),
		AllowVolumeExpansion: true,
		IsDefault:            true,
	}

	manifest := GKEStorageClassManifest(sc)

	if !strings.Contains(manifest, "storageclass.kubernetes.io/is-default-class: \"true\"") {
		t.Error("default storage class manifest should contain is-default-class annotation")
	}
}

func TestGKEStorageClassManifestRegionalPD(t *testing.T) {
	sc := GKEStorageClass{
		Name:                 "regional-class",
		DiskType:             GKEStorageTypePDSSD,
		ReclaimPolicy:        string(ReclaimPolicyRetain),
		VolumeBindingMode:    string(VolumeBindingWaitForFirstConsumer),
		AllowVolumeExpansion: true,
		ReplicationType:      "regional-pd",
	}

	manifest := GKEStorageClassManifest(sc)

	if !strings.Contains(manifest, "replication-type: regional-pd") {
		t.Error("regional storage class manifest should contain replication-type parameter")
	}
}

func TestGetGKEStorageRecommendation(t *testing.T) {
	tests := []struct {
		name         string
		useCase      string
		wantClass    string
		wantDiskType string
	}{
		{
			name:         "Database workload",
			useCase:      "postgresql database",
			wantClass:    "premium-rwo",
			wantDiskType: GKEStorageTypePDSSD,
		},
		{
			name:         "MySQL workload",
			useCase:      "mysql server",
			wantClass:    "premium-rwo",
			wantDiskType: GKEStorageTypePDSSD,
		},
		{
			name:         "High availability",
			useCase:      "high availability deployment",
			wantClass:    "premium-rwx",
			wantDiskType: GKEStorageTypePDSSD,
		},
		{
			name:         "Regional HA",
			useCase:      "regional multi-zone deployment",
			wantClass:    "premium-rwx",
			wantDiskType: GKEStorageTypePDSSD,
		},
		{
			name:         "Logging workload",
			useCase:      "elasticsearch logging",
			wantClass:    "standard-rwo",
			wantDiskType: GKEStorageTypePDStandard,
		},
		{
			name:         "Shared storage",
			useCase:      "shared file storage across pods",
			wantClass:    "filestore-standard",
			wantDiskType: "filestore",
		},
		{
			name:         "NFS requirement",
			useCase:      "need nfs volume",
			wantClass:    "filestore-standard",
			wantDiskType: "filestore",
		},
		{
			name:         "Build workload",
			useCase:      "CI/CD pipeline cache",
			wantClass:    "balanced-rwo",
			wantDiskType: GKEStorageTypePDBalanced,
		},
		{
			name:         "Default workload",
			useCase:      "general application",
			wantClass:    "standard-rwo",
			wantDiskType: GKEStorageTypePDStandard,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := GetGKEStorageRecommendation(tt.useCase)

			if rec.StorageClass != tt.wantClass {
				t.Errorf("GetGKEStorageRecommendation(%q) StorageClass = %s, want %s",
					tt.useCase, rec.StorageClass, tt.wantClass)
			}

			if rec.DiskType != tt.wantDiskType {
				t.Errorf("GetGKEStorageRecommendation(%q) DiskType = %s, want %s",
					tt.useCase, rec.DiskType, tt.wantDiskType)
			}

			if rec.Reason == "" {
				t.Errorf("GetGKEStorageRecommendation(%q) should have a reason", tt.useCase)
			}

			if len(rec.Considerations) == 0 {
				t.Errorf("GetGKEStorageRecommendation(%q) should have considerations", tt.useCase)
			}
		})
	}
}

func TestGKEFilestoreStorageClass(t *testing.T) {
	fc := GKEFilestoreStorageClass(GKEFilestoreTierStandard, "default")

	if fc.Name != "filestore-standard" {
		t.Errorf("expected name 'filestore-standard', got %s", fc.Name)
	}

	if fc.Tier != GKEFilestoreTierStandard {
		t.Errorf("expected tier '%s', got %s", GKEFilestoreTierStandard, fc.Tier)
	}

	if fc.Network != "default" {
		t.Errorf("expected network 'default', got %s", fc.Network)
	}
}

func TestGKEFilestoreManifest(t *testing.T) {
	fc := GKEFilestoreClass{
		Name:              "filestore-premium",
		Tier:              GKEFilestoreTierPremium,
		Network:           "my-vpc",
		ReclaimPolicy:     string(ReclaimPolicyDelete),
		VolumeBindingMode: string(VolumeBindingImmediate),
	}

	manifest := GKEFilestoreManifest(fc)

	expectedStrings := []string{
		"apiVersion: storage.k8s.io/v1",
		"kind: StorageClass",
		"name: filestore-premium",
		"provisioner: filestore.csi.storage.gke.io",
		"tier: premium",
		"network: my-vpc",
		"reclaimPolicy: Delete",
		"allowVolumeExpansion: true",
	}

	for _, expected := range expectedStrings {
		if !strings.Contains(manifest, expected) {
			t.Errorf("Filestore manifest missing expected string: %s\nGot:\n%s", expected, manifest)
		}
	}
}

func TestIsGKEProvisioner(t *testing.T) {
	tests := []struct {
		provisioner string
		want        bool
	}{
		{GKEProvisionerPD, true},
		{GKEProvisionerFilestore, true},
		{EKSProvisionerEBS, false},
		{EKSProvisionerEFS, false},
		{"kubernetes.io/gce-pd", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.provisioner, func(t *testing.T) {
			got := IsGKEProvisioner(tt.provisioner)
			if got != tt.want {
				t.Errorf("IsGKEProvisioner(%q) = %v, want %v", tt.provisioner, got, tt.want)
			}
		})
	}
}

func TestIsEKSProvisioner(t *testing.T) {
	tests := []struct {
		provisioner string
		want        bool
	}{
		{EKSProvisionerEBS, true},
		{EKSProvisionerEFS, true},
		{GKEProvisionerPD, false},
		{GKEProvisionerFilestore, false},
		{"kubernetes.io/aws-ebs", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.provisioner, func(t *testing.T) {
			got := IsEKSProvisioner(tt.provisioner)
			if got != tt.want {
				t.Errorf("IsEKSProvisioner(%q) = %v, want %v", tt.provisioner, got, tt.want)
			}
		})
	}
}

func TestGetGKEDiskTypeDescription(t *testing.T) {
	tests := []struct {
		diskType string
		wantDesc bool
	}{
		{GKEStorageTypePDStandard, true},
		{GKEStorageTypePDBalanced, true},
		{GKEStorageTypePDSSD, true},
		{GKEStorageTypePDExtreme, true},
		{"unknown-type", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.diskType, func(t *testing.T) {
			desc := GetGKEDiskTypeDescription(tt.diskType)

			if tt.wantDesc {
				if desc == "" || desc == "Unknown disk type" {
					t.Errorf("GetGKEDiskTypeDescription(%q) should return a description", tt.diskType)
				}
			} else {
				if desc != "Unknown disk type" {
					t.Errorf("GetGKEDiskTypeDescription(%q) = %s, want 'Unknown disk type'", tt.diskType, desc)
				}
			}
		})
	}
}

func TestGKEStorageClassNotes(t *testing.T) {
	notes := GKEStorageClassNotes()

	if len(notes) == 0 {
		t.Error("expected at least one storage class note")
	}

	// Verify notes contain important information
	notesText := strings.Join(notes, " ")

	expectedTopics := []string{
		"standard",
		"WaitForFirstConsumer",
		"regional",
		"Filestore",
	}

	for _, topic := range expectedTopics {
		if !strings.Contains(notesText, topic) {
			t.Errorf("storage class notes should mention %s", topic)
		}
	}
}

func TestGKEStorageConstants(t *testing.T) {
	// Verify GKE constants are defined correctly
	if GKEProvisionerPD != "pd.csi.storage.gke.io" {
		t.Errorf("GKEProvisionerPD = %s, want pd.csi.storage.gke.io", GKEProvisionerPD)
	}

	if GKEProvisionerFilestore != "filestore.csi.storage.gke.io" {
		t.Errorf("GKEProvisionerFilestore = %s, want filestore.csi.storage.gke.io", GKEProvisionerFilestore)
	}

	if GKEStorageTypePDStandard != "pd-standard" {
		t.Errorf("GKEStorageTypePDStandard = %s, want pd-standard", GKEStorageTypePDStandard)
	}

	if GKEStorageTypePDBalanced != "pd-balanced" {
		t.Errorf("GKEStorageTypePDBalanced = %s, want pd-balanced", GKEStorageTypePDBalanced)
	}

	if GKEStorageTypePDSSD != "pd-ssd" {
		t.Errorf("GKEStorageTypePDSSD = %s, want pd-ssd", GKEStorageTypePDSSD)
	}

	if GKEStorageTypePDExtreme != "pd-extreme" {
		t.Errorf("GKEStorageTypePDExtreme = %s, want pd-extreme", GKEStorageTypePDExtreme)
	}
}

func TestEKSStorageConstants(t *testing.T) {
	// Verify EKS constants for comparison
	if EKSProvisionerEBS != "ebs.csi.aws.com" {
		t.Errorf("EKSProvisionerEBS = %s, want ebs.csi.aws.com", EKSProvisionerEBS)
	}

	if EKSProvisionerEFS != "efs.csi.aws.com" {
		t.Errorf("EKSProvisionerEFS = %s, want efs.csi.aws.com", EKSProvisionerEFS)
	}

	if EKSStorageTypeGP3 != "gp3" {
		t.Errorf("EKSStorageTypeGP3 = %s, want gp3", EKSStorageTypeGP3)
	}
}
