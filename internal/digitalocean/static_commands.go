package digitalocean

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// CreateDigitalOceanCommands creates the Digital Ocean command tree for static commands
func CreateDigitalOceanCommands() *cobra.Command {
	doCmd := &cobra.Command{
		Use:     "do",
		Short:   "Query Digital Ocean infrastructure directly",
		Long:    "Query your Digital Ocean infrastructure without AI interpretation. Useful for getting raw data.",
		Aliases: []string{"digitalocean"},
	}

	doListCmd := &cobra.Command{
		Use:   "list [resource]",
		Short: "List Digital Ocean resources",
		Long: `List Digital Ocean resources of a specific type.

Supported resources:
  droplets             - Compute droplets
  kubernetes, k8s      - Kubernetes clusters
  databases, dbs       - Managed databases
  spaces               - Spaces (object storage)
  apps                 - App Platform apps
  load-balancers, lbs  - Load balancers
  volumes              - Block storage volumes
  vpcs                 - Virtual private clouds
  domains              - DNS domains
  firewalls            - Cloud firewalls
  registries, registry - Container registries`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resourceType := strings.ToLower(strings.TrimSpace(args[0]))

			apiToken := ResolveAPIToken()
			if apiToken == "" {
				return fmt.Errorf("digitalocean api_token is required (set digitalocean.api_token, DO_API_TOKEN, or DIGITALOCEAN_ACCESS_TOKEN)")
			}

			debug := viper.GetBool("debug")
			client, err := NewClient(apiToken, debug)
			if err != nil {
				return err
			}

			ctx := context.Background()

			switch resourceType {
			case "droplets", "droplet":
				return listDroplets(ctx, client)

			case "kubernetes", "k8s", "clusters":
				return listKubernetes(ctx, client)

			case "databases", "dbs", "database":
				return listDatabases(ctx, client)

			case "spaces", "space":
				return listSpaces(ctx, client)

			case "apps", "app":
				return listApps(ctx, client)

			case "load-balancers", "lbs", "lb":
				return listLoadBalancers(ctx, client)

			case "volumes", "volume":
				return listVolumes(ctx, client)

			case "vpcs", "vpc":
				return listVPCs(ctx, client)

			case "domains", "domain", "dns":
				return listDomains(ctx, client)

			case "firewalls", "firewall":
				return listFirewalls(ctx, client)

			case "registries", "registry":
				return listRegistries(ctx, client)

			default:
				return fmt.Errorf("unknown resource type: %s", resourceType)
			}
		},
	}

	doCmd.AddCommand(doListCmd)

	return doCmd
}

// listDroplets lists all droplets
func listDroplets(ctx context.Context, client *Client) error {
	result, err := client.RunDoctl(ctx, "compute", "droplet", "list", "--format",
		"ID,Name,PublicIPv4,PrivateIPv4,Memory,VCPUs,Disk,Region,Image,Status", "--no-header")
	if err != nil {
		return fmt.Errorf("failed to list droplets: %w", err)
	}

	fmt.Println("Digital Ocean Droplets:")
	fmt.Println()
	if strings.TrimSpace(result) == "" {
		fmt.Println("  No droplets found")
	} else {
		fmt.Println(result)
	}
	return nil
}

// listKubernetes lists all Kubernetes clusters
func listKubernetes(ctx context.Context, client *Client) error {
	result, err := client.RunDoctl(ctx, "kubernetes", "cluster", "list", "--format",
		"ID,Name,Region,Version,Status,NodePools", "--no-header")
	if err != nil {
		return fmt.Errorf("failed to list kubernetes clusters: %w", err)
	}

	fmt.Println("Digital Ocean Kubernetes Clusters:")
	fmt.Println()
	if strings.TrimSpace(result) == "" {
		fmt.Println("  No kubernetes clusters found")
	} else {
		fmt.Println(result)
	}
	return nil
}

// listDatabases lists all managed databases
func listDatabases(ctx context.Context, client *Client) error {
	result, err := client.RunDoctl(ctx, "databases", "list", "--format",
		"ID,Name,Engine,Version,Region,Status,Size,NumNodes", "--no-header")
	if err != nil {
		return fmt.Errorf("failed to list databases: %w", err)
	}

	fmt.Println("Digital Ocean Managed Databases:")
	fmt.Println()
	if strings.TrimSpace(result) == "" {
		fmt.Println("  No databases found")
	} else {
		fmt.Println(result)
	}
	return nil
}

// listSpaces lists all Spaces (via regions that support Spaces)
func listSpaces(ctx context.Context, client *Client) error {
	result, err := client.RunDoctl(ctx, "compute", "region", "list", "--format",
		"Slug,Name,Available", "--no-header")
	if err != nil {
		return fmt.Errorf("failed to list regions for spaces: %w", err)
	}

	fmt.Println("Digital Ocean Regions (for Spaces):")
	fmt.Println()
	fmt.Println("Note: Use the Spaces API or s3cmd to list individual Spaces buckets.")
	fmt.Println("Regions available:")
	fmt.Println()
	if strings.TrimSpace(result) == "" {
		fmt.Println("  No regions found")
	} else {
		fmt.Println(result)
	}
	return nil
}

// listApps lists all App Platform apps
func listApps(ctx context.Context, client *Client) error {
	result, err := client.RunDoctl(ctx, "apps", "list", "--format",
		"ID,Spec.Name,DefaultIngress,ActiveDeployment.Phase,UpdatedAt", "--no-header")
	if err != nil {
		return fmt.Errorf("failed to list apps: %w", err)
	}

	fmt.Println("Digital Ocean App Platform Apps:")
	fmt.Println()
	if strings.TrimSpace(result) == "" {
		fmt.Println("  No apps found")
	} else {
		fmt.Println(result)
	}
	return nil
}

// listLoadBalancers lists all load balancers
func listLoadBalancers(ctx context.Context, client *Client) error {
	result, err := client.RunDoctl(ctx, "compute", "load-balancer", "list", "--format",
		"ID,Name,IP,Status,Region,Algorithm,Size", "--no-header")
	if err != nil {
		return fmt.Errorf("failed to list load balancers: %w", err)
	}

	fmt.Println("Digital Ocean Load Balancers:")
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
	result, err := client.RunDoctl(ctx, "compute", "volume", "list", "--format",
		"ID,Name,Size,Region,DropletIDs,Description", "--no-header")
	if err != nil {
		return fmt.Errorf("failed to list volumes: %w", err)
	}

	fmt.Println("Digital Ocean Volumes:")
	fmt.Println()
	if strings.TrimSpace(result) == "" {
		fmt.Println("  No volumes found")
	} else {
		fmt.Println(result)
	}
	return nil
}

// listVPCs lists all VPCs
func listVPCs(ctx context.Context, client *Client) error {
	result, err := client.RunDoctl(ctx, "vpcs", "list", "--format",
		"ID,Name,IPRange,Region,Description,Default", "--no-header")
	if err != nil {
		return fmt.Errorf("failed to list VPCs: %w", err)
	}

	fmt.Println("Digital Ocean VPCs:")
	fmt.Println()
	if strings.TrimSpace(result) == "" {
		fmt.Println("  No VPCs found")
	} else {
		fmt.Println(result)
	}
	return nil
}

// listDomains lists all DNS domains
func listDomains(ctx context.Context, client *Client) error {
	result, err := client.RunDoctl(ctx, "compute", "domain", "list", "--format",
		"Domain,TTL", "--no-header")
	if err != nil {
		return fmt.Errorf("failed to list domains: %w", err)
	}

	fmt.Println("Digital Ocean Domains:")
	fmt.Println()
	if strings.TrimSpace(result) == "" {
		fmt.Println("  No domains found")
	} else {
		fmt.Println(result)
	}
	return nil
}

// listFirewalls lists all cloud firewalls
func listFirewalls(ctx context.Context, client *Client) error {
	result, err := client.RunDoctl(ctx, "compute", "firewall", "list", "--format",
		"ID,Name,Status,DropletIDs,InboundRules,OutboundRules", "--no-header")
	if err != nil {
		return fmt.Errorf("failed to list firewalls: %w", err)
	}

	fmt.Println("Digital Ocean Firewalls:")
	fmt.Println()
	if strings.TrimSpace(result) == "" {
		fmt.Println("  No firewalls found")
	} else {
		fmt.Println(result)
	}
	return nil
}

// listRegistries lists container registries
func listRegistries(ctx context.Context, client *Client) error {
	result, err := client.RunDoctl(ctx, "registry", "get", "--format",
		"Name,Endpoint,Region,StorageUsageBytes,CreatedAt", "--no-header")
	if err != nil {
		return fmt.Errorf("failed to get registry: %w", err)
	}

	fmt.Println("Digital Ocean Container Registry:")
	fmt.Println()
	if strings.TrimSpace(result) == "" {
		fmt.Println("  No container registry found")
	} else {
		fmt.Println(result)
	}
	return nil
}
