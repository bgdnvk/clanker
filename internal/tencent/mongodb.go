package tencent

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	mongodb "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/mongodb/v20190725"
)

// listMongoDB prints every TencentDB for MongoDB instance across regions.
func listMongoDB(c *Client, regions []string) error {
	multi := len(regions) > 1
	type row struct {
		region string
		i      *mongodb.InstanceDetail
	}
	var rows []row
	var warnings []string

	for _, r := range regions {
		client, err := newMongoDBClient(c, r)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: init mongodb client: %v", r, err))
			continue
		}
		req := mongodb.NewDescribeDBInstancesRequest()
		var offset, limit uint64 = 0, 100
		req.Offset = &offset
		req.Limit = &limit
		resp, err := client.DescribeDBInstances(req)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", r, friendlyError(err)))
			continue
		}
		if resp == nil || resp.Response == nil {
			continue
		}
		for _, inst := range resp.Response.InstanceDetails {
			rows = append(rows, row{region: r, i: inst})
		}
	}

	header := fmt.Sprintf("TencentDB for MongoDB (region=%s)", c.Region())
	if multi {
		header = fmt.Sprintf("TencentDB for MongoDB (regions=%d)", len(regions))
	}
	fmt.Printf("%s:\n\n", header)
	if len(rows) == 0 {
		fmt.Println("  No MongoDB instances found")
		printWarnings(warnings)
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	if multi {
		fmt.Fprintln(tw, "REGION\tINSTANCE_ID\tNAME\tSTATUS\tCLUSTER_TYPE\tVIP:PORT\tZONE\tCREATED")
	} else {
		fmt.Fprintln(tw, "INSTANCE_ID\tNAME\tSTATUS\tCLUSTER_TYPE\tVIP:PORT\tZONE\tCREATED")
	}
	for _, r := range rows {
		i := r.i
		fields := []string{
			derefString(i.InstanceId),
			derefString(i.InstanceName),
			mongoStatus(i.Status),
			mongoClusterType(i.ClusterType),
			fmt.Sprintf("%s:%d", derefString(i.Vip), derefUint64(i.Vport)),
			derefString(i.Zone),
			derefString(i.CreateTime),
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

func newMongoDBClient(c *Client, region string) (*mongodb.Client, error) {
	if strings.TrimSpace(region) == "" {
		region = c.creds.Region
	}
	cred := common.NewCredential(c.creds.SecretID, c.creds.SecretKey)
	cpf := newClientProfile("mongodb.tencentcloudapi.com")
	return mongodb.NewClient(cred, region, cpf)
}

func mongoStatus(p *int64) string {
	if p == nil {
		return "-"
	}
	switch *p {
	case 0:
		return "PENDING_INIT"
	case 1:
		return "PROCESSING"
	case 2:
		return "RUNNING"
	case -2:
		return "ISOLATED_PREPAID"
	case -3:
		return "ISOLATED_POSTPAID"
	default:
		return fmt.Sprintf("STATE-%d", *p)
	}
}

func mongoClusterType(p *uint64) string {
	if p == nil {
		return "-"
	}
	switch *p {
	case 0:
		return "REPLICA_SET"
	case 1:
		return "SHARDED"
	default:
		return fmt.Sprintf("TYPE-%d", *p)
	}
}
