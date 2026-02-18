package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// InfraSnapshot is a snapshot of existing AWS infrastructure
type InfraSnapshot struct {
	AccountID                  string   `json:"accountId,omitempty"`
	Region                     string   `json:"region"`
	VPC                        *VPCInfo `json:"vpc,omitempty"`
	ECRRepos                   []string `json:"ecrRepos,omitempty"`                   // existing ECR repos
	CloudFrontDists            []string `json:"cloudFrontDists,omitempty"`            // existing CloudFront distribution domains
	LightsailInstances         []string `json:"lightsailInstances,omitempty"`         // existing Lightsail instances
	LightsailContainerServices []string `json:"lightsailContainerServices,omitempty"` // existing Lightsail container services
	LightsailDistributions     []string `json:"lightsailDistributions,omitempty"`     // existing Lightsail CDN distributions
	ECSClusters                []string `json:"ecsClusters,omitempty"`                // existing ECS clusters
	ALBs                       []string `json:"albs,omitempty"`                       // existing ALBs
	RDSInstances               []string `json:"rdsInstances,omitempty"`               // existing RDS instances
	SecurityGroups             []SGInfo `json:"securityGroups,omitempty"`             // existing SGs in default VPC
	LatestAMI                  string   `json:"latestAmi,omitempty"`                  // latest Amazon Linux 2023 AMI ID
	Summary                    string   `json:"summary"`
}

// VPCInfo is the default VPC + subnets info
type VPCInfo struct {
	VPCID     string   `json:"vpcId"`
	Subnets   []string `json:"subnets"` // subnet IDs
	IsDefault bool     `json:"isDefault"`
}

// SGInfo is a security group summary
type SGInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ScanInfra queries the AWS account to see what's already there.
// Uses the provided profile and region. Fails gracefully on permission errors.
func ScanInfra(ctx context.Context, profile, region string, logf func(string, ...any)) *InfraSnapshot {
	snap := &InfraSnapshot{Region: region}

	logf("[infra-scan] scanning existing infrastructure in %s/%s...", profile, region)

	// account ID
	if out := awsCLI(ctx, profile, region, "sts", "get-caller-identity", "--query", "Account", "--output", "text"); out != "" {
		snap.AccountID = strings.TrimSpace(out)
	}

	// default VPC
	if out := awsCLI(ctx, profile, region, "ec2", "describe-vpcs", "--filters", "Name=isDefault,Values=true", "--query", "Vpcs[0].VpcId", "--output", "text"); out != "" && out != "None" {
		vpcID := strings.TrimSpace(out)
		snap.VPC = &VPCInfo{VPCID: vpcID, IsDefault: true}

		// subnets in default VPC
		if subOut := awsCLI(ctx, profile, region, "ec2", "describe-subnets", "--filters", fmt.Sprintf("Name=vpc-id,Values=%s", vpcID), "--query", "Subnets[].SubnetId", "--output", "json"); subOut != "" {
			var subnets []string
			if err := json.Unmarshal([]byte(subOut), &subnets); err == nil {
				snap.VPC.Subnets = subnets
			}
		}

		// security groups in default VPC (just names/IDs)
		if sgOut := awsCLI(ctx, profile, region, "ec2", "describe-security-groups", "--filters", fmt.Sprintf("Name=vpc-id,Values=%s", vpcID), "--query", "SecurityGroups[].[GroupId,GroupName]", "--output", "json"); sgOut != "" {
			var sgs [][]string
			if err := json.Unmarshal([]byte(sgOut), &sgs); err == nil {
				for _, sg := range sgs {
					if len(sg) == 2 {
						snap.SecurityGroups = append(snap.SecurityGroups, SGInfo{ID: sg[0], Name: sg[1]})
					}
				}
			}
		}
	}

	// ECR repos
	if out := awsCLI(ctx, profile, region, "ecr", "describe-repositories", "--query", "repositories[].repositoryName", "--output", "json"); out != "" {
		var repos []string
		if err := json.Unmarshal([]byte(out), &repos); err == nil {
			snap.ECRRepos = repos
		}
	}

	// CloudFront distributions (global service; region flag is ignored by AWS CLI)
	if out := awsCLI(ctx, profile, region, "cloudfront", "list-distributions", "--query", "DistributionList.Items[].DomainName", "--output", "json"); out != "" {
		var domains []string
		if err := json.Unmarshal([]byte(out), &domains); err == nil {
			snap.CloudFrontDists = capStrings(domains, 25)
		}
	}

	// Lightsail resources (regional)
	if out := awsCLI(ctx, profile, region, "lightsail", "get-instances", "--query", "instances[].name", "--output", "json"); out != "" {
		var names []string
		if err := json.Unmarshal([]byte(out), &names); err == nil {
			snap.LightsailInstances = capStrings(names, 50)
		}
	}
	if out := awsCLI(ctx, profile, region, "lightsail", "get-container-services", "--query", "containerServices[].containerServiceName", "--output", "json"); out != "" {
		var names []string
		if err := json.Unmarshal([]byte(out), &names); err == nil {
			snap.LightsailContainerServices = capStrings(names, 50)
		}
	}
	if out := awsCLI(ctx, profile, region, "lightsail", "get-distributions", "--query", "distributions[].name", "--output", "json"); out != "" {
		var names []string
		if err := json.Unmarshal([]byte(out), &names); err == nil {
			snap.LightsailDistributions = capStrings(names, 50)
		}
	}

	// ECS clusters
	if out := awsCLI(ctx, profile, region, "ecs", "list-clusters", "--query", "clusterArns", "--output", "json"); out != "" {
		var arns []string
		if err := json.Unmarshal([]byte(out), &arns); err == nil {
			for _, arn := range arns {
				// extract cluster name from ARN
				parts := strings.Split(arn, "/")
				if len(parts) > 0 {
					snap.ECSClusters = append(snap.ECSClusters, parts[len(parts)-1])
				}
			}
		}
	}

	// ALBs
	if out := awsCLI(ctx, profile, region, "elbv2", "describe-load-balancers", "--query", "LoadBalancers[].LoadBalancerName", "--output", "json"); out != "" {
		var albs []string
		if err := json.Unmarshal([]byte(out), &albs); err == nil {
			snap.ALBs = albs
		}
	}

	// RDS instances
	if out := awsCLI(ctx, profile, region, "rds", "describe-db-instances", "--query", "DBInstances[].DBInstanceIdentifier", "--output", "json"); out != "" {
		var dbs []string
		if err := json.Unmarshal([]byte(out), &dbs); err == nil {
			snap.RDSInstances = dbs
		}
	}

	// Latest Amazon Linux 2023 AMI (for EC2 deployments)
	if out := awsCLI(ctx, profile, region, "ssm", "get-parameters", "--names", "/aws/service/ami-amazon-linux-latest/al2023-ami-kernel-6.1-x86_64", "--query", "Parameters[0].Value", "--output", "text"); out != "" && out != "None" {
		snap.LatestAMI = strings.TrimSpace(out)
	}

	snap.Summary = buildInfraSummary(snap)
	logf("[infra-scan] %s", snap.Summary)

	return snap
}

func awsCLI(ctx context.Context, profile, region string, args ...string) string {
	fullArgs := append([]string{"--profile", profile, "--region", region}, args...)
	cmd := exec.CommandContext(ctx, "aws", fullArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func buildInfraSummary(s *InfraSnapshot) string {
	parts := []string{}

	if s.AccountID != "" {
		parts = append(parts, fmt.Sprintf("account: %s", s.AccountID))
	}
	if s.VPC != nil {
		parts = append(parts, fmt.Sprintf("VPC: %s (%d subnets)", s.VPC.VPCID, len(s.VPC.Subnets)))
	} else {
		parts = append(parts, "no default VPC found")
	}
	if len(s.ECRRepos) > 0 {
		parts = append(parts, fmt.Sprintf("%d ECR repos", len(s.ECRRepos)))
	}
	if len(s.CloudFrontDists) > 0 {
		parts = append(parts, fmt.Sprintf("%d CloudFront dists", len(s.CloudFrontDists)))
	}
	if len(s.LightsailInstances) > 0 {
		parts = append(parts, fmt.Sprintf("%d Lightsail instances", len(s.LightsailInstances)))
	}
	if len(s.LightsailContainerServices) > 0 {
		parts = append(parts, fmt.Sprintf("%d Lightsail container svcs", len(s.LightsailContainerServices)))
	}
	if len(s.LightsailDistributions) > 0 {
		parts = append(parts, fmt.Sprintf("%d Lightsail dists", len(s.LightsailDistributions)))
	}
	if len(s.ECSClusters) > 0 {
		parts = append(parts, fmt.Sprintf("%d ECS clusters", len(s.ECSClusters)))
	}
	if len(s.ALBs) > 0 {
		parts = append(parts, fmt.Sprintf("%d ALBs", len(s.ALBs)))
	}
	if len(s.RDSInstances) > 0 {
		parts = append(parts, fmt.Sprintf("%d RDS instances", len(s.RDSInstances)))
	}

	if len(parts) == 0 {
		return "no existing infrastructure detected"
	}
	return strings.Join(parts, " • ")
}

// FormatForPrompt formats the infra snapshot as text for the LLM prompt
func (s *InfraSnapshot) FormatForPrompt() string {
	if s == nil {
		return ""
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("- Region: %s\n", s.Region))

	if s.AccountID != "" {
		b.WriteString(fmt.Sprintf("- Account: %s\n", s.AccountID))
	}

	if s.VPC != nil {
		b.WriteString(fmt.Sprintf("- Default VPC: %s\n", s.VPC.VPCID))
		if len(s.VPC.Subnets) > 0 {
			b.WriteString(fmt.Sprintf("- Subnets: %s\n", strings.Join(s.VPC.Subnets, ", ")))
			b.WriteString("  → REUSE these subnets, do NOT create new ones\n")
		}
	}

	if len(s.ECRRepos) > 0 {
		b.WriteString(fmt.Sprintf("- Existing ECR repos: %s\n", strings.Join(s.ECRRepos, ", ")))
		b.WriteString("  → REUSE existing repo if name matches, don't create duplicates\n")
	}

	if len(s.CloudFrontDists) > 0 {
		b.WriteString(fmt.Sprintf("- Existing CloudFront distributions: %s\n", strings.Join(s.CloudFrontDists, ", ")))
		b.WriteString("  → If you create CloudFront, ensure it is uniquely identifiable (e.g., comment includes DEPLOY_ID); avoid accidental reuse\n")
	}

	if len(s.LightsailInstances) > 0 {
		b.WriteString(fmt.Sprintf("- Existing Lightsail instances: %s\n", strings.Join(s.LightsailInstances, ", ")))
		b.WriteString("  → Avoid creating duplicates; reuse where it makes sense\n")
	}
	if len(s.LightsailContainerServices) > 0 {
		b.WriteString(fmt.Sprintf("- Existing Lightsail container services: %s\n", strings.Join(s.LightsailContainerServices, ", ")))
		b.WriteString("  → Avoid creating duplicates; reuse where it makes sense\n")
	}
	if len(s.LightsailDistributions) > 0 {
		b.WriteString(fmt.Sprintf("- Existing Lightsail distributions: %s\n", strings.Join(s.LightsailDistributions, ", ")))
	}

	if len(s.ECSClusters) > 0 {
		b.WriteString(fmt.Sprintf("- Existing ECS clusters: %s\n", strings.Join(s.ECSClusters, ", ")))
		b.WriteString("  → Consider REUSING an existing cluster instead of creating a new one\n")
	}

	if len(s.SecurityGroups) > 0 {
		sgNames := make([]string, 0, len(s.SecurityGroups))
		for _, sg := range s.SecurityGroups {
			sgNames = append(sgNames, fmt.Sprintf("%s (%s)", sg.Name, sg.ID))
		}
		b.WriteString(fmt.Sprintf("- Security groups: %s\n", strings.Join(sgNames, ", ")))
	}

	if len(s.RDSInstances) > 0 {
		b.WriteString(fmt.Sprintf("- Existing RDS instances: %s\n", strings.Join(s.RDSInstances, ", ")))
		b.WriteString("  → Consider reusing if compatible\n")
	}

	if s.LatestAMI != "" {
		b.WriteString(fmt.Sprintf("- Latest Amazon Linux 2023 AMI: %s\n", s.LatestAMI))
		b.WriteString("  → Use this AMI ID directly for EC2 instances (no need to query SSM)\n")
	}

	return b.String()
}

func capStrings(in []string, max int) []string {
	if max <= 0 || len(in) <= max {
		return in
	}
	return in[:max]
}
