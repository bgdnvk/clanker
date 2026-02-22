package deploy

import "strings"

func IsWordPressRepo(p *RepoProfile, deep *DeepAnalysis) bool {
	_ = deep
	if p == nil {
		return false
	}
	repo := strings.ToLower(strings.TrimSpace(p.RepoURL))
	return strings.Contains(repo, "github.com/docker-library/wordpress") || strings.Contains(repo, "docker-library/wordpress")
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
