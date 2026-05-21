package tencent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	billing "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/billing/v20180709"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
)

// newVoucherClient builds a billing client for the voucher APIs. The cost
// APIs are happy on the global ap-guangzhou endpoint, but DescribeVoucherInfo
// and DescribeVoucherUsageDetails reject every region except the account's
// home region with UnsupportedRegion — so this uses the credential's region.
func newVoucherClient(c *Client) (*billing.Client, error) {
	region := strings.TrimSpace(c.creds.Region)
	if region == "" {
		region = "ap-singapore"
	}
	cred := common.NewCredential(c.creds.SecretID, c.creds.SecretKey)
	cpf := profile.NewClientProfile()
	cpf.HttpProfile.Endpoint = "billing.tencentcloudapi.com"
	return billing.NewClient(cred, region, cpf)
}

// Voucher (代金券) queries — DescribeVoucherInfo + DescribeVoucherUsageDetails.
//
// These live alongside billing.go but answer a different question. The cost
// APIs (DescribeBillSummaryByProduct etc.) tell you what a *month* cost; the
// voucher APIs tell you what credit the account holds and how it was burned.
//
// Two quirks Tencent only applies to the voucher surface:
//
//   - Money is fixed-point "micro" units, not decimal strings. Every amount
//     (NominalValue, Balance, UsedAmount, ...) is an int64 where 1 yuan =
//     1e8 micro. See microPerYuan / microYuan below.
//   - There is no per-record sub-account UIN. DescribeVoucherUsageDetails
//     returns deductions per voucher with no UIN field — the owning account
//     is a property of the *voucher* (VoucherInfos.OwnerUin), so the
//     per-account breakdown is built by grouping vouchers, not usage rows.

// microPerYuan is the fixed-point scale Tencent's voucher APIs use: 1 unit of
// account currency == 1e8 "micro". DescribeVoucherInfoResponse.Unit confirms
// this ("micro": 1 micro = 10⁻⁸ CNY/USD).
const microPerYuan = 1e8

// microYuan converts a micro-denominated int64 pointer to account currency.
func microYuan(v *int64) float64 {
	if v == nil {
		return 0
	}
	return float64(*v) / microPerYuan
}

// orDash renders an empty string as "-" for table output (the *string
// derefString helper can't be used here — these fields are already strings).
func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

// voucher is one DescribeVoucherInfo entry with micro amounts converted to
// account currency and "spent" derived as nominal − balance.
type voucher struct {
	VoucherID string  `json:"voucher_id"`
	OwnerUin  string  `json:"owner_uin"`
	Status    string  `json:"status"`
	Active    bool    `json:"active"` // true when still usable (status unUsed)
	Nominal   float64 `json:"nominal"`
	Balance   float64 `json:"balance"`
	Spent     float64 `json:"spent"`
	PayMode   string  `json:"pay_mode,omitempty"`
	PayScene  string  `json:"pay_scene,omitempty"`
	BeginTime string  `json:"begin_time,omitempty"`
	EndTime   string  `json:"end_time,omitempty"`
	Products  string  `json:"products,omitempty"`
	Remark    string  `json:"remark,omitempty"`
}

// voucherOwner aggregates one owner UIN's vouchers — the per-account spend
// breakdown shown next to the flat voucher list.
type voucherOwner struct {
	OwnerUin     string  `json:"owner_uin"`
	VoucherCount int     `json:"voucher_count"`
	ActiveCount  int     `json:"active_count"`
	Nominal      float64 `json:"nominal"`
	Balance      float64 `json:"balance"`
	Spent        float64 `json:"spent"`
}

// voucherUsage is one DescribeVoucherUsageDetails record: a single deduction
// against a voucher, with the product(s) it paid for flattened to a string.
type voucherUsage struct {
	VoucherID  string  `json:"voucher_id"`
	UsedAmount float64 `json:"used_amount"`
	UsedTime   string  `json:"used_time"`
	PayMode    string  `json:"pay_mode,omitempty"`
	PayScene   string  `json:"pay_scene,omitempty"`
	Products   string  `json:"products,omitempty"`
	SeqID      string  `json:"seq_id,omitempty"`
}

// fetchVouchers pages through DescribeVoucherInfo (max 1000 rows/page) and
// returns every voucher matching the optional status filter — one of
// Tencent's enum strings (unUsed / used / delivered / cancel / overdue) or
// "" for all statuses.
func fetchVouchers(c *Client, status string) ([]voucher, error) {
	client, err := newVoucherClient(c)
	if err != nil {
		return nil, err
	}
	var out []voucher
	var page int64 = 1
	const pageSize int64 = 1000
	for {
		req := billing.NewDescribeVoucherInfoRequest()
		req.Limit = common.Int64Ptr(pageSize)
		req.Offset = common.Int64Ptr(page) // Offset is a 1-based page number
		if status != "" {
			req.Status = &status
		}
		resp, err := client.DescribeVoucherInfo(req)
		if err != nil {
			return nil, friendlyError(err)
		}
		if resp == nil || resp.Response == nil {
			break
		}
		for _, v := range resp.Response.VoucherInfos {
			if v == nil {
				continue
			}
			st := derefStringRaw(v.Status)
			nominal := microYuan(v.NominalValue)
			balance := microYuan(v.Balance)
			vc := voucher{
				VoucherID: derefStringRaw(v.VoucherId),
				OwnerUin:  derefStringRaw(v.OwnerUin),
				Status:    st,
				Active:    st == "unUsed",
				Nominal:   nominal,
				Balance:   balance,
				Spent:     nominal - balance,
				PayMode:   derefStringRaw(v.PayMode),
				PayScene:  derefStringRaw(v.PayScene),
				BeginTime: derefStringRaw(v.BeginTime),
				EndTime:   derefStringRaw(v.EndTime),
				Remark:    derefStringRaw(v.PolicyRemark),
			}
			if v.ApplicableProducts != nil {
				vc.Products = derefStringRaw(v.ApplicableProducts.GoodsName)
			}
			out = append(out, vc)
		}
		total := derefInt64(resp.Response.TotalCount)
		if int64(len(out)) >= total || len(resp.Response.VoucherInfos) == 0 {
			break
		}
		page++
	}
	return out, nil
}

// fetchVoucherUsage pages through DescribeVoucherUsageDetails for one voucher
// and returns its deduction history plus the API's reported total.
func fetchVoucherUsage(c *Client, voucherID string) ([]voucherUsage, float64, error) {
	client, err := newVoucherClient(c)
	if err != nil {
		return nil, 0, err
	}
	var out []voucherUsage
	var totalUsed float64
	var page int64 = 1
	const pageSize int64 = 1000
	for {
		req := billing.NewDescribeVoucherUsageDetailsRequest()
		req.Limit = common.Int64Ptr(pageSize)
		req.Offset = common.Int64Ptr(page)
		req.VoucherId = &voucherID
		resp, err := client.DescribeVoucherUsageDetails(req)
		if err != nil {
			return nil, 0, friendlyError(err)
		}
		if resp == nil || resp.Response == nil {
			break
		}
		if resp.Response.TotalUsedAmount != nil {
			totalUsed = microYuan(resp.Response.TotalUsedAmount)
		}
		for _, r := range resp.Response.UsageRecords {
			if r == nil {
				continue
			}
			out = append(out, voucherUsage{
				VoucherID:  derefStringRaw(r.VoucherId),
				UsedAmount: microYuan(r.UsedAmount),
				UsedTime:   derefStringRaw(r.UsedTime),
				PayMode:    derefStringRaw(r.PayMode),
				PayScene:   derefStringRaw(r.PayScene),
				Products:   usageProducts(r.UsageDetails),
				SeqID:      derefStringRaw(r.SeqId),
			})
		}
		total := derefInt64(resp.Response.TotalCount)
		if int64(len(out)) >= total || len(resp.Response.UsageRecords) == 0 {
			break
		}
		page++
	}
	return out, totalUsed, nil
}

// usageProducts flattens a usage record's per-product detail rows into a
// deduplicated comma-separated string, preferring the localized name and
// falling back to the English one.
func usageProducts(ds []*billing.UsageDetails) string {
	if len(ds) == 0 {
		return ""
	}
	seen := map[string]bool{}
	var names []string
	for _, d := range ds {
		if d == nil {
			continue
		}
		n := derefStringRaw(d.ProductName)
		if n == "" {
			n = derefStringRaw(d.ProductEnName)
		}
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		names = append(names, n)
	}
	return strings.Join(names, ", ")
}

// aggregateOwners groups vouchers by owner UIN — the per-account voucher
// spend breakdown. Sorted by spend descending.
func aggregateOwners(vs []voucher) []voucherOwner {
	m := map[string]*voucherOwner{}
	for _, v := range vs {
		o := m[v.OwnerUin]
		if o == nil {
			o = &voucherOwner{OwnerUin: v.OwnerUin}
			m[v.OwnerUin] = o
		}
		o.VoucherCount++
		if v.Active {
			o.ActiveCount++
		}
		o.Nominal += v.Nominal
		o.Balance += v.Balance
		o.Spent += v.Spent
	}
	out := make([]voucherOwner, 0, len(m))
	for _, o := range m {
		out = append(out, *o)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Spent != out[j].Spent {
			return out[i].Spent > out[j].Spent
		}
		return out[i].OwnerUin < out[j].OwnerUin
	})
	return out
}

// VouchersJSON returns the account's vouchers plus a per-owner-UIN spend
// breakdown for the dashboard Cost Explorer. status filters by Tencent's
// voucher-status enum (unUsed/used/delivered/cancel/overdue); "" returns all.
func (c *Client) VouchersJSON(ctx context.Context, status string) (string, error) {
	vs, err := fetchVouchers(c, status)
	if err != nil {
		return "", err
	}
	var totalNominal, totalBalance, totalSpent float64
	for _, v := range vs {
		totalNominal += v.Nominal
		totalBalance += v.Balance
		totalSpent += v.Spent
	}
	out := struct {
		Status       string         `json:"status,omitempty"`
		Count        int            `json:"count"`
		TotalNominal float64        `json:"total_nominal"`
		TotalBalance float64        `json:"total_balance"`
		TotalSpent   float64        `json:"total_spent"`
		Owners       []voucherOwner `json:"owners"`
		Vouchers     []voucher      `json:"vouchers"`
	}{status, len(vs), totalNominal, totalBalance, totalSpent, aggregateOwners(vs), vs}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// VoucherUsageJSON returns the deduction history for one voucher — the
// drill-down behind a row in the Cost Explorer voucher table.
func (c *Client) VoucherUsageJSON(ctx context.Context, voucherID string) (string, error) {
	voucherID = strings.TrimSpace(voucherID)
	if voucherID == "" {
		return "", fmt.Errorf("voucher_id is required")
	}
	records, totalUsed, err := fetchVoucherUsage(c, voucherID)
	if err != nil {
		return "", err
	}
	out := struct {
		VoucherID string         `json:"voucher_id"`
		Count     int            `json:"count"`
		TotalUsed float64        `json:"total_used"`
		Records   []voucherUsage `json:"records"`
	}{voucherID, len(records), totalUsed, records}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// listVouchers prints the voucher inventory and the per-owner spend breakdown.
func listVouchers(c *Client, status string) error {
	vs, err := fetchVouchers(c, status)
	if err != nil {
		return fmt.Errorf("DescribeVoucherInfo: %w", err)
	}
	label := status
	if label == "" {
		label = "all statuses"
	}
	fmt.Printf("Tencent Cloud Vouchers — %s:\n\n", label)
	if len(vs) == 0 {
		fmt.Println("  No vouchers found")
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "VOUCHER_ID\tOWNER_UIN\tSTATUS\tNOMINAL\tBALANCE\tSPENT\tEXPIRES")
	for _, v := range vs {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%.2f\t%.2f\t%.2f\t%s\n",
			orDash(v.VoucherID), orDash(v.OwnerUin), orDash(v.Status),
			v.Nominal, v.Balance, v.Spent, orDash(v.EndTime))
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	owners := aggregateOwners(vs)
	fmt.Printf("\nVoucher spending by owner account:\n\n")
	tw = tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "OWNER_UIN\tVOUCHERS\tACTIVE\tNOMINAL\tBALANCE\tSPENT")
	for _, o := range owners {
		fmt.Fprintf(tw, "%s\t%d\t%d\t%.2f\t%.2f\t%.2f\n",
			orDash(o.OwnerUin), o.VoucherCount, o.ActiveCount, o.Nominal, o.Balance, o.Spent)
	}
	return tw.Flush()
}

// listVoucherUsage prints the deduction history for a single voucher.
func listVoucherUsage(c *Client, voucherID string) error {
	voucherID = strings.TrimSpace(voucherID)
	if voucherID == "" {
		return fmt.Errorf("voucher id is required")
	}
	records, totalUsed, err := fetchVoucherUsage(c, voucherID)
	if err != nil {
		return fmt.Errorf("DescribeVoucherUsageDetails: %w", err)
	}
	fmt.Printf("Voucher %s — usage history:\n\n", voucherID)
	if len(records) == 0 {
		fmt.Println("  No usage records for this voucher")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "USED_TIME\tAMOUNT\tPAY_MODE\tPRODUCTS")
	for _, r := range records {
		fmt.Fprintf(tw, "%s\t%.2f\t%s\t%s\n",
			orDash(r.UsedTime), r.UsedAmount, orDash(r.PayMode), orDash(r.Products))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Printf("\nTotal used: %.2f\n", totalUsed)
	return nil
}
