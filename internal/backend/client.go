package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// BackendURLs maps environment names to backend URLs
var BackendURLs = map[string]string{
	"testing":    "https://lychaz5ra6.execute-api.us-east-1.amazonaws.com/testing",
	"staging":    "https://2gjp7z6bxi.execute-api.us-east-1.amazonaws.com/staging",
	"production": "",
}

// Client is the HTTP client for the clanker backend API
type Client struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
	debug      bool
}

// NewClient creates a new backend client
func NewClient(apiKey string, debug bool) *Client {
	return &Client{
		apiKey:  apiKey,
		baseURL: ResolveBackendURL(),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		debug: debug,
	}
}

// NewClientWithURL creates a new backend client with a specific URL
func NewClientWithURL(apiKey, baseURL string, debug bool) *Client {
	return &Client{
		apiKey:  apiKey,
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		debug: debug,
	}
}

// doRequest performs an HTTP request with authentication
func (c *Client) doRequest(ctx context.Context, method, path string, body interface{}) ([]byte, error) {
	url := c.baseURL + path

	var bodyReader io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(jsonBody)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("X-API-Key", c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	if c.debug {
		fmt.Printf("[backend] %s %s\n", method, path)
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

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("unauthorized: invalid API key")
	}

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("not found: credential or resource does not exist")
	}

	if resp.StatusCode >= 400 {
		var apiResp APIResponse
		if err := json.Unmarshal(respBody, &apiResp); err == nil && apiResp.Error != "" {
			return nil, fmt.Errorf("API error: %s", apiResp.Error)
		}
		return nil, fmt.Errorf("API error: status %d", resp.StatusCode)
	}

	return respBody, nil
}

// GetAWSCredentials retrieves AWS credentials from the backend
func (c *Client) GetAWSCredentials(ctx context.Context) (*AWSCredentials, error) {
	respBody, err := c.doRequest(ctx, http.MethodGet, "/api/v1/cli/credentials/aws", nil)
	if err != nil {
		return nil, err
	}

	var response struct {
		Success bool `json:"success"`
		Data    struct {
			Provider    string         `json:"provider"`
			Credentials AWSCredentials `json:"credentials"`
		} `json:"data"`
	}

	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if !response.Success {
		return nil, fmt.Errorf("failed to get AWS credentials")
	}

	return &response.Data.Credentials, nil
}

// GetGCPCredentials retrieves GCP credentials from the backend
func (c *Client) GetGCPCredentials(ctx context.Context) (*GCPCredentials, error) {
	respBody, err := c.doRequest(ctx, http.MethodGet, "/api/v1/cli/credentials/gcp", nil)
	if err != nil {
		return nil, err
	}

	var response struct {
		Success bool `json:"success"`
		Data    struct {
			Provider    string         `json:"provider"`
			Credentials GCPCredentials `json:"credentials"`
		} `json:"data"`
	}

	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if !response.Success {
		return nil, fmt.Errorf("failed to get GCP credentials")
	}

	return &response.Data.Credentials, nil
}

// GetCloudflareCredentials retrieves Cloudflare credentials from the backend
func (c *Client) GetCloudflareCredentials(ctx context.Context) (*CloudflareCredentials, error) {
	respBody, err := c.doRequest(ctx, http.MethodGet, "/api/v1/cli/credentials/cloudflare", nil)
	if err != nil {
		return nil, err
	}

	var response struct {
		Success bool `json:"success"`
		Data    struct {
			Provider    string                `json:"provider"`
			Credentials CloudflareCredentials `json:"credentials"`
		} `json:"data"`
	}

	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if !response.Success {
		return nil, fmt.Errorf("failed to get Cloudflare credentials")
	}

	return &response.Data.Credentials, nil
}

// GetKubernetesCredentials retrieves Kubernetes credentials from the backend
func (c *Client) GetKubernetesCredentials(ctx context.Context) (*KubernetesCredentials, error) {
	respBody, err := c.doRequest(ctx, http.MethodGet, "/api/v1/cli/credentials/kubernetes", nil)
	if err != nil {
		return nil, err
	}

	var response struct {
		Success bool `json:"success"`
		Data    struct {
			Provider    string                `json:"provider"`
			Credentials KubernetesCredentials `json:"credentials"`
		} `json:"data"`
	}

	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if !response.Success {
		return nil, fmt.Errorf("failed to get Kubernetes credentials")
	}

	return &response.Data.Credentials, nil
}

// GetAzureCredentials retrieves Azure credentials from the backend
func (c *Client) GetAzureCredentials(ctx context.Context) (*AzureCredentials, error) {
	respBody, err := c.doRequest(ctx, http.MethodGet, "/api/v1/cli/credentials/azure", nil)
	if err != nil {
		return nil, err
	}

	var response struct {
		Success bool `json:"success"`
		Data    struct {
			Provider    string           `json:"provider"`
			Credentials AzureCredentials `json:"credentials"`
		} `json:"data"`
	}

	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if !response.Success {
		return nil, fmt.Errorf("failed to get Azure credentials")
	}

	return &response.Data.Credentials, nil
}

// StoreAWSCredentials stores AWS credentials in the backend
func (c *Client) StoreAWSCredentials(ctx context.Context, creds *AWSCredentials) error {
	body := map[string]interface{}{
		"provider":    "aws",
		"credentials": creds,
	}

	respBody, err := c.doRequest(ctx, http.MethodPut, "/api/v1/secrets/aws", body)
	if err != nil {
		return err
	}

	var response APIResponse
	if err := json.Unmarshal(respBody, &response); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if !response.Success {
		if response.Error != "" {
			return fmt.Errorf("failed to store credentials: %s", response.Error)
		}
		return fmt.Errorf("failed to store credentials")
	}

	return nil
}

// StoreGCPCredentials stores GCP credentials in the backend
func (c *Client) StoreGCPCredentials(ctx context.Context, creds *GCPCredentials) error {
	body := map[string]interface{}{
		"provider":    "gcp",
		"credentials": creds,
	}

	respBody, err := c.doRequest(ctx, http.MethodPut, "/api/v1/secrets/gcp", body)
	if err != nil {
		return err
	}

	var response APIResponse
	if err := json.Unmarshal(respBody, &response); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if !response.Success {
		if response.Error != "" {
			return fmt.Errorf("failed to store credentials: %s", response.Error)
		}
		return fmt.Errorf("failed to store credentials")
	}

	return nil
}

// StoreCloudflareCredentials stores Cloudflare credentials in the backend
func (c *Client) StoreCloudflareCredentials(ctx context.Context, creds *CloudflareCredentials) error {
	body := map[string]interface{}{
		"provider":    "cloudflare",
		"credentials": creds,
	}

	respBody, err := c.doRequest(ctx, http.MethodPut, "/api/v1/secrets/cloudflare", body)
	if err != nil {
		return err
	}

	var response APIResponse
	if err := json.Unmarshal(respBody, &response); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if !response.Success {
		if response.Error != "" {
			return fmt.Errorf("failed to store credentials: %s", response.Error)
		}
		return fmt.Errorf("failed to store credentials")
	}

	return nil
}

// StoreKubernetesCredentials stores Kubernetes credentials in the backend
// Note: kubernetes provider must be added to the backend for this to work
func (c *Client) StoreKubernetesCredentials(ctx context.Context, creds *KubernetesCredentials) error {
	body := map[string]interface{}{
		"provider":    "kubernetes",
		"credentials": creds,
	}

	respBody, err := c.doRequest(ctx, http.MethodPut, "/api/v1/secrets/kubernetes", body)
	if err != nil {
		return err
	}

	var response APIResponse
	if err := json.Unmarshal(respBody, &response); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if !response.Success {
		if response.Error != "" {
			return fmt.Errorf("failed to store credentials: %s", response.Error)
		}
		return fmt.Errorf("failed to store credentials")
	}

	return nil
}

// StoreAzureCredentials stores Azure credentials in the backend
func (c *Client) StoreAzureCredentials(ctx context.Context, creds *AzureCredentials) error {
	body := map[string]interface{}{
		"provider":    "azure",
		"credentials": creds,
	}

	respBody, err := c.doRequest(ctx, http.MethodPut, "/api/v1/secrets/azure", body)
	if err != nil {
		return err
	}

	var response APIResponse
	if err := json.Unmarshal(respBody, &response); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if !response.Success {
		if response.Error != "" {
			return fmt.Errorf("failed to store credentials: %s", response.Error)
		}
		return fmt.Errorf("failed to store credentials")
	}

	return nil
}

// ListCredentials lists all credentials stored in the backend
func (c *Client) ListCredentials(ctx context.Context) ([]CredentialEntry, error) {
	respBody, err := c.doRequest(ctx, http.MethodGet, "/api/v1/cli/credentials", nil)
	if err != nil {
		return nil, err
	}

	var response struct {
		Success bool              `json:"success"`
		Data    []CredentialEntry `json:"data"`
	}

	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if !response.Success {
		return nil, fmt.Errorf("failed to list credentials")
	}

	return response.Data, nil
}

// DeleteCredential deletes credentials for a provider from the backend
func (c *Client) DeleteCredential(ctx context.Context, provider CredentialProvider) error {
	path := fmt.Sprintf("/api/v1/secrets/%s", provider)
	respBody, err := c.doRequest(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return err
	}

	var response APIResponse
	if err := json.Unmarshal(respBody, &response); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if !response.Success {
		if response.Error != "" {
			return fmt.Errorf("failed to delete credentials: %s", response.Error)
		}
		return fmt.Errorf("failed to delete credentials")
	}

	return nil
}
