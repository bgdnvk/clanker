package tencent

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"

	cdb "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cdb/v20170320"
	postgres "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/postgres/v20170312"
)

// listMySQL prints every TencentDB for MySQL instance across the given regions.
func listMySQL(c *Client, regions []string) error {
	multi := len(regions) > 1
	type row struct {
		region string
		inst   *cdb.InstanceInfo
	}
	var rows []row
	var warnings []string

	for _, r := range regions {
		client, err := newCDBClient(c, r)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: init cdb client: %v", r, err))
			continue
		}
		req := cdb.NewDescribeDBInstancesRequest()
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
		for _, inst := range resp.Response.Items {
			rows = append(rows, row{region: r, inst: inst})
		}
	}

	header := fmt.Sprintf("TencentDB for MySQL (region=%s)", c.Region())
	if multi {
		header = fmt.Sprintf("TencentDB for MySQL (regions=%d)", len(regions))
	}
	fmt.Printf("%s:\n\n", header)
	if len(rows) == 0 {
		fmt.Println("  No MySQL instances found")
		printWarnings(warnings)
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	if multi {
		fmt.Fprintln(tw, "REGION\tINSTANCE_ID\tNAME\tSTATUS\tENGINE\tMEM(MB)\tDISK(GB)\tPRIVATE_IP\tPUBLIC\tZONE")
	} else {
		fmt.Fprintln(tw, "INSTANCE_ID\tNAME\tSTATUS\tENGINE\tMEM(MB)\tDISK(GB)\tPRIVATE_IP\tPUBLIC\tZONE")
	}
	for _, r := range rows {
		i := r.inst
		fields := []string{
			derefString(i.InstanceId),
			derefString(i.InstanceName),
			mysqlStatus(i.Status),
			derefString(i.EngineVersion),
			fmt.Sprintf("%d", derefInt64(i.Memory)),
			fmt.Sprintf("%d", derefInt64(i.Volume)),
			fmt.Sprintf("%s:%d", derefString(i.Vip), derefInt64(i.Vport)),
			mysqlWan(i),
			derefString(i.Zone),
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

// listPostgres prints every TencentDB for PostgreSQL instance across regions.
func listPostgres(c *Client, regions []string) error {
	multi := len(regions) > 1
	type row struct {
		region string
		inst   *postgres.DBInstance
	}
	var rows []row
	var warnings []string

	for _, r := range regions {
		client, err := newPostgresClient(c, r)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: init postgres client: %v", r, err))
			continue
		}
		req := postgres.NewDescribeDBInstancesRequest()
		var limit, offset uint64 = 100, 0
		req.Limit = &limit
		req.Offset = &offset
		resp, err := client.DescribeDBInstances(req)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", r, friendlyError(err)))
			continue
		}
		if resp == nil || resp.Response == nil {
			continue
		}
		for _, inst := range resp.Response.DBInstanceSet {
			rows = append(rows, row{region: r, inst: inst})
		}
	}

	header := fmt.Sprintf("TencentDB for PostgreSQL (region=%s)", c.Region())
	if multi {
		header = fmt.Sprintf("TencentDB for PostgreSQL (regions=%d)", len(regions))
	}
	fmt.Printf("%s:\n\n", header)
	if len(rows) == 0 {
		fmt.Println("  No PostgreSQL instances found")
		printWarnings(warnings)
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	if multi {
		fmt.Fprintln(tw, "REGION\tINSTANCE_ID\tNAME\tSTATUS\tENGINE\tCPU\tMEM(GB)\tDISK(GB)\tZONE\tCREATED")
	} else {
		fmt.Fprintln(tw, "INSTANCE_ID\tNAME\tSTATUS\tENGINE\tCPU\tMEM(GB)\tDISK(GB)\tZONE\tCREATED")
	}
	for _, r := range rows {
		i := r.inst
		fields := []string{
			derefString(i.DBInstanceId),
			derefString(i.DBInstanceName),
			derefString(i.DBInstanceStatus),
			derefString(i.DBVersion),
			fmt.Sprintf("%d", derefUint64(i.DBInstanceCpu)),
			fmt.Sprintf("%d", derefUint64(i.DBInstanceMemory)),
			fmt.Sprintf("%d", derefUint64(i.DBInstanceStorage)),
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

func newCDBClient(c *Client, region string) (*cdb.Client, error) {
	if strings.TrimSpace(region) == "" {
		region = c.creds.Region
	}
	cred := common.NewCredential(c.creds.SecretID, c.creds.SecretKey)
	cpf := profile.NewClientProfile()
	cpf.HttpProfile.Endpoint = "cdb.tencentcloudapi.com"
	return cdb.NewClient(cred, region, cpf)
}

func newPostgresClient(c *Client, region string) (*postgres.Client, error) {
	if strings.TrimSpace(region) == "" {
		region = c.creds.Region
	}
	cred := common.NewCredential(c.creds.SecretID, c.creds.SecretKey)
	cpf := profile.NewClientProfile()
	cpf.HttpProfile.Endpoint = "postgres.tencentcloudapi.com"
	return postgres.NewClient(cred, region, cpf)
}

// mysqlStatus maps the integer Status field to a human label.
// 0=creating, 1=running, 4=isolating, 5=isolated, plus a fallback.
func mysqlStatus(p *int64) string {
	if p == nil {
		return "-"
	}
	switch *p {
	case 0:
		return "CREATING"
	case 1:
		return "RUNNING"
	case 4:
		return "ISOLATING"
	case 5:
		return "ISOLATED"
	default:
		return fmt.Sprintf("STATE-%d", *p)
	}
}

// mysqlWan returns a compact "yes:domain:port" or "-" depending on whether
// public network access is enabled.
func mysqlWan(i *cdb.InstanceInfo) string {
	if i.WanStatus == nil || *i.WanStatus != 1 {
		return "-"
	}
	domain := strings.TrimSpace(derefString(i.WanDomain))
	port := derefInt64(i.WanPort)
	if domain == "-" || domain == "" {
		return "ENABLED"
	}
	return fmt.Sprintf("%s:%d", domain, port)
}
