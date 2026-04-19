package verda

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// CreateVerdaCommands builds the `verda` command tree. Registered from cmd/root.go
// as a sibling of `cf`, `do`, `hetzner`, `vercel`.
func CreateVerdaCommands() *cobra.Command {
	verdaCmd := &cobra.Command{
		Use:     "verda",
		Short:   "Query Verda Cloud (GPU/AI) resources directly",
		Long:    "Query Verda Cloud (ex-DataCrunch) GPU instances, clusters, volumes, and serverless workloads without AI interpretation.",
		Aliases: []string{"vd"},
	}

	verdaCmd.PersistentFlags().String("client-id", "", "Verda client ID (overrides VERDA_CLIENT_ID)")
	verdaCmd.PersistentFlags().String("client-secret", "", "Verda client secret (overrides VERDA_CLIENT_SECRET)")
	verdaCmd.PersistentFlags().String("project-id", "", "Verda project ID for scoping")
	verdaCmd.PersistentFlags().Bool("raw", false, "Output raw JSON instead of formatted")

	verdaCmd.AddCommand(createVerdaListCmd())
	verdaCmd.AddCommand(createVerdaGetCmd())
	verdaCmd.AddCommand(createVerdaActionCmd())
	verdaCmd.AddCommand(createVerdaBalanceCmd())

	return verdaCmd
}

func newClientFromFlags(cmd *cobra.Command) (*Client, error) {
	clientID, _ := cmd.Flags().GetString("client-id")
	if strings.TrimSpace(clientID) == "" {
		clientID = ResolveClientID()
	}
	clientSecret, _ := cmd.Flags().GetString("client-secret")
	if strings.TrimSpace(clientSecret) == "" {
		clientSecret = ResolveClientSecret()
	}
	projectID, _ := cmd.Flags().GetString("project-id")
	if strings.TrimSpace(projectID) == "" {
		projectID = ResolveProjectID()
	}
	if strings.TrimSpace(clientID) == "" || strings.TrimSpace(clientSecret) == "" {
		return nil, fmt.Errorf("verda credentials not configured — set verda.client_id/client_secret in ~/.clanker.yaml, export VERDA_CLIENT_ID/VERDA_CLIENT_SECRET, or run `verda auth login`")
	}
	debug, _ := cmd.Root().PersistentFlags().GetBool("debug")
	return NewClient(clientID, clientSecret, projectID, debug)
}

func createVerdaListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list [resource]",
		Short: "List Verda resources",
		Long: `List Verda resources of a specific type.

Supported resources:
  instances       - GPU/CPU instances
  clusters        - GPU clusters (Instant / Bare-metal)
  volumes         - Block + shared volumes
  ssh-keys        - SSH keys registered with Verda
  scripts         - Startup scripts
  instance-types  - Available instance types with pricing
  cluster-types   - Available cluster SKUs
  container-types - Serverless container compute types
  containers      - Serverless container deployments
  jobs            - Serverless job deployments
  secrets         - Serverless secrets
  file-secrets    - Serverless file secrets
  registry-creds  - Container registry credentials
  locations       - Available datacenter locations
  balance         - Current project balance
  images          - Available OS images (instance)
  cluster-images  - Available OS images (cluster)
  availability    - Current instance-type availability across regions`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resource := strings.ToLower(args[0])
			raw, _ := cmd.Flags().GetBool("raw")

			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

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
				return fmt.Errorf("unknown resource type: %s", resource)
			}

			body, err := client.RunAPIWithContext(ctx, http.MethodGet, path, "")
			if err != nil {
				return err
			}
			if raw {
				fmt.Println(body)
				return nil
			}
			fmt.Println(prettyJSON(body))
			return nil
		},
	}
	return cmd
}

func createVerdaGetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <instance|cluster|volume|ssh-key|script> <id>",
		Short: "Get a single Verda resource",
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

			var path string
			switch kind {
			case "instance", "vm":
				path = "/v1/instances/" + id
			case "cluster":
				path = "/v1/clusters/" + id
			case "volume":
				path = "/v1/volumes/" + id
			case "ssh-key", "ssh":
				path = "/v1/ssh-keys/" + id
			case "script":
				path = "/v1/scripts/" + id
			default:
				return fmt.Errorf("unknown resource kind: %s (expected instance|cluster|volume|ssh-key|script)", kind)
			}

			body, err := client.RunAPIWithContext(ctx, http.MethodGet, path, "")
			if err != nil {
				return err
			}
			raw, _ := cmd.Flags().GetBool("raw")
			if raw {
				fmt.Println(body)
				return nil
			}
			fmt.Println(prettyJSON(body))
			return nil
		},
	}
	return cmd
}

func createVerdaActionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "action <start|stop|shutdown|delete|discontinue|hibernate|boot|force_shutdown> <instance>",
		Short: "Perform a lifecycle action on a Verda instance",
		Long: `Invoke PUT /v1/instances with the given action.

The <instance> argument accepts either a Verda instance UUID or a hostname —
hostnames are resolved via GET /v1/instances before the action is issued.

The underlying REST call is always PUT /v1/instances regardless of the action verb.
Supported actions: boot, start, shutdown, force_shutdown, delete, discontinue,
hibernate, configure_spot, delete_stuck, deploy, transfer.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			action := strings.ToLower(args[0])
			nameOrID := args[1]

			valid := map[string]bool{
				InstanceActionBoot:          true,
				InstanceActionStart:         true,
				InstanceActionShutdown:      true,
				InstanceActionForceShutdown: true,
				InstanceActionDelete:        true,
				InstanceActionDiscontinue:   true,
				InstanceActionHibernate:     true,
				InstanceActionConfigureSpot: true,
				InstanceActionDeleteStuck:   true,
				InstanceActionDeploy:        true,
				InstanceActionTransfer:      true,
			}
			if !valid[action] {
				return fmt.Errorf("invalid action %q", action)
			}

			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			id, err := client.ResolveInstanceID(ctx, nameOrID)
			if err != nil {
				return err
			}

			payload, err := json.Marshal(PerformInstanceActionRequest{
				Action: action,
				ID:     id,
			})
			if err != nil {
				return fmt.Errorf("marshal action: %w", err)
			}

			body, err := client.RunAPIWithContext(ctx, http.MethodPut, "/v1/instances", string(payload))
			if err != nil {
				return err
			}
			fmt.Println(prettyJSON(body))
			return nil
		},
	}
	return cmd
}

func createVerdaBalanceCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "balance",
		Short: "Show Verda account balance",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newClientFromFlags(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			body, err := client.RunAPIWithContext(ctx, http.MethodGet, "/v1/balance", "")
			if err != nil {
				return err
			}
			fmt.Println(prettyJSON(body))
			return nil
		},
	}
}

// prettyJSON re-encodes a JSON string with indent-2 for human-friendly output.
// Non-JSON input is returned verbatim.
func prettyJSON(s string) string {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return ""
	}
	if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
		return trimmed
	}
	var v interface{}
	if err := json.Unmarshal([]byte(trimmed), &v); err != nil {
		return trimmed
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return trimmed
	}
	return string(out)
}
