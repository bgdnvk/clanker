// Package railway provides a client for the Railway GraphQL API and the
// official `railway` CLI. Mirrors the shape of the Vercel package so wiring
// into cmd/, routing, ask-mode and the desktop backend stays uniform.
//
// Railway exposes a single GraphQL endpoint (Backboard v2) for both queries
// and mutations. Auth is a bearer account token; workspace scoping is passed
// either as a query variable or via `RAILWAY_WORKSPACE_ID`.
package railway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/viper"
	"golang.org/x/sync/errgroup"
)

// graphqlEndpoint is the Railway public GraphQL (Backboard) URL.
const graphqlEndpoint = "https://backboard.railway.com/graphql/v2"

// Client wraps the Railway GraphQL API and the official `railway` CLI.
type Client struct {
	apiToken    string
	workspaceID string
	debug       bool
	// raw, when set, causes static CLI commands to print unformatted JSON
	// responses instead of pretty-printed summaries.
	raw bool
}

// ResolveAPIToken returns the Railway API token from config or environment.
// Resolution order: `railway.api_token` → RAILWAY_API_TOKEN.
//
// Note: RAILWAY_TOKEN is intentionally NOT checked — that variable is a
// project-scoped deploy token used by the Railway CLI and cannot be used
// against the public GraphQL API as a Bearer token.
func ResolveAPIToken() string {
	if t := strings.TrimSpace(viper.GetString("railway.api_token")); t != "" {
		return t
	}
	if env := strings.TrimSpace(os.Getenv("RAILWAY_API_TOKEN")); env != "" {
		return env
	}
	return ""
}

// ResolveWorkspaceID returns the Railway workspace (team) ID from config or env.
// Resolution order: `railway.workspace_id` → RAILWAY_WORKSPACE_ID.
// Workspace scoping is optional — personal accounts have no workspace ID.
func ResolveWorkspaceID() string {
	if t := strings.TrimSpace(viper.GetString("railway.workspace_id")); t != "" {
		return t
	}
	if env := strings.TrimSpace(os.Getenv("RAILWAY_WORKSPACE_ID")); env != "" {
		return env
	}
	return ""
}

// NewClient creates a new Railway client.
func NewClient(apiToken, workspaceID string, debug bool) (*Client, error) {
	if strings.TrimSpace(apiToken) == "" {
		return nil, fmt.Errorf("railway api_token is required")
	}
	return &Client{
		apiToken:    apiToken,
		workspaceID: workspaceID,
		debug:       debug,
	}, nil
}

// BackendRailwayCredentials represents Railway credentials retrieved from
// the backend credential store (clanker-backend).
type BackendRailwayCredentials struct {
	APIToken    string
	WorkspaceID string
}

// NewClientWithCredentials creates a new Railway client using backend
// credentials.
func NewClientWithCredentials(creds *BackendRailwayCredentials, debug bool) (*Client, error) {
	if creds == nil {
		return nil, fmt.Errorf("credentials cannot be nil")
	}
	if strings.TrimSpace(creds.APIToken) == "" {
		return nil, fmt.Errorf("railway api_token is required")
	}
	return &Client{
		apiToken:    creds.APIToken,
		workspaceID: creds.WorkspaceID,
		debug:       debug,
	}, nil
}

// GetAPIToken returns the API token.
func (c *Client) GetAPIToken() string { return c.apiToken }

// GetWorkspaceID returns the workspace ID (may be empty for personal accounts).
func (c *Client) GetWorkspaceID() string { return c.workspaceID }

// gqlRequest is the standard GraphQL request envelope.
type gqlRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

// gqlError is the standard GraphQL error entry.
type gqlError struct {
	Message    string         `json:"message"`
	Path       []any          `json:"path,omitempty"`
	Extensions map[string]any `json:"extensions,omitempty"`
}

// gqlResponse is the envelope Railway returns for every GraphQL call.
type gqlResponse struct {
	Data   json.RawMessage `json:"data,omitempty"`
	Errors []gqlError      `json:"errors,omitempty"`
}

// RunGQL executes a GraphQL query/mutation and decodes the `data` field
// into out. Returns an error for transport failures, HTTP non-2xx, or any
// non-empty `errors[]` array in the response body.
//
// The call is made via `curl` to stay consistent with the rest of the code
// base (matches the Vercel client pattern). Three attempts with exponential
// backoff; honours Retry-After when present.
func (c *Client) RunGQL(ctx context.Context, query string, vars map[string]any, out any) error {
	if _, err := exec.LookPath("curl"); err != nil {
		return fmt.Errorf("curl not found in PATH")
	}
	if strings.TrimSpace(query) == "" {
		return fmt.Errorf("railway: empty graphql query")
	}

	reqBody := gqlRequest{Query: query, Variables: vars}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("failed to marshal graphql request: %w", err)
	}

	// backoffs has three entries, meaning we make up to 3 total attempts
	// (the initial try + 2 retries) before giving up — matches the Vercel
	// client's retry budget.
	backoffs := []time.Duration{200 * time.Millisecond, 500 * time.Millisecond, 1200 * time.Millisecond}
	var lastErr error
	var lastStderr string
	var lastBody string

	for attempt := 0; attempt < len(backoffs); attempt++ {
		args := []string{
			"-s",
			"-w", "\n%{http_code}\n%header{Retry-After}",
			"-X", "POST",
			graphqlEndpoint,
			"-H", "Content-Type: application/json",
			"-H", fmt.Sprintf("Authorization: Bearer %s", c.apiToken),
			"--data-binary", "@-",
		}

		if c.debug {
			// Do not log the bearer token.
			fmt.Printf("[railway] POST %s (query=%d bytes, vars=%v)\n", graphqlEndpoint, len(query), redactVars(vars))
		}

		cmd := exec.CommandContext(ctx, "curl", args...)
		cmd.Stdin = bytes.NewReader(bodyBytes)

		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		runErr := cmd.Run()
		if runErr != nil {
			lastErr = runErr
			lastStderr = strings.TrimSpace(stderr.String())
			if ctx.Err() != nil {
				break
			}
			if !isRetryableError(lastStderr) {
				break
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoffs[attempt]):
			}
			continue
		}

		raw := stdout.String()
		body, status, retryAfter := splitCurlStatus(raw)
		lastBody = body

		if status < 200 || status >= 300 {
			// Retry on 429 / 5xx; honour Retry-After when present.
			if (status == 429 || status >= 500) && attempt < len(backoffs)-1 {
				wait := backoffs[attempt]
				if retryAfter != "" {
					if secs, err := strconv.Atoi(retryAfter); err == nil && secs > 0 {
						wait = time.Duration(secs) * time.Second
					}
				}
				if c.debug {
					fmt.Printf("[railway] http %d (retry in %s)\n", status, wait)
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(wait):
				}
				continue
			}
			return fmt.Errorf("railway graphql http %d: %s%s", status, truncateForError(body), errorHint(body))
		}

		var envelope gqlResponse
		if err := json.Unmarshal([]byte(body), &envelope); err != nil {
			return fmt.Errorf("railway graphql: failed to parse response: %w (body: %s)", err, truncateForError(body))
		}

		if len(envelope.Errors) > 0 {
			msgs := make([]string, 0, len(envelope.Errors))
			retryable := false
			for _, e := range envelope.Errors {
				msgs = append(msgs, e.Message)
				if isRetryableError(e.Message) {
					retryable = true
				}
			}
			combined := strings.Join(msgs, "; ")
			if retryable && attempt < len(backoffs)-1 {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(backoffs[attempt]):
				}
				continue
			}
			return fmt.Errorf("railway graphql errors: %s%s", combined, errorHint(combined))
		}

		if out == nil {
			return nil
		}
		if len(envelope.Data) == 0 {
			return fmt.Errorf("railway graphql: empty data envelope")
		}
		if err := json.Unmarshal(envelope.Data, out); err != nil {
			return fmt.Errorf("railway graphql: decode data: %w", err)
		}
		return nil
	}

	if lastErr == nil && lastBody != "" {
		return fmt.Errorf("railway graphql call failed after retries: %s", truncateForError(lastBody))
	}
	if lastErr == nil {
		return fmt.Errorf("railway graphql call failed")
	}
	return fmt.Errorf("railway graphql call failed: %w, stderr: %s%s", lastErr, lastStderr, errorHint(lastStderr))
}

// RunRailwayCLI executes the `railway` CLI, injecting the API token via env.
// Used for operations the GraphQL API does not expose directly (e.g. `railway
// up`). Returns combined stdout on success.
func (c *Client) RunRailwayCLI(ctx context.Context, args ...string) (string, error) {
	return c.RunRailwayCLIWithStdin(ctx, "", args...)
}

// RunRailwayCLIWithStdin executes the `railway` CLI piping stdin to the
// subprocess. Used for commands like `railway variable set` that accept
// multi-line input.
func (c *Client) RunRailwayCLIWithStdin(ctx context.Context, stdinData string, args ...string) (string, error) {
	if _, err := exec.LookPath("railway"); err != nil {
		return "", fmt.Errorf("railway CLI not found in PATH (install from https://docs.railway.com/guides/cli)")
	}

	cmd := exec.CommandContext(ctx, "railway", args...)
	cmd.Env = append(os.Environ(), fmt.Sprintf("RAILWAY_API_TOKEN=%s", c.apiToken))
	if c.workspaceID != "" {
		cmd.Env = append(cmd.Env, fmt.Sprintf("RAILWAY_WORKSPACE_ID=%s", c.workspaceID))
	}

	if stdinData != "" {
		cmd.Stdin = strings.NewReader(stdinData)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if c.debug {
		if stdinData != "" {
			fmt.Printf("[railway] railway %s (stdin piped)\n", strings.Join(args, " "))
		} else {
			fmt.Printf("[railway] railway %s\n", strings.Join(args, " "))
		}
	}

	if err := cmd.Run(); err != nil {
		stderrStr := strings.TrimSpace(stderr.String())
		return stdout.String(), fmt.Errorf("railway CLI failed: %w, stderr: %s%s", err, stderrStr, errorHint(stderrStr))
	}

	return stdout.String(), nil
}

// --- Typed GraphQL wrappers ---

// GetUser returns the authenticated Railway user.
func (c *Client) GetUser(ctx context.Context) (*User, error) {
	var out struct {
		Me User `json:"me"`
	}
	q := `query Me { me { id name email } }`
	if err := c.RunGQL(ctx, q, nil, &out); err != nil {
		return nil, err
	}
	return &out.Me, nil
}

// ListWorkspaces returns all workspaces visible to the caller. v1 public API
// exposes a limited surface so we fall back to the `me.workspaces` sub-field
// when the top-level `workspaces` query is unavailable.
func (c *Client) ListWorkspaces(ctx context.Context) ([]Workspace, error) {
	var out struct {
		Me struct {
			Workspaces []Workspace `json:"workspaces"`
		} `json:"me"`
	}
	q := `query Workspaces { me { workspaces { id name slug } } }`
	if err := c.RunGQL(ctx, q, nil, &out); err != nil {
		return nil, err
	}
	return out.Me.Workspaces, nil
}

// ListProjects returns all projects accessible to the current token, scoped
// to the workspace when one is configured.
func (c *Client) ListProjects(ctx context.Context) ([]Project, error) {
	var out struct {
		Projects struct {
			Edges []struct {
				Node Project `json:"node"`
			} `json:"edges"`
		} `json:"projects"`
	}
	q := `query Projects { projects { edges { node { id name description teamId createdAt updatedAt } } } }`
	if err := c.RunGQL(ctx, q, nil, &out); err != nil {
		return nil, err
	}
	projects := make([]Project, 0, len(out.Projects.Edges))
	for _, e := range out.Projects.Edges {
		projects = append(projects, e.Node)
	}
	return projects, nil
}

// GetProject returns a single project (with environments and services) by ID.
func (c *Client) GetProject(ctx context.Context, projectID string) (*Project, error) {
	q := `query Project($id: String!) {
		project(id: $id) {
			id name description teamId createdAt updatedAt
			environments { edges { node { id name projectId createdAt updatedAt } } }
			services { edges { node { id name projectId createdAt updatedAt } } }
		}
	}`
	var out struct {
		Project struct {
			Project
			Environments struct {
				Edges []struct {
					Node Environment `json:"node"`
				} `json:"edges"`
			} `json:"environments"`
			Services struct {
				Edges []struct {
					Node Service `json:"node"`
				} `json:"edges"`
			} `json:"services"`
		} `json:"project"`
	}
	if err := c.RunGQL(ctx, q, map[string]any{"id": projectID}, &out); err != nil {
		return nil, err
	}
	proj := out.Project.Project
	for _, e := range out.Project.Environments.Edges {
		proj.Environments = append(proj.Environments, e.Node)
	}
	for _, e := range out.Project.Services.Edges {
		proj.Services = append(proj.Services, e.Node)
	}
	return &proj, nil
}

// ListServices returns services for a given project.
func (c *Client) ListServices(ctx context.Context, projectID string) ([]Service, error) {
	q := `query Services($projectId: String!) {
		project(id: $projectId) { services { edges { node { id name projectId createdAt updatedAt } } } }
	}`
	var out struct {
		Project struct {
			Services struct {
				Edges []struct {
					Node Service `json:"node"`
				} `json:"edges"`
			} `json:"services"`
		} `json:"project"`
	}
	if err := c.RunGQL(ctx, q, map[string]any{"projectId": projectID}, &out); err != nil {
		return nil, err
	}
	services := make([]Service, 0, len(out.Project.Services.Edges))
	for _, e := range out.Project.Services.Edges {
		services = append(services, e.Node)
	}
	return services, nil
}

// ListDeployments returns deployments matching the given filters. Any of the
// filter strings may be empty.
func (c *Client) ListDeployments(ctx context.Context, projectID, environmentID, serviceID string, limit int) ([]Deployment, error) {
	if limit <= 0 {
		limit = 20
	}
	q := `query Deployments($input: DeploymentListInput!, $first: Int) {
		deployments(input: $input, first: $first) {
			edges { node {
				id status url staticUrl projectId serviceId environmentId createdAt updatedAt
				canRedeploy canRollback
				meta { commitHash commitMessage branch }
			} }
		}
	}`
	input := map[string]any{}
	if projectID != "" {
		input["projectId"] = projectID
	}
	if environmentID != "" {
		input["environmentId"] = environmentID
	}
	if serviceID != "" {
		input["serviceId"] = serviceID
	}
	var out struct {
		Deployments struct {
			Edges []struct {
				Node Deployment `json:"node"`
			} `json:"edges"`
		} `json:"deployments"`
	}
	if err := c.RunGQL(ctx, q, map[string]any{"input": input, "first": limit}, &out); err != nil {
		return nil, err
	}
	deployments := make([]Deployment, 0, len(out.Deployments.Edges))
	for _, e := range out.Deployments.Edges {
		deployments = append(deployments, e.Node)
	}
	return deployments, nil
}

// GetDeployment fetches a single deployment by ID.
func (c *Client) GetDeployment(ctx context.Context, deploymentID string) (*Deployment, error) {
	q := `query Deployment($id: String!) {
		deployment(id: $id) {
			id status url staticUrl projectId serviceId environmentId createdAt updatedAt
			canRedeploy canRollback
			meta { commitHash commitMessage branch }
		}
	}`
	var out struct {
		Deployment Deployment `json:"deployment"`
	}
	if err := c.RunGQL(ctx, q, map[string]any{"id": deploymentID}, &out); err != nil {
		return nil, err
	}
	return &out.Deployment, nil
}

// CancelDeployment cancels an in-progress deployment.
func (c *Client) CancelDeployment(ctx context.Context, deploymentID string) error {
	q := `mutation DeploymentCancel($id: String!) { deploymentCancel(id: $id) }`
	return c.RunGQL(ctx, q, map[string]any{"id": deploymentID}, nil)
}

// RedeployDeployment triggers a redeploy of an existing deployment.
func (c *Client) RedeployDeployment(ctx context.Context, deploymentID string) error {
	q := `mutation DeploymentRedeploy($id: String!) { deploymentRedeploy(id: $id) { id } }`
	return c.RunGQL(ctx, q, map[string]any{"id": deploymentID}, nil)
}

// ListDomains returns service + custom domains for a project. Service
// domains (xxx.up.railway.app) and custom domains are returned in a single
// slice; IsCustom distinguishes them.
func (c *Client) ListDomains(ctx context.Context, projectID string) ([]Domain, error) {
	q := `query Domains($projectId: String!) {
		domains(projectId: $projectId) {
			customDomains   { id domain serviceId environmentId projectId status targetPort createdAt }
			serviceDomains  { id domain serviceId environmentId projectId targetPort createdAt }
		}
	}`
	var out struct {
		Domains struct {
			CustomDomains  []Domain `json:"customDomains"`
			ServiceDomains []Domain `json:"serviceDomains"`
		} `json:"domains"`
	}
	if err := c.RunGQL(ctx, q, map[string]any{"projectId": projectID}, &out); err != nil {
		return nil, err
	}
	domains := make([]Domain, 0, len(out.Domains.CustomDomains)+len(out.Domains.ServiceDomains))
	for i := range out.Domains.CustomDomains {
		out.Domains.CustomDomains[i].IsCustom = true
		domains = append(domains, out.Domains.CustomDomains[i])
	}
	for i := range out.Domains.ServiceDomains {
		out.Domains.ServiceDomains[i].IsCustom = false
		domains = append(domains, out.Domains.ServiceDomains[i])
	}
	return domains, nil
}

// ListVariables returns environment variables for a service+environment.
// Values are never returned unless the caller has explicitly elevated scope.
func (c *Client) ListVariables(ctx context.Context, projectID, environmentID, serviceID string) (map[string]string, error) {
	q := `query Variables($projectId: String!, $environmentId: String!, $serviceId: String) {
		variables(projectId: $projectId, environmentId: $environmentId, serviceId: $serviceId)
	}`
	vars := map[string]any{
		"projectId":     projectID,
		"environmentId": environmentID,
	}
	if serviceID != "" {
		vars["serviceId"] = serviceID
	}
	var out struct {
		Variables map[string]string `json:"variables"`
	}
	if err := c.RunGQL(ctx, q, vars, &out); err != nil {
		return nil, err
	}
	if out.Variables == nil {
		out.Variables = map[string]string{}
	}
	return out.Variables, nil
}

// UpsertVariable creates or updates an environment variable.
func (c *Client) UpsertVariable(ctx context.Context, projectID, environmentID, serviceID, name, value string) error {
	q := `mutation VariableUpsert($input: VariableUpsertInput!) { variableUpsert(input: $input) }`
	input := map[string]any{
		"projectId":     projectID,
		"environmentId": environmentID,
		"name":          name,
		"value":         value,
	}
	if serviceID != "" {
		input["serviceId"] = serviceID
	}
	return c.RunGQL(ctx, q, map[string]any{"input": input}, nil)
}

// DeleteVariable removes a variable.
func (c *Client) DeleteVariable(ctx context.Context, projectID, environmentID, serviceID, name string) error {
	q := `mutation VariableDelete($input: VariableDeleteInput!) { variableDelete(input: $input) }`
	input := map[string]any{
		"projectId":     projectID,
		"environmentId": environmentID,
		"name":          name,
	}
	if serviceID != "" {
		input["serviceId"] = serviceID
	}
	return c.RunGQL(ctx, q, map[string]any{"input": input}, nil)
}

// ListVolumes returns volumes for a project.
func (c *Client) ListVolumes(ctx context.Context, projectID string) ([]Volume, error) {
	q := `query Volumes($projectId: String!) {
		project(id: $projectId) {
			volumes { edges { node { id name projectId createdAt } } }
		}
	}`
	var out struct {
		Project struct {
			Volumes struct {
				Edges []struct {
					Node Volume `json:"node"`
				} `json:"edges"`
			} `json:"volumes"`
		} `json:"project"`
	}
	if err := c.RunGQL(ctx, q, map[string]any{"projectId": projectID}, &out); err != nil {
		return nil, err
	}
	volumes := make([]Volume, 0, len(out.Project.Volumes.Edges))
	for _, e := range out.Project.Volumes.Edges {
		volumes = append(volumes, e.Node)
	}
	return volumes, nil
}

// GetUsage returns a workspace-level usage summary. If no workspace ID is
// provided, the client's configured workspace is used.
func (c *Client) GetUsage(ctx context.Context, workspaceID string) (*UsageSummary, error) {
	wid := strings.TrimSpace(workspaceID)
	if wid == "" {
		wid = c.workspaceID
	}
	if wid == "" {
		return nil, fmt.Errorf("railway: workspace_id required for usage query")
	}
	q := `query Usage($workspaceId: String!) { usage(workspaceId: $workspaceId) }`
	var out struct {
		Usage json.RawMessage `json:"usage"`
	}
	if err := c.RunGQL(ctx, q, map[string]any{"workspaceId": wid}, &out); err != nil {
		return nil, err
	}
	var summary UsageSummary
	if len(out.Usage) > 0 {
		// Railway returns a JSON scalar here; surface decode failures so
		// callers can distinguish "no data" from "bad payload".
		if err := json.Unmarshal(out.Usage, &summary); err != nil {
			return nil, fmt.Errorf("decode usage payload: %w", err)
		}
	}
	return &summary, nil
}

// ListDeploymentLogs retrieves build or runtime logs for a deployment.
// buildLogs=true returns build output; false returns runtime logs.
func (c *Client) ListDeploymentLogs(ctx context.Context, deploymentID string, buildLogs bool, limit int) ([]map[string]any, error) {
	if limit <= 0 {
		limit = 100
	}
	field := "deploymentLogs"
	if buildLogs {
		field = "buildLogs"
	}
	q := fmt.Sprintf(`query Logs($id: String!, $limit: Int) { %s(deploymentId: $id, limit: $limit) { timestamp message severity } }`, field)
	var raw map[string]json.RawMessage
	if err := c.RunGQL(ctx, q, map[string]any{"id": deploymentID, "limit": limit}, &raw); err != nil {
		return nil, err
	}
	body, ok := raw[field]
	if !ok || len(body) == 0 {
		return nil, nil
	}
	var entries []map[string]any
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil, fmt.Errorf("failed to decode %s: %w", field, err)
	}
	return entries, nil
}

// GetRelevantContext gathers Railway context for LLM queries. The output is
// a best-effort dump of the resources most likely relevant to the user's
// question. Sections are keyword-gated to keep the context compact.
//
// Fan-out across projects is capped with errgroup so we don't hammer the
// Railway API with N parallel round-trips on large workspaces.
func (c *Client) GetRelevantContext(ctx context.Context, question string) (string, error) {
	questionLower := strings.ToLower(strings.TrimSpace(question))

	// Projects are cheap and many sections need them; fetch once and share.
	var (
		cachedProjects    []Project
		cachedProjectsErr error
		cachedProjectsOK  bool
	)
	getProjects := func() ([]Project, error) {
		if cachedProjectsOK || cachedProjectsErr != nil {
			return cachedProjects, cachedProjectsErr
		}
		cachedProjects, cachedProjectsErr = c.ListProjects(ctx)
		cachedProjectsOK = cachedProjectsErr == nil
		return cachedProjects, cachedProjectsErr
	}

	type section struct {
		name string
		keys []string
		run  func() (string, error)
	}

	sections := []section{
		{
			name: "Projects",
			keys: []string{"project", "railway", "deploy", "service", "app", "nixpacks", "environment"},
			run: func() (string, error) {
				projects, err := getProjects()
				if err != nil {
					return "", err
				}
				return jsonPretty(projects)
			},
		},
		{
			name: "Services",
			keys: []string{"service", "container", "worker", "web", "api", "backend"},
			run: func() (string, error) {
				projects, err := getProjects()
				if err != nil {
					return "", err
				}
				results := make([]string, len(projects))
				g, gctx := errgroup.WithContext(ctx)
				g.SetLimit(3)
				for i, p := range projects {
					i, p := i, p
					g.Go(func() error {
						services, err := c.ListServices(gctx, p.ID)
						if err != nil {
							return err
						}
						if len(services) == 0 {
							return nil
						}
						var sb strings.Builder
						sb.WriteString(fmt.Sprintf("Project %s (%s):\n", p.Name, p.ID))
						for _, s := range services {
							sb.WriteString(fmt.Sprintf("  - %s (%s)\n", s.Name, s.ID))
						}
						results[i] = sb.String()
						return nil
					})
				}
				if err := g.Wait(); err != nil {
					return "", err
				}
				return strings.Join(filterEmpty(results), ""), nil
			},
		},
		{
			name: "Deployments",
			keys: []string{"deployment", "deploy", "preview", "production", "build", "rollback", "redeploy", "logs"},
			run: func() (string, error) {
				deployments, err := c.ListDeployments(ctx, "", "", "", 20)
				if err != nil {
					return "", err
				}
				return jsonPretty(deployments)
			},
		},
		{
			name: "Domains",
			keys: []string{"domain", "dns", "custom domain", "up.railway.app"},
			run: func() (string, error) {
				projects, err := getProjects()
				if err != nil {
					return "", err
				}
				results := make([]string, len(projects))
				g, gctx := errgroup.WithContext(ctx)
				g.SetLimit(3)
				for i, p := range projects {
					i, p := i, p
					g.Go(func() error {
						domains, err := c.ListDomains(gctx, p.ID)
						if err != nil {
							// non-fatal; skip projects we can't enumerate
							return nil
						}
						if len(domains) == 0 {
							return nil
						}
						var sb strings.Builder
						sb.WriteString(fmt.Sprintf("Project %s:\n", p.Name))
						for _, d := range domains {
							kind := "service"
							if d.IsCustom {
								kind = "custom"
							}
							sb.WriteString(fmt.Sprintf("  - %s [%s]\n", d.Domain, kind))
						}
						results[i] = sb.String()
						return nil
					})
				}
				if err := g.Wait(); err != nil {
					return "", err
				}
				return strings.Join(filterEmpty(results), ""), nil
			},
		},
		{
			name: "Variables",
			keys: []string{"variable", "env", "environment variable", "secret", "config"},
			run: func() (string, error) {
				// Variables require project+environment+service scope, so we
				// still need GetProject here (ListProjects doesn't return
				// environments). Cap parallelism to avoid N round-trips.
				projects, err := getProjects()
				if err != nil || len(projects) == 0 {
					return "", err
				}
				results := make([]string, len(projects))
				g, gctx := errgroup.WithContext(ctx)
				g.SetLimit(3)
				for i, p := range projects {
					i, p := i, p
					g.Go(func() error {
						proj, err := c.GetProject(gctx, p.ID)
						if err != nil {
							return nil
						}
						if len(proj.Environments) == 0 {
							return nil
						}
						envID := proj.Environments[0].ID
						envName := proj.Environments[0].Name
						var sb strings.Builder
						for _, svc := range proj.Services {
							keys, err := c.ListVariables(gctx, p.ID, envID, svc.ID)
							if err != nil {
								continue
							}
							if len(keys) == 0 {
								continue
							}
							sb.WriteString(fmt.Sprintf("Project %s / env %s / service %s:\n", p.Name, envName, svc.Name))
							for k := range keys {
								sb.WriteString(fmt.Sprintf("  - %s\n", k))
							}
						}
						results[i] = sb.String()
						return nil
					})
				}
				if err := g.Wait(); err != nil {
					return "", err
				}
				return strings.Join(filterEmpty(results), ""), nil
			},
		},
		{
			name: "Usage",
			keys: []string{"usage", "cost", "billing", "cpu", "memory", "bandwidth", "egress"},
			run: func() (string, error) {
				if c.workspaceID == "" {
					return "", fmt.Errorf("workspace_id required for usage")
				}
				usage, err := c.GetUsage(ctx, c.workspaceID)
				if err != nil {
					return "", err
				}
				return jsonPretty(usage)
			},
		},
	}

	defaults := map[string]bool{
		"Projects": true,
	}

	var out strings.Builder
	var warnings []string

	for _, s := range sections {
		if questionLower != "" && len(s.keys) > 0 {
			matched := false
			for _, key := range s.keys {
				if strings.Contains(questionLower, key) {
					matched = true
					break
				}
			}
			if !matched && !defaults[s.name] {
				continue
			}
		}

		body, err := s.run()
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", s.name, err))
			continue
		}
		if strings.TrimSpace(body) == "" {
			continue
		}
		out.WriteString(s.name + ":\n")
		out.WriteString(body)
		if !strings.HasSuffix(body, "\n") {
			out.WriteString("\n")
		}
		out.WriteString("\n")
	}

	if len(warnings) > 0 {
		out.WriteString("Railway Warnings:\n")
		for i, w := range warnings {
			if i >= 8 {
				out.WriteString("- (additional warnings omitted)\n")
				break
			}
			out.WriteString("- ")
			out.WriteString(w)
			out.WriteString("\n")
		}
		out.WriteString("\n")
	}

	if strings.TrimSpace(out.String()) == "" {
		return "No Railway data available (missing permissions or no resources).", nil
	}
	return out.String(), nil
}

// --- helpers ---

func jsonPretty(v any) (string, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// splitCurlStatus parses the stdout produced by our curl invocation, which
// appends the HTTP status and Retry-After header via `-w`. Returns the body,
// the parsed status code, and the Retry-After header (if any).
func splitCurlStatus(raw string) (body string, status int, retryAfter string) {
	lines := strings.Split(raw, "\n")
	if len(lines) < 2 {
		return strings.TrimSpace(raw), 0, ""
	}
	// Last line: Retry-After (may be empty). Second-to-last: status code.
	// Everything before that is the response body.
	retryAfter = strings.TrimSpace(lines[len(lines)-1])
	statusLine := strings.TrimSpace(lines[len(lines)-2])
	bodyLines := lines[:len(lines)-2]
	body = strings.Join(bodyLines, "\n")
	if code, err := strconv.Atoi(statusLine); err == nil {
		status = code
	}
	return strings.TrimSpace(body), status, retryAfter
}

// redactVars returns a version of vars safe to log (redacts values for
// anything that looks like a secret or a long string).
func redactVars(vars map[string]any) map[string]any {
	if vars == nil {
		return nil
	}
	out := make(map[string]any, len(vars))
	for k, v := range vars {
		ks := strings.ToLower(k)
		if ks == "value" || strings.Contains(ks, "token") || strings.Contains(ks, "secret") || strings.Contains(ks, "password") {
			out[k] = "***"
			continue
		}
		out[k] = v
	}
	return out
}

func truncateForError(body string) string {
	const maxLen = 600
	trimmed := strings.TrimSpace(body)
	if len(trimmed) > maxLen {
		return trimmed[:maxLen] + "..."
	}
	return trimmed
}

// isRetryableError determines whether to retry a Railway API failure.
func isRetryableError(s string) bool {
	lower := strings.ToLower(s)
	retryableCodes := []string{
		"rate limit",
		"rate_limited",
		"too many requests",
		"internal server error",
		"bad gateway",
		"service unavailable",
		"gateway timeout",
		"try again",
	}
	for _, code := range retryableCodes {
		if strings.Contains(lower, code) {
			return true
		}
	}
	if strings.Contains(lower, "timeout") || strings.Contains(lower, "timed out") {
		return true
	}
	if strings.Contains(lower, "connection refused") || strings.Contains(lower, "connection reset") {
		return true
	}
	if strings.Contains(lower, "temporarily unavailable") {
		return true
	}
	return false
}

// filterEmpty drops empty strings so callers can Join over partial fan-out
// results without stray blank lines.
func filterEmpty(ss []string) []string {
	out := ss[:0]
	for _, s := range ss {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// errorHint returns an actionable hint for common Railway error messages.
func errorHint(stderr string) string {
	lower := strings.ToLower(stderr)
	switch {
	case strings.Contains(lower, "unauthorized") || strings.Contains(lower, "invalid token") || strings.Contains(lower, "not authenticated"):
		return " (hint: check your RAILWAY_API_TOKEN is valid — v1 requires an account token, not a project deploy token)"
	case strings.Contains(lower, "forbidden"):
		return " (hint: your Railway token may be missing workspace scope)"
	case strings.Contains(lower, "not found"):
		return " (hint: resource not found — check project/service/deployment IDs)"
	case strings.Contains(lower, "rate limit"):
		return " (hint: rate limited, retrying with backoff)"
	default:
		return ""
	}
}
