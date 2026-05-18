package tencent

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
	dc "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/dc/v20180410"
	vpc "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/vpc/v20170312"
)

// listNATGateways prints every NAT gateway across the given regions.
func listNATGateways(c *Client, regions []string) error {
	multi := len(regions) > 1
	type row struct {
		region string
		g      *vpc.NatGateway
	}
	var rows []row
	var warnings []string

	for _, r := range regions {
		rc := c.WithRegion(r)
		client, err := rc.VPC()
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: init vpc client: %v", r, err))
			continue
		}
		req := vpc.NewDescribeNatGatewaysRequest()
		var offset, limit uint64 = 0, 100
		req.Offset = &offset
		req.Limit = &limit
		resp, err := client.DescribeNatGateways(req)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", r, friendlyError(err)))
			continue
		}
		if resp == nil || resp.Response == nil {
			continue
		}
		for _, g := range resp.Response.NatGatewaySet {
			rows = append(rows, row{region: r, g: g})
		}
	}

	header := fmt.Sprintf("Tencent NAT Gateways (region=%s)", c.Region())
	if multi {
		header = fmt.Sprintf("Tencent NAT Gateways (regions=%d)", len(regions))
	}
	fmt.Printf("%s:\n\n", header)
	if len(rows) == 0 {
		fmt.Println("  No NAT gateways found")
		printWarnings(warnings)
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	if multi {
		fmt.Fprintln(tw, "REGION\tNAT_ID\tNAME\tSTATE\tBANDWIDTH_OUT\tPUBLIC_IPS\tCREATED")
	} else {
		fmt.Fprintln(tw, "NAT_ID\tNAME\tSTATE\tBANDWIDTH_OUT\tPUBLIC_IPS\tCREATED")
	}
	for _, r := range rows {
		g := r.g
		var ips []string
		for _, ip := range g.PublicIpAddressSet {
			if ip != nil && ip.PublicIpAddress != nil {
				ips = append(ips, *ip.PublicIpAddress)
			}
		}
		ipStr := strings.Join(ips, ",")
		if ipStr == "" {
			ipStr = "-"
		}
		fields := []string{
			derefString(g.NatGatewayId),
			derefString(g.NatGatewayName),
			derefString(g.State),
			fmt.Sprintf("%dMbps", derefUint64(g.InternetMaxBandwidthOut)),
			ipStr,
			derefString(g.CreatedTime),
		}
		if multi {
			fmt.Fprintln(tw, r.region+"\t"+strings.Join(fields, "\t"))
		} else {
			fmt.Fprintln(tw, strings.Join(fields, "\t"))
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	printWarnings(warnings)
	return nil
}

// listVPNGateways prints every VPN gateway across the given regions.
func listVPNGateways(c *Client, regions []string) error {
	multi := len(regions) > 1
	type row struct {
		region string
		g      *vpc.VpnGateway
	}
	var rows []row
	var warnings []string

	for _, r := range regions {
		rc := c.WithRegion(r)
		client, err := rc.VPC()
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: init vpc client: %v", r, err))
			continue
		}
		req := vpc.NewDescribeVpnGatewaysRequest()
		var offset, limit uint64 = 0, 100
		req.Offset = &offset
		req.Limit = &limit
		resp, err := client.DescribeVpnGateways(req)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", r, friendlyError(err)))
			continue
		}
		if resp == nil || resp.Response == nil {
			continue
		}
		for _, g := range resp.Response.VpnGatewaySet {
			rows = append(rows, row{region: r, g: g})
		}
	}

	header := fmt.Sprintf("Tencent VPN Gateways (region=%s)", c.Region())
	if multi {
		header = fmt.Sprintf("Tencent VPN Gateways (regions=%d)", len(regions))
	}
	fmt.Printf("%s:\n\n", header)
	if len(rows) == 0 {
		fmt.Println("  No VPN gateways found")
		printWarnings(warnings)
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	if multi {
		fmt.Fprintln(tw, "REGION\tVPN_ID\tNAME\tTYPE\tSTATE\tPUBLIC_IP\tVPC_ID\tCHARGE")
	} else {
		fmt.Fprintln(tw, "VPN_ID\tNAME\tTYPE\tSTATE\tPUBLIC_IP\tVPC_ID\tCHARGE")
	}
	for _, r := range rows {
		g := r.g
		fields := []string{
			derefString(g.VpnGatewayId),
			derefString(g.VpnGatewayName),
			derefString(g.Type),
			derefString(g.State),
			derefString(g.PublicIpAddress),
			derefString(g.VpcId),
			derefString(g.InstanceChargeType),
		}
		if multi {
			fmt.Fprintln(tw, r.region+"\t"+strings.Join(fields, "\t"))
		} else {
			fmt.Fprintln(tw, strings.Join(fields, "\t"))
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	printWarnings(warnings)
	return nil
}

// listCCNs prints every Cloud Connect Network. CCN is account-global so we
// only need one region for the API call.
func listCCNs(c *Client) error {
	client, err := c.VPC()
	if err != nil {
		return fmt.Errorf("init vpc client: %w", err)
	}
	req := vpc.NewDescribeCcnsRequest()
	var offset, limit uint64 = 0, 100
	req.Offset = &offset
	req.Limit = &limit
	resp, err := client.DescribeCcns(req)
	if err != nil {
		return fmt.Errorf("DescribeCcns: %w", friendlyError(err))
	}

	fmt.Println("Tencent Cloud Connect Networks (CCN):")
	fmt.Println()
	if resp == nil || resp.Response == nil || len(resp.Response.CcnSet) == 0 {
		fmt.Println("  No CCNs found")
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "CCN_ID\tNAME\tSTATE\tINSTANCES\tCREATED")
	for _, ccn := range resp.Response.CcnSet {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\n",
			derefString(ccn.CcnId),
			derefString(ccn.CcnName),
			derefString(ccn.State),
			derefUint64(ccn.InstanceCount),
			derefString(ccn.CreateTime),
		)
	}
	return tw.Flush()
}

// listDirectConnects prints every Direct Connect physical line across regions.
func listDirectConnects(c *Client, regions []string) error {
	multi := len(regions) > 1
	type row struct {
		region string
		d      *dc.DirectConnect
	}
	var rows []row
	var warnings []string

	for _, r := range regions {
		client, err := newDCClient(c, r)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: init dc client: %v", r, err))
			continue
		}
		req := dc.NewDescribeDirectConnectsRequest()
		var offset, limit int64 = 0, 100
		req.Offset = &offset
		req.Limit = &limit
		resp, err := client.DescribeDirectConnects(req)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", r, friendlyError(err)))
			continue
		}
		if resp == nil || resp.Response == nil {
			continue
		}
		for _, d := range resp.Response.DirectConnectSet {
			rows = append(rows, row{region: r, d: d})
		}
	}

	header := fmt.Sprintf("Tencent Direct Connect (region=%s)", c.Region())
	if multi {
		header = fmt.Sprintf("Tencent Direct Connect (regions=%d)", len(regions))
	}
	fmt.Printf("%s:\n\n", header)
	if len(rows) == 0 {
		fmt.Println("  No Direct Connect lines found")
		printWarnings(warnings)
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	if multi {
		fmt.Fprintln(tw, "REGION\tDC_ID\tNAME\tSTATE\tACCESS_POINT")
	} else {
		fmt.Fprintln(tw, "DC_ID\tNAME\tSTATE\tACCESS_POINT")
	}
	for _, r := range rows {
		d := r.d
		fields := []string{
			derefString(d.DirectConnectId),
			derefString(d.DirectConnectName),
			derefString(d.State),
			derefString(d.AccessPointId),
		}
		if multi {
			fmt.Fprintln(tw, r.region+"\t"+strings.Join(fields, "\t"))
		} else {
			fmt.Fprintln(tw, strings.Join(fields, "\t"))
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	printWarnings(warnings)
	return nil
}

func newDCClient(c *Client, region string) (*dc.Client, error) {
	if strings.TrimSpace(region) == "" {
		region = c.creds.Region
	}
	cred := common.NewCredential(c.creds.SecretID, c.creds.SecretKey)
	cpf := profile.NewClientProfile()
	cpf.HttpProfile.Endpoint = "dc.tencentcloudapi.com"
	return dc.NewClient(cred, region, cpf)
}
