package azure

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// CreateAzureCommands creates the Azure command tree for static commands.
func CreateAzureCommands() *cobra.Command {
	azureCmd := &cobra.Command{
		Use:   "azure",
		Short: "Query Azure infrastructure directly",
		Long:  "Query your Azure infrastructure without AI interpretation. Useful for getting raw data.",
	}

	azureListCmd := &cobra.Command{
		Use:   "list [resource]",
		Short: "List Azure resources",
		Long: `List Azure resources of a specific type.

Supported resources:
  account              - Current account context
  groups, rg           - Resource groups
  resources            - ARM resources (top 200)
  vms                  - Virtual machines
  containers, aci      - Container Instances
  aks                  - AKS clusters
  containerapps        - Azure Container Apps
  webapps              - App Services (webapps)
  functionapps         - Function Apps
  static-webapps       - Static Web Apps
  acr                  - Azure Container Registries
  storage              - Storage accounts
  keyvaults            - Key Vaults
  cosmosdb             - Cosmos DB accounts
  sql-servers          - Azure SQL servers
  sql-databases        - Azure SQL databases
  postgres             - Azure PostgreSQL Flexible Servers
  mysql                - Azure MySQL Flexible Servers
  redis                - Azure Cache for Redis
  ai-services          - Azure AI Services / Azure OpenAI resources
  ai-search            - Azure AI Search services
  servicebus           - Service Bus namespaces
  eventhubs            - Event Hubs namespaces
  eventgrid            - Event Grid topics
  apim                 - API Management services
  log-analytics        - Log Analytics workspaces
  app-insights         - Application Insights components
  front-door           - Front Door / CDN profiles
  vnets                - Virtual networks
  nsgs                 - Network security groups
  public-ips           - Public IP addresses
  load-balancers, lbs  - Load balancers`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resourceType := strings.ToLower(strings.TrimSpace(args[0]))
			subscriptionID, _ := cmd.Flags().GetString("subscription")
			if subscriptionID == "" {
				subscriptionID = ResolveSubscriptionID()
			}
			if subscriptionID == "" {
				return fmt.Errorf("azure subscription_id is required (set infra.azure.subscription_id, AZURE_SUBSCRIPTION_ID, or use --subscription)")
			}

			debug := viper.GetBool("debug")
			client, err := NewClient(subscriptionID, debug)
			if err != nil {
				return err
			}

			ctx := context.Background()
			exec := func(args ...string) (string, error) {
				return client.execAz(ctx, args...)
			}

			switch resourceType {
			case "account":
				result, err := exec("account", "show", "--output", "json")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "groups", "rg", "resourcegroups", "resource-groups":
				result, err := exec("group", "list", "--output", "table")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "resources":
				result, err := exec("resource", "list", "--query", "[:200].{name:name,type:type,location:location,resourceGroup:resourceGroup}", "--output", "table")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "vms", "vm":
				result, err := exec("vm", "list", "-d", "--output", "table")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "aks", "clusters":
				result, err := exec("aks", "list", "--output", "table")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "containers", "container", "aci":
				result, err := exec("container", "list", "--output", "table")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "containerapps", "containerapp", "aca":
				result, err := exec("containerapp", "list", "--output", "table")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "webapps", "webapp", "appservice", "appservices":
				result, err := exec("webapp", "list", "--output", "table")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "functionapps", "functionapp", "functions":
				result, err := exec("functionapp", "list", "--output", "table")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "static-webapps", "staticwebapps", "staticwebapp":
				result, err := exec("staticwebapp", "list", "--output", "table")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "acr", "registries", "container-registries":
				result, err := exec("acr", "list", "--output", "table")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "storage", "storageaccounts", "storage-accounts":
				result, err := exec("storage", "account", "list", "--output", "table")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "keyvaults", "keyvault", "vaults":
				result, err := exec("keyvault", "list", "--output", "table")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "cosmosdb", "cosmos":
				result, err := exec("cosmosdb", "list", "--output", "table")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "sql-servers":
				result, err := exec("resource", "list", "--resource-type", "Microsoft.Sql/servers", "--query", "[:200].{name:name,location:location,resourceGroup:resourceGroup}", "--output", "table")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "sql-databases":
				result, err := exec("resource", "list", "--resource-type", "Microsoft.Sql/servers/databases", "--query", "[:200].{name:name,location:location,resourceGroup:resourceGroup}", "--output", "table")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "postgres", "postgresql":
				result, err := exec("resource", "list", "--resource-type", "Microsoft.DBforPostgreSQL/flexibleServers", "--query", "[:200].{name:name,location:location,resourceGroup:resourceGroup}", "--output", "table")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "mysql":
				result, err := exec("resource", "list", "--resource-type", "Microsoft.DBforMySQL/flexibleServers", "--query", "[:200].{name:name,location:location,resourceGroup:resourceGroup}", "--output", "table")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "redis", "cache":
				result, err := exec("resource", "list", "--resource-type", "Microsoft.Cache/Redis", "--query", "[:200].{name:name,location:location,resourceGroup:resourceGroup}", "--output", "table")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "ai-services", "cognitive-services", "azure-openai":
				result, err := exec("resource", "list", "--resource-type", "Microsoft.CognitiveServices/accounts", "--query", "[:200].{name:name,kind:kind,location:location,resourceGroup:resourceGroup}", "--output", "table")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "ai-search", "search", "cognitive-search":
				result, err := exec("resource", "list", "--resource-type", "Microsoft.Search/searchServices", "--query", "[:200].{name:name,location:location,resourceGroup:resourceGroup}", "--output", "table")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "servicebus", "service-bus":
				result, err := exec("servicebus", "namespace", "list", "--output", "table")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "eventhubs", "event-hubs":
				result, err := exec("eventhubs", "namespace", "list", "--output", "table")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "eventgrid", "event-grid":
				result, err := exec("eventgrid", "topic", "list", "--output", "table")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "apim", "api-management":
				result, err := exec("apim", "list", "--output", "table")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "log-analytics", "workspaces":
				result, err := exec("monitor", "log-analytics", "workspace", "list", "--output", "table")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "app-insights", "application-insights":
				result, err := exec("resource", "list", "--resource-type", "microsoft.insights/components", "--query", "[:200].{name:name,location:location,resourceGroup:resourceGroup}", "--output", "table")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "front-door", "frontdoor", "cdn":
				result, err := exec("resource", "list", "--resource-type", "Microsoft.Cdn/profiles", "--query", "[:200].{name:name,kind:kind,location:location,resourceGroup:resourceGroup}", "--output", "table")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "vnets", "vnet":
				result, err := exec("network", "vnet", "list", "--output", "table")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "nsgs", "nsg":
				result, err := exec("network", "nsg", "list", "--output", "table")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "public-ips", "publicips", "pip":
				result, err := exec("network", "public-ip", "list", "--output", "table")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "load-balancers", "lbs", "loadbalancers":
				result, err := exec("network", "lb", "list", "--output", "table")
				if err != nil {
					return err
				}
				fmt.Print(result)
			default:
				return fmt.Errorf("unsupported resource type: %s", resourceType)
			}

			return nil
		},
	}

	azureListCmd.Flags().String("subscription", "", "Azure subscription ID")
	azureCmd.AddCommand(azureListCmd)

	return azureCmd
}
