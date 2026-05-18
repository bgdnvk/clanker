package tencent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	billing "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/billing/v20180709"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
)

// listBillByProduct prints a per-service cost breakdown for a given month.
// Tencent's billing API treats month as both BeginTime and EndTime so we
// always pass the same value.
func listBillByProduct(c *Client, month string) error {
	if month == "" {
		month = time.Now().Format("2006-01")
	}
	client, err := newBillingClient(c)
	if err != nil {
		return fmt.Errorf("init billing client: %w", err)
	}
	req := billing.NewDescribeBillSummaryByProductRequest()
	req.BeginTime = &month
	req.EndTime = &month
	resp, err := client.DescribeBillSummaryByProduct(req)
	if err != nil {
		return fmt.Errorf("DescribeBillSummaryByProduct: %w", friendlyError(err))
	}

	fmt.Printf("Tencent Cloud Cost by Product — %s:\n\n", month)
	if resp == nil || resp.Response == nil || resp.Response.SummaryOverview == nil || len(resp.Response.SummaryOverview) == 0 {
		fmt.Println("  No billing data for this month")
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PRODUCT\tREAL_COST\tCASH\tINCENTIVE\tVOUCHER\tPCT")
	var total float64
	for _, it := range resp.Response.SummaryOverview {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s%%\n",
			derefString(it.BusinessCodeName),
			derefString(it.RealTotalCost),
			derefString(it.CashPayAmount),
			derefString(it.IncentivePayAmount),
			derefString(it.VoucherPayAmount),
			derefString(it.RealTotalCostRatio),
		)
		if it.RealTotalCost != nil {
			if v, err := strconv.ParseFloat(*it.RealTotalCost, 64); err == nil {
				total += v
			}
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Printf("\nTotal: %.4f\n", total)
	return nil
}

// listBillResourceTop prints the most expensive resources for the month.
func listBillResourceTop(c *Client, month string, top int) error {
	if month == "" {
		month = time.Now().Format("2006-01")
	}
	if top <= 0 {
		top = 20
	}
	client, err := newBillingClient(c)
	if err != nil {
		return fmt.Errorf("init billing client: %w", err)
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
		return fmt.Errorf("DescribeBillResourceSummary: %w", friendlyError(err))
	}

	fmt.Printf("Top %d Resources by Cost — %s:\n\n", top, month)
	if resp == nil || resp.Response == nil || len(resp.Response.ResourceSummarySet) == 0 {
		fmt.Println("  No resource-level billing data for this month")
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PRODUCT\tRESOURCE_ID\tNAME\tREGION\tPAY_MODE\tACTION\tCOST")
	for _, r := range resp.Response.ResourceSummarySet {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			derefString(r.BusinessCodeName),
			derefString(r.ResourceId),
			derefString(r.ResourceName),
			derefString(r.RegionName),
			derefString(r.PayModeName),
			derefString(r.ActionTypeName),
			derefString(r.RealTotalCost),
		)
	}
	return tw.Flush()
}

func newBillingClient(c *Client) (*billing.Client, error) {
	cred := common.NewCredential(c.creds.SecretID, c.creds.SecretKey)
	cpf := profile.NewClientProfile()
	cpf.HttpProfile.Endpoint = "billing.tencentcloudapi.com"
	return billing.NewClient(cred, "ap-guangzhou", cpf) // billing is global
}

// BillByProductJSON returns the per-service cost breakdown as JSON for the
// dashboard's Cost Explorer view.
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
		Code           string  `json:"code"`
		Name           string  `json:"name"`
		RealCost       float64 `json:"real_cost"`
		CashPay        float64 `json:"cash_pay"`
		IncentivePay   float64 `json:"incentive_pay"`
		VoucherPay     float64 `json:"voucher_pay"`
		Ratio          string  `json:"ratio,omitempty"`
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
	out := struct {
		Month string        `json:"month"`
		Total float64       `json:"total"`
		Items []productCost `json:"items"`
	}{month, total, items}
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

func parseFloat(s string) float64 {
	if v, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
		return v
	}
	return 0
}
