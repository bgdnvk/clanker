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
  account              - Account profile
  actions              - Recent account actions
  droplets             - Compute droplets
  droplet-autoscale    - Droplet autoscale pools
  kubernetes, k8s      - Kubernetes clusters
  databases, dbs       - Managed databases
  spaces               - Spaces (object storage)
  spaces-keys          - Spaces access keys
  apps                 - App Platform apps
  functions            - Serverless Functions
  function-namespaces  - Serverless Functions namespaces
  serverless-inference-models - Serverless inference models
  dedicated-inference  - Dedicated inference endpoints
  dedicated-inference-sizes - Dedicated inference GPU sizes
  gradient-agents      - Gradient AI agents
  gradient-models      - Gradient AI models
  gradient-regions     - Gradient AI regions
  gradient-knowledge-bases - Gradient AI knowledge bases
  gradient-openai-keys - Gradient OpenAI API keys
  load-balancers, lbs  - Load balancers
  cdns                 - CDN endpoints
  volumes              - Block storage volumes
  nfs                  - Network file storage shares
  nfs-snapshots        - NFS snapshots
  vpcs                 - Virtual private clouds
  vpc-peerings         - VPC peerings
  vpc-nat-gateways     - VPC NAT gateways
  domains              - DNS domains
  firewalls            - Cloud firewalls
  reserved-ips         - Reserved IPv4 addresses
  reserved-ipv6        - Reserved IPv6 addresses
  certificates         - SSL certificates
  images               - Images
  snapshots            - Snapshots
  sizes                - Droplet size catalog
  regions              - Region catalog
  ssh-keys             - SSH keys
  tags                 - Tags
  one-clicks           - 1-Click applications
  monitoring-alerts    - Monitoring alert policies
  uptime-checks        - Uptime checks
  uptime-alerts        - Uptime alerts
  network-attachments  - Partner network attachments
  byoip-prefixes       - BYOIP prefixes
  security-scans       - CSPM security scans
  projects             - Projects
  project-resources    - Resources assigned to a project (requires --project-id)
  registries, registry - Container registries`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resourceType := strings.ToLower(strings.TrimSpace(args[0]))
			region, _ := cmd.Flags().GetString("region")
			projectID, _ := cmd.Flags().GetString("project-id")

			ctx := context.Background()
			apiToken := ResolveAPIToken()
			if apiToken == "" && !CLIAuthenticated(ctx) {
				return fmt.Errorf("digitalocean api_token is required (set digitalocean.api_token, DO_API_TOKEN, or DIGITALOCEAN_ACCESS_TOKEN)")
			}

			debug := viper.GetBool("debug")
			client, err := NewClient(apiToken, debug)
			if err != nil {
				return err
			}

			switch resourceType {
			case "account":
				return listDoctlResource(ctx, client, "Digital Ocean Account", "account", "get")

			case "actions":
				return listDoctlResource(ctx, client, "Digital Ocean Actions", "compute", "action", "list")

			case "droplets", "droplet":
				return listDroplets(ctx, client)

			case "droplet-autoscale", "droplet-autoscale-pools", "autoscale":
				return listDoctlResource(ctx, client, "Digital Ocean Droplet Autoscale Pools", "compute", "droplet-autoscale", "list")

			case "kubernetes", "k8s", "clusters":
				return listKubernetes(ctx, client)

			case "databases", "dbs", "database":
				return listDatabases(ctx, client)

			case "spaces", "space":
				return listSpaces(ctx, client)

			case "spaces-keys", "space-keys":
				return listDoctlResource(ctx, client, "Digital Ocean Spaces Keys", "spaces", "keys", "list")

			case "apps", "app":
				return listApps(ctx, client)

			case "functions", "function", "serverless-functions":
				return listFunctions(ctx, client)

			case "function-namespaces", "functions-namespaces", "serverless-namespaces":
				return listFunctionNamespaces(ctx, client)

			case "serverless-inference-models", "inference-models":
				return listDoctlResource(ctx, client, "Digital Ocean Serverless Inference Models", "serverless-inference", "models", "list")

			case "dedicated-inference", "dedicated-inference-endpoints":
				return listDoctlResource(ctx, client, "Digital Ocean Dedicated Inference Endpoints", "dedicated-inference", "list")

			case "dedicated-inference-sizes", "dedicated-inference-gpu-sizes":
				return listDoctlResource(ctx, client, "Digital Ocean Dedicated Inference GPU Sizes", "dedicated-inference", "get-sizes")

			case "gradient-agents", "gradient-agent", "ai-agents":
				return listGradientAgents(ctx, client, region)

			case "gradient-models", "gradient-model", "ai-models":
				return listGradientModels(ctx, client)

			case "gradient-regions":
				return listGradientRegions(ctx, client)

			case "gradient-knowledge-bases", "gradient-knowledge-base", "knowledge-bases":
				return listGradientKnowledgeBases(ctx, client)

			case "gradient-openai-keys", "openai-keys":
				return listDoctlResource(ctx, client, "Digital Ocean Gradient OpenAI API Keys", "gradient", "openai-key", "list")

			case "load-balancers", "lbs", "lb":
				return listLoadBalancers(ctx, client)

			case "cdns", "cdn":
				return listDoctlResource(ctx, client, "Digital Ocean CDN Endpoints", "compute", "cdn", "list")

			case "volumes", "volume":
				return listVolumes(ctx, client)

			case "nfs", "nfs-shares":
				return listDoctlResource(ctx, client, "Digital Ocean NFS Shares", append([]string{"nfs", "list"}, optionalRegionArgs(region)...)...)

			case "nfs-snapshots", "nfs-snapshot":
				return listDoctlResource(ctx, client, "Digital Ocean NFS Snapshots", append([]string{"nfs", "snapshot", "list"}, optionalRegionArgs(region)...)...)

			case "vpcs", "vpc":
				return listVPCs(ctx, client)

			case "vpc-peerings", "vpc-peering":
				return listDoctlResource(ctx, client, "Digital Ocean VPC Peerings", "vpcs", "peerings", "list")

			case "vpc-nat-gateways", "nat-gateways":
				return listDoctlResource(ctx, client, "Digital Ocean VPC NAT Gateways", "compute", "vpc-nat-gateway", "list")

			case "domains", "domain", "dns":
				return listDomains(ctx, client)

			case "firewalls", "firewall":
				return listFirewalls(ctx, client)

			case "reserved-ips", "reserved-ip", "floating-ips":
				return listDoctlResource(ctx, client, "Digital Ocean Reserved IPv4 Addresses", "compute", "reserved-ip", "list")

			case "reserved-ipv6", "reserved-ipv6s":
				return listDoctlResource(ctx, client, "Digital Ocean Reserved IPv6 Addresses", "compute", "reserved-ipv6", "list")

			case "certificates", "certificate":
				return listDoctlResource(ctx, client, "Digital Ocean Certificates", "compute", "certificate", "list")

			case "images", "image":
				return listDoctlResource(ctx, client, "Digital Ocean Images", "compute", "image", "list")

			case "snapshots", "snapshot":
				return listDoctlResource(ctx, client, "Digital Ocean Snapshots", "compute", "snapshot", "list")

			case "sizes", "size":
				return listDoctlResource(ctx, client, "Digital Ocean Droplet Sizes", "compute", "size", "list")

			case "regions", "region":
				return listDoctlResource(ctx, client, "Digital Ocean Regions", "compute", "region", "list")

			case "ssh-keys", "ssh-key", "keys":
				return listDoctlResource(ctx, client, "Digital Ocean SSH Keys", "compute", "ssh-key", "list")

			case "tags", "tag":
				return listDoctlResource(ctx, client, "Digital Ocean Tags", "compute", "tag", "list")

			case "one-clicks", "1-clicks", "one-click", "1-click":
				return listDoctlResource(ctx, client, "Digital Ocean 1-Click Applications", "1-click", "list")

			case "monitoring-alerts", "alerts":
				return listDoctlResource(ctx, client, "Digital Ocean Monitoring Alerts", "monitoring", "alert", "list")

			case "uptime-checks", "uptime":
				return listDoctlResource(ctx, client, "Digital Ocean Uptime Checks", "monitoring", "uptime", "list")

			case "uptime-alerts":
				return listDoctlResource(ctx, client, "Digital Ocean Uptime Alerts", "monitoring", "uptime", "alert", "list")

			case "network-attachments", "partner-attachments":
				return listDoctlResource(ctx, client, "Digital Ocean Partner Network Attachments", "network", "attachment", "list")

			case "byoip-prefixes", "byoip":
				return listDoctlResource(ctx, client, "Digital Ocean BYOIP Prefixes", "network", "byoip-prefix", "list")

			case "security-scans", "cspm-scans":
				return listDoctlResource(ctx, client, "Digital Ocean CSPM Security Scans", "security", "scans", "list")

			case "projects", "project":
				return listProjects(ctx, client)

			case "project-resources":
				if strings.TrimSpace(projectID) == "" {
					return fmt.Errorf("--project-id is required to list project resources")
				}
				return listDoctlResource(ctx, client, "Digital Ocean Project Resources", "projects", "resources", "list", strings.TrimSpace(projectID))

			case "registries", "registry":
				return listRegistries(ctx, client)

			default:
				return fmt.Errorf("unknown resource type: %s", resourceType)
			}
		},
	}

	doListCmd.Flags().String("region", "", "DigitalOcean region for region-scoped resources such as Gradient AI agents")
	doListCmd.Flags().String("project-id", "", "DigitalOcean project ID for project-scoped resources")
	doCmd.AddCommand(doListCmd)

	return doCmd
}

func listDoctlResource(ctx context.Context, client *Client, title string, args ...string) error {
	result, err := client.RunDoctl(ctx, args...)
	if err != nil {
		return fmt.Errorf("failed to list %s: %w", strings.ToLower(title), err)
	}

	fmt.Println(title + ":")
	fmt.Println()
	if strings.TrimSpace(result) == "" {
		fmt.Println("  No resources found")
	} else {
		fmt.Println(result)
	}
	return nil
}

func optionalRegionArgs(region string) []string {
	if strings.TrimSpace(region) == "" {
		return nil
	}
	return []string{"--region", strings.TrimSpace(region)}
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

func listFunctions(ctx context.Context, client *Client) error {
	result, err := client.RunDoctl(ctx, "serverless", "functions", "list", "--format", "Update,Version,Runtime,Function", "--no-header")
	if err != nil {
		return fmt.Errorf("failed to list functions: %w", err)
	}

	fmt.Println("Digital Ocean Serverless Functions:")
	fmt.Println()
	if strings.TrimSpace(result) == "" {
		fmt.Println("  No functions found")
	} else {
		fmt.Println(result)
	}
	return nil
}

func listFunctionNamespaces(ctx context.Context, client *Client) error {
	result, err := client.RunDoctl(ctx, "serverless", "namespaces", "list")
	if err != nil {
		return fmt.Errorf("failed to list functions namespaces: %w", err)
	}

	fmt.Println("Digital Ocean Serverless Function Namespaces:")
	fmt.Println()
	if strings.TrimSpace(result) == "" {
		fmt.Println("  No function namespaces found")
	} else {
		fmt.Println(result)
	}
	return nil
}

func listGradientAgents(ctx context.Context, client *Client, region string) error {
	args := []string{"gradient", "agent", "list"}
	if strings.TrimSpace(region) != "" {
		args = append(args, "--region", strings.TrimSpace(region))
	}
	args = append(args, "--format", "Id,Name,Region,ModelId,ProjectId,CreatedAt", "--no-header")
	result, err := client.RunDoctl(ctx, args...)
	if err != nil {
		return fmt.Errorf("failed to list Gradient AI agents: %w", err)
	}

	fmt.Println("Digital Ocean Gradient AI Agents:")
	fmt.Println()
	if strings.TrimSpace(result) == "" {
		fmt.Println("  No Gradient AI agents found")
	} else {
		fmt.Println(result)
	}
	return nil
}

func listGradientModels(ctx context.Context, client *Client) error {
	result, err := client.RunDoctl(ctx, "gradient", "list-models", "--format", "Id,Name,Agreement,CreatedAt,isFoundational", "--no-header")
	if err != nil {
		return fmt.Errorf("failed to list Gradient AI models: %w", err)
	}

	fmt.Println("Digital Ocean Gradient AI Models:")
	fmt.Println()
	if strings.TrimSpace(result) == "" {
		fmt.Println("  No Gradient AI models found")
	} else {
		fmt.Println(result)
	}
	return nil
}

func listGradientRegions(ctx context.Context, client *Client) error {
	result, err := client.RunDoctl(ctx, "gradient", "list-regions", "--format", "Region,InferenceUrl,ServesInference,ServesBatch", "--no-header")
	if err != nil {
		return fmt.Errorf("failed to list Gradient AI regions: %w", err)
	}

	fmt.Println("Digital Ocean Gradient AI Regions:")
	fmt.Println()
	if strings.TrimSpace(result) == "" {
		fmt.Println("  No Gradient AI regions found")
	} else {
		fmt.Println(result)
	}
	return nil
}

func listGradientKnowledgeBases(ctx context.Context, client *Client) error {
	result, err := client.RunDoctl(ctx, "gradient", "knowledge-base", "list", "--format", "UUID,Name,Region,ProjectId,CreatedAt,UpdatedAt", "--no-header")
	if err != nil {
		return fmt.Errorf("failed to list Gradient AI knowledge bases: %w", err)
	}

	fmt.Println("Digital Ocean Gradient AI Knowledge Bases:")
	fmt.Println()
	if strings.TrimSpace(result) == "" {
		fmt.Println("  No Gradient AI knowledge bases found")
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

func listProjects(ctx context.Context, client *Client) error {
	result, err := client.RunDoctl(ctx, "projects", "list", "--format", "ID,Name,Purpose,Environment,IsDefault,CreatedAt", "--no-header")
	if err != nil {
		return fmt.Errorf("failed to list projects: %w", err)
	}

	fmt.Println("Digital Ocean Projects:")
	fmt.Println()
	if strings.TrimSpace(result) == "" {
		fmt.Println("  No projects found")
	} else {
		fmt.Println(result)
	}
	return nil
}

// listRegistries lists container registries
func listRegistries(ctx context.Context, client *Client) error {
	result, err := client.RunDoctl(ctx, "registries", "list", "--format",
		"Name,Endpoint,Region,StorageUsageBytes,CreatedAt", "--no-header")
	if err != nil {
		return fmt.Errorf("failed to list registries: %w", err)
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
