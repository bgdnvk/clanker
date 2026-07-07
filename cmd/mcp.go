package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/bgdnvk/clanker/internal/ai"
	"github.com/bgdnvk/clanker/internal/clankercloud"
	"github.com/bgdnvk/clanker/internal/flyio"
	"github.com/bgdnvk/clanker/internal/railway"
	"github.com/bgdnvk/clanker/internal/vercel"
	"github.com/bgdnvk/clanker/internal/verda"
	"github.com/mark3labs/mcp-go/mcp"
	mcptransport "github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

type routeArgs struct {
	Question string `json:"question"`
}

type versionArgs struct{}

type commandArgs struct {
	Args       []string `json:"args"`
	Profile    string   `json:"profile,omitempty"`
	BackendURL string   `json:"backendUrl,omitempty"`
	BackendEnv string   `json:"backendEnv,omitempty"`
	Debug      *bool    `json:"debug,omitempty"`
}

type cloudAppStatusArgs struct{}

type cloudAppLaunchArgs struct {
	AppPath        string `json:"appPath,omitempty" jsonschema:"description=Optional explicit path to the Clanker Cloud app or executable"`
	BundleID       string `json:"bundleId,omitempty" jsonschema:"description=Optional macOS bundle identifier override; defaults to com.clanker.cloud"`
	Wait           *bool  `json:"wait,omitempty" jsonschema:"description=Wait for the local app backend to become healthy; defaults to true"`
	TimeoutSeconds int    `json:"timeoutSeconds,omitempty" jsonschema:"description=How long to wait for the backend when wait=true; defaults to 120 seconds and caps at 900"`
}

type cloudAppAskArgs struct {
	Question string `json:"question" jsonschema:"description=Question to ask the running Clanker Cloud app,required"`
	Profile  string `json:"profile,omitempty" jsonschema:"description=Optional AWS profile override"`
}

type cloudBackendAPIArgs struct {
	Method   string            `json:"method" jsonschema:"description=HTTP method; defaults to GET"`
	Path     string            `json:"path" jsonschema:"description=Local Clanker Cloud backend path starting with /api/,required"`
	Query    map[string]string `json:"query,omitempty" jsonschema:"description=Optional query string parameters"`
	BodyJSON string            `json:"bodyJson,omitempty" jsonschema:"description=Optional JSON request body"`
	Profile  string            `json:"profile,omitempty" jsonschema:"description=Optional AWS profile header"`
}

type cloudStartScanArgs struct {
	Regions         []string `json:"regions,omitempty" jsonschema:"description=Regions to include in the regional scan. Defaults to the app/backend configured regions when available."`
	IncludeRegional *bool    `json:"includeRegional,omitempty" jsonschema:"description=Call the regional resources endpoint after /api/infrastructure; defaults to true."`
	ForceFresh      *bool    `json:"forceFresh,omitempty" jsonschema:"description=Invalidate the app infrastructure cache before scanning; defaults to true."`
	Profile         string   `json:"profile,omitempty" jsonschema:"description=Optional AWS profile header."`
}

type cloudPlanChangesArgs struct {
	Question    string `json:"question" jsonschema:"description=Natural-language infrastructure change to plan,required"`
	ContextBlob string `json:"contextBlob,omitempty" jsonschema:"description=Optional live context to include with the plan request."`
	Profile     string `json:"profile,omitempty" jsonschema:"description=Optional AWS profile header."`
}

type cloudApplyPlanArgs struct {
	Plan      map[string]any    `json:"plan,omitempty" jsonschema:"description=Plan object returned by clanker_cloud_plan_changes or the app Maker UI."`
	PlanJSON  string            `json:"planJson,omitempty" jsonschema:"description=Plan JSON string returned by clanker_cloud_plan_changes or the app Maker UI."`
	Profile   string            `json:"profile,omitempty" jsonschema:"description=Optional AWS profile header."`
	Approved  bool              `json:"approved" jsonschema:"description=Must be true after explicit user approval before applying infrastructure changes,required"`
	Destroyer bool              `json:"destroyer,omitempty" jsonschema:"description=Allow destructive delete/destroy actions in the approved plan."`
	EnvVars   map[string]string `json:"envVars,omitempty" jsonschema:"description=Optional environment variables for the apply process."`
	RunID     string            `json:"runId,omitempty" jsonschema:"description=Optional run id to associate with this apply stream."`
}

type vercelAskArgs struct {
	Question string `json:"question" jsonschema:"description=Natural language question about Vercel infrastructure,required"`
	Token    string `json:"token,omitempty" jsonschema:"description=Vercel API token (falls back to config/env)"`
	TeamID   string `json:"teamId,omitempty" jsonschema:"description=Vercel team ID"`
	Debug    bool   `json:"debug,omitempty" jsonschema:"description=Enable debug output"`
}

type vercelListArgs struct {
	Resource string `json:"resource" jsonschema:"description=Resource type: projects|deployments|domains|env|teams|aliases|kv|blob|postgres|edge-configs,required"`
	Token    string `json:"token,omitempty" jsonschema:"description=Vercel API token (falls back to config/env)"`
	TeamID   string `json:"teamId,omitempty" jsonschema:"description=Vercel team ID"`
	Project  string `json:"project,omitempty" jsonschema:"description=Project ID for scoped resources (deployments and env)"`
}

type flyioAskArgs struct {
	Question string `json:"question" jsonschema:"description=Natural language question about Fly.io infrastructure,required"`
	Token    string `json:"token,omitempty" jsonschema:"description=Fly.io API token (falls back to config/env)"`
	OrgSlug  string `json:"orgSlug,omitempty" jsonschema:"description=Fly.io org slug filter (empty = all orgs)"`
	Debug    bool   `json:"debug,omitempty" jsonschema:"description=Enable debug output"`
}

type flyioListArgs struct {
	Resource string `json:"resource" jsonschema:"description=Resource type: apps|machines|volumes|secrets|ips|certs|releases|orgs|regions|postgres|redis|tigris|extensions|tokens,required"`
	Token    string `json:"token,omitempty" jsonschema:"description=Fly.io API token (falls back to config/env)"`
	OrgSlug  string `json:"orgSlug,omitempty" jsonschema:"description=Fly.io org slug filter"`
	App      string `json:"app,omitempty" jsonschema:"description=App name for app-scoped resources (machines/volumes/secrets/ips/certs/releases)"`
}

type railwayAskArgs struct {
	Question    string `json:"question" jsonschema:"description=Natural language question about Railway infrastructure,required"`
	Token       string `json:"token,omitempty" jsonschema:"description=Railway API token (falls back to config/env)"`
	WorkspaceID string `json:"workspaceId,omitempty" jsonschema:"description=Railway workspace ID"`
	Debug       bool   `json:"debug,omitempty" jsonschema:"description=Enable debug output"`
}

type railwayListArgs struct {
	Resource    string `json:"resource" jsonschema:"description=Resource type: projects|services|deployments|domains|variables|volumes|workspaces,required"`
	Token       string `json:"token,omitempty" jsonschema:"description=Railway API token (falls back to config/env)"`
	WorkspaceID string `json:"workspaceId,omitempty" jsonschema:"description=Railway workspace ID"`
	Project     string `json:"project,omitempty" jsonschema:"description=Project ID for scoped resources (services, deployments, domains, variables, volumes)"`
	Environment string `json:"environment,omitempty" jsonschema:"description=Environment ID for scoping deployments/variables"`
	Service     string `json:"service,omitempty" jsonschema:"description=Service ID for scoping deployments/variables"`
}

type verdaAskArgs struct {
	Question     string `json:"question" jsonschema:"description=Natural language question about Verda Cloud (GPU/AI) infrastructure,required"`
	ClientID     string `json:"clientId,omitempty" jsonschema:"description=Verda OAuth2 client ID (falls back to config/env/credentials file)"`
	ClientSecret string `json:"clientSecret,omitempty" jsonschema:"description=Verda OAuth2 client secret (falls back to config/env/credentials file)"`
	ProjectID    string `json:"projectId,omitempty" jsonschema:"description=Verda project ID for conversation scoping"`
	Debug        bool   `json:"debug,omitempty" jsonschema:"description=Enable debug output"`
}

type verdaListArgs struct {
	Resource     string `json:"resource" jsonschema:"description=Resource type: instances|clusters|volumes|ssh-keys|scripts|instance-types|cluster-types|container-types|containers|jobs|secrets|file-secrets|registry-creds|locations|balance|images|cluster-images|availability,required"`
	ClientID     string `json:"clientId,omitempty" jsonschema:"description=Verda OAuth2 client ID (falls back to config/env/credentials file)"`
	ClientSecret string `json:"clientSecret,omitempty" jsonschema:"description=Verda OAuth2 client secret (falls back to config/env/credentials file)"`
	ProjectID    string `json:"projectId,omitempty" jsonschema:"description=Verda project ID"`
}

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Expose Clanker over MCP",
	RunE: func(cmd *cobra.Command, args []string) error {
		transport, _ := cmd.Flags().GetString("transport")
		listenAddr, _ := cmd.Flags().GetString("listen")

		server := newClankerMCPServer()

		switch strings.ToLower(strings.TrimSpace(transport)) {
		case "stdio":
			stdioServer := mcptransport.NewStdioServer(server)
			stdioServer.SetErrorLogger(nil)
			return stdioServer.Listen(cmd.Context(), os.Stdin, os.Stdout)
		case "http", "streamable-http":
			httpServer := mcptransport.NewStreamableHTTPServer(server, mcptransport.WithEndpointPath("/mcp"), mcptransport.WithStateLess(true))
			log.Printf("[mcp] clanker MCP listening on http://%s/mcp", listenAddr)
			return httpServer.Start(listenAddr)
		default:
			return fmt.Errorf("unsupported transport %q", transport)
		}
	},
}

func newClankerMCPServer() *mcptransport.MCPServer {
	server := mcptransport.NewMCPServer("clanker", Version)

	server.AddTool(
		mcp.NewTool(
			"clanker_version",
			mcp.WithDescription("Return the current Clanker CLI version."),
			mcp.WithInputSchema[versionArgs](),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, _ versionArgs) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultJSON(map[string]any{"version": Version})
		}),
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_route_question",
			mcp.WithDescription("Return which internal Clanker route should handle a question, including Clanker Cloud app requests."),
			mcp.WithInputSchema[routeArgs](),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args routeArgs) (*mcp.CallToolResult, error) {
			agent, reason := determineRoutingDecision(args.Question)
			return mcp.NewToolResultJSON(map[string]any{
				"agent":  agent,
				"reason": reason,
			})
		}),
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_run_command",
			mcp.WithDescription("Run the local Clanker CLI with the given arguments. Use this for ask, openclaw, codex-related helper flows, and other CLI features."),
			mcp.WithInputSchema[commandArgs](),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args commandArgs) (*mcp.CallToolResult, error) {
			result, err := runClankerCommand(ctx, args)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultJSON(result)
		}),
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_cloud_app_status",
			mcp.WithDescription("Report whether the Clanker Cloud desktop app backend is running, including the detected API base URL and MCP endpoint."),
			mcp.WithInputSchema[cloudAppStatusArgs](),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, _ cloudAppStatusArgs) (*mcp.CallToolResult, error) {
			client := clankercloud.NewClient()
			return mcp.NewToolResultJSON(client.Status(ctx))
		}),
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_cloud_launch_app",
			mcp.WithDescription("Launch the Clanker Cloud desktop app and optionally wait for the local backend MCP endpoint to become healthy."),
			mcp.WithInputSchema[cloudAppLaunchArgs](),
			mcp.WithDestructiveHintAnnotation(false),
			mcp.WithIdempotentHintAnnotation(true),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args cloudAppLaunchArgs) (*mcp.CallToolResult, error) {
			client := clankercloud.NewClient()
			result := client.LaunchApp(ctx, clankercloud.LaunchOptions{
				AppPath:        args.AppPath,
				BundleID:       args.BundleID,
				Wait:           boolDefault(args.Wait, true),
				TimeoutSeconds: args.TimeoutSeconds,
			})
			return mcp.NewToolResultJSON(result)
		}),
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_cloud_ask_app",
			mcp.WithDescription("Ask the running Clanker Cloud app a question through its local backend. Use clanker_cloud_launch_app first if the app is not running."),
			mcp.WithInputSchema[cloudAppAskArgs](),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args cloudAppAskArgs) (*mcp.CallToolResult, error) {
			question := strings.TrimSpace(args.Question)
			if question == "" {
				return mcp.NewToolResultError("question is required"), nil
			}
			client := clankercloud.NewClient()
			result, err := client.AskAgent(ctx, question, args.Profile)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultJSON(result)
		}),
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_cloud_call_backend_api",
			mcp.WithDescription("Call a local Clanker Cloud backend /api endpoint directly. Use this after clanker_cloud_launch_app when a specific app API is needed."),
			mcp.WithInputSchema[cloudBackendAPIArgs](),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args cloudBackendAPIArgs) (*mcp.CallToolResult, error) {
			var body []byte
			if strings.TrimSpace(args.BodyJSON) != "" {
				body = []byte(args.BodyJSON)
				var probe any
				if err := json.Unmarshal(body, &probe); err != nil {
					return mcp.NewToolResultError(fmt.Sprintf("bodyJson must be valid JSON: %v", err)), nil
				}
			}
			client := clankercloud.NewClient()
			result, err := client.CallAPI(ctx, args.Method, args.Path, args.Query, body, args.Profile)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultJSON(result)
		}),
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_cloud_start_scan",
			mcp.WithDescription("Run the current Clanker Cloud app backend scan path and wait for live API data. Invalidates the cache by default, reads /api/infrastructure, optionally reads /api/regions/resources, and returns current counts/errors."),
			mcp.WithInputSchema[cloudStartScanArgs](),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args cloudStartScanArgs) (*mcp.CallToolResult, error) {
			return handleMCPCloudStartScan(ctx, args)
		}),
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_cloud_plan_changes",
			mcp.WithDescription("Ask the running Clanker Cloud app to create a Maker plan for a requested infrastructure change using saved app settings. Read-only; return the plan for user review."),
			mcp.WithInputSchema[cloudPlanChangesArgs](),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args cloudPlanChangesArgs) (*mcp.CallToolResult, error) {
			return handleMCPCloudPlanChanges(ctx, args)
		}),
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_cloud_apply_plan",
			mcp.WithDescription("Apply a Clanker Cloud Maker plan through the running app backend. Requires approved=true after explicit user approval. Set destroyer=true only when destructive changes were approved."),
			mcp.WithInputSchema[cloudApplyPlanArgs](),
			mcp.WithDestructiveHintAnnotation(true),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args cloudApplyPlanArgs) (*mcp.CallToolResult, error) {
			return handleMCPCloudApplyPlan(ctx, args)
		}),
	)

	registerCloudSandboxMCPTools(server)

	server.AddTool(
		mcp.NewTool(
			"clanker_vercel_ask",
			mcp.WithDescription("Ask a natural language question about your Vercel infrastructure. Gathers Vercel context (projects, deployments, domains) and uses the configured AI provider to answer."),
			mcp.WithInputSchema[vercelAskArgs](),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args vercelAskArgs) (*mcp.CallToolResult, error) {
			return handleMCPVercelAsk(ctx, args)
		}),
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_vercel_list",
			mcp.WithDescription("List Vercel resources (projects, deployments, domains, env vars, teams, aliases, kv, blob, postgres, edge-configs). Returns raw JSON from the Vercel API."),
			mcp.WithInputSchema[vercelListArgs](),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args vercelListArgs) (*mcp.CallToolResult, error) {
			return handleMCPVercelList(ctx, args)
		}),
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_flyio_ask",
			mcp.WithDescription("Ask a natural language question about your Fly.io infrastructure. Gathers Fly.io context (apps, machines, volumes, regions) and uses the configured AI provider to answer."),
			mcp.WithInputSchema[flyioAskArgs](),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args flyioAskArgs) (*mcp.CallToolResult, error) {
			return handleMCPFlyioAsk(ctx, args)
		}),
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_flyio_list",
			mcp.WithDescription("List Fly.io resources (apps, machines, volumes, secrets, ips, certs, releases, orgs, regions, postgres, redis, tigris, extensions, tokens). Returns JSON from the Fly.io API."),
			mcp.WithInputSchema[flyioListArgs](),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args flyioListArgs) (*mcp.CallToolResult, error) {
			return handleMCPFlyioList(ctx, args)
		}),
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_railway_ask",
			mcp.WithDescription("Ask a natural language question about your Railway infrastructure. Gathers Railway context (projects, services, environments, deployments, domains) and uses the configured AI provider to answer."),
			mcp.WithInputSchema[railwayAskArgs](),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args railwayAskArgs) (*mcp.CallToolResult, error) {
			return handleMCPRailwayAsk(ctx, args)
		}),
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_railway_list",
			mcp.WithDescription("List Railway resources (projects, services, deployments, domains, variables, volumes, workspaces). Returns JSON with the requested resource list."),
			mcp.WithInputSchema[railwayListArgs](),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args railwayListArgs) (*mcp.CallToolResult, error) {
			return handleMCPRailwayList(ctx, args)
		}),
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_verda_ask",
			mcp.WithDescription("Ask a natural language question about your Verda Cloud (GPU/AI) infrastructure. Gathers Verda context (instances, clusters, volumes, balance) and uses the configured AI provider to answer."),
			mcp.WithInputSchema[verdaAskArgs](),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args verdaAskArgs) (*mcp.CallToolResult, error) {
			return handleMCPVerdaAsk(ctx, args)
		}),
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_verda_list",
			mcp.WithDescription("List Verda Cloud resources. Returns raw JSON from the Verda REST API."),
			mcp.WithInputSchema[verdaListArgs](),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args verdaListArgs) (*mcp.CallToolResult, error) {
			return handleMCPVerdaList(ctx, args)
		}),
	)

	registerCloudProviderMCPTools(server)
	registerSentryMCPTools(server)
	registerTencentMCPTools(server)
	registerLinearMCPTools(server)
	registerNotionMCPTools(server)
	registerK8sMCPTools(server)

	return server
}

func handleMCPCloudStartScan(ctx context.Context, args cloudStartScanArgs) (*mcp.CallToolResult, error) {
	client := clankercloud.NewClient()
	profile := strings.TrimSpace(args.Profile)
	forceFresh := boolDefault(args.ForceFresh, true)
	includeRegional := boolDefault(args.IncludeRegional, true)
	out := map[string]any{
		"mode":            "api",
		"forceFresh":      forceFresh,
		"includeRegional": includeRegional,
		"profile":         profile,
		"message":         "Scanning resources through the running Clanker Cloud backend; this can take several minutes.",
	}

	if forceFresh {
		invalidation, err := client.CallAPI(ctx, http.MethodPost, "/api/infrastructure/invalidate", nil, nil, profile)
		out["cacheInvalidation"] = cloudAPISummary(invalidation, err)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("cache invalidation failed: %v", err)), nil
		}
	}

	health, healthErr := client.CallAPI(ctx, http.MethodGet, "/api/health", nil, nil, profile)
	out["health"] = cloudAPISummary(health, healthErr)

	resourcesSummary, summaryErr := client.CallAPI(ctx, http.MethodGet, "/api/resources/summary", nil, nil, profile)
	out["resourcesSummary"] = cloudAPISummary(resourcesSummary, summaryErr)

	infrastructure, infraErr := client.CallAPI(ctx, http.MethodGet, "/api/infrastructure", nil, nil, profile)
	out["infrastructure"] = cloudScanBodySummary(infrastructure, infraErr)
	if infraErr != nil {
		return mcp.NewToolResultError(fmt.Sprintf("infrastructure scan failed: %v", infraErr)), nil
	}

	regions := normalizeCloudRegions(args.Regions)
	if includeRegional && len(regions) == 0 {
		regionsCatalog, regionsErr := client.CallAPI(ctx, http.MethodGet, "/api/regions", nil, nil, profile)
		out["regionsCatalog"] = cloudAPISummary(regionsCatalog, regionsErr)
		if regionsErr == nil {
			regions = cloudStringSlice(cloudBodyMap(regionsCatalog.Body)["regions"])
		}
	}
	out["regions"] = regions

	var regional *clankercloud.APICallResult
	var regionalErr error
	if includeRegional && len(regions) > 0 {
		payload, err := json.Marshal(map[string]any{"regions": regions})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("encode regional scan request: %v", err)), nil
		}
		regional, regionalErr = client.CallAPI(ctx, http.MethodPost, "/api/regions/resources", nil, payload, profile)
		out["regional"] = cloudScanBodySummary(regional, regionalErr)
	} else if includeRegional {
		out["regional"] = map[string]any{
			"ok":              true,
			"skippedReason":   "no regions configured or discovered",
			"resourceCount":   0,
			"selectedRegions": []string{},
		}
	}

	baseResources := cloudResources(infrastructure)
	regionalResources := cloudResources(regional)
	merged := mergeCloudResources(baseResources, regionalResources)
	out["resourceCount"] = len(merged)
	out["baseResourceCount"] = len(baseResources)
	out["regionalResourceCount"] = len(regionalResources)
	out["resourcesPreview"] = cloudResourcePreview(merged, 30)
	out["scanErrors"] = append(cloudScanErrors(infrastructure), cloudScanErrors(regional)...)
	out["completedAt"] = time.Now().UTC().Format(time.RFC3339)
	return mcp.NewToolResultJSON(out)
}

func handleMCPCloudPlanChanges(ctx context.Context, args cloudPlanChangesArgs) (*mcp.CallToolResult, error) {
	question := strings.TrimSpace(args.Question)
	if question == "" {
		return mcp.NewToolResultError("question is required"), nil
	}
	payload := map[string]any{
		"question":     question,
		"userQuestion": question,
		"contextBlob":  strings.TrimSpace(args.ContextBlob),
		"profile":      strings.TrimSpace(args.Profile),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("encode plan request: %v", err)), nil
	}
	client := clankercloud.NewClient()
	result, err := client.CallAPI(ctx, http.MethodPost, "/api/maker/plan", nil, body, args.Profile)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("maker plan failed: %v", err)), nil
	}
	return mcp.NewToolResultJSON(result)
}

func handleMCPCloudApplyPlan(ctx context.Context, args cloudApplyPlanArgs) (*mcp.CallToolResult, error) {
	if !args.Approved {
		return mcp.NewToolResultError("approved must be true after explicit user approval before applying infrastructure changes"), nil
	}
	plan, err := encodeCloudApplyPlan(args)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	payload := map[string]any{
		"plan":      json.RawMessage(plan),
		"profile":   strings.TrimSpace(args.Profile),
		"destroyer": args.Destroyer,
	}
	if len(args.EnvVars) > 0 {
		payload["envVars"] = args.EnvVars
	}
	if runID := strings.TrimSpace(args.RunID); runID != "" {
		payload["runId"] = runID
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("encode apply request: %v", err)), nil
	}
	client := clankercloud.NewClient()
	result, err := client.CallAPI(ctx, http.MethodPost, "/api/maker/apply", nil, body, args.Profile)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("maker apply failed: %v", err)), nil
	}
	return mcp.NewToolResultJSON(result)
}

func encodeCloudApplyPlan(args cloudApplyPlanArgs) ([]byte, error) {
	if len(args.Plan) > 0 {
		encoded, err := json.Marshal(args.Plan)
		if err != nil {
			return nil, fmt.Errorf("encode plan: %w", err)
		}
		return encoded, nil
	}
	trimmed := strings.TrimSpace(args.PlanJSON)
	if trimmed == "" {
		return nil, fmt.Errorf("plan or planJson is required")
	}
	var probe any
	if err := json.Unmarshal([]byte(trimmed), &probe); err != nil {
		return nil, fmt.Errorf("planJson must be valid JSON: %w", err)
	}
	return []byte(trimmed), nil
}

func cloudAPISummary(result *clankercloud.APICallResult, err error) map[string]any {
	summary := map[string]any{
		"ok":    err == nil && result != nil && result.Status >= 200 && result.Status < 300,
		"error": "",
	}
	if err != nil {
		summary["error"] = err.Error()
	}
	if result == nil {
		return summary
	}
	summary["status"] = result.Status
	if result.FinalMessage != "" {
		summary["finalMessage"] = result.FinalMessage
	}
	body := cloudBodyMap(result.Body)
	if result.Status < 200 || result.Status >= 300 {
		summary["error"] = firstNonEmpty(fmt.Sprint(summary["error"]), cloudString(body["error"]), cloudString(body["message"]), fmt.Sprintf("status %d", result.Status))
	}
	return summary
}

func cloudScanBodySummary(result *clankercloud.APICallResult, err error) map[string]any {
	summary := cloudAPISummary(result, err)
	if result == nil {
		return summary
	}
	body := cloudBodyMap(result.Body)
	resources := cloudResources(result)
	summary["resourceCount"] = len(resources)
	if totalCost, ok := body["totalCost"]; ok {
		summary["totalCost"] = totalCost
	}
	if partial, ok := body["partial"]; ok {
		summary["partial"] = partial
	}
	if scanErrors := cloudScanErrors(result); len(scanErrors) > 0 {
		summary["scanErrors"] = scanErrors
	}
	if providerStatus := cloudProviderStatus(body); len(providerStatus) > 0 {
		summary["providerStatus"] = providerStatus
	}
	return summary
}

func cloudBodyMap(value any) map[string]any {
	if values, ok := value.(map[string]any); ok {
		return values
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return map[string]any{}
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return map[string]any{}
	}
	return decoded
}

func cloudResources(result *clankercloud.APICallResult) []any {
	if result == nil {
		return nil
	}
	body := cloudBodyMap(result.Body)
	if resources, ok := body["resources"].([]any); ok {
		return resources
	}
	encoded, err := json.Marshal(body["resources"])
	if err != nil {
		return nil
	}
	var decoded []any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return nil
	}
	return decoded
}

func cloudScanErrors(result *clankercloud.APICallResult) []any {
	if result == nil {
		return nil
	}
	body := cloudBodyMap(result.Body)
	if errors, ok := body["scanErrors"].([]any); ok {
		return errors
	}
	return nil
}

func normalizeCloudRegions(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		region := strings.TrimSpace(value)
		if region == "" || seen[region] {
			continue
		}
		seen[region] = true
		out = append(out, region)
	}
	return out
}

func cloudStringSlice(value any) []string {
	switch typed := value.(type) {
	case []string:
		return normalizeCloudRegions(typed)
	case []any:
		values := make([]string, 0, len(typed))
		for _, item := range typed {
			values = append(values, fmt.Sprint(item))
		}
		return normalizeCloudRegions(values)
	default:
		return nil
	}
}

func mergeCloudResources(groups ...[]any) []any {
	seen := map[string]bool{}
	out := make([]any, 0)
	for _, group := range groups {
		for _, resource := range group {
			resourceMap := cloudBodyMap(resource)
			key := strings.ToLower(strings.Join([]string{
				firstNonEmpty(cloudString(resourceMap["id"]), cloudString(resourceMap["resourceId"]), cloudString(resourceMap["name"])),
				cloudString(resourceMap["type"]),
				firstNonEmpty(cloudString(resourceMap["region"]), cloudString(resourceMap["location"])),
				firstNonEmpty(cloudString(resourceMap["provider"]), cloudString(resourceMap["cloud"])),
			}, "|"))
			if key == "|||" {
				key = fmt.Sprintf("resource-%d", len(out))
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, resource)
		}
	}
	return out
}

func cloudResourcePreview(resources []any, limit int) []map[string]any {
	if limit <= 0 {
		limit = 30
	}
	if len(resources) < limit {
		limit = len(resources)
	}
	out := make([]map[string]any, 0, limit)
	for i := 0; i < limit; i++ {
		resource := cloudBodyMap(resources[i])
		out = append(out, map[string]any{
			"id":       firstNonEmpty(cloudString(resource["id"]), cloudString(resource["resourceId"])),
			"name":     firstNonEmpty(cloudString(resource["name"]), cloudString(resource["label"])),
			"type":     cloudString(resource["type"]),
			"provider": firstNonEmpty(cloudString(resource["provider"]), cloudString(resource["cloud"])),
			"region":   firstNonEmpty(cloudString(resource["region"]), cloudString(resource["location"])),
			"state":    firstNonEmpty(cloudString(resource["state"]), cloudString(resource["status"])),
		})
	}
	return out
}

func cloudProviderStatus(body map[string]any) []map[string]any {
	out := make([]map[string]any, 0)
	for key, value := range body {
		valueMap := cloudBodyMap(value)
		if len(valueMap) == 0 {
			continue
		}
		if _, ok := valueMap["attempted"]; !ok {
			if _, ok := valueMap["enabled"]; !ok {
				if _, ok := valueMap["included"]; !ok {
					if _, ok := valueMap["skipped"]; !ok {
						continue
					}
				}
			}
		}
		out = append(out, map[string]any{
			"provider":  key,
			"attempted": valueMap["attempted"],
			"enabled":   valueMap["enabled"],
			"included":  valueMap["included"],
			"skipped":   valueMap["skipped"],
			"error":     valueMap["error"],
		})
	}
	return out
}

func cloudString(value any) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// handleMCPVercelAsk resolves Vercel credentials, gathers context, and asks
// the configured AI provider about the user's Vercel infrastructure.
func handleMCPVercelAsk(ctx context.Context, args vercelAskArgs) (*mcp.CallToolResult, error) {
	token := args.Token
	if token == "" {
		token = vercel.ResolveAPIToken()
	}
	if token == "" {
		return mcp.NewToolResultError("Vercel token not configured. Set vercel.api_token in ~/.clanker.yaml or export VERCEL_TOKEN"), nil
	}

	teamID := args.TeamID
	if teamID == "" {
		teamID = vercel.ResolveTeamID()
	}

	client, err := vercel.NewClient(token, teamID, args.Debug)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to create Vercel client: %v", err)), nil
	}

	vercelContext, _ := client.GetRelevantContext(ctx, args.Question)

	prompt := buildVercelPrompt(args.Question, vercelContext, "")

	provider := viper.GetString("ai.default_provider")
	if provider == "" {
		provider = "openai"
	}
	apiKey := mcpResolveProviderKey(provider)

	aiClient := ai.NewClient(provider, apiKey, args.Debug, provider)

	response, err := aiClient.AskPrompt(ctx, prompt)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("AI query failed: %v", err)), nil
	}

	return mcp.NewToolResultText(response), nil
}

// handleMCPVercelList resolves Vercel credentials and lists the requested
// resource type, returning raw JSON from the Vercel API.
func handleMCPVercelList(ctx context.Context, args vercelListArgs) (*mcp.CallToolResult, error) {
	token := args.Token
	if token == "" {
		token = vercel.ResolveAPIToken()
	}
	if token == "" {
		return mcp.NewToolResultError("Vercel token not configured. Set vercel.api_token in ~/.clanker.yaml or export VERCEL_TOKEN"), nil
	}

	teamID := args.TeamID
	if teamID == "" {
		teamID = vercel.ResolveTeamID()
	}

	client, err := vercel.NewClient(token, teamID, false)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to create Vercel client: %v", err)), nil
	}

	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	resource := strings.ToLower(strings.TrimSpace(args.Resource))
	endpoint := ""

	switch resource {
	case "projects", "project":
		endpoint = "/v9/projects?limit=100"
	case "deployments", "deployment":
		endpoint = "/v6/deployments?limit=20"
		if args.Project != "" {
			endpoint += "&projectId=" + url.QueryEscape(args.Project)
		}
	case "domains", "domain":
		endpoint = "/v5/domains?limit=100"
	case "env", "envs", "env-vars", "environment":
		if args.Project == "" {
			return mcp.NewToolResultError("project is required to list env vars"), nil
		}
		endpoint = fmt.Sprintf("/v10/projects/%s/env", url.PathEscape(args.Project))
	case "teams", "team":
		endpoint = "/v2/teams?limit=50"
	case "aliases", "alias":
		endpoint = "/v4/aliases?limit=50"
		if args.Project != "" {
			endpoint += "&projectId=" + url.QueryEscape(args.Project)
		}
	case "kv":
		endpoint = "/v1/storage/stores?storeType=kv"
	case "blob":
		endpoint = "/v1/storage/stores?storeType=blob"
	case "postgres", "pg":
		endpoint = "/v1/storage/stores?storeType=postgres"
	case "edge-configs", "edge-config", "edge":
		endpoint = "/v1/edge-config"
	default:
		return mcp.NewToolResultError(fmt.Sprintf("unknown resource type: %s (expected: projects, deployments, domains, env, teams, aliases, kv, blob, postgres, edge-configs)", resource)), nil
	}

	result, err := client.RunAPIWithContext(ctx, "GET", endpoint, "")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Vercel API error: %v", err)), nil
	}

	return mcp.NewToolResultText(result), nil
}

// handleMCPFlyioAsk resolves Fly.io credentials, gathers context, and asks
// the configured AI provider about the user's Fly.io infrastructure.
func handleMCPFlyioAsk(ctx context.Context, args flyioAskArgs) (*mcp.CallToolResult, error) {
	token := args.Token
	if token == "" {
		token = flyio.ResolveAPIToken()
	}
	if token == "" {
		return mcp.NewToolResultError("Fly.io token not configured. Set flyio.api_token in ~/.clanker.yaml or export FLY_API_TOKEN"), nil
	}

	orgSlug := args.OrgSlug
	if orgSlug == "" {
		orgSlug = flyio.ResolveOrgSlug()
	}

	client, err := flyio.NewClient(token, orgSlug, args.Debug)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to create Fly.io client: %v", err)), nil
	}

	flyioContext, _ := client.GetRelevantContext(ctx, args.Question)

	prompt := buildFlyioPrompt(args.Question, flyioContext, "")

	provider := viper.GetString("ai.default_provider")
	if provider == "" {
		provider = "openai"
	}
	apiKey := mcpResolveProviderKey(provider)

	aiClient := ai.NewClient(provider, apiKey, args.Debug, provider)

	response, err := aiClient.AskPrompt(ctx, prompt)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("AI query failed: %v", err)), nil
	}

	return mcp.NewToolResultText(response), nil
}

// handleMCPFlyioList resolves Fly.io credentials and lists the requested
// resource type. App-scoped resources require the App argument. Returns the
// raw JSON response from either the REST Machines API or the GraphQL endpoint
// depending on the resource.
func handleMCPFlyioList(ctx context.Context, args flyioListArgs) (*mcp.CallToolResult, error) {
	token := args.Token
	if token == "" {
		token = flyio.ResolveAPIToken()
	}
	if token == "" {
		return mcp.NewToolResultError("Fly.io token not configured. Set flyio.api_token in ~/.clanker.yaml or export FLY_API_TOKEN"), nil
	}

	orgSlug := args.OrgSlug
	if orgSlug == "" {
		orgSlug = flyio.ResolveOrgSlug()
	}

	client, err := flyio.NewClient(token, orgSlug, false)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to create Fly.io client: %v", err)), nil
	}

	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	resource := strings.ToLower(strings.TrimSpace(args.Resource))

	// App-scoped REST resources need --app.
	appScoped := map[string]string{
		"machines": "/machines",
		"machine":  "/machines",
		"volumes":  "/volumes",
		"volume":   "/volumes",
		"secrets":  "/secrets",
		"ips":      "/ips",
		"ip":       "/ips",
		"certs":    "/certificates",
		"cert":     "/certificates",
		"releases": "/releases",
		"release":  "/releases",
	}
	if suffix, ok := appScoped[resource]; ok {
		if args.App == "" {
			return mcp.NewToolResultError(fmt.Sprintf("app is required to list %s", resource)), nil
		}
		result, err := client.RunAPIWithContext(ctx, "GET", "/apps/"+url.PathEscape(args.App)+suffix, "")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Fly.io API error: %v", err)), nil
		}
		return mcp.NewToolResultText(result), nil
	}

	// Top-level REST resources.
	switch resource {
	case "apps", "app":
		result, err := client.RunAPIWithContext(ctx, "GET", "/apps", "")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Fly.io API error: %v", err)), nil
		}
		return mcp.NewToolResultText(result), nil
	case "regions", "region":
		result, err := client.RunAPIWithContext(ctx, "GET", "/platform/regions", "")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Fly.io API error: %v", err)), nil
		}
		return mcp.NewToolResultText(result), nil
	}

	// GraphQL-backed resources.
	var query string
	switch resource {
	case "orgs", "org", "organizations", "organization":
		query = `query { organizations { nodes { id slug name type paidPlan } } }`
	case "postgres", "pg":
		query = `query { apps(role: "postgres_cluster") { nodes { id name status organization { slug } } } }`
	case "redis":
		query = `query { addOns(type: "upstash_redis") { nodes { id name primaryRegion readRegions plan { name } status } } }`
	case "tigris":
		query = `query { addOns(type: "tigris") { nodes { id name primaryRegion } } }`
	case "extensions", "extension":
		query = `query { addOns(type: ["sentry","tigris","upstash_redis","planetscale"]) { nodes { id name } } }`
	case "tokens", "token":
		query = `query { viewer { personalAccountTokens { nodes { id name expiresAt } } } }`
	default:
		return mcp.NewToolResultError(fmt.Sprintf("unknown resource type: %s (expected: apps, machines, volumes, secrets, ips, certs, releases, orgs, regions, postgres, redis, tigris, extensions, tokens)", resource)), nil
	}

	result, err := client.RunGraphQL(ctx, query, nil)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Fly.io GraphQL error: %v", err)), nil
	}
	return mcp.NewToolResultText(result), nil
}

// handleMCPRailwayAsk resolves Railway credentials, gathers context, and asks
// the configured AI provider about the user's Railway infrastructure.
func handleMCPRailwayAsk(ctx context.Context, args railwayAskArgs) (*mcp.CallToolResult, error) {
	token := args.Token
	if token == "" {
		token = railway.ResolveAPIToken()
	}
	if token == "" {
		return mcp.NewToolResultError("Railway token not configured. Set railway.api_token in ~/.clanker.yaml or export RAILWAY_API_TOKEN"), nil
	}

	workspaceID := args.WorkspaceID
	if workspaceID == "" {
		workspaceID = railway.ResolveWorkspaceID()
	}

	client, err := railway.NewClient(token, workspaceID, args.Debug)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to create Railway client: %v", err)), nil
	}

	railwayContext, _ := client.GetRelevantContext(ctx, args.Question)

	prompt := buildRailwayPrompt(args.Question, railwayContext, "")

	provider := viper.GetString("ai.default_provider")
	if provider == "" {
		provider = "openai"
	}
	apiKey := mcpResolveProviderKey(provider)

	aiClient := ai.NewClient(provider, apiKey, args.Debug, provider)

	response, err := aiClient.AskPrompt(ctx, prompt)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("AI query failed: %v", err)), nil
	}

	return mcp.NewToolResultText(response), nil
}

// handleMCPRailwayList resolves Railway credentials and lists the requested
// resource type using the Railway GraphQL client.
func handleMCPRailwayList(ctx context.Context, args railwayListArgs) (*mcp.CallToolResult, error) {
	token := args.Token
	if token == "" {
		token = railway.ResolveAPIToken()
	}
	if token == "" {
		return mcp.NewToolResultError("Railway token not configured. Set railway.api_token in ~/.clanker.yaml or export RAILWAY_API_TOKEN"), nil
	}

	workspaceID := args.WorkspaceID
	if workspaceID == "" {
		workspaceID = railway.ResolveWorkspaceID()
	}

	client, err := railway.NewClient(token, workspaceID, false)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to create Railway client: %v", err)), nil
	}

	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	resource := strings.ToLower(strings.TrimSpace(args.Resource))

	switch resource {
	case "projects", "project":
		projects, err := client.ListProjects(ctx)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Railway API error: %v", err)), nil
		}
		return mcp.NewToolResultJSON(projects)
	case "services", "service":
		if args.Project == "" {
			return mcp.NewToolResultError("project is required to list services"), nil
		}
		services, err := client.ListServices(ctx, args.Project)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Railway API error: %v", err)), nil
		}
		return mcp.NewToolResultJSON(services)
	case "deployments", "deployment":
		if args.Project == "" {
			return mcp.NewToolResultError("project is required to list deployments"), nil
		}
		deployments, err := client.ListDeployments(ctx, args.Project, args.Environment, args.Service, 20)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Railway API error: %v", err)), nil
		}
		return mcp.NewToolResultJSON(deployments)
	case "domains", "domain":
		if args.Project == "" {
			return mcp.NewToolResultError("project is required to list domains"), nil
		}
		domains, err := client.ListDomains(ctx, args.Project)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Railway API error: %v", err)), nil
		}
		return mcp.NewToolResultJSON(domains)
	case "variables", "variable", "vars", "env":
		if args.Project == "" || args.Environment == "" || args.Service == "" {
			return mcp.NewToolResultError("project, environment, and service are required to list variables"), nil
		}
		variables, err := client.ListVariables(ctx, args.Project, args.Environment, args.Service)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Railway API error: %v", err)), nil
		}
		// Scrub values — never return secrets through MCP.
		scrubbed := make(map[string]string, len(variables))
		for k := range variables {
			scrubbed[k] = ""
		}
		return mcp.NewToolResultJSON(scrubbed)
	case "volumes", "volume":
		if args.Project == "" {
			return mcp.NewToolResultError("project is required to list volumes"), nil
		}
		volumes, err := client.ListVolumes(ctx, args.Project)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Railway API error: %v", err)), nil
		}
		return mcp.NewToolResultJSON(volumes)
	case "workspaces", "workspace":
		workspaces, err := client.ListWorkspaces(ctx)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Railway API error: %v", err)), nil
		}
		return mcp.NewToolResultJSON(workspaces)
	default:
		return mcp.NewToolResultError(fmt.Sprintf("unknown resource type: %s (expected: projects, services, deployments, domains, variables, volumes, workspaces)", resource)), nil
	}
}

// handleMCPVerdaAsk resolves Verda credentials, gathers context, and asks the
// configured AI provider about the user's Verda Cloud infrastructure.
func handleMCPVerdaAsk(ctx context.Context, args verdaAskArgs) (*mcp.CallToolResult, error) {
	clientID := args.ClientID
	if clientID == "" {
		clientID = verda.ResolveClientID()
	}
	clientSecret := args.ClientSecret
	if clientSecret == "" {
		clientSecret = verda.ResolveClientSecret()
	}
	if clientID == "" || clientSecret == "" {
		return mcp.NewToolResultError("Verda credentials not configured. Set verda.client_id / verda.client_secret in ~/.clanker.yaml, export VERDA_CLIENT_ID / VERDA_CLIENT_SECRET, or run `verda auth login`"), nil
	}
	projectID := args.ProjectID
	if projectID == "" {
		projectID = verda.ResolveProjectID()
	}

	client, err := verda.NewClient(clientID, clientSecret, projectID, args.Debug)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to create Verda client: %v", err)), nil
	}

	verdaContext, _ := client.GetRelevantContext(ctx, args.Question)

	prompt := buildVerdaPrompt(args.Question, verdaContext, "")

	provider := viper.GetString("ai.default_provider")
	if provider == "" {
		provider = "openai"
	}
	apiKey := mcpResolveProviderKey(provider)
	aiClient := ai.NewClient(provider, apiKey, args.Debug, provider)

	response, err := aiClient.AskPrompt(ctx, prompt)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("AI query failed: %v", err)), nil
	}
	return mcp.NewToolResultText(response), nil
}

// handleMCPVerdaList resolves Verda credentials and lists the requested resource type.
func handleMCPVerdaList(ctx context.Context, args verdaListArgs) (*mcp.CallToolResult, error) {
	clientID := args.ClientID
	if clientID == "" {
		clientID = verda.ResolveClientID()
	}
	clientSecret := args.ClientSecret
	if clientSecret == "" {
		clientSecret = verda.ResolveClientSecret()
	}
	if clientID == "" || clientSecret == "" {
		return mcp.NewToolResultError("Verda credentials not configured. Set verda.client_id / verda.client_secret in ~/.clanker.yaml, export VERDA_CLIENT_ID / VERDA_CLIENT_SECRET, or run `verda auth login`"), nil
	}
	projectID := args.ProjectID
	if projectID == "" {
		projectID = verda.ResolveProjectID()
	}

	client, err := verda.NewClient(clientID, clientSecret, projectID, false)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to create Verda client: %v", err)), nil
	}

	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	resource := strings.ToLower(strings.TrimSpace(args.Resource))
	var path string
	switch resource {
	case "instances", "instance", "vm", "vms":
		path = "/v1/instances"
	case "clusters", "cluster":
		path = "/v1/clusters"
	case "volumes", "volume":
		path = "/v1/volumes"
	case "ssh-keys", "ssh", "keys":
		path = "/v1/ssh-keys"
	case "scripts", "startup-scripts":
		path = "/v1/scripts?pageSize=100"
	case "instance-types", "types":
		path = "/v1/instance-types"
	case "cluster-types":
		path = "/v1/cluster-types"
	case "container-types":
		path = "/v1/container-types"
	case "containers", "container-deployments":
		path = "/v1/container-deployments"
	case "jobs", "job-deployments":
		path = "/v1/job-deployments"
	case "secrets":
		path = "/v1/secrets"
	case "file-secrets":
		path = "/v1/file-secrets"
	case "registry-creds", "registry-credentials":
		path = "/v1/container-registry-credentials"
	case "locations":
		path = "/v1/locations"
	case "balance":
		path = "/v1/balance"
	case "images":
		path = "/v1/images"
	case "cluster-images":
		path = "/v1/images/cluster"
	case "availability":
		path = "/v1/instance-availability"
	default:
		return mcp.NewToolResultError(fmt.Sprintf("unknown resource type %q. Supported: instances, clusters, volumes, ssh-keys, scripts, instance-types, cluster-types, container-types, containers, jobs, secrets, file-secrets, registry-creds, locations, balance, images, cluster-images, availability", resource)), nil
	}

	result, err := client.RunAPIWithContext(ctx, "GET", path, "")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Verda API error: %v", err)), nil
	}
	return mcp.NewToolResultText(result), nil
}

// mcpResolveProviderKey resolves the API key for the given AI provider using
// config and environment variables. Mirrors the resolution logic in ask.go.
func mcpResolveProviderKey(provider string) string {
	switch provider {
	case "bedrock", "claude", "gemini", "gemini-api":
		return ""
	case "openai":
		return resolveOpenAIKey("")
	case "anthropic":
		return resolveAnthropicKey("")
	case "cohere":
		return resolveCohereKey("")
	case "deepseek":
		return resolveDeepSeekKey("")
	case "minimax":
		return resolveMiniMaxKey("")
	default:
		return viper.GetString(fmt.Sprintf("ai.providers.%s.api_key", provider))
	}
}

func runClankerCommand(ctx context.Context, args commandArgs) (map[string]any, error) {
	if len(args.Args) == 0 {
		return nil, fmt.Errorf("args are required")
	}

	cleanArgs := make([]string, 0, len(args.Args)+6)
	for _, arg := range args.Args {
		trimmed := strings.TrimSpace(arg)
		if trimmed == "" {
			continue
		}
		cleanArgs = append(cleanArgs, trimmed)
	}
	if len(cleanArgs) == 0 {
		return nil, fmt.Errorf("args are required")
	}
	if cleanArgs[0] == "mcp" {
		return nil, fmt.Errorf("refusing to recursively invoke clanker mcp")
	}

	if profile := strings.TrimSpace(args.Profile); profile != "" {
		cleanArgs = append(cleanArgs, "--profile", profile)
	}
	if backendURL := strings.TrimSpace(args.BackendURL); backendURL != "" {
		cleanArgs = append(cleanArgs, "--backend-url", backendURL)
	}
	if backendEnv := strings.TrimSpace(args.BackendEnv); backendEnv != "" {
		cleanArgs = append(cleanArgs, "--backend-env", backendEnv)
	}
	if args.Debug != nil && *args.Debug {
		cleanArgs = append(cleanArgs, "--debug")
		viper.Set("debug", true)
	}

	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve clanker executable: %w", err)
	}

	cmd := exec.CommandContext(ctx, exe, cleanArgs...)
	output, err := cmd.CombinedOutput()
	result := map[string]any{
		"command":   append([]string{exe}, cleanArgs...),
		"output":    string(output),
		"exitError": err != nil,
	}
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result["exitCode"] = exitErr.ExitCode()
		} else {
			result["exitCode"] = -1
		}
		return result, fmt.Errorf("clanker command failed: %w", err)
	}
	result["exitCode"] = 0
	return result, nil
}

func boolDefault(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

func init() {
	rootCmd.AddCommand(mcpCmd)
	mcpCmd.Flags().String("transport", "http", "MCP transport to use: http or stdio")
	mcpCmd.Flags().String("listen", "127.0.0.1:39393", "Listen address for HTTP MCP transport")
}
