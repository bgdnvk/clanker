package gcp

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/viper"
)

type Client struct {
	projectID string
	debug     bool
}

func ResolveProjectID() string {
	if projectID := strings.TrimSpace(viper.GetString("infra.gcp.project_id")); projectID != "" {
		return projectID
	}
	if env := strings.TrimSpace(os.Getenv("GCP_PROJECT")); env != "" {
		return env
	}
	if env := strings.TrimSpace(os.Getenv("GOOGLE_CLOUD_PROJECT")); env != "" {
		return env
	}
	if env := strings.TrimSpace(os.Getenv("GCLOUD_PROJECT")); env != "" {
		return env
	}
	return ""
}

func NewClient(projectID string, debug bool) (*Client, error) {
	if strings.TrimSpace(projectID) == "" {
		return nil, fmt.Errorf("gcp project_id is required")
	}

	return &Client{projectID: projectID, debug: debug}, nil
}

func (c *Client) execGcloud(ctx context.Context, args ...string) (string, error) {
	if _, err := exec.LookPath("gcloud"); err != nil {
		return "", fmt.Errorf("gcloud not found in PATH")
	}

	args = append(args, "--project", c.projectID)

	backoffs := []time.Duration{200 * time.Millisecond, 500 * time.Millisecond, 1200 * time.Millisecond}
	var lastErr error
	var lastStderr string

	for attempt := 0; attempt < len(backoffs); attempt++ {
		cmd := exec.CommandContext(ctx, "gcloud", args...)

		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		err := cmd.Run()
		if err == nil {
			return stdout.String(), nil
		}

		lastErr = err
		lastStderr = strings.TrimSpace(stderr.String())

		if ctx.Err() != nil {
			break
		}

		if !isRetryableGcloudError(lastStderr) {
			break
		}

		time.Sleep(backoffs[attempt])
	}

	if lastErr == nil {
		return "", fmt.Errorf("gcloud command failed")
	}

	return "", fmt.Errorf("gcloud command failed: %w, stderr: %s%s", lastErr, lastStderr, gcloudErrorHint(lastStderr))
}

func isRetryableGcloudError(stderr string) bool {
	lower := strings.ToLower(stderr)
	if strings.Contains(lower, "rate") && strings.Contains(lower, "limit") {
		return true
	}
	if strings.Contains(lower, "resource_exhausted") {
		return true
	}
	if strings.Contains(lower, "deadline exceeded") || strings.Contains(lower, "timeout") {
		return true
	}
	if strings.Contains(lower, "temporarily unavailable") || strings.Contains(lower, "internal error") {
		return true
	}
	return false
}

func gcloudErrorHint(stderr string) string {
	lower := strings.ToLower(stderr)
	switch {
	case strings.Contains(lower, "permission") || strings.Contains(lower, "denied"):
		return " (hint: missing IAM permissions or project access)"
	case strings.Contains(lower, "not found") && strings.Contains(lower, "project"):
		return " (hint: project_id may be incorrect)"
	case strings.Contains(lower, "api") && strings.Contains(lower, "not enabled"):
		return " (hint: enable the API for this service)"
	case strings.Contains(lower, "login") || strings.Contains(lower, "auth"):
		return " (hint: gcloud auth or ADC may be missing)"
	case strings.Contains(lower, "permission") || strings.Contains(lower, "insufficient"):
		return " (hint: missing role bindings for the API)"
	case strings.Contains(lower, "endpoint") && strings.Contains(lower, "not found"):
		return " (hint: service may not be available in this region)"
	default:
		return ""
	}
}

func (c *Client) GetRelevantContext(ctx context.Context, question string) (string, error) {
	questionLower := strings.ToLower(strings.TrimSpace(question))

	type section struct {
		name string
		args []string
		keys []string
	}

	sections := []section{
		{name: "IAM Service Accounts", args: []string{"iam", "service-accounts", "list", "--format", "table(email,displayName,disabled)"}, keys: []string{"iam service account", "service account", "service accounts"}},
		{name: "IAM Roles", args: []string{"iam", "roles", "list", "--format", "table(name,title,stage)"}, keys: []string{"iam role", "iam roles"}},
		{name: "Cloud Run Services", args: []string{"run", "services", "list", "--platform", "managed", "--format", "table(name,region,url)"}, keys: []string{"cloud run", "cloudrun", "run service", "run services"}},
		{name: "Cloud Run Jobs", args: []string{"run", "jobs", "list", "--platform", "managed", "--format", "table(name,region,createTime)"}, keys: []string{"cloud run job", "run job", "run jobs"}},
		{name: "Firestore Databases", args: []string{"firestore", "databases", "list", "--format", "table(name,locationId,type)"}, keys: []string{"firestore", "datastore"}},
		{name: "Firebase Apps", args: []string{"firebase", "apps", "list", "--format", "table(appId,displayName,platform)"}, keys: []string{"firebase"}},
		{name: "Compute Instances", args: []string{"compute", "instances", "list", "--format", "table(name,zone,status,networkInterfaces[0].networkIP,networkInterfaces[0].accessConfigs[0].natIP)"}, keys: []string{"compute engine", "gce"}},
		{name: "Instance Groups", args: []string{"compute", "instance-groups", "list", "--format", "table(name,zone,network)"}, keys: []string{"instance group", "instance groups", "mig"}},
		{name: "VPC Networks", args: []string{"compute", "networks", "list", "--format", "table(name,autoCreateSubnetworks,subnetMode)"}, keys: []string{"gcp vpc", "gcp network", "vpc network"}},
		{name: "Subnets", args: []string{"compute", "networks", "subnets", "list", "--format", "table(name,region,network,ipCidrRange)"}, keys: []string{"gcp subnet", "gcp subnets"}},
		{name: "Firewall Rules", args: []string{"compute", "firewall-rules", "list", "--format", "table(name,network,direction,priority,allowed,sourceRanges)"}, keys: []string{"gcp firewall", "cloud firewall"}},
		{name: "Load Balancers", args: []string{"compute", "forwarding-rules", "list", "--format", "table(name,region,IPAddress,IPProtocol,portRange,target)"}, keys: []string{"cloud load balancing", "gcp load balancer"}},
		{name: "Cloud Armor Policies", args: []string{"compute", "security-policies", "list", "--format", "table(name,description)"}, keys: []string{"cloud armor", "gcp armor"}},
		{name: "Cloud DNS Zones", args: []string{"dns", "managed-zones", "list", "--format", "table(name,dnsName,visibility)"}, keys: []string{"cloud dns", "gcp dns"}},
		{name: "GKE Clusters", args: []string{"container", "clusters", "list", "--format", "table(name,location,status,masterVersion)"}, keys: []string{"gke", "kubernetes engine"}},
		{name: "Cloud SQL Instances", args: []string{"sql", "instances", "list", "--format", "table(name,region,databaseVersion,state)"}, keys: []string{"cloud sql", "cloudsql"}},
		{name: "BigQuery Datasets", args: []string{"bigquery", "datasets", "list", "--format", "table(id,location)"}, keys: []string{"bigquery"}},
		{name: "Cloud Spanner Instances", args: []string{"spanner", "instances", "list", "--format", "table(name,config,displayName,state)"}, keys: []string{"spanner"}},
		{name: "Bigtable Instances", args: []string{"bigtable", "instances", "list", "--format", "table(name,displayName,state)"}, keys: []string{"bigtable"}},
		{name: "Memorystore Redis", args: []string{"redis", "instances", "list", "--format", "table(name,region,tier,host,port)"}, keys: []string{"memorystore", "redis"}},
		{name: "Memorystore Memcached", args: []string{"memcache", "instances", "list", "--format", "table(name,region,memcacheVersion)"}, keys: []string{"memcache"}},
		{name: "Cloud Storage Buckets", args: []string{"storage", "buckets", "list", "--format", "table(name,location,storageClass)"}, keys: []string{"gcs", "cloud storage", "storage bucket"}},
		{name: "Artifact Registry Repos", args: []string{"artifacts", "repositories", "list", "--format", "table(name,format,location)"}, keys: []string{"artifact registry", "gar"}},
		{name: "Cloud Functions", args: []string{"functions", "list", "--format", "table(name,region,status,trigger)"}, keys: []string{"cloud functions", "cloud function"}},
		{name: "Cloud Functions Gen2", args: []string{"functions", "list", "--gen2", "--format", "table(name,region,state,trigger)"}, keys: []string{"functions gen2", "cloud functions gen2", "cloud functions v2"}},
		{name: "Pub/Sub Topics", args: []string{"pubsub", "topics", "list", "--format", "table(name)"}, keys: []string{"pubsub", "pub/sub"}},
		{name: "Pub/Sub Subscriptions", args: []string{"pubsub", "subscriptions", "list", "--format", "table(name,topic,ackDeadlineSeconds)"}, keys: []string{"pubsub subscription", "pub/sub subscription"}},
		{name: "Cloud Tasks Queues", args: []string{"tasks", "queues", "list", "--format", "table(name,rateLimits.maxDispatchesPerSecond)"}, keys: []string{"cloud tasks", "tasks queue"}},
		{name: "Cloud Scheduler Jobs", args: []string{"scheduler", "jobs", "list", "--format", "table(name,schedule,timezone)"}, keys: []string{"cloud scheduler", "scheduler job"}},
		{name: "Secret Manager Secrets", args: []string{"secrets", "list", "--format", "table(name,createTime,labels)"}, keys: []string{"secret manager", "secrets"}},
		{name: "Cloud KMS Keyrings", args: []string{"kms", "keyrings", "list", "--location", "global", "--format", "table(name,locationId)"}, keys: []string{"kms", "keyring", "keyrings", "key management"}},
		{name: "Cloud Build Triggers", args: []string{"builds", "triggers", "list", "--format", "table(name,description,createTime)"}, keys: []string{"cloud build", "build trigger", "build triggers"}},
		{name: "Cloud Deploy Pipelines", args: []string{"deploy", "delivery-pipelines", "list", "--format", "table(name,region,createTime)"}, keys: []string{"cloud deploy", "deploy pipeline"}},
		{name: "Logging Sinks", args: []string{"logging", "sinks", "list", "--format", "table(name,destination,filter)"}, keys: []string{"cloud logging", "logging sink"}},
		{name: "Monitoring Alert Policies", args: []string{"monitoring", "alert-policies", "list", "--format", "table(name,displayName,enabled)"}, keys: []string{"cloud monitoring", "alert policy", "alerts"}},
		{name: "API Gateway APIs", args: []string{"api-gateway", "apis", "list", "--format", "table(name,displayName,createTime)"}, keys: []string{"api gateway", "apigateway"}},
	}

	defaultSections := map[string]bool{
		"IAM Service Accounts":   true,
		"Firestore Databases":    true,
		"Cloud Run Services":     true,
		"Compute Instances":      true,
		"GKE Clusters":           true,
		"Cloud SQL Instances":    true,
		"Cloud Storage Buckets":  true,
		"Cloud Functions":        true,
		"Pub/Sub Topics":         true,
		"Secret Manager Secrets": true,
	}

	var out strings.Builder
	var warnings []string
	for _, s := range sections {
		if questionLower != "" && len(s.keys) > 0 {
			matched := false
			for _, key := range s.keys {
				if strings.Contains(questionLower, key) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		result, err := c.execGcloud(ctx, s.args...)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", s.name, err))
			continue
		}
		if strings.TrimSpace(result) == "" {
			continue
		}
		out.WriteString(s.name)
		out.WriteString(":\n")
		out.WriteString(result)
		out.WriteString("\n")
	}

	if strings.TrimSpace(out.String()) == "" {
		for _, s := range sections {
			if !defaultSections[s.name] {
				continue
			}
			result, err := c.execGcloud(ctx, s.args...)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("%s: %v", s.name, err))
				continue
			}
			if strings.TrimSpace(result) == "" {
				continue
			}
			out.WriteString(s.name)
			out.WriteString(":\n")
			out.WriteString(result)
			out.WriteString("\n")
		}
	}

	if len(warnings) > 0 {
		out.WriteString("GCP Warnings:\n")
		for i, warn := range warnings {
			if i >= 8 {
				out.WriteString("- (additional warnings omitted)\n")
				break
			}
			out.WriteString("- ")
			out.WriteString(warn)
			out.WriteString("\n")
		}
		out.WriteString("\n")
	}

	if strings.TrimSpace(out.String()) == "" {
		return "No GCP data available (missing permissions or project has no resources).", nil
	}

	return out.String(), nil
}
