package deploy

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// UserConfig holds user-provided deployment configuration
type UserConfig struct {
	EnvVars      map[string]string // all env vars (required + optional with values)
	AppPort      int               // confirmed listening port
	DeployMode   string            // docker or native
	StartCommand string            // confirmed start command
	BuildCommand string            // build command if needed
}

// PromptForConfig collects required configuration from user before deployment
func PromptForConfig(deep *DeepAnalysis, profile *RepoProfile) (*UserConfig, error) {
	config := &UserConfig{
		EnvVars:      make(map[string]string),
		AppPort:      deep.ListeningPort,
		StartCommand: deep.StartCommand,
		BuildCommand: deep.BuildCommand,
	}

	// Default port if not detected
	if config.AppPort == 0 {
		config.AppPort = 3000
	}
	if config.StartCommand == "" {
		config.StartCommand = "npm start"
	}

	reader := bufio.NewReader(os.Stdin)

	// Show what was detected
	fmt.Fprintf(os.Stderr, "\n[deploy] Detected Node.js application:\n")
	fmt.Fprintf(os.Stderr, "  Port: %d\n", config.AppPort)
	fmt.Fprintf(os.Stderr, "  Start: %s\n", config.StartCommand)
	if deep.BuildCommand != "" {
		fmt.Fprintf(os.Stderr, "  Build: %s\n", deep.BuildCommand)
	}
	if deep.NodeVersion != "" {
		fmt.Fprintf(os.Stderr, "  Node: %s\n", deep.NodeVersion)
	}
	if deep.ExposesHTTP {
		fmt.Fprintf(os.Stderr, "  Type: HTTP server\n")
	} else {
		fmt.Fprintf(os.Stderr, "  Type: Non-HTTP (WebSocket/worker/CLI)\n")
	}

	// Prompt for required env vars
	if len(deep.RequiredEnvVars) > 0 {
		fmt.Fprintf(os.Stderr, "\n[deploy] Required configuration:\n")
		for _, env := range deep.RequiredEnvVars {
			fmt.Fprintf(os.Stderr, "\n  %s", env.Name)
			if env.Description != "" {
				fmt.Fprintf(os.Stderr, " - %s", env.Description)
			}
			fmt.Fprintf(os.Stderr, "\n")
			if env.Example != "" {
				fmt.Fprintf(os.Stderr, "  Example: %s\n", env.Example)
			}

			for {
				fmt.Fprintf(os.Stderr, "  Enter value: ")
				value, err := reader.ReadString('\n')
				if err != nil {
					return nil, fmt.Errorf("failed to read input: %w", err)
				}
				value = strings.TrimSpace(value)
				if value == "" {
					fmt.Fprintf(os.Stderr, "  [!] This value is required\n")
					continue
				}
				config.EnvVars[env.Name] = value
				break
			}
		}
	}

	// Prompt for optional env vars with defaults
	if len(deep.OptionalEnvVars) > 0 {
		fmt.Fprintf(os.Stderr, "\n[deploy] Optional configuration (Enter to use default):\n")
		for _, env := range deep.OptionalEnvVars {
			fmt.Fprintf(os.Stderr, "\n  %s", env.Name)
			if env.Description != "" {
				fmt.Fprintf(os.Stderr, " - %s", env.Description)
			}
			if env.Default != "" {
				fmt.Fprintf(os.Stderr, " [default: %s]", env.Default)
			}
			fmt.Fprintf(os.Stderr, "\n")
			fmt.Fprintf(os.Stderr, "  Enter value: ")
			value, _ := reader.ReadString('\n')
			value = strings.TrimSpace(value)
			if value != "" {
				config.EnvVars[env.Name] = value
			} else if env.Default != "" {
				config.EnvVars[env.Name] = env.Default
			}
		}
	}

	// Determine deployment mode
	if deep.PreferDocker || profile.HasDocker {
		config.DeployMode = "docker"
		fmt.Fprintf(os.Stderr, "\n[deploy] Will use Docker deployment\n")
	} else {
		config.DeployMode = "native"
		fmt.Fprintf(os.Stderr, "\n[deploy] Will use native Node.js deployment (PM2)\n")
	}

	return config, nil
}

// DefaultUserConfig returns a default config when no prompting is needed
func DefaultUserConfig(deep *DeepAnalysis, profile *RepoProfile) *UserConfig {
	config := &UserConfig{
		EnvVars:      make(map[string]string),
		AppPort:      3000,
		DeployMode:   "docker",
		StartCommand: "npm start",
	}

	if deep != nil {
		if deep.ListeningPort > 0 {
			config.AppPort = deep.ListeningPort
		}
		if deep.StartCommand != "" {
			config.StartCommand = deep.StartCommand
		}
		if deep.BuildCommand != "" {
			config.BuildCommand = deep.BuildCommand
		}
		if !deep.PreferDocker && !profile.HasDocker {
			config.DeployMode = "native"
		}
	}

	return config
}
