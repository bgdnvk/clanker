package sre

import (
	"context"
	"time"
)

func collectHetznerSignals(ctx context.Context) map[string]any {
	out := map[string]any{}

	// --- Servers ---
	if v, err := runCommandOutput(ctx, 4*time.Second,
		"hcloud", "server", "list", "-o", "json",
	); err == nil {
		out["servers"] = jsonParseList(v)
	}

	// --- Load balancers ---
	if v, err := runCommandOutput(ctx, 4*time.Second,
		"hcloud", "load-balancer", "list", "-o", "json",
	); err == nil {
		out["loadBalancers"] = jsonParseList(v)
	}

	// --- Volumes ---
	if v, err := runCommandOutput(ctx, 3*time.Second,
		"hcloud", "volume", "list", "-o", "json",
	); err == nil {
		out["volumes"] = jsonParseList(v)
	}

	// --- Networks ---
	if v, err := runCommandOutput(ctx, 3*time.Second,
		"hcloud", "network", "list", "-o", "json",
	); err == nil {
		out["networks"] = jsonParseList(v)
	}

	// --- Firewalls ---
	if v, err := runCommandOutput(ctx, 3*time.Second,
		"hcloud", "firewall", "list", "-o", "json",
	); err == nil {
		out["firewalls"] = jsonParseList(v)
	}

	// --- Floating IPs ---
	if v, err := runCommandOutput(ctx, 3*time.Second,
		"hcloud", "floating-ip", "list", "-o", "json",
	); err == nil {
		out["floatingIPs"] = jsonParseList(v)
	}

	// --- Snapshots (age awareness) ---
	if v, err := runCommandOutput(ctx, 3*time.Second,
		"hcloud", "image", "list", "--type", "snapshot", "-o", "json",
	); err == nil {
		out["snapshots"] = jsonParseList(v)
	}

	// --- Certificates ---
	if v, err := runCommandOutput(ctx, 3*time.Second,
		"hcloud", "certificate", "list", "-o", "json",
	); err == nil {
		out["certificates"] = jsonParseList(v)
	}

	return out
}

// collectK8sWarnings runs deep Kubernetes health collection beyond the basics.
// All kubectl calls use --all-namespaces so nothing is missed.
