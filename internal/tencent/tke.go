package tencent

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	tke "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/tke/v20180525"
)

// listTKEClusters prints every TKE cluster across the given regions.
func listTKEClusters(c *Client, regions []string) error {
	multi := len(regions) > 1
	type row struct {
		region  string
		cluster *tke.Cluster
	}
	var rows []row
	var warnings []string

	for _, r := range regions {
		client, err := newTKEClient(c, r)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: init tke client: %v", r, err))
			continue
		}
		req := tke.NewDescribeClustersRequest()
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
		for _, cl := range resp.Response.Clusters {
			rows = append(rows, row{region: r, cluster: cl})
		}
	}

	header := fmt.Sprintf("Tencent Kubernetes Engine clusters (region=%s)", c.Region())
	if multi {
		header = fmt.Sprintf("Tencent Kubernetes Engine clusters (regions=%d)", len(regions))
	}
	fmt.Printf("%s:\n\n", header)
	if len(rows) == 0 {
		fmt.Println("  No TKE clusters found")
		printWarnings(warnings)
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	if multi {
		fmt.Fprintln(tw, "REGION\tCLUSTER_ID\tNAME\tSTATUS\tK8S_VER\tTYPE\tNODES\tNETWORK\tCREATED")
	} else {
		fmt.Fprintln(tw, "CLUSTER_ID\tNAME\tSTATUS\tK8S_VER\tTYPE\tNODES\tNETWORK\tCREATED")
	}
	for _, r := range rows {
		cl := r.cluster
		network := "-"
		if cl.ClusterNetworkSettings != nil && cl.ClusterNetworkSettings.VpcId != nil {
			network = *cl.ClusterNetworkSettings.VpcId
		}
		fields := []string{
			derefString(cl.ClusterId),
			derefString(cl.ClusterName),
			derefString(cl.ClusterStatus),
			derefString(cl.ClusterVersion),
			derefString(cl.ClusterType),
			fmt.Sprintf("%d", derefUint64(cl.ClusterNodeNum)),
			network,
			derefString(cl.CreatedTime),
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

// getTKEKubeconfig fetches a kubeconfig YAML for a single cluster and prints
// it on stdout. When public is true, the externally-routable endpoint is
// returned; otherwise the VPC-internal endpoint.
//
// The cluster's region must match the client's region — kubeconfig fetch is
// region-scoped and the API will 404 if the cluster lives elsewhere.
func getTKEKubeconfig(c *Client, clusterID string, public bool) error {
	client, err := newTKEClient(c, c.creds.Region)
	if err != nil {
		return fmt.Errorf("init tke client: %w", err)
	}
	req := tke.NewDescribeClusterKubeconfigRequest()
	req.ClusterId = &clusterID
	req.IsExtranet = &public
	resp, err := client.DescribeClusterKubeconfig(req)
	if err != nil {
		return fmt.Errorf("DescribeClusterKubeconfig: %w", friendlyError(err))
	}
	if resp == nil || resp.Response == nil || resp.Response.Kubeconfig == nil {
		return fmt.Errorf("empty kubeconfig response for %s", clusterID)
	}
	fmt.Print(*resp.Response.Kubeconfig)
	if !strings.HasSuffix(*resp.Response.Kubeconfig, "\n") {
		fmt.Println()
	}
	return nil
}

// newDescribeKubeconfigReq builds a TKE DescribeClusterKubeconfig request.
// Extracted so both the CLI command and the HTTP API layer can share the
// construction without exporting an SDK request type from this package.
func newDescribeKubeconfigReq(clusterID string, public bool) *tke.DescribeClusterKubeconfigRequest {
	req := tke.NewDescribeClusterKubeconfigRequest()
	req.ClusterId = &clusterID
	req.IsExtranet = &public
	return req
}

func newTKEClient(c *Client, region string) (*tke.Client, error) {
	if strings.TrimSpace(region) == "" {
		region = c.creds.Region
	}
	cred := common.NewCredential(c.creds.SecretID, c.creds.SecretKey)
	cpf := newClientProfile("tke.tencentcloudapi.com")
	return tke.NewClient(cred, region, cpf)
}
