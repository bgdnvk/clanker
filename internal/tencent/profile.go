package tencent

import (
	"context"
	"log"

	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
)

// gatherPageSize is the Tencent SDK's standard per-page limit for inventory
// Describe* calls. Most APIs cap their own response at 100; a few accept
// higher (CDB up to 2000) but 100 is the safe lowest-common-denominator.
const gatherPageSize = 100

// gatherMaxItems caps the total number of items any single gather function
// will accumulate across paginated calls. Production accounts can have
// thousands of resources, and we'd rather truncate the LLM context (with a
// visible warning) than stream a 50 MB JSON blob into the prompt window.
// When this fires, the caller logs "(showing first N of M)" alongside the
// data so consumers know they're seeing a partial view.
const gatherMaxItems = 1000

// ctxDone returns ctx.Err() if the caller has cancelled, otherwise nil.
// The Tencent SDK doesn't expose WithContext variants for its typed clients,
// so we can't actually interrupt a request in flight — but we can avoid
// firing the NEXT pagination page (or the next section of GetRelevantContext)
// after the caller has cancelled. Combined with the ReqTimeout in
// newClientProfile, this bounds the worst-case wall-clock cost of a
// cancelled gather to one in-flight SDK call.
func ctxDone(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

// logGatherTruncated reports that a paginated gather stopped early because
// it hit gatherMaxItems. Logged at the stdlib default logger (stderr) so
// operators see it in dev compose logs and clanker-api stderr in prod;
// kept off the JSON output so the wire shape stays a flat []item.
func logGatherTruncated(resourceType, region string, totalReported int64, accumulated int) {
	log.Printf("[tencent] %s in %s: truncated at %d of %d (gatherMaxItems cap; LLM context will be incomplete)",
		resourceType, region, accumulated, totalReported)
}

// tencentReqTimeoutSec bounds every Tencent SDK HTTP call. The SDK exposes
// no WithContext variants, so caller ctx cancellation can't actually
// interrupt a request mid-flight — but ReqTimeout ensures a hung call
// can't pin a request handler forever. Set generously enough that legitimate
// slow APIs (billing, audit-coverage scan) still complete.
const tencentReqTimeoutSec = 30

// newClientProfile builds the SDK ClientProfile every typed service client
// shares: a service endpoint plus the standard ReqTimeout. Use this from
// every per-service constructor so the timeout is uniform.
func newClientProfile(endpoint string) *profile.ClientProfile {
	cpf := profile.NewClientProfile()
	cpf.HttpProfile.Endpoint = endpoint
	cpf.HttpProfile.ReqTimeout = tencentReqTimeoutSec
	return cpf
}
