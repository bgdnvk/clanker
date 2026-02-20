package deploy

import (
	"regexp"
	"sort"
	"strings"
)

type PreflightReport struct {
	PackageManager     string   `json:"packageManager"`
	IsMonorepo         bool     `json:"isMonorepo"`
	LockFiles          []string `json:"lockFiles,omitempty"`
	BootstrapScripts   []string `json:"bootstrapScripts,omitempty"`
	EnvExampleFiles    []string `json:"envExampleFiles,omitempty"`
	MigrationHints     []string `json:"migrationHints,omitempty"`
	NativeDeps         []string `json:"nativeDeps,omitempty"`
	BuildOutputDir     string   `json:"buildOutputDir,omitempty"`
	IsStaticSite       bool     `json:"isStaticSite"`
	ComposeHardEnvVars []string `json:"composeHardEnvVars,omitempty"`
	Warnings           []string `json:"warnings,omitempty"`
}

func BuildPreflightReport(p *RepoProfile, docker *DockerAnalysis, deep *DeepAnalysis) *PreflightReport {
	if p == nil {
		return &PreflightReport{}
	}
	r := &PreflightReport{
		PackageManager:   strings.TrimSpace(p.PackageManager),
		IsMonorepo:       p.IsMonorepo,
		LockFiles:        append([]string(nil), p.LockFiles...),
		BootstrapScripts: append([]string(nil), p.BootstrapScripts...),
		EnvExampleFiles:  append([]string(nil), p.EnvExampleFiles...),
		MigrationHints:   append([]string(nil), p.MigrationHints...),
		NativeDeps:       append([]string(nil), p.NativeDeps...),
		BuildOutputDir:   strings.TrimSpace(p.BuildOutputDir),
		IsStaticSite:     p.IsStaticSite,
	}
	if docker != nil {
		r.ComposeHardEnvVars = append([]string(nil), docker.HardRequiredEnvVars...)
	}

	// Normalize
	sort.Strings(r.LockFiles)
	sort.Strings(r.BootstrapScripts)
	sort.Strings(r.EnvExampleFiles)
	sort.Strings(r.MigrationHints)
	sort.Strings(r.NativeDeps)
	sort.Strings(r.ComposeHardEnvVars)

	// Warnings
	if r.IsMonorepo {
		r.Warnings = append(r.Warnings, "monorepo detected: run install/build commands in the correct package directory")
	}
	if r.PackageManager != "" && len(r.LockFiles) > 0 {
		r.Warnings = append(r.Warnings, "lockfiles present: do not mix package managers")
	}
	if len(r.NativeDeps) > 0 {
		r.Warnings = append(r.Warnings, "native deps detected: Docker/VM may need build tools and system libraries")
	}
	if len(r.MigrationHints) > 0 {
		r.Warnings = append(r.Warnings, "migration tooling detected: ensure migrations run before starting the app")
	}
	if deep != nil && strings.TrimSpace(deep.HealthEndpoint) == "" {
		// Only a warning; some apps use / or TCP.
		r.Warnings = append(r.Warnings, "no health endpoint detected: ALB health check may need to use / or TCP")
	}

	return r
}

func (r *PreflightReport) FormatForPrompt() string {
	if r == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Preflight (Static checks)\n")
	if r.PackageManager != "" {
		b.WriteString("- Package manager: " + r.PackageManager + "\n")
	}
	if r.IsMonorepo {
		b.WriteString("- Monorepo: yes\n")
	}
	if r.IsStaticSite {
		b.WriteString("- Static site: likely yes\n")
		if r.BuildOutputDir != "" {
			b.WriteString("- Static build output dir: " + r.BuildOutputDir + "\n")
		}
	}
	if len(r.LockFiles) > 0 {
		b.WriteString("- Lockfiles: " + strings.Join(r.LockFiles, ", ") + "\n")
	}
	if len(r.BootstrapScripts) > 0 {
		b.WriteString("- Bootstrap scripts: " + strings.Join(r.BootstrapScripts, ", ") + "\n")
	}
	if len(r.EnvExampleFiles) > 0 {
		b.WriteString("- Env example files: " + strings.Join(r.EnvExampleFiles, ", ") + "\n")
	}
	if len(r.ComposeHardEnvVars) > 0 {
		b.WriteString("- Compose hard-required env: " + strings.Join(r.ComposeHardEnvVars, ", ") + "\n")
	}
	if len(r.NativeDeps) > 0 {
		b.WriteString("- Native deps: " + strings.Join(r.NativeDeps, ", ") + "\n")
	}
	if len(r.MigrationHints) > 0 {
		b.WriteString("- Migrations: " + strings.Join(r.MigrationHints, ", ") + "\n")
	}
	if len(r.Warnings) > 0 {
		b.WriteString("- Warnings:\n")
		for _, w := range r.Warnings {
			b.WriteString("  - " + w + "\n")
		}
	}
	return b.String()
}

var (
	secretLikeRe = regexp.MustCompile(`(?i)(AKIA[0-9A-Z]{16}|ASIA[0-9A-Z]{16}|sk-[A-Za-z0-9]{10,}|-----BEGIN [A-Z ]+ PRIVATE KEY-----)`)
)

func containsSecretLikeText(s string) bool {
	if strings.TrimSpace(s) == "" {
		return false
	}
	return secretLikeRe.FindStringIndex(s) != nil
}
