package storage

import (
	"fmt"
	"strings"
)

// AKSStorageClass represents an AKS-specific storage class configuration
type AKSStorageClass struct {
	Name                 string
	SKUName              string
	ReclaimPolicy        string
	VolumeBindingMode    string
	AllowVolumeExpansion bool
	FSType               string
	IsDefault            bool
	Description          string
	CachingMode          string // None, ReadOnly, ReadWrite
}

// AKSStorageClasses returns the standard AKS storage class configurations
func AKSStorageClasses() []AKSStorageClass {
	return []AKSStorageClass{
		{
			Name:                 "managed-csi",
			SKUName:              AKSStorageTypePremiumSSD,
			ReclaimPolicy:        string(ReclaimPolicyDelete),
			VolumeBindingMode:    string(VolumeBindingWaitForFirstConsumer),
			AllowVolumeExpansion: true,
			FSType:               "ext4",
			IsDefault:            true,
			Description:          "Premium SSD managed disk - default storage class for AKS",
			CachingMode:          AKSCachingModeReadOnly,
		},
		{
			Name:                 "managed-csi-premium",
			SKUName:              AKSStorageTypePremiumSSD,
			ReclaimPolicy:        string(ReclaimPolicyDelete),
			VolumeBindingMode:    string(VolumeBindingWaitForFirstConsumer),
			AllowVolumeExpansion: true,
			FSType:               "ext4",
			IsDefault:            false,
			Description:          "Premium SSD managed disk for high performance workloads",
			CachingMode:          AKSCachingModeReadOnly,
		},
		{
			Name:                 "managed-csi-standard",
			SKUName:              AKSStorageTypeStandardSSD,
			ReclaimPolicy:        string(ReclaimPolicyDelete),
			VolumeBindingMode:    string(VolumeBindingWaitForFirstConsumer),
			AllowVolumeExpansion: true,
			FSType:               "ext4",
			IsDefault:            false,
			Description:          "Standard SSD managed disk - balanced cost and performance",
			CachingMode:          AKSCachingModeReadOnly,
		},
		{
			Name:                 "managed-csi-hdd",
			SKUName:              AKSStorageTypeStandardHDD,
			ReclaimPolicy:        string(ReclaimPolicyDelete),
			VolumeBindingMode:    string(VolumeBindingWaitForFirstConsumer),
			AllowVolumeExpansion: true,
			FSType:               "ext4",
			IsDefault:            false,
			Description:          "Standard HDD managed disk - cost effective for sequential workloads",
			CachingMode:          AKSCachingModeNone,
		},
		{
			Name:                 "managed-csi-premium-v2",
			SKUName:              AKSStorageTypePremiumSSDv2,
			ReclaimPolicy:        string(ReclaimPolicyDelete),
			VolumeBindingMode:    string(VolumeBindingWaitForFirstConsumer),
			AllowVolumeExpansion: true,
			FSType:               "ext4",
			IsDefault:            false,
			Description:          "Premium SSD v2 - configurable IOPS and throughput",
			CachingMode:          AKSCachingModeNone,
		},
		{
			Name:                 "managed-csi-ultra",
			SKUName:              AKSStorageTypeUltraSSD,
			ReclaimPolicy:        string(ReclaimPolicyDelete),
			VolumeBindingMode:    string(VolumeBindingWaitForFirstConsumer),
			AllowVolumeExpansion: true,
			FSType:               "ext4",
			IsDefault:            false,
			Description:          "Ultra SSD - highest IOPS and throughput for demanding workloads",
			CachingMode:          AKSCachingModeNone,
		},
	}
}

// AKSFileStorageClasses returns Azure Files storage class configurations
func AKSFileStorageClasses() []AKSStorageClass {
	return []AKSStorageClass{
		{
			Name:                 "azurefile-csi",
			SKUName:              AKSFilesStandard,
			ReclaimPolicy:        string(ReclaimPolicyDelete),
			VolumeBindingMode:    string(VolumeBindingImmediate),
			AllowVolumeExpansion: true,
			IsDefault:            false,
			Description:          "Standard Azure Files - SMB file share with shared access",
		},
		{
			Name:                 "azurefile-csi-premium",
			SKUName:              AKSFilesPremium,
			ReclaimPolicy:        string(ReclaimPolicyDelete),
			VolumeBindingMode:    string(VolumeBindingImmediate),
			AllowVolumeExpansion: true,
			IsDefault:            false,
			Description:          "Premium Azure Files - SSD-backed file share for high performance",
		},
		{
			Name:                 "azurefile-csi-nfs",
			SKUName:              AKSFilesPremium,
			ReclaimPolicy:        string(ReclaimPolicyDelete),
			VolumeBindingMode:    string(VolumeBindingImmediate),
			AllowVolumeExpansion: true,
			IsDefault:            false,
			Description:          "Azure Files NFS - Premium NFS file share for Linux workloads",
		},
	}
}

// AKSStorageClassManifest generates a YAML manifest for an AKS storage class
func AKSStorageClassManifest(sc AKSStorageClass) string {
	var sb strings.Builder

	sb.WriteString("apiVersion: storage.k8s.io/v1\n")
	sb.WriteString("kind: StorageClass\n")
	sb.WriteString("metadata:\n")
	sb.WriteString(fmt.Sprintf("  name: %s\n", sc.Name))

	if sc.IsDefault {
		sb.WriteString("  annotations:\n")
		sb.WriteString("    storageclass.kubernetes.io/is-default-class: \"true\"\n")
	}

	// Determine provisioner based on storage class name
	if strings.Contains(sc.Name, "azurefile") {
		sb.WriteString(fmt.Sprintf("provisioner: %s\n", AKSProvisionerFile))
	} else {
		sb.WriteString(fmt.Sprintf("provisioner: %s\n", AKSProvisionerDisk))
	}

	sb.WriteString("parameters:\n")
	sb.WriteString(fmt.Sprintf("  skuName: %s\n", sc.SKUName))

	if sc.CachingMode != "" && !strings.Contains(sc.Name, "azurefile") {
		sb.WriteString(fmt.Sprintf("  cachingMode: %s\n", sc.CachingMode))
	}

	if sc.FSType != "" && !strings.Contains(sc.Name, "azurefile") {
		sb.WriteString(fmt.Sprintf("  csi.storage.k8s.io/fstype: %s\n", sc.FSType))
	}

	// NFS-specific parameter for Azure Files
	if strings.Contains(sc.Name, "nfs") {
		sb.WriteString("  protocol: nfs\n")
	}

	sb.WriteString(fmt.Sprintf("reclaimPolicy: %s\n", sc.ReclaimPolicy))
	sb.WriteString(fmt.Sprintf("volumeBindingMode: %s\n", sc.VolumeBindingMode))
	sb.WriteString(fmt.Sprintf("allowVolumeExpansion: %v\n", sc.AllowVolumeExpansion))

	return sb.String()
}

// GetAKSStorageRecommendation returns AKS-specific storage recommendations for a use case
func GetAKSStorageRecommendation(useCase string) StorageRecommendation {
	useCaseLower := strings.ToLower(useCase)

	// Database workloads
	if containsAny(useCaseLower, []string{"database", "mysql", "postgres", "mongodb", "redis", "cassandra", "sql"}) {
		return StorageRecommendation{
			StorageClass: "managed-csi-premium",
			DiskType:     AKSStorageTypePremiumSSD,
			AccessMode:   string(AccessModeReadWriteOnce),
			Reason:       "Databases require high IOPS and low latency provided by Premium SSD",
			Considerations: []string{
				"Consider Premium SSD v2 for customizable IOPS and throughput",
				"Use Ultra SSD for mission-critical databases requiring highest performance",
				"Enable ReadOnly caching for read-heavy workloads",
				"Zone-redundant storage (ZRS) available for Premium SSD",
			},
		}
	}

	// High performance workloads
	if containsAny(useCaseLower, []string{"high performance", "iops", "latency", "ultra", "mission critical"}) {
		return StorageRecommendation{
			StorageClass: "managed-csi-ultra",
			DiskType:     AKSStorageTypeUltraSSD,
			AccessMode:   string(AccessModeReadWriteOnce),
			Reason:       "Ultra SSD provides highest IOPS and throughput for demanding workloads",
			Considerations: []string{
				"Ultra SSD requires specific VM sizes that support it",
				"Available in limited regions",
				"Configurable IOPS up to 160,000 and throughput up to 4,000 MB/s",
				"Higher cost than Premium SSD",
			},
		}
	}

	// Logging and analytics
	if containsAny(useCaseLower, []string{"log", "elastic", "analytics", "data lake", "archiv", "backup"}) {
		return StorageRecommendation{
			StorageClass: "managed-csi-hdd",
			DiskType:     AKSStorageTypeStandardHDD,
			AccessMode:   string(AccessModeReadWriteOnce),
			Reason:       "Standard HDD is cost effective for sequential read/write workloads",
			Considerations: []string{
				"Good throughput for large sequential operations",
				"Consider Standard SSD for better random I/O performance",
				"Use Azure Files for shared access if needed",
			},
		}
	}

	// Shared file storage
	if containsAny(useCaseLower, []string{"shared", "nfs", "file", "readwritemany", "rwx", "smb"}) {
		return StorageRecommendation{
			StorageClass: "azurefile-csi-premium",
			DiskType:     AKSFilesPremium,
			AccessMode:   string(AccessModeReadWriteMany),
			Reason:       "Azure Files provides shared file storage accessible by multiple pods",
			Considerations: []string{
				"Use NFS protocol for Linux workloads (azurefile-csi-nfs)",
				"SMB protocol available for Windows workloads",
				"Premium tier required for NFS",
				"Consider Azure NetApp Files for enterprise NFS workloads",
			},
		}
	}

	// Build and CI/CD
	if containsAny(useCaseLower, []string{"build", "ci", "cd", "pipeline", "jenkins", "cache"}) {
		return StorageRecommendation{
			StorageClass: "managed-csi-standard",
			DiskType:     AKSStorageTypeStandardSSD,
			AccessMode:   string(AccessModeReadWriteOnce),
			Reason:       "Standard SSD provides good performance for build workloads at moderate cost",
			Considerations: []string{
				"Consider Premium SSD for faster build times",
				"Enable volume expansion for growing cache",
				"Use emptyDir for ephemeral build artifacts",
			},
		}
	}

	// Development and testing
	if containsAny(useCaseLower, []string{"dev", "test", "staging", "preview", "ephemeral"}) {
		return StorageRecommendation{
			StorageClass: "managed-csi-standard",
			DiskType:     AKSStorageTypeStandardSSD,
			AccessMode:   string(AccessModeReadWriteOnce),
			Reason:       "Standard SSD offers good balance for development workloads",
			Considerations: []string{
				"Use Standard HDD for lowest cost non-production workloads",
				"Consider ephemeral OS disks for stateless workloads",
			},
		}
	}

	// Default recommendation
	return StorageRecommendation{
		StorageClass: "managed-csi",
		DiskType:     AKSStorageTypePremiumSSD,
		AccessMode:   string(AccessModeReadWriteOnce),
		Reason:       "Premium SSD is the default storage class for AKS with good performance",
		Considerations: []string{
			"Default storage class in AKS clusters",
			"Use Standard SSD or HDD for cost optimization",
			"Consider Azure Files if you need ReadWriteMany access",
			"Premium SSD v2 available for customizable performance",
		},
	}
}

// GetAKSDiskTypeDescription returns a human readable description for an AKS disk type
func GetAKSDiskTypeDescription(skuName string) string {
	descriptions := map[string]string{
		AKSStorageTypeStandardHDD:  "Standard HDD - Cost effective for sequential workloads and backups",
		AKSStorageTypeStandardSSD:  "Standard SSD - Balanced performance and cost for general workloads",
		AKSStorageTypePremiumSSD:   "Premium SSD - High IOPS and throughput for production workloads",
		AKSStorageTypePremiumSSDv2: "Premium SSD v2 - Configurable IOPS and throughput",
		AKSStorageTypeUltraSSD:     "Ultra SSD - Highest performance for mission-critical workloads",
		AKSFilesStandard:           "Standard Azure Files - SMB/NFS file shares for shared storage",
		AKSFilesPremium:            "Premium Azure Files - SSD-backed file shares with higher performance",
	}

	if desc, ok := descriptions[skuName]; ok {
		return desc
	}
	return "Unknown disk type"
}

// AKSStorageClassNotes returns important notes about AKS storage classes
func AKSStorageClassNotes() []string {
	return []string{
		"AKS automatically creates 'managed-csi' and 'azurefile-csi' storage classes",
		"Use WaitForFirstConsumer binding mode for better pod scheduling with Azure Disk",
		"Azure Files uses Immediate binding mode as it is not topology-constrained",
		"Premium SSD supports zone-redundant storage (ZRS) for high availability",
		"Ultra SSD requires specific VM sizes and is available in limited regions",
		"Azure Files NFS requires Premium tier and is recommended for Linux workloads",
		"Volume expansion is supported but may require pod restart for Azure Disk",
		"Consider Azure NetApp Files for enterprise NFS requirements",
	}
}

// IsAKSProvisioner checks if a provisioner is an AKS CSI driver
func IsAKSProvisioner(provisioner string) bool {
	return provisioner == AKSProvisionerDisk ||
		provisioner == AKSProvisionerFile ||
		provisioner == AKSProvisionerBlob
}

// EKSStorageComparison returns comparison notes between AKS and EKS storage
func EKSStorageComparison() map[string]string {
	return map[string]string{
		"aks_block_storage": "Azure Disk (managed-csi)",
		"eks_block_storage": "EBS (ebs-csi)",
		"gke_block_storage": "Persistent Disk (pd-csi)",
		"aks_file_storage":  "Azure Files (azurefile-csi)",
		"eks_file_storage":  "EFS (efs-csi)",
		"gke_file_storage":  "Filestore (filestore-csi)",
		"aks_default_class": "managed-csi (Premium SSD)",
		"eks_default_class": "gp2 or gp3",
		"gke_default_class": "standard (pd-standard)",
		"aks_high_perf":     "Ultra SSD, Premium SSD v2",
		"eks_high_perf":     "io2 Block Express",
		"gke_high_perf":     "pd-extreme",
	}
}

// GKEStorageComparison returns comparison notes between AKS and GKE storage
func GKEStorageComparison() map[string]string {
	return map[string]string{
		"aks_provisioner": AKSProvisionerDisk,
		"gke_provisioner": GKEProvisionerPD,
		"aks_default":     AKSStorageTypePremiumSSD,
		"gke_default":     GKEStorageTypePDStandard,
		"aks_premium":     AKSStorageTypePremiumSSD,
		"gke_premium":     GKEStorageTypePDSSD,
		"aks_extreme":     AKSStorageTypeUltraSSD,
		"gke_extreme":     GKEStorageTypePDExtreme,
		"aks_files":       AKSProvisionerFile,
		"gke_files":       GKEProvisionerFilestore,
	}
}
