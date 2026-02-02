package backend

import (
	"os"
	"strings"

	"github.com/spf13/viper"
)

// ResolveAPIKey returns the backend API key from flag, config, or environment
// Priority: flag > config > env
func ResolveAPIKey(flagValue string) string {
	if strings.TrimSpace(flagValue) != "" {
		return strings.TrimSpace(flagValue)
	}

	if apiKey := strings.TrimSpace(viper.GetString("backend.api_key")); apiKey != "" {
		return apiKey
	}

	if env := strings.TrimSpace(os.Getenv("CLANKER_BACKEND_API_KEY")); env != "" {
		return env
	}

	return ""
}

// ResolveBackendURL returns the backend URL based on configuration
// Priority: explicit URL > env URL > env name > config env > default (staging)
func ResolveBackendURL() string {
	// Priority 1: Explicit URL from config
	if url := strings.TrimSpace(viper.GetString("backend.url")); url != "" {
		return url
	}

	// Priority 2: URL from environment variable
	if url := strings.TrimSpace(os.Getenv("CLANKER_BACKEND_URL")); url != "" {
		return url
	}

	// Priority 3: Environment name from config or env var
	env := strings.TrimSpace(viper.GetString("backend.env"))
	if env == "" {
		env = strings.TrimSpace(os.Getenv("CLANKER_BACKEND_ENV"))
	}

	// Default to staging
	if env == "" {
		env = "staging"
	}

	// Look up URL for environment
	if url, ok := BackendURLs[env]; ok && url != "" {
		return url
	}

	// Fallback to staging if environment not found or empty
	return BackendURLs["staging"]
}

// ResolveBackendEnv returns the backend environment name
func ResolveBackendEnv() string {
	// Check config first
	if env := strings.TrimSpace(viper.GetString("backend.env")); env != "" {
		return env
	}

	// Check environment variable
	if env := strings.TrimSpace(os.Getenv("CLANKER_BACKEND_ENV")); env != "" {
		return env
	}

	// Default to staging
	return "staging"
}

// IsConfigured returns true if a backend API key is available
func IsConfigured() bool {
	return ResolveAPIKey("") != ""
}

// ValidEnvironments returns the list of valid backend environments
func ValidEnvironments() []string {
	return []string{"testing", "staging", "production"}
}

// IsValidEnvironment checks if the given environment name is valid
func IsValidEnvironment(env string) bool {
	for _, valid := range ValidEnvironments() {
		if env == valid {
			return true
		}
	}
	return false
}
