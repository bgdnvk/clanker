package storage

import (
	"fmt"
	"strings"
)

// GKEStorageClass represents a GKE-specific storage class configuration
type GKEStorageClass struct {
	Name                 string
	DiskType             string
	ReclaimPolicy        string
	VolumeBindingMode    string
	AllowVolumeExpansion bool
	FSType               string
	IsDefault            bool
	Description          string
	ReplicationType      string // none, regional-pd
}

// GKEStorageClasses returns the standard GKE storage class configurations
func GKEStorageClasses() []GKEStorageClass {
	return []GKEStorageClass{
		{
			Name:                 "standard",
			DiskType:             GKEStorageTypePDStandard,
			ReclaimPolicy:        string(ReclaimPolicyDelete),
			VolumeBindingMode:    string(VolumeBindingWaitForFirstConsumer),
			AllowVolumeExpansion: true,
			FSType:               "ext4",
			IsDefault:            true,
			Description:          "Standard persistent disk (HDD) - cost effective for sequential workloads",
			ReplicationType:      "none",
		},
		{
			Name:                 "standard-rwo",
			DiskType:             GKEStorageTypePDStandard,
			ReclaimPolicy:        string(ReclaimPolicyDelete),
			VolumeBindingMode:    string(VolumeBindingWaitForFirstConsumer),
			AllowVolumeExpansion: true,
			FSType:               "ext4",
			IsDefault:            false,
			Description:          "Standard persistent disk with ReadWriteOnce access",
			ReplicationType:      "none",
		},
		{
			Name:                 "premium-rwo",
			DiskType:             GKEStorageTypePDSSD,
			ReclaimPolicy:        string(ReclaimPolicyDelete),
			VolumeBindingMode:    string(VolumeBindingWaitForFirstConsumer),
			AllowVolumeExpansion: true,
			FSType:               "ext4",
			IsDefault:            false,
			Description:          "SSD persistent disk for high performance workloads",
			ReplicationType:      "none",
		},
		{
			Name:                 "balanced-rwo",
			DiskType:             GKEStorageTypePDBalanced,
			ReclaimPolicy:        string(ReclaimPolicyDelete),
			VolumeBindingMode:    string(VolumeBindingWaitForFirstConsumer),
			AllowVolumeExpansion: true,
			FSType:               "ext4",
			IsDefault:            false,
			Description:          "Balanced SSD persistent disk - good balance of cost and performance",
			ReplicationType:      "none",
		},
		{
			Name:                 "extreme-rwo",
			DiskType:             GKEStorageTypePDExtreme,
			ReclaimPolicy:        string(ReclaimPolicyDelete),
			VolumeBindingMode:    string(VolumeBindingWaitForFirstConsumer),
			AllowVolumeExpansion: true,
			FSType:               "ext4",
			IsDefault:            false,
			Description:          "Extreme persistent disk for highest IOPS requirements",
			ReplicationType:      "none",
		},
	}
}

// GKERegionalStorageClasses returns regional persistent disk storage classes
func GKERegionalStorageClasses() []GKEStorageClass {
	return []GKEStorageClass{
		{
			Name:                 "standard-rwx",
			DiskType:             GKEStorageTypePDStandard,
			ReclaimPolicy:        string(ReclaimPolicyDelete),
			VolumeBindingMode:    string(VolumeBindingWaitForFirstConsumer),
			AllowVolumeExpansion: true,
			FSType:               "ext4",
			IsDefault:            false,
			Description:          "Regional standard persistent disk for high availability",
			ReplicationType:      "regional-pd",
		},
		{
			Name:                 "premium-rwx",
			DiskType:             GKEStorageTypePDSSD,
			ReclaimPolicy:        string(ReclaimPolicyDelete),
			VolumeBindingMode:    string(VolumeBindingWaitForFirstConsumer),
			AllowVolumeExpansion: true,
			FSType:               "ext4",
			IsDefault:            false,
			Description:          "Regional SSD persistent disk for high availability and performance",
			ReplicationType:      "regional-pd",
		},
	}
}

// GKEStorageClassManifest generates a YAML manifest for a GKE storage class
func GKEStorageClassManifest(sc GKEStorageClass) string {
	var sb strings.Builder

	sb.WriteString("apiVersion: storage.k8s.io/v1\n")
	sb.WriteString("kind: StorageClass\n")
	sb.WriteString("metadata:\n")
	sb.WriteString(fmt.Sprintf("  name: %s\n", sc.Name))

	if sc.IsDefault {
		sb.WriteString("  annotations:\n")
		sb.WriteString("    storageclass.kubernetes.io/is-default-class: \"true\"\n")
	}

	sb.WriteString(fmt.Sprintf("provisioner: %s\n", GKEProvisionerPD))

	sb.WriteString("parameters:\n")
	sb.WriteString(fmt.Sprintf("  type: %s\n", sc.DiskType))

	if sc.ReplicationType == "regional-pd" {
		sb.WriteString("  replication-type: regional-pd\n")
	}

	if sc.FSType != "" {
		sb.WriteString(fmt.Sprintf("  csi.storage.k8s.io/fstype: %s\n", sc.FSType))
	}

	sb.WriteString(fmt.Sprintf("reclaimPolicy: %s\n", sc.ReclaimPolicy))
	sb.WriteString(fmt.Sprintf("volumeBindingMode: %s\n", sc.VolumeBindingMode))
	sb.WriteString(fmt.Sprintf("allowVolumeExpansion: %v\n", sc.AllowVolumeExpansion))

	return sb.String()
}

// GetGKEStorageRecommendation returns GKE-specific storage recommendations for a use case
func GetGKEStorageRecommendation(useCase string) StorageRecommendation {
	useCaseLower := strings.ToLower(useCase)

	// Database workloads
	if containsAny(useCaseLower, []string{"database", "mysql", "postgres", "mongodb", "redis", "cassandra"}) {
		return StorageRecommendation{
			StorageClass: "premium-rwo",
			DiskType:     GKEStorageTypePDSSD,
			AccessMode:   string(AccessModeReadWriteOnce),
			Reason:       "Databases require high IOPS and low latency provided by SSD persistent disks",
			Considerations: []string{
				"Consider pd-extreme for very high IOPS requirements",
				"Use regional-pd for high availability if your database supports it",
				"Enable volume expansion for growing databases",
			},
		}
	}

	// High availability workloads
	if containsAny(useCaseLower, []string{"high availability", "ha ", "regional", "multi-zone", "disaster recovery"}) {
		return StorageRecommendation{
			StorageClass: "premium-rwx",
			DiskType:     GKEStorageTypePDSSD,
			AccessMode:   string(AccessModeReadWriteOnce),
			Reason:       "Regional persistent disks replicate data across zones for high availability",
			Considerations: []string{
				"Regional PDs have slightly higher latency due to replication",
				"Cost is approximately 2x standard PDs due to replication",
				"Supports automatic failover in case of zone outage",
			},
		}
	}

	// Logging and analytics
	if containsAny(useCaseLower, []string{"log", "elastic", "analytics", "data lake", "archiv"}) {
		return StorageRecommendation{
			StorageClass: "standard-rwo",
			DiskType:     GKEStorageTypePDStandard,
			AccessMode:   string(AccessModeReadWriteOnce),
			Reason:       "Standard persistent disks are cost effective for sequential read/write workloads",
			Considerations: []string{
				"Standard PDs offer good sequential throughput",
				"Consider pd-balanced for mixed workloads",
				"Use Filestore for shared access across pods",
			},
		}
	}

	// Shared file storage
	if containsAny(useCaseLower, []string{"shared", "nfs", "file", "readwritemany", "rwx"}) {
		return StorageRecommendation{
			StorageClass: "filestore-standard",
			DiskType:     "filestore",
			AccessMode:   string(AccessModeReadWriteMany),
			Reason:       "Filestore provides NFS-based shared file storage accessible by multiple pods",
			Considerations: []string{
				"Minimum capacity is 1TB for standard tier",
				"Use premium tier for higher performance requirements",
				"Filestore has higher cost than persistent disks",
			},
		}
	}

	// Build and CI/CD
	if containsAny(useCaseLower, []string{"build", "ci", "cd", "pipeline", "jenkins", "cache"}) {
		return StorageRecommendation{
			StorageClass: "balanced-rwo",
			DiskType:     GKEStorageTypePDBalanced,
			AccessMode:   string(AccessModeReadWriteOnce),
			Reason:       "Balanced persistent disks provide good performance for build workloads at moderate cost",
			Considerations: []string{
				"Consider SSD for faster build times if budget allows",
				"Enable volume expansion for growing cache",
				"Consider emptyDir for ephemeral build artifacts",
			},
		}
	}

	// Default recommendation
	return StorageRecommendation{
		StorageClass: "standard-rwo",
		DiskType:     GKEStorageTypePDStandard,
		AccessMode:   string(AccessModeReadWriteOnce),
		Reason:       "Standard persistent disks offer a good balance of cost and performance for general workloads",
		Considerations: []string{
			"Upgrade to pd-balanced or pd-ssd for better performance",
			"Use regional PDs for high availability requirements",
			"Consider Filestore if you need ReadWriteMany access",
		},
	}
}

// StorageRecommendation represents a storage recommendation
type StorageRecommendation struct {
	StorageClass   string
	DiskType       string
	AccessMode     string
	Reason         string
	Considerations []string
}

// GKEFilestoreStorageClass returns a Filestore storage class configuration
func GKEFilestoreStorageClass(tier string, networkName string) GKEFilestoreClass {
	return GKEFilestoreClass{
		Name:              fmt.Sprintf("filestore-%s", tier),
		Tier:              tier,
		Network:           networkName,
		ReclaimPolicy:     string(ReclaimPolicyDelete),
		VolumeBindingMode: string(VolumeBindingImmediate),
	}
}

// GKEFilestoreClass represents a GKE Filestore storage class
type GKEFilestoreClass struct {
	Name              string
	Tier              string
	Network           string
	ReclaimPolicy     string
	VolumeBindingMode string
}

// GKEFilestoreManifest generates a YAML manifest for a Filestore storage class
func GKEFilestoreManifest(fc GKEFilestoreClass) string {
	var sb strings.Builder

	sb.WriteString("apiVersion: storage.k8s.io/v1\n")
	sb.WriteString("kind: StorageClass\n")
	sb.WriteString("metadata:\n")
	sb.WriteString(fmt.Sprintf("  name: %s\n", fc.Name))
	sb.WriteString(fmt.Sprintf("provisioner: %s\n", GKEProvisionerFilestore))
	sb.WriteString("parameters:\n")
	sb.WriteString(fmt.Sprintf("  tier: %s\n", fc.Tier))

	if fc.Network != "" {
		sb.WriteString(fmt.Sprintf("  network: %s\n", fc.Network))
	}

	sb.WriteString(fmt.Sprintf("reclaimPolicy: %s\n", fc.ReclaimPolicy))
	sb.WriteString(fmt.Sprintf("volumeBindingMode: %s\n", fc.VolumeBindingMode))
	sb.WriteString("allowVolumeExpansion: true\n")

	return sb.String()
}

// IsGKEProvisioner checks if a provisioner is a GKE CSI driver
func IsGKEProvisioner(provisioner string) bool {
	return provisioner == GKEProvisionerPD || provisioner == GKEProvisionerFilestore
}

// IsEKSProvisioner checks if a provisioner is an EKS CSI driver
func IsEKSProvisioner(provisioner string) bool {
	return provisioner == EKSProvisionerEBS || provisioner == EKSProvisionerEFS
}

// GetGKEDiskTypeDescription returns a human readable description for a GKE disk type
func GetGKEDiskTypeDescription(diskType string) string {
	descriptions := map[string]string{
		GKEStorageTypePDStandard: "Standard persistent disk (HDD) - cost effective for sequential I/O",
		GKEStorageTypePDBalanced: "Balanced persistent disk - SSD performance at moderate cost",
		GKEStorageTypePDSSD:      "SSD persistent disk - high IOPS and throughput",
		GKEStorageTypePDExtreme:  "Extreme persistent disk - highest IOPS for demanding workloads",
	}

	if desc, ok := descriptions[diskType]; ok {
		return desc
	}
	return "Unknown disk type"
}

// GKEStorageClassNotes returns important notes about GKE storage classes
func GKEStorageClassNotes() []string {
	return []string{
		"GKE automatically creates 'standard' and 'standard-rwo' storage classes",
		"Use WaitForFirstConsumer binding mode for better pod scheduling",
		"Regional PDs (replication-type: regional-pd) provide zone-level redundancy",
		"Filestore requires a minimum of 1TB capacity for standard tier",
		"Volume expansion is supported but may require pod restart",
		"pd-extreme requires a minimum of 500GB and provides up to 120,000 IOPS",
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
