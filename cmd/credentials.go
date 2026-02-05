package cmd

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/bgdnvk/clanker/internal/backend"
	"github.com/bgdnvk/clanker/internal/cloudflare"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var credentialsCmd = &cobra.Command{
	Use:   "credentials",
	Short: "Manage cloud credentials stored in clanker backend",
	Long: `Store, list, test, and delete cloud credentials in the clanker backend.

Credentials stored in the backend can be used across machines by providing
your API key via --api-key flag or CLANKER_BACKEND_API_KEY environment variable.

Examples:
  clanker credentials store aws --profile myaws
  clanker credentials list
  clanker credentials test aws
  clanker credentials delete aws`,
}

var credentialsStoreCmd = &cobra.Command{
	Use:   "store <provider>",
	Short: "Store credentials in the backend",
	Long: `Upload local credentials to the clanker backend.

Supported providers: aws, gcp, cloudflare, k8s

AWS:
  Exports credentials from local AWS CLI profile using 'aws configure export-credentials'.

GCP:
  Reads Application Default Credentials or specified service account file.

Cloudflare:
  Uses api_token and account_id from config or environment variables.

K8s:
  Uploads kubeconfig file content (base64 encoded).

Examples:
  clanker credentials store aws --profile dev
  clanker credentials store gcp --project myproject
  clanker credentials store cloudflare
  clanker credentials store k8s --kubeconfig ~/.kube/config`,
	Args: cobra.ExactArgs(1),
	RunE: runCredentialsStore,
}

var credentialsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List stored credentials",
	Long:  `List all credentials stored in the clanker backend for your account.`,
	RunE:  runCredentialsList,
}

var credentialsTestCmd = &cobra.Command{
	Use:   "test [provider]",
	Short: "Test stored credentials",
	Long: `Test that credentials stored in the backend are valid and working.

If no provider is specified, tests all stored credentials.

Examples:
  clanker credentials test aws
  clanker credentials test gcp
  clanker credentials test cloudflare
  clanker credentials test k8s
  clanker credentials test`,
	Args: cobra.MaximumNArgs(1),
	RunE: runCredentialsTest,
}

var credentialsDeleteCmd = &cobra.Command{
	Use:   "delete <provider>",
	Short: "Delete stored credentials",
	Long: `Delete credentials for a specific provider from the clanker backend.

Examples:
  clanker credentials delete aws
  clanker credentials delete gcp`,
	Args: cobra.ExactArgs(1),
	RunE: runCredentialsDelete,
}

func init() {
	rootCmd.AddCommand(credentialsCmd)
	credentialsCmd.AddCommand(credentialsStoreCmd)
	credentialsCmd.AddCommand(credentialsListCmd)
	credentialsCmd.AddCommand(credentialsTestCmd)
	credentialsCmd.AddCommand(credentialsDeleteCmd)

	// Store command flags
	credentialsStoreCmd.Flags().String("profile", "", "AWS profile to export credentials from")
	credentialsStoreCmd.Flags().String("project", "", "GCP project ID")
	credentialsStoreCmd.Flags().String("service-account", "", "GCP service account JSON file path")
	credentialsStoreCmd.Flags().String("kubeconfig", "", "Path to kubeconfig file (default: ~/.kube/config)")
	credentialsStoreCmd.Flags().String("context", "", "Kubernetes context name to use")
}

func requireAPIKey(cmd *cobra.Command) (string, error) {
	apiKeyFlag, _ := cmd.Flags().GetString("api-key")
	if apiKeyFlag == "" {
		apiKeyFlag, _ = cmd.Root().PersistentFlags().GetString("api-key")
	}
	apiKey := backend.ResolveAPIKey(apiKeyFlag)
	if apiKey == "" {
		return "", fmt.Errorf("API key required: use --api-key flag or set CLANKER_BACKEND_API_KEY")
	}
	return apiKey, nil
}

func runCredentialsStore(cmd *cobra.Command, args []string) error {
	provider := strings.ToLower(args[0])
	debug := viper.GetBool("debug")

	apiKey, err := requireAPIKey(cmd)
	if err != nil {
		return err
	}

	client := backend.NewClient(apiKey, debug)
	ctx := context.Background()

	switch provider {
	case "aws":
		return storeAWSCredentials(ctx, cmd, client)
	case "gcp":
		return storeGCPCredentials(ctx, cmd, client)
	case "cloudflare", "cf":
		return storeCloudflareCredentials(ctx, cmd, client)
	case "k8s", "kubernetes":
		return storeKubernetesCredentials(ctx, cmd, client)
	default:
		return fmt.Errorf("unsupported provider: %s (supported: aws, gcp, cloudflare, k8s)", provider)
	}
}

func storeAWSCredentials(ctx context.Context, cmd *cobra.Command, client *backend.Client) error {
	profile, _ := cmd.Flags().GetString("profile")
	if profile == "" {
		profile = "default"
	}

	fmt.Printf("Exporting AWS credentials from profile: %s\n", profile)

	// Get credentials using AWS CLI
	exportCmd := exec.CommandContext(ctx, "aws", "configure", "export-credentials", "--profile", profile, "--format", "process")
	output, err := exportCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to export AWS credentials: %w (make sure you are logged in with 'aws sso login --profile %s' or have valid credentials)", err, profile)
	}

	var cliCreds struct {
		AccessKeyId     string `json:"AccessKeyId"`
		SecretAccessKey string `json:"SecretAccessKey"`
		SessionToken    string `json:"SessionToken"`
	}
	if err := json.Unmarshal(output, &cliCreds); err != nil {
		return fmt.Errorf("failed to parse AWS credentials: %w", err)
	}

	// Get region
	regionCmd := exec.CommandContext(ctx, "aws", "configure", "get", "region", "--profile", profile)
	regionOutput, _ := regionCmd.Output()
	region := strings.TrimSpace(string(regionOutput))
	if region == "" {
		region = "us-east-1"
	}

	creds := &backend.AWSCredentials{
		AccessKeyID:     cliCreds.AccessKeyId,
		SecretAccessKey: cliCreds.SecretAccessKey,
		SessionToken:    cliCreds.SessionToken,
		Region:          region,
	}

	if err := client.StoreAWSCredentials(ctx, creds); err != nil {
		return fmt.Errorf("failed to store AWS credentials: %w", err)
	}

	fmt.Printf("AWS credentials stored successfully (region: %s)\n", region)
	return nil
}

func storeGCPCredentials(ctx context.Context, cmd *cobra.Command, client *backend.Client) error {
	projectID, _ := cmd.Flags().GetString("project")
	serviceAccountFile, _ := cmd.Flags().GetString("service-account")

	// Get project ID from flag, config, or environment
	if projectID == "" {
		projectID = viper.GetString("infra.gcp.project_id")
	}
	if projectID == "" {
		projectID = os.Getenv("GCP_PROJECT")
	}
	if projectID == "" {
		projectID = os.Getenv("GOOGLE_CLOUD_PROJECT")
	}
	if projectID == "" {
		return fmt.Errorf("GCP project ID required: use --project flag, set infra.gcp.project_id in config, or GCP_PROJECT env var")
	}

	var serviceAccountJSON string

	if serviceAccountFile != "" {
		// Read service account file
		data, err := os.ReadFile(serviceAccountFile)
		if err != nil {
			return fmt.Errorf("failed to read service account file: %w", err)
		}
		serviceAccountJSON = string(data)
		fmt.Printf("Using service account from: %s\n", serviceAccountFile)
	} else {
		// Try to read Application Default Credentials
		adcPath := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
		if adcPath == "" {
			homeDir, _ := os.UserHomeDir()
			adcPath = homeDir + "/.config/gcloud/application_default_credentials.json"
		}

		if data, err := os.ReadFile(adcPath); err == nil {
			serviceAccountJSON = string(data)
			fmt.Printf("Using Application Default Credentials from: %s\n", adcPath)
		} else {
			fmt.Println("No service account file found, storing project ID only")
		}
	}

	creds := &backend.GCPCredentials{
		ProjectID:          projectID,
		ServiceAccountJSON: serviceAccountJSON,
	}

	if err := client.StoreGCPCredentials(ctx, creds); err != nil {
		return fmt.Errorf("failed to store GCP credentials: %w", err)
	}

	fmt.Printf("GCP credentials stored successfully (project: %s)\n", projectID)
	return nil
}

func storeCloudflareCredentials(ctx context.Context, cmd *cobra.Command, client *backend.Client) error {
	apiToken := cloudflare.ResolveAPIToken()
	accountID := cloudflare.ResolveAccountID()

	if apiToken == "" {
		return fmt.Errorf("Cloudflare API token required: set cloudflare.api_token in config or CLOUDFLARE_API_TOKEN env var")
	}

	creds := &backend.CloudflareCredentials{
		APIToken:  apiToken,
		AccountID: accountID,
	}

	if err := client.StoreCloudflareCredentials(ctx, creds); err != nil {
		return fmt.Errorf("failed to store Cloudflare credentials: %w", err)
	}

	fmt.Println("Cloudflare credentials stored successfully")
	if accountID != "" {
		fmt.Printf("Account ID: %s\n", accountID)
	}
	return nil
}

func storeKubernetesCredentials(ctx context.Context, cmd *cobra.Command, client *backend.Client) error {
	kubeconfigPath, _ := cmd.Flags().GetString("kubeconfig")
	contextName, _ := cmd.Flags().GetString("context")

	// Default kubeconfig path
	if kubeconfigPath == "" {
		kubeconfigPath = viper.GetString("kubernetes.kubeconfig")
	}
	if kubeconfigPath == "" {
		kubeconfigPath = os.Getenv("KUBECONFIG")
	}
	if kubeconfigPath == "" {
		homeDir, _ := os.UserHomeDir()
		kubeconfigPath = homeDir + "/.kube/config"
	}

	// Read kubeconfig file
	data, err := os.ReadFile(kubeconfigPath)
	if err != nil {
		return fmt.Errorf("failed to read kubeconfig file: %w", err)
	}

	// Base64 encode the kubeconfig content
	encodedContent := base64.StdEncoding.EncodeToString(data)

	creds := &backend.KubernetesCredentials{
		KubeconfigContent: encodedContent,
		ContextName:       contextName,
	}

	if err := client.StoreKubernetesCredentials(ctx, creds); err != nil {
		return fmt.Errorf("failed to store Kubernetes credentials: %w", err)
	}

	fmt.Printf("Kubernetes credentials stored successfully (from: %s)\n", kubeconfigPath)
	if contextName != "" {
		fmt.Printf("Context: %s\n", contextName)
	}
	return nil
}

func runCredentialsList(cmd *cobra.Command, args []string) error {
	debug := viper.GetBool("debug")

	apiKey, err := requireAPIKey(cmd)
	if err != nil {
		return err
	}

	client := backend.NewClient(apiKey, debug)
	ctx := context.Background()

	creds, err := client.ListCredentials(ctx)
	if err != nil {
		return fmt.Errorf("failed to list credentials: %w", err)
	}

	if len(creds) == 0 {
		fmt.Println("No credentials stored.")
		fmt.Println("\nTo store credentials, use:")
		fmt.Println("  clanker credentials store aws --profile <profile>")
		fmt.Println("  clanker credentials store gcp --project <project>")
		fmt.Println("  clanker credentials store cloudflare")
		fmt.Println("  clanker credentials store k8s")
		return nil
	}

	fmt.Printf("Stored credentials (%d):\n\n", len(creds))
	for _, cred := range creds {
		fmt.Printf("Provider: %s\n", cred.Provider)
		fmt.Printf("  Created: %s\n", cred.CreatedAt.Format("2006-01-02 15:04:05"))
		fmt.Printf("  Updated: %s\n", cred.UpdatedAt.Format("2006-01-02 15:04:05"))
		if len(cred.Masked) > 0 {
			fmt.Println("  Fields:")
			for key, value := range cred.Masked {
				fmt.Printf("    %s: %s\n", key, value)
			}
		}
		fmt.Println()
	}

	return nil
}

func runCredentialsTest(cmd *cobra.Command, args []string) error {
	debug := viper.GetBool("debug")

	apiKey, err := requireAPIKey(cmd)
	if err != nil {
		return err
	}

	client := backend.NewClient(apiKey, debug)
	ctx := context.Background()

	// If provider specified, test only that one
	if len(args) > 0 {
		provider := strings.ToLower(args[0])
		return testCredential(ctx, client, backend.CredentialProvider(provider), debug)
	}

	// Test all stored credentials
	creds, err := client.ListCredentials(ctx)
	if err != nil {
		return fmt.Errorf("failed to list credentials: %w", err)
	}

	if len(creds) == 0 {
		fmt.Println("No credentials stored to test.")
		return nil
	}

	fmt.Printf("Testing %d stored credential(s)...\n\n", len(creds))
	allPassed := true
	for _, cred := range creds {
		if err := testCredential(ctx, client, cred.Provider, debug); err != nil {
			allPassed = false
		}
		fmt.Println()
	}

	if !allPassed {
		return fmt.Errorf("some credential tests failed")
	}
	return nil
}

func testCredential(ctx context.Context, client *backend.Client, provider backend.CredentialProvider, debug bool) error {
	fmt.Printf("Testing %s credentials...\n", provider)

	switch provider {
	case backend.ProviderAWS:
		return testAWSCredentials(ctx, client, debug)
	case backend.ProviderGCP:
		return testGCPCredentials(ctx, client, debug)
	case backend.ProviderCloudflare:
		return testCloudflareCredentials(ctx, client, debug)
	case backend.ProviderKubernetes:
		return testKubernetesCredentials(ctx, client, debug)
	default:
		return fmt.Errorf("unknown provider: %s", provider)
	}
}

func testAWSCredentials(ctx context.Context, client *backend.Client, debug bool) error {
	creds, err := client.GetAWSCredentials(ctx)
	if err != nil {
		fmt.Printf("  FAILED: %v\n", err)
		return err
	}

	// Test with aws sts get-caller-identity
	cmd := exec.CommandContext(ctx, "aws", "sts", "get-caller-identity", "--no-cli-pager")
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("AWS_ACCESS_KEY_ID=%s", creds.AccessKeyID),
		fmt.Sprintf("AWS_SECRET_ACCESS_KEY=%s", creds.SecretAccessKey),
	)
	if creds.SessionToken != "" {
		cmd.Env = append(cmd.Env, fmt.Sprintf("AWS_SESSION_TOKEN=%s", creds.SessionToken))
	}
	if creds.Region != "" {
		cmd.Env = append(cmd.Env, fmt.Sprintf("AWS_DEFAULT_REGION=%s", creds.Region))
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Printf("  FAILED: %v\n", err)
		if debug {
			fmt.Printf("  Output: %s\n", string(output))
		}
		return fmt.Errorf("AWS credential test failed")
	}

	// Parse the output to show account info
	var identity struct {
		Account string `json:"Account"`
		Arn     string `json:"Arn"`
	}
	if err := json.Unmarshal(output, &identity); err == nil {
		fmt.Printf("  PASSED: Account %s\n", identity.Account)
		if debug {
			fmt.Printf("  ARN: %s\n", identity.Arn)
		}
	} else {
		fmt.Println("  PASSED")
	}
	return nil
}

func testGCPCredentials(ctx context.Context, client *backend.Client, debug bool) error {
	creds, err := client.GetGCPCredentials(ctx)
	if err != nil {
		fmt.Printf("  FAILED: %v\n", err)
		return err
	}

	if creds.ProjectID == "" {
		fmt.Println("  FAILED: no project ID stored")
		return fmt.Errorf("no GCP project ID")
	}

	// If we have service account JSON, write it to temp file and test
	if creds.ServiceAccountJSON != "" {
		tmpFile, err := os.CreateTemp("", "gcp-creds-*.json")
		if err != nil {
			fmt.Printf("  FAILED: could not create temp file: %v\n", err)
			return err
		}
		defer os.Remove(tmpFile.Name())

		if _, err := tmpFile.WriteString(creds.ServiceAccountJSON); err != nil {
			tmpFile.Close()
			fmt.Printf("  FAILED: could not write temp file: %v\n", err)
			return err
		}
		tmpFile.Close()

		// Test with gcloud
		cmd := exec.CommandContext(ctx, "gcloud", "projects", "describe", creds.ProjectID, "--format", "value(projectId)")
		cmd.Env = append(os.Environ(),
			fmt.Sprintf("GOOGLE_APPLICATION_CREDENTIALS=%s", tmpFile.Name()),
		)

		output, err := cmd.CombinedOutput()
		if err != nil {
			fmt.Printf("  FAILED: %v\n", err)
			if debug {
				fmt.Printf("  Output: %s\n", string(output))
			}
			return fmt.Errorf("GCP credential test failed")
		}

		fmt.Printf("  PASSED: Project %s\n", strings.TrimSpace(string(output)))
	} else {
		// Just verify project exists (using default credentials)
		cmd := exec.CommandContext(ctx, "gcloud", "projects", "describe", creds.ProjectID, "--format", "value(projectId)")
		output, err := cmd.CombinedOutput()
		if err != nil {
			fmt.Printf("  PASSED: Project ID stored (%s), but could not verify: %v\n", creds.ProjectID, err)
			return nil
		}
		fmt.Printf("  PASSED: Project %s\n", strings.TrimSpace(string(output)))
	}
	return nil
}

func testCloudflareCredentials(ctx context.Context, client *backend.Client, debug bool) error {
	creds, err := client.GetCloudflareCredentials(ctx)
	if err != nil {
		fmt.Printf("  FAILED: %v\n", err)
		return err
	}

	if creds.APIToken == "" {
		fmt.Println("  FAILED: no API token stored")
		return fmt.Errorf("no Cloudflare API token")
	}

	// Test by verifying token
	cmd := exec.CommandContext(ctx, "curl", "-s",
		"https://api.cloudflare.com/client/v4/user/tokens/verify",
		"-H", fmt.Sprintf("Authorization: Bearer %s", creds.APIToken),
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Printf("  FAILED: %v\n", err)
		return err
	}

	var response struct {
		Success bool `json:"success"`
		Result  struct {
			Status string `json:"status"`
		} `json:"result"`
	}
	if err := json.Unmarshal(output, &response); err != nil {
		fmt.Printf("  FAILED: could not parse response\n")
		return err
	}

	if response.Success && response.Result.Status == "active" {
		fmt.Println("  PASSED: Token is active")
	} else {
		fmt.Printf("  FAILED: Token status: %s\n", response.Result.Status)
		return fmt.Errorf("Cloudflare token not active")
	}
	return nil
}

func testKubernetesCredentials(ctx context.Context, client *backend.Client, debug bool) error {
	creds, err := client.GetKubernetesCredentials(ctx)
	if err != nil {
		fmt.Printf("  FAILED: %v\n", err)
		return err
	}

	if creds.KubeconfigContent == "" {
		fmt.Println("  FAILED: no kubeconfig stored")
		return fmt.Errorf("no kubeconfig")
	}

	// Decode base64 kubeconfig
	decodedConfig, err := base64.StdEncoding.DecodeString(creds.KubeconfigContent)
	if err != nil {
		fmt.Printf("  FAILED: could not decode kubeconfig: %v\n", err)
		return err
	}

	// Write to temp file
	tmpFile, err := os.CreateTemp("", "kubeconfig-*.yaml")
	if err != nil {
		fmt.Printf("  FAILED: could not create temp file: %v\n", err)
		return err
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.Write(decodedConfig); err != nil {
		tmpFile.Close()
		fmt.Printf("  FAILED: could not write temp file: %v\n", err)
		return err
	}
	tmpFile.Close()

	// Test with kubectl cluster-info
	args := []string{"cluster-info", "--kubeconfig", tmpFile.Name()}
	if creds.ContextName != "" {
		args = append(args, "--context", creds.ContextName)
	}

	cmd := exec.CommandContext(ctx, "kubectl", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Printf("  FAILED: %v\n", err)
		if debug {
			fmt.Printf("  Output: %s\n", string(output))
		}
		return fmt.Errorf("Kubernetes credential test failed")
	}

	// Extract first line for summary
	lines := strings.Split(string(output), "\n")
	if len(lines) > 0 {
		fmt.Printf("  PASSED: %s\n", strings.TrimSpace(lines[0]))
	} else {
		fmt.Println("  PASSED")
	}
	return nil
}

func runCredentialsDelete(cmd *cobra.Command, args []string) error {
	provider := strings.ToLower(args[0])
	debug := viper.GetBool("debug")

	apiKey, err := requireAPIKey(cmd)
	if err != nil {
		return err
	}

	// Normalize provider name
	var credProvider backend.CredentialProvider
	switch provider {
	case "aws":
		credProvider = backend.ProviderAWS
	case "gcp":
		credProvider = backend.ProviderGCP
	case "cloudflare", "cf":
		credProvider = backend.ProviderCloudflare
	case "k8s", "kubernetes":
		credProvider = backend.ProviderKubernetes
	default:
		return fmt.Errorf("unsupported provider: %s (supported: aws, gcp, cloudflare, k8s)", provider)
	}

	client := backend.NewClient(apiKey, debug)
	ctx := context.Background()

	if err := client.DeleteCredential(ctx, credProvider); err != nil {
		return fmt.Errorf("failed to delete credentials: %w", err)
	}

	fmt.Printf("%s credentials deleted successfully\n", credProvider)
	return nil
}
