package cost

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Exporter handles cost data export
type Exporter struct{}

// NewExporter creates a new exporter
func NewExporter() *Exporter {
	return &Exporter{}
}

// ExportToFile exports cost data to a file
func (e *Exporter) ExportToFile(data interface{}, format, outputPath string) error {
	// Ensure output directory exists
	dir := filepath.Dir(outputPath)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create output directory: %w", err)
		}
	}

	// Generate output based on format
	var content []byte
	var err error

	switch format {
	case "json":
		content, err = e.toJSON(data)
	case "csv":
		content, err = e.toCSV(data)
	default:
		return fmt.Errorf("unsupported format: %s", format)
	}

	if err != nil {
		return err
	}

	// Write to file
	if err := os.WriteFile(outputPath, content, 0644); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	return nil
}

// toJSON converts data to JSON format
func (e *Exporter) toJSON(data interface{}) ([]byte, error) {
	return json.MarshalIndent(data, "", "  ")
}

// toCSV converts data to CSV format
func (e *Exporter) toCSV(data interface{}) ([]byte, error) {
	switch v := data.(type) {
	case *CostSummary:
		return e.summaryToCSV(v)
	case *ProviderCost:
		return e.providerToCSV(v)
	case []ServiceCost:
		return e.servicesToCSV(v)
	case *CostTrendResponse:
		return e.trendToCSV(v)
	case *CostAnomaliesResponse:
		return e.anomaliesToCSV(v)
	case *LLMCostSummary:
		return e.llmUsageToCSV(v)
	case *TagsResponse:
		return e.tagsToCSV(v)
	default:
		return nil, fmt.Errorf("unsupported data type for CSV export")
	}
}

func (e *Exporter) summaryToCSV(summary *CostSummary) ([]byte, error) {
	var sb strings.Builder
	w := csv.NewWriter(&sb)

	// Header
	w.Write([]string{"Provider", "Total Cost", "Currency", "Change %"})

	// Provider data
	for _, pc := range summary.ProviderCosts {
		w.Write([]string{
			pc.Provider,
			fmt.Sprintf("%.2f", pc.TotalCost),
			pc.Currency,
			fmt.Sprintf("%.2f", pc.Change),
		})
	}

	// Add totals row
	w.Write([]string{
		"TOTAL",
		fmt.Sprintf("%.2f", summary.TotalCost),
		summary.Currency,
		"",
	})

	w.Flush()
	if err := w.Error(); err != nil {
		return nil, err
	}

	return []byte(sb.String()), nil
}

func (e *Exporter) providerToCSV(cost *ProviderCost) ([]byte, error) {
	var sb strings.Builder
	w := csv.NewWriter(&sb)

	// Header
	w.Write([]string{"Service", "Cost", "Resource Count"})

	// Service data
	for _, svc := range cost.ServiceBreakdown {
		w.Write([]string{
			svc.Service,
			fmt.Sprintf("%.2f", svc.Cost),
			fmt.Sprintf("%d", svc.ResourceCount),
		})
	}

	w.Flush()
	if err := w.Error(); err != nil {
		return nil, err
	}

	return []byte(sb.String()), nil
}

func (e *Exporter) servicesToCSV(services []ServiceCost) ([]byte, error) {
	var sb strings.Builder
	w := csv.NewWriter(&sb)

	// Header
	w.Write([]string{"Service", "Cost", "Usage Quantity", "Usage Unit", "Resource Count"})

	// Service data
	for _, svc := range services {
		w.Write([]string{
			svc.Service,
			fmt.Sprintf("%.2f", svc.Cost),
			fmt.Sprintf("%.2f", svc.UsageQuantity),
			svc.UsageUnit,
			fmt.Sprintf("%d", svc.ResourceCount),
		})
	}

	w.Flush()
	if err := w.Error(); err != nil {
		return nil, err
	}

	return []byte(sb.String()), nil
}

func (e *Exporter) trendToCSV(trend *CostTrendResponse) ([]byte, error) {
	var sb strings.Builder
	w := csv.NewWriter(&sb)

	// Header
	w.Write([]string{"Date", "Cost", "Provider"})

	// Daily data
	for _, dc := range trend.Trend {
		provider := dc.Provider
		if provider == "" {
			provider = "all"
		}
		w.Write([]string{
			dc.Date.Format("2006-01-02"),
			fmt.Sprintf("%.2f", dc.Cost),
			provider,
		})
	}

	w.Flush()
	if err := w.Error(); err != nil {
		return nil, err
	}

	return []byte(sb.String()), nil
}

func (e *Exporter) anomaliesToCSV(anomalies *CostAnomaliesResponse) ([]byte, error) {
	var sb strings.Builder
	w := csv.NewWriter(&sb)

	// Header
	w.Write([]string{"Service", "Provider", "Expected Cost", "Actual Cost", "Change %", "Description"})

	// Anomaly data
	for _, a := range anomalies.Anomalies {
		w.Write([]string{
			a.Service,
			a.Provider,
			fmt.Sprintf("%.2f", a.ExpectedCost),
			fmt.Sprintf("%.2f", a.ActualCost),
			fmt.Sprintf("%.2f", a.PercentChange),
			a.Description,
		})
	}

	w.Flush()
	if err := w.Error(); err != nil {
		return nil, err
	}

	return []byte(sb.String()), nil
}

func (e *Exporter) llmUsageToCSV(usage *LLMCostSummary) ([]byte, error) {
	var sb strings.Builder
	w := csv.NewWriter(&sb)

	// Header
	w.Write([]string{"Provider", "Model", "Input Tokens", "Output Tokens", "Total Tokens", "Requests", "Cost"})

	// By model data
	for _, m := range usage.ByModel {
		w.Write([]string{
			m.Provider,
			m.Model,
			fmt.Sprintf("%d", m.InputTokens),
			fmt.Sprintf("%d", m.OutputTokens),
			fmt.Sprintf("%d", m.TotalTokens),
			fmt.Sprintf("%d", m.RequestCount),
			fmt.Sprintf("%.4f", m.EstimatedCost),
		})
	}

	// Totals row
	w.Write([]string{
		"TOTAL",
		"",
		"",
		"",
		fmt.Sprintf("%d", usage.TotalTokens),
		fmt.Sprintf("%d", usage.TotalRequests),
		fmt.Sprintf("%.4f", usage.TotalCost),
	})

	w.Flush()
	if err := w.Error(); err != nil {
		return nil, err
	}

	return []byte(sb.String()), nil
}

func (e *Exporter) tagsToCSV(tags *TagsResponse) ([]byte, error) {
	var sb strings.Builder
	w := csv.NewWriter(&sb)

	// Header
	w.Write([]string{"Tag Key", "Tag Value", "Cost", "Resource Count"})

	// Tag data
	for _, t := range tags.Tags {
		w.Write([]string{
			t.TagKey,
			t.TagValue,
			fmt.Sprintf("%.2f", t.Cost),
			fmt.Sprintf("%d", t.ResourceCount),
		})
	}

	w.Flush()
	if err := w.Error(); err != nil {
		return nil, err
	}

	return []byte(sb.String()), nil
}

// GenerateFilename generates a filename for export
func (e *Exporter) GenerateFilename(prefix, format string) string {
	timestamp := time.Now().Format("20060102-150405")
	return fmt.Sprintf("%s-%s.%s", prefix, timestamp, format)
}
