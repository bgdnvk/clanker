package cloudflare

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// CreateCloudflareCommands creates the Cloudflare command tree for static commands
func CreateCloudflareCommands() *cobra.Command {
	cfCmd := &cobra.Command{
		Use:     "cf",
		Short:   "Query Cloudflare infrastructure directly",
		Long:    "Query your Cloudflare infrastructure without AI interpretation. Useful for getting raw data.",
		Aliases: []string{"cloudflare"},
	}

	cfListCmd := &cobra.Command{
		Use:   "list [resource]",
		Short: "List Cloudflare resources",
		Long: `List Cloudflare resources of a specific type.

Supported resources:
  zones                - DNS zones (domains)
  records              - DNS records (requires --zone)
  workers              - Cloudflare Workers (requires wrangler)
  kv-namespaces        - Workers KV namespaces (requires wrangler)
  d1-databases         - D1 databases (requires wrangler)
  r2-buckets           - R2 storage buckets (requires wrangler)
  tunnels              - Cloudflare Tunnels (requires cloudflared)
  firewall-rules       - Firewall rules (requires --zone)
  page-rules           - Page rules (requires --zone)`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resourceType := strings.ToLower(args[0])
			zoneID, _ := cmd.Flags().GetString("zone")
			zoneName, _ := cmd.Flags().GetString("zone-name")

			accountID := ResolveAccountID()
			apiToken := ResolveAPIToken()

			if apiToken == "" {
				return fmt.Errorf("cloudflare api_token is required (set cloudflare.api_token, CLOUDFLARE_API_TOKEN, or CF_API_TOKEN)")
			}

			debug := viper.GetBool("debug")
			client, err := NewClient(accountID, apiToken, debug)
			if err != nil {
				return err
			}

			ctx := context.Background()

			// If zone name provided but not zone ID, look up the ID
			if zoneID == "" && zoneName != "" {
				zoneID, err = lookupZoneID(ctx, client, zoneName)
				if err != nil {
					return err
				}
			}

			switch resourceType {
			case "zones", "zone", "domains":
				return listZones(ctx, client)

			case "records", "dns", "dns-records":
				if zoneID == "" {
					return fmt.Errorf("--zone or --zone-name is required to list DNS records")
				}
				return listRecords(ctx, client, zoneID)

			case "workers", "worker":
				return listWorkers(ctx, client)

			case "kv", "kv-namespaces":
				return listKVNamespaces(ctx, client)

			case "d1", "d1-databases":
				return listD1Databases(ctx, client)

			case "r2", "r2-buckets":
				return listR2Buckets(ctx, client)

			case "tunnels", "tunnel":
				return listTunnels(ctx, client)

			case "firewall", "firewall-rules":
				if zoneID == "" {
					return fmt.Errorf("--zone or --zone-name is required to list firewall rules")
				}
				return listFirewallRules(ctx, client, zoneID)

			case "page-rules", "pagerules":
				if zoneID == "" {
					return fmt.Errorf("--zone or --zone-name is required to list page rules")
				}
				return listPageRules(ctx, client, zoneID)

			default:
				return fmt.Errorf("unknown resource type: %s", resourceType)
			}
		},
	}

	cfListCmd.Flags().String("zone", "", "Zone ID for zone-specific resources")
	cfListCmd.Flags().String("zone-name", "", "Zone name (domain) to look up zone ID")

	cfCmd.AddCommand(cfListCmd)

	return cfCmd
}

// lookupZoneID looks up a zone ID by name
func lookupZoneID(ctx context.Context, client *Client, zoneName string) (string, error) {
	result, err := client.RunAPIWithContext(ctx, "GET", fmt.Sprintf("/zones?name=%s", zoneName), "")
	if err != nil {
		return "", err
	}

	var response struct {
		Success bool `json:"success"`
		Result  []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"result"`
	}

	if err := json.Unmarshal([]byte(result), &response); err != nil {
		return "", fmt.Errorf("failed to parse zone response: %w", err)
	}

	if !response.Success || len(response.Result) == 0 {
		return "", fmt.Errorf("zone not found: %s", zoneName)
	}

	return response.Result[0].ID, nil
}

// listZones lists all zones
func listZones(ctx context.Context, client *Client) error {
	result, err := client.RunAPIWithContext(ctx, "GET", "/zones", "")
	if err != nil {
		return err
	}

	var response struct {
		Success bool `json:"success"`
		Result  []struct {
			ID          string   `json:"id"`
			Name        string   `json:"name"`
			Status      string   `json:"status"`
			NameServers []string `json:"name_servers"`
			Plan        struct {
				Name string `json:"name"`
			} `json:"plan"`
		} `json:"result"`
	}

	if err := json.Unmarshal([]byte(result), &response); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if !response.Success {
		return fmt.Errorf("API request failed")
	}

	fmt.Println("Cloudflare Zones:")
	fmt.Println()
	for _, zone := range response.Result {
		fmt.Printf("  %s\n", zone.Name)
		fmt.Printf("    ID: %s\n", zone.ID)
		fmt.Printf("    Status: %s\n", zone.Status)
		fmt.Printf("    Plan: %s\n", zone.Plan.Name)
		if len(zone.NameServers) > 0 {
			fmt.Printf("    Nameservers: %s\n", strings.Join(zone.NameServers, ", "))
		}
		fmt.Println()
	}

	return nil
}

// listRecords lists DNS records for a zone
func listRecords(ctx context.Context, client *Client, zoneID string) error {
	result, err := client.RunAPIWithContext(ctx, "GET", fmt.Sprintf("/zones/%s/dns_records", zoneID), "")
	if err != nil {
		return err
	}

	var response struct {
		Success bool `json:"success"`
		Result  []struct {
			ID      string `json:"id"`
			Type    string `json:"type"`
			Name    string `json:"name"`
			Content string `json:"content"`
			Proxied bool   `json:"proxied"`
			TTL     int    `json:"ttl"`
		} `json:"result"`
	}

	if err := json.Unmarshal([]byte(result), &response); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if !response.Success {
		return fmt.Errorf("API request failed")
	}

	fmt.Println("DNS Records:")
	fmt.Println()
	for _, record := range response.Result {
		proxiedStr := "DNS only"
		if record.Proxied {
			proxiedStr = "Proxied"
		}

		ttlStr := "Auto"
		if record.TTL > 1 {
			ttlStr = fmt.Sprintf("%d", record.TTL)
		}

		fmt.Printf("  %s %s -> %s\n", record.Type, record.Name, record.Content)
		fmt.Printf("    ID: %s\n", record.ID)
		fmt.Printf("    TTL: %s, %s\n", ttlStr, proxiedStr)
		fmt.Println()
	}

	return nil
}

// listWorkers lists Cloudflare Workers using wrangler
func listWorkers(ctx context.Context, client *Client) error {
	result, err := client.RunWranglerWithContext(ctx, "deployments", "list")
	if err != nil {
		// Try alternative command
		result, err = client.RunWranglerWithContext(ctx, "whoami")
		if err != nil {
			return fmt.Errorf("failed to list workers: %w", err)
		}
		fmt.Println("Workers information:")
		fmt.Println(result)
		fmt.Println("\nNote: Use 'wrangler deployments list' directly for deployment info")
		return nil
	}

	fmt.Println("Cloudflare Workers:")
	fmt.Println(result)
	return nil
}

// listKVNamespaces lists Workers KV namespaces
func listKVNamespaces(ctx context.Context, client *Client) error {
	result, err := client.RunWranglerWithContext(ctx, "kv:namespace", "list")
	if err != nil {
		return fmt.Errorf("failed to list KV namespaces: %w", err)
	}

	fmt.Println("Workers KV Namespaces:")
	fmt.Println(result)
	return nil
}

// listD1Databases lists D1 databases
func listD1Databases(ctx context.Context, client *Client) error {
	result, err := client.RunWranglerWithContext(ctx, "d1", "list")
	if err != nil {
		return fmt.Errorf("failed to list D1 databases: %w", err)
	}

	fmt.Println("D1 Databases:")
	fmt.Println(result)
	return nil
}

// listR2Buckets lists R2 storage buckets
func listR2Buckets(ctx context.Context, client *Client) error {
	result, err := client.RunWranglerWithContext(ctx, "r2", "bucket", "list")
	if err != nil {
		return fmt.Errorf("failed to list R2 buckets: %w", err)
	}

	fmt.Println("R2 Buckets:")
	fmt.Println(result)
	return nil
}

// listTunnels lists Cloudflare Tunnels
func listTunnels(ctx context.Context, client *Client) error {
	result, err := client.RunCloudflaredWithContext(ctx, "tunnel", "list")
	if err != nil {
		return fmt.Errorf("failed to list tunnels: %w", err)
	}

	fmt.Println("Cloudflare Tunnels:")
	fmt.Println(result)
	return nil
}

// listFirewallRules lists firewall rules for a zone
func listFirewallRules(ctx context.Context, client *Client, zoneID string) error {
	result, err := client.RunAPIWithContext(ctx, "GET", fmt.Sprintf("/zones/%s/firewall/rules", zoneID), "")
	if err != nil {
		return err
	}

	var response struct {
		Success bool `json:"success"`
		Result  []struct {
			ID          string `json:"id"`
			Description string `json:"description"`
			Action      string `json:"action"`
			Priority    int    `json:"priority"`
			Paused      bool   `json:"paused"`
			Filter      struct {
				Expression string `json:"expression"`
			} `json:"filter"`
		} `json:"result"`
	}

	if err := json.Unmarshal([]byte(result), &response); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if !response.Success {
		return fmt.Errorf("API request failed")
	}

	fmt.Println("Firewall Rules:")
	fmt.Println()
	for _, rule := range response.Result {
		pausedStr := ""
		if rule.Paused {
			pausedStr = " (paused)"
		}

		fmt.Printf("  %s%s\n", rule.Description, pausedStr)
		fmt.Printf("    ID: %s\n", rule.ID)
		fmt.Printf("    Action: %s\n", rule.Action)
		fmt.Printf("    Priority: %d\n", rule.Priority)
		fmt.Printf("    Expression: %s\n", rule.Filter.Expression)
		fmt.Println()
	}

	return nil
}

// listPageRules lists page rules for a zone
func listPageRules(ctx context.Context, client *Client, zoneID string) error {
	result, err := client.RunAPIWithContext(ctx, "GET", fmt.Sprintf("/zones/%s/pagerules", zoneID), "")
	if err != nil {
		return err
	}

	var response struct {
		Success bool `json:"success"`
		Result  []struct {
			ID       string `json:"id"`
			Status   string `json:"status"`
			Priority int    `json:"priority"`
			Targets  []struct {
				Target     string `json:"target"`
				Constraint struct {
					Operator string `json:"operator"`
					Value    string `json:"value"`
				} `json:"constraint"`
			} `json:"targets"`
			Actions []struct {
				ID    string      `json:"id"`
				Value interface{} `json:"value"`
			} `json:"actions"`
		} `json:"result"`
	}

	if err := json.Unmarshal([]byte(result), &response); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if !response.Success {
		return fmt.Errorf("API request failed")
	}

	fmt.Println("Page Rules:")
	fmt.Println()
	for _, rule := range response.Result {
		fmt.Printf("  Rule %d (Priority: %d, Status: %s)\n", rule.Priority, rule.Priority, rule.Status)
		fmt.Printf("    ID: %s\n", rule.ID)
		for _, target := range rule.Targets {
			fmt.Printf("    Match: %s %s\n", target.Constraint.Operator, target.Constraint.Value)
		}
		for _, action := range rule.Actions {
			fmt.Printf("    Action: %s = %v\n", action.ID, action.Value)
		}
		fmt.Println()
	}

	return nil
}
