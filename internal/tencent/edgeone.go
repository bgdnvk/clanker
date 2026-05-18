package tencent

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
	teo "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/teo/v20220901"
)

// listEdgeOneZones prints every EdgeOne (TEO) zone. EdgeOne is account-global.
func listEdgeOneZones(c *Client) error {
	client, err := newEdgeOneClient(c)
	if err != nil {
		return fmt.Errorf("init edgeone client: %w", err)
	}
	req := teo.NewDescribeZonesRequest()
	var offset, limit int64 = 0, 100
	req.Offset = &offset
	req.Limit = &limit
	resp, err := client.DescribeZones(req)
	if err != nil {
		return fmt.Errorf("DescribeZones: %w", friendlyError(err))
	}

	fmt.Println("Tencent EdgeOne (TEO) Zones:")
	fmt.Println()
	if resp == nil || resp.Response == nil || len(resp.Response.Zones) == 0 {
		fmt.Println("  No EdgeOne zones found")
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ZONE_ID\tNAME\tTYPE\tAREA\tSTATUS")
	for _, z := range resp.Response.Zones {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			derefString(z.ZoneId),
			derefString(z.ZoneName),
			derefString(z.Type),
			derefString(z.Area),
			derefString(z.Status),
		)
	}
	return tw.Flush()
}

func newEdgeOneClient(c *Client) (*teo.Client, error) {
	cred := common.NewCredential(c.creds.SecretID, c.creds.SecretKey)
	cpf := profile.NewClientProfile()
	cpf.HttpProfile.Endpoint = "teo.tencentcloudapi.com"
	return teo.NewClient(cred, "ap-guangzhou", cpf)
}

// listEdgeOneZoneNames returns zone names for use by audits.
func listEdgeOneZoneNames(c *Client) []string {
	client, err := newEdgeOneClient(c)
	if err != nil {
		return nil
	}
	req := teo.NewDescribeZonesRequest()
	var offset, limit int64 = 0, 200
	req.Offset = &offset
	req.Limit = &limit
	resp, err := client.DescribeZones(req)
	if err != nil || resp == nil || resp.Response == nil {
		return nil
	}
	out := make([]string, 0, len(resp.Response.Zones))
	for _, z := range resp.Response.Zones {
		if s := strings.TrimSpace(derefString(z.ZoneName)); s != "" && s != "-" {
			out = append(out, s)
		}
	}
	return out
}
