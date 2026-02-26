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

// AzureProvider implements Provider for Azure Cost Management
type AzureProvider struct {
	subscriptionID string
	debug          bool
}

// NewAzureProvider creates a new Azure cost provider
func NewAzureProvider(ctx context.Context, subscriptionID string, debug bool) (*AzureProvider, error) {
	// If no subscription ID provided, try to get from environment or az CLI
	if subscriptionID == "" {
		subscriptionID = os.Getenv("AZURE_SUBSCRIPTION_ID")
	}
	if subscriptionID == "" {
		// Try to get from az CLI
		cmd := exec.CommandContext(ctx, "az", "account", "show", "--query", "id", "-o", "tsv")
		output, err := cmd.Output()
		if err == nil {
			subscriptionID = strings.TrimSpace(string(output))
		}
	}

	if subscriptionID == "" {
		return nil, fmt.Errorf("Azure subscription ID not configured")
	}

	return &AzureProvider{
		subscriptionID: subscriptionID,
		debug:          debug,
	}, nil
}

// GetName returns the provider identifier
func (p *AzureProvider) GetName() string {
	return "azure"
}

// IsConfigured checks if Azure CLI is authenticated
func (p *AzureProvider) IsConfigured() bool {
	cmd := exec.Command("az", "account", "show")
	err := cmd.Run()
	return err == nil
}

// GetCosts returns total Azure costs for the given time period
func (p *AzureProvider) GetCosts(ctx context.Context, start, end time.Time) (*ProviderCost, error) {
	if p.debug {
		log.Printf("[azure-cost] fetching costs from %s to %s", start.Format("2006-01-02"), end.Format("2006-01-02"))
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
		Provider:         "azure",
		TotalCost:        totalCost,
		Currency:         "USD",
		ServiceBreakdown: services,
	}, nil
}

// azureCostQueryResult represents the Azure Cost Management query result
type azureCostQueryResult struct {
	Properties struct {
		Rows    [][]interface{} `json:"rows"`
		Columns []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"columns"`
	} `json:"properties"`
}

// GetCostsByService returns costs broken down by Azure service
func (p *AzureProvider) GetCostsByService(ctx context.Context, start, end time.Time) ([]ServiceCost, error) {
	if p.debug {
		log.Printf("[azure-cost] fetching service costs for subscription %s", p.subscriptionID)
	}

	// Use Azure CLI to query Cost Management API
	queryBody := fmt.Sprintf(`{
		"type": "ActualCost",
		"timeframe": "Custom",
		"timePeriod": {
			"from": "%sT00:00:00Z",
			"to": "%sT23:59:59Z"
		},
		"dataset": {
			"granularity": "None",
			"aggregation": {
				"totalCost": {
					"name": "Cost",
					"function": "Sum"
				}
			},
			"grouping": [
				{
					"type": "Dimension",
					"name": "ServiceName"
				}
			]
		}
	}`, start.Format("2006-01-02"), end.Format("2006-01-02"))

	cmd := exec.CommandContext(ctx, "az", "rest",
		"--method", "POST",
		"--url", fmt.Sprintf("https://management.azure.com/subscriptions/%s/providers/Microsoft.CostManagement/query?api-version=2023-03-01", p.subscriptionID),
		"--body", queryBody)

	output, err := cmd.Output()
	if err != nil {
		// Fallback to resource-based estimation
		if p.debug {
			log.Printf("[azure-cost] Cost Management API failed, using resource estimation: %v", err)
		}
		return p.estimateCostsFromResources(ctx)
	}

	var result azureCostQueryResult
	if err := json.Unmarshal(output, &result); err != nil {
		return nil, fmt.Errorf("failed to parse Azure cost response: %w", err)
	}

	var services []ServiceCost
	for _, row := range result.Properties.Rows {
		if len(row) < 2 {
			continue
		}
		serviceName, ok := row[1].(string)
		if !ok {
			continue
		}
		var cost float64
		switch v := row[0].(type) {
		case float64:
			cost = v
		case string:
			cost, _ = strconv.ParseFloat(v, 64)
		}

		services = append(services, ServiceCost{
			Service: serviceName,
			Cost:    cost,
		})
	}

	// Sort by cost descending
	sort.Slice(services, func(i, j int) bool {
		return services[i].Cost > services[j].Cost
	})

	return services, nil
}

// estimateCostsFromResources estimates costs based on active resources
func (p *AzureProvider) estimateCostsFromResources(ctx context.Context) ([]ServiceCost, error) {
	var services []ServiceCost

	// Get Virtual Machines
	vmCost, err := p.getVMCosts(ctx)
	if err == nil && vmCost > 0 {
		services = append(services, ServiceCost{
			Service: "Virtual Machines",
			Cost:    vmCost,
		})
	}

	// Get Azure SQL
	sqlCost, err := p.getSQLCosts(ctx)
	if err == nil && sqlCost > 0 {
		services = append(services, ServiceCost{
			Service: "Azure SQL Database",
			Cost:    sqlCost,
		})
	}

	// Get AKS clusters
	aksCost, err := p.getAKSCosts(ctx)
	if err == nil && aksCost > 0 {
		services = append(services, ServiceCost{
			Service: "Azure Kubernetes Service",
			Cost:    aksCost,
		})
	}

	// Get Storage accounts
	storageCost, err := p.getStorageCosts(ctx)
	if err == nil && storageCost > 0 {
		services = append(services, ServiceCost{
			Service: "Storage",
			Cost:    storageCost,
		})
	}

	// Get App Service
	appServiceCost, err := p.getAppServiceCosts(ctx)
	if err == nil && appServiceCost > 0 {
		services = append(services, ServiceCost{
			Service: "App Service",
			Cost:    appServiceCost,
		})
	}

	// Sort by cost descending
	sort.Slice(services, func(i, j int) bool {
		return services[i].Cost > services[j].Cost
	})

	return services, nil
}

// getVMCosts estimates Virtual Machine costs
func (p *AzureProvider) getVMCosts(ctx context.Context) (float64, error) {
	cmd := exec.CommandContext(ctx, "az", "vm", "list",
		"--subscription", p.subscriptionID,
		"-o", "json")

	output, err := cmd.Output()
	if err != nil {
		return 0, err
	}

	var vms []struct {
		HardwareProfile struct {
			VMSize string `json:"vmSize"`
		} `json:"hardwareProfile"`
	}

	if err := json.Unmarshal(output, &vms); err != nil {
		return 0, err
	}

	// Rough cost estimation based on VM count (~$50/month average per VM)
	dayOfMonth := float64(time.Now().Day())
	return float64(len(vms)) * 50.0 * (dayOfMonth / 30.0), nil
}

// getSQLCosts estimates Azure SQL costs
func (p *AzureProvider) getSQLCosts(ctx context.Context) (float64, error) {
	cmd := exec.CommandContext(ctx, "az", "sql", "db", "list",
		"--subscription", p.subscriptionID,
		"-o", "json")

	output, err := cmd.Output()
	if err != nil {
		return 0, err
	}

	var dbs []struct {
		Sku struct {
			Name string `json:"name"`
			Tier string `json:"tier"`
		} `json:"sku"`
	}

	if err := json.Unmarshal(output, &dbs); err != nil {
		return 0, err
	}

	// Rough cost estimation (~$25/month per database)
	dayOfMonth := float64(time.Now().Day())
	return float64(len(dbs)) * 25.0 * (dayOfMonth / 30.0), nil
}

// getAKSCosts estimates AKS costs
func (p *AzureProvider) getAKSCosts(ctx context.Context) (float64, error) {
	cmd := exec.CommandContext(ctx, "az", "aks", "list",
		"--subscription", p.subscriptionID,
		"-o", "json")

	output, err := cmd.Output()
	if err != nil {
		return 0, err
	}

	var clusters []struct {
		AgentPoolProfiles []struct {
			Count int `json:"count"`
		} `json:"agentPoolProfiles"`
	}

	if err := json.Unmarshal(output, &clusters); err != nil {
		return 0, err
	}

	// Rough cost estimation (~$75/month per node)
	var totalNodes int
	for _, cluster := range clusters {
		for _, pool := range cluster.AgentPoolProfiles {
			totalNodes += pool.Count
		}
	}

	dayOfMonth := float64(time.Now().Day())
	return float64(totalNodes) * 75.0 * (dayOfMonth / 30.0), nil
}

// getStorageCosts estimates Storage costs
func (p *AzureProvider) getStorageCosts(ctx context.Context) (float64, error) {
	cmd := exec.CommandContext(ctx, "az", "storage", "account", "list",
		"--subscription", p.subscriptionID,
		"-o", "json")

	output, err := cmd.Output()
	if err != nil {
		return 0, err
	}

	var accounts []struct {
		Sku struct {
			Name string `json:"name"`
		} `json:"sku"`
	}

	if err := json.Unmarshal(output, &accounts); err != nil {
		return 0, err
	}

	// Rough cost estimation (~$5/month per storage account base)
	dayOfMonth := float64(time.Now().Day())
	return float64(len(accounts)) * 5.0 * (dayOfMonth / 30.0), nil
}

// getAppServiceCosts estimates App Service costs
func (p *AzureProvider) getAppServiceCosts(ctx context.Context) (float64, error) {
	cmd := exec.CommandContext(ctx, "az", "webapp", "list",
		"--subscription", p.subscriptionID,
		"-o", "json")

	output, err := cmd.Output()
	if err != nil {
		return 0, err
	}

	var apps []struct {
		Sku struct {
			Name string `json:"name"`
			Tier string `json:"tier"`
		} `json:"sku"`
	}

	if err := json.Unmarshal(output, &apps); err != nil {
		return 0, err
	}

	// Rough cost estimation (~$10/month per app)
	dayOfMonth := float64(time.Now().Day())
	return float64(len(apps)) * 10.0 * (dayOfMonth / 30.0), nil
}

// GetDailyTrend returns daily cost breakdown
func (p *AzureProvider) GetDailyTrend(ctx context.Context, start, end time.Time) ([]DailyCost, error) {
	if p.debug {
		log.Printf("[azure-cost] fetching daily trend from %s to %s", start.Format("2006-01-02"), end.Format("2006-01-02"))
	}

	queryBody := fmt.Sprintf(`{
		"type": "ActualCost",
		"timeframe": "Custom",
		"timePeriod": {
			"from": "%sT00:00:00Z",
			"to": "%sT23:59:59Z"
		},
		"dataset": {
			"granularity": "Daily",
			"aggregation": {
				"totalCost": {
					"name": "Cost",
					"function": "Sum"
				}
			}
		}
	}`, start.Format("2006-01-02"), end.Format("2006-01-02"))

	cmd := exec.CommandContext(ctx, "az", "rest",
		"--method", "POST",
		"--url", fmt.Sprintf("https://management.azure.com/subscriptions/%s/providers/Microsoft.CostManagement/query?api-version=2023-03-01", p.subscriptionID),
		"--body", queryBody)

	output, err := cmd.Output()
	if err != nil {
		// Fallback: distribute estimated total evenly
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
				Provider: "azure",
			})
		}
		return trend, nil
	}

	var result azureCostQueryResult
	if err := json.Unmarshal(output, &result); err != nil {
		return nil, fmt.Errorf("failed to parse Azure cost response: %w", err)
	}

	var trend []DailyCost
	for _, row := range result.Properties.Rows {
		if len(row) < 2 {
			continue
		}

		var cost float64
		switch v := row[0].(type) {
		case float64:
			cost = v
		case string:
			cost, _ = strconv.ParseFloat(v, 64)
		}

		// Date is in format like 20240215
		dateStr, ok := row[1].(string)
		if !ok {
			// Try as float
			if dateFloat, ok := row[1].(float64); ok {
				dateStr = fmt.Sprintf("%.0f", dateFloat)
			} else {
				continue
			}
		}

		date, err := time.Parse("20060102", dateStr)
		if err != nil {
			continue
		}

		trend = append(trend, DailyCost{
			Date:     date,
			Cost:     cost,
			Provider: "azure",
		})
	}

	// Sort by date
	sort.Slice(trend, func(i, j int) bool {
		return trend[i].Date.Before(trend[j].Date)
	})

	return trend, nil
}

// GetCostsByTag returns costs grouped by Azure tags
func (p *AzureProvider) GetCostsByTag(ctx context.Context, tagKey string, start, end time.Time) ([]TagCost, error) {
	queryBody := fmt.Sprintf(`{
		"type": "ActualCost",
		"timeframe": "Custom",
		"timePeriod": {
			"from": "%sT00:00:00Z",
			"to": "%sT23:59:59Z"
		},
		"dataset": {
			"granularity": "None",
			"aggregation": {
				"totalCost": {
					"name": "Cost",
					"function": "Sum"
				}
			},
			"grouping": [
				{
					"type": "Tag",
					"name": "%s"
				}
			]
		}
	}`, start.Format("2006-01-02"), end.Format("2006-01-02"), tagKey)

	cmd := exec.CommandContext(ctx, "az", "rest",
		"--method", "POST",
		"--url", fmt.Sprintf("https://management.azure.com/subscriptions/%s/providers/Microsoft.CostManagement/query?api-version=2023-03-01", p.subscriptionID),
		"--body", queryBody)

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("Azure Cost Management API failed: %w", err)
	}

	var result azureCostQueryResult
	if err := json.Unmarshal(output, &result); err != nil {
		return nil, fmt.Errorf("failed to parse Azure cost response: %w", err)
	}

	var tags []TagCost
	for _, row := range result.Properties.Rows {
		if len(row) < 2 {
			continue
		}

		var cost float64
		switch v := row[0].(type) {
		case float64:
			cost = v
		case string:
			cost, _ = strconv.ParseFloat(v, 64)
		}

		tagValue, _ := row[1].(string)

		tags = append(tags, TagCost{
			TagKey:   tagKey,
			TagValue: tagValue,
			Cost:     cost,
		})
	}

	// Sort by cost descending
	sort.Slice(tags, func(i, j int) bool {
		return tags[i].Cost > tags[j].Cost
	})

	return tags, nil
}
