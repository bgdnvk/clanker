package tencent

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	clb "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/clb/v20180317"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
)

// listCLBs prints every Cloud Load Balancer in the given regions.
func listCLBs(c *Client, regions []string) error {
	multi := len(regions) > 1
	type row struct {
		region string
		lb     *clb.LoadBalancer
	}
	var rows []row
	var warnings []string

	for _, r := range regions {
		client, err := newCLBClient(c, r)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: init clb client: %v", r, err))
			continue
		}
		req := clb.NewDescribeLoadBalancersRequest()
		var offset, limit int64 = 0, 100
		req.Offset = &offset
		req.Limit = &limit
		resp, err := client.DescribeLoadBalancers(req)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", r, friendlyError(err)))
			continue
		}
		if resp == nil || resp.Response == nil {
			continue
		}
		for _, lb := range resp.Response.LoadBalancerSet {
			rows = append(rows, row{region: r, lb: lb})
		}
	}

	header := fmt.Sprintf("Cloud Load Balancers (region=%s)", c.Region())
	if multi {
		header = fmt.Sprintf("Cloud Load Balancers (regions=%d)", len(regions))
	}
	fmt.Printf("%s:\n\n", header)
	if len(rows) == 0 {
		fmt.Println("  No CLB instances found")
		printWarnings(warnings)
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	if multi {
		fmt.Fprintln(tw, "REGION\tLB_ID\tNAME\tTYPE\tSTATUS\tVIPS\tVPC_ID\tCREATED")
	} else {
		fmt.Fprintln(tw, "LB_ID\tNAME\tTYPE\tSTATUS\tVIPS\tVPC_ID\tCREATED")
	}
	for _, r := range rows {
		lb := r.lb
		fields := []string{
			derefString(lb.LoadBalancerId),
			derefString(lb.LoadBalancerName),
			derefString(lb.LoadBalancerType),
			clbStatus(lb.Status),
			joinIPs(lb.LoadBalancerVips),
			derefString(lb.VpcId),
			derefString(lb.CreateTime),
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

func newCLBClient(c *Client, region string) (*clb.Client, error) {
	if strings.TrimSpace(region) == "" {
		region = c.creds.Region
	}
	cred := common.NewCredential(c.creds.SecretID, c.creds.SecretKey)
	cpf := profile.NewClientProfile()
	cpf.HttpProfile.Endpoint = "clb.tencentcloudapi.com"
	return clb.NewClient(cred, region, cpf)
}

func clbStatus(p *uint64) string {
	if p == nil {
		return "-"
	}
	if *p == 0 {
		return "CREATING"
	}
	if *p == 1 {
		return "RUNNING"
	}
	return fmt.Sprintf("STATE-%d", *p)
}

// fetchCLBListeners pulls the listener set for one LB. Used by the
// public-exposure audit so we can flag CLBs that have open listeners on
// sensitive ports.
func fetchCLBListeners(c *Client, region, lbID string) ([]*clb.Listener, error) {
	client, err := newCLBClient(c, region)
	if err != nil {
		return nil, err
	}
	req := clb.NewDescribeListenersRequest()
	req.LoadBalancerId = &lbID
	resp, err := client.DescribeListeners(req)
	if err != nil {
		return nil, friendlyError(err)
	}
	if resp == nil || resp.Response == nil {
		return nil, nil
	}
	return resp.Response.Listeners, nil
}
