package gcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// CreateGCPCommands creates the GCP command tree for static commands
func CreateGCPCommands() *cobra.Command {
	gcpCmd := &cobra.Command{
		Use:   "gcp",
		Short: "Query GCP infrastructure directly",
		Long:  "Query your GCP infrastructure without AI interpretation. Useful for getting raw data.",
	}

	gcpListCmd := &cobra.Command{
		Use:   "list [resource]",
		Short: "List GCP resources",
		Long: `List GCP resources of a specific type.

Supported resources:
  iam, service-accounts - IAM service accounts
  iam-roles             - IAM roles
  cloudrun, run         - Cloud Run services
  run-jobs              - Cloud Run jobs
	firestore             - Firestore databases
	firebase-apps         - Firebase apps
  compute, instances    - Compute Engine instances
  instance-groups       - Compute instance groups
  networks              - VPC networks
  subnets               - VPC subnets
  firewall              - Firewall rules
  load-balancers        - Forwarding rules (load balancers)
  armor                 - Cloud Armor security policies
  dns                   - Cloud DNS managed zones
  gke, clusters         - GKE clusters
  cloudsql, sql         - Cloud SQL instances
  bigquery              - BigQuery datasets
  spanner               - Spanner instances
  bigtable              - Bigtable instances
  redis                 - Memorystore (Redis)
  memcache              - Memorystore (Memcached)
  gcs, buckets          - Cloud Storage buckets
  artifacts             - Artifact Registry repositories
  functions             - Cloud Functions
	functions-gen2        - Cloud Functions (Gen2)
  pubsub, topics        - Pub/Sub topics
  subscriptions         - Pub/Sub subscriptions
  tasks                 - Cloud Tasks queues
  scheduler             - Cloud Scheduler jobs
  secrets               - Secret Manager secrets
	kms                   - Cloud KMS keyrings
  build-triggers        - Cloud Build triggers
  deploy-pipelines      - Cloud Deploy delivery pipelines
  logging-sinks         - Cloud Logging sinks
  alert-policies        - Cloud Monitoring alert policies
  api-gateway           - API Gateway APIs`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resourceType := strings.ToLower(args[0])
			projectID, _ := cmd.Flags().GetString("project")

			if projectID == "" {
				projectID = ResolveProjectID()
			}
			if projectID == "" {
				return fmt.Errorf("gcp project_id is required (set infra.gcp.project_id or use --project)")
			}

			debug := viper.GetBool("debug")
			client, err := NewClient(projectID, debug)
			if err != nil {
				return err
			}

			ctx := context.Background()
			exec := func(args ...string) (string, error) {
				return client.execGcloud(ctx, args...)
			}

			switch resourceType {
			case "iam", "service-accounts":
				result, err := exec("iam", "service-accounts", "list", "--format", "table(email,displayName,disabled)")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "iam-roles":
				result, err := exec("iam", "roles", "list", "--format", "table(name,title,stage)")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "cloudrun", "run":
				result, err := exec("run", "services", "list", "--platform", "managed", "--format", "table(name,region,url)")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "run-jobs":
				result, err := exec("run", "jobs", "list", "--platform", "managed", "--format", "table(name,region,createTime)")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "firestore", "datastore":
				result, err := exec("firestore", "databases", "list", "--format", "table(name,locationId,type)")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "firebase", "firebase-apps":
				result, err := exec("firebase", "apps", "list", "--format", "table(appId,displayName,platform)")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "compute", "instances":
				result, err := exec("compute", "instances", "list", "--format", "table(name,zone,status,networkInterfaces[0].networkIP,networkInterfaces[0].accessConfigs[0].natIP)")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "instance-groups":
				result, err := exec("compute", "instance-groups", "list", "--format", "table(name,zone,network)")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "networks":
				result, err := exec("compute", "networks", "list", "--format", "table(name,autoCreateSubnetworks,subnetMode)")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "subnets":
				result, err := exec("compute", "networks", "subnets", "list", "--format", "table(name,region,network,ipCidrRange)")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "firewall":
				result, err := exec("compute", "firewall-rules", "list", "--format", "table(name,network,direction,priority,allowed,sourceRanges)")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "load-balancers":
				result, err := exec("compute", "forwarding-rules", "list", "--format", "table(name,region,IPAddress,IPProtocol,portRange,target)")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "armor":
				result, err := exec("compute", "security-policies", "list", "--format", "table(name,description)")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "dns":
				result, err := exec("dns", "managed-zones", "list", "--format", "table(name,dnsName,visibility)")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "gke", "clusters":
				result, err := exec("container", "clusters", "list", "--format", "table(name,location,status,masterVersion)")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "cloudsql", "sql":
				result, err := exec("sql", "instances", "list", "--format", "table(name,region,databaseVersion,state)")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "bigquery":
				result, err := exec("bigquery", "datasets", "list", "--format", "table(id,location)")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "spanner":
				result, err := exec("spanner", "instances", "list", "--format", "table(name,config,displayName,state)")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "bigtable":
				result, err := exec("bigtable", "instances", "list", "--format", "table(name,displayName,state)")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "redis":
				result, err := exec("redis", "instances", "list", "--format", "table(name,region,tier,host,port)")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "memcache":
				result, err := exec("memcache", "instances", "list", "--format", "table(name,region,memcacheVersion)")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "gcs", "buckets":
				result, err := exec("storage", "buckets", "list", "--format", "table(name,location,storageClass)")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "artifacts":
				result, err := exec("artifacts", "repositories", "list", "--format", "table(name,format,location)")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "functions":
				result, err := exec("functions", "list", "--format", "table(name,region,status,trigger)")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "functions-gen2":
				result, err := exec("functions", "list", "--gen2", "--format", "table(name,region,state,trigger)")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "pubsub", "topics":
				result, err := exec("pubsub", "topics", "list", "--format", "table(name)")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "subscriptions":
				result, err := exec("pubsub", "subscriptions", "list", "--format", "table(name,topic,ackDeadlineSeconds)")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "tasks":
				result, err := exec("tasks", "queues", "list", "--format", "table(name,rateLimits.maxDispatchesPerSecond)")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "scheduler":
				result, err := exec("scheduler", "jobs", "list", "--format", "table(name,schedule,timezone)")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "secrets":
				result, err := exec("secrets", "list", "--format", "table(name,createTime,labels)")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "kms", "keyrings":
				result, err := exec("kms", "keyrings", "list", "--location", "global", "--format", "table(name,locationId)")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "build-triggers":
				result, err := exec("builds", "triggers", "list", "--format", "table(name,description,createTime)")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "deploy-pipelines":
				result, err := exec("deploy", "delivery-pipelines", "list", "--format", "table(name,region,createTime)")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "logging-sinks":
				result, err := exec("logging", "sinks", "list", "--format", "table(name,destination,filter)")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "alert-policies":
				result, err := exec("monitoring", "alert-policies", "list", "--format", "table(name,displayName,enabled)")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "api-gateway":
				result, err := exec("api-gateway", "apis", "list", "--format", "table(name,displayName,createTime)")
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

	gcpListCmd.Flags().StringP("project", "p", "", "GCP project ID")
	gcpCmd.AddCommand(gcpListCmd)

	return gcpCmd
}
