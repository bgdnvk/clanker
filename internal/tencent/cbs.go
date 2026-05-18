package tencent

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	cbs "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cbs/v20170312"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
)

// listCBS prints every Cloud Block Storage (disk) across the given regions.
// The encryption flag is the high-value column for security audits.
func listCBS(c *Client, regions []string) error {
	multi := len(regions) > 1
	type row struct {
		region string
		d      *cbs.Disk
	}
	var rows []row
	var warnings []string

	for _, r := range regions {
		client, err := newCBSClient(c, r)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: init cbs client: %v", r, err))
			continue
		}
		req := cbs.NewDescribeDisksRequest()
		var offset, limit uint64 = 0, 100
		req.Offset = &offset
		req.Limit = &limit
		resp, err := client.DescribeDisks(req)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", r, friendlyError(err)))
			continue
		}
		if resp == nil || resp.Response == nil {
			continue
		}
		for _, d := range resp.Response.DiskSet {
			rows = append(rows, row{region: r, d: d})
		}
	}

	header := fmt.Sprintf("Tencent Cloud Block Storage (region=%s)", c.Region())
	if multi {
		header = fmt.Sprintf("Tencent Cloud Block Storage (regions=%d)", len(regions))
	}
	fmt.Printf("%s:\n\n", header)
	if len(rows) == 0 {
		fmt.Println("  No CBS volumes found")
		printWarnings(warnings)
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	if multi {
		fmt.Fprintln(tw, "REGION\tDISK_ID\tNAME\tTYPE\tSIZE_GB\tSTATE\tENCRYPTED\tINSTANCE_ID\tZONE")
	} else {
		fmt.Fprintln(tw, "DISK_ID\tNAME\tTYPE\tSIZE_GB\tSTATE\tENCRYPTED\tINSTANCE_ID\tZONE")
	}
	for _, r := range rows {
		d := r.d
		zone := "-"
		if d.Placement != nil {
			zone = derefString(d.Placement.Zone)
		}
		fields := []string{
			derefString(d.DiskId),
			derefString(d.DiskName),
			derefString(d.DiskType),
			fmt.Sprintf("%d", derefUint64(d.DiskSize)),
			derefString(d.DiskState),
			fmt.Sprintf("%v", derefBool(d.Encrypt)),
			derefString(d.InstanceId),
			zone,
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

func newCBSClient(c *Client, region string) (*cbs.Client, error) {
	if strings.TrimSpace(region) == "" {
		region = c.creds.Region
	}
	cred := common.NewCredential(c.creds.SecretID, c.creds.SecretKey)
	cpf := profile.NewClientProfile()
	cpf.HttpProfile.Endpoint = "cbs.tencentcloudapi.com"
	return cbs.NewClient(cred, region, cpf)
}
