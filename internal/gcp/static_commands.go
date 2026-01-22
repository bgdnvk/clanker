package gcp

import (
	"context"
	"fmt"
	"os"
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
	lb-forwarding-rules    - Forwarding rules (global + regional)
	lb-target-proxies      - Target HTTP/HTTPS proxies
	lb-url-maps            - URL maps
	lb-backend-services    - Backend services
	lb-health-checks       - Health checks
	lb-ssl-certs           - SSL certificates
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
	secret-versions        - Secret Manager secret versions (last 5)
	kms                   - Cloud KMS keyrings
  build-triggers        - Cloud Build triggers
  deploy-pipelines      - Cloud Deploy delivery pipelines
  logging-sinks         - Cloud Logging sinks
  alert-policies        - Cloud Monitoring alert policies
	artifact-packages      - Artifact Registry packages (per repo, limited)
	artifact-images        - Artifact Registry docker images (per repo, limited)
	dns-record-sets        - Cloud DNS record sets (per zone)
	eventarc-triggers      - Eventarc triggers (multi-region)
	api-gateway           - API Gateway APIs`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resourceType := strings.ToLower(args[0])
			projectID, _ := cmd.Flags().GetString("project")
			location, _ := cmd.Flags().GetString("location")

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

			commonRegions := []string{
				"us-central1", "us-east1", "us-east4", "us-west1", "us-west2",
				"europe-west1", "europe-west2", "asia-east1", "asia-northeast1",
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
			case "lb-forwarding-rules":
				result, err := exec("compute", "forwarding-rules", "list", "--format", "table(name,region,IPAddress,IPProtocol,portRange,target)")
				if err != nil {
					return err
				}
				fmt.Print(result)
				result, err = exec("compute", "forwarding-rules", "list", "--global", "--format", "table(name,IPAddress,IPProtocol,portRange,target)")
				if err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to list global forwarding rules: %v\n", err)
					return nil
				}
				fmt.Print(result)
			case "lb-target-proxies":
				fmt.Println("Target HTTP Proxies:")
				result, err := exec("compute", "target-http-proxies", "list", "--format", "table(name,urlMap)")
				if err != nil {
					return err
				}
				fmt.Print(result)
				fmt.Println("\nTarget HTTPS Proxies:")
				result, err = exec("compute", "target-https-proxies", "list", "--format", "table(name,urlMap,sslCertificates.list())")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "lb-url-maps":
				result, err := exec("compute", "url-maps", "list", "--format", "table(name,defaultService)")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "lb-backend-services":
				result, err := exec("compute", "backend-services", "list", "--format", "table(name,protocol,healthChecks.list())")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "lb-health-checks":
				result, err := exec("compute", "health-checks", "list", "--format", "table(name,type,checkIntervalSec,timeoutSec)")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "lb-ssl-certs":
				result, err := exec("compute", "ssl-certificates", "list", "--format", "table(name,type,expireTime)")
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
			case "secret-versions":
				secretsList, err := exec("secrets", "list", "--format", "value(name)")
				if err != nil {
					return err
				}
				secrets := strings.Fields(secretsList)
				if len(secrets) == 0 {
					fmt.Println("(no secrets)")
					return nil
				}
				for _, sec := range secrets {
					sec = strings.TrimSpace(sec)
					if sec == "" {
						continue
					}
					fmt.Printf("Secret: %s\n", sec)
					result, err := exec("secrets", "versions", "list", sec, "--limit", "5", "--format", "table(name,state,createTime)")
					if err != nil {
						fmt.Fprintf(os.Stderr, "warning: failed to list versions for %s: %v\n", sec, err)
						continue
					}
					fmt.Print(result)
					fmt.Println()
				}
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
			case "dns-record-sets":
				zonesList, err := exec("dns", "managed-zones", "list", "--format", "value(name)")
				if err != nil {
					return err
				}
				zones := strings.Fields(zonesList)
				if len(zones) == 0 {
					fmt.Println("(no DNS zones)")
					return nil
				}
				for _, zone := range zones {
					zone = strings.TrimSpace(zone)
					if zone == "" {
						continue
					}
					fmt.Printf("Zone: %s\n", zone)
					result, err := exec("dns", "record-sets", "list", "--zone", zone, "--format", "table(name,type,ttl,rrdatas[0])")
					if err != nil {
						fmt.Fprintf(os.Stderr, "warning: failed to list record-sets for zone %s: %v\n", zone, err)
						continue
					}
					fmt.Print(result)
					fmt.Println()
				}
			case "eventarc-triggers", "eventarc", "triggers":
				regions := commonRegions
				if strings.TrimSpace(location) != "" {
					regions = []string{strings.TrimSpace(location)}
				}
				var any bool
				var firstErr error
				for _, r := range regions {
					result, err := exec("eventarc", "triggers", "list", "--location", r, "--format", "table(name,location,destination.cloudRun.service,transport.pubsub.topic,serviceAccount)")
					if err != nil {
						if firstErr == nil {
							firstErr = err
						}
						fmt.Fprintf(os.Stderr, "warning: failed to list triggers in %s: %v\n", r, err)
						continue
					}
					if strings.TrimSpace(result) == "" {
						continue
					}
					fmt.Printf("Location: %s\n", r)
					fmt.Print(result)
					fmt.Println()
					any = true
				}
				if !any && firstErr != nil {
					return firstErr
				}
			case "artifact-packages":
				reposList, err := exec("artifacts", "repositories", "list", "--format", "value(name)")
				if err != nil {
					return err
				}
				repos := strings.Fields(reposList)
				if len(repos) == 0 {
					fmt.Println("(no Artifact Registry repositories)")
					return nil
				}
				for _, full := range repos {
					full = strings.TrimSpace(full)
					if full == "" {
						continue
					}
					loc := ""
					repo := ""
					parts := strings.Split(strings.Trim(full, "/"), "/")
					for i := 0; i+1 < len(parts); i++ {
						if parts[i] == "locations" {
							loc = parts[i+1]
						}
						if parts[i] == "repositories" {
							repo = parts[i+1]
						}
					}
					if loc == "" || repo == "" {
						continue
					}
					fmt.Printf("Repo: %s (%s)\n", repo, loc)
					result, err := exec("artifacts", "packages", "list", "--repository", repo, "--location", loc, "--limit", "20", "--format", "table(name,createTime,updateTime)")
					if err != nil {
						fmt.Fprintf(os.Stderr, "warning: failed to list packages for %s/%s: %v\n", loc, repo, err)
						continue
					}
					fmt.Print(result)
					fmt.Println()
				}
			case "artifact-images":
				reposList, err := exec("artifacts", "repositories", "list", "--format", "value(name,format,location)")
				if err != nil {
					return err
				}
				lines := strings.Split(reposList, "\n")
				var any bool
				for _, line := range lines {
					line = strings.TrimSpace(line)
					if line == "" {
						continue
					}
					parts := strings.Fields(line)
					if len(parts) < 3 {
						continue
					}
					full := parts[0]
					format := strings.ToUpper(strings.TrimSpace(parts[1]))
					loc := strings.TrimSpace(parts[2])
					if format != "DOCKER" {
						continue
					}
					repo := ""
					p := strings.Split(strings.Trim(full, "/"), "/")
					for i := 0; i+1 < len(p); i++ {
						if p[i] == "repositories" {
							repo = p[i+1]
						}
					}
					if repo == "" || loc == "" {
						continue
					}
					fmt.Printf("Repo: %s (%s)\n", repo, loc)
					imagePath := fmt.Sprintf("%s-docker.pkg.dev/%s/%s", loc, projectID, repo)
					result, err := exec("artifacts", "docker", "images", "list", imagePath, "--include-tags", "--limit", "50")
					if err != nil {
						fmt.Fprintf(os.Stderr, "warning: failed to list docker images for %s: %v\n", imagePath, err)
						continue
					}
					fmt.Print(result)
					fmt.Println()
					any = true
				}
				if !any {
					fmt.Println("(no DOCKER repositories)")
				}
			default:
				return fmt.Errorf("unsupported resource type: %s", resourceType)
			}

			return nil
		},
	}

	gcpListCmd.Flags().StringP("project", "p", "", "GCP project ID")
	gcpListCmd.Flags().String("location", "", "GCP location/region for region-scoped resources")
	gcpCmd.AddCommand(gcpListCmd)

	return gcpCmd
}
