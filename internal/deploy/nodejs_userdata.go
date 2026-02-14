package deploy

import (
	"fmt"
	"strings"
)

// GenerateNodeJSUserData creates EC2 user-data for native Node.js deployment.
// This handles any Node.js app: clones repo, installs deps, runs with PM2.
func GenerateNodeJSUserData(repoURL string, deep *DeepAnalysis, config *UserConfig) string {
	var b strings.Builder

	b.WriteString("#!/bin/bash\n")
	b.WriteString("set -ex\n")
	b.WriteString("exec > /var/log/user-data.log 2>&1\n\n")

	// Install Node.js (use detected version or default to LTS)
	nodeVersion := "22" // default to latest LTS
	if deep != nil && deep.NodeVersion != "" {
		nodeVersion = extractMajorVersion(deep.NodeVersion)
	}
	b.WriteString("# Install Node.js\n")
	b.WriteString(fmt.Sprintf("curl -fsSL https://rpm.nodesource.com/setup_%s.x | bash -\n", nodeVersion))
	b.WriteString("yum install -y nodejs git\n\n")

	// Install PM2 for process management
	b.WriteString("# Install PM2 for process management\n")
	b.WriteString("npm install -g pm2\n\n")

	// Clone the repository
	b.WriteString("# Clone application\n")
	b.WriteString("mkdir -p /opt/app\n")
	b.WriteString(fmt.Sprintf("git clone --depth 1 %s /opt/app\n", repoURL))
	b.WriteString("cd /opt/app\n\n")

	// Install dependencies
	b.WriteString("# Install dependencies\n")
	b.WriteString("npm install\n\n")

	// Run build command if needed
	if config.BuildCommand != "" {
		b.WriteString("# Build application\n")
		b.WriteString(fmt.Sprintf("%s\n\n", config.BuildCommand))
	}

	// Create environment file
	if len(config.EnvVars) > 0 {
		b.WriteString("# Create environment file\n")
		b.WriteString("cat > /opt/app/.env << 'ENVEOF'\n")
		for name, value := range config.EnvVars {
			// Escape special chars for heredoc
			escapedValue := strings.ReplaceAll(value, "'", "'\\''")
			b.WriteString(fmt.Sprintf("%s=%s\n", name, escapedValue))
		}
		b.WriteString("ENVEOF\n\n")
	}

	// Create PM2 ecosystem file
	b.WriteString("# Create PM2 config\n")
	b.WriteString("cat > /opt/app/ecosystem.config.js << 'PM2EOF'\n")
	b.WriteString("module.exports = {\n")
	b.WriteString("  apps: [{\n")
	b.WriteString("    name: 'app',\n")
	b.WriteString("    cwd: '/opt/app',\n")
	b.WriteString(fmt.Sprintf("    script: '%s',\n", getScriptFromStartCmd(config.StartCommand)))
	b.WriteString("    env: {\n")
	for name, value := range config.EnvVars {
		escapedValue := strings.ReplaceAll(value, "'", "\\'")
		escapedValue = strings.ReplaceAll(escapedValue, "\\", "\\\\")
		b.WriteString(fmt.Sprintf("      '%s': '%s',\n", name, escapedValue))
	}
	b.WriteString(fmt.Sprintf("      'PORT': '%d',\n", config.AppPort))
	b.WriteString("      'NODE_ENV': 'production',\n")
	b.WriteString("    },\n")
	b.WriteString("    instances: 1,\n")
	b.WriteString("    autorestart: true,\n")
	b.WriteString("    max_restarts: 10,\n")
	b.WriteString("    restart_delay: 5000,\n")
	b.WriteString("  }]\n")
	b.WriteString("};\n")
	b.WriteString("PM2EOF\n\n")

	// Start with PM2 and configure startup
	b.WriteString("# Start application with PM2\n")
	b.WriteString("cd /opt/app && pm2 start ecosystem.config.js\n")
	b.WriteString("pm2 save\n")
	b.WriteString("pm2 startup systemd -u root --hp /root\n\n")

	// Log completion
	b.WriteString("echo 'Deployment complete!'\n")

	return b.String()
}

// getScriptFromStartCmd extracts the script path from "npm start" or "node index.js"
func getScriptFromStartCmd(startCmd string) string {
	if strings.HasPrefix(startCmd, "node ") {
		return strings.TrimPrefix(startCmd, "node ")
	}
	// For npm start, PM2 can run npm directly
	return "npm -- start"
}

// extractMajorVersion extracts the major version from version strings like ">=22", "^18", "20.x"
func extractMajorVersion(version string) string {
	// Handle ">=22", "^18", "20.x", "18", etc.
	version = strings.TrimLeft(version, ">=^~")
	parts := strings.Split(version, ".")
	if len(parts) > 0 {
		return strings.TrimSuffix(parts[0], ".x")
	}
	return "22" // default
}
