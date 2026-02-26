package cost

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// CloudflareProvider implements Provider for Cloudflare billing
type CloudflareProvider struct {
	accountID string
	apiToken  string
	debug     bool
}

// NewCloudflareProvider creates a new Cloudflare cost provider
func NewCloudflareProvider(ctx context.Context, debug bool) (*CloudflareProvider, error) {
	accountID := os.Getenv("CLOUDFLARE_ACCOUNT_ID")
	apiToken := os.Getenv("CLOUDFLARE_API_TOKEN")

	// Try to get from wrangler config if not in env
	if accountID == "" {
		cmd := exec.CommandContext(ctx, "wrangler", "whoami")
		output, err := cmd.Output()
		if err == nil {
			// Parse account ID from output
			lines := strings.Split(string(output), "\n")
			for _, line := range lines {
				if strings.Contains(line, "Account ID") {
					parts := strings.Split(line, ":")
					if len(parts) >= 2 {
						accountID = strings.TrimSpace(parts[1])
					}
				}
			}
		}
	}

	if accountID == "" {
		return nil, fmt.Errorf("Cloudflare account ID not configured")
	}

	return &CloudflareProvider{
		accountID: accountID,
		apiToken:  apiToken,
		debug:     debug,
	}, nil
}

// GetName returns the provider identifier
func (p *CloudflareProvider) GetName() string {
	return "cloudflare"
}

// IsConfigured checks if Cloudflare credentials are available
func (p *CloudflareProvider) IsConfigured() bool {
	// Check if wrangler is available and authenticated
	cmd := exec.Command("wrangler", "whoami")
	err := cmd.Run()
	return err == nil || p.apiToken != ""
}

// GetCosts returns total Cloudflare costs for the given time period
func (p *CloudflareProvider) GetCosts(ctx context.Context, start, end time.Time) (*ProviderCost, error) {
	if p.debug {
		log.Printf("[cloudflare-cost] fetching costs from %s to %s", start.Format("2006-01-02"), end.Format("2006-01-02"))
	}

	services, err := p.GetCostsByService(ctx, start, end)
	if err != nil {
		return nil, err
	}

	var totalCost float64
	for _, svc := range services {
		totalCost += svc.Cost
	}

	return &ProviderCost{
		Provider:         "cloudflare",
		TotalCost:        totalCost,
		Currency:         "USD",
		ServiceBreakdown: services,
	}, nil
}

// GetCostsByService returns costs broken down by Cloudflare service
// Cloudflare doesn't have a public billing API, so we estimate based on usage
func (p *CloudflareProvider) GetCostsByService(ctx context.Context, start, end time.Time) ([]ServiceCost, error) {
	if p.debug {
		log.Printf("[cloudflare-cost] estimating service costs for account %s", p.accountID)
	}

	var services []ServiceCost

	// Get Workers usage and costs
	workersCost, err := p.getWorkersCosts(ctx, start, end)
	if err == nil && workersCost > 0 {
		services = append(services, ServiceCost{
			Service: "Workers",
			Cost:    workersCost,
		})
	}

	// Get KV usage and costs
	kvCost, err := p.getKVCosts(ctx, start, end)
	if err == nil && kvCost > 0 {
		services = append(services, ServiceCost{
			Service: "Workers KV",
			Cost:    kvCost,
		})
	}

	// Get R2 usage and costs
	r2Cost, err := p.getR2Costs(ctx, start, end)
	if err == nil && r2Cost > 0 {
		services = append(services, ServiceCost{
			Service: "R2 Storage",
			Cost:    r2Cost,
		})
	}

	// Get D1 usage and costs
	d1Cost, err := p.getD1Costs(ctx, start, end)
	if err == nil && d1Cost > 0 {
		services = append(services, ServiceCost{
			Service: "D1 Database",
			Cost:    d1Cost,
		})
	}

	// Get Pages usage
	pagesCost, err := p.getPagesCosts(ctx)
	if err == nil && pagesCost > 0 {
		services = append(services, ServiceCost{
			Service: "Pages",
			Cost:    pagesCost,
		})
	}

	// Get Stream usage
	streamCost, err := p.getStreamCosts(ctx, start, end)
	if err == nil && streamCost > 0 {
		services = append(services, ServiceCost{
			Service: "Stream",
			Cost:    streamCost,
		})
	}

	// Sort by cost descending
	sort.Slice(services, func(i, j int) bool {
		return services[i].Cost > services[j].Cost
	})

	return services, nil
}

// getWorkersCosts estimates Workers costs based on requests
func (p *CloudflareProvider) getWorkersCosts(ctx context.Context, start, end time.Time) (float64, error) {
	// Use wrangler to get Workers list and estimate based on typical usage
	cmd := exec.CommandContext(ctx, "wrangler", "deployments", "list", "--json")
	output, err := cmd.Output()
	if err != nil {
		return 0, err
	}

	var deployments []struct {
		ID string `json:"id"`
	}

	if err := json.Unmarshal(output, &deployments); err != nil {
		// Try alternative format
		return p.estimateWorkersCostsFromList(ctx)
	}

	// Workers pricing: $0.50/million requests after free tier (10M/month)
	// Estimate based on deployment count
	return float64(len(deployments)) * 5.0 * (float64(time.Now().Day()) / 30.0), nil
}

func (p *CloudflareProvider) estimateWorkersCostsFromList(ctx context.Context) (float64, error) {
	cmd := exec.CommandContext(ctx, "wrangler", "list")
	output, err := cmd.Output()
	if err != nil {
		return 0, err
	}

	// Count workers from output
	lines := strings.Split(string(output), "\n")
	workerCount := 0
	for _, line := range lines {
		if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "Name") && !strings.HasPrefix(line, "---") {
			workerCount++
		}
	}

	// Estimate ~$5/worker/month for typical usage
	return float64(workerCount) * 5.0 * (float64(time.Now().Day()) / 30.0), nil
}

// getKVCosts estimates Workers KV costs
func (p *CloudflareProvider) getKVCosts(ctx context.Context, start, end time.Time) (float64, error) {
	cmd := exec.CommandContext(ctx, "wrangler", "kv:namespace", "list", "--json")
	output, err := cmd.Output()
	if err != nil {
		return 0, err
	}

	var namespaces []struct {
		ID string `json:"id"`
	}

	if err := json.Unmarshal(output, &namespaces); err != nil {
		return 0, err
	}

	// KV pricing: $0.50/million reads, $5/million writes, $0.50/GB stored
	// Estimate based on namespace count
	return float64(len(namespaces)) * 2.0 * (float64(time.Now().Day()) / 30.0), nil
}

// getR2Costs estimates R2 storage costs
func (p *CloudflareProvider) getR2Costs(ctx context.Context, start, end time.Time) (float64, error) {
	cmd := exec.CommandContext(ctx, "wrangler", "r2", "bucket", "list", "--json")
	output, err := cmd.Output()
	if err != nil {
		return 0, err
	}

	var buckets []struct {
		Name string `json:"name"`
	}

	if err := json.Unmarshal(output, &buckets); err != nil {
		return 0, err
	}

	// R2 pricing: $0.015/GB/month storage, $0.36/million Class A ops, $0.036/million Class B ops
	// Estimate based on bucket count
	return float64(len(buckets)) * 3.0 * (float64(time.Now().Day()) / 30.0), nil
}

// getD1Costs estimates D1 database costs
func (p *CloudflareProvider) getD1Costs(ctx context.Context, start, end time.Time) (float64, error) {
	cmd := exec.CommandContext(ctx, "wrangler", "d1", "list", "--json")
	output, err := cmd.Output()
	if err != nil {
		return 0, err
	}

	var databases []struct {
		UUID string `json:"uuid"`
	}

	if err := json.Unmarshal(output, &databases); err != nil {
		return 0, err
	}

	// D1 pricing: $0.75/million rows read, $1.00/million rows written
	// Estimate based on database count
	return float64(len(databases)) * 1.0 * (float64(time.Now().Day()) / 30.0), nil
}

// getPagesCosts estimates Pages costs
func (p *CloudflareProvider) getPagesCosts(ctx context.Context) (float64, error) {
	cmd := exec.CommandContext(ctx, "wrangler", "pages", "project", "list", "--json")
	output, err := cmd.Output()
	if err != nil {
		return 0, err
	}

	var projects []struct {
		Name string `json:"name"`
	}

	if err := json.Unmarshal(output, &projects); err != nil {
		return 0, err
	}

	// Pages is free for most usage, minimal costs for builds
	return float64(len(projects)) * 0.5 * (float64(time.Now().Day()) / 30.0), nil
}

// getStreamCosts estimates Stream costs
func (p *CloudflareProvider) getStreamCosts(ctx context.Context, start, end time.Time) (float64, error) {
	// Stream doesn't have a wrangler command, would need API call
	// For now, return 0 as it requires API token
	return 0, nil
}

// GetDailyTrend returns daily cost breakdown
func (p *CloudflareProvider) GetDailyTrend(ctx context.Context, start, end time.Time) ([]DailyCost, error) {
	if p.debug {
		log.Printf("[cloudflare-cost] fetching daily trend from %s to %s", start.Format("2006-01-02"), end.Format("2006-01-02"))
	}

	// Cloudflare doesn't provide daily billing API
	// Estimate based on total cost distributed evenly
	totalCost, err := p.GetCosts(ctx, start, end)
	if err != nil {
		return nil, err
	}

	days := int(end.Sub(start).Hours() / 24)
	if days <= 0 {
		days = 1
	}
	dailyCost := totalCost.TotalCost / float64(days)

	var trend []DailyCost
	for d := start; d.Before(end); d = d.AddDate(0, 0, 1) {
		trend = append(trend, DailyCost{
			Date:     d,
			Cost:     dailyCost,
			Provider: "cloudflare",
		})
	}

	return trend, nil
}

// CloudflareAnalyticsResponse represents a GraphQL analytics response
type CloudflareAnalyticsResponse struct {
	Data struct {
		Viewer struct {
			Accounts []struct {
				WorkersInvocationsAdaptive []struct {
					Sum struct {
						Requests int64 `json:"requests"`
					} `json:"sum"`
				} `json:"workersInvocationsAdaptive"`
			} `json:"accounts"`
		} `json:"viewer"`
	} `json:"data"`
}

// getWorkersRequestsFromAPI gets Workers request count via GraphQL API
func (p *CloudflareProvider) getWorkersRequestsFromAPI(ctx context.Context, start, end time.Time) (int64, error) {
	if p.apiToken == "" {
		return 0, fmt.Errorf("API token required for analytics")
	}

	query := fmt.Sprintf(`{
		"query": "query { viewer { accounts(filter: {accountTag: \"%s\"}) { workersInvocationsAdaptive(filter: {datetime_geq: \"%s\", datetime_lt: \"%s\"}, limit: 1000) { sum { requests } } } } }"
	}`, p.accountID, start.Format(time.RFC3339), end.Format(time.RFC3339))

	cmd := exec.CommandContext(ctx, "curl", "-s",
		"https://api.cloudflare.com/client/v4/graphql",
		"-H", fmt.Sprintf("Authorization: Bearer %s", p.apiToken),
		"-H", "Content-Type: application/json",
		"--data", query)

	output, err := cmd.Output()
	if err != nil {
		return 0, err
	}

	var result CloudflareAnalyticsResponse
	if err := json.Unmarshal(output, &result); err != nil {
		return 0, err
	}

	var totalRequests int64
	for _, account := range result.Data.Viewer.Accounts {
		for _, inv := range account.WorkersInvocationsAdaptive {
			totalRequests += inv.Sum.Requests
		}
	}

	return totalRequests, nil
}

// getBillingFromAPI attempts to get billing data from Cloudflare API
func (p *CloudflareProvider) getBillingFromAPI(ctx context.Context) (map[string]float64, error) {
	if p.apiToken == "" {
		return nil, fmt.Errorf("API token required for billing")
	}

	cmd := exec.CommandContext(ctx, "curl", "-s",
		fmt.Sprintf("https://api.cloudflare.com/client/v4/accounts/%s/billing/profile", p.accountID),
		"-H", fmt.Sprintf("Authorization: Bearer %s", p.apiToken),
		"-H", "Content-Type: application/json")

	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var result struct {
		Success bool `json:"success"`
		Result  struct {
			CurrentPlan struct {
				Price    float64 `json:"price"`
				Currency string  `json:"currency"`
			} `json:"current_plan"`
		} `json:"result"`
	}

	if err := json.Unmarshal(output, &result); err != nil {
		return nil, err
	}

	costs := make(map[string]float64)
	if result.Success && result.Result.CurrentPlan.Price > 0 {
		costs["Plan"] = result.Result.CurrentPlan.Price
	}

	return costs, nil
}

// estimateCostsFromUsage calculates estimated costs from usage metrics
func estimateCostsFromUsage(requests int64, storageGB float64, kvOps int64) float64 {
	var cost float64

	// Workers: $0.50/million requests (after 10M free)
	if requests > 10000000 {
		billableRequests := requests - 10000000
		cost += float64(billableRequests) / 1000000 * 0.50
	}

	// R2: $0.015/GB/month
	cost += storageGB * 0.015

	// KV: $0.50/million operations
	cost += float64(kvOps) / 1000000 * 0.50

	return cost
}

// getUsageMetrics collects various usage metrics
func (p *CloudflareProvider) getUsageMetrics(ctx context.Context, start, end time.Time) (requests int64, storageGB float64, kvOps int64, err error) {
	// Get Workers requests
	requests, _ = p.getWorkersRequestsFromAPI(ctx, start, end)

	// Get R2 storage size
	cmd := exec.CommandContext(ctx, "wrangler", "r2", "bucket", "list", "--json")
	output, err := cmd.Output()
	if err == nil {
		var buckets []struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(output, &buckets) == nil {
			// Estimate ~10GB per bucket average
			storageGB = float64(len(buckets)) * 10.0
		}
	}

	// KV operations are harder to estimate without API access
	kvOps = 0

	return requests, storageGB, kvOps, nil
}

// GetCostsByTag returns costs grouped by tag (not supported by Cloudflare)
func (p *CloudflareProvider) GetCostsByTag(ctx context.Context, tagKey string, start, end time.Time) ([]TagCost, error) {
	return nil, fmt.Errorf("Cloudflare does not support cost allocation by tags")
}

// formatCurrency formats a float as currency string
func formatCurrency(amount float64) string {
	return fmt.Sprintf("$%.2f", amount)
}

// parseCurrency parses a currency string to float
func parseCurrency(s string) float64 {
	s = strings.TrimPrefix(s, "$")
	s = strings.ReplaceAll(s, ",", "")
	f, _ := strconv.ParseFloat(s, 64)
	return f
}
