package sre

import (
	"context"
	"time"
)

func collectAzureSignals(ctx context.Context) map[string]any {
	out := map[string]any{}

	// --- Web Apps ---
	if v, err := runCommandOutput(ctx, 5*time.Second,
		"az", "webapp", "list",
		"--query", "[*].{name:name,state:state,defaultHostName:defaultHostName,sku:sku.name}",
		"-o", "json",
	); err == nil {
		out["webApps"] = jsonParseList(v)
	}

	// --- Function Apps ---
	if v, err := runCommandOutput(ctx, 5*time.Second,
		"az", "functionapp", "list",
		"--query", "[*].{name:name,state:state,kind:kind}",
		"-o", "json",
	); err == nil {
		out["functionApps"] = jsonParseList(v)
	}

	// --- App Service Plans ---
	if v, err := runCommandOutput(ctx, 4*time.Second,
		"az", "appservice", "plan", "list",
		"--query", "[*].{name:name,status:status,sku:sku.name,workerCount:numberOfSites}",
		"-o", "json",
	); err == nil {
		out["appServicePlans"] = jsonParseList(v)
	}

	// --- AKS clusters ---
	if v, err := runCommandOutput(ctx, 5*time.Second,
		"az", "aks", "list",
		"--query", "[*].{name:name,powerState:powerState.code,provisioningState:provisioningState,nodeCount:agentPoolProfiles[0].count}",
		"-o", "json",
	); err == nil {
		out["aksClusters"] = jsonParseList(v)
	}

	// --- SQL servers + databases ---
	if v, err := runCommandOutput(ctx, 5*time.Second,
		"az", "sql", "server", "list",
		"--query", "[*].{name:name,state:state,fullyQualifiedDomainName:fullyQualifiedDomainName}",
		"-o", "json",
	); err == nil {
		out["sqlServers"] = jsonParseList(v)
	}
	if v, err := runCommandOutput(ctx, 5*time.Second,
		"az", "sql", "db", "list", "--server", ".", "--resource-group", ".",
		"--query", "[*].{name:name,status:status,edition:edition}",
		"-o", "json",
	); err == nil {
		out["sqlDatabases"] = jsonParseList(v)
	}

	// --- CosmosDB accounts ---
	if v, err := runCommandOutput(ctx, 5*time.Second,
		"az", "cosmosdb", "list",
		"--query", "[*].{name:name,kind:kind,documentEndpoint:documentEndpoint}",
		"-o", "json",
	); err == nil {
		out["cosmosDBAccounts"] = jsonParseList(v)
	}

	// --- Redis Cache ---
	if v, err := runCommandOutput(ctx, 4*time.Second,
		"az", "redis", "list",
		"--query", "[*].{name:name,provisioningState:provisioningState,sku:sku.name,capacity:sku.capacity}",
		"-o", "json",
	); err == nil {
		out["redisCaches"] = jsonParseList(v)
	}

	// --- Service Bus namespaces ---
	if v, err := runCommandOutput(ctx, 4*time.Second,
		"az", "servicebus", "namespace", "list",
		"--query", "[*].{name:name,status:status,sku:sku.name}",
		"-o", "json",
	); err == nil {
		out["serviceBusNamespaces"] = jsonParseList(v)
	}

	// --- Event Hubs namespaces ---
	if v, err := runCommandOutput(ctx, 4*time.Second,
		"az", "eventhubs", "namespace", "list",
		"--query", "[*].{name:name,provisioningState:provisioningState,sku:sku.name}",
		"-o", "json",
	); err == nil {
		out["eventHubsNamespaces"] = jsonParseList(v)
	}

	// --- Storage accounts ---
	if v, err := runCommandOutput(ctx, 4*time.Second,
		"az", "storage", "account", "list",
		"--query", "[*].{name:name,kind:kind,location:location}",
		"-o", "json",
	); err == nil {
		out["storageAccounts"] = jsonParseList(v)
	}

	// --- Azure Monitor alerts in Fired state ---
	if v, err := runCommandOutput(ctx, 5*time.Second,
		"az", "monitor", "alert", "list",
		"--query", "[?alertState=='Fired'].{name:name,alertState:alertState,severity:severity,firedTime:firedDateTime}",
		"-o", "json",
	); err == nil {
		out["monitorFiredAlerts"] = jsonParseList(v)
	}

	// --- Key Vault secrets + certs expiring ---
	if v, err := runCommandOutput(ctx, 4*time.Second,
		"az", "keyvault", "list",
		"--query", "[*].{name:name,resourceGroup:resourceGroup}",
		"-o", "json",
	); err == nil {
		out["keyVaults"] = jsonParseList(v)
	}

	// --- Container Registry ---
	if v, err := runCommandOutput(ctx, 4*time.Second,
		"az", "acr", "list",
		"--query", "[*].{name:name,loginServer:loginServer,sku:sku.name}",
		"-o", "json",
	); err == nil {
		out["containerRegistries"] = jsonParseList(v)
	}

	// --- Container Instances ---
	if v, err := runCommandOutput(ctx, 4*time.Second,
		"az", "container", "list",
		"--query", "[*].{name:name,provisioningState:provisioningState,instanceView:instanceView.state}",
		"-o", "json",
	); err == nil {
		out["containerInstances"] = jsonParseList(v)
	}

	// --- Activity log: failed ops last 1h ---
	if v, err := runCommandOutput(ctx, 5*time.Second,
		"az", "monitor", "activity-log", "list",
		"--status", "Failed",
		"--start-time", utcMinus(time.Hour),
		"--query", "[*].{time:eventTimestamp,op:operationName.value,caller:caller,resource:resourceId}",
		"-o", "json",
	); err == nil {
		out["activityLogFailures"] = jsonParseList(v)
	}

	// --- VMs ---
	if v, err := runCommandOutput(ctx, 5*time.Second,
		"az", "vm", "list",
		"--query", "[*].{name:name,powerState:powerState,location:location,size:hardwareProfile.vmSize}",
		"-o", "json",
	); err == nil {
		out["virtualMachines"] = jsonParseList(v)
	}

	return out
}

// collectDOSignals queries DigitalOcean via doctl CLI.
// Requires: DIGITALOCEAN_ACCESS_TOKEN or doctl auth init.
