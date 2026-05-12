package flyio

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// CreateFlyioCommands creates the Fly.io command tree for static commands.
// Registered from cmd/root.go as a sibling of `cf`, `do`, `hetzner`, `vercel`, etc.
//
// The top-level command uses `Use: "fly"` with `Aliases: []string{"flyio"}` so
// users can type either `clanker fly ...` (matching the flyctl convention) or
// `clanker flyio ...`. The flag for routing into ask mode is `--flyio` (matches
// `--vercel`, `--digitalocean`, etc.).
func CreateFlyioCommands() *cobra.Command {
	flyioCmd := &cobra.Command{
		Use:     "fly",
		Short:   "Query and manage Fly.io apps, machines, and addons",
		Long:    "Query your Fly.io org directly without AI interpretation. Useful for getting raw data and applying scripted changes. Most commands hit the Machines REST API; deploys and SSH use the local `flyctl` binary.",
		Aliases: []string{"flyio"},
	}

	flyioCmd.PersistentFlags().String("api-token", "", "Fly.io API token (overrides FLY_API_TOKEN)")
	flyioCmd.PersistentFlags().String("org", "", "Fly.io org slug filter (overrides FLY_ORG)")
	flyioCmd.PersistentFlags().String("app", "", "Default app name for commands scoped to a single app")
	flyioCmd.PersistentFlags().Bool("raw", false, "Output raw JSON instead of formatted")

	flyioCmd.AddCommand(createFlyioListCmd())
	flyioCmd.AddCommand(createFlyioGetCmd())
	flyioCmd.AddCommand(createFlyioLogsCmd())
	flyioCmd.AddCommand(createFlyioDeployCmd())
	flyioCmd.AddCommand(createFlyioRedeployCmd())
	flyioCmd.AddCommand(createFlyioRollbackCmd())
	flyioCmd.AddCommand(createFlyioRestartCmd())
	flyioCmd.AddCommand(createFlyioStartCmd())
	flyioCmd.AddCommand(createFlyioStopCmd())
	flyioCmd.AddCommand(createFlyioSuspendCmd())
	flyioCmd.AddCommand(createFlyioDestroyCmd())
	flyioCmd.AddCommand(createFlyioCloneCmd())
	flyioCmd.AddCommand(createFlyioCordonCmd())
	flyioCmd.AddCommand(createFlyioUncordonCmd())
	flyioCmd.AddCommand(createFlyioExecCmd())
	flyioCmd.AddCommand(createFlyioScaleCmd())
	flyioCmd.AddCommand(createFlyioSecretsCmd())
	flyioCmd.AddCommand(createFlyioIPsCmd())
	flyioCmd.AddCommand(createFlyioCertsCmd())
	flyioCmd.AddCommand(createFlyioVolumesCmd())
	flyioCmd.AddCommand(createFlyioReleasesCmd())
	flyioCmd.AddCommand(createFlyioPostgresCmd())
	flyioCmd.AddCommand(createFlyioMPGCmd())
	flyioCmd.AddCommand(createFlyioRedisCmd())
	flyioCmd.AddCommand(createFlyioTigrisCmd())
	flyioCmd.AddCommand(createFlyioMysqlCmd())
	flyioCmd.AddCommand(createFlyioRegionsCmd())
	flyioCmd.AddCommand(createFlyioOrgsCmd())
	flyioCmd.AddCommand(createFlyioAuthCmd())
	flyioCmd.AddCommand(createFlyioTokensCmd())
	flyioCmd.AddCommand(createFlyioWireGuardCmd())
	flyioCmd.AddCommand(createFlyioExtensionsCmd())

	return flyioCmd
}

// newClientFromFlags resolves credentials from flags > config > env and builds a Client.
func newClientFromFlags(cmd *cobra.Command) (*Client, error) {
	apiToken, _ := cmd.Flags().GetString("api-token")
	if apiToken == "" {
		apiToken = ResolveAPIToken()
	}
	if apiToken == "" {
		return nil, fmt.Errorf("flyio api_token is required (set flyio.api_token, FLY_API_TOKEN, or --api-token)")
	}

	orgSlug, _ := cmd.Flags().GetString("org")
	if orgSlug == "" {
		orgSlug = ResolveOrgSlug()
	}

	debug := viper.GetBool("debug")
	client, err := NewClient(apiToken, orgSlug, debug)
	if err != nil {
		return nil, err
	}
	if raw, _ := cmd.Flags().GetBool("raw"); raw {
		client.SetRaw(true)
	}
	return client, nil
}

// resolveAppFlag returns the --app flag value, falling back to the default
// when the persistent flag is empty.
func resolveAppFlag(cmd *cobra.Command) string {
	return strings.TrimSpace(getFlag(cmd, "app"))
}

func getFlag(cmd *cobra.Command, name string) string {
	v, _ := cmd.Flags().GetString(name)
	return v
}

func requireApp(cmd *cobra.Command) (string, error) {
	app := resolveAppFlag(cmd)
	if app == "" {
		return "", fmt.Errorf("--app is required for this command")
	}
	return app, nil
}

// rawOutput reports whether the user asked to print unformatted JSON.
func rawOutput(c *Client) bool {
	if c == nil {
		return false
	}
	return c.raw
}

// ---------------------------------------------------------------------
// list
// ---------------------------------------------------------------------

func createFlyioListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list [resource]",
		Short: "List Fly.io resources",
		Long: `List Fly.io resources of a specific type.

Supported resources:
  apps                  - Fly applications
  machines              - Machines for one app (requires --app)
  volumes               - Volumes for one app (requires --app)
  secrets               - Secret names + digests (requires --app, never values)
  ips                   - IPs allocated to one app (requires --app)
  certs / certificates  - TLS certs for one app (requires --app)
  releases              - Release history for one app (requires --app)
  orgs / organizations  - Orgs accessible by the token
  regions               - Fly platform regions
  postgres              - Postgres clusters (both managed and unmanaged)
  redis                 - Upstash Redis instances
  tigris                - Tigris object-storage buckets
  mysql                 - Managed MySQL instances
  wireguard             - WireGuard peers
  tokens                - API tokens
  extensions            - Marketplace extensions (Sentry, etc.)`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resource := strings.ToLower(args[0])

			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			switch resource {
			case "apps", "app":
				return listApps(ctx, client)
			case "machines", "machine":
				app, err := requireApp(cmd)
				if err != nil {
					return err
				}
				return listMachines(ctx, client, app)
			case "volumes", "volume":
				app, err := requireApp(cmd)
				if err != nil {
					return err
				}
				return listVolumes(ctx, client, app)
			case "secrets":
				app, err := requireApp(cmd)
				if err != nil {
					return err
				}
				return listSecrets(ctx, client, app)
			case "ips", "ip":
				app, err := requireApp(cmd)
				if err != nil {
					return err
				}
				return listIPs(ctx, client, app)
			case "certs", "cert", "certificates", "certificate":
				app, err := requireApp(cmd)
				if err != nil {
					return err
				}
				return listCertificates(ctx, client, app)
			case "releases", "release":
				app, err := requireApp(cmd)
				if err != nil {
					return err
				}
				return listReleases(ctx, client, app)
			case "orgs", "org", "organizations", "organization":
				return listOrgs(ctx, client)
			case "regions", "region":
				return listRegions(ctx, client)
			case "postgres", "pg":
				return listPostgres(ctx, client)
			case "redis":
				return listRedis(ctx, client)
			case "tigris":
				return listTigris(ctx, client)
			case "mysql":
				return listMysql(ctx, client)
			case "wireguard", "wg":
				return listWireGuard(ctx, client)
			case "tokens", "token":
				return listAuthTokens(ctx, client)
			case "extensions", "extension":
				return listExtensions(ctx, client)
			default:
				return fmt.Errorf("unknown resource type: %s", resource)
			}
		},
	}
	return cmd
}

// ---------------------------------------------------------------------
// get
// ---------------------------------------------------------------------

func createFlyioGetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <app|machine|volume|cert|release|org> <id>",
		Short: "Get a single Fly.io resource",
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
			case "app":
				return getApp(ctx, client, id)
			case "machine":
				app, err := requireApp(cmd)
				if err != nil {
					return err
				}
				return getMachine(ctx, client, app, id)
			case "volume", "vol":
				app, err := requireApp(cmd)
				if err != nil {
					return err
				}
				return getVolume(ctx, client, app, id)
			case "cert", "certificate":
				app, err := requireApp(cmd)
				if err != nil {
					return err
				}
				return getCertificate(ctx, client, app, id)
			case "release":
				app, err := requireApp(cmd)
				if err != nil {
					return err
				}
				return getRelease(ctx, client, app, id)
			case "org", "organization":
				return getOrg(ctx, client, id)
			default:
				return fmt.Errorf("unknown resource kind: %s (expected app|machine|volume|cert|release|org)", kind)
			}
		},
	}
	return cmd
}

// ---------------------------------------------------------------------
// logs
// ---------------------------------------------------------------------

func createFlyioLogsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Tail logs for an app",
		Long: `Stream logs for one app. Snapshot mode (default) returns the most recent
log lines; --follow streams continuously via the local flyctl binary.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := requireApp(cmd)
			if err != nil {
				return err
			}
			follow, _ := cmd.Flags().GetBool("follow")
			region, _ := cmd.Flags().GetString("region")

			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}

			if follow {
				args := []string{"logs", "--app", app}
				if region != "" {
					args = append(args, "--region", region)
				}
				// Stream directly to stdout — flyctl handles its own pty-aware
				// formatting. RunFlyctl buffers; for follow mode we want a live
				// stream so we shell out via the same env-setting flow.
				out, runErr := client.RunFlyctl(args...)
				if runErr != nil {
					return runErr
				}
				fmt.Print(out)
				return nil
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			return getLogsSnapshot(ctx, client, app)
		},
	}
	cmd.Flags().Bool("follow", false, "Stream logs continuously (shells flyctl)")
	cmd.Flags().String("region", "", "Filter to one region (e.g. iad)")
	return cmd
}

// ---------------------------------------------------------------------
// deploy / redeploy / rollback
// ---------------------------------------------------------------------

func createFlyioDeployCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "deploy [path]",
		Short: "Deploy an app from source via flyctl",
		Long: `Deploy from a local working directory. Requires flyctl on PATH.

Examples:
  clanker fly deploy --app my-app
  clanker fly deploy . --app my-app --region iad --strategy=immediate`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "."
			if len(args) > 0 {
				path = args[0]
			}
			app := resolveAppFlag(cmd)
			region, _ := cmd.Flags().GetString("region")
			strategy, _ := cmd.Flags().GetString("strategy")
			image, _ := cmd.Flags().GetString("image")
			remote, _ := cmd.Flags().GetBool("remote-only")

			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}

			flyArgs := []string{"deploy", path}
			if app != "" {
				flyArgs = append(flyArgs, "--app", app)
			}
			if region != "" {
				flyArgs = append(flyArgs, "--region", region)
			}
			if strategy != "" {
				flyArgs = append(flyArgs, "--strategy", strategy)
			}
			if image != "" {
				flyArgs = append(flyArgs, "--image", image)
			}
			if remote {
				flyArgs = append(flyArgs, "--remote-only")
			}

			out, err := client.RunFlyctl(flyArgs...)
			if err != nil {
				return err
			}
			fmt.Print(out)
			return nil
		},
	}
	cmd.Flags().String("region", "", "Initial region to deploy to")
	cmd.Flags().String("strategy", "", "Deployment strategy (immediate|rolling|canary|bluegreen)")
	cmd.Flags().String("image", "", "Use a pre-built image instead of building from source")
	cmd.Flags().Bool("remote-only", false, "Build remotely on Fly's builders")
	return cmd
}

func createFlyioRedeployCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "redeploy",
		Short: "Redeploy the latest release via flyctl",
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := requireApp(cmd)
			if err != nil {
				return err
			}
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			out, err := client.RunFlyctl("deploy", "--app", app, "--strategy", "rolling")
			if err != nil {
				return err
			}
			fmt.Print(out)
			return nil
		},
	}
	return cmd
}

func createFlyioRollbackCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rollback [version]",
		Short: "Roll back an app to a previous release",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := requireApp(cmd)
			if err != nil {
				return err
			}
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			flyArgs := []string{"releases", "rollback", "--app", app}
			if len(args) > 0 {
				flyArgs = append(flyArgs, args[0])
			}
			out, err := client.RunFlyctl(flyArgs...)
			if err != nil {
				return err
			}
			fmt.Print(out)
			return nil
		},
	}
	return cmd
}

// ---------------------------------------------------------------------
// Machine lifecycle (REST — no flyctl required)
// ---------------------------------------------------------------------

func createFlyioRestartCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "restart <app|machine> <id>",
		Short: "Restart an app or one of its machines",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind := strings.ToLower(args[0])
			id := args[1]
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			switch kind {
			case "app":
				_, err := client.RunAPIWithContext(ctx, "POST", "/apps/"+url.PathEscape(id)+"/restart", "")
				if err != nil {
					return err
				}
				fmt.Printf("Restart triggered for app %s.\n", id)
				return nil
			case "machine":
				app, err := requireApp(cmd)
				if err != nil {
					return err
				}
				_, err = client.RunAPIWithContext(ctx, "POST", "/apps/"+url.PathEscape(app)+"/machines/"+url.PathEscape(id)+"/restart", "")
				if err != nil {
					return err
				}
				fmt.Printf("Restart triggered for machine %s (app %s).\n", id, app)
				return nil
			default:
				return fmt.Errorf("unknown kind: %s (expected app|machine)", kind)
			}
		},
	}
	return cmd
}

func machineLifecycleCmd(use, short, suffix, verb string) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := requireApp(cmd)
			if err != nil {
				return err
			}
			id := args[0]
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			_, err = client.RunAPIWithContext(ctx, "POST", "/apps/"+url.PathEscape(app)+"/machines/"+url.PathEscape(id)+"/"+suffix, "")
			if err != nil {
				return err
			}
			fmt.Printf("%s machine %s (app %s).\n", verb, id, app)
			return nil
		},
	}
}

func createFlyioStartCmd() *cobra.Command {
	return machineLifecycleCmd("start <machineId>", "Start a stopped or suspended machine", "start", "Started")
}

func createFlyioStopCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stop <machineId>",
		Short: "Stop a running machine",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := requireApp(cmd)
			if err != nil {
				return err
			}
			id := args[0]
			signal, _ := cmd.Flags().GetString("signal")
			timeout, _ := cmd.Flags().GetString("timeout")

			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			body := "{}"
			if signal != "" || timeout != "" {
				m := map[string]string{}
				if signal != "" {
					m["signal"] = signal
				}
				if timeout != "" {
					m["timeout"] = timeout
				}
				if b, err := json.Marshal(m); err == nil {
					body = string(b)
				}
			}
			_, err = client.RunAPIWithContext(ctx, "POST", "/apps/"+url.PathEscape(app)+"/machines/"+url.PathEscape(id)+"/stop", body)
			if err != nil {
				return err
			}
			fmt.Printf("Stopped machine %s (app %s).\n", id, app)
			return nil
		},
	}
	cmd.Flags().String("signal", "", "Signal to send (e.g. SIGINT, SIGTERM)")
	cmd.Flags().String("timeout", "", "Graceful timeout (e.g. 30s)")
	return cmd
}

func createFlyioSuspendCmd() *cobra.Command {
	return machineLifecycleCmd("suspend <machineId>", "Suspend a running machine (preserves disk, stops billing for compute)", "suspend", "Suspended")
}

func createFlyioCordonCmd() *cobra.Command {
	return machineLifecycleCmd("cordon <machineId>", "Drain a machine from the load balancer (does not stop it)", "cordon", "Cordoned")
}

func createFlyioUncordonCmd() *cobra.Command {
	return machineLifecycleCmd("uncordon <machineId>", "Re-enable a machine in the load balancer", "uncordon", "Uncordoned")
}

func createFlyioDestroyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "destroy <app|machine|volume> <id>",
		Short: "Destroy a Fly resource. Use --force to skip confirmation.",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind := strings.ToLower(args[0])
			id := args[1]
			force, _ := cmd.Flags().GetBool("force")

			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			var endpoint string
			switch kind {
			case "app":
				endpoint = "/apps/" + url.PathEscape(id)
			case "machine":
				app, err := requireApp(cmd)
				if err != nil {
					return err
				}
				endpoint = "/apps/" + url.PathEscape(app) + "/machines/" + url.PathEscape(id)
				if force {
					endpoint += "?force=true"
				}
			case "volume", "vol":
				app, err := requireApp(cmd)
				if err != nil {
					return err
				}
				endpoint = "/apps/" + url.PathEscape(app) + "/volumes/" + url.PathEscape(id)
			default:
				return fmt.Errorf("unknown kind: %s (expected app|machine|volume)", kind)
			}

			if _, err := client.RunAPIWithContext(ctx, "DELETE", endpoint, ""); err != nil {
				return err
			}
			fmt.Printf("Destroyed %s %s.\n", kind, id)
			return nil
		},
	}
	cmd.Flags().Bool("force", false, "Force destroy without graceful shutdown (machines only)")
	return cmd
}

func createFlyioCloneCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "clone <machineId>",
		Short: "Clone an existing machine into a new region or with a new config",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := requireApp(cmd)
			if err != nil {
				return err
			}
			id := args[0]
			region, _ := cmd.Flags().GetString("region")
			name, _ := cmd.Flags().GetString("name")

			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			body := map[string]string{}
			if region != "" {
				body["region"] = region
			}
			if name != "" {
				body["name"] = name
			}
			payload, _ := json.Marshal(body)

			out, err := client.RunAPIWithContext(ctx, "POST", "/apps/"+url.PathEscape(app)+"/machines/"+url.PathEscape(id)+"/clone", string(payload))
			if err != nil {
				return err
			}
			fmt.Println(out)
			return nil
		},
	}
	cmd.Flags().String("region", "", "Target region for the clone")
	cmd.Flags().String("name", "", "Name for the new machine")
	return cmd
}

func createFlyioExecCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "exec <machineId> -- <command...>",
		Short: "Run a command on a machine via flyctl ssh console",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := requireApp(cmd)
			if err != nil {
				return err
			}
			id := args[0]
			rest := args[1:]
			if len(rest) == 0 {
				return fmt.Errorf("command is required after machine id")
			}

			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}

			flyArgs := []string{"ssh", "console", "--app", app, "-s", id, "-C", strings.Join(rest, " ")}
			out, err := client.RunFlyctl(flyArgs...)
			if err != nil {
				return err
			}
			fmt.Print(out)
			return nil
		},
	}
	return cmd
}

// ---------------------------------------------------------------------
// scale (count / vm / memory / cpu)
// ---------------------------------------------------------------------

func createFlyioScaleCmd() *cobra.Command {
	scale := &cobra.Command{
		Use:   "scale",
		Short: "Adjust app scale (count, vm preset, memory, cpu)",
	}

	count := &cobra.Command{
		Use:   "count <N>",
		Short: "Set the desired machine count (uses flyctl scale count)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := requireApp(cmd)
			if err != nil {
				return err
			}
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			out, err := client.RunFlyctl("scale", "count", args[0], "--app", app)
			if err != nil {
				return err
			}
			fmt.Print(out)
			return nil
		},
	}

	vm := &cobra.Command{
		Use:   "vm <preset>",
		Short: "Change machine size preset (e.g. shared-cpu-2x, performance-1x)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := requireApp(cmd)
			if err != nil {
				return err
			}
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			out, err := client.RunFlyctl("scale", "vm", args[0], "--app", app)
			if err != nil {
				return err
			}
			fmt.Print(out)
			return nil
		},
	}

	memory := &cobra.Command{
		Use:   "memory <MB>",
		Short: "Set machine memory in MB",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := requireApp(cmd)
			if err != nil {
				return err
			}
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			out, err := client.RunFlyctl("scale", "memory", args[0], "--app", app)
			if err != nil {
				return err
			}
			fmt.Print(out)
			return nil
		},
	}

	cpu := &cobra.Command{
		Use:   "cpu <count>",
		Short: "Set machine cpu count",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := requireApp(cmd)
			if err != nil {
				return err
			}
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			out, err := client.RunFlyctl("scale", "cpu", args[0], "--app", app)
			if err != nil {
				return err
			}
			fmt.Print(out)
			return nil
		},
	}

	scale.AddCommand(count, vm, memory, cpu)
	return scale
}

// ---------------------------------------------------------------------
// secrets (flyctl-mediated; values never echoed back)
// ---------------------------------------------------------------------

func createFlyioSecretsCmd() *cobra.Command {
	secrets := &cobra.Command{
		Use:   "secrets",
		Short: "Manage app secrets (set, unset, list, deploy)",
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "List secret names + digests (never values)",
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := requireApp(cmd)
			if err != nil {
				return err
			}
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			return listSecrets(ctx, client, app)
		},
	}

	set := &cobra.Command{
		Use:   "set <KEY=VALUE> [...]",
		Short: "Set one or more secrets",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := requireApp(cmd)
			if err != nil {
				return err
			}
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			flyArgs := append([]string{"secrets", "set", "--app", app}, args...)
			out, err := client.RunFlyctl(flyArgs...)
			if err != nil {
				return err
			}
			fmt.Print(out)
			return nil
		},
	}

	unset := &cobra.Command{
		Use:   "unset <KEY> [...]",
		Short: "Remove one or more secrets",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := requireApp(cmd)
			if err != nil {
				return err
			}
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			flyArgs := append([]string{"secrets", "unset", "--app", app}, args...)
			out, err := client.RunFlyctl(flyArgs...)
			if err != nil {
				return err
			}
			fmt.Print(out)
			return nil
		},
	}

	deploy := &cobra.Command{
		Use:   "deploy",
		Short: "Deploy staged secrets (force-restart machines)",
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := requireApp(cmd)
			if err != nil {
				return err
			}
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			out, err := client.RunFlyctl("secrets", "deploy", "--app", app)
			if err != nil {
				return err
			}
			fmt.Print(out)
			return nil
		},
	}

	secrets.AddCommand(list, set, unset, deploy)
	return secrets
}

// ---------------------------------------------------------------------
// ips (REST allocate/release)
// ---------------------------------------------------------------------

func createFlyioIPsCmd() *cobra.Command {
	ips := &cobra.Command{
		Use:   "ips",
		Short: "Manage app IPs (list, allocate, release)",
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "List IPs allocated to an app",
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := requireApp(cmd)
			if err != nil {
				return err
			}
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			return listIPs(ctx, client, app)
		},
	}

	allocate := &cobra.Command{
		Use:   "allocate",
		Short: "Allocate a new IP for an app (--type v4|v6|shared|private)",
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := requireApp(cmd)
			if err != nil {
				return err
			}
			ipType, _ := cmd.Flags().GetString("type")
			region, _ := cmd.Flags().GetString("region")
			if ipType == "" {
				ipType = "v4"
			}

			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			body := map[string]string{"type": ipType}
			if region != "" {
				body["region"] = region
			}
			payload, _ := json.Marshal(body)
			out, err := client.RunAPIWithContext(ctx, "POST", "/apps/"+url.PathEscape(app)+"/ips", string(payload))
			if err != nil {
				return err
			}
			fmt.Println(out)
			return nil
		},
	}
	allocate.Flags().String("type", "v4", "IP type: v4 | v6 | shared | private")
	allocate.Flags().String("region", "", "Pin to a specific region (v4 only)")

	release := &cobra.Command{
		Use:   "release <ipId>",
		Short: "Release an IP",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := requireApp(cmd)
			if err != nil {
				return err
			}
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_, err = client.RunAPIWithContext(ctx, "DELETE", "/apps/"+url.PathEscape(app)+"/ips/"+url.PathEscape(args[0]), "")
			if err != nil {
				return err
			}
			fmt.Printf("Released IP %s (app %s).\n", args[0], app)
			return nil
		},
	}

	ips.AddCommand(list, allocate, release)
	return ips
}

// ---------------------------------------------------------------------
// certificates (REST)
// ---------------------------------------------------------------------

func createFlyioCertsCmd() *cobra.Command {
	certs := &cobra.Command{
		Use:   "certs",
		Short: "Manage TLS certificates (list, add, check, remove)",
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "List certificates for an app",
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := requireApp(cmd)
			if err != nil {
				return err
			}
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			return listCertificates(ctx, client, app)
		},
	}

	add := &cobra.Command{
		Use:   "add <hostname>",
		Short: "Add a certificate for a custom hostname",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := requireApp(cmd)
			if err != nil {
				return err
			}
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			body := fmt.Sprintf(`{"hostname":%q}`, args[0])
			out, err := client.RunAPIWithContext(ctx, "POST", "/apps/"+url.PathEscape(app)+"/certificates", body)
			if err != nil {
				return err
			}
			fmt.Println(out)
			return nil
		},
	}

	check := &cobra.Command{
		Use:   "check <hostname>",
		Short: "Re-check DNS and validate a certificate",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := requireApp(cmd)
			if err != nil {
				return err
			}
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			out, err := client.RunAPIWithContext(ctx, "GET", "/apps/"+url.PathEscape(app)+"/certificates/"+url.PathEscape(args[0])+"/check", "")
			if err != nil {
				return err
			}
			fmt.Println(out)
			return nil
		},
	}

	remove := &cobra.Command{
		Use:   "remove <hostname>",
		Short: "Remove a certificate",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := requireApp(cmd)
			if err != nil {
				return err
			}
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_, err = client.RunAPIWithContext(ctx, "DELETE", "/apps/"+url.PathEscape(app)+"/certificates/"+url.PathEscape(args[0]), "")
			if err != nil {
				return err
			}
			fmt.Printf("Removed certificate for %s (app %s).\n", args[0], app)
			return nil
		},
	}

	certs.AddCommand(list, add, check, remove)
	return certs
}

// ---------------------------------------------------------------------
// volumes
// ---------------------------------------------------------------------

func createFlyioVolumesCmd() *cobra.Command {
	volumes := &cobra.Command{
		Use:   "volumes",
		Short: "Manage volumes (list, create, destroy, extend, snapshots)",
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "List volumes for an app",
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := requireApp(cmd)
			if err != nil {
				return err
			}
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			return listVolumes(ctx, client, app)
		},
	}

	create := &cobra.Command{
		Use:   "create",
		Short: "Create a volume",
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := requireApp(cmd)
			if err != nil {
				return err
			}
			name, _ := cmd.Flags().GetString("name")
			region, _ := cmd.Flags().GetString("region")
			sizeGB, _ := cmd.Flags().GetInt("size-gb")
			encrypted, _ := cmd.Flags().GetBool("encrypted")
			if name == "" || region == "" || sizeGB <= 0 {
				return fmt.Errorf("--name, --region, and --size-gb are required")
			}

			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			body := map[string]interface{}{
				"name":      name,
				"region":    region,
				"size_gb":   sizeGB,
				"encrypted": encrypted,
			}
			payload, _ := json.Marshal(body)
			out, err := client.RunAPIWithContext(ctx, "POST", "/apps/"+url.PathEscape(app)+"/volumes", string(payload))
			if err != nil {
				return err
			}
			fmt.Println(out)
			return nil
		},
	}
	create.Flags().String("name", "", "Volume name (required)")
	create.Flags().String("region", "", "Region (required, e.g. iad)")
	create.Flags().Int("size-gb", 0, "Size in GB (required)")
	create.Flags().Bool("encrypted", true, "Encrypt the volume at rest")

	extend := &cobra.Command{
		Use:   "extend <volumeId>",
		Short: "Increase a volume's size",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := requireApp(cmd)
			if err != nil {
				return err
			}
			sizeGB, _ := cmd.Flags().GetInt("size-gb")
			if sizeGB <= 0 {
				return fmt.Errorf("--size-gb is required")
			}
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			body := fmt.Sprintf(`{"size_gb":%d}`, sizeGB)
			out, err := client.RunAPIWithContext(ctx, "PUT", "/apps/"+url.PathEscape(app)+"/volumes/"+url.PathEscape(args[0])+"/extend", body)
			if err != nil {
				return err
			}
			fmt.Println(out)
			return nil
		},
	}
	extend.Flags().Int("size-gb", 0, "New size in GB (required)")

	snapshots := &cobra.Command{
		Use:   "snapshots <volumeId>",
		Short: "List snapshots of a volume",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := requireApp(cmd)
			if err != nil {
				return err
			}
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			out, err := client.RunAPIWithContext(ctx, "GET", "/apps/"+url.PathEscape(app)+"/volumes/"+url.PathEscape(args[0])+"/snapshots", "")
			if err != nil {
				return err
			}
			fmt.Println(out)
			return nil
		},
	}

	volumes.AddCommand(list, create, extend, snapshots)
	return volumes
}

// ---------------------------------------------------------------------
// releases
// ---------------------------------------------------------------------

func createFlyioReleasesCmd() *cobra.Command {
	releases := &cobra.Command{
		Use:   "releases",
		Short: "Show release history (list)",
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "List recent releases for an app",
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := requireApp(cmd)
			if err != nil {
				return err
			}
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			return listReleases(ctx, client, app)
		},
	}

	releases.AddCommand(list)
	return releases
}

// ---------------------------------------------------------------------
// postgres / managed postgres / redis / tigris / mysql
// ---------------------------------------------------------------------

func createFlyioPostgresCmd() *cobra.Command {
	pg := &cobra.Command{
		Use:   "postgres",
		Short: "Manage Fly Postgres clusters (legacy unmanaged)",
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "List Postgres clusters across managed + unmanaged",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			return listPostgres(ctx, client)
		},
	}

	create := &cobra.Command{
		Use:   "create",
		Short: "Create a Postgres cluster via flyctl postgres create",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			flyArgs := []string{"postgres", "create"}
			if v, _ := cmd.Flags().GetString("name"); v != "" {
				flyArgs = append(flyArgs, "--name", v)
			}
			if v, _ := cmd.Flags().GetString("region"); v != "" {
				flyArgs = append(flyArgs, "--region", v)
			}
			if v, _ := cmd.Flags().GetString("vm-size"); v != "" {
				flyArgs = append(flyArgs, "--vm-size", v)
			}
			if v, _ := cmd.Flags().GetInt("volume-size"); v > 0 {
				flyArgs = append(flyArgs, "--volume-size", fmt.Sprint(v))
			}
			out, err := client.RunFlyctl(flyArgs...)
			if err != nil {
				return err
			}
			fmt.Print(out)
			return nil
		},
	}
	create.Flags().String("name", "", "Cluster name")
	create.Flags().String("region", "", "Primary region")
	create.Flags().String("vm-size", "", "VM size (e.g. shared-cpu-1x)")
	create.Flags().Int("volume-size", 0, "Volume size in GB")

	attach := &cobra.Command{
		Use:   "attach <clusterApp>",
		Short: "Attach a Postgres cluster to an app (sets DATABASE_URL secret)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := requireApp(cmd)
			if err != nil {
				return err
			}
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			out, err := client.RunFlyctl("postgres", "attach", args[0], "--app", app)
			if err != nil {
				return err
			}
			fmt.Print(out)
			return nil
		},
	}

	detach := &cobra.Command{
		Use:   "detach <clusterApp>",
		Short: "Detach a Postgres cluster from an app",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := requireApp(cmd)
			if err != nil {
				return err
			}
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			out, err := client.RunFlyctl("postgres", "detach", args[0], "--app", app)
			if err != nil {
				return err
			}
			fmt.Print(out)
			return nil
		},
	}

	failover := &cobra.Command{
		Use:   "failover <clusterApp>",
		Short: "Trigger a Postgres failover via flyctl",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			out, err := client.RunFlyctl("postgres", "failover", "--app", args[0])
			if err != nil {
				return err
			}
			fmt.Print(out)
			return nil
		},
	}

	pg.AddCommand(list, create, attach, detach, failover)
	return pg
}

func createFlyioMPGCmd() *cobra.Command {
	mpg := &cobra.Command{
		Use:   "mpg",
		Short: "Managed Postgres (MPG) — list, create, destroy",
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "List managed Postgres clusters",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			out, err := client.RunAPIWithContext(ctx, "GET", "/mpg/clusters", "")
			if err != nil {
				return err
			}
			fmt.Println(out)
			return nil
		},
	}

	create := &cobra.Command{
		Use:   "create",
		Short: "Create a managed Postgres cluster via flyctl mpg create",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			flyArgs := []string{"mpg", "create"}
			if v, _ := cmd.Flags().GetString("name"); v != "" {
				flyArgs = append(flyArgs, "--name", v)
			}
			if v, _ := cmd.Flags().GetString("region"); v != "" {
				flyArgs = append(flyArgs, "--region", v)
			}
			out, err := client.RunFlyctl(flyArgs...)
			if err != nil {
				return err
			}
			fmt.Print(out)
			return nil
		},
	}
	create.Flags().String("name", "", "Cluster name")
	create.Flags().String("region", "", "Primary region")

	mpg.AddCommand(list, create)
	return mpg
}

func createFlyioRedisCmd() *cobra.Command {
	redis := &cobra.Command{
		Use:   "redis",
		Short: "Manage Upstash Redis instances",
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "List Redis instances",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			return listRedis(ctx, client)
		},
	}

	create := &cobra.Command{
		Use:   "create",
		Short: "Create a Redis instance via flyctl",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			flyArgs := []string{"redis", "create"}
			if v, _ := cmd.Flags().GetString("name"); v != "" {
				flyArgs = append(flyArgs, "--name", v)
			}
			if v, _ := cmd.Flags().GetString("region"); v != "" {
				flyArgs = append(flyArgs, "--region", v)
			}
			if v, _ := cmd.Flags().GetString("plan"); v != "" {
				flyArgs = append(flyArgs, "--plan", v)
			}
			out, err := client.RunFlyctl(flyArgs...)
			if err != nil {
				return err
			}
			fmt.Print(out)
			return nil
		},
	}
	create.Flags().String("name", "", "Redis instance name")
	create.Flags().String("region", "", "Primary region")
	create.Flags().String("plan", "", "Plan name")

	redis.AddCommand(list, create)
	return redis
}

func createFlyioTigrisCmd() *cobra.Command {
	tigris := &cobra.Command{
		Use:   "tigris",
		Short: "Manage Tigris object-storage buckets",
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "List Tigris buckets",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			return listTigris(ctx, client)
		},
	}

	createBucket := &cobra.Command{
		Use:   "create-bucket",
		Short: "Create a Tigris bucket via flyctl",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			flyArgs := []string{"storage", "create"}
			if v, _ := cmd.Flags().GetString("name"); v != "" {
				flyArgs = append(flyArgs, "--name", v)
			}
			out, err := client.RunFlyctl(flyArgs...)
			if err != nil {
				return err
			}
			fmt.Print(out)
			return nil
		},
	}
	createBucket.Flags().String("name", "", "Bucket name")

	tigris.AddCommand(list, createBucket)
	return tigris
}

func createFlyioMysqlCmd() *cobra.Command {
	mysql := &cobra.Command{
		Use:   "mysql",
		Short: "Manage Fly MySQL (preview)",
	}
	mysql.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List MySQL instances",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			return listMysql(ctx, client)
		},
	})
	return mysql
}

// ---------------------------------------------------------------------
// regions / orgs / auth / tokens / wireguard / extensions
// ---------------------------------------------------------------------

func createFlyioRegionsCmd() *cobra.Command {
	regions := &cobra.Command{
		Use:   "regions",
		Short: "List Fly platform regions",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			return listRegions(ctx, client)
		},
	}
	return regions
}

func createFlyioOrgsCmd() *cobra.Command {
	orgs := &cobra.Command{
		Use:   "orgs",
		Short: "List organizations accessible to your token",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			return listOrgs(ctx, client)
		},
	}
	return orgs
}

func createFlyioAuthCmd() *cobra.Command {
	auth := &cobra.Command{
		Use:   "auth",
		Short: "Auth helpers (whoami)",
	}
	auth.AddCommand(&cobra.Command{
		Use:   "whoami",
		Short: "Show the email/user behind the configured token",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			query := `query { viewer { id email name } }`
			out, err := client.RunGraphQL(ctx, query, nil)
			if err != nil {
				return err
			}
			fmt.Println(out)
			return nil
		},
	})
	return auth
}

func createFlyioTokensCmd() *cobra.Command {
	tokens := &cobra.Command{
		Use:   "tokens",
		Short: "Manage API tokens (list, revoke)",
	}
	tokens.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List API tokens",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			return listAuthTokens(ctx, client)
		},
	})
	return tokens
}

func createFlyioWireGuardCmd() *cobra.Command {
	wg := &cobra.Command{
		Use:   "wireguard",
		Short: "Manage WireGuard peers",
	}
	wg.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List WireGuard peers",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			return listWireGuard(ctx, client)
		},
	})
	return wg
}

func createFlyioExtensionsCmd() *cobra.Command {
	ext := &cobra.Command{
		Use:   "extensions",
		Short: "List marketplace extensions (Sentry, etc.)",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			return listExtensions(ctx, client)
		},
	}
	return ext
}

// ---------------------------------------------------------------------
// Listers (REST + GraphQL)
// ---------------------------------------------------------------------

func listApps(ctx context.Context, client *Client) error {
	out, err := client.RunAPIWithContext(ctx, "GET", "/apps", "")
	if err != nil {
		return err
	}
	if rawOutput(client) {
		fmt.Println(out)
		return nil
	}
	// Fly returns either {"apps":[...]} or a bare [...] depending on path.
	var resp struct {
		Apps []App `json:"apps"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err == nil && len(resp.Apps) > 0 {
		printApps(resp.Apps)
		return nil
	}
	var bare []App
	if err := json.Unmarshal([]byte(out), &bare); err == nil {
		printApps(bare)
		return nil
	}
	fmt.Println(out)
	return nil
}

func printApps(apps []App) {
	if len(apps) == 0 {
		fmt.Println("No Fly apps found.")
		return
	}
	fmt.Printf("Fly Apps (%d):\n\n", len(apps))
	for _, a := range apps {
		fmt.Printf("  %s\n", a.Name)
		if a.Organization.Slug != "" {
			fmt.Printf("    Org: %s\n", a.Organization.Slug)
		}
		if a.Status != "" {
			fmt.Printf("    Status: %s\n", a.Status)
		}
		if a.Hostname != "" {
			fmt.Printf("    Hostname: %s\n", a.Hostname)
		}
		if a.AppURL != "" {
			fmt.Printf("    URL: %s\n", a.AppURL)
		}
		fmt.Println()
	}
}

func listMachines(ctx context.Context, client *Client, app string) error {
	out, err := client.RunAPIWithContext(ctx, "GET", "/apps/"+url.PathEscape(app)+"/machines", "")
	if err != nil {
		return err
	}
	if rawOutput(client) {
		fmt.Println(out)
		return nil
	}
	var machines []Machine
	if err := json.Unmarshal([]byte(out), &machines); err != nil {
		fmt.Println(out)
		return nil
	}
	if len(machines) == 0 {
		fmt.Printf("No machines for app %s.\n", app)
		return nil
	}
	fmt.Printf("Machines for %s (%d):\n\n", app, len(machines))
	for _, m := range machines {
		fmt.Printf("  %s\n", m.ID)
		if m.Name != "" {
			fmt.Printf("    Name: %s\n", m.Name)
		}
		if m.State != "" {
			fmt.Printf("    State: %s\n", m.State)
		}
		if m.Region != "" {
			fmt.Printf("    Region: %s\n", m.Region)
		}
		if m.PrivateIP != "" {
			fmt.Printf("    Private IP: %s\n", m.PrivateIP)
		}
		if m.Config != nil && m.Config.Guest != nil {
			fmt.Printf("    Guest: %s-%d %dMB\n", m.Config.Guest.CPUKind, m.Config.Guest.CPUs, m.Config.Guest.MemoryMB)
		}
		fmt.Println()
	}
	return nil
}

func listVolumes(ctx context.Context, client *Client, app string) error {
	out, err := client.RunAPIWithContext(ctx, "GET", "/apps/"+url.PathEscape(app)+"/volumes", "")
	if err != nil {
		return err
	}
	if rawOutput(client) {
		fmt.Println(out)
		return nil
	}
	var volumes []Volume
	if err := json.Unmarshal([]byte(out), &volumes); err != nil {
		fmt.Println(out)
		return nil
	}
	if len(volumes) == 0 {
		fmt.Printf("No volumes for app %s.\n", app)
		return nil
	}
	fmt.Printf("Volumes for %s (%d):\n\n", app, len(volumes))
	for _, v := range volumes {
		fmt.Printf("  %s\n", v.ID)
		if v.Name != "" {
			fmt.Printf("    Name: %s\n", v.Name)
		}
		fmt.Printf("    State: %s, Region: %s, Size: %dGB\n", v.State, v.Region, v.SizeGB)
		if v.AttachedMachineID != "" {
			fmt.Printf("    Attached: %s\n", v.AttachedMachineID)
		}
		fmt.Println()
	}
	return nil
}

func listSecrets(ctx context.Context, client *Client, app string) error {
	out, err := client.RunAPIWithContext(ctx, "GET", "/apps/"+url.PathEscape(app)+"/secrets", "")
	if err != nil {
		return err
	}
	if rawOutput(client) {
		fmt.Println(out)
		return nil
	}
	var secrets []Secret
	if err := json.Unmarshal([]byte(out), &secrets); err != nil {
		fmt.Println(out)
		return nil
	}
	if len(secrets) == 0 {
		fmt.Printf("No secrets for app %s.\n", app)
		return nil
	}
	fmt.Printf("Secrets for %s (%d) — names and digests only:\n\n", app, len(secrets))
	for _, s := range secrets {
		fmt.Printf("  %s\n", s.Name)
		if s.Digest != "" {
			fmt.Printf("    Digest: %s\n", s.Digest)
		}
		if s.CreatedAt != "" {
			fmt.Printf("    Created: %s\n", s.CreatedAt)
		}
	}
	return nil
}

func listIPs(ctx context.Context, client *Client, app string) error {
	out, err := client.RunAPIWithContext(ctx, "GET", "/apps/"+url.PathEscape(app)+"/ips", "")
	if err != nil {
		return err
	}
	if rawOutput(client) {
		fmt.Println(out)
		return nil
	}
	var ips []IPAddress
	if err := json.Unmarshal([]byte(out), &ips); err != nil {
		fmt.Println(out)
		return nil
	}
	if len(ips) == 0 {
		fmt.Printf("No IPs for app %s.\n", app)
		return nil
	}
	fmt.Printf("IPs for %s (%d):\n\n", app, len(ips))
	for _, ip := range ips {
		fmt.Printf("  %s (%s)\n", ip.Address, ip.Type)
		if ip.Region != "" {
			fmt.Printf("    Region: %s\n", ip.Region)
		}
	}
	return nil
}

func listCertificates(ctx context.Context, client *Client, app string) error {
	out, err := client.RunAPIWithContext(ctx, "GET", "/apps/"+url.PathEscape(app)+"/certificates", "")
	if err != nil {
		return err
	}
	if rawOutput(client) {
		fmt.Println(out)
		return nil
	}
	var certs []Certificate
	if err := json.Unmarshal([]byte(out), &certs); err != nil {
		fmt.Println(out)
		return nil
	}
	if len(certs) == 0 {
		fmt.Printf("No certificates for app %s.\n", app)
		return nil
	}
	fmt.Printf("Certificates for %s (%d):\n\n", app, len(certs))
	for _, c := range certs {
		fmt.Printf("  %s\n", c.Hostname)
		if c.Configured {
			fmt.Printf("    Configured: yes\n")
		}
		if c.ClientStatus != "" {
			fmt.Printf("    Status: %s\n", c.ClientStatus)
		}
	}
	return nil
}

func listReleases(ctx context.Context, client *Client, app string) error {
	out, err := client.RunAPIWithContext(ctx, "GET", "/apps/"+url.PathEscape(app)+"/releases", "")
	if err != nil {
		return err
	}
	if rawOutput(client) {
		fmt.Println(out)
		return nil
	}
	var releases []Release
	if err := json.Unmarshal([]byte(out), &releases); err != nil {
		fmt.Println(out)
		return nil
	}
	if len(releases) == 0 {
		fmt.Printf("No releases for app %s.\n", app)
		return nil
	}
	fmt.Printf("Releases for %s (%d):\n\n", app, len(releases))
	for _, r := range releases {
		fmt.Printf("  v%d  %s  %s\n", r.Version, r.Status, r.CreatedAt)
		if r.Description != "" {
			fmt.Printf("    %s\n", r.Description)
		}
	}
	return nil
}

func listRegions(ctx context.Context, client *Client) error {
	out, err := client.RunAPIWithContext(ctx, "GET", "/platform/regions", "")
	if err != nil {
		return err
	}
	if rawOutput(client) {
		fmt.Println(out)
		return nil
	}
	fmt.Println(out)
	return nil
}

func listOrgs(ctx context.Context, client *Client) error {
	// Orgs are GraphQL-only.
	query := `query { organizations { nodes { id slug name type paidPlan } } }`
	out, err := client.RunGraphQL(ctx, query, nil)
	if err != nil {
		return err
	}
	if rawOutput(client) {
		fmt.Println(out)
		return nil
	}
	fmt.Println(out)
	return nil
}

func listPostgres(ctx context.Context, client *Client) error {
	// Managed clusters via REST.
	if out, err := client.RunAPIWithContext(ctx, "GET", "/mpg/clusters", ""); err == nil {
		fmt.Println("== Managed Postgres (MPG) ==")
		fmt.Println(out)
		fmt.Println()
	}
	// Unmanaged clusters via GraphQL.
	query := `query { apps(role: "postgres_cluster") { nodes { id name organization { slug } status } } }`
	out, err := client.RunGraphQL(ctx, query, nil)
	if err != nil {
		return err
	}
	fmt.Println("== Unmanaged Postgres clusters ==")
	fmt.Println(out)
	return nil
}

func listRedis(ctx context.Context, client *Client) error {
	query := `query { addOns(type: "upstash_redis") { nodes { id name primaryRegion readRegions plan { name } } } }`
	out, err := client.RunGraphQL(ctx, query, nil)
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

func listTigris(ctx context.Context, client *Client) error {
	query := `query { addOns(type: "tigris") { nodes { id name primaryRegion } } }`
	out, err := client.RunGraphQL(ctx, query, nil)
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

func listMysql(ctx context.Context, client *Client) error {
	query := `query { addOns(type: "mysql") { nodes { id name primaryRegion } } }`
	out, err := client.RunGraphQL(ctx, query, nil)
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

func listWireGuard(ctx context.Context, client *Client) error {
	query := `query { wireGuardPeers { name region peerip publickey endpoint } }`
	out, err := client.RunGraphQL(ctx, query, nil)
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

func listAuthTokens(ctx context.Context, client *Client) error {
	query := `query { viewer { personalAccountTokens { nodes { id name expiresAt } } } }`
	out, err := client.RunGraphQL(ctx, query, nil)
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

func listExtensions(ctx context.Context, client *Client) error {
	query := `query { addOns(type: ["sentry","tigris","upstash_redis"]) { nodes { id name type primaryRegion } } }`
	out, err := client.RunGraphQL(ctx, query, nil)
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

// ---------------------------------------------------------------------
// Getters
// ---------------------------------------------------------------------

func getApp(ctx context.Context, client *Client, name string) error {
	out, err := client.RunAPIWithContext(ctx, "GET", "/apps/"+url.PathEscape(name), "")
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

func getMachine(ctx context.Context, client *Client, app, id string) error {
	out, err := client.RunAPIWithContext(ctx, "GET", "/apps/"+url.PathEscape(app)+"/machines/"+url.PathEscape(id), "")
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

func getVolume(ctx context.Context, client *Client, app, id string) error {
	out, err := client.RunAPIWithContext(ctx, "GET", "/apps/"+url.PathEscape(app)+"/volumes/"+url.PathEscape(id), "")
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

func getCertificate(ctx context.Context, client *Client, app, hostname string) error {
	out, err := client.RunAPIWithContext(ctx, "GET", "/apps/"+url.PathEscape(app)+"/certificates/"+url.PathEscape(hostname), "")
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

func getRelease(ctx context.Context, client *Client, app, id string) error {
	out, err := client.RunAPIWithContext(ctx, "GET", "/apps/"+url.PathEscape(app)+"/releases/"+url.PathEscape(id), "")
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

func getOrg(ctx context.Context, client *Client, slug string) error {
	query := `query($slug: String!) { organization(slug: $slug) { id slug name type paidPlan } }`
	out, err := client.RunGraphQL(ctx, query, map[string]interface{}{"slug": slug})
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

func getLogsSnapshot(ctx context.Context, client *Client, app string) error {
	out, err := client.RunAPIWithContext(ctx, "GET", "/apps/"+url.PathEscape(app)+"/logs", "")
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}
