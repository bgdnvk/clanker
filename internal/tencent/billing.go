package tencent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	billing "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/billing/v20180709"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
)

// BillByProductItem is one row of the cost-by-product breakdown.
// Mirrors the JSON shape consumed by the clanker-cloud Tencent cost
// provider — every monetary value is a string-encoded decimal because
// Tencent's billing API returns them that way (the SDK doesn't parse
// them and we don't want to lose precision in a float round-trip).
type BillByProductItem struct {
	Product   string `json:"product"`
	RealCost  string `json:"real_cost"`
	Cash      string `json:"cash"`
	Incentive string `json:"incentive"`
	Voucher   string `json:"voucher"`
	Pct       string `json:"pct"`
}

// BillByProductReport is the top-level JSON envelope. Total is the
// sum of RealCost across every product, computed client-side so the
// downstream consumer doesn't have to re-tokenise the strings.
type BillByProductReport struct {
	Month string              `json:"month"`
	Items []BillByProductItem `json:"items"`
	Total float64             `json:"total"`
}

// listBillByProduct prints a per-service cost breakdown for a given month.
// Tencent's billing API treats month as both BeginTime and EndTime so we
// always pass the same value.
//
// Format dispatch: when format="json" the report is emitted as a JSON
// object; otherwise the historical tabwriter table is printed.
func listBillByProduct(c *Client, month string, format string) error {
	report, err := buildBillByProductReport(c, month)
	if err != nil {
		return err
	}
	if strings.EqualFold(strings.TrimSpace(format), "json") {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}
	return writeBillByProductTable(report)
}

func buildBillByProductReport(c *Client, month string) (BillByProductReport, error) {
	if month == "" {
		month = time.Now().Format("2006-01")
	}
	report := BillByProductReport{Month: month}
	client, err := newBillingClient(c)
	if err != nil {
		return report, fmt.Errorf("init billing client: %w", err)
	}
	req := billing.NewDescribeBillSummaryByProductRequest()
	req.BeginTime = &month
	req.EndTime = &month
	resp, err := client.DescribeBillSummaryByProduct(req)
	if err != nil {
		return report, fmt.Errorf("DescribeBillSummaryByProduct: %w", friendlyError(err))
	}
	if resp == nil || resp.Response == nil || resp.Response.SummaryOverview == nil {
		return report, nil
	}
	for _, it := range resp.Response.SummaryOverview {
		row := BillByProductItem{
			Product:   derefString(it.BusinessCodeName),
			RealCost:  derefString(it.RealTotalCost),
			Cash:      derefString(it.CashPayAmount),
			Incentive: derefString(it.IncentivePayAmount),
			Voucher:   derefString(it.VoucherPayAmount),
			Pct:       derefString(it.RealTotalCostRatio),
		}
		report.Items = append(report.Items, row)
		if v, err := strconv.ParseFloat(row.RealCost, 64); err == nil {
			report.Total += v
		}
	}
	return report, nil
}

func writeBillByProductTable(report BillByProductReport) error {
	fmt.Printf("Tencent Cloud Cost by Product — %s:\n\n", report.Month)
	if len(report.Items) == 0 {
		fmt.Println("  No billing data for this month")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PRODUCT\tREAL_COST\tCASH\tINCENTIVE\tVOUCHER\tPCT")
	for _, it := range report.Items {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s%%\n",
			it.Product, it.RealCost, it.Cash, it.Incentive, it.Voucher, it.Pct)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Printf("\nTotal: %.4f\n", report.Total)
	return nil
}

// BillTopResource is one row of the per-resource top-N report.
type BillTopResource struct {
	Product    string `json:"product"`
	ResourceID string `json:"resource_id"`
	Name       string `json:"name"`
	Region     string `json:"region"`
	PayMode    string `json:"pay_mode"`
	Action     string `json:"action"`
	Cost       string `json:"cost"`
}

// BillTopResourceReport is the JSON envelope for `cost top --format json`.
type BillTopResourceReport struct {
	Month string            `json:"month"`
	Top   int               `json:"top"`
	Items []BillTopResource `json:"items"`
}

// listBillResourceTop prints (or emits as JSON) the most expensive
// resources for the month.
func listBillResourceTop(c *Client, month string, top int, format string) error {
	report, err := buildBillTopReport(c, month, top)
	if err != nil {
		return err
	}
	if strings.EqualFold(strings.TrimSpace(format), "json") {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}
	return writeBillTopTable(report)
}

func buildBillTopReport(c *Client, month string, top int) (BillTopResourceReport, error) {
	if month == "" {
		month = time.Now().Format("2006-01")
	}
	if top <= 0 {
		top = 20
	}
	report := BillTopResourceReport{Month: month, Top: top}
	client, err := newBillingClient(c)
	if err != nil {
		return report, fmt.Errorf("init billing client: %w", err)
	}
	req := billing.NewDescribeBillResourceSummaryRequest()
	var offset uint64 = 0
	limit := uint64(top)
	if limit > 1000 {
		limit = 1000
	}
	req.Offset = &offset
	req.Limit = &limit
	req.Month = &month
	period := "byUsedTime"
	req.PeriodType = &period
	resp, err := client.DescribeBillResourceSummary(req)
	if err != nil {
		return report, fmt.Errorf("DescribeBillResourceSummary: %w", friendlyError(err))
	}
	if resp == nil || resp.Response == nil {
		return report, nil
	}
	for _, r := range resp.Response.ResourceSummarySet {
		report.Items = append(report.Items, BillTopResource{
			Product:    derefString(r.BusinessCodeName),
			ResourceID: derefString(r.ResourceId),
			Name:       derefString(r.ResourceName),
			Region:     derefString(r.RegionName),
			PayMode:    derefString(r.PayModeName),
			Action:     derefString(r.ActionTypeName),
			Cost:       derefString(r.RealTotalCost),
		})
	}
	return report, nil
}

func writeBillTopTable(report BillTopResourceReport) error {
	fmt.Printf("Top %d Resources by Cost — %s:\n\n", report.Top, report.Month)
	if len(report.Items) == 0 {
		fmt.Println("  No resource-level billing data for this month")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PRODUCT\tRESOURCE_ID\tNAME\tREGION\tPAY_MODE\tACTION\tCOST")
	for _, r := range report.Items {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			r.Product, r.ResourceID, r.Name, r.Region, r.PayMode, r.Action, r.Cost)
	}
	return tw.Flush()
}

func newBillingClient(c *Client) (*billing.Client, error) {
	cred := common.NewCredential(c.creds.SecretID, c.creds.SecretKey)
	cpf := newClientProfile("billing.tencentcloudapi.com")
	return billing.NewClient(cred, "ap-guangzhou", cpf) // billing is global
}

// billFeeBreakdown is the fee-type decomposition of a month's bill. It comes
// from DescribeCostExplorerSummary — the only Tencent billing API that
// separates out tax. DescribeBillSummaryByProduct (used for the per-product
// list) returns RealCost but no tax field, which is why a Clanker cost
// total never matched the console's tax-inclusive headline.
//
// Reconciliation (verified against a real April bill):
//
//	consumption = voucher + cash_before_tax + tax
//	cash_incl_tax = cash_before_tax + tax   ← the console's headline "Total Cost"
type billFeeBreakdown struct {
	Consumption   float64 `json:"consumption"`     // total RealCost: voucher + cash + tax
	Voucher       float64 `json:"voucher"`         // amount covered by vouchers
	CashBeforeTax float64 `json:"cash_before_tax"` // cash portion, pre-tax
	Tax           float64 `json:"tax"`             // tax amount
	CashInclTax   float64 `json:"cash_incl_tax"`   // cash_before_tax + tax (out-of-pocket)
	Note          string  `json:"note,omitempty"`  // set when the breakdown call failed
}

// monthDateRange turns "2026-04" into the [begin, end] datetime strings
// DescribeCostExplorerSummary expects (yyyy-mm-dd hh:ii:ss). Begin is the
// first day at 00:00:00, end is the last day at 23:59:59.
func monthDateRange(month string) (string, string) {
	t, err := time.Parse("2006-01", strings.TrimSpace(month))
	if err != nil {
		now := time.Now()
		t = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	}
	begin := t.Format("2006-01-02") + " 00:00:00"
	end := t.AddDate(0, 1, -1).Format("2006-01-02") + " 23:59:59"
	return begin, end
}

// billFeeSummary pulls the voucher / cash-before-tax / tax decomposition for
// a month via DescribeCostExplorerSummary (Dimensions=feeType, FeeType=cost).
//
// The Detail item names are localized display strings — for an English
// account they are "Voucher", "Tax Amount", "Total Amount After Discount
// (Excluding Tax)". We match defensively on lowercased substrings so a
// locale change doesn't silently break the mapping.
func billFeeSummary(client *billing.Client, month string) (billFeeBreakdown, error) {
	var out billFeeBreakdown
	begin, end := monthDateRange(month)

	req := billing.NewDescribeCostExplorerSummaryRequest()
	billType, periodType, dim, feeType := "1", "month", "feeType", "cost"
	req.BeginTime, req.EndTime = &begin, &end
	req.BillType, req.PeriodType = &billType, &periodType
	req.Dimensions, req.FeeType = &dim, &feeType
	var pageSize, pageNo uint64 = 100, 1
	req.PageSize, req.PageNo = &pageSize, &pageNo

	resp, err := client.DescribeCostExplorerSummary(req)
	if err != nil {
		return out, friendlyError(err)
	}
	if resp == nil || resp.Response == nil {
		return out, nil
	}
	if td := resp.Response.TotalDetail; td != nil {
		out.Consumption = parseFloat(derefString(td.Total))
	}
	for _, d := range resp.Response.Detail {
		if d == nil {
			continue
		}
		name := strings.ToLower(derefStringRaw(d.Name))
		val := parseFloat(derefString(d.Total))
		// Order matters: the cash line is literally "Total Amount After
		// Discount (Excluding Tax)" — it contains the word "tax", so the
		// discount check MUST come before the tax check or the cash line
		// gets misclassified as tax.
		switch {
		case strings.Contains(name, "discount"):
			out.CashBeforeTax = val
		case strings.Contains(name, "voucher"):
			out.Voucher = val
		case strings.Contains(name, "tax"):
			out.Tax = val
		}
	}
	out.CashInclTax = out.CashBeforeTax + out.Tax
	return out, nil
}

// BillByProductJSON returns the per-service cost breakdown as JSON for the
// dashboard's Cost Explorer view. The `summary` object adds the tax-aware
// waterfall (consumption → voucher / cash / tax) so the dashboard total can
// match the Tencent console's tax-inclusive headline.
func (c *Client) BillByProductJSON(ctx context.Context, month string) (string, error) {
	if month == "" {
		month = time.Now().Format("2006-01")
	}
	client, err := newBillingClient(c)
	if err != nil {
		return "", err
	}
	req := billing.NewDescribeBillSummaryByProductRequest()
	req.BeginTime = &month
	req.EndTime = &month
	resp, err := client.DescribeBillSummaryByProduct(req)
	if err != nil {
		return "", friendlyError(err)
	}
	type productCost struct {
		Code         string  `json:"code"`
		Name         string  `json:"name"`
		RealCost     float64 `json:"real_cost"`
		CashPay      float64 `json:"cash_pay"`
		IncentivePay float64 `json:"incentive_pay"`
		VoucherPay   float64 `json:"voucher_pay"`
		Ratio        string  `json:"ratio,omitempty"`
	}
	var items []productCost
	var total float64
	if resp != nil && resp.Response != nil {
		for _, it := range resp.Response.SummaryOverview {
			rc := parseFloat(derefString(it.RealTotalCost))
			total += rc
			items = append(items, productCost{
				Code:         derefStringRaw(it.BusinessCode),
				Name:         derefStringRaw(it.BusinessCodeName),
				RealCost:     rc,
				CashPay:      parseFloat(derefString(it.CashPayAmount)),
				IncentivePay: parseFloat(derefString(it.IncentivePayAmount)),
				VoucherPay:   parseFloat(derefString(it.VoucherPayAmount)),
				Ratio:        derefStringRaw(it.RealTotalCostRatio),
			})
		}
	}

	// Pull the tax-aware waterfall. A failure here is non-fatal — the
	// per-product list is still useful; summary just stays zero-valued.
	summary, ferr := billFeeSummary(client, month)
	if ferr != nil {
		summary.Note = "fee breakdown unavailable: " + ferr.Error()
	}

	out := struct {
		Month   string           `json:"month"`
		Total   float64          `json:"total"`
		Summary billFeeBreakdown `json:"summary"`
		Items   []productCost    `json:"items"`
	}{month, total, summary, items}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// BillResourceTopJSON returns the top-N resources by spend for the month.
func (c *Client) BillResourceTopJSON(ctx context.Context, month string, top int) (string, error) {
	if month == "" {
		month = time.Now().Format("2006-01")
	}
	if top <= 0 || top > 200 {
		top = 50
	}
	client, err := newBillingClient(c)
	if err != nil {
		return "", err
	}
	req := billing.NewDescribeBillResourceSummaryRequest()
	var offset uint64 = 0
	limit := uint64(top)
	req.Offset = &offset
	req.Limit = &limit
	req.Month = &month
	period := "byUsedTime"
	req.PeriodType = &period
	resp, err := client.DescribeBillResourceSummary(req)
	if err != nil {
		return "", friendlyError(err)
	}
	type resourceCost struct {
		Product    string  `json:"product"`
		ResourceID string  `json:"resource_id"`
		Name       string  `json:"name,omitempty"`
		Region     string  `json:"region,omitempty"`
		PayMode    string  `json:"pay_mode,omitempty"`
		Action     string  `json:"action,omitempty"`
		Cost       float64 `json:"cost"`
	}
	var items []resourceCost
	if resp != nil && resp.Response != nil {
		for _, r := range resp.Response.ResourceSummarySet {
			items = append(items, resourceCost{
				Product:    derefStringRaw(r.BusinessCodeName),
				ResourceID: derefStringRaw(r.ResourceId),
				Name:       derefStringRaw(r.ResourceName),
				Region:     derefStringRaw(r.RegionName),
				PayMode:    derefStringRaw(r.PayModeName),
				Action:     derefStringRaw(r.ActionTypeName),
				Cost:       parseFloat(derefString(r.RealTotalCost)),
			})
		}
	}
	out := struct {
		Month string         `json:"month"`
		Top   int            `json:"top"`
		Items []resourceCost `json:"items"`
	}{month, top, items}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// VoucherByOwnerJSON attributes a month's voucher deduction to the account
// that owns each billed resource.
//
// This is the only month-scoped, per-account view of voucher spend. The
// voucher APIs (DescribeVoucherInfo / DescribeVoucherUsageDetails) carry an
// OwnerUin only on the voucher itself and have no month dimension, but
// DescribeBillResourceSummary tags every billed line with OwnerUin /
// PayerUin / OperateUin alongside VoucherPayAmount — so summing
// VoucherPayAmount grouped by OwnerUin answers "which account had voucher
// deductions, and how much, this month".
//
// It pages the full month (DescribeBillResourceSummary, byUsedTime, 1000
// lines/page) rather than a top-N slice.
func (c *Client) VoucherByOwnerJSON(ctx context.Context, month string) (string, error) {
	if month == "" {
		month = time.Now().Format("2006-01")
	}
	client, err := newBillingClient(c)
	if err != nil {
		return "", err
	}

	type ownerAgg struct {
		voucherPay    float64
		realCost      float64
		cashPay       float64
		resourceCount int
		payers        map[string]bool
		operators     map[string]bool
	}
	owners := map[string]*ownerAgg{}
	var totalVoucher, totalReal float64
	var lineItems int

	var offset uint64 = 0
	const pageSize uint64 = 1000
	period := "byUsedTime"
	for {
		req := billing.NewDescribeBillResourceSummaryRequest()
		o, l := offset, pageSize
		req.Offset = &o
		req.Limit = &l
		req.Month = &month
		req.PeriodType = &period
		resp, err := client.DescribeBillResourceSummary(req)
		if err != nil {
			return "", friendlyError(err)
		}
		if resp == nil || resp.Response == nil {
			break
		}
		set := resp.Response.ResourceSummarySet
		for _, r := range set {
			if r == nil {
				continue
			}
			ow := derefStringRaw(r.OwnerUin)
			a := owners[ow]
			if a == nil {
				a = &ownerAgg{payers: map[string]bool{}, operators: map[string]bool{}}
				owners[ow] = a
			}
			vp := parseFloat(derefString(r.VoucherPayAmount))
			rc := parseFloat(derefString(r.RealTotalCost))
			a.voucherPay += vp
			a.realCost += rc
			a.cashPay += parseFloat(derefString(r.CashPayAmount))
			a.resourceCount++
			if p := derefStringRaw(r.PayerUin); p != "" {
				a.payers[p] = true
			}
			if op := derefStringRaw(r.OperateUin); op != "" {
				a.operators[op] = true
			}
			totalVoucher += vp
			totalReal += rc
			lineItems++
		}
		if uint64(len(set)) < pageSize {
			break
		}
		offset += pageSize
	}

	type ownerVoucher struct {
		OwnerUin      string   `json:"owner_uin"`
		VoucherPay    float64  `json:"voucher_pay"`
		RealCost      float64  `json:"real_cost"`
		CashPay       float64  `json:"cash_pay"`
		ResourceCount int      `json:"resource_count"`
		PayerUins     []string `json:"payer_uins,omitempty"`
		OperateUins   []string `json:"operate_uins,omitempty"`
	}
	items := make([]ownerVoucher, 0, len(owners))
	for uin, a := range owners {
		items = append(items, ownerVoucher{
			OwnerUin:      uin,
			VoucherPay:    a.voucherPay,
			RealCost:      a.realCost,
			CashPay:       a.cashPay,
			ResourceCount: a.resourceCount,
			PayerUins:     sortedKeys(a.payers),
			OperateUins:   sortedKeys(a.operators),
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].VoucherPay != items[j].VoucherPay {
			return items[i].VoucherPay > items[j].VoucherPay
		}
		return items[i].OwnerUin < items[j].OwnerUin
	})

	out := struct {
		Month        string         `json:"month"`
		TotalVoucher float64        `json:"total_voucher"`
		TotalReal    float64        `json:"total_real_cost"`
		OwnerCount   int            `json:"owner_count"`
		LineItems    int            `json:"line_items"`
		Owners       []ownerVoucher `json:"owners"`
	}{month, totalVoucher, totalReal, len(items), lineItems, items}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// sortedKeys returns a set's keys as a sorted slice.
func sortedKeys(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func parseFloat(s string) float64 {
	if v, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
		return v
	}
	return 0
}
