package tencent

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	vpc "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/vpc/v20170312"
)

// listEIPs prints every Elastic IP (Address) across the given regions.
// EIPs are managed through the VPC service so no new SDK package is needed.
func listEIPs(c *Client, regions []string) error {
	multi := len(regions) > 1
	type row struct {
		region string
		eip    *vpc.Address
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
		req := vpc.NewDescribeAddressesRequest()
		var offset, limit int64 = 0, 100
		req.Offset = &offset
		req.Limit = &limit
		resp, err := client.DescribeAddresses(req)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", r, friendlyError(err)))
			continue
		}
		if resp == nil || resp.Response == nil {
			continue
		}
		for _, a := range resp.Response.AddressSet {
			rows = append(rows, row{region: r, eip: a})
		}
	}

	header := fmt.Sprintf("Tencent Cloud Elastic IPs (region=%s)", c.Region())
	if multi {
		header = fmt.Sprintf("Tencent Cloud Elastic IPs (regions=%d)", len(regions))
	}
	fmt.Printf("%s:\n\n", header)
	if len(rows) == 0 {
		fmt.Println("  No EIPs found")
		printWarnings(warnings)
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	if multi {
		fmt.Fprintln(tw, "REGION\tEIP_ID\tNAME\tIP\tSTATUS\tTYPE\tBOUND_TO\tCREATED")
	} else {
		fmt.Fprintln(tw, "EIP_ID\tNAME\tIP\tSTATUS\tTYPE\tBOUND_TO\tCREATED")
	}
	for _, r := range rows {
		a := r.eip
		fields := []string{
			derefString(a.AddressId),
			derefString(a.AddressName),
			derefString(a.AddressIp),
			derefString(a.AddressStatus),
			derefString(a.AddressType),
			derefString(a.InstanceId),
			derefString(a.CreatedTime),
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
