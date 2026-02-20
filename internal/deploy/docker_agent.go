package deploy

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

type DockerAnalysis struct {
	HasDockerfile       bool     `json:"hasDockerfile"`
	HasCompose          bool     `json:"hasCompose"`
	BuildUsesMultiStage bool     `json:"buildUsesMultiStage"`
	HasPlatformPin      bool     `json:"hasPlatformPin"`
	PlatformPins        []string `json:"platformPins,omitempty"`
	ComposeServices     []string `json:"composeServices,omitempty"`
	ExposedPorts        []int    `json:"exposedPorts,omitempty"`
	PublishedPorts      []int    `json:"publishedPorts,omitempty"`
	PrimaryPort         int      `json:"primaryPort,omitempty"`
	HasHealthcheck      bool     `json:"hasHealthcheck"`
	HealthcheckHint     string   `json:"healthcheckHint,omitempty"`
	VolumeMounts        []string `json:"volumeMounts,omitempty"`
	EnvFiles            []string `json:"envFiles,omitempty"`
	ReferencedEnvVars   []string `json:"referencedEnvVars,omitempty"`
	HardRequiredEnvVars []string `json:"hardRequiredEnvVars,omitempty"`
	BuildCommand        string   `json:"buildCommand,omitempty"`
	RunCommand          string   `json:"runCommand,omitempty"`
	Warnings            []string `json:"warnings,omitempty"`
}

func AnalyzeDockerAgent(profile *RepoProfile) *DockerAnalysis {
	analysis := &DockerAnalysis{
		HasDockerfile: profile != nil && profile.HasDocker,
		HasCompose:    profile != nil && profile.HasCompose,
	}
	if profile == nil {
		analysis.Warnings = append(analysis.Warnings, "docker analysis skipped: missing profile")
		return analysis
	}

	if profile.HasDocker {
		parseDockerfile(profile.KeyFiles["Dockerfile"], analysis)
		if profile.KeyFiles["Dockerfile"] == "" {
			parseDockerfile(profile.KeyFiles["dockerfile"], analysis)
		}
	}

	if profile.HasCompose {
		composeText := firstNonEmpty(
			profile.KeyFiles["docker-compose.yml"],
			profile.KeyFiles["docker-compose.yaml"],
			profile.KeyFiles["compose.yml"],
			profile.KeyFiles["compose.yaml"],
		)
		parseCompose(composeText, analysis)
	}

	if analysis.PrimaryPort == 0 {
		if len(analysis.PublishedPorts) > 0 {
			analysis.PrimaryPort = analysis.PublishedPorts[0]
		} else if len(analysis.ExposedPorts) > 0 {
			analysis.PrimaryPort = analysis.ExposedPorts[0]
		} else if len(profile.Ports) > 0 {
			analysis.PrimaryPort = profile.Ports[0]
		}
	}

	if analysis.HasCompose {
		if len(analysis.ComposeServices) > 0 {
			analysis.BuildCommand = "docker compose build"
			analysis.RunCommand = "docker compose up -d " + choosePrimaryService(analysis.ComposeServices)
		} else {
			analysis.BuildCommand = "docker compose build"
			analysis.RunCommand = "docker compose up -d"
		}
	} else if analysis.HasDockerfile {
		analysis.BuildCommand = "docker build -t clanker-app ."
		if analysis.PrimaryPort > 0 {
			analysis.RunCommand = fmt.Sprintf("docker run -d -p %d:%d --name clanker-app clanker-app:latest", analysis.PrimaryPort, analysis.PrimaryPort)
		} else {
			analysis.RunCommand = "docker run -d --name clanker-app clanker-app:latest"
		}
	}

	if profile.HasDocker && len(analysis.ExposedPorts) == 0 {
		analysis.Warnings = append(analysis.Warnings, "Dockerfile has no EXPOSE; ensure runtime port mapping is explicit")
	}
	if profile.HasCompose && !analysis.HasHealthcheck {
		analysis.Warnings = append(analysis.Warnings, "docker-compose has no healthcheck; ALB/target warm-up may be slower")
	}
	if profile.HasDB && strings.EqualFold(profile.DBType, "sqlite") && len(analysis.VolumeMounts) == 0 {
		analysis.Warnings = append(analysis.Warnings, "sqlite detected but no compose volume mounts found; persistence risk")
	}

	sort.Ints(analysis.ExposedPorts)
	sort.Ints(analysis.PublishedPorts)
	analysis.ComposeServices = uniqueStrings(analysis.ComposeServices)
	analysis.VolumeMounts = uniqueStrings(analysis.VolumeMounts)
	analysis.EnvFiles = uniqueStrings(analysis.EnvFiles)
	analysis.ReferencedEnvVars = uniqueStrings(analysis.ReferencedEnvVars)
	analysis.HardRequiredEnvVars = uniqueStrings(analysis.HardRequiredEnvVars)
	analysis.Warnings = uniqueStrings(analysis.Warnings)

	return analysis
}

func (d *DockerAnalysis) FormatForPrompt() string {
	if d == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Docker Agent Findings\n")
	b.WriteString(fmt.Sprintf("- Dockerfile: %t\n", d.HasDockerfile))
	b.WriteString(fmt.Sprintf("- Compose: %t\n", d.HasCompose))
	if d.BuildUsesMultiStage {
		b.WriteString("- Build: multi-stage Dockerfile\n")
	}
	if d.HasPlatformPin {
		b.WriteString("- Dockerfile: has --platform pin\n")
		if len(d.PlatformPins) > 0 {
			b.WriteString("- Platform pins: " + strings.Join(d.PlatformPins, ", ") + "\n")
		}
	}
	if len(d.ComposeServices) > 0 {
		b.WriteString("- Compose services: " + strings.Join(d.ComposeServices, ", ") + "\n")
	}
	if len(d.ExposedPorts) > 0 {
		b.WriteString("- Dockerfile EXPOSE ports: " + intsToCSV(d.ExposedPorts) + "\n")
	}
	if len(d.PublishedPorts) > 0 {
		b.WriteString("- Compose published host ports: " + intsToCSV(d.PublishedPorts) + "\n")
	}
	if d.PrimaryPort > 0 {
		b.WriteString(fmt.Sprintf("- Primary published port: %d\n", d.PrimaryPort))
	}
	if d.BuildCommand != "" {
		b.WriteString("- Recommended build command: " + d.BuildCommand + "\n")
	}
	if d.RunCommand != "" {
		b.WriteString("- Recommended run command: " + d.RunCommand + "\n")
	}
	if d.HasHealthcheck {
		b.WriteString("- Healthcheck: present")
		if strings.TrimSpace(d.HealthcheckHint) != "" {
			b.WriteString(" (" + strings.TrimSpace(d.HealthcheckHint) + ")")
		}
		b.WriteString("\n")
	}
	if len(d.VolumeMounts) > 0 {
		b.WriteString("- Volume mounts: " + strings.Join(d.VolumeMounts, ", ") + "\n")
	}
	if len(d.EnvFiles) > 0 {
		b.WriteString("- Env files: " + strings.Join(d.EnvFiles, ", ") + "\n")
	}
	if len(d.HardRequiredEnvVars) > 0 {
		b.WriteString("- Hard-required env vars (compose fails if empty): " + strings.Join(d.HardRequiredEnvVars, ", ") + "\n")
	}
	if len(d.Warnings) > 0 {
		b.WriteString("- Docker warnings:\n")
		for _, warning := range d.Warnings {
			b.WriteString("  - " + warning + "\n")
		}
	}
	return b.String()
}

func parseDockerfile(content string, analysis *DockerAnalysis) {
	if strings.TrimSpace(content) == "" || analysis == nil {
		return
	}
	fromCount := 0
	exposeRe := regexp.MustCompile(`(?i)^\s*EXPOSE\s+([0-9]{2,5})`)
	platformRe := regexp.MustCompile(`(?i)^\s*FROM\s+--platform=([^\s]+)\s+`)
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToUpper(trimmed), "FROM ") {
			fromCount++
			if m := platformRe.FindStringSubmatch(trimmed); len(m) == 2 {
				analysis.HasPlatformPin = true
				analysis.PlatformPins = append(analysis.PlatformPins, strings.TrimSpace(m[1]))
			}
		}
		if m := exposeRe.FindStringSubmatch(trimmed); len(m) == 2 {
			if port := parsePort(m[1]); port > 0 {
				analysis.ExposedPorts = append(analysis.ExposedPorts, port)
			}
		}
		if strings.HasPrefix(strings.ToUpper(trimmed), "HEALTHCHECK") {
			analysis.HasHealthcheck = true
			analysis.HealthcheckHint = trimmed
		}
	}
	if fromCount > 1 {
		analysis.BuildUsesMultiStage = true
	}
	analysis.PlatformPins = uniqueStrings(analysis.PlatformPins)
}

func parseCompose(content string, analysis *DockerAnalysis) {
	if strings.TrimSpace(content) == "" || analysis == nil {
		return
	}
	portMapRe := regexp.MustCompile(`^\s*-?\s*['"]?([0-9]{2,5}):([0-9]{2,5})(?:/(?:tcp|udp))?['"]?\s*$`)
	// Handles compose ports with variable/templated host ports like "${FOO:-18789}:18789" (leading '-' allowed).
	varHostPortDefaultRe := regexp.MustCompile(`^\s*-?\s*['\"]?\$\{\s*([A-Za-z_][A-Za-z0-9_]*)\s*:-([0-9]{2,5})\}:([0-9]{2,5})(?:/(?:tcp|udp))?['\"]?\s*$`)
	// Handles compose ports with required host ports like "${FOO}:18789".
	varHostPortRequiredRe := regexp.MustCompile(`^\s*-?\s*['\"]?\$\{\s*([A-Za-z_][A-Za-z0-9_]*)\s*\}:([0-9]{2,5})(?:/(?:tcp|udp))?['\"]?\s*$`)
	serviceLineRe := regexp.MustCompile(`^\s{2}([a-zA-Z0-9_-]+):\s*$`)
	volumeLineRe := regexp.MustCompile(`^\s*-\s*([^\s#]+:[^\s#]+)\s*$`)
	envFileRe := regexp.MustCompile(`^\s*env_file\s*:\s*(.+)$`)
	envRefRe := regexp.MustCompile(`\$\{\s*([A-Za-z_][A-Za-z0-9_]*)`) // ${VAR} or ${VAR:-...}
	volumeHostRequiredVarRe := regexp.MustCompile(`^\$\{\s*([A-Za-z_][A-Za-z0-9_]*)\s*\}$`)

	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") || trimmed == "" {
			continue
		}
		if m := serviceLineRe.FindStringSubmatch(line); len(m) == 2 {
			analysis.ComposeServices = append(analysis.ComposeServices, strings.TrimSpace(m[1]))
		}
		// Prefer published HOST ports for deployment/health checks.
		if m := varHostPortDefaultRe.FindStringSubmatch(line); len(m) == 4 {
			analysis.ReferencedEnvVars = append(analysis.ReferencedEnvVars, strings.TrimSpace(m[1]))
			if hostPort := parsePort(m[2]); hostPort > 0 {
				analysis.PublishedPorts = append(analysis.PublishedPorts, hostPort)
			}
		} else if m := varHostPortRequiredRe.FindStringSubmatch(line); len(m) == 3 {
			analysis.ReferencedEnvVars = append(analysis.ReferencedEnvVars, strings.TrimSpace(m[1]))
			analysis.HardRequiredEnvVars = append(analysis.HardRequiredEnvVars, strings.TrimSpace(m[1]))
		} else if m := portMapRe.FindStringSubmatch(line); len(m) == 3 {
			if hostPort := parsePort(m[1]); hostPort > 0 {
				analysis.PublishedPorts = append(analysis.PublishedPorts, hostPort)
			}
		}
		if m := volumeLineRe.FindStringSubmatch(line); len(m) == 2 {
			// Avoid treating port mappings as volume mounts.
			if portMapRe.MatchString(line) || varHostPortDefaultRe.MatchString(line) || varHostPortRequiredRe.MatchString(line) {
				continue
			}
			mount := strings.TrimSpace(m[1])
			analysis.VolumeMounts = append(analysis.VolumeMounts, mount)
			parts := strings.SplitN(mount, ":", 2)
			if len(parts) == 2 {
				host := strings.TrimSpace(parts[0])
				if vm := volumeHostRequiredVarRe.FindStringSubmatch(host); len(vm) == 2 {
					analysis.ReferencedEnvVars = append(analysis.ReferencedEnvVars, strings.TrimSpace(vm[1]))
					analysis.HardRequiredEnvVars = append(analysis.HardRequiredEnvVars, strings.TrimSpace(vm[1]))
				}
			}
		}
		if m := envFileRe.FindStringSubmatch(line); len(m) == 2 {
			analysis.EnvFiles = append(analysis.EnvFiles, strings.Trim(strings.TrimSpace(m[1]), `"'`))
		}
		for _, m := range envRefRe.FindAllStringSubmatch(line, -1) {
			if len(m) == 2 {
				analysis.ReferencedEnvVars = append(analysis.ReferencedEnvVars, strings.TrimSpace(m[1]))
			}
		}
		if strings.HasPrefix(strings.ToLower(trimmed), "healthcheck:") {
			analysis.HasHealthcheck = true
			if analysis.HealthcheckHint == "" {
				analysis.HealthcheckHint = "compose healthcheck"
			}
		}
	}
}

func choosePrimaryService(services []string) string {
	if len(services) == 0 {
		return ""
	}
	// Strong preferences for common gateway naming.
	strong := []string{"openclaw-gateway", "gateway", "api", "server", "web", "app"}
	for _, want := range strong {
		for _, s := range services {
			if strings.EqualFold(strings.TrimSpace(s), want) {
				return s
			}
		}
	}
	// Prefix/suffix matches.
	for _, s := range services {
		ls := strings.ToLower(strings.TrimSpace(s))
		if strings.HasSuffix(ls, "-gateway") || strings.Contains(ls, "gateway") {
			return s
		}
	}
	preferred := []string{"gateway", "api", "app", "server", "web"}
	for _, p := range preferred {
		for _, s := range services {
			if strings.Contains(strings.ToLower(s), p) {
				return s
			}
		}
	}
	return services[0]
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func intsToCSV(values []int) string {
	if len(values) == 0 {
		return ""
	}
	parts := make([]string, 0, len(values))
	for _, v := range values {
		parts = append(parts, fmt.Sprintf("%d", v))
	}
	return strings.Join(parts, ", ")
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		key := strings.TrimSpace(value)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, key)
	}
	return result
}
