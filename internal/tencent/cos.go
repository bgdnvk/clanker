package tencent

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"text/tabwriter"
	"time"

	cos "github.com/tencentyun/cos-go-sdk-v5"
)

// listCOSBuckets enumerates every COS bucket the credential can see.
// Unlike CVM/VPC/DB, COS uses an S3-style service endpoint that's not
// region-scoped — the service-level GET returns all buckets the credential
// owns across every region, so multi-region fan-out is not required here.
func listCOSBuckets(c *Client) error {
	client := cos.NewClient(nil, &http.Client{
		Timeout: 30 * time.Second,
		Transport: &cos.AuthorizationTransport{
			SecretID:  c.creds.SecretID,
			SecretKey: c.creds.SecretKey,
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, _, err := client.Service.Get(ctx)
	if err != nil {
		return fmt.Errorf("cos service get: %w", err)
	}

	fmt.Println("Tencent Cloud Object Storage (COS) Buckets:")
	fmt.Println()
	if resp == nil || len(resp.Buckets) == 0 {
		fmt.Println("  No COS buckets found")
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tREGION\tCREATED\tTYPE")
	for _, b := range resp.Buckets {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
			b.Name, b.Region, b.CreationDate, b.BucketType,
		)
	}
	return tw.Flush()
}
