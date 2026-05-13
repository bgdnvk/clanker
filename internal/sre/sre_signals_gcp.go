package sre

import (
	"context"
	"time"
)

func collectGCPSignals(ctx context.Context) map[string]any {
	out := map[string]any{}

	// --- Cloud Run services ---
	if v, err := runCommandOutput(ctx, 5*time.Second,
		"gcloud", "run", "services", "list",
		"--format=json(metadata.name,status.conditions,status.url)",
		"--quiet",
	); err == nil {
		out["cloudRunServices"] = jsonParseList(v)
	}

	// --- Cloud Functions ---
	if v, err := runCommandOutput(ctx, 5*time.Second,
		"gcloud", "functions", "list",
		"--format=json(name,status,runtime,updateTime)",
		"--quiet",
	); err == nil {
		out["cloudFunctions"] = jsonParseList(v)
	}

	// --- GKE clusters ---
	if v, err := runCommandOutput(ctx, 5*time.Second,
		"gcloud", "container", "clusters", "list",
		"--format=json(name,status,currentNodeCount,location)",
		"--quiet",
	); err == nil {
		out["gkeClusters"] = jsonParseList(v)
	}

	// --- Cloud SQL instances ---
	if v, err := runCommandOutput(ctx, 5*time.Second,
		"gcloud", "sql", "instances", "list",
		"--format=json(name,databaseVersion,state,settings.tier)",
		"--quiet",
	); err == nil {
		out["cloudSQLInstances"] = jsonParseList(v)
	}

	// --- Memorystore (Redis) ---
	if v, err := runCommandOutput(ctx, 5*time.Second,
		"gcloud", "redis", "instances", "list",
		"--format=json(name,state,tier,memorySizeGb)",
		"--quiet",
	); err == nil {
		out["memorystoreInstances"] = jsonParseList(v)
	}

	// --- GCS bucket count ---
	if v, err := runCommandOutput(ctx, 4*time.Second,
		"gcloud", "storage", "buckets", "list",
		"--format=json(name,location,storageClass)",
		"--quiet",
	); err == nil {
		out["gcsBuckets"] = jsonParseList(v)
	}

	// --- BigQuery recent failed jobs ---
	if v, err := runCommandOutput(ctx, 5*time.Second,
		"gcloud", "alpha", "bq", "jobs", "list",
		"--filter=status.state=DONE AND status.errorResult!=null",
		"--format=json(id,status.state,status.errorResult.message,statistics.creationTime)",
		"--limit=20",
		"--quiet",
	); err == nil {
		out["bigQueryFailedJobs"] = jsonParseList(v)
	}

	// --- Pub/Sub topics ---
	if v, err := runCommandOutput(ctx, 4*time.Second,
		"gcloud", "pubsub", "topics", "list",
		"--format=json(name)",
		"--quiet",
	); err == nil {
		out["pubSubTopics"] = jsonParseList(v)
	}
	if v, err := runCommandOutput(ctx, 4*time.Second,
		"gcloud", "pubsub", "subscriptions", "list",
		"--format=json(name,topic,deadLetterPolicy,pushConfig)",
		"--quiet",
	); err == nil {
		out["pubSubSubscriptions"] = jsonParseList(v)
	}

	// --- Compute instances ---
	if v, err := runCommandOutput(ctx, 5*time.Second,
		"gcloud", "compute", "instances", "list",
		"--format=json(name,status,machineType,zone)",
		"--quiet",
	); err == nil {
		out["computeInstances"] = jsonParseList(v)
	}

	// --- Artifact Registry repositories ---
	if v, err := runCommandOutput(ctx, 4*time.Second,
		"gcloud", "artifacts", "repositories", "list",
		"--format=json(name,format,createTime)",
		"--quiet",
	); err == nil {
		out["artifactRegistries"] = jsonParseList(v)
	}

	// --- Cloud Armor security policies ---
	if v, err := runCommandOutput(ctx, 4*time.Second,
		"gcloud", "compute", "security-policies", "list",
		"--format=json(name,description)",
		"--quiet",
	); err == nil {
		out["cloudArmorPolicies"] = jsonParseList(v)
	}

	// --- Error Reporting: recent critical events ---
	if v, err := runCommandOutput(ctx, 5*time.Second,
		"gcloud", "beta", "error-reporting", "events", "list",
		"--format=json(message,eventTime,serviceContext.service)",
		"--limit=30",
		"--quiet",
	); err == nil {
		out["errorReportingEvents"] = jsonParseList(v)
	}

	// --- Logging: recent ERROR/CRITICAL entries (last 5m) ---
	filter := "severity>=ERROR timestamp>=\"" + utcMinus(5*time.Minute) + "\""
	if v, err := runCommandOutput(ctx, 5*time.Second,
		"gcloud", "logging", "read", filter,
		"--format=json(timestamp,severity,resource.type,textPayload,jsonPayload.message)",
		"--limit=40",
		"--quiet",
	); err == nil {
		out["recentErrorLogs"] = jsonParseList(v)
	}

	// --- Certificate Manager: certs expiring within 30 days ---
	if v, err := runCommandOutput(ctx, 4*time.Second,
		"gcloud", "certificate-manager", "certificates", "list",
		"--format=json(name,managed.state,managed.expireTime)",
		"--quiet",
	); err == nil {
		out["certificates"] = jsonParseList(v)
	}

	return out
}

// collectAzureSignals queries Azure resources via az CLI.
// Requires: az login or AZURE_CLIENT_ID/SECRET/TENANT_ID env vars.
// Roles needed: Reader on subscription, plus Monitor Reader.
