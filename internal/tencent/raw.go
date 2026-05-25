//go:generate go run gen_services.go

package tencent

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	tchttp "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/http"
)

// serviceVersions is generated from the vendored Tencent SDK by
// gen_services.go — see service_versions_gen.go. Don't edit it by hand;
// run `go generate ./internal/tencent/...` after upgrading the SDK.

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
		return "", fmt.Errorf("unsupported tencent service %q (known: %s)", service, knownServices())
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
	cpf := newClientProfile(service + ".tencentcloudapi.com")
	client := common.NewCommonClient(cred, region, cpf)

	req := tchttp.NewCommonRequest(service, version, action)
	if strings.TrimSpace(paramsJSON) != "" {
		if len(paramsJSON) > maxParamsJSONBytes {
			return "", fmt.Errorf("params JSON too large (%d bytes; cap %d)", len(paramsJSON), maxParamsJSONBytes)
		}
		var params map[string]interface{}
		if err := json.Unmarshal([]byte(paramsJSON), &params); err != nil {
			return "", fmt.Errorf("invalid params JSON: %w", err)
		}
		if err := checkParamsFieldSize(params, ""); err != nil {
			return "", err
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

// maxParamsJSONBytes / maxParamsFieldBytes bound the LLM-supplied paramsJSON
// for SendRaw. Effective ceiling without these would be the HTTP body limit
// (~1 MiB), which is far larger than any legitimate Tencent action payload.
// 64 KiB per field accommodates user-data scripts and policy documents while
// rejecting accidentally-pasted dumps that would inflate the LLM context.
const (
	maxParamsJSONBytes  = 256 * 1024 // total payload cap
	maxParamsFieldBytes = 64 * 1024  // per-string-field cap
)

// checkParamsFieldSize walks the parsed params and rejects any string field
// (recursing into nested maps and slices) that exceeds maxParamsFieldBytes.
// path is the dotted location used in the error message so the caller knows
// which field tripped the limit.
func checkParamsFieldSize(v interface{}, path string) error {
	switch x := v.(type) {
	case string:
		if len(x) > maxParamsFieldBytes {
			return fmt.Errorf("params field %q too large (%d bytes; cap %d)", path, len(x), maxParamsFieldBytes)
		}
	case map[string]interface{}:
		for k, val := range x {
			sub := k
			if path != "" {
				sub = path + "." + k
			}
			if err := checkParamsFieldSize(val, sub); err != nil {
				return err
			}
		}
	case []interface{}:
		for i, val := range x {
			sub := fmt.Sprintf("%s[%d]", path, i)
			if err := checkParamsFieldSize(val, sub); err != nil {
				return err
			}
		}
	}
	return nil
}

// knownServices returns a comma-separated, sorted list of services in the
// generated map — used in error messages so the list stays accurate as the
// SDK gains or drops services.
func knownServices() string {
	keys := make([]string, 0, len(serviceVersions))
	for k := range serviceVersions {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}
