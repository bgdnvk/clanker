package cost

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
)

// AWSProvider implements Provider for AWS Cost Explorer
type AWSProvider struct {
	client  *costexplorer.Client
	profile string
	debug   bool
}

// NewAWSProvider creates a new AWS cost provider
func NewAWSProvider(ctx context.Context, profile string, debug bool) (*AWSProvider, error) {
	var cfg aws.Config
	var err error

	if profile != "" {
		cfg, err = config.LoadDefaultConfig(ctx, config.WithSharedConfigProfile(profile))
	} else {
		cfg, err = config.LoadDefaultConfig(ctx)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	return &AWSProvider{
		client:  costexplorer.NewFromConfig(cfg),
		profile: profile,
		debug:   debug,
	}, nil
}

// GetName returns the provider identifier
func (p *AWSProvider) GetName() string {
	return "aws"
}

// IsConfigured checks if AWS credentials are available
func (p *AWSProvider) IsConfigured() bool {
	return p.client != nil
}

// GetCosts returns total AWS costs for the given time period
func (p *AWSProvider) GetCosts(ctx context.Context, start, end time.Time) (*ProviderCost, error) {
	startStr := start.Format("2006-01-02")
	endStr := end.Format("2006-01-02")

	if p.debug {
		log.Printf("[aws-cost] fetching costs from %s to %s", startStr, endStr)
	}

	input := &costexplorer.GetCostAndUsageInput{
		TimePeriod: &types.DateInterval{
			Start: aws.String(startStr),
			End:   aws.String(endStr),
		},
		Granularity: types.GranularityMonthly,
		Metrics:     []string{"UnblendedCost"},
		GroupBy: []types.GroupDefinition{
			{
				Type: types.GroupDefinitionTypeDimension,
				Key:  aws.String("SERVICE"),
			},
		},
	}

	result, err := p.client.GetCostAndUsage(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("failed to get AWS costs: %w", err)
	}

	var totalCost float64
	var services []ServiceCost

	for _, period := range result.ResultsByTime {
		for _, group := range period.Groups {
			if len(group.Keys) == 0 {
				continue
			}
			serviceName := group.Keys[0]
			costAmount := 0.0
			if cost, ok := group.Metrics["UnblendedCost"]; ok && cost.Amount != nil {
				costAmount, _ = strconv.ParseFloat(*cost.Amount, 64)
			}
			totalCost += costAmount
			services = append(services, ServiceCost{
				Service: serviceName,
				Cost:    costAmount,
			})
		}
	}

	return &ProviderCost{
		Provider:         "aws",
		TotalCost:        totalCost,
		Currency:         "USD",
		ServiceBreakdown: services,
	}, nil
}

// GetCostsByService returns costs broken down by AWS service
func (p *AWSProvider) GetCostsByService(ctx context.Context, start, end time.Time) ([]ServiceCost, error) {
	costs, err := p.GetCosts(ctx, start, end)
	if err != nil {
		return nil, err
	}
	return costs.ServiceBreakdown, nil
}

// GetDailyTrend returns daily cost breakdown
func (p *AWSProvider) GetDailyTrend(ctx context.Context, start, end time.Time) ([]DailyCost, error) {
	startStr := start.Format("2006-01-02")
	endStr := end.Format("2006-01-02")

	if p.debug {
		log.Printf("[aws-cost] fetching daily trend from %s to %s", startStr, endStr)
	}

	input := &costexplorer.GetCostAndUsageInput{
		TimePeriod: &types.DateInterval{
			Start: aws.String(startStr),
			End:   aws.String(endStr),
		},
		Granularity: types.GranularityDaily,
		Metrics:     []string{"UnblendedCost"},
	}

	result, err := p.client.GetCostAndUsage(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("failed to get AWS daily costs: %w", err)
	}

	var trend []DailyCost
	for _, period := range result.ResultsByTime {
		if period.TimePeriod == nil || period.TimePeriod.Start == nil {
			continue
		}
		date, err := time.Parse("2006-01-02", *period.TimePeriod.Start)
		if err != nil {
			continue
		}

		costAmount := 0.0
		if cost, ok := period.Total["UnblendedCost"]; ok && cost.Amount != nil {
			costAmount, _ = strconv.ParseFloat(*cost.Amount, 64)
		}

		trend = append(trend, DailyCost{
			Date:     date,
			Cost:     costAmount,
			Provider: "aws",
		})
	}

	return trend, nil
}

// GetForecast returns AWS cost forecast
func (p *AWSProvider) GetForecast(ctx context.Context) (*CostForecast, error) {
	now := time.Now()
	startOfMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	endOfMonth := startOfMonth.AddDate(0, 1, 0)
	endOfNextMonth := endOfMonth.AddDate(0, 1, 0)

	// Get forecast for end of current month
	eomInput := &costexplorer.GetCostForecastInput{
		TimePeriod: &types.DateInterval{
			Start: aws.String(now.AddDate(0, 0, 1).Format("2006-01-02")),
			End:   aws.String(endOfMonth.Format("2006-01-02")),
		},
		Metric:      types.MetricUnblendedCost,
		Granularity: types.GranularityMonthly,
	}

	eomResult, err := p.client.GetCostForecast(ctx, eomInput)
	if err != nil {
		return nil, fmt.Errorf("failed to get AWS forecast: %w", err)
	}

	// Get current MTD cost
	mtdInput := &costexplorer.GetCostAndUsageInput{
		TimePeriod: &types.DateInterval{
			Start: aws.String(startOfMonth.Format("2006-01-02")),
			End:   aws.String(now.Format("2006-01-02")),
		},
		Granularity: types.GranularityMonthly,
		Metrics:     []string{"UnblendedCost"},
	}

	mtdResult, err := p.client.GetCostAndUsage(ctx, mtdInput)
	if err != nil {
		return nil, fmt.Errorf("failed to get AWS MTD cost: %w", err)
	}

	var mtdCost float64
	for _, period := range mtdResult.ResultsByTime {
		if cost, ok := period.Total["UnblendedCost"]; ok && cost.Amount != nil {
			mtdCost, _ = strconv.ParseFloat(*cost.Amount, 64)
		}
	}

	var eomForecast float64
	if eomResult.Total != nil && eomResult.Total.Amount != nil {
		eomForecast, _ = strconv.ParseFloat(*eomResult.Total.Amount, 64)
	}

	// Get forecast for next month
	nextMonthInput := &costexplorer.GetCostForecastInput{
		TimePeriod: &types.DateInterval{
			Start: aws.String(endOfMonth.Format("2006-01-02")),
			End:   aws.String(endOfNextMonth.Format("2006-01-02")),
		},
		Metric:      types.MetricUnblendedCost,
		Granularity: types.GranularityMonthly,
	}

	nextMonthResult, err := p.client.GetCostForecast(ctx, nextMonthInput)
	var nextMonthForecast float64
	if err == nil && nextMonthResult.Total != nil && nextMonthResult.Total.Amount != nil {
		nextMonthForecast, _ = strconv.ParseFloat(*nextMonthResult.Total.Amount, 64)
	}

	return &CostForecast{
		EstimatedEndOfMonth: mtdCost + eomForecast,
		EstimatedNextMonth:  nextMonthForecast,
		Confidence:          80.0, // AWS doesn't provide confidence, default to 80%
	}, nil
}

// GetAnomalies returns detected cost anomalies
func (p *AWSProvider) GetAnomalies(ctx context.Context) ([]CostAnomaly, error) {
	// Get last 7 days of daily costs to detect anomalies
	now := time.Now()
	end := now
	start := now.AddDate(0, 0, -7)

	dailyCosts, err := p.GetDailyTrend(ctx, start, end)
	if err != nil {
		return nil, err
	}

	// Calculate average and detect anomalies (>30% deviation)
	if len(dailyCosts) < 2 {
		return nil, nil
	}

	var total float64
	for _, dc := range dailyCosts[:len(dailyCosts)-1] {
		total += dc.Cost
	}
	avg := total / float64(len(dailyCosts)-1)

	var anomalies []CostAnomaly
	today := dailyCosts[len(dailyCosts)-1]
	if avg > 0 {
		change := ((today.Cost - avg) / avg) * 100
		if change > 30 || change < -30 {
			anomalies = append(anomalies, CostAnomaly{
				Service:       "Total AWS",
				Provider:      "aws",
				ExpectedCost:  avg,
				ActualCost:    today.Cost,
				PercentChange: change,
				Description:   fmt.Sprintf("Daily cost deviation of %.1f%% from 7-day average", change),
			})
		}
	}

	return anomalies, nil
}

// GetCostsByTag returns costs grouped by tag
func (p *AWSProvider) GetCostsByTag(ctx context.Context, tagKey string, start, end time.Time) ([]TagCost, error) {
	startStr := start.Format("2006-01-02")
	endStr := end.Format("2006-01-02")

	input := &costexplorer.GetCostAndUsageInput{
		TimePeriod: &types.DateInterval{
			Start: aws.String(startStr),
			End:   aws.String(endStr),
		},
		Granularity: types.GranularityMonthly,
		Metrics:     []string{"UnblendedCost"},
		GroupBy: []types.GroupDefinition{
			{
				Type: types.GroupDefinitionTypeTag,
				Key:  aws.String(tagKey),
			},
		},
	}

	result, err := p.client.GetCostAndUsage(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("failed to get AWS costs by tag: %w", err)
	}

	var tags []TagCost
	for _, period := range result.ResultsByTime {
		for _, group := range period.Groups {
			if len(group.Keys) == 0 {
				continue
			}
			tagValue := group.Keys[0]
			costAmount := 0.0
			if cost, ok := group.Metrics["UnblendedCost"]; ok && cost.Amount != nil {
				costAmount, _ = strconv.ParseFloat(*cost.Amount, 64)
			}
			tags = append(tags, TagCost{
				TagKey:   tagKey,
				TagValue: tagValue,
				Cost:     costAmount,
			})
		}
	}

	return tags, nil
}
