package cost

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
)

// Colors for terminal output
const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorBlue   = "\033[34m"
	colorPurple = "\033[35m"
	colorCyan   = "\033[36m"
	colorWhite  = "\033[37m"
	colorBold   = "\033[1m"
)

// Formatter handles output formatting
type Formatter struct {
	format string
	color  bool
}

// NewFormatter creates a new formatter
func NewFormatter(format string, color bool) *Formatter {
	return &Formatter{
		format: format,
		color:  color,
	}
}

// FormatSummary formats the cost summary
func (f *Formatter) FormatSummary(summary *CostSummary) (string, error) {
	if f.format == "json" {
		return f.toJSON(summary)
	}

	var sb strings.Builder

	// Header
	sb.WriteString(f.header("Cost Summary"))
	sb.WriteString(fmt.Sprintf("Period: %s to %s\n",
		summary.Period.StartDate.Format("2006-01-02"),
		summary.Period.EndDate.Format("2006-01-02")))
	sb.WriteString("\n")

	// Total cost
	sb.WriteString(f.bold(fmt.Sprintf("Total Cost: %s%.2f %s%s\n",
		colorGreen, summary.TotalCost, summary.Currency, colorReset)))
	sb.WriteString("\n")

	// Forecast
	if summary.Forecast != nil {
		sb.WriteString(f.subheader("Forecast"))
		sb.WriteString(fmt.Sprintf("  End of Month: $%.2f\n", summary.Forecast.EstimatedEndOfMonth))
		sb.WriteString(fmt.Sprintf("  Next Month:   $%.2f\n", summary.Forecast.EstimatedNextMonth))
		sb.WriteString(fmt.Sprintf("  Confidence:   %.1f%%\n", summary.Forecast.Confidence*100))
		sb.WriteString("\n")
	}

	// Provider breakdown
	sb.WriteString(f.subheader("By Provider"))
	w := tabwriter.NewWriter(&sb, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PROVIDER\tCOST\tCHANGE")
	fmt.Fprintln(w, "--------\t----\t------")
	for _, pc := range summary.ProviderCosts {
		change := f.formatChange(pc.Change)
		fmt.Fprintf(w, "%s\t$%.2f\t%s\n", strings.ToUpper(pc.Provider), pc.TotalCost, change)
	}
	w.Flush()
	sb.WriteString("\n")

	// Top services
	if len(summary.TopServices) > 0 {
		sb.WriteString(f.subheader("Top Services"))
		w = tabwriter.NewWriter(&sb, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "SERVICE\tCOST\tRESOURCES")
		fmt.Fprintln(w, "-------\t----\t---------")
		for i, svc := range summary.TopServices {
			if i >= 10 {
				break
			}
			fmt.Fprintf(w, "%s\t$%.2f\t%d\n", svc.Service, svc.Cost, svc.ResourceCount)
		}
		w.Flush()
	}

	return sb.String(), nil
}

// FormatProviderCost formats provider cost details
func (f *Formatter) FormatProviderCost(cost *ProviderCost) (string, error) {
	if f.format == "json" {
		return f.toJSON(cost)
	}

	var sb strings.Builder

	sb.WriteString(f.header(fmt.Sprintf("%s Costs", strings.ToUpper(cost.Provider))))
	sb.WriteString(f.bold(fmt.Sprintf("Total: $%.2f %s\n", cost.TotalCost, cost.Currency)))
	change := f.formatChange(cost.Change)
	sb.WriteString(fmt.Sprintf("Change from last period: %s\n", change))
	sb.WriteString("\n")

	// Service breakdown
	sb.WriteString(f.subheader("Service Breakdown"))
	w := tabwriter.NewWriter(&sb, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SERVICE\tCOST\tRESOURCES")
	fmt.Fprintln(w, "-------\t----\t---------")
	for _, svc := range cost.ServiceBreakdown {
		fmt.Fprintf(w, "%s\t$%.2f\t%d\n", svc.Service, svc.Cost, svc.ResourceCount)
	}
	w.Flush()

	return sb.String(), nil
}

// FormatServices formats service costs
func (f *Formatter) FormatServices(services []ServiceCost, top int) (string, error) {
	if f.format == "json" {
		return f.toJSON(services)
	}

	var sb strings.Builder

	sb.WriteString(f.header("Cost by Service"))

	w := tabwriter.NewWriter(&sb, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SERVICE\tCOST\tUSAGE\tRESOURCES")
	fmt.Fprintln(w, "-------\t----\t-----\t---------")
	for i, svc := range services {
		if top > 0 && i >= top {
			break
		}
		usage := ""
		if svc.UsageQuantity > 0 && svc.UsageUnit != "" {
			usage = fmt.Sprintf("%.2f %s", svc.UsageQuantity, svc.UsageUnit)
		}
		fmt.Fprintf(w, "%s\t$%.2f\t%s\t%d\n", svc.Service, svc.Cost, usage, svc.ResourceCount)
	}
	w.Flush()

	return sb.String(), nil
}

// FormatTrend formats cost trend data
func (f *Formatter) FormatTrend(trend *CostTrendResponse) (string, error) {
	if f.format == "json" {
		return f.toJSON(trend)
	}

	var sb strings.Builder

	sb.WriteString(f.header("Cost Trend"))
	sb.WriteString(fmt.Sprintf("Period: %s to %s\n",
		trend.Period.StartDate.Format("2006-01-02"),
		trend.Period.EndDate.Format("2006-01-02")))
	sb.WriteString("\n")

	w := tabwriter.NewWriter(&sb, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "DATE\tCOST\tPROVIDER")
	fmt.Fprintln(w, "----\t----\t--------")
	for _, dc := range trend.DailyCosts {
		provider := dc.Provider
		if provider == "" {
			provider = "all"
		}
		fmt.Fprintf(w, "%s\t$%.2f\t%s\n", dc.Date.Format("2006-01-02"), dc.Cost, provider)
	}
	w.Flush()

	return sb.String(), nil
}

// FormatForecast formats forecast data
func (f *Formatter) FormatForecast(forecast *CostForecast) (string, error) {
	if f.format == "json" {
		return f.toJSON(forecast)
	}

	var sb strings.Builder

	sb.WriteString(f.header("Cost Forecast"))
	sb.WriteString("\n")
	sb.WriteString(fmt.Sprintf("  End of Month Estimate:  %s$%.2f%s\n", colorGreen, forecast.EstimatedEndOfMonth, colorReset))
	sb.WriteString(fmt.Sprintf("  Next Month Estimate:    %s$%.2f%s\n", colorCyan, forecast.EstimatedNextMonth, colorReset))
	sb.WriteString(fmt.Sprintf("  Confidence Level:       %.1f%%\n", forecast.Confidence*100))

	return sb.String(), nil
}

// FormatAnomalies formats cost anomalies
func (f *Formatter) FormatAnomalies(anomalies *CostAnomaliesResponse) (string, error) {
	if f.format == "json" {
		return f.toJSON(anomalies)
	}

	var sb strings.Builder

	sb.WriteString(f.header("Cost Anomalies"))

	if len(anomalies.Anomalies) == 0 {
		sb.WriteString(fmt.Sprintf("%sNo cost anomalies detected%s\n", colorGreen, colorReset))
		return sb.String(), nil
	}

	sb.WriteString(fmt.Sprintf("Found %d anomalies:\n\n", len(anomalies.Anomalies)))

	for i, a := range anomalies.Anomalies {
		color := colorRed
		if a.PercentChange < 0 {
			color = colorGreen
		}

		sb.WriteString(fmt.Sprintf("%d. %s%s%s (%s)\n", i+1, colorBold, a.Service, colorReset, a.Provider))
		sb.WriteString(fmt.Sprintf("   Expected: $%.2f  |  Actual: $%.2f  |  Change: %s%+.1f%%%s\n",
			a.ExpectedCost, a.ActualCost, color, a.PercentChange, colorReset))
		sb.WriteString(fmt.Sprintf("   %s\n\n", a.Description))
	}

	return sb.String(), nil
}

// FormatLLMUsage formats LLM usage data
func (f *Formatter) FormatLLMUsage(usage *LLMCostSummary) (string, error) {
	if f.format == "json" {
		return f.toJSON(usage)
	}

	var sb strings.Builder

	sb.WriteString(f.header("LLM Usage"))

	// Summary stats
	sb.WriteString(f.subheader("Summary"))
	sb.WriteString(fmt.Sprintf("  Total Cost:     %s$%.4f%s\n", colorGreen, usage.TotalCost, colorReset))
	sb.WriteString(fmt.Sprintf("  Total Tokens:   %s\n", f.formatTokens(usage.TotalTokens)))
	sb.WriteString(fmt.Sprintf("  Total Requests: %d\n", usage.TotalRequests))
	sb.WriteString("\n")

	// By provider
	if len(usage.ByProvider) > 0 {
		sb.WriteString(f.subheader("By Provider"))
		w := tabwriter.NewWriter(&sb, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "PROVIDER\tTOKENS\tREQUESTS\tCOST")
		fmt.Fprintln(w, "--------\t------\t--------\t----")
		for _, p := range usage.ByProvider {
			fmt.Fprintf(w, "%s\t%s\t%d\t$%.4f\n",
				p.Provider, f.formatTokens(p.TotalTokens), p.RequestCount, p.EstimatedCost)
		}
		w.Flush()
		sb.WriteString("\n")
	}

	// By model
	if len(usage.ByModel) > 0 {
		sb.WriteString(f.subheader("By Model"))
		w := tabwriter.NewWriter(&sb, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "MODEL\tINPUT\tOUTPUT\tCOST")
		fmt.Fprintln(w, "-----\t-----\t------\t----")
		for _, m := range usage.ByModel {
			model := m.Model
			if m.Provider != "" {
				model = fmt.Sprintf("%s/%s", m.Provider, m.Model)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t$%.4f\n",
				model, f.formatTokens(m.InputTokens), f.formatTokens(m.OutputTokens), m.EstimatedCost)
		}
		w.Flush()
	}

	return sb.String(), nil
}

// FormatTags formats tag cost data
func (f *Formatter) FormatTags(tags *TagsResponse) (string, error) {
	if f.format == "json" {
		return f.toJSON(tags)
	}

	var sb strings.Builder

	sb.WriteString(f.header("Cost by Tags"))

	if len(tags.Tags) == 0 {
		sb.WriteString("No tag data available\n")
		return sb.String(), nil
	}

	w := tabwriter.NewWriter(&sb, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "TAG KEY\tTAG VALUE\tCOST\tRESOURCES")
	fmt.Fprintln(w, "-------\t---------\t----\t---------")
	for _, t := range tags.Tags {
		fmt.Fprintf(w, "%s\t%s\t$%.2f\t%d\n", t.TagKey, t.TagValue, t.Cost, t.ResourceCount)
	}
	w.Flush()

	return sb.String(), nil
}

// Helper methods

func (f *Formatter) header(text string) string {
	if f.color {
		return fmt.Sprintf("\n%s%s=== %s ===%s\n\n", colorBold, colorCyan, text, colorReset)
	}
	return fmt.Sprintf("\n=== %s ===\n\n", text)
}

func (f *Formatter) subheader(text string) string {
	if f.color {
		return fmt.Sprintf("%s%s%s%s\n", colorBold, colorYellow, text, colorReset)
	}
	return fmt.Sprintf("%s\n", text)
}

func (f *Formatter) bold(text string) string {
	if f.color {
		return fmt.Sprintf("%s%s%s", colorBold, text, colorReset)
	}
	return text
}

func (f *Formatter) formatChange(change float64) string {
	if change == 0 {
		return "0%"
	}

	sign := "+"
	color := colorRed
	if change < 0 {
		sign = ""
		color = colorGreen
	}

	if f.color {
		return fmt.Sprintf("%s%s%.1f%%%s", color, sign, change, colorReset)
	}
	return fmt.Sprintf("%s%.1f%%", sign, change)
}

func (f *Formatter) formatTokens(tokens int64) string {
	if tokens >= 1000000 {
		return fmt.Sprintf("%.2fM", float64(tokens)/1000000)
	}
	if tokens >= 1000 {
		return fmt.Sprintf("%.1fK", float64(tokens)/1000)
	}
	return fmt.Sprintf("%d", tokens)
}

func (f *Formatter) toJSON(v interface{}) (string, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal to JSON: %w", err)
	}
	return string(data), nil
}

// Print outputs to stdout
func (f *Formatter) Print(output string) {
	fmt.Fprint(os.Stdout, output)
}
