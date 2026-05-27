package tencent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// listAsJSON renders the `clanker tencent list <type> --format json`
// output. It reuses the existing JSON* methods on *Client (the same
// ones the HTTP API surfaces in clanker-cloud) so the wire format is
// shared between CLI and HTTP consumers.
//
// Output shape:
//
//	Single region (default):
//	  <json-array-or-empty-string-from-JSONX>
//
//	With --all-regions:
//	  {"regions": [{"region": "<code>", "data": <json-from-JSONX>}, ...]}
//
// The single-region shape preserves backward compatibility with
// anything that already consumes `JSONCVMs` etc. directly (HTTP API).
// The multi-region envelope is new — explicit so downstream tools can
// trivially filter per region.
func listAsJSON(ctx context.Context, client *Client, resourceType string, regions []string, allRegions bool) error {
	// Single-region fast path: most resource types only make sense in
	// one region anyway, and the HTTP API shape matches this exactly.
	if !allRegions || len(regions) <= 1 {
		region := client.Region()
		if len(regions) == 1 {
			region = regions[0]
		}
		scoped := client.WithRegion(region)
		body, err := emitTypedJSON(ctx, scoped, resourceType)
		if err != nil {
			return err
		}
		fmt.Println(body)
		return nil
	}

	// Multi-region fan-out: walk every region serially. Errors are
	// captured per-region so a single regional outage doesn't poison
	// the whole sweep — callers see {"errors":[{"region":..., "error":...}]}.
	type regionData struct {
		Region string          `json:"region"`
		Data   json.RawMessage `json:"data"`
	}
	type regionErr struct {
		Region string `json:"region"`
		Error  string `json:"error"`
	}
	var entries []regionData
	var errors []regionErr
	for _, r := range regions {
		scoped := client.WithRegion(r)
		body, err := emitTypedJSON(ctx, scoped, resourceType)
		if err != nil {
			errors = append(errors, regionErr{Region: r, Error: err.Error()})
			continue
		}
		// Empty body → empty array so the consumer doesn't need a
		// special case for "no resources here."
		if strings.TrimSpace(body) == "" {
			body = "[]"
		}
		entries = append(entries, regionData{Region: r, Data: json.RawMessage(body)})
	}

	envelope := map[string]interface{}{
		"regions": entries,
	}
	if len(errors) > 0 {
		envelope["errors"] = errors
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(envelope)
}

// emitTypedJSON dispatches the resource-type string to the matching
// JSON method on Client. Returns the raw JSON string (which may be an
// empty string when the SDK returned no resources for the type).
//
// Service-global types (cos, ssl, cam, cdn, edgeone, waf, antiddos,
// ccn, cloudaudit) ignore the client's region; they're listed here for
// completeness so the dispatch is exhaustive against the table-mode
// switch above.
func emitTypedJSON(ctx context.Context, client *Client, resourceType string) (string, error) {
	switch resourceType {
	case "cvm", "instance", "instances", "vm", "vms":
		return client.JSONCVMs(ctx)
	case "vpc", "vpcs":
		return client.JSONVPCs(ctx)
	case "sg", "sgs", "security-group", "security-groups":
		return client.JSONSecurityGroups(ctx)
	case "mysql", "cdb":
		return client.JSONMySQL(ctx)
	case "postgres", "postgresql", "pg":
		return client.JSONPostgres(ctx)
	case "cos", "bucket", "buckets":
		return client.JSONCOS(ctx)
	case "tke", "k8s", "cluster", "clusters", "kubernetes":
		return client.JSONTKE(ctx)
	case "clb", "lb", "lbs", "load-balancer", "load-balancers":
		return client.JSONCLB(ctx)
	case "eip", "eips", "address", "addresses":
		return client.JSONEIP(ctx)
	case "cbs", "disk", "disks", "volume", "volumes":
		return client.JSONCBS(ctx)
	case "ssl", "cert", "certs", "certificate", "certificates":
		return client.JSONSSL(ctx)
	case "cam", "iam", "user", "users":
		return client.JSONCAM(ctx)
	case "redis", "valkey":
		return client.JSONRedis(ctx)
	case "mongo", "mongodb":
		return client.JSONMongoDB(ctx)
	case "cynosdb", "tdsql-c", "tdsqlc":
		return client.JSONCynosDB(ctx)
	case "cdn", "cdn-domains":
		return client.JSONCDN(ctx)
	case "edgeone", "teo", "zones":
		return client.JSONEdgeOne(ctx)
	case "waf", "waf-hosts":
		return client.JSONWAF(ctx)
	case "antiddos", "ddos":
		return client.JSONAntiDDoS(ctx)
	case "nat", "nat-gateway", "natgateway":
		return client.JSONNATGateways(ctx)
	case "vpn", "vpn-gateway", "vpngateway":
		return client.JSONVPNGateways(ctx)
	case "ccn", "cloud-connect":
		return client.JSONCCNs(ctx)
	case "dc", "direct-connect", "directconnect":
		return client.JSONDirectConnects(ctx)
	case "monitor", "alarm", "alarms", "alarm-policy":
		return client.JSONAlarmPolicies(ctx)
	case "cls", "log", "logs", "log-topics":
		return client.JSONCLSTopics(ctx)
	case "cloudaudit", "audit", "tracks":
		return client.JSONCloudAudit(ctx)
	case "subnet", "subnets":
		// Subnets don't have a dedicated JSON method; they're embedded
		// in JSONVPCs. Returning an explicit message lets the user
		// know rather than silently emitting the VPCs payload.
		return "", fmt.Errorf("--format json is not yet supported for subnets (use `tencent list vpc --format json` and read the embedded Subnets field)")
	default:
		return "", fmt.Errorf("unknown resource type: %s", resourceType)
	}
}
