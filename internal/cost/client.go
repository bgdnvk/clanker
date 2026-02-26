package cost

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Client is the HTTP client for cost data
type Client struct {
	baseURL    string
	httpClient *http.Client
	debug      bool
}

// NewClient creates a new cost client
func NewClient(baseURL string, debug bool) *Client {
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
		debug: debug,
	}
}

// doRequest performs an HTTP request
func (c *Client) doRequest(ctx context.Context, method, path string, body interface{}) ([]byte, error) {
	reqURL := c.baseURL + path

	var bodyReader io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(jsonBody)
	}

	req, err := http.NewRequestWithContext(ctx, method, reqURL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	if c.debug {
		fmt.Printf("[cost] %s %s\n", method, path)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("API error: status %d - %s", resp.StatusCode, string(respBody))
	}

	return respBody, nil
}

// GetSummary fetches the cost summary
func (c *Client) GetSummary(ctx context.Context, startDate, endDate string) (*CostSummary, error) {
	params := url.Values{}
	if startDate != "" {
		params.Set("startDate", startDate)
	}
	if endDate != "" {
		params.Set("endDate", endDate)
	}

	path := "/api/cost/summary"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}

	respBody, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}

	var summary CostSummary
	if err := json.Unmarshal(respBody, &summary); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &summary, nil
}

// GetByProvider fetches costs for a specific provider
func (c *Client) GetByProvider(ctx context.Context, provider, startDate, endDate string) (*ProviderCost, error) {
	params := url.Values{}
	if startDate != "" {
		params.Set("startDate", startDate)
	}
	if endDate != "" {
		params.Set("endDate", endDate)
	}

	path := fmt.Sprintf("/api/cost/provider/%s", provider)
	if len(params) > 0 {
		path += "?" + params.Encode()
	}

	respBody, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}

	var cost ProviderCost
	if err := json.Unmarshal(respBody, &cost); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &cost, nil
}

// GetServices fetches cost breakdown by service
func (c *Client) GetServices(ctx context.Context, provider, startDate, endDate string) ([]ServiceCost, error) {
	params := url.Values{}
	if provider != "" && provider != "all" {
		params.Set("provider", provider)
	}
	if startDate != "" {
		params.Set("startDate", startDate)
	}
	if endDate != "" {
		params.Set("endDate", endDate)
	}

	path := "/api/cost/services"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}

	respBody, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}

	var response struct {
		Services []ServiceCost `json:"services"`
	}
	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return response.Services, nil
}

// GetTrend fetches the cost trend data
func (c *Client) GetTrend(ctx context.Context, startDate, endDate, granularity string) (*CostTrendResponse, error) {
	params := url.Values{}
	if startDate != "" {
		params.Set("startDate", startDate)
	}
	if endDate != "" {
		params.Set("endDate", endDate)
	}
	if granularity != "" {
		params.Set("granularity", granularity)
	}

	path := "/api/cost/trend"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}

	respBody, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}

	var trend CostTrendResponse
	if err := json.Unmarshal(respBody, &trend); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &trend, nil
}

// GetForecast fetches the cost forecast
func (c *Client) GetForecast(ctx context.Context) (*CostForecast, error) {
	respBody, err := c.doRequest(ctx, http.MethodGet, "/api/cost/forecast", nil)
	if err != nil {
		return nil, err
	}

	var forecast CostForecast
	if err := json.Unmarshal(respBody, &forecast); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &forecast, nil
}

// GetAnomalies fetches cost anomalies
func (c *Client) GetAnomalies(ctx context.Context) (*CostAnomaliesResponse, error) {
	respBody, err := c.doRequest(ctx, http.MethodGet, "/api/cost/anomalies", nil)
	if err != nil {
		return nil, err
	}

	var anomalies CostAnomaliesResponse
	if err := json.Unmarshal(respBody, &anomalies); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &anomalies, nil
}

// GetLLMUsage fetches LLM usage data
func (c *Client) GetLLMUsage(ctx context.Context, startDate, endDate string) (*LLMCostSummary, error) {
	params := url.Values{}
	if startDate != "" {
		params.Set("startDate", startDate)
	}
	if endDate != "" {
		params.Set("endDate", endDate)
	}

	path := "/api/cost/llm"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}

	respBody, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}

	var usage LLMCostSummary
	if err := json.Unmarshal(respBody, &usage); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &usage, nil
}

// GetTags fetches cost grouped by tags
func (c *Client) GetTags(ctx context.Context, tagKey, startDate, endDate string) (*TagsResponse, error) {
	params := url.Values{}
	if tagKey != "" {
		params.Set("tagKey", tagKey)
	}
	if startDate != "" {
		params.Set("startDate", startDate)
	}
	if endDate != "" {
		params.Set("endDate", endDate)
	}

	path := "/api/cost/tags"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}

	respBody, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}

	var tags TagsResponse
	if err := json.Unmarshal(respBody, &tags); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &tags, nil
}

// GetProviders fetches the list of configured providers
func (c *Client) GetProviders(ctx context.Context) (*ProvidersResponse, error) {
	respBody, err := c.doRequest(ctx, http.MethodGet, "/api/cost/providers", nil)
	if err != nil {
		return nil, err
	}

	var providers ProvidersResponse
	if err := json.Unmarshal(respBody, &providers); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &providers, nil
}

// Export exports cost data in the specified format
func (c *Client) Export(ctx context.Context, req *CostExportRequest) ([]byte, error) {
	respBody, err := c.doRequest(ctx, http.MethodPost, "/api/cost/export", req)
	if err != nil {
		return nil, err
	}

	return respBody, nil
}
