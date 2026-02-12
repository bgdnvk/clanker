package storage

import (
	"strings"
	"testing"
)

func TestAKSStorageClasses(t *testing.T) {
	classes := AKSStorageClasses()

	if len(classes) == 0 {
		t.Error("expected at least one storage class")
	}

	// Verify expected storage classes exist
	expectedClasses := []string{
		"managed-csi",
		"managed-csi-premium",
		"managed-csi-standard",
		"managed-csi-hdd",
		"managed-csi-premium-v2",
		"managed-csi-ultra",
	}

	classNames := make(map[string]bool)
	for _, sc := range classes {
		classNames[sc.Name] = true
	}

	for _, expected := range expectedClasses {
		if !classNames[expected] {
			t.Errorf("expected storage class %s not found", expected)
		}
	}

	// Verify default class
	hasDefault := false
	for _, sc := range classes {
		if sc.IsDefault {
			hasDefault = true
			if sc.Name != "managed-csi" {
				t.Errorf("expected managed-csi to be default, got %s", sc.Name)
			}
			break
		}
	}
	if !hasDefault {
		t.Error("expected one storage class to be marked as default")
	}
}

func TestAKSFileStorageClasses(t *testing.T) {
	classes := AKSFileStorageClasses()

	if len(classes) == 0 {
		t.Error("expected at least one file storage class")
	}

	// Verify expected file storage classes exist
	expectedClasses := []string{
		"azurefile-csi",
		"azurefile-csi-premium",
		"azurefile-csi-nfs",
	}

	classNames := make(map[string]bool)
	for _, sc := range classes {
		classNames[sc.Name] = true
	}

	for _, expected := range expectedClasses {
		if !classNames[expected] {
			t.Errorf("expected file storage class %s not found", expected)
		}
	}
}

func TestAKSStorageClassManifest(t *testing.T) {
	tests := []struct {
		name           string
		storageClass   AKSStorageClass
		wantProvisioner string
		wantSKU        string
	}{
		{
			name: "Premium SSD",
			storageClass: AKSStorageClass{
				Name:                 "managed-csi-premium",
				SKUName:              AKSStorageTypePremiumSSD,
				ReclaimPolicy:        string(ReclaimPolicyDelete),
				VolumeBindingMode:    string(VolumeBindingWaitForFirstConsumer),
				AllowVolumeExpansion: true,
				FSType:               "ext4",
				CachingMode:          AKSCachingModeReadOnly,
			},
			wantProvisioner: AKSProvisionerDisk,
			wantSKU:        AKSStorageTypePremiumSSD,
		},
		{
			name: "Azure Files",
			storageClass: AKSStorageClass{
				Name:                 "azurefile-csi-premium",
				SKUName:              AKSFilesPremium,
				ReclaimPolicy:        string(ReclaimPolicyDelete),
				VolumeBindingMode:    string(VolumeBindingImmediate),
				AllowVolumeExpansion: true,
			},
			wantProvisioner: AKSProvisionerFile,
			wantSKU:        AKSFilesPremium,
		},
		{
			name: "Azure Files NFS",
			storageClass: AKSStorageClass{
				Name:                 "azurefile-csi-nfs",
				SKUName:              AKSFilesPremium,
				ReclaimPolicy:        string(ReclaimPolicyDelete),
				VolumeBindingMode:    string(VolumeBindingImmediate),
				AllowVolumeExpansion: true,
			},
			wantProvisioner: AKSProvisionerFile,
			wantSKU:        AKSFilesPremium,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifest := AKSStorageClassManifest(tt.storageClass)

			if !strings.Contains(manifest, tt.wantProvisioner) {
				t.Errorf("manifest should contain provisioner %s", tt.wantProvisioner)
			}

			if !strings.Contains(manifest, tt.wantSKU) {
				t.Errorf("manifest should contain SKU %s", tt.wantSKU)
			}

			if !strings.Contains(manifest, "apiVersion: storage.k8s.io/v1") {
				t.Error("manifest should contain apiVersion")
			}

			if !strings.Contains(manifest, "kind: StorageClass") {
				t.Error("manifest should contain kind: StorageClass")
			}

			// NFS should have protocol parameter
			if strings.Contains(tt.storageClass.Name, "nfs") {
				if !strings.Contains(manifest, "protocol: nfs") {
					t.Error("NFS storage class should have protocol: nfs")
				}
			}
		})
	}
}

func TestAKSStorageClassManifestDefault(t *testing.T) {
	sc := AKSStorageClass{
		Name:                 "managed-csi",
		SKUName:              AKSStorageTypePremiumSSD,
		ReclaimPolicy:        string(ReclaimPolicyDelete),
		VolumeBindingMode:    string(VolumeBindingWaitForFirstConsumer),
		AllowVolumeExpansion: true,
		IsDefault:            true,
	}

	manifest := AKSStorageClassManifest(sc)

	if !strings.Contains(manifest, "storageclass.kubernetes.io/is-default-class: \"true\"") {
		t.Error("default storage class should have is-default-class annotation")
	}
}

func TestGetAKSStorageRecommendation(t *testing.T) {
	tests := []struct {
		name                string
		useCase             string
		wantStorageClass    string
		wantDiskTypeContains string
	}{
		{
			name:                "Database",
			useCase:             "postgresql database",
			wantStorageClass:    "managed-csi-premium",
			wantDiskTypeContains: "Premium",
		},
		{
			name:                "High Performance",
			useCase:             "ultra high iops workload",
			wantStorageClass:    "managed-csi-ultra",
			wantDiskTypeContains: "Ultra",
		},
		{
			name:                "Logging",
			useCase:             "elasticsearch logging",
			wantStorageClass:    "managed-csi-hdd",
			wantDiskTypeContains: "Standard",
		},
		{
			name:                "Shared Storage",
			useCase:             "shared nfs volume",
			wantStorageClass:    "azurefile-csi-premium",
			wantDiskTypeContains: "Premium",
		},
		{
			name:                "CI/CD",
			useCase:             "jenkins build cache",
			wantStorageClass:    "managed-csi-standard",
			wantDiskTypeContains: "Standard",
		},
		{
			name:                "Development",
			useCase:             "dev test environment",
			wantStorageClass:    "managed-csi-standard",
			wantDiskTypeContains: "Standard",
		},
		{
			name:                "Default",
			useCase:             "general application",
			wantStorageClass:    "managed-csi",
			wantDiskTypeContains: "Premium",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := GetAKSStorageRecommendation(tt.useCase)

			if rec.StorageClass != tt.wantStorageClass {
				t.Errorf("StorageClass = %s, want %s", rec.StorageClass, tt.wantStorageClass)
			}

			if !strings.Contains(rec.DiskType, tt.wantDiskTypeContains) {
				t.Errorf("DiskType = %s, want containing %s", rec.DiskType, tt.wantDiskTypeContains)
			}

			if rec.Reason == "" {
				t.Error("recommendation should have a reason")
			}

			if len(rec.Considerations) == 0 {
				t.Error("recommendation should have considerations")
			}
		})
	}
}

func TestGetAKSDiskTypeDescription(t *testing.T) {
	diskTypes := []string{
		AKSStorageTypeStandardHDD,
		AKSStorageTypeStandardSSD,
		AKSStorageTypePremiumSSD,
		AKSStorageTypePremiumSSDv2,
		AKSStorageTypeUltraSSD,
		AKSFilesStandard,
		AKSFilesPremium,
	}

	for _, diskType := range diskTypes {
		desc := GetAKSDiskTypeDescription(diskType)

		if desc == "" {
			t.Errorf("expected description for disk type %s", diskType)
		}

		if desc == "Unknown disk type" {
			t.Errorf("expected specific description for %s, got unknown", diskType)
		}
	}

	// Test unknown disk type
	unknownDesc := GetAKSDiskTypeDescription("unknown-type")
	if unknownDesc != "Unknown disk type" {
		t.Errorf("expected 'Unknown disk type' for unknown type, got %s", unknownDesc)
	}
}

func TestAKSStorageClassNotes(t *testing.T) {
	notes := AKSStorageClassNotes()

	if len(notes) == 0 {
		t.Error("expected at least one storage class note")
	}

	notesText := strings.Join(notes, " ")

	expectedTopics := []string{
		"managed-csi",
		"azurefile",
		"WaitForFirstConsumer",
		"Ultra SSD",
		"Azure Files",
		"Premium",
	}

	for _, topic := range expectedTopics {
		if !strings.Contains(notesText, topic) {
			t.Errorf("storage class notes should mention %s", topic)
		}
	}
}

func TestIsAKSProvisioner(t *testing.T) {
	tests := []struct {
		provisioner string
		want        bool
	}{
		{AKSProvisionerDisk, true},
		{AKSProvisionerFile, true},
		{AKSProvisionerBlob, true},
		{GKEProvisionerPD, false},
		{GKEProvisionerFilestore, false},
		{EKSProvisionerEBS, false},
		{EKSProvisionerEFS, false},
		{"kubernetes.io/aws-ebs", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.provisioner, func(t *testing.T) {
			got := IsAKSProvisioner(tt.provisioner)
			if got != tt.want {
				t.Errorf("IsAKSProvisioner(%q) = %v, want %v", tt.provisioner, got, tt.want)
			}
		})
	}
}

func TestEKSStorageComparison(t *testing.T) {
	comparison := EKSStorageComparison()

	if len(comparison) == 0 {
		t.Error("expected storage comparison entries")
	}

	// Verify AKS entries
	aksKeys := []string{"aks_block_storage", "aks_file_storage", "aks_default_class", "aks_high_perf"}
	for _, key := range aksKeys {
		if _, ok := comparison[key]; !ok {
			t.Errorf("expected comparison key %s", key)
		}
	}

	// Verify EKS entries
	eksKeys := []string{"eks_block_storage", "eks_file_storage", "eks_default_class", "eks_high_perf"}
	for _, key := range eksKeys {
		if _, ok := comparison[key]; !ok {
			t.Errorf("expected comparison key %s", key)
		}
	}

	// Verify GKE entries
	gkeKeys := []string{"gke_block_storage", "gke_file_storage", "gke_default_class", "gke_high_perf"}
	for _, key := range gkeKeys {
		if _, ok := comparison[key]; !ok {
			t.Errorf("expected comparison key %s", key)
		}
	}
}

func TestGKEStorageComparison(t *testing.T) {
	comparison := GKEStorageComparison()

	if len(comparison) == 0 {
		t.Error("expected GKE storage comparison entries")
	}

	expectedKeys := []string{
		"aks_provisioner",
		"gke_provisioner",
		"aks_default",
		"gke_default",
		"aks_premium",
		"gke_premium",
		"aks_extreme",
		"gke_extreme",
		"aks_files",
		"gke_files",
	}

	for _, key := range expectedKeys {
		if _, ok := comparison[key]; !ok {
			t.Errorf("expected comparison key %s", key)
		}
	}

	if comparison["aks_provisioner"] != AKSProvisionerDisk {
		t.Errorf("aks_provisioner = %s, want %s", comparison["aks_provisioner"], AKSProvisionerDisk)
	}
}

func TestAKSStorageClassStruct(t *testing.T) {
	sc := AKSStorageClass{
		Name:                 "test-class",
		SKUName:              AKSStorageTypePremiumSSD,
		ReclaimPolicy:        string(ReclaimPolicyDelete),
		VolumeBindingMode:    string(VolumeBindingWaitForFirstConsumer),
		AllowVolumeExpansion: true,
		FSType:               "ext4",
		IsDefault:            false,
		Description:          "Test storage class",
		CachingMode:          AKSCachingModeReadOnly,
	}

	if sc.Name != "test-class" {
		t.Errorf("expected name 'test-class', got %s", sc.Name)
	}

	if sc.SKUName != AKSStorageTypePremiumSSD {
		t.Errorf("expected SKU %s, got %s", AKSStorageTypePremiumSSD, sc.SKUName)
	}

	if sc.CachingMode != AKSCachingModeReadOnly {
		t.Errorf("expected caching mode %s, got %s", AKSCachingModeReadOnly, sc.CachingMode)
	}
}

func TestAKSStorageConstants(t *testing.T) {
	// Verify CSI provisioner constants
	if AKSProvisionerDisk != "disk.csi.azure.com" {
		t.Errorf("AKSProvisionerDisk = %s, want disk.csi.azure.com", AKSProvisionerDisk)
	}

	if AKSProvisionerFile != "file.csi.azure.com" {
		t.Errorf("AKSProvisionerFile = %s, want file.csi.azure.com", AKSProvisionerFile)
	}

	if AKSProvisionerBlob != "blob.csi.azure.com" {
		t.Errorf("AKSProvisionerBlob = %s, want blob.csi.azure.com", AKSProvisionerBlob)
	}

	// Verify storage type constants
	if AKSStorageTypePremiumSSD != "Premium_LRS" {
		t.Errorf("AKSStorageTypePremiumSSD = %s, want Premium_LRS", AKSStorageTypePremiumSSD)
	}

	if AKSStorageTypeUltraSSD != "UltraSSD_LRS" {
		t.Errorf("AKSStorageTypeUltraSSD = %s, want UltraSSD_LRS", AKSStorageTypeUltraSSD)
	}
}

func TestAKSCachingModeConstants(t *testing.T) {
	if AKSCachingModeNone != "None" {
		t.Errorf("AKSCachingModeNone = %s, want None", AKSCachingModeNone)
	}

	if AKSCachingModeReadOnly != "ReadOnly" {
		t.Errorf("AKSCachingModeReadOnly = %s, want ReadOnly", AKSCachingModeReadOnly)
	}

	if AKSCachingModeReadWrite != "ReadWrite" {
		t.Errorf("AKSCachingModeReadWrite = %s, want ReadWrite", AKSCachingModeReadWrite)
	}
}
