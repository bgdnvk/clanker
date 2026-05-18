package tencent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	tchttp "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/http"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
)

// serviceVersions maps Tencent service short names to their public API
// versions. The maker executor builds a CommonRequest from these.
//
// Versions are pinned to the values the Tencent SDK uses internally for each
// typed service. Adding a new service is a one-line change here plus an
// endpoint mapping in serviceEndpoints.
var serviceVersions = map[string]string{
	"cvm":      "2017-03-12",
	"vpc":      "2017-03-12",
	"cbs":      "2017-03-12",
	"clb":      "2018-03-17",
	"cdb":      "2017-03-20",
	"postgres": "2017-03-12",
	"redis":    "2018-04-12",
	"mongodb":  "2019-07-25",
	"tke":      "2018-05-25",
	"cos":      "", // COS uses object-storage REST API (different shape), not handled here
	"tag":      "2018-08-13",
	"cam":      "2019-01-16",
	"monitor":  "2018-07-24",
	"cls":      "2020-10-16",
}

// knownHallucinatedActions maps LLM-invented action names to the real Tencent
// action they probably meant. Curated empirically from Qwen3 maker output —
// keep this list short and high-confidence. The point is to fail FAST with a
// useful message instead of paying a Tencent round-trip for a definite typo.
//
// Key is "service.action" lowercased. Value is a human-readable hint string.
var knownHallucinatedActions = map[string]string{
	"monitor.getproductmetricdata":   "Use GetMonitorData. Tencent's Monitor service has no GetProductMetricData action.",
	"monitor.describemonitordata":    "Use GetMonitorData. Tencent's Monitor service has no DescribeMonitorData action.",
	"monitor.getproductmetrics":      "Use GetMonitorData or DescribeBaseMetrics.",
	"monitor.describemetricdata":     "Use GetMonitorData.",
	"monitor.describealarmpolicies":  "Use DescribeAlarmPolicy (singular).",
	"billing.describebillsummary":    "Use DescribeBillSummaryByProduct, DescribeBillSummaryByPayMode, or DescribeBillSummaryByRegion.",
	"billing.describeresourcebills":  "Use DescribeBillResourceSummary or DescribeBillDetail.",
	"cvm.describeinstancestate":      "Use DescribeInstancesStatus.",
	"cvm.listinstances":              "Use DescribeInstances (Tencent's discovery actions are always Describe*, never List*).",
	"vpc.listvpcs":                   "Use DescribeVpcs.",
	"cls.describetopics":             "Use DescribeTopics — make sure your service is `cls`, not `log`.",
}

// SendRaw makes a generic Tencent API call. Used by maker plan execution and
// any future agent path that wants to invoke an action by string name.
//
// The request body is the JSON-encoded action parameters Tencent's API expects
// (matching the SDK request struct fields). On success the returned string is
// the raw JSON response body.
func (c *Client) SendRaw(service, action, region, paramsJSON string) (string, error) {
	service = strings.ToLower(strings.TrimSpace(service))
	action = strings.TrimSpace(action)
	region = strings.TrimSpace(region)

	// Fail fast on known-invented action names with a "did you mean" hint.
	// Tencent's own error is the generic "InvalidAction" which doesn't help
	// the user (or the LLM, on a retry) understand what to fix.
	if hint, bad := knownHallucinatedActions[service+"."+strings.ToLower(action)]; bad {
		return "", fmt.Errorf("action %q is not a real Tencent %s API action — %s", action, service, hint)
	}

	version, ok := serviceVersions[service]
	if !ok {
		return "", fmt.Errorf("unsupported tencent service %q (known: cvm, vpc, cbs, clb, cdb, postgres, redis, mongodb, tke, tag, cam, monitor, cls)", service)
	}
	if version == "" {
		return "", fmt.Errorf("service %q does not use the generic action API (use service-specific path)", service)
	}
	if action == "" {
		return "", fmt.Errorf("action is required")
	}
	if region == "" {
		region = c.creds.Region
	}

	cred := common.NewCredential(c.creds.SecretID, c.creds.SecretKey)
	cpf := profile.NewClientProfile()
	cpf.HttpProfile.Endpoint = service + ".tencentcloudapi.com"
	client := common.NewCommonClient(cred, region, cpf)

	req := tchttp.NewCommonRequest(service, version, action)
	if strings.TrimSpace(paramsJSON) != "" {
		var params map[string]interface{}
		if err := json.Unmarshal([]byte(paramsJSON), &params); err != nil {
			return "", fmt.Errorf("invalid params JSON: %w", err)
		}
		if err := req.SetActionParameters(params); err != nil {
			return "", fmt.Errorf("set action parameters: %w", err)
		}
	}

	resp := tchttp.NewCommonResponse()
	if err := client.Send(req, resp); err != nil {
		return "", friendlyError(err)
	}
	return string(resp.GetBody()), nil
}
