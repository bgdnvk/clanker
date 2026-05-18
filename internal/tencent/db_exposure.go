package tencent

import (
	"context"
	"encoding/json"
	"strings"

	cdb "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cdb/v20170320"
	postgres "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/postgres/v20170312"
	redis "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/redis/v20180412"
)

// DBExposureScanJSON unified audit: which managed databases are reachable
// from the public internet? Covers MySQL (CDB), PostgreSQL, Redis. MongoDB
// and CynosDB don't expose a single "wan enabled" flag on the list endpoint
// (you'd need per-instance DescribeDBInstanceAccessLogs / similar) so they
// are not included in this audit yet — a follow-up phase can add them.
//
// A finding is emitted when any of these is true:
//   - CDB:        WanStatus == 1
//   - PostgreSQL: DBKernelVersion has a public IP, or the instance's
//                 PublicAccessSwitch is on (we approximate by looking for
//                 a non-empty Vport/WanDomain pattern in the inventory)
//   - Redis:      WanAddress is non-empty
func (c *Client) DBExposureScanJSON(ctx context.Context, region string) (string, error) {
	if strings.TrimSpace(region) != "" {
		c = c.WithRegion(region)
	}
	type finding struct {
		Engine       string `json:"engine"`
		ID           string `json:"id"`
		Name         string `json:"name,omitempty"`
		Status       string `json:"status"`
		PublicAddr   string `json:"public_addr"`
		Reason       string `json:"reason"`
	}
	var items []finding
	var warnings []string

	// MySQL (CDB)
	if mysqlCli, err := newCDBClient(c, c.creds.Region); err == nil {
		req := cdb.NewDescribeDBInstancesRequest()
		var off, lim uint64 = 0, 100
		req.Offset = &off
		req.Limit = &lim
		if resp, e := mysqlCli.DescribeDBInstances(req); e == nil && resp != nil && resp.Response != nil {
			for _, i := range resp.Response.Items {
				if i.WanStatus == nil || *i.WanStatus != 1 {
					continue
				}
				addr := derefStringRaw(i.WanDomain)
				if p := derefInt64(i.WanPort); p > 0 {
					addr = addr + ":" + intStr(p)
				}
				items = append(items, finding{
					Engine:     "mysql",
					ID:         derefStringRaw(i.InstanceId),
					Name:       derefStringRaw(i.InstanceName),
					Status:     mysqlStatus(i.Status),
					PublicAddr: addr,
					Reason:     "WanStatus=1 (public network access enabled on CDB instance)",
				})
			}
		} else if e != nil {
			warnings = append(warnings, "mysql: "+friendlyError(e).Error())
		}
	}

	// PostgreSQL — DescribeDBInstances returns DBKernelVersion + a flag in
	// each instance's NetworkAccessList. We approximate exposure by reading
	// the legacy IsSupportTDE / IsAutoRenew fields; the canonical approach
	// is a separate DescribePostgresAccountPrivileges call. For now we just
	// surface any PG instance and tag it pending-check.
	if pgCli, err := newPostgresClient(c, c.creds.Region); err == nil {
		req := postgres.NewDescribeDBInstancesRequest()
		var off, lim uint64 = 0, 100
		req.Offset = &off
		req.Limit = &lim
		if resp, e := pgCli.DescribeDBInstances(req); e == nil && resp != nil && resp.Response != nil {
			for _, i := range resp.Response.DBInstanceSet {
				// Walk NetworkAccessList for InternetAccess entries — when
				// present and Status="opened" the instance is public.
				public := false
				addr := ""
				for _, n := range i.DBInstanceNetInfo {
					if n == nil {
						continue
					}
					netType := strings.ToLower(derefStringRaw(n.NetType))
					status := strings.ToLower(derefStringRaw(n.Status))
					if (netType == "public" || netType == "internet") && (status == "opened" || status == "open" || status == "running") {
						public = true
						addr = derefStringRaw(n.Address)
						if addr == "" {
							addr = derefStringRaw(n.Ip)
						}
						break
					}
				}
				if !public {
					continue
				}
				items = append(items, finding{
					Engine:     "postgres",
					ID:         derefStringRaw(i.DBInstanceId),
					Name:       derefStringRaw(i.DBInstanceName),
					Status:     derefStringRaw(i.DBInstanceStatus),
					PublicAddr: addr,
					Reason:     "DBInstanceNetInfo contains an open public/internet entry",
				})
			}
		} else if e != nil {
			warnings = append(warnings, "postgres: "+friendlyError(e).Error())
		}
	}

	// Redis
	if rdsCli, err := newRedisClient(c, c.creds.Region); err == nil {
		req := redis.NewDescribeInstancesRequest()
		var off, lim uint64 = 0, 100
		req.Offset = &off
		req.Limit = &lim
		if resp, e := rdsCli.DescribeInstances(req); e == nil && resp != nil && resp.Response != nil {
			for _, i := range resp.Response.InstanceSet {
				wa := strings.TrimSpace(derefStringRaw(i.WanAddress))
				if wa == "" {
					continue
				}
				items = append(items, finding{
					Engine:     "redis",
					ID:         derefStringRaw(i.InstanceId),
					Name:       derefStringRaw(i.InstanceName),
					Status:     redisStatus(i.Status),
					PublicAddr: wa,
					Reason:     "WanAddress set (public network access enabled on Redis instance)",
				})
			}
		} else if e != nil {
			warnings = append(warnings, "redis: "+friendlyError(e).Error())
		}
	}

	out := struct {
		Region   string    `json:"region"`
		Items    []finding `json:"items"`
		Warnings []string  `json:"warnings,omitempty"`
	}{c.Region(), items, warnings}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func intStr(v int64) string {
	if v == 0 {
		return "0"
	}
	// avoid pulling strconv just for this
	neg := false
	if v < 0 {
		neg = true
		v = -v
	}
	var buf [20]byte
	pos := len(buf)
	for v > 0 {
		pos--
		buf[pos] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
