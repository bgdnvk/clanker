package tencent

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	cynosdb "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cynosdb/v20190107"
)

// listCynosDB prints every CynosDB (TDSQL-C serverless) cluster across regions.
func listCynosDB(c *Client, regions []string) error {
	multi := len(regions) > 1
	type row struct {
		region string
		c      *cynosdb.CynosdbCluster
	}
	var rows []row
	var warnings []string

	for _, r := range regions {
		client, err := newCynosDBClient(c, r)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: init cynosdb client: %v", r, err))
			continue
		}
		req := cynosdb.NewDescribeClustersRequest()
		var offset, limit int64 = 0, 100
		req.Offset = &offset
		req.Limit = &limit
		resp, err := client.DescribeClusters(req)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", r, friendlyError(err)))
			continue
		}
		if resp == nil || resp.Response == nil {
			continue
		}
		for _, cl := range resp.Response.ClusterSet {
			rows = append(rows, row{region: r, c: cl})
		}
	}

	header := fmt.Sprintf("TDSQL-C (CynosDB) Clusters (region=%s)", c.Region())
	if multi {
		header = fmt.Sprintf("TDSQL-C (CynosDB) Clusters (regions=%d)", len(regions))
	}
	fmt.Printf("%s:\n\n", header)
	if len(rows) == 0 {
		fmt.Println("  No CynosDB clusters found")
		printWarnings(warnings)
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	if multi {
		fmt.Fprintln(tw, "REGION\tCLUSTER_ID\tNAME\tSTATUS\tENGINE\tDB_VERSION\tINSTANCES\tZONE")
	} else {
		fmt.Fprintln(tw, "CLUSTER_ID\tNAME\tSTATUS\tENGINE\tDB_VERSION\tINSTANCES\tZONE")
	}
	for _, r := range rows {
		cl := r.c
		fields := []string{
			derefString(cl.ClusterId),
			derefString(cl.ClusterName),
			derefString(cl.Status),
			derefString(cl.DbType),
			derefString(cl.DbVersion),
			fmt.Sprintf("%d", derefInt64(cl.InstanceNum)),
			derefString(cl.Zone),
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

func newCynosDBClient(c *Client, region string) (*cynosdb.Client, error) {
	if strings.TrimSpace(region) == "" {
		region = c.creds.Region
	}
	cred := common.NewCredential(c.creds.SecretID, c.creds.SecretKey)
	cpf := newClientProfile("cynosdb.tencentcloudapi.com")
	return cynosdb.NewClient(cred, region, cpf)
}
