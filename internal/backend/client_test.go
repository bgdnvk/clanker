package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewClient(t *testing.T) {
	client := NewClient("test-api-key", false)
	if client == nil {
		t.Fatal("NewClient returned nil")
	}
	if client.apiKey != "test-api-key" {
		t.Errorf("expected apiKey 'test-api-key', got '%s'", client.apiKey)
	}
}

func TestNewClientWithURL(t *testing.T) {
	client := NewClientWithURL("test-api-key", "https://custom.example.com", false)
	if client == nil {
		t.Fatal("NewClientWithURL returned nil")
	}
	if client.baseURL != "https://custom.example.com" {
		t.Errorf("expected baseURL 'https://custom.example.com', got '%s'", client.baseURL)
	}
}

func TestGetAWSCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		if r.URL.Path != "/api/v1/cli/credentials/aws/raw" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		response := map[string]interface{}{
			"success": true,
			"data": map[string]interface{}{
				"credentials": map[string]string{
					"access_key_id":     "AKIAIOSFODNN7EXAMPLE",
					"secret_access_key": "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
					"region":            "us-east-1",
					"session_token":     "",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	client := NewClientWithURL("test-key", server.URL, false)
	creds, err := client.GetAWSCredentials(context.Background())
	if err != nil {
		t.Fatalf("GetAWSCredentials failed: %v", err)
	}

	if creds.AccessKeyID != "AKIAIOSFODNN7EXAMPLE" {
		t.Errorf("expected AccessKeyID 'AKIAIOSFODNN7EXAMPLE', got '%s'", creds.AccessKeyID)
	}
	if creds.Region != "us-east-1" {
		t.Errorf("expected Region 'us-east-1', got '%s'", creds.Region)
	}
}

func TestGetAWSCredentials_Unauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	client := NewClientWithURL("invalid-key", server.URL, false)
	_, err := client.GetAWSCredentials(context.Background())
	if err == nil {
		t.Fatal("expected error for unauthorized request")
	}
}

func TestListCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/cli/credentials" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		response := map[string]interface{}{
			"success": true,
			"data": []map[string]interface{}{
				{
					"provider":   "aws",
					"created_at": "2024-01-01T00:00:00Z",
					"updated_at": "2024-01-02T00:00:00Z",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	client := NewClientWithURL("test-key", server.URL, false)
	creds, err := client.ListCredentials(context.Background())
	if err != nil {
		t.Fatalf("ListCredentials failed: %v", err)
	}

	if len(creds) != 1 {
		t.Errorf("expected 1 credential, got %d", len(creds))
	}
	if creds[0].Provider != "aws" {
		t.Errorf("expected provider 'aws', got '%s'", creds[0].Provider)
	}
}

func TestDeleteCredential(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		if r.URL.Path != "/api/v1/secrets/aws" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		response := map[string]interface{}{
			"success": true,
			"message": "Credential deleted",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	client := NewClientWithURL("test-key", server.URL, false)
	err := client.DeleteCredential(context.Background(), ProviderAWS)
	if err != nil {
		t.Fatalf("DeleteCredential failed: %v", err)
	}
}

func TestStoreAWSCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		if r.URL.Path != "/api/v1/secrets/aws" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		response := map[string]interface{}{
			"success": true,
			"message": "Credential stored",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	client := NewClientWithURL("test-key", server.URL, false)
	creds := &AWSCredentials{
		AccessKeyID:     "AKIAIOSFODNN7EXAMPLE",
		SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		Region:          "us-east-1",
	}
	err := client.StoreAWSCredentials(context.Background(), creds)
	if err != nil {
		t.Fatalf("StoreAWSCredentials failed: %v", err)
	}
}
