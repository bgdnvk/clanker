package cmd

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

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
	html                      string
	htmlFile                  string
	appSpecJSON               string
	appSpecFile               string
	agenticProviders          []string
	agenticDailyRequests      int
	agenticMaxInputCharacters int
	agenticMaxOutputTokens    int
	agenticLimitsSet          bool
	idempotencyKey            string
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

	cmd := newCloudAppsCommand(client)
	cmd.PersistentFlags().StringVar(&apiBaseURL, "api-base-url", "", "Clanker Cloud API base URL (HTTPS, or loopback HTTP for development)")
	cmd.PersistentFlags().StringVar(&apiKey, "api-key", "", "Clanker Cloud account API key (prefer CLANKER_CLOUD_API_KEY)")
	return cmd
}

func newCloudAppsCommand(client func() *clankercloud.AppsClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "apps",
		Aliases: []string{"app"},
		Short:   "Create, publish, roll back, unpublish, and delete Clanker Apps",
		Long: strings.TrimSpace(`Manage account-scoped Clanker Apps and immutable deployments.

Deploy complete HTML/CSS/JavaScript when the app needs normal browser features,
or use declarative appSpec v1 data for a fixed-renderer snapshot. Creating an
app or deployment is private. Activate a deployment to get a public link,
unpublish it to remove public access, or delete the app and all of its retained
deployments.`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

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
		Short: "Permanently delete an app and all retained deployments",
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
		Short:   "List immutable deployments and runtime details for an app",
		Long: strings.TrimSpace(`List every retained deployment for an app.

HTML deployments are identified as clanker-html-v1 and include fileCount,
totalBytes, networkPolicy, and any enabled agentic capability in the JSON
output.`),
		Args: cobra.ExactArgs(1),
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
		Short: "Create a private immutable HTML or declarative deployment",
		Long: strings.TrimSpace(`Create a private immutable deployment from exactly one runtime input.

Use --html or --html-file for a complete UTF-8 HTML document up to 2 MiB. Its
HTML, CSS, and JavaScript run as supplied, including browser storage, navigation,
external resources, and network calls subject to ordinary browser rules.

Use --app-spec-json or --app-spec-file for declarative appSpec v1 data up to
256 KiB. Supported block types are metrics, text, table, cards, and list.

HTML deployments can opt into the hosted agentic endpoint with one or two
repeatable --agentic-provider flags. Supported providers are gemini and kimi.
The versioned controls default to their launch maxima: 25 requests per app per
UTC day, 16000 input characters, and 1024 output tokens. The app-wide daily
counter continues across deployment activations. The deployed HTML calls the
root-stable /a/{slug}/__clanker/llm endpoint.`),
		Example: strings.TrimSpace(`clanker cloud apps deploy APP_ID --html-file ./index.html
clanker cloud apps deploy APP_ID --html '<!doctype html><script>document.body.textContent = "Hello"</script>'
clanker cloud apps deploy APP_ID --html-file ./agent.html --agentic-provider gemini --agentic-provider kimi
clanker cloud apps deploy APP_ID --app-spec-file ./app-spec.json`),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			flags.agenticLimitsSet =
				cmd.Flags().Changed("agentic-daily-requests") ||
					cmd.Flags().Changed("agentic-max-input-chars") ||
					cmd.Flags().Changed("agentic-max-output-tokens")
			payload, err := flags.input()
			if err != nil {
				return err
			}
			idempotencyKey, err := resolveCLIAppIdempotencyKey(flags.idempotencyKey, "deploy")
			if err != nil {
				return err
			}
			payload.IdempotencyKey = idempotencyKey
			result, err := client().CreateDeployment(cmd.Context(), args[0], payload)
			return printAppsResult(result, err)
		},
	}
	flags.addRuntimeFlags(cmd)
	return cmd
}

func newCloudAppsActivateCmd(client func() *clankercloud.AppsClient) *cobra.Command {
	return &cobra.Command{
		Use:     "activate APP_ID DEPLOYMENT_ID",
		Aliases: []string{"publish", "share", "rollback"},
		Short:   "Make one reviewed deployment public",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := client().ActivateDeployment(cmd.Context(), args[0], args[1])
			return printAppsActivationResult(cmd, result, err)
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

func (flags *appDeploymentFlags) addRuntimeFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&flags.html, "html", "", "complete single-file HTML/CSS/JavaScript content")
	cmd.Flags().StringVar(&flags.htmlFile, "html-file", "", "read complete single-file HTML/CSS/JavaScript from this path")
	cmd.Flags().StringVar(&flags.appSpecJSON, "app-spec-json", "", "declarative appSpec v1 JSON object")
	cmd.Flags().StringVar(&flags.appSpecFile, "app-spec-file", "", "read declarative appSpec v1 JSON from this path")
	cmd.Flags().StringArrayVar(&flags.agenticProviders, "agentic-provider", nil, "enable hosted app inference for this provider; repeat for gemini and kimi")
	cmd.Flags().IntVar(&flags.agenticDailyRequests, "agentic-daily-requests", clankercloud.DefaultAppAgenticDailyRequests, "agentic requests per app per UTC day (1-25)")
	cmd.Flags().IntVar(&flags.agenticMaxInputCharacters, "agentic-max-input-chars", clankercloud.DefaultAppAgenticInputCharacters, "maximum input characters per agentic request (1-16000)")
	cmd.Flags().IntVar(&flags.agenticMaxOutputTokens, "agentic-max-output-tokens", clankercloud.DefaultAppAgenticOutputTokens, "maximum output tokens per agentic request (1-1024)")
	cmd.Flags().StringVar(&flags.idempotencyKey, "idempotency-key", "", "stable 8-128 character retry key (generated automatically when omitted)")
	cmd.MarkFlagsMutuallyExclusive("html", "html-file", "app-spec-json", "app-spec-file")
	cmd.MarkFlagsOneRequired("html", "html-file", "app-spec-json", "app-spec-file")
}

func (flags appDeploymentFlags) input() (clankercloud.AppDeploymentCreateRequest, error) {
	sources := 0
	if flags.html != "" {
		sources++
	}
	if strings.TrimSpace(flags.htmlFile) != "" {
		sources++
	}
	if strings.TrimSpace(flags.appSpecJSON) != "" {
		sources++
	}
	if strings.TrimSpace(flags.appSpecFile) != "" {
		sources++
	}
	if sources != 1 {
		return clankercloud.AppDeploymentCreateRequest{}, fmt.Errorf("provide exactly one of --html, --html-file, --app-spec-json, or --app-spec-file")
	}

	agentic, err := flags.agenticConfig()
	if err != nil {
		return clankercloud.AppDeploymentCreateRequest{}, err
	}
	if flags.html != "" {
		html, err := validateCLIAppHTML([]byte(flags.html), "inline app HTML")
		if err != nil {
			return clankercloud.AppDeploymentCreateRequest{}, err
		}
		return clankercloud.AppDeploymentCreateRequest{HTML: &html, Agentic: agentic}, nil
	}
	if path := strings.TrimSpace(flags.htmlFile); path != "" {
		content, err := readBoundedAppFileWithLimit(path, clankercloud.MaxAppHTMLBytes)
		if err != nil {
			return clankercloud.AppDeploymentCreateRequest{}, fmt.Errorf("read app HTML: %w", err)
		}
		html, err := validateCLIAppHTML(content, "app HTML")
		if err != nil {
			return clankercloud.AppDeploymentCreateRequest{}, err
		}
		return clankercloud.AppDeploymentCreateRequest{HTML: &html, Agentic: agentic}, nil
	}
	if agentic != nil {
		return clankercloud.AppDeploymentCreateRequest{}, fmt.Errorf("agentic options are only supported with --html or --html-file")
	}

	var data []byte
	if path := strings.TrimSpace(flags.appSpecFile); path != "" {
		content, err := readBoundedAppFileWithLimit(path, clankercloud.MaxAppSpecBytes)
		if err != nil {
			return clankercloud.AppDeploymentCreateRequest{}, fmt.Errorf("read app spec JSON: %w", err)
		}
		data = content
	} else {
		raw := flags.appSpecJSON
		trimmed := strings.TrimSpace(raw)
		// Keep the branch's @path spelling working while the clearer
		// --app-spec-file flag becomes the documented interface.
		if strings.HasPrefix(trimmed, "@") {
			path := strings.TrimSpace(strings.TrimPrefix(trimmed, "@"))
			if path == "" {
				return clankercloud.AppDeploymentCreateRequest{}, fmt.Errorf("app spec JSON @path is empty")
			}
			content, err := readBoundedAppFileWithLimit(path, clankercloud.MaxAppSpecBytes)
			if err != nil {
				return clankercloud.AppDeploymentCreateRequest{}, fmt.Errorf("read app spec JSON: %w", err)
			}
			data = content
		} else {
			if len(raw) > clankercloud.MaxAppSpecBytes {
				return clankercloud.AppDeploymentCreateRequest{}, fmt.Errorf("inline app spec JSON exceeds %d bytes", clankercloud.MaxAppSpecBytes)
			}
			data = []byte(raw)
		}
	}
	spec, err := clankercloud.DecodeAppSpecJSON(data)
	if err != nil {
		return clankercloud.AppDeploymentCreateRequest{}, fmt.Errorf("decode app spec JSON: %w", err)
	}
	return clankercloud.AppDeploymentCreateRequest{AppSpec: &spec}, nil
}

func (flags appDeploymentFlags) agenticConfig() (*clankercloud.AppAgenticConfig, error) {
	if len(flags.agenticProviders) == 0 {
		if flags.agenticLimitsSet {
			return nil, fmt.Errorf("at least one --agentic-provider is required when setting agentic limits")
		}
		return nil, nil
	}
	config, err := clankercloud.ValidateAppAgenticConfig(clankercloud.AppAgenticConfig{
		Providers:          flags.agenticProviders,
		DailyRequestLimit:  flags.agenticDailyRequests,
		MaxInputCharacters: flags.agenticMaxInputCharacters,
		MaxOutputTokens:    flags.agenticMaxOutputTokens,
	})
	if err != nil {
		return nil, err
	}
	return &config, nil
}

func validateCLIAppHTML(content []byte, label string) (string, error) {
	if len(content) > clankercloud.MaxAppHTMLBytes {
		return "", fmt.Errorf("%s exceeds %d bytes", label, clankercloud.MaxAppHTMLBytes)
	}
	if !utf8.Valid(content) {
		return "", fmt.Errorf("%s must be valid UTF-8", label)
	}
	html := string(content)
	if len(html) == 0 {
		return "", fmt.Errorf("%s must not be empty", label)
	}
	return html, nil
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
	return readBoundedAppFileWithLimit(path, maxCLIAppInputBytes)
}

func readBoundedAppFileWithLimit(path string, limit int) ([]byte, error) {
	if limit < 0 {
		return nil, fmt.Errorf("invalid file size limit %d", limit)
	}
	// Reject ordinary directories, devices, and pipes before opening, then
	// recheck the descriptor to catch common path-replacement races. The
	// limited reader remains the authoritative byte bound.
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if info.Size() > int64(limit) {
		return nil, fmt.Errorf("%s exceeds %d bytes", path, limit)
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	openedInfo, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !openedInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if openedInfo.Size() > int64(limit) {
		return nil, fmt.Errorf("%s exceeds %d bytes", path, limit)
	}

	content, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(content) > limit {
		return nil, fmt.Errorf("%s exceeds %d bytes", path, limit)
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

func printAppsActivationResult(cmd *cobra.Command, result *clankercloud.AppsAPIResult, err error) error {
	if err := printAppsResult(result, err); err != nil {
		return err
	}
	if publicURL := appPublicURL(result); publicURL != "" {
		fmt.Fprintf(cmd.ErrOrStderr(), "Public URL: %s\n", publicURL)
	}
	return nil
}

func appPublicURL(result *clankercloud.AppsAPIResult) string {
	if result == nil {
		return ""
	}
	body, ok := result.Body.(map[string]any)
	if !ok {
		return ""
	}
	if app, ok := body["app"].(map[string]any); ok {
		if publicURL, ok := app["publicUrl"].(string); ok {
			return strings.TrimSpace(publicURL)
		}
	}
	publicURL, _ := body["publicUrl"].(string)
	return strings.TrimSpace(publicURL)
}

func init() {
	cloudCmd.AddCommand(newCloudAppsCmd())
}
