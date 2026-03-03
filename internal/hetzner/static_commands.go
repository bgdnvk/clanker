package hetzner

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// CreateHetznerCommands creates the Hetzner command tree for static commands
func CreateHetznerCommands() *cobra.Command {
	hetznerCmd := &cobra.Command{
		Use:     "hetzner",
		Short:   "Query Hetzner Cloud infrastructure directly",
		Long:    "Query your Hetzner Cloud infrastructure without AI interpretation. Useful for getting raw data.",
		Aliases: []string{"hz"},
	}

	hetznerListCmd := &cobra.Command{
		Use:   "list [resource]",
		Short: "List Hetzner Cloud resources",
		Long: `List Hetzner Cloud resources of a specific type.

Supported resources:
  servers              - Cloud servers
  load-balancers, lbs  - Load balancers
  volumes              - Block storage volumes
  networks             - Networks
  firewalls            - Firewalls
  floating-ips         - Floating IPs
  primary-ips          - Primary IPs
  ssh-keys             - SSH keys
  images               - Images and snapshots
  certificates         - TLS certificates`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resourceType := strings.ToLower(strings.TrimSpace(args[0]))

			apiToken := ResolveAPIToken()
			if apiToken == "" {
				return fmt.Errorf("hetzner api_token is required (set hetzner.api_token or HCLOUD_TOKEN)")
			}

			debug := viper.GetBool("debug")
			client, err := NewClient(apiToken, debug)
			if err != nil {
				return err
			}

			ctx := context.Background()

			switch resourceType {
			case "servers", "server":
				return listServers(ctx, client)

			case "load-balancers", "lbs", "lb":
				return listLoadBalancers(ctx, client)

			case "volumes", "volume":
				return listVolumes(ctx, client)

			case "networks", "network":
				return listNetworks(ctx, client)

			case "firewalls", "firewall":
				return listFirewalls(ctx, client)

			case "floating-ips", "floating-ip", "fip":
				return listFloatingIPs(ctx, client)

			case "primary-ips", "primary-ip", "pip":
				return listPrimaryIPs(ctx, client)

			case "ssh-keys", "ssh-key", "keys":
				return listSSHKeys(ctx, client)

			case "images", "image":
				return listImages(ctx, client)

			case "certificates", "certificate", "cert", "certs":
				return listCertificates(ctx, client)

			default:
				return fmt.Errorf("unknown resource type: %s", resourceType)
			}
		},
	}

	hetznerCmd.AddCommand(hetznerListCmd)

	return hetznerCmd
}

// listServers lists all servers
func listServers(ctx context.Context, client *Client) error {
	result, err := client.RunHcloud(ctx, "server", "list")
	if err != nil {
		return fmt.Errorf("failed to list servers: %w", err)
	}

	fmt.Println("Hetzner Cloud Servers:")
	fmt.Println()
	if strings.TrimSpace(result) == "" {
		fmt.Println("  No servers found")
	} else {
		fmt.Println(result)
	}
	return nil
}

// listLoadBalancers lists all load balancers
func listLoadBalancers(ctx context.Context, client *Client) error {
	result, err := client.RunHcloud(ctx, "load-balancer", "list")
	if err != nil {
		return fmt.Errorf("failed to list load balancers: %w", err)
	}

	fmt.Println("Hetzner Cloud Load Balancers:")
	fmt.Println()
	if strings.TrimSpace(result) == "" {
		fmt.Println("  No load balancers found")
	} else {
		fmt.Println(result)
	}
	return nil
}

// listVolumes lists all block storage volumes
func listVolumes(ctx context.Context, client *Client) error {
	result, err := client.RunHcloud(ctx, "volume", "list")
	if err != nil {
		return fmt.Errorf("failed to list volumes: %w", err)
	}

	fmt.Println("Hetzner Cloud Volumes:")
	fmt.Println()
	if strings.TrimSpace(result) == "" {
		fmt.Println("  No volumes found")
	} else {
		fmt.Println(result)
	}
	return nil
}

// listNetworks lists all networks
func listNetworks(ctx context.Context, client *Client) error {
	result, err := client.RunHcloud(ctx, "network", "list")
	if err != nil {
		return fmt.Errorf("failed to list networks: %w", err)
	}

	fmt.Println("Hetzner Cloud Networks:")
	fmt.Println()
	if strings.TrimSpace(result) == "" {
		fmt.Println("  No networks found")
	} else {
		fmt.Println(result)
	}
	return nil
}

// listFirewalls lists all firewalls
func listFirewalls(ctx context.Context, client *Client) error {
	result, err := client.RunHcloud(ctx, "firewall", "list")
	if err != nil {
		return fmt.Errorf("failed to list firewalls: %w", err)
	}

	fmt.Println("Hetzner Cloud Firewalls:")
	fmt.Println()
	if strings.TrimSpace(result) == "" {
		fmt.Println("  No firewalls found")
	} else {
		fmt.Println(result)
	}
	return nil
}

// listFloatingIPs lists all floating IPs
func listFloatingIPs(ctx context.Context, client *Client) error {
	result, err := client.RunHcloud(ctx, "floating-ip", "list")
	if err != nil {
		return fmt.Errorf("failed to list floating IPs: %w", err)
	}

	fmt.Println("Hetzner Cloud Floating IPs:")
	fmt.Println()
	if strings.TrimSpace(result) == "" {
		fmt.Println("  No floating IPs found")
	} else {
		fmt.Println(result)
	}
	return nil
}

// listPrimaryIPs lists all primary IPs
func listPrimaryIPs(ctx context.Context, client *Client) error {
	result, err := client.RunHcloud(ctx, "primary-ip", "list")
	if err != nil {
		return fmt.Errorf("failed to list primary IPs: %w", err)
	}

	fmt.Println("Hetzner Cloud Primary IPs:")
	fmt.Println()
	if strings.TrimSpace(result) == "" {
		fmt.Println("  No primary IPs found")
	} else {
		fmt.Println(result)
	}
	return nil
}

// listSSHKeys lists all SSH keys
func listSSHKeys(ctx context.Context, client *Client) error {
	result, err := client.RunHcloud(ctx, "ssh-key", "list")
	if err != nil {
		return fmt.Errorf("failed to list SSH keys: %w", err)
	}

	fmt.Println("Hetzner Cloud SSH Keys:")
	fmt.Println()
	if strings.TrimSpace(result) == "" {
		fmt.Println("  No SSH keys found")
	} else {
		fmt.Println(result)
	}
	return nil
}

// listImages lists all images
func listImages(ctx context.Context, client *Client) error {
	result, err := client.RunHcloud(ctx, "image", "list")
	if err != nil {
		return fmt.Errorf("failed to list images: %w", err)
	}

	fmt.Println("Hetzner Cloud Images:")
	fmt.Println()
	if strings.TrimSpace(result) == "" {
		fmt.Println("  No images found")
	} else {
		fmt.Println(result)
	}
	return nil
}

// listCertificates lists all certificates
func listCertificates(ctx context.Context, client *Client) error {
	result, err := client.RunHcloud(ctx, "certificate", "list")
	if err != nil {
		return fmt.Errorf("failed to list certificates: %w", err)
	}

	fmt.Println("Hetzner Cloud Certificates:")
	fmt.Println()
	if strings.TrimSpace(result) == "" {
		fmt.Println("  No certificates found")
	} else {
		fmt.Println(result)
	}
	return nil
}
