package ai

import (
	"fmt"

	awsclient "github.com/bgdnvk/clanker/internal/aws"
	"github.com/spf13/viper"
)

// getAIProfile returns the AI configuration for the given profile name
func (c *Client) getAIProfile(profileName string) (*awsclient.AIProfile, error) {
	return awsclient.GetAIProfile(profileName)
}

// getRegionForAWSProfile returns the region for the given AWS profile from configuration
func (c *Client) getRegionForAWSProfile(profileName string) string {
	defaultProvider := viper.GetString("infra.default_provider")
	if defaultProvider == "" {
		defaultProvider = "aws" // fallback to aws
	}

	// First, try to find which environment uses this profile
	environmentsMap := viper.GetStringMap(fmt.Sprintf("infra.%s.environments", defaultProvider))
	for envName := range environmentsMap {
		envProfileKey := fmt.Sprintf("infra.%s.environments.%s.profile", defaultProvider, envName)
		envProfile := viper.GetString(envProfileKey)
		if envProfile == profileName {
			// Found the environment that uses this profile, get its region
			regionKey := fmt.Sprintf("infra.%s.environments.%s.region", defaultProvider, envName)
			region := viper.GetString(regionKey)
			if region != "" {
				return region
			}
		}
	}

	// Fallback to legacy structure
	profileKey := fmt.Sprintf("infra.aws.profiles.%s.region", profileName)
	legacyRegion := viper.GetString(profileKey)
	if legacyRegion != "" {
		return legacyRegion
	}

	// Fallback to default region if not found
	region := "us-east-1"
	fmt.Printf("⚠️  No region found for profile %s, using fallback: %s\n", profileName, region)
	return region
} // findLLMCallProfile finds the default AI provider
func (c *Client) findLLMCallProfile() string {
	// Get the default AI provider
	defaultProvider := viper.GetString("ai.default_provider")
	if defaultProvider != "" {
		return defaultProvider
	}

	// Fallback to first available provider
	providers := viper.GetStringMap("ai.providers")
	for providerName := range providers {
		return providerName
	}

	// Ultimate fallback
	return "openai"
}

// FindInfraAnalysisProfile finds the AWS profile from infrastructure config
func FindInfraAnalysisProfile() string {
	// Get the default environment and provider
	defaultEnv := viper.GetString("infra.default_environment")
	if defaultEnv == "" {
		defaultEnv = "dev" // fallback to dev
	}

	defaultProvider := viper.GetString("infra.default_provider")
	if defaultProvider == "" {
		defaultProvider = "aws" // fallback to aws
	}

	// Try to get profile from environment-based structure
	profileKey := fmt.Sprintf("infra.%s.environments.%s.profile", defaultProvider, defaultEnv)
	profile := viper.GetString(profileKey)
	if profile != "" {
		return profile
	}

	// Fallback to legacy structure for backward compatibility
	legacyProfileKey := fmt.Sprintf("infra.%s.default_profile", defaultProvider)
	legacyProfile := viper.GetString(legacyProfileKey)
	if legacyProfile != "" {
		return legacyProfile
	}

	// Ultimate fallback
	return "default"
}

// FindInfraAnalysisRegion finds the AWS region from infrastructure config
func FindInfraAnalysisRegion() string {
	// Get the default environment and provider
	defaultEnv := viper.GetString("infra.default_environment")
	if defaultEnv == "" {
		defaultEnv = "dev" // fallback to dev
	}

	defaultProvider := viper.GetString("infra.default_provider")
	if defaultProvider == "" {
		defaultProvider = "aws" // fallback to aws
	}

	// Try to get region from environment-based structure
	regionKey := fmt.Sprintf("infra.%s.environments.%s.region", defaultProvider, defaultEnv)
	region := viper.GetString(regionKey)
	if region != "" {
		return region
	}

	// Fallback to legacy structure for backward compatibility
	profile := FindInfraAnalysisProfile()
	legacyRegionKey := fmt.Sprintf("infra.%s.profiles.%s.region", defaultProvider, profile)
	legacyRegion := viper.GetString(legacyRegionKey)
	if legacyRegion != "" {
		return legacyRegion
	}

	// Ultimate fallback
	return "us-east-1"
}
