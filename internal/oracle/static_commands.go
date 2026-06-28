package oracle

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

type ResourceDefinition struct {
	Name             string
	Title            string
	Aliases          []string
	Args             []string
	Scope            string
	Keys             []string
	NeedsNamespace   bool
	NeedsCompartment bool
}

type ListResult struct {
	Title    string           `json:"title"`
	Resource string           `json:"resource"`
	Data     []map[string]any `json:"data,omitempty"`
	Raw      string           `json:"raw,omitempty"`
	Warnings []string         `json:"warnings,omitempty"`
}

const (
	scopeTenancy     = "tenancy"
	scopeCompartment = "compartment"
)

var resourceDefinitions = []ResourceDefinition{
	{Name: "compartments", Title: "Oracle Cloud Compartments", Aliases: []string{"compartment"}, Scope: scopeTenancy, Args: []string{"iam", "compartment", "list", "--access-level", "ACCESSIBLE", "--compartment-id-in-subtree", "true", "--all"}, Keys: []string{"compartment", "compartments", "tenancy"}},
	{Name: "instances", Title: "Oracle Cloud Compute Instances", Aliases: []string{"instance", "compute", "vms", "vm"}, Scope: scopeCompartment, Args: []string{"compute", "instance", "list", "--all"}, Keys: []string{"instance", "instances", "compute", "vm", "virtual machine"}},
	{Name: "instance-pools", Title: "Oracle Cloud Instance Pools", Aliases: []string{"instance-pool", "pools"}, Scope: scopeCompartment, Args: []string{"compute-management", "instance-pool", "list", "--all"}, Keys: []string{"instance pool", "instance pools", "autoscale", "autoscaling"}},
	{Name: "images", Title: "Oracle Cloud Compute Images", Aliases: []string{"image"}, Scope: scopeCompartment, Args: []string{"compute", "image", "list", "--all"}, Keys: []string{"image", "images", "custom image"}},
	{Name: "boot-volumes", Title: "Oracle Cloud Boot Volumes", Aliases: []string{"boot-volume"}, Scope: scopeCompartment, Args: []string{"bv", "boot-volume", "list", "--all"}, Keys: []string{"boot volume", "boot volumes"}},
	{Name: "volumes", Title: "Oracle Cloud Block Volumes", Aliases: []string{"volume", "block-volumes", "block-volume"}, Scope: scopeCompartment, Args: []string{"bv", "volume", "list", "--all"}, Keys: []string{"volume", "volumes", "block volume", "block storage", "disk"}},
	{Name: "volume-backups", Title: "Oracle Cloud Volume Backups", Aliases: []string{"volume-backup", "backups"}, Scope: scopeCompartment, Args: []string{"bv", "backup", "list", "--all"}, Keys: []string{"volume backup", "volume backups", "backup", "backups"}},
	{Name: "vcns", Title: "Oracle Cloud VCNs", Aliases: []string{"vcn", "networks", "network"}, Scope: scopeCompartment, Args: []string{"network", "vcn", "list", "--all"}, Keys: []string{"vcn", "vcns", "network", "networks", "virtual cloud network"}},
	{Name: "subnets", Title: "Oracle Cloud Subnets", Aliases: []string{"subnet"}, Scope: scopeCompartment, Args: []string{"network", "subnet", "list", "--all"}, Keys: []string{"subnet", "subnets"}},
	{Name: "route-tables", Title: "Oracle Cloud Route Tables", Aliases: []string{"route-table", "routes"}, Scope: scopeCompartment, Args: []string{"network", "route-table", "list", "--all"}, Keys: []string{"route table", "route tables", "routes"}},
	{Name: "security-lists", Title: "Oracle Cloud Security Lists", Aliases: []string{"security-list"}, Scope: scopeCompartment, Args: []string{"network", "security-list", "list", "--all"}, Keys: []string{"security list", "security lists"}},
	{Name: "nsgs", Title: "Oracle Cloud Network Security Groups", Aliases: []string{"nsg", "network-security-groups", "network-security-group"}, Scope: scopeCompartment, Args: []string{"network", "nsg", "list", "--all"}, Keys: []string{"nsg", "network security group", "network security groups"}},
	{Name: "internet-gateways", Title: "Oracle Cloud Internet Gateways", Aliases: []string{"internet-gateway", "igw"}, Scope: scopeCompartment, Args: []string{"network", "internet-gateway", "list", "--all"}, Keys: []string{"internet gateway", "internet gateways", "igw"}},
	{Name: "nat-gateways", Title: "Oracle Cloud NAT Gateways", Aliases: []string{"nat-gateway"}, Scope: scopeCompartment, Args: []string{"network", "nat-gateway", "list", "--all"}, Keys: []string{"nat gateway", "nat gateways"}},
	{Name: "service-gateways", Title: "Oracle Cloud Service Gateways", Aliases: []string{"service-gateway"}, Scope: scopeCompartment, Args: []string{"network", "service-gateway", "list", "--all"}, Keys: []string{"service gateway", "service gateways"}},
	{Name: "load-balancers", Title: "Oracle Cloud Load Balancers", Aliases: []string{"load-balancer", "lbs", "lb"}, Scope: scopeCompartment, Args: []string{"lb", "load-balancer", "list", "--all"}, Keys: []string{"load balancer", "load balancers", "lb", "lbs"}},
	{Name: "network-load-balancers", Title: "Oracle Cloud Network Load Balancers", Aliases: []string{"network-load-balancer", "nlbs", "nlb"}, Scope: scopeCompartment, Args: []string{"nlb", "network-load-balancer", "list", "--all"}, Keys: []string{"network load balancer", "network load balancers", "nlb", "nlbs"}},
	{Name: "buckets", Title: "Oracle Cloud Object Storage Buckets", Aliases: []string{"bucket", "object-storage"}, Scope: scopeCompartment, Args: []string{"os", "bucket", "list", "--all"}, Keys: []string{"bucket", "buckets", "object storage"}, NeedsNamespace: true},
	{Name: "file-systems", Title: "Oracle Cloud File Systems", Aliases: []string{"file-system", "fss"}, Scope: scopeCompartment, Args: []string{"fs", "file-system", "list", "--all"}, Keys: []string{"file system", "file systems", "fss", "filesystem"}},
	{Name: "mount-targets", Title: "Oracle Cloud Mount Targets", Aliases: []string{"mount-target"}, Scope: scopeCompartment, Args: []string{"fs", "mount-target", "list", "--all"}, Keys: []string{"mount target", "mount targets"}},
	{Name: "databases", Title: "Oracle Cloud DB Systems", Aliases: []string{"db-systems", "db-system", "database"}, Scope: scopeCompartment, Args: []string{"db", "system", "list", "--all"}, Keys: []string{"database", "databases", "db system", "db systems"}},
	{Name: "autonomous-databases", Title: "Oracle Cloud Autonomous Databases", Aliases: []string{"autonomous-database", "adb"}, Scope: scopeCompartment, Args: []string{"db", "autonomous-database", "list", "--all"}, Keys: []string{"autonomous database", "autonomous databases", "adb"}},
	{Name: "mysql-systems", Title: "Oracle Cloud MySQL DB Systems", Aliases: []string{"mysql", "mysql-db-systems"}, Scope: scopeCompartment, Args: []string{"mysql", "db-system", "list", "--all"}, Keys: []string{"mysql", "mysql db system", "mysql db systems"}},
	{Name: "oke-clusters", Title: "Oracle Kubernetes Engine Clusters", Aliases: []string{"oke", "clusters", "cluster", "kubernetes"}, Scope: scopeCompartment, Args: []string{"ce", "cluster", "list", "--all"}, Keys: []string{"oke", "kubernetes", "cluster", "clusters", "container engine"}},
	{Name: "node-pools", Title: "Oracle Kubernetes Engine Node Pools", Aliases: []string{"node-pool", "oke-node-pools"}, Scope: scopeCompartment, Args: []string{"ce", "node-pool", "list", "--all"}, Keys: []string{"node pool", "node pools"}},
	{Name: "functions", Title: "Oracle Cloud Functions Applications", Aliases: []string{"fn", "function-apps", "applications"}, Scope: scopeCompartment, Args: []string{"fn", "application", "list", "--all"}, Keys: []string{"function", "functions", "fn", "serverless"}},
	{Name: "container-repositories", Title: "Oracle Cloud Container Repositories", Aliases: []string{"container-repository", "repos", "repositories", "ocir"}, Scope: scopeCompartment, Args: []string{"artifacts", "container", "repository", "list", "--all"}, Keys: []string{"container repository", "container repositories", "ocir", "registry", "image registry"}},
	{Name: "api-gateways", Title: "Oracle Cloud API Gateways", Aliases: []string{"api-gateway", "gateways"}, Scope: scopeCompartment, Args: []string{"api-gateway", "gateway", "list", "--all"}, Keys: []string{"api gateway", "api gateways", "gateway", "gateways"}},
	{Name: "dns-zones", Title: "Oracle Cloud DNS Zones", Aliases: []string{"dns", "zones", "zone"}, Scope: scopeCompartment, Args: []string{"dns", "zone", "list", "--all"}, Keys: []string{"dns", "zone", "zones"}},
	{Name: "vaults", Title: "Oracle Cloud Vaults", Aliases: []string{"vault"}, Scope: scopeCompartment, Args: []string{"vault", "vault", "list", "--all"}, Keys: []string{"vault", "vaults", "kms"}},
	{Name: "secrets", Title: "Oracle Cloud Vault Secrets", Aliases: []string{"secret"}, Scope: scopeCompartment, Args: []string{"vault", "secret", "list", "--all"}, Keys: []string{"secret", "secrets"}},
	{Name: "streams", Title: "Oracle Cloud Streams", Aliases: []string{"stream", "streaming"}, Scope: scopeCompartment, Args: []string{"streaming", "admin", "stream", "list", "--all"}, Keys: []string{"stream", "streams", "streaming"}},
	{Name: "queues", Title: "Oracle Cloud Queues", Aliases: []string{"queue"}, Scope: scopeCompartment, Args: []string{"queue", "queue-admin", "queue", "list", "--all"}, Keys: []string{"queue", "queues"}},
	{Name: "alarms", Title: "Oracle Cloud Monitoring Alarms", Aliases: []string{"alarm", "monitoring"}, Scope: scopeCompartment, Args: []string{"monitoring", "alarm", "list", "--all"}, Keys: []string{"alarm", "alarms", "monitoring"}},
	{Name: "log-groups", Title: "Oracle Cloud Log Groups", Aliases: []string{"log-group", "logs", "logging"}, Scope: scopeCompartment, Args: []string{"logging", "log-group", "list", "--all"}, Keys: []string{"log group", "log groups", "logs", "logging"}},
	{Name: "events-rules", Title: "Oracle Cloud Events Rules", Aliases: []string{"events", "rules", "event-rules"}, Scope: scopeCompartment, Args: []string{"events", "rule", "list", "--all"}, Keys: []string{"event", "events", "rule", "rules"}},
}

func CreateOracleCommands() *cobra.Command {
	oracleCmd := &cobra.Command{
		Use:     "oracle",
		Short:   "Query Oracle Cloud Infrastructure directly",
		Long:    "Query your Oracle Cloud Infrastructure without AI interpretation. Uses the official OCI CLI and local OCI config profiles.",
		Aliases: []string{"oci"},
	}

	listCmd := &cobra.Command{
		Use:   "list [resource]",
		Short: "List Oracle Cloud resources",
		Long:  oracleListHelp(),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			profile, _ := cmd.Flags().GetString("profile")
			compartmentID, _ := cmd.Flags().GetString("compartment-id")
			tenancyOCID, _ := cmd.Flags().GetString("tenancy-ocid")
			client, err := NewClient(profile, compartmentID, tenancyOCID, viper.GetBool("debug"))
			if err != nil {
				return err
			}
			result, err := client.ListResource(context.Background(), args[0])
			if err != nil {
				return err
			}
			body, err := json.MarshalIndent(result, "", "  ")
			if err != nil {
				return err
			}
			fmt.Println(string(body))
			return nil
		},
	}
	listCmd.Flags().String("profile", "", "OCI CLI profile name (default: oracle.profile, OCI_CLI_PROFILE, or DEFAULT)")
	listCmd.Flags().String("compartment-id", "", "OCI compartment OCID to query (default: oracle.compartment_id, OCI_COMPARTMENT_ID, or tenancy OCID)")
	listCmd.Flags().String("tenancy-ocid", "", "OCI tenancy OCID for compartment discovery (default: oracle.tenancy_ocid, OCI_TENANCY_OCID, or ~/.oci/config tenancy)")
	oracleCmd.AddCommand(listCmd)

	profilesCmd := &cobra.Command{
		Use:   "profiles",
		Short: "List local OCI config profiles",
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := json.MarshalIndent(map[string]any{"profiles": ConfigProfiles()}, "", "  ")
			if err != nil {
				return err
			}
			fmt.Println(string(body))
			return nil
		},
	}
	oracleCmd.AddCommand(profilesCmd)

	return oracleCmd
}

func oracleListHelp() string {
	var b strings.Builder
	b.WriteString("List Oracle Cloud resources of a specific type.\n\nSupported resources:\n")
	for _, def := range resourceDefinitions {
		b.WriteString("  ")
		b.WriteString(def.Name)
		if len(def.Aliases) > 0 {
			b.WriteString(" (aliases: ")
			b.WriteString(strings.Join(def.Aliases, ", "))
			b.WriteString(")")
		}
		b.WriteString("\n")
	}
	return b.String()
}

func lookupResourceDefinition(resource string) (ResourceDefinition, bool) {
	resource = strings.ToLower(strings.TrimSpace(resource))
	for _, def := range resourceDefinitions {
		if resource == def.Name {
			return def, true
		}
		for _, alias := range def.Aliases {
			if resource == alias {
				return def, true
			}
		}
	}
	return ResourceDefinition{}, false
}

func (c *Client) ListResource(ctx context.Context, resource string) (ListResult, error) {
	def, ok := lookupResourceDefinition(resource)
	if !ok {
		return ListResult{}, fmt.Errorf("unknown Oracle Cloud resource type: %s", strings.TrimSpace(resource))
	}
	result := ListResult{Title: def.Title, Resource: def.Name}

	if def.Scope == scopeTenancy {
		tenancyID := strings.TrimSpace(c.tenancyOCID)
		if tenancyID == "" {
			tenancyID = strings.TrimSpace(c.compartmentID)
		}
		if tenancyID == "" {
			return result, fmt.Errorf("Oracle tenancy OCID is required for %s (set oracle.tenancy_ocid, OCI_TENANCY_OCID, or configure ~/.oci/config)", def.Name)
		}
		args := append([]string{}, def.Args...)
		args = append(args, "--compartment-id", tenancyID)
		raw, err := c.RunOCI(ctx, args...)
		if err != nil {
			return result, err
		}
		result.Raw = raw
		result.Data = parseOCIDataArray(raw, "")
		return result, nil
	}

	compartments, warnings := c.compartments(ctx)
	result.Warnings = append(result.Warnings, warnings...)
	namespace := ""
	if def.NeedsNamespace {
		ns, err := c.objectStorageNamespace(ctx)
		if err != nil {
			return result, err
		}
		namespace = ns
	}

	for _, compartmentID := range compartments {
		args := append([]string{}, def.Args...)
		if def.NeedsCompartment || def.Scope == scopeCompartment {
			args = append(args, "--compartment-id", compartmentID)
		}
		if def.NeedsNamespace {
			args = append(args, "--namespace-name", namespace)
		}
		raw, err := c.RunOCI(ctx, args...)
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("%s in %s: %v", def.Name, shortOCID(compartmentID), err))
			continue
		}
		result.Data = append(result.Data, parseOCIDataArray(raw, compartmentID)...)
		if strings.TrimSpace(result.Raw) == "" {
			result.Raw = raw
		}
	}
	return result, nil
}

func (c *Client) compartments(ctx context.Context) ([]string, []string) {
	if compartmentID := strings.TrimSpace(c.compartmentID); compartmentID != "" && compartmentID != strings.TrimSpace(c.tenancyOCID) {
		return []string{compartmentID}, nil
	}
	tenancyID := strings.TrimSpace(c.tenancyOCID)
	if tenancyID == "" {
		tenancyID = strings.TrimSpace(c.compartmentID)
	}
	if tenancyID == "" {
		return nil, []string{"missing tenancy OCID; set --compartment-id or oracle.tenancy_ocid/OCI_TENANCY_OCID"}
	}
	ids := []string{tenancyID}
	raw, err := c.RunOCI(ctx, "iam", "compartment", "list", "--compartment-id", tenancyID, "--access-level", "ACCESSIBLE", "--compartment-id-in-subtree", "true", "--all", "--lifecycle-state", "ACTIVE")
	if err != nil {
		return ids, []string{fmt.Sprintf("could not list child compartments: %v", err)}
	}
	for _, item := range parseOCIDataArray(raw, "") {
		if id := stringValue(item, "id"); id != "" {
			ids = append(ids, id)
		}
	}
	return uniqueSorted(ids), nil
}

func (c *Client) objectStorageNamespace(ctx context.Context) (string, error) {
	raw, err := c.RunOCI(ctx, "os", "ns", "get")
	if err != nil {
		return "", fmt.Errorf("failed to resolve OCI Object Storage namespace: %w", err)
	}
	var payload struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err == nil && strings.TrimSpace(payload.Data) != "" {
		return strings.TrimSpace(payload.Data), nil
	}
	return "", fmt.Errorf("failed to parse OCI Object Storage namespace")
}

func parseOCIDataArray(raw string, compartmentID string) []map[string]any {
	var payload struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil
	}
	items := make([]map[string]any, 0, len(payload.Data))
	for _, item := range payload.Data {
		if strings.TrimSpace(compartmentID) != "" {
			item["compartmentId"] = compartmentID
		}
		items = append(items, item)
	}
	return items
}

func stringValue(m map[string]any, key string) string {
	if value, ok := m[key]; ok {
		return strings.TrimSpace(fmt.Sprint(value))
	}
	return ""
}

func shortOCID(id string) string {
	id = strings.TrimSpace(id)
	if len(id) <= 18 {
		return id
	}
	return id[:18] + "..."
}
