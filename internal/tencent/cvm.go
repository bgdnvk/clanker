package tencent

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	sdkerrors "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/errors"
	cvm "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cvm/v20170312"
)

// listCVM prints every CVM instance across the given regions. When more than
// one region is supplied a REGION column is added.
func listCVM(c *Client, regions []string) error {
	multi := len(regions) > 1

	type row struct {
		region string
		inst   *cvm.Instance
	}
	var rows []row
	var warnings []string

	for _, r := range regions {
		rc := c.WithRegion(r)
		client, err := rc.CVM()
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: init cvm client: %v", r, err))
			continue
		}
		req := cvm.NewDescribeInstancesRequest()
		var offset, limit int64 = 0, 100
		req.Offset = &offset
		req.Limit = &limit
		for {
			resp, err := client.DescribeInstances(req)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("%s: %v", r, friendlyError(err)))
				break
			}
			if resp == nil || resp.Response == nil {
				break
			}
			for _, inst := range resp.Response.InstanceSet {
				rows = append(rows, row{region: r, inst: inst})
			}
			total := derefInt64(resp.Response.TotalCount)
			offset += int64(len(resp.Response.InstanceSet))
			if offset >= total || len(resp.Response.InstanceSet) == 0 {
				break
			}
			req.Offset = &offset
		}
	}

	header := fmt.Sprintf("Tencent Cloud CVM Instances (region=%s)", c.Region())
	if multi {
		header = fmt.Sprintf("Tencent Cloud CVM Instances (regions=%d)", len(regions))
	}
	fmt.Printf("%s:\n\n", header)
	if len(rows) == 0 {
		fmt.Println("  No CVM instances found")
		printWarnings(warnings)
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	if multi {
		fmt.Fprintln(tw, "REGION\tINSTANCE_ID\tNAME\tSTATE\tTYPE\tPRIVATE_IP\tPUBLIC_IP\tZONE\tCREATED")
	} else {
		fmt.Fprintln(tw, "INSTANCE_ID\tNAME\tSTATE\tTYPE\tPRIVATE_IP\tPUBLIC_IP\tZONE\tCREATED")
	}
	for _, r := range rows {
		inst := r.inst
		fields := []string{
			derefString(inst.InstanceId),
			derefString(inst.InstanceName),
			derefString(inst.InstanceState),
			derefString(inst.InstanceType),
			joinIPs(inst.PrivateIpAddresses),
			joinIPs(inst.PublicIpAddresses),
			derefString(inst.Placement.Zone),
			derefString(inst.CreatedTime),
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

func printWarnings(warns []string) {
	if len(warns) == 0 {
		return
	}
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Warnings:")
	for _, w := range warns {
		fmt.Fprintln(os.Stderr, "  -", w)
	}
}

func joinIPs(ptrs []*string) string {
	if len(ptrs) == 0 {
		return "-"
	}
	var out []string
	for _, p := range ptrs {
		if p != nil && *p != "" {
			out = append(out, *p)
		}
	}
	if len(out) == 0 {
		return "-"
	}
	return strings.Join(out, ",")
}

func derefString(s *string) string {
	if s == nil {
		return "-"
	}
	v := strings.TrimSpace(*s)
	if v == "" {
		return "-"
	}
	return v
}

func derefInt64(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

// friendlyError converts Tencent SDK errors into something users can act on
// without exposing the full SDK wrapper noise.
func friendlyError(err error) error {
	if err == nil {
		return nil
	}
	if sdkErr, ok := err.(*sdkerrors.TencentCloudSDKError); ok {
		hint := ""
		switch sdkErr.Code {
		case "AuthFailure", "AuthFailure.SignatureFailure", "AuthFailure.SecretIdNotFound":
			hint = " (check TENCENTCLOUD_SECRET_ID/TENCENT_SECRET_ID and matching secret key)"
		case "UnauthorizedOperation.CamNoAuth", "UnauthorizedOperation":
			hint = " (sub-account is missing CAM permissions for this API)"
		}
		return fmt.Errorf("[%s] %s%s", sdkErr.Code, sdkErr.Message, hint)
	}
	return err
}
