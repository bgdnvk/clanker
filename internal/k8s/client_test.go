package k8s

import (
	"encoding/base64"
	"os"
	"strings"
	"testing"
)

func TestNewClient(t *testing.T) {
	tests := []struct {
		name         string
		kubeconfig   string
		kubeContext  string
		debug        bool
		expectConfig string
		expectCtx    string
	}{
		{
			name:         "with all options",
			kubeconfig:   "/path/to/kubeconfig",
			kubeContext:  "my-context",
			debug:        true,
			expectConfig: "/path/to/kubeconfig",
			expectCtx:    "my-context",
		},
		{
			name:         "with empty options",
			kubeconfig:   "",
			kubeContext:  "",
			debug:        false,
			expectConfig: "",
			expectCtx:    "",
		},
		{
			name:         "only kubeconfig",
			kubeconfig:   "~/.kube/config",
			kubeContext:  "",
			debug:        false,
			expectConfig: "~/.kube/config",
			expectCtx:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient(tt.kubeconfig, tt.kubeContext, tt.debug)
			if client == nil {
				t.Fatal("expected client, got nil")
			}
			if client.kubeconfig != tt.expectConfig {
				t.Errorf("expected kubeconfig %q, got %q", tt.expectConfig, client.kubeconfig)
			}
			if client.context != tt.expectCtx {
				t.Errorf("expected context %q, got %q", tt.expectCtx, client.context)
			}
			if client.namespace != "default" {
				t.Errorf("expected namespace 'default', got %q", client.namespace)
			}
			if client.debug != tt.debug {
				t.Errorf("expected debug %v, got %v", tt.debug, client.debug)
			}
		})
	}
}

func TestNewClientWithCredentials(t *testing.T) {
	t.Run("nil credentials", func(t *testing.T) {
		client, path, err := NewClientWithCredentials(nil, false)
		if err == nil {
			t.Error("expected error for nil credentials")
		}
		if client != nil {
			t.Error("expected nil client")
		}
		if path != "" {
			t.Error("expected empty path")
		}
	})

	t.Run("empty kubeconfig content", func(t *testing.T) {
		creds := &BackendKubernetesCredentials{
			KubeconfigContent: "",
			ContextName:       "test-context",
		}
		client, path, err := NewClientWithCredentials(creds, false)
		if err == nil {
			t.Error("expected error for empty kubeconfig content")
		}
		if client != nil {
			t.Error("expected nil client")
		}
		if path != "" {
			t.Error("expected empty path")
		}
	})

	t.Run("valid credentials", func(t *testing.T) {
		kubeconfigContent := `apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://test.example.com
  name: test-cluster
contexts:
- context:
    cluster: test-cluster
    user: test-user
  name: test-context
current-context: test-context
users:
- name: test-user
  user:
    token: test-token
`
		encoded := base64.StdEncoding.EncodeToString([]byte(kubeconfigContent))
		creds := &BackendKubernetesCredentials{
			KubeconfigContent: encoded,
			ContextName:       "test-context",
		}

		client, path, err := NewClientWithCredentials(creds, true)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if client == nil {
			t.Fatal("expected client, got nil")
		}
		if path == "" {
			t.Fatal("expected non-empty path")
		}

		// Cleanup
		defer CleanupKubeconfig(path)

		// Verify file was created
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Error("kubeconfig file was not created")
		}

		// Verify client settings
		if client.kubeconfig != path {
			t.Errorf("expected kubeconfig %q, got %q", path, client.kubeconfig)
		}
		if client.context != "test-context" {
			t.Errorf("expected context 'test-context', got %q", client.context)
		}
		if client.namespace != "default" {
			t.Errorf("expected namespace 'default', got %q", client.namespace)
		}
		if !client.debug {
			t.Error("expected debug to be true")
		}
	})

	t.Run("invalid base64", func(t *testing.T) {
		creds := &BackendKubernetesCredentials{
			KubeconfigContent: "not-valid-base64!!!",
			ContextName:       "test-context",
		}
		client, path, err := NewClientWithCredentials(creds, false)
		if err == nil {
			t.Error("expected error for invalid base64")
		}
		if client != nil {
			t.Error("expected nil client")
		}
		if path != "" {
			t.Error("expected empty path")
		}
	})
}

func TestBase64DecodeString(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{
			name:    "standard encoding",
			input:   base64.StdEncoding.EncodeToString([]byte("hello world")),
			wantErr: false,
		},
		{
			name:    "URL encoding",
			input:   base64.URLEncoding.EncodeToString([]byte("hello/world+test")),
			wantErr: false,
		},
		{
			name:    "raw standard encoding",
			input:   base64.RawStdEncoding.EncodeToString([]byte("no padding")),
			wantErr: false,
		},
		{
			name:    "raw URL encoding",
			input:   base64.RawURLEncoding.EncodeToString([]byte("url/safe")),
			wantErr: false,
		},
		{
			name:    "invalid base64",
			input:   "!!!invalid!!!",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := base64DecodeString(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if result == nil {
				t.Error("expected non-nil result")
			}
		})
	}
}

func TestCleanupKubeconfig(t *testing.T) {
	t.Run("valid file", func(t *testing.T) {
		// Create temp file
		tmpFile, err := os.CreateTemp("", "kubeconfig-test-*.yaml")
		if err != nil {
			t.Fatalf("failed to create temp file: %v", err)
		}
		tmpPath := tmpFile.Name()
		tmpFile.Close()

		// Verify file exists
		if _, err := os.Stat(tmpPath); os.IsNotExist(err) {
			t.Fatal("temp file was not created")
		}

		// Cleanup
		CleanupKubeconfig(tmpPath)

		// Verify file was removed
		if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
			t.Error("file was not cleaned up")
		}
	})

	t.Run("empty path", func(t *testing.T) {
		// Should not panic
		CleanupKubeconfig("")
	})

	t.Run("non-existent file", func(t *testing.T) {
		// Should not panic
		CleanupKubeconfig("/nonexistent/path/to/file")
	})
}

func TestClientSetNamespace(t *testing.T) {
	client := NewClient("", "", false)

	// Default namespace
	if client.namespace != "default" {
		t.Errorf("expected default namespace 'default', got %q", client.namespace)
	}

	// Set custom namespace
	client.SetNamespace("kube-system")
	if client.namespace != "kube-system" {
		t.Errorf("expected namespace 'kube-system', got %q", client.namespace)
	}

	// Set empty namespace
	client.SetNamespace("")
	if client.namespace != "" {
		t.Errorf("expected empty namespace, got %q", client.namespace)
	}
}

func TestClientSetContext(t *testing.T) {
	client := NewClient("", "", false)

	// Default context
	if client.context != "" {
		t.Errorf("expected empty context, got %q", client.context)
	}

	// Set custom context
	client.SetContext("my-cluster")
	if client.context != "my-cluster" {
		t.Errorf("expected context 'my-cluster', got %q", client.context)
	}
}

func TestClientBuildArgs(t *testing.T) {
	tests := []struct {
		name       string
		kubeconfig string
		context    string
		namespace  string
		clientNS   string
		inputArgs  []string
		contains   []string
		notContain []string
	}{
		{
			name:       "all options",
			kubeconfig: "/path/to/kubeconfig",
			context:    "my-context",
			namespace:  "custom-ns",
			clientNS:   "default",
			inputArgs:  []string{"get", "pods"},
			contains:   []string{"--kubeconfig", "/path/to/kubeconfig", "--context", "my-context", "-n", "custom-ns", "get", "pods"},
			notContain: []string{},
		},
		{
			name:       "no kubeconfig",
			kubeconfig: "",
			context:    "ctx",
			namespace:  "ns",
			clientNS:   "default",
			inputArgs:  []string{"get", "nodes"},
			contains:   []string{"--context", "ctx", "-n", "ns", "get", "nodes"},
			notContain: []string{"--kubeconfig"},
		},
		{
			name:       "no context",
			kubeconfig: "/config",
			context:    "",
			namespace:  "ns",
			clientNS:   "default",
			inputArgs:  []string{"get", "pods"},
			contains:   []string{"--kubeconfig", "/config", "-n", "ns"},
			notContain: []string{"--context"},
		},
		{
			name:       "empty namespace uses client default",
			kubeconfig: "",
			context:    "",
			namespace:  "",
			clientNS:   "my-default",
			inputArgs:  []string{"get", "pods"},
			contains:   []string{"-n", "my-default", "get", "pods"},
			notContain: []string{},
		},
		{
			name:       "all namespace skips -n",
			kubeconfig: "",
			context:    "",
			namespace:  "all",
			clientNS:   "default",
			inputArgs:  []string{"get", "pods"},
			contains:   []string{"get", "pods"},
			notContain: []string{"-n"},
		},
		{
			name:       "empty client namespace and empty arg namespace",
			kubeconfig: "",
			context:    "",
			namespace:  "",
			clientNS:   "",
			inputArgs:  []string{"cluster-info"},
			contains:   []string{"cluster-info"},
			notContain: []string{"-n"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &Client{
				kubeconfig: tt.kubeconfig,
				context:    tt.context,
				namespace:  tt.clientNS,
			}

			result := client.buildArgs(tt.namespace, tt.inputArgs)
			resultStr := strings.Join(result, " ")

			for _, substr := range tt.contains {
				if !strings.Contains(resultStr, substr) {
					t.Errorf("expected result to contain %q, got %q", substr, resultStr)
				}
			}
			for _, substr := range tt.notContain {
				if strings.Contains(resultStr, substr) {
					t.Errorf("expected result NOT to contain %q, got %q", substr, resultStr)
				}
			}
		})
	}
}

func TestClientBuildHelmArgs(t *testing.T) {
	tests := []struct {
		name       string
		kubeconfig string
		context    string
		namespace  string
		clientNS   string
		inputArgs  []string
		contains   []string
		notContain []string
	}{
		{
			name:       "all options",
			kubeconfig: "/path/to/kubeconfig",
			context:    "my-context",
			namespace:  "custom-ns",
			clientNS:   "default",
			inputArgs:  []string{"list"},
			contains:   []string{"--kubeconfig", "/path/to/kubeconfig", "--kube-context", "my-context", "-n", "custom-ns", "list"},
			notContain: []string{},
		},
		{
			name:       "no kubeconfig",
			kubeconfig: "",
			context:    "ctx",
			namespace:  "ns",
			clientNS:   "default",
			inputArgs:  []string{"install", "mychart", "."},
			contains:   []string{"--kube-context", "ctx", "-n", "ns", "install"},
			notContain: []string{"--kubeconfig"},
		},
		{
			name:       "no context",
			kubeconfig: "/config",
			context:    "",
			namespace:  "ns",
			clientNS:   "default",
			inputArgs:  []string{"upgrade"},
			contains:   []string{"--kubeconfig", "/config", "-n", "ns"},
			notContain: []string{"--kube-context"},
		},
		{
			name:       "all namespace skips -n",
			kubeconfig: "",
			context:    "",
			namespace:  "all",
			clientNS:   "default",
			inputArgs:  []string{"list"},
			contains:   []string{"list"},
			notContain: []string{"-n"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &Client{
				kubeconfig: tt.kubeconfig,
				context:    tt.context,
				namespace:  tt.clientNS,
			}

			result := client.buildHelmArgs(tt.namespace, tt.inputArgs)
			resultStr := strings.Join(result, " ")

			for _, substr := range tt.contains {
				if !strings.Contains(resultStr, substr) {
					t.Errorf("expected result to contain %q, got %q", substr, resultStr)
				}
			}
			for _, substr := range tt.notContain {
				if strings.Contains(resultStr, substr) {
					t.Errorf("expected result NOT to contain %q, got %q", substr, resultStr)
				}
			}
		})
	}
}

func TestLogOptions(t *testing.T) {
	opts := LogOptions{
		Container: "nginx",
		Follow:    true,
		Previous:  false,
		TailLines: 100,
		Since:     "1h",
	}

	if opts.Container != "nginx" {
		t.Errorf("expected Container 'nginx', got %q", opts.Container)
	}
	if !opts.Follow {
		t.Error("expected Follow to be true")
	}
	if opts.Previous {
		t.Error("expected Previous to be false")
	}
	if opts.TailLines != 100 {
		t.Errorf("expected TailLines 100, got %d", opts.TailLines)
	}
	if opts.Since != "1h" {
		t.Errorf("expected Since '1h', got %q", opts.Since)
	}
}

func TestBackendKubernetesCredentials(t *testing.T) {
	creds := BackendKubernetesCredentials{
		KubeconfigContent: "base64-encoded-content",
		ContextName:       "my-context",
	}

	if creds.KubeconfigContent != "base64-encoded-content" {
		t.Errorf("expected KubeconfigContent 'base64-encoded-content', got %q", creds.KubeconfigContent)
	}
	if creds.ContextName != "my-context" {
		t.Errorf("expected ContextName 'my-context', got %q", creds.ContextName)
	}
}

func TestBuildArgsOrder(t *testing.T) {
	client := &Client{
		kubeconfig: "/config",
		context:    "ctx",
		namespace:  "ns",
	}

	result := client.buildArgs("", []string{"get", "pods", "-o", "wide"})

	// Verify order: kubeconfig -> context -> namespace -> args
	kubeconfigIdx := -1
	contextIdx := -1
	namespaceIdx := -1
	argsStart := -1

	for i, arg := range result {
		if arg == "--kubeconfig" {
			kubeconfigIdx = i
		} else if arg == "--context" {
			contextIdx = i
		} else if arg == "-n" {
			namespaceIdx = i
		} else if arg == "get" {
			argsStart = i
		}
	}

	if kubeconfigIdx >= contextIdx {
		t.Error("kubeconfig should come before context")
	}
	if contextIdx >= namespaceIdx {
		t.Error("context should come before namespace")
	}
	if namespaceIdx >= argsStart {
		t.Error("namespace should come before command args")
	}
}

func TestIsKubectlAvailable(t *testing.T) {
	// This test may pass or fail depending on whether kubectl is installed
	// We just verify the function doesn't panic
	_ = IsKubectlAvailable()
}

func TestIsHelmAvailable(t *testing.T) {
	// This test may pass or fail depending on whether helm is installed
	// We just verify the function doesn't panic
	_ = IsHelmAvailable()
}
