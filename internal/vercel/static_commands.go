package vercel

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// CreateVercelCommands creates the Vercel command tree for static commands.
// Registered from cmd/root.go as a sibling of `cf`, `do`, `hetzner`, etc.
func CreateVercelCommands() *cobra.Command {
	vercelCmd := &cobra.Command{
		Use:     "vercel",
		Short:   "Query Vercel projects and deployments directly",
		Long:    "Query your Vercel account without AI interpretation. Useful for getting raw data.",
		Aliases: []string{"vc"},
	}

	vercelCmd.PersistentFlags().String("api-token", "", "Vercel API token (overrides VERCEL_TOKEN)")
	vercelCmd.PersistentFlags().String("team-id", "", "Vercel team ID (overrides VERCEL_TEAM_ID)")

	vercelCmd.AddCommand(createVercelListCmd())
	vercelCmd.AddCommand(createVercelGetCmd())
	vercelCmd.AddCommand(createVercelLogsCmd())
	vercelCmd.AddCommand(createVercelAnalyticsCmd())

	return vercelCmd
}

func createVercelListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list [resource]",
		Short: "List Vercel resources",
		Long: `List Vercel resources of a specific type.

Supported resources:
  projects       - Vercel projects
  deployments    - Project deployments (supports --project)
  domains        - Custom domains
  env            - Environment variables (requires --project)
  teams          - Teams you belong to
  aliases        - Deployment aliases
  kv             - Vercel KV (Redis) stores
  blob           - Vercel Blob stores
  postgres       - Vercel Postgres databases
  edge-configs   - Edge Config stores`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resource := strings.ToLower(args[0])
			projectID, _ := cmd.Flags().GetString("project")

			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			switch resource {
			case "projects", "project":
				return listProjects(ctx, client)
			case "deployments", "deployment":
				return listDeployments(ctx, client, projectID)
			case "domains", "domain":
				return listDomains(ctx, client)
			case "env", "envs", "env-vars", "environment":
				if projectID == "" {
					return fmt.Errorf("--project is required to list env vars")
				}
				return listEnvVars(ctx, client, projectID)
			case "teams", "team":
				return listTeams(ctx, client)
			case "aliases", "alias":
				return listAliases(ctx, client, projectID)
			case "kv":
				return listKV(ctx, client)
			case "blob":
				return listBlob(ctx, client)
			case "postgres", "pg":
				return listPostgres(ctx, client)
			case "edge-configs", "edge-config", "edge":
				return listEdgeConfigs(ctx, client)
			default:
				return fmt.Errorf("unknown resource type: %s", resource)
			}
		},
	}
	cmd.Flags().String("project", "", "Project ID or name (scopes deployments / env listings)")
	return cmd
}

func createVercelGetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <project|deployment> <id>",
		Short: "Get a single Vercel resource",
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
				return getProject(ctx, client, id)
			case "deployment":
				return getDeployment(ctx, client, id)
			default:
				return fmt.Errorf("unknown resource kind: %s (expected project|deployment)", kind)
			}
		},
	}
	return cmd
}

func createVercelLogsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logs <deploymentId>",
		Short: "Fetch build + runtime events for a deployment",
		Long: `Fetch events for a deployment. By default returns a one-shot snapshot of recent events.
Use --follow (phase 3+) to stream events continuously.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			deploymentID := args[0]
			follow, _ := cmd.Flags().GetBool("follow")

			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			if follow {
				fmt.Fprintln(cmd.OutOrStderr(), "[vercel] --follow is not wired in phase 1 — returning the latest snapshot instead.")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			return getDeploymentEventsSnapshot(ctx, client, deploymentID)
		},
	}
	cmd.Flags().Bool("follow", false, "Stream events live (enabled in phase 3)")
	return cmd
}

func createVercelAnalyticsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "analytics",
		Short: "Show recent usage summary (bandwidth, invocations, build minutes)",
		RunE: func(cmd *cobra.Command, args []string) error {
			period, _ := cmd.Flags().GetString("period")

			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			if client.GetTeamID() == "" {
				return fmt.Errorf("analytics requires --team-id (or vercel.team_id / VERCEL_TEAM_ID)")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			return getUsage(ctx, client, period)
		},
	}
	cmd.Flags().String("period", "30d", "Time window (e.g. 7d, 30d, 90d)")
	cmd.Flags().String("project", "", "Optional project filter")
	return cmd
}

// newClientFromFlags resolves credentials from flags > config > env and builds a Client.
func newClientFromFlags(cmd *cobra.Command) (*Client, error) {
	apiToken, _ := cmd.Flags().GetString("api-token")
	if apiToken == "" {
		apiToken = ResolveAPIToken()
	}
	if apiToken == "" {
		return nil, fmt.Errorf("vercel api_token is required (set vercel.api_token, VERCEL_TOKEN, or --api-token)")
	}

	teamID, _ := cmd.Flags().GetString("team-id")
	if teamID == "" {
		teamID = ResolveTeamID()
	}

	debug := viper.GetBool("debug")
	return NewClient(apiToken, teamID, debug)
}

// --- Listers ---

func listProjects(ctx context.Context, client *Client) error {
	out, err := client.RunAPIWithContext(ctx, "GET", "/v9/projects?limit=100", "")
	if err != nil {
		return err
	}
	var resp struct {
		Projects []Project `json:"projects"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return fmt.Errorf("failed to parse projects response: %w", err)
	}
	if len(resp.Projects) == 0 {
		fmt.Println("No Vercel projects found.")
		return nil
	}
	fmt.Printf("Vercel Projects (%d):\n\n", len(resp.Projects))
	for _, p := range resp.Projects {
		fmt.Printf("  %s\n", p.Name)
		fmt.Printf("    ID: %s\n", p.ID)
		if p.Framework != "" {
			fmt.Printf("    Framework: %s\n", p.Framework)
		}
		if p.Link != nil && p.Link.Repo != "" {
			fmt.Printf("    Repo: %s (%s)\n", p.Link.Repo, p.Link.Type)
		}
		fmt.Println()
	}
	return nil
}

func listDeployments(ctx context.Context, client *Client, projectID string) error {
	endpoint := "/v6/deployments?limit=20"
	if projectID != "" {
		endpoint += "&projectId=" + projectID
	}
	out, err := client.RunAPIWithContext(ctx, "GET", endpoint, "")
	if err != nil {
		return err
	}
	var resp struct {
		Deployments []Deployment `json:"deployments"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return fmt.Errorf("failed to parse deployments response: %w", err)
	}
	if len(resp.Deployments) == 0 {
		fmt.Println("No deployments found.")
		return nil
	}
	fmt.Printf("Vercel Deployments (%d):\n\n", len(resp.Deployments))
	for _, d := range resp.Deployments {
		state := d.State
		if state == "" {
			state = d.ReadyState
		}
		target := d.Target
		if target == "" {
			target = "preview"
		}
		fmt.Printf("  %s  [%s / %s]\n", d.UID, state, target)
		if d.URL != "" {
			fmt.Printf("    URL: https://%s\n", d.URL)
		}
		if d.Creator != nil {
			fmt.Printf("    Creator: %s\n", d.Creator.Username)
		}
		fmt.Println()
	}
	return nil
}

func listDomains(ctx context.Context, client *Client) error {
	out, err := client.RunAPIWithContext(ctx, "GET", "/v5/domains?limit=100", "")
	if err != nil {
		return err
	}
	var resp struct {
		Domains []Domain `json:"domains"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return fmt.Errorf("failed to parse domains response: %w", err)
	}
	if len(resp.Domains) == 0 {
		fmt.Println("No custom domains found.")
		return nil
	}
	fmt.Printf("Vercel Domains (%d):\n\n", len(resp.Domains))
	for _, d := range resp.Domains {
		fmt.Printf("  %s  verified=%v\n", d.Name, d.Verified)
	}
	return nil
}

func listEnvVars(ctx context.Context, client *Client, projectID string) error {
	out, err := client.RunAPIWithContext(ctx, "GET", fmt.Sprintf("/v10/projects/%s/env", projectID), "")
	if err != nil {
		return err
	}
	var resp struct {
		Envs []EnvVar `json:"envs"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return fmt.Errorf("failed to parse env response: %w", err)
	}
	if len(resp.Envs) == 0 {
		fmt.Printf("No env vars found for project %s.\n", projectID)
		return nil
	}
	fmt.Printf("Env vars for project %s (%d):\n\n", projectID, len(resp.Envs))
	for _, e := range resp.Envs {
		targets := strings.Join(e.Target, ",")
		if targets == "" {
			targets = "-"
		}
		fmt.Printf("  %s  [%s]  targets=%s\n", e.Key, e.Type, targets)
	}
	return nil
}

func listTeams(ctx context.Context, client *Client) error {
	out, err := client.RunAPIWithContext(ctx, "GET", "/v2/teams?limit=50", "")
	if err != nil {
		return err
	}
	var resp struct {
		Teams []Team `json:"teams"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return fmt.Errorf("failed to parse teams response: %w", err)
	}
	if len(resp.Teams) == 0 {
		fmt.Println("No teams found (personal account).")
		return nil
	}
	fmt.Printf("Vercel Teams (%d):\n\n", len(resp.Teams))
	for _, t := range resp.Teams {
		fmt.Printf("  %s  (%s)\n    ID: %s\n\n", t.Name, t.Slug, t.ID)
	}
	return nil
}

func listAliases(ctx context.Context, client *Client, projectID string) error {
	endpoint := "/v4/aliases?limit=50"
	if projectID != "" {
		endpoint += "&projectId=" + projectID
	}
	out, err := client.RunAPIWithContext(ctx, "GET", endpoint, "")
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

func listKV(ctx context.Context, client *Client) error {
	// Vercel Storage API: GET /v1/storage/stores?storeType=kv
	return listStorage(ctx, client, "kv")
}

func listBlob(ctx context.Context, client *Client) error {
	return listStorage(ctx, client, "blob")
}

func listPostgres(ctx context.Context, client *Client) error {
	return listStorage(ctx, client, "postgres")
}

func listStorage(ctx context.Context, client *Client, storeType string) error {
	endpoint := fmt.Sprintf("/v1/storage/stores?storeType=%s", storeType)
	out, err := client.RunAPIWithContext(ctx, "GET", endpoint, "")
	if err != nil {
		return err
	}
	fmt.Printf("Vercel %s stores:\n%s\n", storeType, out)
	return nil
}

func listEdgeConfigs(ctx context.Context, client *Client) error {
	out, err := client.RunAPIWithContext(ctx, "GET", "/v1/edge-config", "")
	if err != nil {
		return err
	}
	fmt.Println("Vercel Edge Configs:")
	fmt.Println(out)
	return nil
}

// --- Getters ---

func getProject(ctx context.Context, client *Client, idOrName string) error {
	out, err := client.RunAPIWithContext(ctx, "GET", "/v9/projects/"+idOrName, "")
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

func getDeployment(ctx context.Context, client *Client, id string) error {
	out, err := client.RunAPIWithContext(ctx, "GET", "/v13/deployments/"+id, "")
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

func getDeploymentEventsSnapshot(ctx context.Context, client *Client, id string) error {
	out, err := client.RunAPIWithContext(ctx, "GET", "/v2/deployments/"+id+"/events?limit=200", "")
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

func getUsage(ctx context.Context, client *Client, period string) error {
	if period == "" {
		period = "30d"
	}
	endpoint := fmt.Sprintf("/v1/teams/%s/analytics/usage?period=%s", client.GetTeamID(), period)
	out, err := client.RunAPIWithContext(ctx, "GET", endpoint, "")
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}
