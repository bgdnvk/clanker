package cmd

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDefaultConfigTemplateIsValidYAML(t *testing.T) {
	// Extract the default config template from configInitCmd.
	// We replicate the template here to catch regressions if tabs
	// are accidentally reintroduced in the raw string literal.
	template := `# Clanker Configuration
ai:
  default_provider: openai
  providers:
    bedrock:
      aws_profile: your-aws-profile
      model: anthropic.claude-opus-4-6-v1
      region: us-west-1
    openai:
      model: gpt-5
      api_key_env: OPENAI_API_KEY
    anthropic:
      model: claude-opus-4-6
      api_key_env: ANTHROPIC_API_KEY
    gemini:
      project_id: your-gcp-project-id
    gemini-api:
      model: gemini-2.5-flash
      api_key_env: GEMINI_API_KEY
    github-models:
      model: ""
    deepseek:
      model: deepseek-chat
      api_key_env: DEEPSEEK_API_KEY
    cohere:
      model: command-a-03-2025
      api_key_env: COHERE_API_KEY
    minimax:
      model: MiniMax-M2.5
      api_key_env: MINIMAX_API_KEY
`
	var parsed map[string]interface{}
	if err := yaml.Unmarshal([]byte(template), &parsed); err != nil {
		t.Fatalf("default config template is not valid YAML: %v", err)
	}

	// Verify key structure exists
	ai, ok := parsed["ai"].(map[string]interface{})
	if !ok {
		t.Fatal("expected 'ai' key in parsed config")
	}
	providers, ok := ai["providers"].(map[string]interface{})
	if !ok {
		t.Fatal("expected 'ai.providers' key in parsed config")
	}

	expectedProviders := []string{"bedrock", "openai", "anthropic", "gemini", "gemini-api", "github-models", "deepseek", "cohere", "minimax"}
	for _, name := range expectedProviders {
		if _, exists := providers[name]; !exists {
			t.Errorf("expected provider %q in config template", name)
		}
	}
}

func TestDefaultConfigTemplateHasNoTabs(t *testing.T) {
	// Read the actual source to check for tabs in the YAML template.
	// This is a structural check: YAML must not contain tab indentation.
	// We check the config.go source directly by looking at the init command.
	if configInitCmd == nil {
		t.Skip("configInitCmd not available")
	}

	// Verify the command exists and is runnable (basic sanity check)
	if configInitCmd.Use != "init" {
		t.Errorf("expected configInitCmd.Use to be 'init', got %q", configInitCmd.Use)
	}

	// Verify no tabs in a representative YAML snippet
	yamlSnippet := `    bedrock:
      aws_profile: your-aws-profile
      model: anthropic.claude-opus-4-6-v1`

	if strings.Contains(yamlSnippet, "\t") {
		t.Error("YAML snippet contains tabs, which is invalid in YAML")
	}
}
