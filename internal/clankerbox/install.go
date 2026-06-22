package clankerbox

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type InstallResult struct {
	Agent     string `json:"agent"`
	Installed bool   `json:"installed"`
	Command   string `json:"command,omitempty"`
	Version   string `json:"version,omitempty"`
	Output    string `json:"output,omitempty"`
}

func InstallAgent(ctx context.Context, rawAgent string) (InstallResult, error) {
	agent, ok := AgentByID(rawAgent)
	if !ok {
		return InstallResult{}, fmt.Errorf("unsupported agent %q", strings.TrimSpace(rawAgent))
	}
	return installAgent(ctx, agent.ID)
}

func InstallAllAgents(ctx context.Context) ([]InstallResult, error) {
	results := make([]InstallResult, 0, len(Agents()))
	for _, agent := range Agents() {
		result, err := installAgent(ctx, agent.ID)
		results = append(results, result)
		if err != nil {
			return results, err
		}
	}
	return results, nil
}

func ensureAgentInstalled(ctx context.Context, agentID string) error {
	if !parseBoolEnv("CLANKER_BOX_AUTO_INSTALL", true) {
		return nil
	}
	_, err := installAgent(ctx, agentID)
	return err
}

func installAgent(parent context.Context, agentID string) (InstallResult, error) {
	ctx, cancel := context.WithTimeout(parent, installTimeout())
	defer cancel()
	switch normalizeID(agentID) {
	case "empty":
		return InstallResult{Agent: "empty", Installed: true, Command: "sh", Version: "base-shell"}, nil
	case "clanker-cli":
		return installClankerCLI(ctx)
	case "codex":
		return installCodex(ctx)
	case "claude-code":
		return installClaudeCode(ctx)
	case "openclaw":
		return installOpenClaw(ctx)
	case "hermes":
		return installHermes(ctx)
	default:
		return InstallResult{}, fmt.Errorf("unsupported agent %q", agentID)
	}
}

func installClankerCLI(ctx context.Context) (InstallResult, error) {
	return ensureCommand(ctx, "clanker-cli", "clanker", []string{executable(), "--version"}, "")
}

func installCodex(ctx context.Context) (InstallResult, error) {
	return ensureCommand(ctx, "codex", "codex", []string{"codex", "--version"}, "npm install -g @openai/codex@latest")
}

func installClaudeCode(ctx context.Context) (InstallResult, error) {
	return ensureCommand(ctx, "claude-code", "claude", []string{"claude", "--version"}, "npm install -g @anthropic-ai/claude-code@latest")
}

func installOpenClaw(ctx context.Context) (InstallResult, error) {
	return ensureCommand(ctx, "openclaw", "openclaw", []string{"openclaw", "--version"}, "npm install -g openclaw@latest")
}

func installHermes(ctx context.Context) (InstallResult, error) {
	if path, err := findHermesVendorPath(); err == nil && path != "" {
		return InstallResult{Agent: "hermes", Installed: true, Command: "vendor/hermes-agent", Version: path}, nil
	}
	workdir := terminalWorkingDir("")
	vendorDir := filepath.Join(workdir, "vendor", "hermes-agent")
	cmd := fmt.Sprintf(
		`set -e; mkdir -p %q; if [ -d %q/.git ]; then git -C %q pull --recurse-submodules; git -C %q submodule update --init --recursive; else git clone --recurse-submodules https://github.com/NousResearch/hermes-agent.git %q; fi; cd %q; python3 -m venv .venv; .venv/bin/python -m pip install --upgrade pip wheel setuptools; .venv/bin/python -m pip install -e '.[all]'; if [ -d mini-swe-agent ] && [ -f mini-swe-agent/pyproject.toml ]; then .venv/bin/python -m pip install -e ./mini-swe-agent; fi; mkdir -p "$HOME/.local/bin"; if [ -x .venv/bin/hermes ]; then ln -sf %q "$HOME/.local/bin/hermes"; fi`,
		filepath.Dir(vendorDir),
		vendorDir,
		vendorDir,
		vendorDir,
		vendorDir,
		vendorDir,
		filepath.Join(vendorDir, ".venv", "bin", "hermes"),
	)
	output, err := runShell(ctx, cmd)
	result := InstallResult{Agent: "hermes", Command: "git clone https://github.com/NousResearch/hermes-agent.git", Output: output}
	if err != nil {
		return result, err
	}
	result.Installed = true
	result.Version = vendorDir
	return result, nil
}

func ensureCommand(ctx context.Context, agentID, binary string, versionCmd []string, installCmd string) (InstallResult, error) {
	if path, err := exec.LookPath(binary); err == nil && path != "" {
		version := ""
		if len(versionCmd) > 0 {
			out, _ := exec.CommandContext(ctx, versionCmd[0], versionCmd[1:]...).CombinedOutput()
			version = strings.TrimSpace(string(out))
		}
		return InstallResult{Agent: agentID, Installed: true, Command: path, Version: version}, nil
	}
	if strings.TrimSpace(installCmd) == "" {
		return InstallResult{Agent: agentID, Installed: false}, fmt.Errorf("%s is not installed", binary)
	}
	output, err := runShell(ctx, installCmd)
	result := InstallResult{Agent: agentID, Command: installCmd, Output: output}
	if err != nil {
		return result, err
	}
	if path, err := exec.LookPath(binary); err == nil && path != "" {
		result.Installed = true
		result.Command = path
		if len(versionCmd) > 0 {
			out, _ := exec.CommandContext(ctx, versionCmd[0], versionCmd[1:]...).CombinedOutput()
			result.Version = strings.TrimSpace(string(out))
		}
		return result, nil
	}
	return result, fmt.Errorf("%s install completed but binary was not found in PATH", binary)
}

func runShell(ctx context.Context, command string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "sh", "-lc", command)
	cmd.Dir = terminalWorkingDir("")
	cmd.Env = os.Environ()
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	output := strings.TrimSpace(stdout.String() + "\n" + stderr.String())
	if err != nil {
		return trimTerminalOutput(output), fmt.Errorf("install command failed: %w: %s", err, trimTerminalOutput(output))
	}
	return trimTerminalOutput(output), nil
}

func installTimeout() time.Duration {
	raw := strings.TrimSpace(os.Getenv("CLANKER_BOX_INSTALL_TIMEOUT_SECONDS"))
	if raw == "" {
		return 15 * time.Minute
	}
	duration, err := time.ParseDuration(raw)
	if err == nil && duration > 0 {
		return duration
	}
	duration, err = time.ParseDuration(raw + "s")
	if err == nil && duration > 0 {
		return duration
	}
	return 15 * time.Minute
}

func findHermesVendorPath() (string, error) {
	if p := strings.TrimSpace(os.Getenv("CLANKER_BOX_HERMES_PATH")); p != "" && isHermesDir(p) {
		return p, nil
	}
	candidate := filepath.Join(terminalWorkingDir(""), "vendor", "hermes-agent")
	if isHermesDir(candidate) {
		return candidate, nil
	}
	return "", fmt.Errorf("hermes vendor install not found")
}

func isHermesDir(path string) bool {
	info, err := os.Stat(filepath.Join(path, ".venv", "bin", "python"))
	return err == nil && !info.IsDir()
}
