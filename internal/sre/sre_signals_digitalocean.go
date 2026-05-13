package sre

import (
	"context"
	"time"
)

func collectDOSignals(ctx context.Context) map[string]any {
	out := map[string]any{}

	// --- Droplets ---
	if v, err := runCommandOutput(ctx, 5*time.Second,
		"doctl", "compute", "droplet", "list",
		"--format", "ID,Name,Status,Region,Size,PublicIPv4",
		"--output", "json",
	); err == nil {
		out["droplets"] = jsonParseList(v)
	}

	// --- Managed databases ---
	if v, err := runCommandOutput(ctx, 5*time.Second,
		"doctl", "databases", "list",
		"--format", "ID,Name,Engine,Status,Region",
		"--output", "json",
	); err == nil {
		out["databases"] = jsonParseList(v)
	}

	// --- Kubernetes clusters ---
	if v, err := runCommandOutput(ctx, 5*time.Second,
		"doctl", "kubernetes", "cluster", "list",
		"--format", "ID,Name,Status,Region",
		"--output", "json",
	); err == nil {
		out["kubernetesClusters"] = jsonParseList(v)
	}

	// --- Apps + recent deployment failures ---
	if v, err := runCommandOutput(ctx, 5*time.Second,
		"doctl", "apps", "list",
		"--format", "ID,Spec.Name,ActiveDeployment.Phase,InProgressDeployment.Phase",
		"--output", "json",
	); err == nil {
		out["apps"] = jsonParseList(v)
	}

	// --- Load balancers ---
	if v, err := runCommandOutput(ctx, 4*time.Second,
		"doctl", "compute", "load-balancer", "list",
		"--format", "ID,Name,Status,Region,IP",
		"--output", "json",
	); err == nil {
		out["loadBalancers"] = jsonParseList(v)
	}

	// --- Spaces buckets ---
	if v, err := runCommandOutput(ctx, 4*time.Second,
		"doctl", "compute", "domain", "list",
		"--format", "Domain",
		"--output", "json",
	); err == nil {
		out["domains"] = jsonParseList(v)
	}

	// --- Container Registry ---
	if v, err := runCommandOutput(ctx, 4*time.Second,
		"doctl", "registry", "get",
		"--output", "json",
	); err == nil {
		out["containerRegistry"] = splitLinesLimited(v, 20)
	}

	// --- Firewall rules ---
	if v, err := runCommandOutput(ctx, 4*time.Second,
		"doctl", "compute", "firewall", "list",
		"--format", "ID,Name,Status",
		"--output", "json",
	); err == nil {
		out["firewalls"] = jsonParseList(v)
	}

	// --- Floating IPs ---
	if v, err := runCommandOutput(ctx, 3*time.Second,
		"doctl", "compute", "floating-ip", "list",
		"--format", "IP,RegionSlug,DropletID",
		"--output", "json",
	); err == nil {
		out["floatingIPs"] = jsonParseList(v)
	}

	return out
}

// collectHetznerSignals queries Hetzner Cloud via hcloud CLI.
// Requires: HCLOUD_TOKEN env var or hcloud context set.
