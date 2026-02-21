package analyzer

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

var findingCounter int

// GenerateFindingID generates a unique finding ID
func GenerateFindingID() string {
	findingCounter++
	return fmt.Sprintf("IAM-%d-%d", time.Now().Unix(), findingCounter)
}

// SeverityScore returns a numeric score for severity comparison
func SeverityScore(severity string) int {
	switch severity {
	case SeverityCritical:
		return 5
	case SeverityHigh:
		return 4
	case SeverityMedium:
		return 3
	case SeverityLow:
		return 2
	case SeverityInfo:
		return 1
	default:
		return 0
	}
}

// SortFindingsBySeverity sorts findings by severity (critical first)
func SortFindingsBySeverity(findings []SecurityFinding) {
	sort.Slice(findings, func(i, j int) bool {
		return SeverityScore(findings[i].Severity) > SeverityScore(findings[j].Severity)
	})
}

// FilterFindingsBySeverity returns findings at or above the specified severity
func FilterFindingsBySeverity(findings []SecurityFinding, minSeverity string) []SecurityFinding {
	minScore := SeverityScore(minSeverity)
	var filtered []SecurityFinding
	for _, f := range findings {
		if SeverityScore(f.Severity) >= minScore {
			filtered = append(filtered, f)
		}
	}
	return filtered
}

// FormatFinding formats a single finding for display
func FormatFinding(f SecurityFinding) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("[%s] %s\n", strings.ToUpper(f.Severity), f.Type))
	sb.WriteString(fmt.Sprintf("Resource: %s\n", f.ResourceARN))
	sb.WriteString(fmt.Sprintf("Description: %s\n", f.Description))

	if len(f.Actions) > 0 {
		sb.WriteString(fmt.Sprintf("Actions: %s\n", strings.Join(f.Actions, ", ")))
	}
	if len(f.Resources) > 0 {
		sb.WriteString(fmt.Sprintf("Resources: %s\n", strings.Join(f.Resources, ", ")))
	}

	sb.WriteString(fmt.Sprintf("Remediation: %s\n", f.Remediation))

	return sb.String()
}

// FormatFindings formats multiple findings for display
func FormatFindings(findings []SecurityFinding) string {
	if len(findings) == 0 {
		return "No security findings identified."
	}

	// Sort by severity
	SortFindingsBySeverity(findings)

	var sb strings.Builder

	// Summary
	sb.WriteString(fmt.Sprintf("Security Analysis Results: %d findings\n\n", len(findings)))

	// Count by severity
	counts := make(map[string]int)
	for _, f := range findings {
		counts[f.Severity]++
	}

	sb.WriteString("Summary by Severity:\n")
	if c := counts[SeverityCritical]; c > 0 {
		sb.WriteString(fmt.Sprintf("  - Critical: %d\n", c))
	}
	if c := counts[SeverityHigh]; c > 0 {
		sb.WriteString(fmt.Sprintf("  - High: %d\n", c))
	}
	if c := counts[SeverityMedium]; c > 0 {
		sb.WriteString(fmt.Sprintf("  - Medium: %d\n", c))
	}
	if c := counts[SeverityLow]; c > 0 {
		sb.WriteString(fmt.Sprintf("  - Low: %d\n", c))
	}
	if c := counts[SeverityInfo]; c > 0 {
		sb.WriteString(fmt.Sprintf("  - Info: %d\n", c))
	}

	sb.WriteString("\nDetailed Findings:\n")
	sb.WriteString(strings.Repeat("-", 60) + "\n")

	for i, f := range findings {
		sb.WriteString(fmt.Sprintf("\n%d. ", i+1))
		sb.WriteString(FormatFinding(f))
		sb.WriteString(strings.Repeat("-", 60) + "\n")
	}

	return sb.String()
}

// AnalyzeCredentialReport analyzes a credential report for security issues
func AnalyzeCredentialReport(report *CredentialReport) []SecurityFinding {
	var findings []SecurityFinding

	now := time.Now()
	accessKeyRotationThreshold := 90 * 24 * time.Hour // 90 days

	for _, user := range report.Users {
		// Check root account usage
		if user.User == "<root_account>" {
			if user.AccessKey1Active || user.AccessKey2Active {
				findings = append(findings, SecurityFinding{
					ID:          GenerateFindingID(),
					Severity:    SeverityCritical,
					Type:        FindingRootAccountUsage,
					ResourceARN: user.ARN,
					Description: "Root account has active access keys",
					Remediation: "Delete root account access keys and use IAM users/roles instead",
				})
			}
			if !user.MFAActive {
				findings = append(findings, SecurityFinding{
					ID:          GenerateFindingID(),
					Severity:    SeverityCritical,
					Type:        FindingMissingMFA,
					ResourceARN: user.ARN,
					Description: "Root account does not have MFA enabled",
					Remediation: "Enable MFA on the root account immediately",
				})
			}
			continue
		}

		// Check MFA for console users
		if user.PasswordEnabled && !user.MFAActive {
			findings = append(findings, SecurityFinding{
				ID:          GenerateFindingID(),
				Severity:    SeverityHigh,
				Type:        FindingMissingMFA,
				ResourceARN: user.ARN,
				Description: fmt.Sprintf("User %s has console access but no MFA enabled", user.User),
				Remediation: "Enable MFA for all users with console access",
			})
		}

		// Check access key rotation
		if user.AccessKey1Active && user.AccessKey1LastRotated != nil {
			age := now.Sub(*user.AccessKey1LastRotated)
			if age > accessKeyRotationThreshold {
				findings = append(findings, SecurityFinding{
					ID:          GenerateFindingID(),
					Severity:    SeverityMedium,
					Type:        FindingOldAccessKeys,
					ResourceARN: user.ARN,
					Description: fmt.Sprintf("User %s has access key 1 that is %d days old", user.User, int(age.Hours()/24)),
					Remediation: "Rotate access keys regularly (recommended: every 90 days)",
				})
			}
		}

		if user.AccessKey2Active && user.AccessKey2LastRotated != nil {
			age := now.Sub(*user.AccessKey2LastRotated)
			if age > accessKeyRotationThreshold {
				findings = append(findings, SecurityFinding{
					ID:          GenerateFindingID(),
					Severity:    SeverityMedium,
					Type:        FindingOldAccessKeys,
					ResourceARN: user.ARN,
					Description: fmt.Sprintf("User %s has access key 2 that is %d days old", user.User, int(age.Hours()/24)),
					Remediation: "Rotate access keys regularly (recommended: every 90 days)",
				})
			}
		}

		// Check for inactive access keys
		if user.AccessKey1Active && user.AccessKey1LastUsedDate == nil {
			findings = append(findings, SecurityFinding{
				ID:          GenerateFindingID(),
				Severity:    SeverityLow,
				Type:        FindingInactiveKeys,
				ResourceARN: user.ARN,
				Description: fmt.Sprintf("User %s has active access key 1 that has never been used", user.User),
				Remediation: "Delete unused access keys to reduce security risk",
			})
		}

		if user.AccessKey2Active && user.AccessKey2LastUsedDate == nil {
			findings = append(findings, SecurityFinding{
				ID:          GenerateFindingID(),
				Severity:    SeverityLow,
				Type:        FindingInactiveKeys,
				ResourceARN: user.ARN,
				Description: fmt.Sprintf("User %s has active access key 2 that has never been used", user.User),
				Remediation: "Delete unused access keys to reduce security risk",
			})
		}
	}

	return findings
}

// GroupFindingsByResource groups findings by their resource ARN
func GroupFindingsByResource(findings []SecurityFinding) map[string][]SecurityFinding {
	grouped := make(map[string][]SecurityFinding)
	for _, f := range findings {
		grouped[f.ResourceARN] = append(grouped[f.ResourceARN], f)
	}
	return grouped
}

// GroupFindingsByType groups findings by their type
func GroupFindingsByType(findings []SecurityFinding) map[string][]SecurityFinding {
	grouped := make(map[string][]SecurityFinding)
	for _, f := range findings {
		grouped[f.Type] = append(grouped[f.Type], f)
	}
	return grouped
}

// GetOverallRisk determines the overall risk level based on findings
func GetOverallRisk(findings []SecurityFinding) string {
	if len(findings) == 0 {
		return SeverityInfo
	}

	maxSeverity := SeverityInfo
	maxScore := 0

	for _, f := range findings {
		score := SeverityScore(f.Severity)
		if score > maxScore {
			maxScore = score
			maxSeverity = f.Severity
		}
	}

	return maxSeverity
}
