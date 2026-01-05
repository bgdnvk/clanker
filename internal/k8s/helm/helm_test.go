package helm

import (
	"context"
	"testing"
)

// mockHelmClient implements HelmClient for testing
type mockHelmClient struct {
	runOutput    string
	runErr       error
	runCalls     [][]string
	runNsCalls   []struct {
		namespace string
		args      []string
	}
}

func (m *mockHelmClient) Run(ctx context.Context, args ...string) (string, error) {
	m.runCalls = append(m.runCalls, args)
	return m.runOutput, m.runErr
}

func (m *mockHelmClient) RunWithNamespace(ctx context.Context, namespace string, args ...string) (string, error) {
	m.runNsCalls = append(m.runNsCalls, struct {
		namespace string
		args      []string
	}{namespace, args})
	return m.runOutput, m.runErr
}

func TestNewSubAgent(t *testing.T) {
	client := &mockHelmClient{}
	agent := NewSubAgent(client, false)

	if agent == nil {
		t.Fatal("expected non-nil agent")
	}
	if agent.client != client {
		t.Error("client not set correctly")
	}
	if agent.releases == nil {
		t.Error("releases manager not initialized")
	}
	if agent.charts == nil {
		t.Error("charts manager not initialized")
	}
}

func TestDetectResourceType(t *testing.T) {
	agent := NewSubAgent(&mockHelmClient{}, false)

	tests := []struct {
		query    string
		expected ResourceType
	}{
		{"list helm repos", ResourceRepo},
		{"show repository bitnami", ResourceRepo},
		{"search chart nginx", ResourceChart},
		{"find chart for redis", ResourceChart},
		{"list releases", ResourceRelease},
		{"status of my-release", ResourceRelease},
		{"upgrade my-app", ResourceRelease},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			result := agent.detectResourceType(tt.query)
			if result != tt.expected {
				t.Errorf("detectResourceType(%q) = %v, want %v", tt.query, result, tt.expected)
			}
		})
	}
}

func TestDetectOperation(t *testing.T) {
	agent := NewSubAgent(&mockHelmClient{}, false)

	tests := []struct {
		query    string
		expected string
	}{
		{"list all releases", "list"},
		{"show all releases", "list"},
		{"status of nginx-release", "status"},
		{"state of my-app", "status"},
		{"history of my-release", "history"},
		{"revisions of app", "history"},
		{"values for release", "values"},
		{"configuration of app", "values"},
		{"search for nginx chart", "search"},
		{"find chart redis", "search"},
		{"install nginx bitnami/nginx", "install"},
		{"deploy chart prometheus", "install"},
		{"upgrade my-release", "upgrade"},
		{"update release nginx", "upgrade"},
		{"rollback my-release", "rollback"},
		{"revert to previous", "rollback"},
		{"uninstall my-release", "uninstall"},
		{"remove release nginx", "uninstall"},
		{"add repo bitnami", "add"},
		{"update repo", "update"},
		{"refresh repositories", "update"},
		{"remove repo bitnami", "remove"},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			result := agent.detectOperation(tt.query)
			if result != tt.expected {
				t.Errorf("detectOperation(%q) = %v, want %v", tt.query, result, tt.expected)
			}
		})
	}
}

func TestExtractReleaseName(t *testing.T) {
	agent := NewSubAgent(&mockHelmClient{}, false)

	tests := []struct {
		query    string
		expected string
	}{
		{"status of my-release", "my-release"},
		{"release named nginx-app", "nginx-app"},
		{"history of prometheus", "prometheus"},
		{"uninstall my-app", "my-app"},
		{"upgrade redis-cache", "redis-cache"},
		{"rollback grafana", "grafana"},
		{"list releases", ""},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			result := agent.extractReleaseName(tt.query)
			if result != tt.expected {
				t.Errorf("extractReleaseName(%q) = %v, want %v", tt.query, result, tt.expected)
			}
		})
	}
}

func TestExtractNamespace(t *testing.T) {
	agent := NewSubAgent(&mockHelmClient{}, false)

	tests := []struct {
		query    string
		expected string
	}{
		{"list releases in namespace monitoring", "monitoring"},
		{"get release -n kube-system", "kube-system"},
		{"status in ns production", "production"},
		{"list all releases", ""},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			result := agent.extractNamespace(tt.query)
			if result != tt.expected {
				t.Errorf("extractNamespace(%q) = %v, want %v", tt.query, result, tt.expected)
			}
		})
	}
}

func TestExtractChartName(t *testing.T) {
	agent := NewSubAgent(&mockHelmClient{}, false)

	tests := []struct {
		query    string
		expected string
	}{
		{"search for chart nginx", "nginx"},
		{"install myrelease bitnami/redis", "bitnami/redis"},
		{"show chart prometheus", "prometheus"},
		{"list releases", ""},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			result := agent.extractChartName(tt.query)
			if result != tt.expected {
				t.Errorf("extractChartName(%q) = %v, want %v", tt.query, result, tt.expected)
			}
		})
	}
}

func TestExtractRepoName(t *testing.T) {
	agent := NewSubAgent(&mockHelmClient{}, false)

	tests := []struct {
		query    string
		expected string
	}{
		{"add repo bitnami", "bitnami"},
		{"remove repository prometheus", "prometheus"},
		{"repo named stable", "stable"},
		{"list repos", ""},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			result := agent.extractRepoName(tt.query)
			if result != tt.expected {
				t.Errorf("extractRepoName(%q) = %v, want %v", tt.query, result, tt.expected)
			}
		})
	}
}

func TestExtractRevision(t *testing.T) {
	agent := NewSubAgent(&mockHelmClient{}, false)

	tests := []struct {
		query    string
		expected int
	}{
		{"rollback to revision 3", 3},
		{"revert to version 5", 5},
		{"rollback my-release revision 2", 2},
		{"rollback my-release", 0},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			result := agent.extractRevision(tt.query)
			if result != tt.expected {
				t.Errorf("extractRevision(%q) = %v, want %v", tt.query, result, tt.expected)
			}
		})
	}
}

func TestIsReadOnlyOperation(t *testing.T) {
	agent := NewSubAgent(&mockHelmClient{}, false)

	readOnlyOps := []string{"list", "status", "history", "values", "search", "show"}
	modifyOps := []string{"install", "upgrade", "rollback", "uninstall", "add", "update", "remove"}

	for _, op := range readOnlyOps {
		if !agent.isReadOnlyOperation(op) {
			t.Errorf("expected %q to be read-only", op)
		}
	}

	for _, op := range modifyOps {
		if agent.isReadOnlyOperation(op) {
			t.Errorf("expected %q to not be read-only", op)
		}
	}
}

func TestAnalyzeQuery(t *testing.T) {
	agent := NewSubAgent(&mockHelmClient{}, false)

	tests := []struct {
		query            string
		expectedType     ResourceType
		expectedOp       string
		expectedReadOnly bool
	}{
		{"list helm releases", ResourceRelease, "list", true},
		{"status of my-release", ResourceRelease, "status", true},
		{"install myapp bitnami/nginx", ResourceRelease, "install", false},
		{"upgrade my-release", ResourceRelease, "upgrade", false},
		{"search chart redis", ResourceChart, "search", true},
		{"list repos", ResourceRepo, "list", true},
		{"add repo bitnami", ResourceRepo, "add", false},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			analysis := agent.analyzeQuery(tt.query)
			if analysis.ResourceType != tt.expectedType {
				t.Errorf("ResourceType = %v, want %v", analysis.ResourceType, tt.expectedType)
			}
			if analysis.Operation != tt.expectedOp {
				t.Errorf("Operation = %v, want %v", analysis.Operation, tt.expectedOp)
			}
			if analysis.IsReadOnly != tt.expectedReadOnly {
				t.Errorf("IsReadOnly = %v, want %v", analysis.IsReadOnly, tt.expectedReadOnly)
			}
		})
	}
}

func TestParseInstallFromQuery(t *testing.T) {
	agent := NewSubAgent(&mockHelmClient{}, false)

	query := "install my-nginx bitnami/nginx create namespace"
	opts := agent.parseInstallFromQuery(query, "default")

	if opts.ReleaseName != "my-nginx" {
		t.Errorf("ReleaseName = %v, want my-nginx", opts.ReleaseName)
	}
	if opts.Chart != "bitnami/nginx" {
		t.Errorf("Chart = %v, want bitnami/nginx", opts.Chart)
	}
	if !opts.CreateNamespace {
		t.Error("CreateNamespace should be true")
	}
	if opts.Namespace != "default" {
		t.Errorf("Namespace = %v, want default", opts.Namespace)
	}
}

func TestParseUpgradeFromQuery(t *testing.T) {
	agent := NewSubAgent(&mockHelmClient{}, false)

	query := "upgrade my-redis bitnami/redis install if not exists"
	opts := agent.parseUpgradeFromQuery(query, "production")

	if opts.ReleaseName != "my-redis" {
		t.Errorf("ReleaseName = %v, want my-redis", opts.ReleaseName)
	}
	if opts.Chart != "bitnami/redis" {
		t.Errorf("Chart = %v, want bitnami/redis", opts.Chart)
	}
	if !opts.Install {
		t.Error("Install should be true")
	}
	if opts.Namespace != "production" {
		t.Errorf("Namespace = %v, want production", opts.Namespace)
	}
}

func TestParseRollbackFromQuery(t *testing.T) {
	agent := NewSubAgent(&mockHelmClient{}, false)

	query := "rollback my-app to revision 3"
	opts := agent.parseRollbackFromQuery(query, "staging", 3)

	if opts.ReleaseName != "my-app" {
		t.Errorf("ReleaseName = %v, want my-app", opts.ReleaseName)
	}
	if opts.Revision != 3 {
		t.Errorf("Revision = %v, want 3", opts.Revision)
	}
	if opts.Namespace != "staging" {
		t.Errorf("Namespace = %v, want staging", opts.Namespace)
	}
}

func TestParseAddRepoFromQuery(t *testing.T) {
	agent := NewSubAgent(&mockHelmClient{}, false)

	query := "add repo bitnami https://charts.bitnami.com/bitnami"
	opts := agent.parseAddRepoFromQuery(query)

	if opts.Name != "bitnami" {
		t.Errorf("Name = %v, want bitnami", opts.Name)
	}
	if opts.URL != "https://charts.bitnami.com/bitnami" {
		t.Errorf("URL = %v, want https://charts.bitnami.com/bitnami", opts.URL)
	}
}

func TestReleaseManagerInstallPlan(t *testing.T) {
	client := &mockHelmClient{}
	manager := NewReleaseManager(client, false)

	opts := InstallOptions{
		ReleaseName:     "my-nginx",
		Chart:           "bitnami/nginx",
		Namespace:       "web",
		CreateNamespace: true,
		Version:         "1.0.0",
		Wait:            true,
	}

	plan := manager.InstallReleasePlan(opts)

	if plan.Summary == "" {
		t.Error("expected non-empty summary")
	}
	if len(plan.Steps) == 0 {
		t.Error("expected at least one step")
	}

	step := plan.Steps[0]
	if step.Command != "helm" {
		t.Errorf("Command = %v, want helm", step.Command)
	}

	// Check that args contain expected values
	argsStr := ""
	for _, arg := range step.Args {
		argsStr += arg + " "
	}
	if !containsSubstring(argsStr, "install") {
		t.Error("expected args to contain 'install'")
	}
	if !containsSubstring(argsStr, "my-nginx") {
		t.Error("expected args to contain 'my-nginx'")
	}
	if !containsSubstring(argsStr, "bitnami/nginx") {
		t.Error("expected args to contain 'bitnami/nginx'")
	}
}

func TestReleaseManagerUpgradePlan(t *testing.T) {
	client := &mockHelmClient{}
	manager := NewReleaseManager(client, false)

	opts := UpgradeOptions{
		ReleaseName: "my-nginx",
		Chart:       "bitnami/nginx",
		Namespace:   "web",
		Install:     true,
		ReuseValues: true,
	}

	plan := manager.UpgradeReleasePlan(opts)

	if plan.Summary == "" {
		t.Error("expected non-empty summary")
	}
	if len(plan.Steps) == 0 {
		t.Error("expected at least one step")
	}

	step := plan.Steps[0]
	argsStr := ""
	for _, arg := range step.Args {
		argsStr += arg + " "
	}
	if !containsSubstring(argsStr, "upgrade") {
		t.Error("expected args to contain 'upgrade'")
	}
	if !containsSubstring(argsStr, "--install") {
		t.Error("expected args to contain '--install'")
	}
	if !containsSubstring(argsStr, "--reuse-values") {
		t.Error("expected args to contain '--reuse-values'")
	}
}

func TestReleaseManagerRollbackPlan(t *testing.T) {
	client := &mockHelmClient{}
	manager := NewReleaseManager(client, false)

	opts := RollbackOptions{
		ReleaseName: "my-app",
		Revision:    3,
		Namespace:   "production",
	}

	plan := manager.RollbackReleasePlan(opts)

	if plan.Summary == "" {
		t.Error("expected non-empty summary")
	}
	if len(plan.Steps) == 0 {
		t.Error("expected at least one step")
	}

	step := plan.Steps[0]
	argsStr := ""
	for _, arg := range step.Args {
		argsStr += arg + " "
	}
	if !containsSubstring(argsStr, "rollback") {
		t.Error("expected args to contain 'rollback'")
	}
	if !containsSubstring(argsStr, "my-app") {
		t.Error("expected args to contain 'my-app'")
	}
	if !containsSubstring(argsStr, "3") {
		t.Error("expected args to contain '3'")
	}
}

func TestReleaseManagerUninstallPlan(t *testing.T) {
	client := &mockHelmClient{}
	manager := NewReleaseManager(client, false)

	opts := UninstallOptions{
		ReleaseName: "my-nginx",
		Namespace:   "web",
		KeepHistory: true,
	}

	plan := manager.UninstallReleasePlan(opts)

	if plan.Summary == "" {
		t.Error("expected non-empty summary")
	}
	if len(plan.Steps) == 0 {
		t.Error("expected at least one step")
	}

	step := plan.Steps[0]
	argsStr := ""
	for _, arg := range step.Args {
		argsStr += arg + " "
	}
	if !containsSubstring(argsStr, "uninstall") {
		t.Error("expected args to contain 'uninstall'")
	}
	if !containsSubstring(argsStr, "--keep-history") {
		t.Error("expected args to contain '--keep-history'")
	}
}

func TestChartManagerAddRepoPlan(t *testing.T) {
	client := &mockHelmClient{}
	manager := NewChartManager(client, false)

	opts := AddRepoOptions{
		Name: "bitnami",
		URL:  "https://charts.bitnami.com/bitnami",
	}

	plan := manager.AddRepoPlan(opts)

	if plan.Summary == "" {
		t.Error("expected non-empty summary")
	}
	if len(plan.Steps) < 2 {
		t.Error("expected at least two steps (add and update)")
	}

	// First step should be add repo
	addStep := plan.Steps[0]
	argsStr := ""
	for _, arg := range addStep.Args {
		argsStr += arg + " "
	}
	if !containsSubstring(argsStr, "repo add") {
		t.Error("expected args to contain 'repo add'")
	}
	if !containsSubstring(argsStr, "bitnami") {
		t.Error("expected args to contain 'bitnami'")
	}

	// Second step should be update repos
	updateStep := plan.Steps[1]
	argsStr = ""
	for _, arg := range updateStep.Args {
		argsStr += arg + " "
	}
	if !containsSubstring(argsStr, "repo update") {
		t.Error("expected args to contain 'repo update'")
	}
}

func TestChartManagerRemoveRepoPlan(t *testing.T) {
	client := &mockHelmClient{}
	manager := NewChartManager(client, false)

	plan := manager.RemoveRepoPlan("bitnami")

	if plan.Summary == "" {
		t.Error("expected non-empty summary")
	}
	if len(plan.Steps) == 0 {
		t.Error("expected at least one step")
	}

	step := plan.Steps[0]
	argsStr := ""
	for _, arg := range step.Args {
		argsStr += arg + " "
	}
	if !containsSubstring(argsStr, "repo remove") {
		t.Error("expected args to contain 'repo remove'")
	}
	if !containsSubstring(argsStr, "bitnami") {
		t.Error("expected args to contain 'bitnami'")
	}
}

func TestChartManagerUpdateReposPlan(t *testing.T) {
	client := &mockHelmClient{}
	manager := NewChartManager(client, false)

	plan := manager.UpdateReposPlan()

	if plan.Summary == "" {
		t.Error("expected non-empty summary")
	}
	if len(plan.Steps) == 0 {
		t.Error("expected at least one step")
	}

	step := plan.Steps[0]
	argsStr := ""
	for _, arg := range step.Args {
		argsStr += arg + " "
	}
	if !containsSubstring(argsStr, "repo update") {
		t.Error("expected args to contain 'repo update'")
	}
}

func TestParseReleaseListJSON(t *testing.T) {
	client := &mockHelmClient{}
	manager := NewReleaseManager(client, false)

	jsonData := `[
		{
			"name": "nginx",
			"namespace": "default",
			"revision": "1",
			"status": "deployed",
			"chart": "nginx-1.0.0",
			"app_version": "1.21.0"
		},
		{
			"name": "redis",
			"namespace": "cache",
			"revision": "3",
			"status": "deployed",
			"chart": "redis-17.0.0",
			"app_version": "7.0.0"
		}
	]`

	releases, err := manager.parseReleaseList([]byte(jsonData))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(releases) != 2 {
		t.Fatalf("expected 2 releases, got %d", len(releases))
	}

	if releases[0].Name != "nginx" {
		t.Errorf("releases[0].Name = %v, want nginx", releases[0].Name)
	}
	if releases[0].Namespace != "default" {
		t.Errorf("releases[0].Namespace = %v, want default", releases[0].Namespace)
	}
	if releases[0].Status != "deployed" {
		t.Errorf("releases[0].Status = %v, want deployed", releases[0].Status)
	}

	if releases[1].Name != "redis" {
		t.Errorf("releases[1].Name = %v, want redis", releases[1].Name)
	}
	if releases[1].Revision != 3 {
		t.Errorf("releases[1].Revision = %v, want 3", releases[1].Revision)
	}
}

func TestParseReleaseHistoryJSON(t *testing.T) {
	client := &mockHelmClient{}
	manager := NewReleaseManager(client, false)

	jsonData := `[
		{
			"revision": 1,
			"status": "superseded",
			"chart": "nginx-1.0.0",
			"app_version": "1.20.0",
			"description": "Install complete"
		},
		{
			"revision": 2,
			"status": "deployed",
			"chart": "nginx-1.1.0",
			"app_version": "1.21.0",
			"description": "Upgrade complete"
		}
	]`

	history, err := manager.parseReleaseHistory([]byte(jsonData))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(history) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(history))
	}

	if history[0].Revision != 1 {
		t.Errorf("history[0].Revision = %v, want 1", history[0].Revision)
	}
	if history[0].Status != "superseded" {
		t.Errorf("history[0].Status = %v, want superseded", history[0].Status)
	}

	if history[1].Revision != 2 {
		t.Errorf("history[1].Revision = %v, want 2", history[1].Revision)
	}
	if history[1].Status != "deployed" {
		t.Errorf("history[1].Status = %v, want deployed", history[1].Status)
	}
}

func TestParseChartListJSON(t *testing.T) {
	client := &mockHelmClient{}
	manager := NewChartManager(client, false)

	jsonData := `[
		{
			"name": "bitnami/nginx",
			"version": "13.2.0",
			"app_version": "1.23.0",
			"description": "NGINX web server"
		},
		{
			"name": "bitnami/redis",
			"version": "17.3.0",
			"app_version": "7.0.5",
			"description": "Redis database"
		}
	]`

	charts, err := manager.parseChartList([]byte(jsonData))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(charts) != 2 {
		t.Fatalf("expected 2 charts, got %d", len(charts))
	}

	if charts[0].Name != "bitnami/nginx" {
		t.Errorf("charts[0].Name = %v, want bitnami/nginx", charts[0].Name)
	}
	if charts[0].Version != "13.2.0" {
		t.Errorf("charts[0].Version = %v, want 13.2.0", charts[0].Version)
	}

	if charts[1].Name != "bitnami/redis" {
		t.Errorf("charts[1].Name = %v, want bitnami/redis", charts[1].Name)
	}
}

func TestParseRepoListJSON(t *testing.T) {
	client := &mockHelmClient{}
	manager := NewChartManager(client, false)

	jsonData := `[
		{
			"name": "bitnami",
			"url": "https://charts.bitnami.com/bitnami"
		},
		{
			"name": "prometheus",
			"url": "https://prometheus-community.github.io/helm-charts"
		}
	]`

	repos, err := manager.parseRepoList([]byte(jsonData))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(repos) != 2 {
		t.Fatalf("expected 2 repos, got %d", len(repos))
	}

	if repos[0].Name != "bitnami" {
		t.Errorf("repos[0].Name = %v, want bitnami", repos[0].Name)
	}
	if repos[0].URL != "https://charts.bitnami.com/bitnami" {
		t.Errorf("repos[0].URL = %v, want https://charts.bitnami.com/bitnami", repos[0].URL)
	}

	if repos[1].Name != "prometheus" {
		t.Errorf("repos[1].Name = %v, want prometheus", repos[1].Name)
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		seconds  int
		expected string
	}{
		{30, "30s"},
		{90, "1m"},
		{3600, "1h"},
		{86400, "1d"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			// formatDuration is unexported, test indirectly through plan creation
			// This is a placeholder for when the function is exported or tested differently
		})
	}
}

// Helper function
func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstringHelper(s, substr))
}

func containsSubstringHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
