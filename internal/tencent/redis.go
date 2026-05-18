package tencent

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
	redis "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/redis/v20180412"
)

// listRedis prints every TencentDB for Redis instance across regions.
// WanAddress is the security-critical field — when non-empty it means the
// instance is reachable from the public internet.
func listRedis(c *Client, regions []string) error {
	multi := len(regions) > 1
	type row struct {
		region string
		i      *redis.InstanceSet
	}
	var rows []row
	var warnings []string

	for _, r := range regions {
		client, err := newRedisClient(c, r)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: init redis client: %v", r, err))
			continue
		}
		req := redis.NewDescribeInstancesRequest()
		var offset, limit uint64 = 0, 100
		req.Offset = &offset
		req.Limit = &limit
		resp, err := client.DescribeInstances(req)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", r, friendlyError(err)))
			continue
		}
		if resp == nil || resp.Response == nil {
			continue
		}
		for _, inst := range resp.Response.InstanceSet {
			rows = append(rows, row{region: r, i: inst})
		}
	}

	header := fmt.Sprintf("TencentDB for Redis (region=%s)", c.Region())
	if multi {
		header = fmt.Sprintf("TencentDB for Redis (regions=%d)", len(regions))
	}
	fmt.Printf("%s:\n\n", header)
	if len(rows) == 0 {
		fmt.Println("  No Redis instances found")
		printWarnings(warnings)
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	if multi {
		fmt.Fprintln(tw, "REGION\tINSTANCE_ID\tNAME\tSTATUS\tSIZE_MB\tVIP:PORT\tPUBLIC\tCREATED")
	} else {
		fmt.Fprintln(tw, "INSTANCE_ID\tNAME\tSTATUS\tSIZE_MB\tVIP:PORT\tPUBLIC\tCREATED")
	}
	for _, r := range rows {
		i := r.i
		size := int64(0)
		if i.Size != nil {
			size = int64(*i.Size)
		}
		pub := "-"
		if w := strings.TrimSpace(derefString(i.WanAddress)); w != "" && w != "-" {
			pub = w
		}
		fields := []string{
			derefString(i.InstanceId),
			derefString(i.InstanceName),
			redisStatus(i.Status),
			fmt.Sprintf("%d", size),
			fmt.Sprintf("%s:%d", derefString(i.WanIp), derefInt64(i.Port)),
			pub,
			derefString(i.Createtime),
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

func newRedisClient(c *Client, region string) (*redis.Client, error) {
	if strings.TrimSpace(region) == "" {
		region = c.creds.Region
	}
	cred := common.NewCredential(c.creds.SecretID, c.creds.SecretKey)
	cpf := profile.NewClientProfile()
	cpf.HttpProfile.Endpoint = "redis.tencentcloudapi.com"
	return redis.NewClient(cred, region, cpf)
}

func redisStatus(p *int64) string {
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
		return "ISOLATED"
	case -3:
		return "PENDING_DELETE"
	default:
		return fmt.Sprintf("STATE-%d", *p)
	}
}
