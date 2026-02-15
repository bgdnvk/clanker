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
	ComposeServices     []string `json:"composeServices,omitempty"`
	ExposedPorts        []int    `json:"exposedPorts,omitempty"`
	PublishedPorts      []int    `json:"publishedPorts,omitempty"`
	PrimaryPort         int      `json:"primaryPort,omitempty"`
	HasHealthcheck      bool     `json:"hasHealthcheck"`
	HealthcheckHint     string   `json:"healthcheckHint,omitempty"`
	VolumeMounts        []string `json:"volumeMounts,omitempty"`
	EnvFiles            []string `json:"envFiles,omitempty"`
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
	if len(d.ComposeServices) > 0 {
		b.WriteString("- Compose services: " + strings.Join(d.ComposeServices, ", ") + "\n")
	}
	if len(d.ExposedPorts) > 0 {
		b.WriteString("- Dockerfile EXPOSE ports: " + intsToCSV(d.ExposedPorts) + "\n")
	}
	if len(d.PublishedPorts) > 0 {
		b.WriteString("- Compose container ports: " + intsToCSV(d.PublishedPorts) + "\n")
	}
	if d.PrimaryPort > 0 {
		b.WriteString(fmt.Sprintf("- Primary container port: %d\n", d.PrimaryPort))
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
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToUpper(trimmed), "FROM ") {
			fromCount++
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
}

func parseCompose(content string, analysis *DockerAnalysis) {
	if strings.TrimSpace(content) == "" || analysis == nil {
		return
	}
	portMapRe := regexp.MustCompile(`['"]?([0-9]{2,5}):([0-9]{2,5})['"]?`)
	serviceLineRe := regexp.MustCompile(`^\s{2}([a-zA-Z0-9_-]+):\s*$`)
	volumeLineRe := regexp.MustCompile(`^\s*-\s*([^\s#]+:[^\s#]+)\s*$`)
	envFileRe := regexp.MustCompile(`^\s*env_file\s*:\s*(.+)$`)

	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") || trimmed == "" {
			continue
		}
		if m := serviceLineRe.FindStringSubmatch(line); len(m) == 2 {
			analysis.ComposeServices = append(analysis.ComposeServices, strings.TrimSpace(m[1]))
		}
		if m := portMapRe.FindStringSubmatch(trimmed); len(m) == 3 {
			if containerPort := parsePort(m[2]); containerPort > 0 {
				analysis.PublishedPorts = append(analysis.PublishedPorts, containerPort)
			}
		}
		if m := volumeLineRe.FindStringSubmatch(line); len(m) == 2 {
			analysis.VolumeMounts = append(analysis.VolumeMounts, strings.TrimSpace(m[1]))
		}
		if m := envFileRe.FindStringSubmatch(line); len(m) == 2 {
			analysis.EnvFiles = append(analysis.EnvFiles, strings.Trim(strings.TrimSpace(m[1]), `"'`))
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
