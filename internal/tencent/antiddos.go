package tencent

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	antiddos "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/antiddos/v20200309"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
)

// listAntiDDoS prints every Anti-DDoS Advanced BGP-IP instance.
func listAntiDDoS(c *Client) error {
	client, err := newAntiDDoSClient(c)
	if err != nil {
		return fmt.Errorf("init antiddos client: %w", err)
	}
	req := antiddos.NewDescribeListBGPIPInstancesRequest()
	var offset, limit uint64 = 0, 100
	req.Offset = &offset
	req.Limit = &limit
	resp, err := client.DescribeListBGPIPInstances(req)
	if err != nil {
		return fmt.Errorf("DescribeListBGPIPInstances: %w", friendlyError(err))
	}

	fmt.Println("Tencent Anti-DDoS Advanced (BGP-IP) Instances:")
	fmt.Println()
	if resp == nil || resp.Response == nil || len(resp.Response.InstanceList) == 0 {
		fmt.Println("  No Anti-DDoS Advanced instances (account uses Basic protection only)")
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "INSTANCE_ID\tNAME\tSTATUS\tREGION\tCREATED\tEXPIRES")
	for _, i := range resp.Response.InstanceList {
		region := "-"
		if i.Region != nil {
			region = derefString(i.Region.Region)
		}
		instanceID := "-"
		if i.InstanceDetail != nil {
			instanceID = derefString(i.InstanceDetail.InstanceId)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			instanceID,
			derefString(i.Name),
			derefString(i.Status),
			region,
			derefString(i.CreatedTime),
			derefString(i.ExpiredTime),
		)
	}
	return tw.Flush()
}

func newAntiDDoSClient(c *Client) (*antiddos.Client, error) {
	cred := common.NewCredential(c.creds.SecretID, c.creds.SecretKey)
	cpf := newClientProfile("antiddos.tencentcloudapi.com")
	return antiddos.NewClient(cred, "ap-guangzhou", cpf)
}

// hasAntiDDoSAdvanced reports whether the account has *any* Anti-DDoS
// Advanced subscription. The detailed per-IP coverage check is gated
// behind that flag — if the account has 0 Advanced instances, then by
// definition every public IP is on Basic protection only.
func hasAntiDDoSAdvanced(c *Client) (bool, []string, error) {
	client, err := newAntiDDoSClient(c)
	if err != nil {
		return false, nil, err
	}
	req := antiddos.NewDescribeListBGPIPInstancesRequest()
	var offset, limit uint64 = 0, 200
	req.Offset = &offset
	req.Limit = &limit
	resp, err := client.DescribeListBGPIPInstances(req)
	if err != nil {
		return false, nil, friendlyError(err)
	}
	if resp == nil || resp.Response == nil || len(resp.Response.InstanceList) == 0 {
		return false, nil, nil
	}
	var ids []string
	for _, i := range resp.Response.InstanceList {
		if i.InstanceDetail != nil {
			if id := strings.TrimSpace(derefString(i.InstanceDetail.InstanceId)); id != "" {
				ids = append(ids, id)
			}
		}
	}
	return true, ids, nil
}
