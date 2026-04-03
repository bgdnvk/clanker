package cost

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/viper"
)

const hetznerBaseURL = "https://api.hetzner.cloud/v1"

// HetznerProvider implements Provider for Hetzner Cloud.
// Since Hetzner has no billing API, costs are computed by enumerating
// resources and multiplying by catalog prices from the /pricing endpoint.
type HetznerProvider struct {
	apiToken   string
	httpClient *http.Client
	debug      bool
}

// NewHetznerProvider creates a new Hetzner cost provider.
func NewHetznerProvider(apiToken string, debug bool) (*HetznerProvider, error) {
	if apiToken == "" {
		apiToken = strings.TrimSpace(viper.GetString("hetzner.api_token"))
	}
	if apiToken == "" {
		apiToken = strings.TrimSpace(os.Getenv("HCLOUD_TOKEN"))
	}
	if apiToken == "" {
		return nil, fmt.Errorf("hetzner api token not configured")
	}

	return &HetznerProvider{
		apiToken:   apiToken,
		httpClient: &http.Client{Timeout: 15 * time.Second},
		debug:      debug,
	}, nil
}

func (p *HetznerProvider) GetName() string {
	return "hetzner"
}

func (p *HetznerProvider) IsConfigured() bool {
	return p.apiToken != ""
}

func (p *HetznerProvider) doGet(ctx context.Context, endpoint string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", hetznerBaseURL+endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+p.apiToken)
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("hetzner api error: status %d", resp.StatusCode)
	}
	return data, nil
}

// hetzner pricing and resource response types

type hzPricingResponse struct {
	Pricing struct {
		ServerTypes []struct {
			Name   string `json:"name"`
			Prices []struct {
				Location     string `json:"location"`
				PriceMonthly struct {
					Gross string `json:"gross"`
				} `json:"price_monthly"`
			} `json:"prices"`
		} `json:"server_types"`
		Volume struct {
			PricePerGBMonthly struct {
				Gross string `json:"gross"`
			} `json:"price_per_gb_monthly"`
		} `json:"volume"`
		FloatingIP struct {
			PriceMonthly struct {
				Gross string `json:"gross"`
			} `json:"price_monthly"`
		} `json:"floating_ip"`
		PrimaryIPs struct {
			Prices []struct {
				Type         string `json:"type"`
				PriceMonthly struct {
					Gross string `json:"gross"`
				} `json:"price_monthly"`
			} `json:"prices"`
		} `json:"primary_ips"`
		LoadBalancerTypes []struct {
			Name   string `json:"name"`
			Prices []struct {
				Location     string `json:"location"`
				PriceMonthly struct {
					Gross string `json:"gross"`
				} `json:"price_monthly"`
			} `json:"prices"`
		} `json:"load_balancer_types"`
	} `json:"pricing"`
}

type hzServersResp struct {
	Servers []struct {
		ServerType struct {
			Name string `json:"name"`
		} `json:"server_type"`
		Datacenter struct {
			Location struct {
				Name string `json:"name"`
			} `json:"location"`
		} `json:"datacenter"`
	} `json:"servers"`
}

type hzVolumesResp struct {
	Volumes []struct {
		Size int `json:"size"`
	} `json:"volumes"`
}

type hzLoadBalancersResp struct {
	LoadBalancers []struct {
		LoadBalancerType struct {
			Name string `json:"name"`
		} `json:"load_balancer_type"`
		Location struct {
			Name string `json:"name"`
		} `json:"location"`
	} `json:"load_balancers"`
}

type hzFloatingIPsResp struct {
	FloatingIPs []struct {
		ID int `json:"id"`
	} `json:"floating_ips"`
}

type hzPrimaryIPsResp struct {
	PrimaryIPs []struct {
		Type string `json:"type"`
	} `json:"primary_ips"`
}

func parseHzPrice(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}

func (p *HetznerProvider) GetCosts(ctx context.Context, start, end time.Time) (*ProviderCost, error) {
	// Fetch pricing catalog
	pricingData, err := p.doGet(ctx, "/pricing")
	if err != nil {
		return nil, fmt.Errorf("failed to fetch pricing: %w", err)
	}
	var pricing hzPricingResponse
	if err := json.Unmarshal(pricingData, &pricing); err != nil {
		return nil, fmt.Errorf("failed to parse pricing: %w", err)
	}

	// Build lookup maps
	serverPrices := make(map[string]float64)
	for _, st := range pricing.Pricing.ServerTypes {
		for _, p := range st.Prices {
			serverPrices[st.Name+"_"+p.Location] = parseHzPrice(p.PriceMonthly.Gross)
		}
	}
	volumePricePerGB := parseHzPrice(pricing.Pricing.Volume.PricePerGBMonthly.Gross)
	floatingIPPrice := parseHzPrice(pricing.Pricing.FloatingIP.PriceMonthly.Gross)

	var primaryIPPrice float64
	for _, pip := range pricing.Pricing.PrimaryIPs.Prices {
		if pip.Type == "ipv4" {
			primaryIPPrice = parseHzPrice(pip.PriceMonthly.Gross)
			break
		}
	}

	lbPrices := make(map[string]float64)
	for _, lbt := range pricing.Pricing.LoadBalancerTypes {
		for _, lp := range lbt.Prices {
			lbPrices[lbt.Name+"_"+lp.Location] = parseHzPrice(lp.PriceMonthly.Gross)
		}
	}

	// Fetch resources and compute costs
	var services []ServiceCost
	var totalCost float64

	// Servers
	serversData, err := p.doGet(ctx, "/servers?per_page=50")
	if err == nil {
		var resp hzServersResp
		if json.Unmarshal(serversData, &resp) == nil {
			serverCost := 0.0
			for _, s := range resp.Servers {
				key := s.ServerType.Name + "_" + s.Datacenter.Location.Name
				serverCost += serverPrices[key]
			}
			if serverCost > 0 || len(resp.Servers) > 0 {
				services = append(services, ServiceCost{
					Service:       "Cloud Servers",
					Cost:          serverCost,
					ResourceCount: len(resp.Servers),
				})
				totalCost += serverCost
			}
		}
	}

	// Volumes
	volumesData, err := p.doGet(ctx, "/volumes?per_page=50")
	if err == nil {
		var resp hzVolumesResp
		if json.Unmarshal(volumesData, &resp) == nil {
			volCost := 0.0
			for _, v := range resp.Volumes {
				volCost += volumePricePerGB * float64(v.Size)
			}
			if volCost > 0 || len(resp.Volumes) > 0 {
				services = append(services, ServiceCost{
					Service:       "Volumes",
					Cost:          volCost,
					ResourceCount: len(resp.Volumes),
				})
				totalCost += volCost
			}
		}
	}

	// Load Balancers
	lbData, err := p.doGet(ctx, "/load_balancers?per_page=50")
	if err == nil {
		var resp hzLoadBalancersResp
		if json.Unmarshal(lbData, &resp) == nil {
			lbCost := 0.0
			for _, lb := range resp.LoadBalancers {
				key := lb.LoadBalancerType.Name + "_" + lb.Location.Name
				lbCost += lbPrices[key]
			}
			if lbCost > 0 || len(resp.LoadBalancers) > 0 {
				services = append(services, ServiceCost{
					Service:       "Load Balancers",
					Cost:          lbCost,
					ResourceCount: len(resp.LoadBalancers),
				})
				totalCost += lbCost
			}
		}
	}

	// Floating IPs
	fipData, err := p.doGet(ctx, "/floating_ips?per_page=50")
	if err == nil {
		var resp hzFloatingIPsResp
		if json.Unmarshal(fipData, &resp) == nil {
			fipCost := floatingIPPrice * float64(len(resp.FloatingIPs))
			if fipCost > 0 || len(resp.FloatingIPs) > 0 {
				services = append(services, ServiceCost{
					Service:       "Floating IPs",
					Cost:          fipCost,
					ResourceCount: len(resp.FloatingIPs),
				})
				totalCost += fipCost
			}
		}
	}

	// Primary IPs
	pipData, err := p.doGet(ctx, "/primary_ips?per_page=50")
	if err == nil {
		var resp hzPrimaryIPsResp
		if json.Unmarshal(pipData, &resp) == nil {
			pipCost := primaryIPPrice * float64(len(resp.PrimaryIPs))
			if pipCost > 0 || len(resp.PrimaryIPs) > 0 {
				services = append(services, ServiceCost{
					Service:       "Primary IPs",
					Cost:          pipCost,
					ResourceCount: len(resp.PrimaryIPs),
				})
				totalCost += pipCost
			}
		}
	}

	return &ProviderCost{
		Provider:         "hetzner",
		TotalCost:        totalCost,
		Currency:         "EUR",
		ServiceBreakdown: services,
	}, nil
}

func (p *HetznerProvider) GetCostsByService(ctx context.Context, start, end time.Time) ([]ServiceCost, error) {
	costs, err := p.GetCosts(ctx, start, end)
	if err != nil {
		return nil, err
	}
	return costs.ServiceBreakdown, nil
}

func (p *HetznerProvider) GetDailyTrend(ctx context.Context, start, end time.Time) ([]DailyCost, error) {
	// Hetzner has no historical billing API, so daily trend is not available.
	return nil, nil
}
