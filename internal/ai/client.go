package ai

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"

	// AWS SDK imports - commented out but kept for future use
	// "github.com/aws/aws-sdk-go-v2/aws"
	// "github.com/aws/aws-sdk-go-v2/config"
	// "github.com/aws/aws-sdk-go-v2/service/bedrockruntime"

	"github.com/bgdnvk/clanker/internal/agent"
	awsclient "github.com/bgdnvk/clanker/internal/aws"
	ghclient "github.com/bgdnvk/clanker/internal/github"
	"github.com/spf13/viper"
	"google.golang.org/genai"
)

type anthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type anthropicRequest struct {
	Model       string             `json:"model"`
	MaxTokens   int                `json:"max_tokens"`
	Temperature float64            `json:"temperature,omitempty"`
	Messages    []anthropicMessage `json:"messages"`
}

type anthropicResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

type anthropicModelsResponse struct {
	Data []struct {
		ID        string `json:"id"`
		CreatedAt string `json:"created_at"`
	} `json:"data"`
}

type Client struct {
	provider     string
	apiKey       string
	baseURL      string
	geminiClient *genai.Client
	awsClient    *awsclient.Client
	githubClient *ghclient.Client
	aiProfile    string
	debug        bool

	// AWS SDK fields - commented out but kept for future use
	// bedrockClient *bedrockruntime.Client
	// awsConfig     aws.Config
}

// Tool definitions for function calling
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema ToolInputSchema `json:"input_schema"`
}

type ToolInputSchema struct {
	Type       string            `json:"type"`
	Properties map[string]string `json:"properties"`
	Required   []string          `json:"required"`
}

// Bedrock Claude request/response types
type ClaudeRequest struct {
	AnthropicVersion string    `json:"anthropic_version"`
	MaxTokens        int       `json:"max_tokens"`
	Messages         []Message `json:"messages"`
	Tools            []Tool    `json:"tools,omitempty"`
}

type ClaudeResponse struct {
	Content []ClaudeContent `json:"content"`
	ID      string          `json:"id"`
	Model   string          `json:"model"`
	Role    string          `json:"role"`
	Type    string          `json:"type"`
}

type ClaudeContent struct {
	Text string `json:"text"`
	Type string `json:"type"`
}

// OpenAI types (keeping for compatibility)
type OpenAIRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type OpenAIResponse struct {
	Choices []Choice `json:"choices"`
}

type Choice struct {
	Message Message `json:"message"`
}

func looksLikeEnvVarName(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) < 8 {
		return false
	}
	// Must be all caps/underscores/digits and start with a letter.
	for i, r := range s {
		if i == 0 {
			if r < 'A' || r > 'Z' {
				return false
			}
			continue
		}
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			continue
		}
		return false
	}
	return true
}

func resolveEnvVarKeyPointer(apiKey string) string {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return ""
	}
	if !looksLikeEnvVarName(apiKey) {
		return apiKey
	}
	if v := strings.TrimSpace(os.Getenv(apiKey)); v != "" {
		return v
	}
	return apiKey
}

func NewClient(provider, apiKey string, debug bool, aiProfile ...string) *Client {
	client := &Client{
		provider: provider,
		apiKey:   resolveEnvVarKeyPointer(apiKey),
		debug:    debug,
	}

	// Set AI profile if provided, otherwise find the first profile-llm-call* profile
	if len(aiProfile) > 0 && aiProfile[0] != "" {
		client.aiProfile = aiProfile[0]
	} else {
		client.aiProfile = client.findLLMCallProfile()
	}

	switch provider {
	case "bedrock", "claude":
		// AWS SDK initialization - commented out but kept for future use
		// This was working before but had SSO credential caching issues
		// cfg, err := config.LoadDefaultConfig(context.Background(),
		//     config.WithSharedConfigProfile("commercial-dev"))
		// if err == nil {
		//     client.awsConfig = cfg
		//     client.bedrockClient = bedrockruntime.NewFromConfig(cfg)
		// }

		// Currently using AWS CLI approach which works reliably with SSO
	case "gemini":
		// For Gemini, use Application Default Credentials (like gemini CLI)
		// User should run: gcloud auth application-default login
		ctx := context.Background()
		geminiClient, err := genai.NewClient(ctx, &genai.ClientConfig{
			// No APIKey - will automatically use Application Default Credentials
			// This works just like the gemini CLI tool
		})
		if err == nil {
			client.geminiClient = geminiClient
		} else {
			client.tryFallbackToOpenAI(err)
		}
	case "gemini-api":
		// For Gemini API (requires API key from Google AI Studio)
		if apiKey == "" {
			client.tryFallbackToOpenAI(fmt.Errorf("gemini-api provider configured without API key"))
			break
		}

		ctx := context.Background()
		geminiClient, err := genai.NewClient(ctx, &genai.ClientConfig{
			APIKey: apiKey,
		})
		if err == nil {
			client.geminiClient = geminiClient
		} else {
			client.tryFallbackToOpenAI(err)
		}
	case "openai":
		client.baseURL = "https://api.openai.com/v1"
	case "anthropic":
		client.baseURL = "https://api.anthropic.com/v1"
	case "deepseek":
		client.baseURL = "https://api.deepseek.com/v1"
	case "minimax":
		client.baseURL = "https://api.minimax.io/anthropic"
	default:
		// Default to OpenAI for best compatibility when no provider specified
		client.provider = "openai"
		client.baseURL = "https://api.openai.com/v1"
	}

	return client
}

func NewClientWithTools(provider, apiKey string, awsClient *awsclient.Client, githubClient *ghclient.Client, debug bool, aiProfile ...string) *Client {
	client := NewClient(provider, apiKey, debug, aiProfile...)
	client.awsClient = awsClient
	client.githubClient = githubClient
	return client
}

func (c *Client) GetTools() []Tool {
	return []Tool{
		{
			Name:        "get_latest_batch_jobs",
			Description: "Get information about the latest AWS Batch jobs",
			InputSchema: ToolInputSchema{
				Type: "object",
				Properties: map[string]string{
					"limit": "Number of latest jobs to retrieve (default: 5)",
				},
				Required: []string{},
			},
		},
		{
			Name:        "get_lambda_functions",
			Description: "Get information about AWS Lambda functions",
			InputSchema: ToolInputSchema{
				Type: "object",
				Properties: map[string]string{
					"filter": "Optional filter for function names",
				},
				Required: []string{},
			},
		},
		{
			Name:        "get_ec2_instances",
			Description: "Get information about EC2 instances",
			InputSchema: ToolInputSchema{
				Type: "object",
				Properties: map[string]string{
					"state": "Optional state filter (running, stopped, etc.)",
				},
				Required: []string{},
			},
		},
		{
			Name:        "get_ecs_clusters",
			Description: "Get information about ECS clusters and services",
			InputSchema: ToolInputSchema{
				Type: "object",
				Properties: map[string]string{
					"cluster": "Optional cluster name filter",
				},
				Required: []string{},
			},
		},
		{
			Name:        "get_ecs_tasks",
			Description: "Get information about ECS tasks",
			InputSchema: ToolInputSchema{
				Type: "object",
				Properties: map[string]string{
					"cluster": "Optional cluster name to filter tasks",
					"service": "Optional service name to filter tasks",
				},
				Required: []string{},
			},
		},
		{
			Name:        "get_cloudwatch_logs",
			Description: "Get CloudWatch log groups information",
			InputSchema: ToolInputSchema{
				Type: "object",
				Properties: map[string]string{
					"filter": "Optional filter for log group names",
				},
				Required: []string{},
			},
		},
		{
			Name:        "get_github_workflows",
			Description: "Get GitHub Actions workflow information",
			InputSchema: ToolInputSchema{
				Type: "object",
				Properties: map[string]string{
					"status": "Optional status filter (active, disabled, etc.)",
				},
				Required: []string{},
			},
		},
	}
}

func (c *Client) Ask(ctx context.Context, question, awsContext, codeContext string, githubContext ...string) (string, error) {
	// Use the default profile from the AI configuration for LLM calls
	profileLLMCall, err := c.getAIProfile(c.aiProfile)
	if err != nil {
		return "", fmt.Errorf("failed to get AI profile for LLM calls: %w", err)
	}

	// Use the AWS profile from the AI configuration as the infrastructure analysis profile
	// This maintains backward compatibility
	return c.AskWithTools(ctx, question, awsContext, codeContext, profileLLMCall.AWSProfile, githubContext...)
}

// AskWithTools performs the full AWS tool calling workflow
func (c *Client) AskWithTools(ctx context.Context, question, awsContext, codeContext, profileInfraAnalysis string, githubContext ...string) (string, error) {
	// Check if this query would benefit from intelligent agent investigation
	if c.shouldUseAgent(question) && c.awsClient != nil {
		return c.askWithAgentInvestigation(ctx, question, awsContext, codeContext, profileInfraAnalysis, githubContext...)
	}

	// Otherwise use the standard dynamic three-stage approach
	return c.askWithDynamicAnalysis(ctx, question, awsContext, codeContext, profileInfraAnalysis, githubContext...)
}

// askWithDynamicAnalysis implements the three-stage dynamic analysis approach for all AI providers
func (c *Client) askWithDynamicAnalysis(ctx context.Context, question, awsContext, codeContext, profileInfraAnalysis string, githubContext ...string) (string, error) {
	if c.debug {
		fmt.Printf("🔍 Stage 1: Analyzing query with dynamic tool selection...\n")
	}

	// Stage 1: Ask Claude to analyze the query and determine what AWS operations are needed
	// This uses the LLM profile (profileLLMCall) from the AI configuration
	analysisPrompt := awsclient.GetLLMAnalysisPrompt(question)
	if c.debug {
		fmt.Printf("📝 Analysis prompt length: %d characters\n", len(analysisPrompt))
	}

	// Get analysis from the configured AI provider (uses AI profile for LLM calls)
	var analysisResponse string
	var err error
	switch c.provider {
	case "bedrock", "claude":
		analysisResponse, err = c.askBedrock(ctx, analysisPrompt)
	case "openai":
		analysisResponse, err = c.askOpenAI(ctx, analysisPrompt)
	case "anthropic":
		analysisResponse, err = c.askAnthropic(ctx, analysisPrompt)
	case "gemini", "gemini-api":
		analysisResponse, err = c.askGemini(ctx, analysisPrompt)
	default:
		analysisResponse, err = c.askBedrock(ctx, analysisPrompt)
	}
	if err != nil {
		return "", fmt.Errorf("failed to analyze query: %w", err)
	}

	if c.debug {
		fmt.Printf("📥 Raw analysis response length: %d characters\n", len(analysisResponse))
		fmt.Printf("📄 Raw analysis response: %s\n", analysisResponse)
	}

	// Clean and extract JSON from the response
	cleanedResponse := c.extractAndCleanJSON(analysisResponse)
	if c.debug {
		fmt.Printf("🧹 Cleaned JSON length: %d characters\n", len(cleanedResponse))
		fmt.Printf("🔧 Cleaned JSON: %s\n", cleanedResponse)
	}

	// Parse the analysis response
	var analysis awsclient.LLMAnalysis
	if err := json.Unmarshal([]byte(cleanedResponse), &analysis); err != nil {
		// If JSON parsing fails, fall back to original approach
		if c.debug {
			fmt.Printf("❌ Warning: Failed to parse analysis response, falling back to no-tool approach: %v\n", err)
			fmt.Printf("📋 Raw response (first 500 chars): %s...\n", func() string {
				if len(cleanedResponse) > 500 {
					return cleanedResponse[:500]
				}
				return cleanedResponse
			}())
		}
		// Continue with no AWS operations
	} else {
		if c.debug {
			fmt.Printf("✅ Successfully parsed analysis: %d operations found\n", len(analysis.Operations))
			for i, op := range analysis.Operations {
				fmt.Printf("  %d. %s - %s\n", i+1, op.Operation, op.Reason)
			}
		}
	}

	// Stage 2: Execute the determined AWS operations concurrently using AWS client
	if c.debug {
		fmt.Printf("🔧 Stage 2: Executing AWS operations...\n")
	}
	var awsResults string
	if c.awsClient != nil && len(analysis.Operations) > 0 {
		if c.debug {
			fmt.Printf("🚀 Executing %d operations with infrastructure profile: %s...\n", len(analysis.Operations), profileInfraAnalysis)
		}

		// Get the region for the infrastructure analysis profile from config
		region := c.getRegionForAWSProfile(profileInfraAnalysis)

		var err error
		awsResults, err = c.awsClient.ExecuteOperationsWithAWSProfile(ctx, analysis.Operations, profileInfraAnalysis, region)
		if err != nil {
			if c.debug {
				fmt.Printf("❌ Warning: Failed to execute AWS operations: %v\n", err)
			}
			awsResults = ""
		} else {
			if c.debug {
				fmt.Printf("✅ AWS operations completed. Results length: %d characters\n", len(awsResults))
			}
		}
	} else {
		if c.awsClient == nil {
			if c.debug {
				if len(analysis.Operations) > 0 {
					fmt.Printf("⚠️  Selected %d AWS operation(s) but AWS is not enabled (no AWS client). Re-run with --aws to execute.\n", len(analysis.Operations))
				} else {
					fmt.Printf("⚠️  AWS client is nil - cannot execute operations\n")
				}
			}
		} else {
			if c.debug {
				fmt.Printf("ℹ️  No operations to execute\n")
			}
		}
	}

	// Stage 3: Build final context and get Claude's response
	if c.debug {
		fmt.Printf("📝 Stage 3: Building final context and getting response...\n")
	}
	var finalContext strings.Builder

	if awsContext != "" {
		finalContext.WriteString("Initial AWS Context:\n")
		finalContext.WriteString(awsContext)
		finalContext.WriteString("\n\n")
		if c.debug {
			fmt.Printf("📊 Added initial AWS context: %d characters\n", len(awsContext))
		}
	}

	if awsResults != "" {
		finalContext.WriteString("Live AWS Data:\n")
		finalContext.WriteString(awsResults)
		finalContext.WriteString("\n")
		if c.debug {
			fmt.Printf("🔥 Added live AWS data: %d characters\n", len(awsResults))
		}
	}

	if codeContext != "" {
		finalContext.WriteString("Code Context:\n")
		finalContext.WriteString(codeContext)
		finalContext.WriteString("\n\n")
		if c.debug {
			fmt.Printf("💻 Added code context: %d characters\n", len(codeContext))
		}
	}

	if len(githubContext) > 0 && githubContext[0] != "" {
		finalContext.WriteString("GitHub Context:\n")
		finalContext.WriteString(githubContext[0])
		finalContext.WriteString("\n\n")
		if c.debug {
			fmt.Printf("🐙 Added GitHub context: %d characters\n", len(githubContext[0]))
		}
	}

	// Summarize context if too large to avoid CLI arg limits and reduce token usage
	summarizedContext, err := c.summarizeContextIfNeeded(ctx, question, finalContext.String())
	if err != nil {
		// Fallback: truncate context if summarization fails
		const fallbackLimit = 80000
		if len(finalContext.String()) > fallbackLimit {
			summarizedContext = finalContext.String()[:fallbackLimit]
		} else {
			summarizedContext = finalContext.String()
		}
	}

	// Build final prompt
	finalPrompt := fmt.Sprintf(`%s

Context:
%s

Please provide a comprehensive answer based on the live data above.`, question, summarizedContext)

	if c.debug {
		fmt.Printf("🎯 Final prompt length: %d characters\n", len(finalPrompt))
		fmt.Printf("🚀 Sending final request to AI provider (%s)...\n", c.provider)
		fmt.Printf("📤 Query to LLM: %s\n", question)
	}

	// Use the same provider switching logic as in the analysis phase
	switch c.provider {
	case "bedrock", "claude":
		return c.askBedrock(ctx, finalPrompt)
	case "openai":
		return c.askOpenAI(ctx, finalPrompt)
	case "anthropic":
		return c.askAnthropic(ctx, finalPrompt)
	case "gemini", "gemini-api":
		return c.askGemini(ctx, finalPrompt)
	default:
		return c.askBedrock(ctx, finalPrompt)
	}
}

// Original Ask method for backward compatibility - replaced above
func (c *Client) AskOriginal(ctx context.Context, question, awsContext, codeContext string, githubContext ...string) (string, error) {
	// If we have AWS/GitHub clients, try to get specific data based on the question
	var enhancedContext strings.Builder

	if awsContext != "" {
		enhancedContext.WriteString(awsContext)
		enhancedContext.WriteString("\n")
	}

	if codeContext != "" {
		enhancedContext.WriteString(codeContext)
		enhancedContext.WriteString("\n")
	}

	if len(githubContext) > 0 && githubContext[0] != "" {
		enhancedContext.WriteString(githubContext[0])
		enhancedContext.WriteString("\n")
	}

	// Analyze question and get specific data if we have the right clients
	if c.awsClient != nil {
		specificData, err := c.getSpecificAWSData(ctx, question)
		if err == nil && specificData != "" {
			enhancedContext.WriteString("Specific AWS Data:\n")
			enhancedContext.WriteString(specificData)
			enhancedContext.WriteString("\n")
		}
	}

	if c.githubClient != nil {
		specificData, err := c.getSpecificGitHubData(ctx, question)
		if err == nil && specificData != "" {
			enhancedContext.WriteString("Specific GitHub Data:\n")
			enhancedContext.WriteString(specificData)
			enhancedContext.WriteString("\n")
		}
	}

	// Build the prompt with enhanced context
	prompt := c.buildPrompt(question, enhancedContext.String(), "", "")

	switch c.provider {
	case "bedrock", "claude":
		return c.askBedrock(ctx, prompt)
	case "gemini", "gemini-api":
		return c.askGemini(ctx, prompt)
	case "anthropic":
		return c.askAnthropic(ctx, prompt)
	case "openai":
		return c.askOpenAI(ctx, prompt)
	default:
		// Default to Bedrock (currently uses AWS CLI)
		// AWS SDK fallback was: if c.bedrockClient != nil { return c.askBedrock(ctx, prompt) }
		return c.askBedrock(ctx, prompt)
	}
}

func (c *Client) buildPrompt(question, awsContext, codeContext, githubContext string) string {
	var prompt strings.Builder

	prompt.WriteString("You are an AI assistant helping with AWS infrastructure and GitHub repository management. ")
	prompt.WriteString("Answer the following question based on the provided context.\n\n")

	if awsContext != "" {
		prompt.WriteString("AWS Infrastructure Context:\n")
		prompt.WriteString(awsContext)
		prompt.WriteString("\n")
	}

	if codeContext != "" {
		prompt.WriteString("Codebase Context:\n")
		prompt.WriteString(codeContext)
		prompt.WriteString("\n")
	}

	if githubContext != "" {
		prompt.WriteString("GitHub Repository Context:\n")
		prompt.WriteString(githubContext)
		prompt.WriteString("\n")
	}

	prompt.WriteString("Question: ")
	prompt.WriteString(question)
	prompt.WriteString("\n\nPlease provide a helpful and accurate answer based on the context above.")

	return prompt.String()
}

func (c *Client) askBedrock(ctx context.Context, prompt string) (string, error) {
	// NOTE: Using AWS CLI approach due to SSO credential caching issues with Go SDK
	// Previous working AWS SDK approach (commented out for reference):
	// cfg, err := config.LoadDefaultConfig(ctx, config.WithSharedConfigProfile("commercial-dev"))
	// bedrockClient := bedrockruntime.NewFromConfig(cfg)
	// result, err := bedrockClient.InvokeModel(ctx, input)

	// Get AI profile configuration (this is the profileLLMCall for Bedrock API access)
	profileLLMCall, err := c.getAIProfile(c.aiProfile)
	if err != nil {
		return "", fmt.Errorf("failed to get AI profile for LLM calls: %w", err)
	}

	// Sanitize to ASCII-only to satisfy AWS CLI argv constraints
	prompt = sanitizeASCII(prompt)

	// Use AWS CLI directly since it works while Go SDK has SSO credential issues
	request := ClaudeRequest{
		AnthropicVersion: "bedrock-2023-05-31",
		MaxTokens:        4000,
		Messages: []Message{
			{
				Role:    "user",
				Content: prompt,
			},
		},
	}

	requestBody, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	// Write request body to a temp file to avoid command line length limits
	bodyFile, err := os.CreateTemp("", "bedrock-request-*.json")
	if err != nil {
		return "", fmt.Errorf("failed to create body temp file: %w", err)
	}
	bodyFilePath := bodyFile.Name()
	if _, err := bodyFile.Write(requestBody); err != nil {
		bodyFile.Close()
		os.Remove(bodyFilePath)
		return "", fmt.Errorf("failed to write body temp file: %w", err)
	}
	bodyFile.Close()
	defer os.Remove(bodyFilePath)

	// Create a cross-platform temporary file for the response
	tmpFile, err := os.CreateTemp("", "bedrock-response-*.json")
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpFilePath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpFilePath)

	// Call AWS CLI with LLM profile from config (for Bedrock API access)
	// Use fileb:// to read body from file as binary blob to avoid command line length limits
	cmd := exec.CommandContext(ctx, "aws", "bedrock-runtime", "invoke-model",
		"--model-id", profileLLMCall.Model,
		"--body", "fileb://"+bodyFilePath,
		"--profile", profileLLMCall.AWSProfile,
		"--region", profileLLMCall.Region,
		tmpFilePath)

	cmd.Env = append(os.Environ(), fmt.Sprintf("AWS_PROFILE=%s", profileLLMCall.AWSProfile))

	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("AWS CLI call failed: %w, output: %s", err, string(output))
	}

	// Read the response file
	responseData, err := os.ReadFile(tmpFilePath)
	if err != nil {
		return "", fmt.Errorf("failed to read response file: %w", err)
	}

	var response ClaudeResponse
	if err := json.Unmarshal(responseData, &response); err != nil {
		return "", fmt.Errorf("failed to unmarshal response: %w", err)
	}

	if len(response.Content) == 0 {
		return "", fmt.Errorf("no response content from Claude")
	}

	return response.Content[0].Text, nil
}

// executeAWSTool executes a specific AWS tool with the given parameters
func (c *Client) askGemini(ctx context.Context, prompt string) (string, error) {
	if c.geminiClient == nil {
		return "", fmt.Errorf("gemini client not initialized")
	}

	// Get the AI profile configuration (this is the profileLLMCall for Gemini API access)
	profileLLMCall, err := c.getAIProfile(c.aiProfile)
	if err != nil {
		return "", fmt.Errorf("failed to get AI profile for LLM calls: %w", err)
	}

	// Create content from text
	content := genai.NewContentFromText(sanitizeASCII(prompt), genai.RoleUser)

	// Generate content using the configured model
	resp, err := c.geminiClient.Models.GenerateContent(ctx, profileLLMCall.Model, []*genai.Content{content}, nil)
	if err != nil {
		return "", fmt.Errorf("failed to generate content with Gemini: %w", err)
	}

	if len(resp.Candidates) == 0 {
		return "", fmt.Errorf("no response candidates from Gemini")
	}

	// Extract text from the first candidate
	var result strings.Builder
	for _, part := range resp.Candidates[0].Content.Parts {
		if part.Text != "" {
			result.WriteString(part.Text)
		}
	}

	return result.String(), nil
}

func (c *Client) tryFallbackToOpenAI(reason error) {
	fallbackKey := resolveFallbackOpenAIKey(c.apiKey)
	if fallbackKey == "" {
		if c.debug {
			fmt.Printf("Gemini unavailable (%v) and no OpenAI key available for fallback\n", reason)
		}
		return
	}

	if c.debug {
		fmt.Printf("Gemini unavailable (%v). Falling back to OpenAI.\n", reason)
	}

	c.provider = "openai"
	c.apiKey = fallbackKey
	c.baseURL = "https://api.openai.com/v1"
	c.geminiClient = nil
}

func resolveFallbackOpenAIKey(existing string) string {
	if existing != "" {
		return existing
	}
	if key := viper.GetString("ai.providers.openai.api_key"); key != "" {
		return key
	}
	if envName := viper.GetString("ai.providers.openai.api_key_env"); envName != "" {
		if envVal := os.Getenv(envName); envVal != "" {
			return envVal
		}
	}
	if envVal := os.Getenv("OPENAI_API_KEY"); envVal != "" {
		return envVal
	}
	return ""
}

func (c *Client) askOpenAI(ctx context.Context, prompt string) (string, error) {
	// Get the AI profile configuration (this is the profileLLMCall for OpenAI API access)
	profileLLMCall, err := c.getAIProfile(c.aiProfile)
	if err != nil {
		return "", fmt.Errorf("failed to get AI profile for LLM calls: %w", err)
	}

	if c.apiKey == "" {
		return "", fmt.Errorf("OpenAI API key not configured")
	}

	request := OpenAIRequest{
		Model: profileLLMCall.Model,
		Messages: []Message{
			{
				Role:    "user",
				Content: sanitizeASCII(prompt),
			},
		},
	}

	jsonData, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/chat/completions", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(body))
	}

	var response OpenAIResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return "", fmt.Errorf("failed to unmarshal response: %w", err)
	}

	if len(response.Choices) == 0 {
		return "", fmt.Errorf("no response from AI")
	}

	return response.Choices[0].Message.Content, nil
}

func (c *Client) askAnthropic(ctx context.Context, prompt string) (string, error) {
	profileLLMCall, err := c.getAIProfile(c.aiProfile)
	if err != nil {
		return "", fmt.Errorf("failed to get AI profile for LLM calls: %w", err)
	}

	if strings.TrimSpace(c.apiKey) == "" {
		return "", fmt.Errorf("Anthropic API key not configured")
	}

	keyLen := len(strings.TrimSpace(c.apiKey))
	keyHash := ""
	{
		sum := sha256.Sum256([]byte(strings.TrimSpace(c.apiKey)))
		keyHash = fmt.Sprintf("%x", sum)[:8]
	}

	model := strings.TrimSpace(profileLLMCall.Model)
	if model == "" {
		latest, lErr := c.getLatestAnthropicModelID(ctx)
		if lErr != nil {
			return "", lErr
		}
		model = latest
	}

	// Anthropic API is strict about ASCII in some client setups; keep consistent with other providers.
	prompt = sanitizeASCII(prompt)

	reqBody := anthropicRequest{
		Model:       model,
		MaxTokens:   4000,
		Temperature: 0.1,
		Messages: []anthropicMessage{{
			Role: "user",
			// Use the content-block format which is compatible with modern Anthropic Messages API.
			Content: []map[string]any{{"type": "text", "text": prompt}},
		}},
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.baseURL, "/")+"/messages", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", strings.TrimSpace(c.apiKey))
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	client := &http.Client{}
	resp, err := client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Anthropic API request failed with status %d (keyLen=%d keyHash=%s): %s", resp.StatusCode, keyLen, keyHash, string(body))
	}

	var parsed anthropicResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("failed to unmarshal response: %w", err)
	}

	for _, c := range parsed.Content {
		if strings.TrimSpace(c.Text) != "" {
			return c.Text, nil
		}
	}

	return "", fmt.Errorf("no response content from Anthropic")
}

func (c *Client) getLatestAnthropicModelID(ctx context.Context) (string, error) {
	base := strings.TrimRight(c.baseURL, "/")
	url := base + "/models"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create models request: %w", err)
	}
	req.Header.Set("x-api-key", strings.TrimSpace(c.apiKey))
	req.Header.Set("anthropic-version", "2023-06-01")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to fetch Anthropic models: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read Anthropic models response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Anthropic models request failed with status %d: %s", resp.StatusCode, string(body))
	}

	var parsed anthropicModelsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("failed to unmarshal Anthropic models response: %w", err)
	}
	for _, m := range parsed.Data {
		id := strings.TrimSpace(m.ID)
		if id != "" {
			// Docs: newest models are listed first.
			return id, nil
		}
	}

	return "", fmt.Errorf("no Anthropic models returned")
}

func (c *Client) getSpecificAWSData(ctx context.Context, question string) (string, error) {
	if c.awsClient == nil {
		return "", nil
	}

	questionLower := strings.ToLower(question)
	var result strings.Builder

	// Check for batch job queries
	if strings.Contains(questionLower, "batch") && (strings.Contains(questionLower, "latest") || strings.Contains(questionLower, "recent")) {
		info, err := c.awsClient.GetRelevantContext(ctx, "batch jobs latest")
		if err == nil {
			result.WriteString("Latest AWS Batch Jobs:\n")
			result.WriteString(info)
			result.WriteString("\n")
		}
	}

	// Check for Lambda function queries
	if strings.Contains(questionLower, "lambda") && (strings.Contains(questionLower, "latest") || strings.Contains(questionLower, "recent") || strings.Contains(questionLower, "status")) {
		info, err := c.awsClient.GetRelevantContext(ctx, "lambda functions status")
		if err == nil {
			result.WriteString("Lambda Functions Status:\n")
			result.WriteString(info)
			result.WriteString("\n")
		}
	}

	// Check for EC2 instance queries
	if strings.Contains(questionLower, "ec2") && (strings.Contains(questionLower, "running") || strings.Contains(questionLower, "status") || strings.Contains(questionLower, "latest")) {
		info, err := c.awsClient.GetRelevantContext(ctx, "ec2 instances running")
		if err == nil {
			result.WriteString("Running EC2 Instances:\n")
			result.WriteString(info)
			result.WriteString("\n")
		}
	}

	// Check for CloudWatch logs queries
	if strings.Contains(questionLower, "log") && (strings.Contains(questionLower, "latest") || strings.Contains(questionLower, "recent") || strings.Contains(questionLower, "error")) {
		info, err := c.awsClient.GetRelevantContext(ctx, "cloudwatch logs recent")
		if err == nil {
			result.WriteString("Recent CloudWatch Logs:\n")
			result.WriteString(info)
			result.WriteString("\n")
		}
	}

	// Check for ECS cluster queries
	if strings.Contains(questionLower, "ecs") && (strings.Contains(questionLower, "cluster") || strings.Contains(questionLower, "service") || strings.Contains(questionLower, "status")) {
		info, err := c.awsClient.GetRelevantContext(ctx, "ecs clusters services")
		if err == nil {
			result.WriteString("ECS Clusters and Services:\n")
			result.WriteString(info)
			result.WriteString("\n")
		}
	}

	// Check for ECS task queries
	if strings.Contains(questionLower, "ecs") && (strings.Contains(questionLower, "task") || strings.Contains(questionLower, "running") || strings.Contains(questionLower, "container")) {
		info, err := c.awsClient.GetRelevantContext(ctx, "ecs tasks running")
		if err == nil {
			result.WriteString("ECS Tasks Status:\n")
			result.WriteString(info)
			result.WriteString("\n")
		}
	}

	return result.String(), nil
}

func (c *Client) getSpecificGitHubData(ctx context.Context, question string) (string, error) {
	if c.githubClient == nil {
		return "", nil
	}

	questionLower := strings.ToLower(question)
	var result strings.Builder

	// Check for workflow queries
	if strings.Contains(questionLower, "workflow") || strings.Contains(questionLower, "action") || strings.Contains(questionLower, "ci") {
		if strings.Contains(questionLower, "latest") || strings.Contains(questionLower, "recent") || strings.Contains(questionLower, "status") {
			info, err := c.githubClient.GetRelevantContext(ctx, "workflow runs latest")
			if err == nil {
				result.WriteString("Latest Workflow Runs:\n")
				result.WriteString(info)
				result.WriteString("\n")
			}
		} else {
			info, err := c.githubClient.GetRelevantContext(ctx, "workflows")
			if err == nil {
				result.WriteString("GitHub Workflows:\n")
				result.WriteString(info)
				result.WriteString("\n")
			}
		}
	}

	// Check for PR queries
	if strings.Contains(questionLower, "pr") || strings.Contains(questionLower, "pull") && strings.Contains(questionLower, "request") {
		info, err := c.githubClient.GetRelevantContext(ctx, "pull requests")
		if err == nil {
			result.WriteString("Recent Pull Requests:\n")
			result.WriteString(info)
			result.WriteString("\n")
		}
	}

	return result.String(), nil
}

func stripMarkdownCodeFences(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "```") {
			continue
		}
		out = append(out, ln)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// extractAndCleanJSON extracts the first valid JSON value from an LLM response.
// It is robust against braces inside JSON strings and markdown code fences.
func (c *Client) extractAndCleanJSON(response string) string {
	s := strings.TrimSpace(response)
	if s == "" {
		return s
	}

	// Remove markdown code fences, which often introduce leading backticks.
	s = stripMarkdownCodeFences(s)

	// Scan for a JSON object/array start and attempt decoding from there.
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch != '{' && ch != '[' {
			continue
		}
		dec := json.NewDecoder(strings.NewReader(s[i:]))
		dec.UseNumber()
		var raw json.RawMessage
		if err := dec.Decode(&raw); err == nil {
			trimmed := strings.TrimSpace(string(raw))
			if trimmed != "" {
				return trimmed
			}
		}
	}

	return strings.TrimSpace(response)
}

// AskPrompt sends a raw prompt to the configured provider without adding additional wrapper context.
func (c *Client) AskPrompt(ctx context.Context, prompt string) (string, error) {
	switch c.provider {
	case "bedrock", "claude":
		return c.askBedrock(ctx, prompt)
	case "openai":
		return c.askOpenAI(ctx, prompt)
	case "anthropic":
		return c.askAnthropic(ctx, prompt)
	case "gemini", "gemini-api":
		return c.askGemini(ctx, prompt)
	default:
		return c.askBedrock(ctx, prompt)
	}
}

// CleanJSONResponse extracts the first JSON object from a response and applies minimal cleanup.
func (c *Client) CleanJSONResponse(response string) string {
	return c.extractAndCleanJSON(response)
}

// shouldUseAgent determines if the query would benefit from intelligent agent investigation
func (c *Client) shouldUseAgent(question string) bool {
	questionLower := strings.ToLower(question)

	// Agent keywords that indicate need for log analysis and service investigation
	agentKeywords := []string{
		"chat", "logs", "latest", "recent", "error", "issue", "problem", "failure",
		"debug", "investigate", "analyze", "status", "health", "performance",
		"image", "processing", "service", "api", "response", "timeout",
		"summary", "what happened", "whats wrong", "why", "how", "when",
	}

	for _, keyword := range agentKeywords {
		if strings.Contains(questionLower, keyword) {
			return true
		}
	}

	return false
}

// askWithAgentInvestigation uses the intelligent agent to gather context before answering
func (c *Client) askWithAgentInvestigation(ctx context.Context, question, awsContext, codeContext, profileInfraAnalysis string, githubContext ...string) (string, error) {
	if c.debug {
		fmt.Printf("🤖 Using intelligent agent for context investigation...\n")
	}

	// Find the appropriate AI profile for agent operations
	profile := c.findAgentProfile(profileInfraAnalysis)
	if profile == nil {
		if c.debug {
			fmt.Printf("⚠️  No suitable AI profile found for agent operations, falling back to standard approach\n")
		}
		return c.askWithDynamicAnalysis(ctx, question, awsContext, codeContext, profileInfraAnalysis, githubContext...)
	}

	// Create and run the investigative agent
	investigator := agent.NewAgent(c.awsClient, c.debug)

	// Set AI decision function so agent can make intelligent decisions
	investigator.SetAIDecisionFunction(func(ctx context.Context, prompt string) (string, error) {
		return c.Ask(ctx, prompt, "", "")
	})

	agentContext, err := investigator.InvestigateQuery(ctx, question)
	if err != nil {
		if c.debug {
			fmt.Printf("⚠️  Agent investigation failed: %v, falling back to standard approach\n", err)
		}
		return c.askWithDynamicAnalysis(ctx, question, awsContext, codeContext, profileInfraAnalysis, githubContext...)
	}

	// Build final context with agent's findings
	finalContext := investigator.BuildFinalContext(agentContext)

	if c.debug {
		fmt.Printf("🎯 Agent gathered %d chars of context in %d steps\n", len(finalContext), agentContext.CurrentStep)
	}

	// Combine with existing contexts
	combinedContext := ""
	if awsContext != "" {
		combinedContext += "=== EXISTING AWS CONTEXT ===\n" + awsContext + "\n\n"
	}
	if codeContext != "" {
		combinedContext += "=== CODE CONTEXT ===\n" + codeContext + "\n\n"
	}
	if len(githubContext) > 0 && githubContext[0] != "" {
		combinedContext += "=== GITHUB CONTEXT ===\n" + githubContext[0] + "\n\n"
	}

	combinedContext += finalContext

	// Summarize context if too large to avoid CLI arg limits and reduce token usage
	summarizedContext, sErr := c.summarizeContextIfNeeded(ctx, question, combinedContext)
	if sErr != nil {
		// Fallback: truncate context if summarization fails
		if c.debug {
			fmt.Printf("⚠️  Summarization failed: %v. Falling back to truncation.\n", sErr)
		}
		const fallbackLimit = 80000
		if len(combinedContext) > fallbackLimit {
			summarizedContext = combinedContext[:fallbackLimit]
		} else {
			summarizedContext = combinedContext
		}
	}

	if c.debug {
		fmt.Printf("📦 Combined context length: %d, summarized length: %d\n", len(combinedContext), len(summarizedContext))
	}

	// Final LLM call with compacted context
	finalPrompt := fmt.Sprintf(`Based on the comprehensive investigation below, please answer the user's question: "%s"

%s

CRITICAL INSTRUCTIONS:

Take your time to thoroughly analyze the data. Think extremely hard about what the evidence tells you and what actions should be taken. Please provide a comprehensive, actionable response based on the gathered information, ensuring all critical findings and specific details are prominently featured.`, question, summarizedContext)

	// Use the same AI provider for the final response
	var response string
	switch c.provider {
	case "bedrock", "claude":
		response, err = c.askBedrock(ctx, finalPrompt)
	case "openai":
		response, err = c.askOpenAI(ctx, finalPrompt)
	case "anthropic":
		response, err = c.askAnthropic(ctx, finalPrompt)
	case "gemini", "gemini-api":
		response, err = c.askGemini(ctx, finalPrompt)
	default:
		response, err = c.askBedrock(ctx, finalPrompt)
	}

	if err != nil {
		return "", fmt.Errorf("failed to get final AI response: %w", err)
	}

	return response, nil
}

// summarizeContextIfNeeded reduces context size by chunking and summarizing when it exceeds limits
func (c *Client) summarizeContextIfNeeded(ctx context.Context, question, contextText string) (string, error) {
	// Allow override via config, else default safe limit under macOS arg max
	maxChars := viper.GetInt("ai.max_prompt_chars")
	if maxChars <= 0 {
		maxChars = 120000
	}

	if len(contextText) <= maxChars {
		return contextText, nil
	}

	// Chunk the context
	chunkSize := viper.GetInt("ai.chunk_chars")
	if chunkSize <= 0 {
		chunkSize = 120000
	}

	chunks := chunkString(contextText, chunkSize)
	// Limit total chunks to keep latency reasonable
	maxChunks := viper.GetInt("ai.max_chunks")
	if maxChunks <= 0 {
		maxChunks = 6
	}
	if len(chunks) > maxChunks {
		if c.debug {
			fmt.Printf("🧩 Context split into %d chunks; sampling to %d for summarization...\n", len(chunks), maxChunks)
		}
		chunks = sampleChunks(chunks, maxChunks)
	}
	if c.debug {
		fmt.Printf("🧠 Summarizing %d chunk(s), ~%d chars each (target <= %d chars)\n", len(chunks), chunkSize, maxChars)
	}

	summaries := make([]string, 0, len(chunks))
	for i, ch := range chunks {
		if c.debug {
			fmt.Printf("📝 Summarizing chunk %d/%d (size %d chars)\n", i+1, len(chunks), len(ch))
		}
		sum, err := c.summarizeChunk(ctx, question, ch, i+1, len(chunks))
		if err != nil {
			// Fallback: truncate chunk
			const chunkFallback = 5000
			if len(ch) > chunkFallback {
				summaries = append(summaries, ch[:chunkFallback])
			} else {
				summaries = append(summaries, ch)
			}
			continue
		}
		summaries = append(summaries, sum)
	}

	// Merge summaries and, if still too big, do a final pass summarization
	merged := strings.Join(summaries, "\n\n")
	if len(merged) > maxChars {
		final, err := c.summarizeText(ctx, question, merged, "MERGE")
		if err == nil {
			// Ensure within limit
			if len(final) > maxChars {
				return final[:maxChars], nil
			}
			return final, nil
		}
		// Fallback: truncate merged summaries
		return merged[:maxChars], nil
	}
	return merged, nil
}

// summarizeChunk summarizes a single chunk with strong guidance
func (c *Client) summarizeChunk(ctx context.Context, question, chunk string, idx, total int) (string, error) {
	prompt := fmt.Sprintf(`You are condensing AWS investigation output to the essentials needed to answer the question: "%s".

CHUNK %d/%d. Create a concise, lossless summary with only:
- Specific service/function names, ARNs, and log group names
- Time ranges, timestamps, counts, metrics
- Errors/exceptions with messages and frequencies
- Alarms and states
- Any anomalies or patterns strongly related to the question

Remove boilerplate, headers, pagination, and duplicates. Keep it under 1500 words.

Content:\n%s`, question, idx, total, chunk)

	return c.dispatchLLM(ctx, prompt)
}

// summarizeText performs a final merge summarization
func (c *Client) summarizeText(ctx context.Context, question, text, mode string) (string, error) {
	prompt := fmt.Sprintf(`You are merging summarized AWS findings to answer: "%s".

Task: Combine the summaries into a single concise context preserving all concrete findings (names, timestamps, errors, counts, states). Remove duplicates. Keep it under 2000 words.

Mode: %s

Summaries:\n%s`, question, mode, text)
	return c.dispatchLLM(ctx, prompt)
}

// dispatchLLM routes a small prompt to the configured LLM provider
func (c *Client) dispatchLLM(ctx context.Context, prompt string) (string, error) {
	switch c.provider {
	case "bedrock", "claude":
		return c.askBedrock(ctx, prompt)
	case "openai":
		return c.askOpenAI(ctx, prompt)
	case "anthropic":
		return c.askAnthropic(ctx, prompt)
	case "gemini", "gemini-api":
		return c.askGemini(ctx, prompt)
	default:
		return c.askBedrock(ctx, prompt)
	}
}

// chunkString splits s into chunks up to size n runes (approx by bytes here)
func chunkString(s string, n int) []string {
	if n <= 0 || len(s) <= n {
		return []string{s}
	}
	chunks := make([]string, 0, (len(s)+n-1)/n)
	for start := 0; start < len(s); start += n {
		end := start + n
		if end > len(s) {
			end = len(s)
		}
		chunks = append(chunks, s[start:end])
	}
	return chunks
}

// sampleChunks selects up to k chunks evenly from the sequence, preserving start/end
func sampleChunks(chunks []string, k int) []string {
	if k <= 0 || len(chunks) <= k {
		return chunks
	}
	sampled := make([]string, 0, k)
	// Always include first and last
	sampled = append(sampled, chunks[0])
	sampled = append(sampled, chunks[len(chunks)-1])
	if k == 2 {
		return sampled
	}
	// Evenly sample remaining from the middle range
	remaining := k - 2
	step := float64(len(chunks)-2) / float64(remaining+1)
	used := map[int]bool{0: true, len(chunks) - 1: true}
	for i := 1; i <= remaining; i++ {
		idx := 1 + int(step*float64(i))
		if idx >= len(chunks)-1 {
			idx = len(chunks) - 2
		}
		if !used[idx] {
			sampled = append(sampled, chunks[idx])
			used[idx] = true
		}
	}
	return sampled
}

// sanitizeASCII strips non-ASCII runes to avoid CLI argv issues and provider limits
func sanitizeASCII(s string) string {
	// Fast path: if all bytes < 128
	allASCII := true
	for i := 0; i < len(s); i++ {
		if s[i] >= 128 {
			allASCII = false
			break
		}
	}
	if allASCII {
		return s
	}
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] < 128 {
			b = append(b, s[i])
		}
	}
	return string(b)
}

// findAgentProfile finds the appropriate AI profile for agent operations
func (c *Client) findAgentProfile(fallbackProfile string) *awsclient.AIProfile {
	// Try to find agent-specific profile first
	if c.awsClient != nil {
		profiles := c.awsClient.GetAIProfiles()

		// Look for agent-specific profiles
		for name, profile := range profiles {
			if strings.Contains(strings.ToLower(name), "agent") {
				return &profile
			}
		}

		// Look for LLM call profiles
		for name, profile := range profiles {
			if strings.Contains(strings.ToLower(name), "llm-call") {
				return &profile
			}
		}

		// Use the fallback profile if specified
		if fallbackProfile != "" {
			if profile, exists := profiles[fallbackProfile]; exists {
				return &profile
			}
		}

		// Use the default AI provider from config
		defaultProvider := viper.GetString("ai.default_provider")
		if defaultProvider != "" {
			if profile, exists := profiles[defaultProvider]; exists {
				return &profile
			}
		}

		// Use default profile as last resort
		if profile, exists := profiles["default"]; exists {
			return &profile
		}

		// If we have any profiles, use the first one
		for _, profile := range profiles {
			return &profile
		}
	}

	return nil
}
