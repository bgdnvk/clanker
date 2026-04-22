package railway

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// CreateRailwayCommands creates the Railway command tree for static commands.
// Registered from cmd/root.go as a sibling of `cf`, `do`, `hetzner`, `vercel`.
func CreateRailwayCommands() *cobra.Command {
	railwayCmd := &cobra.Command{
		Use:     "railway",
		Short:   "Query Railway projects, services, and deployments directly",
		Long:    "Query your Railway account without AI interpretation. Useful for getting raw data.",
		Aliases: []string{"rw"},
	}

	railwayCmd.PersistentFlags().String("api-token", "", "Railway API token (overrides RAILWAY_API_TOKEN)")
	railwayCmd.PersistentFlags().String("workspace-id", "", "Railway workspace ID (overrides RAILWAY_WORKSPACE_ID)")
	railwayCmd.PersistentFlags().Bool("raw", false, "Output raw JSON instead of formatted")

	railwayCmd.AddCommand(createRailwayListCmd())
	railwayCmd.AddCommand(createRailwayGetCmd())
	railwayCmd.AddCommand(createRailwayLogsCmd())
	railwayCmd.AddCommand(createRailwayAnalyticsCmd())
	railwayCmd.AddCommand(createRailwayDeployCmd())
	railwayCmd.AddCommand(createRailwayRedeployCmd())
	railwayCmd.AddCommand(createRailwayCancelCmd())
	railwayCmd.AddCommand(createRailwayVariableCmd())
	railwayCmd.AddCommand(createRailwayDomainCmd())
	railwayCmd.AddCommand(createRailwayEnvironmentCmd())

	return railwayCmd
}

func createRailwayListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list [resource]",
		Short: "List Railway resources",
		Long: `List Railway resources of a specific type.

Supported resources:
  projects        - Railway projects
  services        - Services (requires --project)
  deployments     - Deployments (supports --project / --service / --environment)
  domains         - Service + custom domains (requires --project)
  variables       - Environment variables (requires --project / --environment / --service)
  environments    - Project environments (requires --project)
  volumes         - Persistent volumes (requires --project)
  workspaces      - Workspaces accessible to the current token`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resource := strings.ToLower(args[0])
			projectID, _ := cmd.Flags().GetString("project")
			serviceID, _ := cmd.Flags().GetString("service")
			environmentID, _ := cmd.Flags().GetString("environment")

			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			switch resource {
			case "projects", "project":
				return listProjectsCmd(ctx, client)
			case "services", "service":
				if projectID == "" {
					return fmt.Errorf("--project is required to list services")
				}
				return listServicesCmd(ctx, client, projectID)
			case "deployments", "deployment":
				return listDeploymentsCmd(ctx, client, projectID, environmentID, serviceID)
			case "domains", "domain":
				if projectID == "" {
					return fmt.Errorf("--project is required to list domains")
				}
				return listDomainsCmd(ctx, client, projectID)
			case "variables", "variable", "vars", "var", "env":
				if projectID == "" || environmentID == "" {
					return fmt.Errorf("--project and --environment are required to list variables")
				}
				return listVariablesCmd(ctx, client, projectID, environmentID, serviceID)
			case "environments", "environment":
				if projectID == "" {
					return fmt.Errorf("--project is required to list environments")
				}
				return listEnvironmentsCmd(ctx, client, projectID)
			case "volumes", "volume":
				if projectID == "" {
					return fmt.Errorf("--project is required to list volumes")
				}
				return listVolumesCmd(ctx, client, projectID)
			case "workspaces", "workspace", "teams", "team":
				return listWorkspacesCmd(ctx, client)
			default:
				return fmt.Errorf("unknown resource type: %s", resource)
			}
		},
	}
	cmd.Flags().String("project", "", "Project ID (scopes deployments / services / env listings)")
	cmd.Flags().String("service", "", "Service ID (optional deployment/variable filter)")
	cmd.Flags().String("environment", "", "Environment ID (required for variables)")
	return cmd
}

func createRailwayGetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <project|service|deployment> <id>",
		Short: "Get a single Railway resource",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind := strings.ToLower(args[0])
			id := args[1]

			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			switch kind {
			case "project":
				return getProjectCmd(ctx, client, id)
			case "service":
				return getServiceCmd(ctx, client, id)
			case "deployment":
				return getDeploymentCmd(ctx, client, id)
			default:
				return fmt.Errorf("unknown resource kind: %s (expected project|service|deployment)", kind)
			}
		},
	}
	return cmd
}

func createRailwayLogsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logs <deploymentId>",
		Short: "Fetch build + runtime events for a deployment",
		Long: `Fetch events for a deployment. By default returns a one-shot snapshot of recent events.
Use --follow to stream events continuously (when supported by the transport).
Use --build to fetch build logs instead of runtime logs.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			deploymentID := args[0]
			follow, _ := cmd.Flags().GetBool("follow")
			build, _ := cmd.Flags().GetBool("build")
			limit, _ := cmd.Flags().GetInt("limit")

			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			if follow {
				// Streaming support requires the CLI — fall back to a snapshot
				// from the GraphQL API for parity with Vercel.
				fmt.Fprintln(cmd.OutOrStderr(), "[railway] --follow falls back to a snapshot via GraphQL API; use `railway logs` CLI for true streaming.")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			return getDeploymentLogsCmd(ctx, client, deploymentID, build, limit)
		},
	}
	cmd.Flags().Bool("follow", false, "Follow log output (snapshot in phase 1)")
	cmd.Flags().Bool("build", false, "Fetch build logs instead of runtime logs")
	cmd.Flags().Int("limit", 100, "Number of log entries to fetch")
	return cmd
}

func createRailwayAnalyticsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "analytics",
		Short: "Show recent usage summary (CPU, memory, network)",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			if client.GetWorkspaceID() == "" {
				return fmt.Errorf("analytics requires --workspace-id (or railway.workspace_id / RAILWAY_WORKSPACE_ID)")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			return getUsageCmd(ctx, client)
		},
	}
	return cmd
}

// newClientFromFlags resolves credentials from flags > config > env and
// builds a Client.
func newClientFromFlags(cmd *cobra.Command) (*Client, error) {
	apiToken, _ := cmd.Flags().GetString("api-token")
	if apiToken == "" {
		apiToken = ResolveAPIToken()
	}
	if apiToken == "" {
		return nil, fmt.Errorf("railway api_token is required (set railway.api_token, RAILWAY_API_TOKEN, or --api-token)")
	}

	workspaceID, _ := cmd.Flags().GetString("workspace-id")
	if workspaceID == "" {
		workspaceID = ResolveWorkspaceID()
	}

	debug := viper.GetBool("debug")
	client, err := NewClient(apiToken, workspaceID, debug)
	if err != nil {
		return nil, err
	}
	if raw, _ := cmd.Flags().GetBool("raw"); raw {
		client.raw = true
	}
	return client, nil
}

// rawOutput reports whether the user asked to print unformatted JSON.
func rawOutput(c *Client) bool {
	if c == nil {
		return false
	}
	return c.raw
}

// --- Listers ---

func listProjectsCmd(ctx context.Context, client *Client) error {
	projects, err := client.ListProjects(ctx)
	if err != nil {
		return err
	}
	if rawOutput(client) {
		return printJSON(projects)
	}
	if len(projects) == 0 {
		fmt.Println("No Railway projects found.")
		return nil
	}
	fmt.Printf("Railway Projects (%d):\n\n", len(projects))
	for _, p := range projects {
		fmt.Printf("  %s\n", p.Name)
		fmt.Printf("    ID: %s\n", p.ID)
		if p.Description != "" {
			fmt.Printf("    Description: %s\n", p.Description)
		}
		if p.TeamID != "" {
			fmt.Printf("    Team: %s\n", p.TeamID)
		}
		fmt.Println()
	}
	return nil
}

func listServicesCmd(ctx context.Context, client *Client, projectID string) error {
	services, err := client.ListServices(ctx, projectID)
	if err != nil {
		return err
	}
	if rawOutput(client) {
		return printJSON(services)
	}
	if len(services) == 0 {
		fmt.Printf("No services found for project %s.\n", projectID)
		return nil
	}
	fmt.Printf("Railway Services for project %s (%d):\n\n", projectID, len(services))
	for _, s := range services {
		fmt.Printf("  %s\n    ID: %s\n\n", s.Name, s.ID)
	}
	return nil
}

func listDeploymentsCmd(ctx context.Context, client *Client, projectID, environmentID, serviceID string) error {
	deployments, err := client.ListDeployments(ctx, projectID, environmentID, serviceID, 20)
	if err != nil {
		return err
	}
	if rawOutput(client) {
		return printJSON(deployments)
	}
	if len(deployments) == 0 {
		fmt.Println("No deployments found.")
		return nil
	}
	fmt.Printf("Railway Deployments (%d):\n\n", len(deployments))
	for _, d := range deployments {
		fmt.Printf("  %s  [%s]\n", d.ID, d.Status)
		if d.URL != "" {
			fmt.Printf("    URL: %s\n", d.URL)
		} else if d.StaticURL != "" {
			fmt.Printf("    URL: %s\n", d.StaticURL)
		}
		if d.ServiceID != "" {
			fmt.Printf("    Service: %s\n", d.ServiceID)
		}
		if d.Meta.CommitHash != "" {
			fmt.Printf("    Commit: %s (%s)\n", d.Meta.CommitHash, d.Meta.Branch)
		}
		fmt.Println()
	}
	return nil
}

func listDomainsCmd(ctx context.Context, client *Client, projectID string) error {
	domains, err := client.ListDomains(ctx, projectID)
	if err != nil {
		return err
	}
	if rawOutput(client) {
		return printJSON(domains)
	}
	if len(domains) == 0 {
		fmt.Println("No domains found.")
		return nil
	}
	fmt.Printf("Railway Domains for project %s (%d):\n\n", projectID, len(domains))
	for _, d := range domains {
		kind := "service"
		if d.IsCustom {
			kind = "custom"
		}
		fmt.Printf("  %s  [%s]\n", d.Domain, kind)
		if d.TargetPort > 0 {
			fmt.Printf("    Target port: %d\n", d.TargetPort)
		}
		if d.Status != "" {
			fmt.Printf("    Status: %s\n", d.Status)
		}
	}
	return nil
}

func listVariablesCmd(ctx context.Context, client *Client, projectID, environmentID, serviceID string) error {
	vars, err := client.ListVariables(ctx, projectID, environmentID, serviceID)
	if err != nil {
		return err
	}
	if rawOutput(client) {
		return printJSON(vars)
	}
	if len(vars) == 0 {
		fmt.Println("No variables found.")
		return nil
	}
	fmt.Printf("Railway variables (%d):\n\n", len(vars))
	for k := range vars {
		// Values intentionally omitted unless --raw.
		fmt.Printf("  %s\n", k)
	}
	return nil
}

func listEnvironmentsCmd(ctx context.Context, client *Client, projectID string) error {
	proj, err := client.GetProject(ctx, projectID)
	if err != nil {
		return err
	}
	if rawOutput(client) {
		return printJSON(proj.Environments)
	}
	if len(proj.Environments) == 0 {
		fmt.Println("No environments found.")
		return nil
	}
	fmt.Printf("Railway environments (%d):\n\n", len(proj.Environments))
	for _, e := range proj.Environments {
		fmt.Printf("  %s (%s)\n", e.Name, e.ID)
	}
	return nil
}

func listVolumesCmd(ctx context.Context, client *Client, projectID string) error {
	volumes, err := client.ListVolumes(ctx, projectID)
	if err != nil {
		return err
	}
	if rawOutput(client) {
		return printJSON(volumes)
	}
	if len(volumes) == 0 {
		fmt.Println("No volumes found.")
		return nil
	}
	fmt.Printf("Railway volumes (%d):\n\n", len(volumes))
	for _, v := range volumes {
		fmt.Printf("  %s (%s)\n", v.Name, v.ID)
		if v.MountPath != "" {
			fmt.Printf("    Mount: %s\n", v.MountPath)
		}
	}
	return nil
}

func listWorkspacesCmd(ctx context.Context, client *Client) error {
	workspaces, err := client.ListWorkspaces(ctx)
	if err != nil {
		return err
	}
	if rawOutput(client) {
		return printJSON(workspaces)
	}
	if len(workspaces) == 0 {
		fmt.Println("No workspaces found (personal account).")
		return nil
	}
	fmt.Printf("Railway Workspaces (%d):\n\n", len(workspaces))
	for _, w := range workspaces {
		fmt.Printf("  %s", w.Name)
		if w.Slug != "" {
			fmt.Printf(" (%s)", w.Slug)
		}
		fmt.Printf("\n    ID: %s\n\n", w.ID)
	}
	return nil
}

// --- Getters ---

func getProjectCmd(ctx context.Context, client *Client, id string) error {
	proj, err := client.GetProject(ctx, id)
	if err != nil {
		return err
	}
	if rawOutput(client) {
		return printJSON(proj)
	}
	fmt.Printf("Railway Project: %s\n\n", proj.Name)
	fmt.Printf("  ID: %s\n", proj.ID)
	if proj.Description != "" {
		fmt.Printf("  Description: %s\n", proj.Description)
	}
	if len(proj.Environments) > 0 {
		fmt.Println("  Environments:")
		for _, e := range proj.Environments {
			fmt.Printf("    - %s (%s)\n", e.Name, e.ID)
		}
	}
	if len(proj.Services) > 0 {
		fmt.Println("  Services:")
		for _, s := range proj.Services {
			fmt.Printf("    - %s (%s)\n", s.Name, s.ID)
		}
	}
	return nil
}

func getServiceCmd(ctx context.Context, client *Client, id string) error {
	q := `query Service($id: String!) { service(id: $id) { id name projectId createdAt updatedAt } }`
	var out struct {
		Service Service `json:"service"`
	}
	if err := client.RunGQL(ctx, q, map[string]any{"id": id}, &out); err != nil {
		return err
	}
	if rawOutput(client) {
		return printJSON(out.Service)
	}
	fmt.Printf("Railway Service: %s\n\n", out.Service.Name)
	fmt.Printf("  ID: %s\n", out.Service.ID)
	if out.Service.ProjectID != "" {
		fmt.Printf("  Project: %s\n", out.Service.ProjectID)
	}
	return nil
}

func getDeploymentCmd(ctx context.Context, client *Client, id string) error {
	d, err := client.GetDeployment(ctx, id)
	if err != nil {
		return err
	}
	if rawOutput(client) {
		return printJSON(d)
	}
	fmt.Printf("Railway Deployment: %s\n\n", d.ID)
	fmt.Printf("  Status: %s\n", d.Status)
	if d.URL != "" {
		fmt.Printf("  URL: %s\n", d.URL)
	}
	if d.StaticURL != "" {
		fmt.Printf("  Static URL: %s\n", d.StaticURL)
	}
	if d.ServiceID != "" {
		fmt.Printf("  Service: %s\n", d.ServiceID)
	}
	if d.Meta.CommitHash != "" {
		fmt.Printf("  Commit: %s (%s)\n", d.Meta.CommitHash, d.Meta.Branch)
	}
	return nil
}

func getDeploymentLogsCmd(ctx context.Context, client *Client, id string, buildLogs bool, limit int) error {
	entries, err := client.ListDeploymentLogs(ctx, id, buildLogs, limit)
	if err != nil {
		return err
	}
	if rawOutput(client) {
		return printJSON(entries)
	}
	if len(entries) == 0 {
		fmt.Println("No deployment log entries found.")
		return nil
	}
	kind := "runtime"
	if buildLogs {
		kind = "build"
	}
	fmt.Printf("Railway %s logs (%d entries):\n\n", kind, len(entries))
	for _, e := range entries {
		ts, _ := e["timestamp"].(string)
		msg, _ := e["message"].(string)
		sev, _ := e["severity"].(string)
		if ts != "" {
			fmt.Printf("  [%s] %-5s %s\n", ts, sev, msg)
		} else {
			fmt.Printf("  %-5s %s\n", sev, msg)
		}
	}
	return nil
}

func getUsageCmd(ctx context.Context, client *Client) error {
	usage, err := client.GetUsage(ctx, client.GetWorkspaceID())
	if err != nil {
		return err
	}
	return printJSON(usage)
}

// --- Deploy / Redeploy / Cancel ---

func createRailwayDeployCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "deploy [path]",
		Short: "Deploy the current directory (or a specific path) to Railway",
		Long: `Deploy by shelling out to 'railway up'. Use --service to target a specific
service and --environment to select a non-default environment. Pass --detach to return
immediately after scheduling the deploy.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}

			cliArgs := []string{"up"}
			if svc, _ := cmd.Flags().GetString("service"); svc != "" {
				cliArgs = append(cliArgs, "--service", svc)
			}
			if env, _ := cmd.Flags().GetString("environment"); env != "" {
				cliArgs = append(cliArgs, "--environment", env)
			}
			if detach, _ := cmd.Flags().GetBool("detach"); detach {
				cliArgs = append(cliArgs, "--detach")
			}
			if len(args) > 0 {
				cliArgs = append(cliArgs, "--path", args[0])
			}

			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
			defer cancel()

			out, err := client.RunRailwayCLI(ctx, cliArgs...)
			if err != nil {
				return err
			}
			fmt.Print(out)
			return nil
		},
	}
	cmd.Flags().StringP("service", "s", "", "Target service name or ID")
	cmd.Flags().StringP("environment", "e", "", "Target environment name or ID")
	cmd.Flags().Bool("detach", false, "Return immediately after scheduling the deploy")
	return cmd
}

func createRailwayRedeployCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "redeploy <deployment-id>",
		Short: "Redeploy an existing Railway deployment",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()

			if err := client.RedeployDeployment(ctx, args[0]); err != nil {
				return err
			}
			fmt.Printf("Redeploy triggered for %s\n", args[0])
			return nil
		},
	}
	return cmd
}

func createRailwayCancelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cancel <deployment-id>",
		Short: "Cancel an in-progress Railway deployment",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()

			if err := client.CancelDeployment(ctx, args[0]); err != nil {
				return err
			}
			fmt.Printf("Deployment %s cancelled\n", args[0])
			return nil
		},
	}
	return cmd
}

// --- Variable subcommands ---

func createRailwayVariableCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "variable",
		Aliases: []string{"variables", "var", "vars", "env"},
		Short:   "Manage Railway service environment variables",
	}
	cmd.PersistentFlags().String("project", "", "Project ID (required)")
	cmd.PersistentFlags().String("environment", "", "Environment ID (required)")
	cmd.PersistentFlags().String("service", "", "Service ID (optional — omit for shared vars)")

	cmd.AddCommand(createRailwayVariableSetCmd())
	cmd.AddCommand(createRailwayVariableRmCmd())
	cmd.AddCommand(createRailwayVariableLsCmd())
	return cmd
}

func createRailwayVariableSetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "set <KEY=VALUE>",
		Aliases: []string{"add"},
		Short:   "Set an environment variable (KEY=VALUE)",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			projectID, _ := cmd.Flags().GetString("project")
			environmentID, _ := cmd.Flags().GetString("environment")
			serviceID, _ := cmd.Flags().GetString("service")
			if projectID == "" || environmentID == "" {
				return fmt.Errorf("--project and --environment are required")
			}

			key, value, ok := strings.Cut(args[0], "=")
			if !ok || key == "" {
				return fmt.Errorf("expected KEY=VALUE, got %q", args[0])
			}

			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			if err := client.UpsertVariable(ctx, projectID, environmentID, serviceID, key, value); err != nil {
				return err
			}
			fmt.Printf("Variable %s set\n", key)
			return nil
		},
	}
	return cmd
}

func createRailwayVariableRmCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rm <KEY>",
		Short: "Remove an environment variable",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			projectID, _ := cmd.Flags().GetString("project")
			environmentID, _ := cmd.Flags().GetString("environment")
			serviceID, _ := cmd.Flags().GetString("service")
			if projectID == "" || environmentID == "" {
				return fmt.Errorf("--project and --environment are required")
			}
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			if err := client.DeleteVariable(ctx, projectID, environmentID, serviceID, args[0]); err != nil {
				return err
			}
			fmt.Printf("Variable %s removed\n", args[0])
			return nil
		},
	}
	return cmd
}

func createRailwayVariableLsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List environment variables",
		RunE: func(cmd *cobra.Command, args []string) error {
			projectID, _ := cmd.Flags().GetString("project")
			environmentID, _ := cmd.Flags().GetString("environment")
			serviceID, _ := cmd.Flags().GetString("service")
			if projectID == "" || environmentID == "" {
				return fmt.Errorf("--project and --environment are required")
			}
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			return listVariablesCmd(ctx, client, projectID, environmentID, serviceID)
		},
	}
	return cmd
}

// --- Domain subcommands ---

func createRailwayDomainCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "domain",
		Short: "Manage Railway custom domains",
	}
	cmd.PersistentFlags().String("project", "", "Project ID (required)")
	cmd.PersistentFlags().String("environment", "", "Environment ID (required for add)")
	cmd.PersistentFlags().String("service", "", "Service ID (required for add)")

	cmd.AddCommand(createRailwayDomainAddCmd())
	cmd.AddCommand(createRailwayDomainRmCmd())
	cmd.AddCommand(createRailwayDomainLsCmd())
	return cmd
}

func createRailwayDomainAddCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add <domain>",
		Short: "Add a custom domain to a service",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			environmentID, _ := cmd.Flags().GetString("environment")
			serviceID, _ := cmd.Flags().GetString("service")
			if environmentID == "" || serviceID == "" {
				return fmt.Errorf("--environment and --service are required")
			}
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			q := `mutation CustomDomainCreate($input: CustomDomainCreateInput!) {
				customDomainCreate(input: $input) { id domain }
			}`
			input := map[string]any{
				"domain":        args[0],
				"environmentId": environmentID,
				"serviceId":     serviceID,
			}
			if err := client.RunGQL(ctx, q, map[string]any{"input": input}, nil); err != nil {
				return err
			}
			fmt.Printf("Custom domain %s added\n", args[0])
			return nil
		},
	}
	return cmd
}

func createRailwayDomainRmCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rm <domain-id>",
		Short: "Remove a custom domain",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			q := `mutation CustomDomainDelete($id: String!) { customDomainDelete(id: $id) }`
			if err := client.RunGQL(ctx, q, map[string]any{"id": args[0]}, nil); err != nil {
				return err
			}
			fmt.Printf("Custom domain %s removed\n", args[0])
			return nil
		},
	}
	return cmd
}

func createRailwayDomainLsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List domains for a project",
		RunE: func(cmd *cobra.Command, args []string) error {
			projectID, _ := cmd.Flags().GetString("project")
			if projectID == "" {
				return fmt.Errorf("--project is required")
			}
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			return listDomainsCmd(ctx, client, projectID)
		},
	}
	return cmd
}

// --- Environment subcommands ---

func createRailwayEnvironmentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "environment",
		Aliases: []string{"env-cmd", "envs"},
		Short:   "Manage Railway environments",
	}
	cmd.PersistentFlags().String("project", "", "Project ID (required)")
	cmd.AddCommand(createRailwayEnvironmentNewCmd())
	cmd.AddCommand(createRailwayEnvironmentRmCmd())
	cmd.AddCommand(createRailwayEnvironmentLsCmd())
	return cmd
}

func createRailwayEnvironmentNewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "new <name>",
		Short: "Create a new environment",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			projectID, _ := cmd.Flags().GetString("project")
			if projectID == "" {
				return fmt.Errorf("--project is required")
			}
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			q := `mutation EnvironmentCreate($input: EnvironmentCreateInput!) {
				environmentCreate(input: $input) { id name }
			}`
			input := map[string]any{
				"projectId": projectID,
				"name":      args[0],
			}
			if err := client.RunGQL(ctx, q, map[string]any{"input": input}, nil); err != nil {
				return err
			}
			fmt.Printf("Environment %s created\n", args[0])
			return nil
		},
	}
	return cmd
}

func createRailwayEnvironmentRmCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rm <environment-id>",
		Short: "Delete an environment",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			q := `mutation EnvironmentDelete($id: String!) { environmentDelete(id: $id) }`
			if err := client.RunGQL(ctx, q, map[string]any{"id": args[0]}, nil); err != nil {
				return err
			}
			fmt.Printf("Environment %s deleted\n", args[0])
			return nil
		},
	}
	return cmd
}

func createRailwayEnvironmentLsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List environments for a project",
		RunE: func(cmd *cobra.Command, args []string) error {
			projectID, _ := cmd.Flags().GetString("project")
			if projectID == "" {
				return fmt.Errorf("--project is required")
			}
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			return listEnvironmentsCmd(ctx, client, projectID)
		},
	}
	return cmd
}

// printJSON prints v as pretty JSON.
func printJSON(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}
