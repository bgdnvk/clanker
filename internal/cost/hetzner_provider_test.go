package cost

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHetznerProviderGetName(t *testing.T) {
	p := &HetznerProvider{apiToken: "test"}
	if p.GetName() != "hetzner" {
		t.Errorf("GetName() = %q, want %q", p.GetName(), "hetzner")
	}
}

func TestHetznerProviderIsConfigured(t *testing.T) {
	p := &HetznerProvider{apiToken: "test"}
	if !p.IsConfigured() {
		t.Error("IsConfigured() = false, want true")
	}

	p2 := &HetznerProvider{apiToken: ""}
	if p2.IsConfigured() {
		t.Error("IsConfigured() = true with empty token, want false")
	}
}

func TestParseHzPrice(t *testing.T) {
	tests := []struct {
		input    string
		expected float64
	}{
		{"4.5100", 4.51},
		{"0.0524", 0.0524},
		{"", 0},
		{"invalid", 0},
		{"  3.79  ", 3.79},
	}

	for _, tt := range tests {
		got := parseHzPrice(tt.input)
		if math.Abs(got-tt.expected) > 0.0001 {
			t.Errorf("parseHzPrice(%q) = %f, want %f", tt.input, got, tt.expected)
		}
	}
}

func TestHetznerProviderGetCosts(t *testing.T) {
	mux := http.NewServeMux()

	mux.HandleFunc("/v1/pricing", func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"pricing": map[string]interface{}{
				"server_types": []map[string]interface{}{
					{
						"name": "cx22",
						"prices": []map[string]interface{}{
							{
								"location":      "fsn1",
								"price_monthly": map[string]string{"gross": "4.5100"},
							},
						},
					},
				},
				"volume": map[string]interface{}{
					"price_per_gb_monthly": map[string]string{"gross": "0.0524"},
				},
				"floating_ip": map[string]interface{}{
					"price_monthly": map[string]string{"gross": "4.7600"},
				},
				"primary_ips": map[string]interface{}{
					"prices": []map[string]interface{}{
						{
							"type":          "ipv4",
							"price_monthly": map[string]string{"gross": "4.2800"},
						},
					},
				},
				"load_balancer_types": []map[string]interface{}{
					{
						"name": "lb11",
						"prices": []map[string]interface{}{
							{
								"location":      "fsn1",
								"price_monthly": map[string]string{"gross": "6.3500"},
							},
						},
					},
				},
			},
		}
		json.NewEncoder(w).Encode(resp)
	})

	mux.HandleFunc("/v1/servers", func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"servers": []map[string]interface{}{
				{
					"server_type": map[string]string{"name": "cx22"},
					"datacenter":  map[string]interface{}{"location": map[string]string{"name": "fsn1"}},
				},
				{
					"server_type": map[string]string{"name": "cx22"},
					"datacenter":  map[string]interface{}{"location": map[string]string{"name": "fsn1"}},
				},
			},
		}
		json.NewEncoder(w).Encode(resp)
	})

	mux.HandleFunc("/v1/volumes", func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"volumes": []map[string]interface{}{
				{"size": 50},
			},
		}
		json.NewEncoder(w).Encode(resp)
	})

	mux.HandleFunc("/v1/load_balancers", func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{"load_balancers": []interface{}{}}
		json.NewEncoder(w).Encode(resp)
	})

	mux.HandleFunc("/v1/floating_ips", func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"floating_ips": []map[string]interface{}{
				{"id": 1},
			},
		}
		json.NewEncoder(w).Encode(resp)
	})

	mux.HandleFunc("/v1/primary_ips", func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"primary_ips": []map[string]interface{}{
				{"type": "ipv4"},
				{"type": "ipv4"},
			},
		}
		json.NewEncoder(w).Encode(resp)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	// We need to override the base URL. Since it's a package constant, we create
	// a provider with a custom httpClient that redirects to our test server.
	// Instead, we use a transport that rewrites the URL.
	transport := &rewriteTransport{
		base:    http.DefaultTransport,
		testURL: server.URL,
	}

	provider := &HetznerProvider{
		apiToken:   "test-token",
		httpClient: &http.Client{Transport: transport, Timeout: 5 * time.Second},
		debug:      false,
	}

	ctx := context.Background()
	now := time.Now()
	costs, err := provider.GetCosts(ctx, now.AddDate(0, 0, -30), now)
	if err != nil {
		t.Fatalf("GetCosts failed: %v", err)
	}

	if costs.Provider != "hetzner" {
		t.Errorf("Provider = %q, want %q", costs.Provider, "hetzner")
	}

	// 2 servers * 4.51 = 9.02
	// 1 volume * 50GB * 0.0524 = 2.62
	// 1 floating IP * 4.76 = 4.76
	// 2 primary IPs * 4.28 = 8.56
	// Total = 24.96
	expectedTotal := 9.02 + 2.62 + 4.76 + 8.56
	if math.Abs(costs.TotalCost-expectedTotal) > 0.1 {
		t.Errorf("TotalCost = %f, want ~%f", costs.TotalCost, expectedTotal)
	}

	if len(costs.ServiceBreakdown) != 4 {
		t.Errorf("ServiceBreakdown has %d entries, want 4", len(costs.ServiceBreakdown))
	}
}

func TestHetznerProviderGetDailyTrend(t *testing.T) {
	p := &HetznerProvider{apiToken: "test"}
	trend, err := p.GetDailyTrend(context.Background(), time.Now(), time.Now())
	if err != nil {
		t.Fatalf("GetDailyTrend failed: %v", err)
	}
	if trend != nil {
		t.Errorf("GetDailyTrend should return nil, got %v", trend)
	}
}

// rewriteTransport redirects requests from the hetzner API base to the test server.
type rewriteTransport struct {
	base    http.RoundTripper
	testURL string // e.g. "http://127.0.0.1:12345"
}

func (rt *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Rewrite: https://api.hetzner.cloud/v1/pricing -> http://testserver/v1/pricing
	path := req.URL.Path // e.g. "/v1/pricing"
	newURL := rt.testURL + path
	if req.URL.RawQuery != "" {
		newURL += "?" + req.URL.RawQuery
	}
	newReq, err := http.NewRequestWithContext(req.Context(), req.Method, newURL, req.Body)
	if err != nil {
		return nil, err
	}
	newReq.Header = req.Header
	return rt.base.RoundTrip(newReq)
}
