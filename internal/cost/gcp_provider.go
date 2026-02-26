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

// GCPProvider implements Provider for GCP Cloud Billing
type GCPProvider struct {
	projectID      string
	billingAccount string
	debug          bool
}

// NewGCPProvider creates a new GCP cost provider
func NewGCPProvider(ctx context.Context, projectID string, debug bool) (*GCPProvider, error) {
	// If no project ID provided, try to get from environment or gcloud config
	if projectID == "" {
		projectID = os.Getenv("GCP_PROJECT_ID")
	}
	if projectID == "" {
		projectID = os.Getenv("GOOGLE_CLOUD_PROJECT")
	}
	if projectID == "" {
		// Try to get from gcloud config
		cmd := exec.CommandContext(ctx, "gcloud", "config", "get-value", "project")
		output, err := cmd.Output()
		if err == nil {
			projectID = strings.TrimSpace(string(output))
		}
	}

	if projectID == "" {
		return nil, fmt.Errorf("GCP project ID not configured")
	}

	// Get billing account ID
	billingAccount := os.Getenv("GCP_BILLING_ACCOUNT")

	return &GCPProvider{
		projectID:      projectID,
		billingAccount: billingAccount,
		debug:          debug,
	}, nil
}

// GetName returns the provider identifier
func (p *GCPProvider) GetName() string {
	return "gcp"
}

// IsConfigured checks if GCP credentials are available
func (p *GCPProvider) IsConfigured() bool {
	// Check if gcloud CLI is available and authenticated
	cmd := exec.Command("gcloud", "auth", "list", "--filter=status:ACTIVE", "--format=value(account)")
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(output)) != ""
}

// GetCosts returns total GCP costs for the given time period using gcloud CLI
func (p *GCPProvider) GetCosts(ctx context.Context, start, end time.Time) (*ProviderCost, error) {
	if p.debug {
		log.Printf("[gcp-cost] fetching costs from %s to %s", start.Format("2006-01-02"), end.Format("2006-01-02"))
	}

	// Use gcloud billing command to get costs
	// Note: This requires the Cloud Billing API to be enabled and proper permissions
	services, err := p.GetCostsByService(ctx, start, end)
	if err != nil {
		return nil, err
	}

	var totalCost float64
	for _, svc := range services {
		totalCost += svc.Cost
	}

	return &ProviderCost{
		Provider:         "gcp",
		TotalCost:        totalCost,
		Currency:         "USD",
		ServiceBreakdown: services,
	}, nil
}

// gcpBillingRecord represents a billing record from BigQuery export
type gcpBillingRecord struct {
	ServiceDescription string  `json:"service.description"`
	Cost               float64 `json:"cost"`
	Currency           string  `json:"currency"`
	UsageStartTime     string  `json:"usage_start_time"`
}

// GetCostsByService returns costs broken down by GCP service
func (p *GCPProvider) GetCostsByService(ctx context.Context, start, end time.Time) ([]ServiceCost, error) {
	if p.debug {
		log.Printf("[gcp-cost] fetching service costs for project %s", p.projectID)
	}

	// Try to get costs via BigQuery billing export if available
	costs, err := p.getCostsFromBigQuery(ctx, start, end)
	if err == nil && len(costs) > 0 {
		return costs, nil
	}

	// Fallback: Use gcloud CLI to estimate costs based on resource usage
	// This is less accurate but works without BigQuery billing export
	if p.debug {
		log.Printf("[gcp-cost] BigQuery billing not available, using resource-based estimation")
	}

	return p.estimateCostsFromResources(ctx)
}

// getCostsFromBigQuery queries the billing export table in BigQuery
func (p *GCPProvider) getCostsFromBigQuery(ctx context.Context, start, end time.Time) ([]ServiceCost, error) {
	// Check if billing export dataset is configured
	billingDataset := os.Getenv("GCP_BILLING_DATASET")
	if billingDataset == "" {
		return nil, fmt.Errorf("GCP_BILLING_DATASET not configured")
	}

	billingTable := os.Getenv("GCP_BILLING_TABLE")
	if billingTable == "" {
		billingTable = "gcp_billing_export_v1"
	}

	query := fmt.Sprintf(`
		SELECT
			service.description as service_description,
			SUM(cost) as total_cost,
			currency
		FROM %s.%s.%s
		WHERE DATE(usage_start_time) >= '%s'
		AND DATE(usage_start_time) < '%s'
		AND project.id = '%s'
		GROUP BY service.description, currency
		ORDER BY total_cost DESC
	`, p.projectID, billingDataset, billingTable,
		start.Format("2006-01-02"),
		end.Format("2006-01-02"),
		p.projectID)

	cmd := exec.CommandContext(ctx, "bq", "query",
		"--use_legacy_sql=false",
		"--format=json",
		query)

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("BigQuery query failed: %w", err)
	}

	var results []struct {
		ServiceDescription string `json:"service_description"`
		TotalCost          string `json:"total_cost"`
		Currency           string `json:"currency"`
	}

	if err := json.Unmarshal(output, &results); err != nil {
		return nil, fmt.Errorf("failed to parse BigQuery results: %w", err)
	}

	var services []ServiceCost
	for _, r := range results {
		cost, _ := strconv.ParseFloat(r.TotalCost, 64)
		services = append(services, ServiceCost{
			Service: r.ServiceDescription,
			Cost:    cost,
		})
	}

	return services, nil
}

// estimateCostsFromResources estimates costs based on active resources
func (p *GCPProvider) estimateCostsFromResources(ctx context.Context) ([]ServiceCost, error) {
	var services []ServiceCost

	// Get Compute Engine instances
	computeCost, err := p.getComputeEngineCosts(ctx)
	if err == nil && computeCost > 0 {
		services = append(services, ServiceCost{
			Service: "Compute Engine",
			Cost:    computeCost,
		})
	}

	// Get Cloud SQL instances
	sqlCost, err := p.getCloudSQLCosts(ctx)
	if err == nil && sqlCost > 0 {
		services = append(services, ServiceCost{
			Service: "Cloud SQL",
			Cost:    sqlCost,
		})
	}

	// Get GKE clusters
	gkeCost, err := p.getGKECosts(ctx)
	if err == nil && gkeCost > 0 {
		services = append(services, ServiceCost{
			Service: "Kubernetes Engine",
			Cost:    gkeCost,
		})
	}

	// Get Cloud Storage
	storageCost, err := p.getStorageCosts(ctx)
	if err == nil && storageCost > 0 {
		services = append(services, ServiceCost{
			Service: "Cloud Storage",
			Cost:    storageCost,
		})
	}

	// Get Cloud Run
	runCost, err := p.getCloudRunCosts(ctx)
	if err == nil && runCost > 0 {
		services = append(services, ServiceCost{
			Service: "Cloud Run",
			Cost:    runCost,
		})
	}

	// Sort by cost descending
	sort.Slice(services, func(i, j int) bool {
		return services[i].Cost > services[j].Cost
	})

	return services, nil
}

// getComputeEngineCosts estimates Compute Engine costs
func (p *GCPProvider) getComputeEngineCosts(ctx context.Context) (float64, error) {
	cmd := exec.CommandContext(ctx, "gcloud", "compute", "instances", "list",
		"--project", p.projectID,
		"--format=json")

	output, err := cmd.Output()
	if err != nil {
		return 0, err
	}

	var instances []struct {
		MachineType string `json:"machineType"`
		Status      string `json:"status"`
	}

	if err := json.Unmarshal(output, &instances); err != nil {
		return 0, err
	}

	// Rough cost estimation based on instance count and type
	var totalCost float64
	for _, inst := range instances {
		if inst.Status != "RUNNING" {
			continue
		}
		// Estimate ~$25/month per running instance (rough average)
		totalCost += 25.0 * (float64(time.Now().Day()) / 30.0)
	}

	return totalCost, nil
}

// getCloudSQLCosts estimates Cloud SQL costs
func (p *GCPProvider) getCloudSQLCosts(ctx context.Context) (float64, error) {
	cmd := exec.CommandContext(ctx, "gcloud", "sql", "instances", "list",
		"--project", p.projectID,
		"--format=json")

	output, err := cmd.Output()
	if err != nil {
		return 0, err
	}

	var instances []struct {
		State string `json:"state"`
		Tier  string `json:"settings.tier"`
	}

	if err := json.Unmarshal(output, &instances); err != nil {
		return 0, err
	}

	// Rough cost estimation
	var totalCost float64
	for _, inst := range instances {
		if inst.State != "RUNNABLE" {
			continue
		}
		// Estimate ~$50/month per SQL instance (rough average)
		totalCost += 50.0 * (float64(time.Now().Day()) / 30.0)
	}

	return totalCost, nil
}

// getGKECosts estimates GKE costs
func (p *GCPProvider) getGKECosts(ctx context.Context) (float64, error) {
	cmd := exec.CommandContext(ctx, "gcloud", "container", "clusters", "list",
		"--project", p.projectID,
		"--format=json")

	output, err := cmd.Output()
	if err != nil {
		return 0, err
	}

	var clusters []struct {
		Status           string `json:"status"`
		CurrentNodeCount int    `json:"currentNodeCount"`
	}

	if err := json.Unmarshal(output, &clusters); err != nil {
		return 0, err
	}

	// Rough cost estimation
	var totalCost float64
	for _, cluster := range clusters {
		if cluster.Status != "RUNNING" {
			continue
		}
		// Estimate ~$75/month per node + $72/month management fee
		nodeCount := cluster.CurrentNodeCount
		if nodeCount == 0 {
			nodeCount = 3 // default
		}
		totalCost += (72.0 + float64(nodeCount)*75.0) * (float64(time.Now().Day()) / 30.0)
	}

	return totalCost, nil
}

// getStorageCosts estimates Cloud Storage costs
func (p *GCPProvider) getStorageCosts(ctx context.Context) (float64, error) {
	cmd := exec.CommandContext(ctx, "gsutil", "du", "-s", fmt.Sprintf("gs://*"))
	output, err := cmd.Output()
	if err != nil {
		return 0, err
	}

	// Parse total bytes and estimate cost (~$0.02/GB/month for standard storage)
	var totalBytes int64
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		parts := strings.Fields(line)
		if len(parts) >= 1 {
			bytes, _ := strconv.ParseInt(parts[0], 10, 64)
			totalBytes += bytes
		}
	}

	// Convert to GB and calculate monthly cost
	gb := float64(totalBytes) / (1024 * 1024 * 1024)
	return gb * 0.02, nil
}

// getCloudRunCosts estimates Cloud Run costs
func (p *GCPProvider) getCloudRunCosts(ctx context.Context) (float64, error) {
	cmd := exec.CommandContext(ctx, "gcloud", "run", "services", "list",
		"--project", p.projectID,
		"--platform=managed",
		"--format=json")

	output, err := cmd.Output()
	if err != nil {
		return 0, err
	}

	var services []struct {
		Status struct {
			Conditions []struct {
				Type   string `json:"type"`
				Status string `json:"status"`
			} `json:"conditions"`
		} `json:"status"`
	}

	if err := json.Unmarshal(output, &services); err != nil {
		return 0, err
	}

	// Cloud Run is pay-per-use, estimate based on service count
	// This is a rough estimate; actual costs depend on usage
	return float64(len(services)) * 5.0 * (float64(time.Now().Day()) / 30.0), nil
}

// GetDailyTrend returns daily cost breakdown
func (p *GCPProvider) GetDailyTrend(ctx context.Context, start, end time.Time) ([]DailyCost, error) {
	if p.debug {
		log.Printf("[gcp-cost] fetching daily trend from %s to %s", start.Format("2006-01-02"), end.Format("2006-01-02"))
	}

	// Try BigQuery first
	trend, err := p.getDailyTrendFromBigQuery(ctx, start, end)
	if err == nil && len(trend) > 0 {
		return trend, nil
	}

	// Fallback: estimate based on total cost
	totalCost, err := p.GetCosts(ctx, start, end)
	if err != nil {
		return nil, err
	}

	// Distribute cost evenly across days
	days := int(end.Sub(start).Hours() / 24)
	if days <= 0 {
		days = 1
	}
	dailyCost := totalCost.TotalCost / float64(days)

	var trend2 []DailyCost
	for d := start; d.Before(end); d = d.AddDate(0, 0, 1) {
		trend2 = append(trend2, DailyCost{
			Date:     d,
			Cost:     dailyCost,
			Provider: "gcp",
		})
	}

	return trend2, nil
}

// getDailyTrendFromBigQuery queries daily costs from BigQuery
func (p *GCPProvider) getDailyTrendFromBigQuery(ctx context.Context, start, end time.Time) ([]DailyCost, error) {
	billingDataset := os.Getenv("GCP_BILLING_DATASET")
	if billingDataset == "" {
		return nil, fmt.Errorf("GCP_BILLING_DATASET not configured")
	}

	billingTable := os.Getenv("GCP_BILLING_TABLE")
	if billingTable == "" {
		billingTable = "gcp_billing_export_v1"
	}

	query := fmt.Sprintf(`
		SELECT
			DATE(usage_start_time) as date,
			SUM(cost) as total_cost
		FROM %s.%s.%s
		WHERE DATE(usage_start_time) >= '%s'
		AND DATE(usage_start_time) < '%s'
		AND project.id = '%s'
		GROUP BY date
		ORDER BY date
	`, p.projectID, billingDataset, billingTable,
		start.Format("2006-01-02"),
		end.Format("2006-01-02"),
		p.projectID)

	cmd := exec.CommandContext(ctx, "bq", "query",
		"--use_legacy_sql=false",
		"--format=json",
		query)

	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var results []struct {
		Date      string `json:"date"`
		TotalCost string `json:"total_cost"`
	}

	if err := json.Unmarshal(output, &results); err != nil {
		return nil, err
	}

	var trend []DailyCost
	for _, r := range results {
		date, _ := time.Parse("2006-01-02", r.Date)
		cost, _ := strconv.ParseFloat(r.TotalCost, 64)
		trend = append(trend, DailyCost{
			Date:     date,
			Cost:     cost,
			Provider: "gcp",
		})
	}

	return trend, nil
}

// GetCostsByTag returns costs grouped by label
func (p *GCPProvider) GetCostsByTag(ctx context.Context, tagKey string, start, end time.Time) ([]TagCost, error) {
	billingDataset := os.Getenv("GCP_BILLING_DATASET")
	if billingDataset == "" {
		return nil, fmt.Errorf("GCP_BILLING_DATASET not configured for tag-based costs")
	}

	billingTable := os.Getenv("GCP_BILLING_TABLE")
	if billingTable == "" {
		billingTable = "gcp_billing_export_v1"
	}

	query := fmt.Sprintf(`
		SELECT
			labels.key as tag_key,
			labels.value as tag_value,
			SUM(cost) as total_cost
		FROM %s.%s.%s, UNNEST(labels) as labels
		WHERE DATE(usage_start_time) >= '%s'
		AND DATE(usage_start_time) < '%s'
		AND project.id = '%s'
		AND labels.key = '%s'
		GROUP BY tag_key, tag_value
		ORDER BY total_cost DESC
	`, p.projectID, billingDataset, billingTable,
		start.Format("2006-01-02"),
		end.Format("2006-01-02"),
		p.projectID,
		tagKey)

	cmd := exec.CommandContext(ctx, "bq", "query",
		"--use_legacy_sql=false",
		"--format=json",
		query)

	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var results []struct {
		TagKey    string `json:"tag_key"`
		TagValue  string `json:"tag_value"`
		TotalCost string `json:"total_cost"`
	}

	if err := json.Unmarshal(output, &results); err != nil {
		return nil, err
	}

	var tags []TagCost
	for _, r := range results {
		cost, _ := strconv.ParseFloat(r.TotalCost, 64)
		tags = append(tags, TagCost{
			TagKey:   r.TagKey,
			TagValue: r.TagValue,
			Cost:     cost,
		})
	}

	return tags, nil
}
