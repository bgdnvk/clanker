package cmd

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/bgdnvk/clanker/internal/clankercloud"
	"github.com/spf13/cobra"
)

const maxCLIAppInputBytes = 8 << 20

type appCreateFlags struct {
	description    string
	projectID      string
	metadataJSON   string
	idempotencyKey string
}

type appDeploymentFlags struct {
	html            string
	htmlFile        string
	filesJSON       string
	entrypoint      string
	spa             bool
	dataSummaryJSON string
	exposureJSON    string
	idempotencyKey  string
}

func newCloudAppsCmd() *cobra.Command {
	var apiBaseURL string
	var apiKey string
	client := func() *clankercloud.AppsClient {
		return clankercloud.NewAppsClient(clankercloud.AppsClientOptions{
			BaseURL:    apiBaseURL,
			AccountKey: apiKey,
		})
	}

	cmd := &cobra.Command{
		Use:     "apps",
		Aliases: []string{"app"},
		Short:   "Create, publish, roll back, unpublish, and delete Clanker Apps",
		Long: strings.TrimSpace(`Manage account-scoped Clanker Apps and immutable deployments.

Creating an app or deployment is private. Use activate only after reviewing the deployment you want to share publicly.`),
	}
	cmd.PersistentFlags().StringVar(&apiBaseURL, "api-base-url", "", "Clanker Cloud API base URL (HTTPS, or loopback HTTP for development)")
	cmd.PersistentFlags().StringVar(&apiKey, "api-key", "", "Clanker Cloud account API key (prefer CLANKER_CLOUD_API_KEY)")

	cmd.AddCommand(newCloudAppsListCmd(client))
	cmd.AddCommand(newCloudAppsCreateCmd(client))
	cmd.AddCommand(newCloudAppsGetCmd(client))
	cmd.AddCommand(newCloudAppsDeleteCmd(client))
	cmd.AddCommand(newCloudAppsDeploymentsCmd(client))
	cmd.AddCommand(newCloudAppsDeployCmd(client))
	cmd.AddCommand(newCloudAppsActivateCmd(client))
	cmd.AddCommand(newCloudAppsUnpublishCmd(client))
	return cmd
}

func newCloudAppsListCmd(client func() *clankercloud.AppsClient) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List account-owned apps",
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := client().ListApps(cmd.Context())
			return printAppsResult(result, err)
		},
	}
}

func newCloudAppsCreateCmd(client func() *clankercloud.AppsClient) *cobra.Command {
	var flags appCreateFlags
	cmd := &cobra.Command{
		Use:   "create NAME",
		Short: "Create a private app draft",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			metadata, err := readAppJSONObject(flags.metadataJSON)
			if err != nil {
				return fmt.Errorf("decode app metadata: %w", err)
			}
			idempotencyKey, err := resolveCLIAppIdempotencyKey(flags.idempotencyKey, "create")
			if err != nil {
				return err
			}
			result, err := client().CreateApp(cmd.Context(), clankercloud.AppCreateRequest{
				Name:           strings.TrimSpace(args[0]),
				Description:    strings.TrimSpace(flags.description),
				ProjectID:      strings.TrimSpace(flags.projectID),
				Metadata:       metadata,
				IdempotencyKey: idempotencyKey,
			})
			return printAppsResult(result, err)
		},
	}
	cmd.Flags().StringVar(&flags.description, "description", "", "optional app description")
	cmd.Flags().StringVar(&flags.projectID, "project-id", "", "optional Clanker Cloud project id")
	cmd.Flags().StringVar(&flags.metadataJSON, "metadata-json", "", "JSON object with non-secret app metadata, or @path")
	cmd.Flags().StringVar(&flags.idempotencyKey, "idempotency-key", "", "stable 8-128 character retry key (generated automatically when omitted)")
	return cmd
}

func newCloudAppsGetCmd(client func() *clankercloud.AppsClient) *cobra.Command {
	return &cobra.Command{
		Use:   "get APP_ID",
		Short: "Inspect an app",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := client().GetApp(cmd.Context(), args[0])
			return printAppsResult(result, err)
		},
	}
}

func newCloudAppsDeleteCmd(client func() *clankercloud.AppsClient) *cobra.Command {
	return &cobra.Command{
		Use:   "delete APP_ID",
		Short: "Delete an app and its retained deployments",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := client().DeleteApp(cmd.Context(), args[0])
			return printAppsResult(result, err)
		},
	}
}

func newCloudAppsDeploymentsCmd(client func() *clankercloud.AppsClient) *cobra.Command {
	return &cobra.Command{
		Use:     "deployments APP_ID",
		Aliases: []string{"versions"},
		Short:   "List immutable deployments for an app",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := client().ListDeployments(cmd.Context(), args[0])
			return printAppsResult(result, err)
		},
	}
}

func newCloudAppsDeployCmd(client func() *clankercloud.AppsClient) *cobra.Command {
	var flags appDeploymentFlags
	cmd := &cobra.Command{
		Use:   "deploy APP_ID",
		Short: "Create a private immutable deployment",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			input, err := flags.input()
			if err != nil {
				return err
			}
			idempotencyKey, err := resolveCLIAppIdempotencyKey(flags.idempotencyKey, "deploy")
			if err != nil {
				return err
			}
			result, err := client().CreateDeployment(cmd.Context(), args[0], clankercloud.AppDeploymentCreateRequest{
				AppDeploymentInput: input,
				IdempotencyKey:     idempotencyKey,
			})
			return printAppsResult(result, err)
		},
	}
	flags.addArtifactFlags(cmd)
	return cmd
}

func newCloudAppsActivateCmd(client func() *clankercloud.AppsClient) *cobra.Command {
	return &cobra.Command{
		Use:     "activate APP_ID DEPLOYMENT_ID",
		Aliases: []string{"publish", "rollback"},
		Short:   "Make one reviewed deployment public",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := client().ActivateDeployment(cmd.Context(), args[0], args[1])
			return printAppsResult(result, err)
		},
	}
}

func newCloudAppsUnpublishCmd(client func() *clankercloud.AppsClient) *cobra.Command {
	return &cobra.Command{
		Use:   "unpublish APP_ID",
		Short: "Remove public access while retaining app deployments",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := client().UnpublishApp(cmd.Context(), args[0])
			return printAppsResult(result, err)
		},
	}
}

func (flags *appDeploymentFlags) addArtifactFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&flags.html, "html", "", "single-file HTML content")
	cmd.Flags().StringVar(&flags.htmlFile, "html-file", "", "read single-file HTML content from this path")
	cmd.Flags().StringVar(&flags.filesJSON, "files-json", "", "JSON array of app files, or @path to a JSON file")
	cmd.Flags().StringVar(&flags.entrypoint, "entrypoint", "index.html", "deployment entrypoint")
	cmd.Flags().BoolVar(&flags.spa, "spa", false, "serve the entrypoint for unknown paths")
	cmd.Flags().StringVar(&flags.dataSummaryJSON, "data-summary-json", "", "JSON object describing included data, or @path")
	cmd.Flags().StringVar(&flags.exposureJSON, "exposure-json", "", "JSON object describing reviewed public data exposure, or @path")
	cmd.Flags().StringVar(&flags.idempotencyKey, "idempotency-key", "", "stable 8-128 character retry key (generated automatically when omitted)")
}

func (flags appDeploymentFlags) input() (clankercloud.AppDeploymentInput, error) {
	if strings.TrimSpace(flags.html) != "" && strings.TrimSpace(flags.htmlFile) != "" {
		return clankercloud.AppDeploymentInput{}, fmt.Errorf("--html and --html-file cannot be used together")
	}

	html := flags.html
	if path := strings.TrimSpace(flags.htmlFile); path != "" {
		content, err := readBoundedAppFile(path)
		if err != nil {
			return clankercloud.AppDeploymentInput{}, fmt.Errorf("read app HTML: %w", err)
		}
		html = string(content)
	}
	if len(html) > maxCLIAppInputBytes {
		return clankercloud.AppDeploymentInput{}, fmt.Errorf("inline app HTML exceeds %d bytes", maxCLIAppInputBytes)
	}

	var files []clankercloud.AppFile
	if raw := strings.TrimSpace(flags.filesJSON); raw != "" {
		data, err := readAppJSONArgument(raw)
		if err != nil {
			return clankercloud.AppDeploymentInput{}, fmt.Errorf("read app files JSON: %w", err)
		}
		if err := json.Unmarshal(data, &files); err != nil {
			return clankercloud.AppDeploymentInput{}, fmt.Errorf("decode app files JSON: %w", err)
		}
	}
	if (html != "") == (len(files) > 0) {
		return clankercloud.AppDeploymentInput{}, fmt.Errorf("provide exactly one of --html/--html-file or --files-json")
	}

	dataSummary, err := readAppJSONObject(flags.dataSummaryJSON)
	if err != nil {
		return clankercloud.AppDeploymentInput{}, fmt.Errorf("decode data summary: %w", err)
	}
	exposure, err := readAppJSONObject(flags.exposureJSON)
	if err != nil {
		return clankercloud.AppDeploymentInput{}, fmt.Errorf("decode exposure summary: %w", err)
	}

	return clankercloud.AppDeploymentInput{
		HTML:          html,
		Files:         files,
		Entrypoint:    strings.TrimSpace(flags.entrypoint),
		SPA:           flags.spa,
		DataSummary:   dataSummary,
		NetworkPolicy: "none",
		Exposure:      exposure,
	}, nil
}

func resolveCLIAppIdempotencyKey(raw string, operation string) (string, error) {
	if strings.TrimSpace(raw) != "" {
		return clankercloud.ValidateAppIdempotencyKey(raw)
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate app idempotency key: %w", err)
	}
	prefix := strings.Trim(strings.ToLower(strings.TrimSpace(operation)), "-")
	if prefix == "" {
		prefix = "request"
	}
	return clankercloud.ValidateAppIdempotencyKey("cli-" + prefix + "-" + hex.EncodeToString(random))
}

func readAppJSONObject(raw string) (map[string]any, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	data, err := readAppJSONArgument(raw)
	if err != nil {
		return nil, err
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, err
	}
	if decoded == nil {
		return nil, fmt.Errorf("expected a JSON object")
	}
	return decoded, nil
}

func readAppJSONArgument(raw string) ([]byte, error) {
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "@") {
		path := strings.TrimSpace(strings.TrimPrefix(trimmed, "@"))
		if path == "" {
			return nil, fmt.Errorf("JSON @path is empty")
		}
		return readBoundedAppFile(path)
	}
	if len(trimmed) > maxCLIAppInputBytes {
		return nil, fmt.Errorf("inline JSON exceeds %d bytes", maxCLIAppInputBytes)
	}
	return []byte(trimmed), nil
}

func readBoundedAppFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%s is a directory", path)
	}
	if info.Size() > maxCLIAppInputBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", path, maxCLIAppInputBytes)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(content) > maxCLIAppInputBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", path, maxCLIAppInputBytes)
	}
	return content, nil
}

func printAppsResult(result *clankercloud.AppsAPIResult, err error) error {
	if err != nil {
		return err
	}
	if err := printJSON(result); err != nil {
		return err
	}
	return clankercloud.AppsResultStatusError(result)
}

func init() {
	cloudCmd.AddCommand(newCloudAppsCmd())
}
