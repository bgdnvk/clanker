package deploy

import (
	"fmt"
	"strings"
)

func IsWordPressRepo(p *RepoProfile, deep *DeepAnalysis) bool {
	if p == nil {
		return false
	}

	// URL-based: docker-library/wordpress or wordpress in repo name
	repo := strings.ToLower(strings.TrimSpace(p.RepoURL))
	if strings.Contains(repo, "docker-library/wordpress") {
		return true
	}
	if strings.Contains(repo, "/wordpress") && !strings.Contains(repo, "openclaw") {
		return true
	}

	// Content-based: wp-config.php or wp-content in key files / file tree
	for name := range p.KeyFiles {
		lower := strings.ToLower(name)
		if lower == "wp-config.php" || lower == "wp-config-sample.php" {
			return true
		}
	}
	tree := strings.ToLower(p.FileTree)
	if strings.Contains(tree, "wp-config.php") || strings.Contains(tree, "wp-content/") {
		return true
	}

	// Language hint: PHP + framework=wordpress
	if strings.ToLower(p.Language) == "php" {
		if strings.Contains(strings.ToLower(p.Framework), "wordpress") {
			return true
		}
	}

	// Deep analysis hint
	if deep != nil {
		desc := strings.ToLower(deep.AppDescription)
		if strings.Contains(desc, "wordpress") {
			return true
		}
	}

	return false
}

func ApplyWordPressArchitectureDefaults(targetProvider string, opts *DeployOptions, p *RepoProfile, deep *DeepAnalysis, arch *ArchitectDecision) bool {
	_ = opts
	if arch == nil {
		return false
	}
	if !IsWordPressRepo(p, deep) {
		return false
	}

	provider := strings.ToLower(strings.TrimSpace(targetProvider))
	if provider == "" {
		provider = "aws"
	}
	if provider != "aws" {
		return false
	}

	arch.Provider = "aws"
	arch.Method = "ec2"
	arch.NeedsALB = true
	arch.UseAPIGateway = false
	arch.Reasoning = "WordPress one-click deploy: run wordpress + mariadb (Docker Hub images) on EC2 and expose via an ALB (health check /wp-login.php); persist DB + wp-content via Docker volumes"
	return true
}

func AppendWordPressDeploymentRequirements(b *strings.Builder, p *RepoProfile, deep *DeepAnalysis) bool {
	if b == nil {
		return false
	}
	if !IsWordPressRepo(p, deep) {
		return false
	}

	b.WriteString("\n## WordPress One-Click Requirements\n")
	b.WriteString("- Deploy AWS EC2 + ALB (HTTP)\n")
	b.WriteString("- Run Docker Hub images: mariadb + wordpress\n")
	b.WriteString("- Expose WordPress on port 80\n")
	b.WriteString("- ALB target group health check path: /wp-login.php\n")
	b.WriteString("- Require env var WORDPRESS_DB_PASSWORD (user provided)\n")
	b.WriteString("- Persist DB + wp-content using Docker volumes\n")
	b.WriteString("- Do NOT write WORDPRESS_DB_PASSWORD to SSM Parameter Store\n")
	return true
}

// WordPressEC2Prompt returns a detailed EC2 deployment prompt for WordPress.
// Unlike generic EC2, WordPress pulls images from Docker Hub (no ECR).
func WordPressEC2Prompt(p *RepoProfile, opts *DeployOptions) string {
	var b strings.Builder
	deployID := ""
	if opts != nil {
		deployID = opts.DeployID
	}
	resourcePrefix := repoResourcePrefix(p.RepoURL, deployID)
	projectTag := resourcePrefix

	roleName := awsName(resourcePrefix, "-ec2-role", 64)
	profileName := awsName(resourcePrefix, "-ec2-profile", 128)
	albSGName := awsName(resourcePrefix, "-alb-sg", 255)
	ec2SGName := awsName(resourcePrefix, "-ec2-sg", 255)
	instanceName := awsName(resourcePrefix, "", 128)
	albName := awsName(resourcePrefix, "-alb", 32)
	tgName := awsName(resourcePrefix, "-tg", 32)
	instanceType := "t3.small"
	if opts != nil && opts.InstanceType != "" {
		instanceType = opts.InstanceType
	}

	b.WriteString(fmt.Sprintf("Deploy WordPress to EC2 (%s) using Docker Hub images (NO ECR build needed).\n\n", instanceType))
	b.WriteString("IMPORTANT: WordPress uses pre-built Docker Hub images. Do NOT create an ECR repo or build locally.\n\n")

	// VPC (use default)
	b.WriteString("## VPC\n")
	b.WriteString("Discover the default VPC and its subnets:\n")
	b.WriteString("   aws ec2 describe-vpcs --filters Name=isDefault,Values=true\n")
	b.WriteString("   aws ec2 describe-subnets --filters Name=vpc-id,Values=<VPC_ID>\n\n")

	// Security Groups
	b.WriteString("## Security Groups\n")
	b.WriteString(fmt.Sprintf("1. ALB SG (%s): allow TCP 80 from 0.0.0.0/0\n", albSGName))
	b.WriteString(fmt.Sprintf("2. EC2 SG (%s): allow TCP 80 from ALB SG only; no SSH open to internet\n\n", ec2SGName))

	// IAM
	b.WriteString("## IAM Role + Instance Profile\n")
	b.WriteString(fmt.Sprintf("Create role %s with ec2 assume-role trust, attach AmazonSSMManagedInstanceCore.\n", roleName))
	b.WriteString(fmt.Sprintf("Create instance profile %s and add the role.\n\n", profileName))

	// EC2 launch
	b.WriteString("## Launch EC2\n")
	b.WriteString("Get AMI: aws ssm get-parameters --names /aws/service/ami-amazon-linux-latest/al2023-ami-kernel-6.1-x86_64\n\n")
	b.WriteString("Run instance with user-data that:\n")
	b.WriteString("  1. Installs Docker + Docker Compose plugin\n")
	b.WriteString("  2. Creates /opt/wordpress with a docker-compose.yml:\n")
	b.WriteString("     services:\n")
	b.WriteString("       db:\n")
	b.WriteString("         image: mariadb:latest\n")
	b.WriteString("         environment:\n")
	b.WriteString("           MYSQL_ROOT_PASSWORD: <WORDPRESS_DB_PASSWORD>\n")
	b.WriteString("           MYSQL_DATABASE: wordpress\n")
	b.WriteString("         volumes: [db_data:/var/lib/mysql]\n")
	b.WriteString("       wordpress:\n")
	b.WriteString("         image: wordpress:latest\n")
	b.WriteString("         ports: ['80:80']\n")
	b.WriteString("         environment:\n")
	b.WriteString("           WORDPRESS_DB_HOST: db\n")
	b.WriteString("           WORDPRESS_DB_PASSWORD: <WORDPRESS_DB_PASSWORD>\n")
	b.WriteString("         volumes: [wp_content:/var/www/html]\n")
	b.WriteString("         depends_on: [db]\n")
	b.WriteString("     volumes:\n")
	b.WriteString("       db_data:\n")
	b.WriteString("       wp_content:\n")
	b.WriteString("  3. Runs docker compose up -d\n\n")

	b.WriteString(fmt.Sprintf("   aws ec2 run-instances --instance-type %s --image-id <AMI_ID> ", instanceType))
	b.WriteString(fmt.Sprintf("--subnet-id <SUBNET_1A_ID> --security-group-ids <EC2_SG_ID> --iam-instance-profile Name=%s ", profileName))
	b.WriteString(fmt.Sprintf("--tag-specifications 'ResourceType=instance,Tags=[{Key=Name,Value=%s},{Key=Project,Value=%s}]' ", instanceName, projectTag))
	b.WriteString("--user-data <USER_DATA>\n")
	b.WriteString("produces: {\"INSTANCE_ID\": \"$.Instances[0].InstanceId\"}\n\n")

	b.WriteString("Wait: aws ec2 wait instance-running --instance-ids <INSTANCE_ID>\n\n")

	// ALB
	b.WriteString("## ALB\n")
	b.WriteString(fmt.Sprintf("1. Create ALB %s (elbv2), subnets <SUBNET_1A_ID> <SUBNET_1B_ID>, SG <ALB_SG_ID>\n", albName))
	b.WriteString(fmt.Sprintf("2. Create target group %s: HTTP port 80, health-check-path /wp-login.php\n", tgName))
	b.WriteString("3. Register instance <INSTANCE_ID> in target group\n")
	b.WriteString("4. Create listener: HTTP 80 forward to target group\n")
	b.WriteString("5. Wait for target healthy\n\n")

	b.WriteString("## Output\n")
	b.WriteString("WordPress accessible at ALB DNS: http://<ALB_DNS>\n")

	return b.String()
}
