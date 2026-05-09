package cost

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ScanBackendCandidatePorts mirrors the auto-detect range used by the
// clanker-cloud desktop app's frontend. The desktop binary's port may
// shift if 8080 is taken on the user's machine.
var ScanBackendCandidatePorts = []int{8080, 8081, 8082, 8083, 8084}

// DiscoverScanBackend probes the candidate ports on localhost to find
// a running clanker-cloud desktop backend that *also* has the
// /api/cost/scan endpoint registered. Returns the base URL (e.g.
// "http://127.0.0.1:8081") or empty string if no backend has the
// endpoint.
//
// We can't just probe /api/ping — the user may have an older desktop
// build running on one port and a newer one on another, and an older
// build won't have the scan endpoint. So we issue a GET against
// /api/cost/scan, which the new backend registers as a cheap
// "ScanCapabilities" probe handler returning 200 with metadata.
// Older builds without the endpoint return 404 and we move on.
//
// Probe is fast (250ms HTTP timeout per port, with a 100ms TCP pre-
// check) so the CLI doesn't hang when the user has no app running.
func DiscoverScanBackend(ctx context.Context, debug bool) string {
	httpC := &http.Client{Timeout: 250 * time.Millisecond}
	for _, port := range ScanBackendCandidatePorts {
		base := fmt.Sprintf("http://127.0.0.1:%d", port)
		// Optimisation: TCP-probe before HTTP so closed ports fail in
		// nanoseconds rather than waiting for the full HTTP timeout.
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
		if err != nil {
			continue
		}
		_ = conn.Close()
		// GET on the scan path: backends with our endpoint return
		// 200 with capability metadata. Older builds return 404 since
		// they have neither the GET probe nor the POST handler
		// registered.
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/cost/scan", nil)
		if err != nil {
			continue
		}
		resp, err := httpC.Do(req)
		if err != nil {
			if debug {
				fmt.Printf("[scan] backend probe %s: %v\n", base, err)
			}
			continue
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			if debug {
				fmt.Printf("[scan] backend at %s lacks /api/cost/scan (status %d)\n", base, resp.StatusCode)
			}
			continue
		}
		return base
	}
	return ""
}

// FetchScanReceipt calls the clanker-cloud backend's POST /api/cost/scan
// endpoint and returns a parsed ScanReceipt. The CLI uses this when a
// backend is reachable; otherwise it falls back to the local
// commitment-recommendation path (see ProjectSavingsToReceipt).
//
// `awsProfile` is sent as both the `profile` query param AND the
// X-AWS-Profile header to mirror how the desktop app threads creds —
// the backend reads either, so being explicit on both is harmless.
func (c *Client) FetchScanReceipt(ctx context.Context, mode, awsProfile, lookback, term string) (*ScanReceipt, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = "quick"
	}
	params := url.Values{}
	params.Set("mode", mode)
	if awsProfile != "" {
		params.Set("profile", awsProfile)
	}
	if lookback != "" {
		params.Set("lookback", lookback)
	}
	if term != "" {
		params.Set("term", term)
	}
	path := "/api/cost/scan?" + params.Encode()

	headers := map[string]string{"X-AWS-Profile": awsProfile}
	body, err := c.doRequest(ctx, http.MethodPost, path, nil, headers)
	if err != nil {
		return nil, fmt.Errorf("scan request: %w", err)
	}
	var receipt ScanReceipt
	if err := json.Unmarshal(body, &receipt); err != nil {
		return nil, fmt.Errorf("decode scan receipt: %w", err)
	}
	return &receipt, nil
}
