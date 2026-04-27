package resourcedb

import (
	"bytes"
	"database/sql"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResourceExtraction(t *testing.T) {
	args := []string{"ec2", "run-instances", "--instance-type", "t3.micro", "--image-id", "ami-123", "--tags", "Key=Name,Value=test"}
	output := `{"Instances": [{"InstanceId": "i-0abc123def"}]}`

	resource := ExtractResource(args, output, 0, "run-123", "us-east-1", "default", "123456789012", "")
	if resource == nil {
		t.Fatal("resource extraction returned nil")
	}

	if resource.ResourceType != "ec2:instance" {
		t.Errorf("expected resource type ec2:instance, got %s", resource.ResourceType)
	}

	if resource.ResourceID != "i-0abc123def" {
		t.Errorf("expected resource ID i-0abc123def, got %s", resource.ResourceID)
	}

	if resource.Service != "ec2" {
		t.Errorf("expected service ec2, got %s", resource.Service)
	}

	if resource.Operation != "run-instances" {
		t.Errorf("expected operation run-instances, got %s", resource.Operation)
	}

	if resource.Metadata["instance_type"] != "t3.micro" {
		t.Errorf("expected instance_type t3.micro, got %s", resource.Metadata["instance_type"])
	}
}

func TestResourceExtractionOnlyAfterConfirmedCreation(t *testing.T) {
	// Non-creation operations should return nil
	describeArgs := []string{"ec2", "describe-instances", "--instance-ids", "i-123"}
	describeOutput := `{"Reservations": [{"Instances": [{"InstanceId": "i-123"}]}]}`
	if resource := ExtractResource(describeArgs, describeOutput, 0, "run-123", "us-east-1", "default", "123456789012", ""); resource != nil {
		t.Error("describe operation should not create a resource record")
	}

	// Creation operation but output has no resource ID should return nil
	createArgs := []string{"ec2", "run-instances", "--instance-type", "t3.micro"}
	emptyOutput := `{"error": "some error"}` // No InstanceId in output
	if resource := ExtractResource(createArgs, emptyOutput, 0, "run-123", "us-east-1", "default", "123456789012", ""); resource != nil {
		t.Error("creation without confirmed resource ID should not create a resource record")
	}

	// Creation operation with valid output should return resource
	validOutput := `{"Instances": [{"InstanceId": "i-0def456abc789"}]}`
	resource := ExtractResource(createArgs, validOutput, 0, "run-123", "us-east-1", "default", "123456789012", "")
	if resource == nil {
		t.Fatal("creation with confirmed resource ID should create a resource record")
	}
	if resource.ResourceID != "i-0def456abc789" {
		t.Errorf("expected resource ID i-0def456abc789, got %s", resource.ResourceID)
	}
}

func TestStoreOperations(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "resourcedb-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer store.Close()

	// Test resource record
	resource := &Resource{
		RunID:        "run-123",
		CommandIndex: 0,
		Provider:     "aws",
		Service:      "ec2",
		Operation:    "run-instances",
		ResourceType: "ec2:instance",
		ResourceID:   "i-0abc123def",
		Region:       "us-east-1",
		Profile:      "default",
		AccountID:    "123456789012",
		Metadata:     map[string]string{"instance_type": "t3.micro"},
		Tags:         map[string]string{"Name": "test"},
	}

	err = store.RecordResource(resource)
	if err != nil {
		t.Fatalf("RecordResource failed: %v", err)
	}

	// Test retrieval by run
	resources, err := store.GetResourcesByRun("run-123")
	if err != nil {
		t.Fatalf("GetResourcesByRun failed: %v", err)
	}

	if len(resources) != 1 {
		t.Fatalf("expected 1 resource, got %d", len(resources))
	}

	if resources[0].ResourceID != "i-0abc123def" {
		t.Errorf("expected resource ID i-0abc123def, got %s", resources[0].ResourceID)
	}

	// Test retrieval by type
	resources, err = store.GetResourcesByType("ec2:instance")
	if err != nil {
		t.Fatalf("GetResourcesByType failed: %v", err)
	}

	if len(resources) != 1 {
		t.Fatalf("expected 1 resource, got %d", len(resources))
	}

	// Test count
	count, err := store.CountResources()
	if err != nil {
		t.Fatalf("CountResources failed: %v", err)
	}

	if count != 1 {
		t.Errorf("expected count 1, got %d", count)
	}

	// Test delete
	err = store.DeleteResourceRecord("run-123", 0)
	if err != nil {
		t.Fatalf("DeleteResourceRecord failed: %v", err)
	}

	count, _ = store.CountResources()
	if count != 0 {
		t.Errorf("expected count 0 after delete, got %d", count)
	}
}

func TestSecretFiltering(t *testing.T) {
	tests := []struct {
		key      string
		expected bool
	}{
		{"INSTANCE_ID", false},
		{"API_KEY", true},
		{"DATABASE_PASSWORD", true},
		{"SECRET_TOKEN", true},
		{"ENV_VAR", true},
		{"USER_DATA", true},
		{"REGION", false},
		{"VPC_ID", false},
		{"ACCESS_KEY_ID", true},
	}

	for _, tt := range tests {
		if IsSecretKey(tt.key) != tt.expected {
			t.Errorf("IsSecretKey(%s) = %v, expected %v", tt.key, !tt.expected, tt.expected)
		}
	}
}

func TestIsCreationOperation(t *testing.T) {
	tests := []struct {
		service  string
		op       string
		expected bool
	}{
		{"ec2", "run-instances", true},
		{"ec2", "create-vpc", true},
		{"ec2", "describe-instances", false},
		{"elbv2", "create-load-balancer", true},
		{"elbv2", "describe-load-balancers", false},
		{"iam", "create-role", true},
		{"iam", "get-role", false},
		{"rds", "create-db-instance", true},
		{"ec2", "allocate-address", true},
	}

	for _, tt := range tests {
		if IsCreationOperation(tt.service, tt.op) != tt.expected {
			t.Errorf("IsCreationOperation(%s, %s) = %v, expected %v", tt.service, tt.op, !tt.expected, tt.expected)
		}
	}
}

func TestHydrateResource_LogsMalformedJSON(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)

	r := &Resource{ID: 42}
	hydrateResource(r,
		sql.NullString{String: "{not json", Valid: true},
		sql.NullString{String: `{"k":"v"}`, Valid: true},
		"2024-06-01T12:00:00Z",
	)

	if got := buf.String(); !bytes.Contains([]byte(got), []byte("malformed metadata JSON")) {
		t.Errorf("expected malformed-metadata log, got %q", got)
	}
	if r.Metadata == nil {
		t.Error("metadata map should be initialized even on parse failure")
	}
	if r.Tags["k"] != "v" {
		t.Errorf("tags should still hydrate from valid JSON; got %#v", r.Tags)
	}
	if r.CreatedAt.IsZero() {
		t.Error("CreatedAt should parse from valid RFC3339")
	}
}

func TestHydrateResource_LogsBadTimestamp(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)

	r := &Resource{ID: 7}
	hydrateResource(r, sql.NullString{}, sql.NullString{}, "definitely-not-a-time")

	if !r.CreatedAt.Equal(time.Time{}) {
		t.Errorf("CreatedAt should remain zero on parse failure; got %v", r.CreatedAt)
	}
	if got := buf.String(); !bytes.Contains([]byte(got), []byte("unparseable created_at")) {
		t.Errorf("expected unparseable-timestamp log, got %q", got)
	}
}

func TestInferResourceType(t *testing.T) {
	tests := []struct {
		service  string
		op       string
		expected string
	}{
		{"ec2", "run-instances", "ec2:instance"},
		{"ec2", "create-vpc", "ec2:vpc"},
		{"ec2", "create-subnet", "ec2:subnet"},
		{"elbv2", "create-load-balancer", "elbv2:load-balancer"},
		{"rds", "create-db-instance", "rds:db-instance"},
		{"ecr", "create-repository", "ecr:repository"},
		{"unknown", "create-something", "unknown:something"},
	}

	for _, tt := range tests {
		result := InferResourceType(tt.service, tt.op)
		if result != tt.expected {
			t.Errorf("InferResourceType(%s, %s) = %s, expected %s", tt.service, tt.op, result, tt.expected)
		}
	}
}
