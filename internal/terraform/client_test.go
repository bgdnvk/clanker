package terraform

import (
	"testing"

	"github.com/spf13/viper"
)

func TestNewClient_InvalidWorkspaceDataType(t *testing.T) {
	viper.Reset()
	defer viper.Reset()

	// Set workspace data to a non-map type to trigger the type assertion guard.
	viper.Set("terraform.workspaces", map[string]interface{}{
		"bad": "not-a-map",
	})

	_, err := NewClient("bad")
	if err == nil {
		t.Fatal("expected error for invalid workspace data type, got nil")
	}
	expected := "terraform workspace 'bad' has invalid configuration format"
	if err.Error() != expected {
		t.Errorf("unexpected error message: got %q, want %q", err.Error(), expected)
	}
}

func TestNewClient_MissingPath(t *testing.T) {
	viper.Reset()
	defer viper.Reset()

	viper.Set("terraform.workspaces", map[string]interface{}{
		"nopath": map[string]interface{}{
			"description": "no path here",
		},
	})

	_, err := NewClient("nopath")
	if err == nil {
		t.Fatal("expected error for missing path, got nil")
	}
	expected := "terraform workspace 'nopath' has no path configured"
	if err.Error() != expected {
		t.Errorf("unexpected error message: got %q, want %q", err.Error(), expected)
	}
}

func TestNewClient_WorkspaceNotFound(t *testing.T) {
	viper.Reset()
	defer viper.Reset()

	viper.Set("terraform.workspaces", map[string]interface{}{
		"dev": map[string]interface{}{"path": "/tmp"},
	})

	_, err := NewClient("nonexistent")
	if err == nil {
		t.Fatal("expected error for missing workspace, got nil")
	}
	expected := "terraform workspace 'nonexistent' not found in configuration"
	if err.Error() != expected {
		t.Errorf("unexpected error message: got %q, want %q", err.Error(), expected)
	}
}

func TestNewClient_NoWorkspacesConfigured(t *testing.T) {
	viper.Reset()
	defer viper.Reset()

	_, err := NewClient("anything")
	if err == nil {
		t.Fatal("expected error when no workspaces configured, got nil")
	}
	expected := "no terraform workspaces configured"
	if err.Error() != expected {
		t.Errorf("unexpected error message: got %q, want %q", err.Error(), expected)
	}
}

func TestLooksLikePath(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"", false},
		{"dev", false},
		{"./terraform", true},
		{"/home/user/tf", true},
		{"~/infrastructure", true},
		{"path/to/dir", true},
	}

	for _, tt := range tests {
		got := looksLikePath(tt.input)
		if got != tt.want {
			t.Errorf("looksLikePath(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}
