package maker

import (
	"context"
	"fmt"
	"strings"

	"github.com/bgdnvk/clanker/internal/verda"
)

// ExecuteVerdaPlan executes a Verda Cloud maker plan. Commands take the form
// ["verda-api", METHOD, "/v1/path", BODY?] and are dispatched to the shared
// verda.Client which handles OAuth2, retries, and typed error decoding.
//
// Unlike the Vercel/Cloudflare/Hetzner executors, we don't shell out — Verda's
// REST surface is well-defined and the verda package already owns the auth
// plumbing, so embedding that client is cleaner than piping args through a
// CLI. The tradeoff is that the maker plan format is Verda-specific (verb
// "verda-api" rather than a standard CLI binary name) but the rules file
// documents the shape clearly for the planner LLM.
func ExecuteVerdaPlan(ctx context.Context, plan *Plan, opts ExecOptions) error {
	if plan == nil {
		return fmt.Errorf("nil plan")
	}
	if opts.Writer == nil {
		return fmt.Errorf("missing output writer")
	}
	if opts.VerdaClientID == "" || opts.VerdaClientSecret == "" {
		return fmt.Errorf("missing verda client_id / client_secret (set verda.client_id in ~/.clanker.yaml, export VERDA_CLIENT_ID / VERDA_CLIENT_SECRET, or run `verda auth login`)")
	}

	client, err := verda.NewClient(opts.VerdaClientID, opts.VerdaClientSecret, opts.VerdaProjectID, opts.Debug)
	if err != nil {
		return fmt.Errorf("build verda client: %w", err)
	}

	bindings := make(map[string]string)

	for idx, cmdSpec := range plan.Commands {
		args := make([]string, 0, len(cmdSpec.Args)+2)
		args = append(args, cmdSpec.Args...)
		args = applyPlanBindings(args, bindings)

		if err := validateVerdaCommand(args, opts.Destroyer); err != nil {
			return fmt.Errorf("command %d rejected: %w", idx+1, err)
		}
		if hasUnresolvedPlaceholders(args) {
			return fmt.Errorf("command %d has unresolved placeholders after substitutions", idx+1)
		}

		method := strings.ToUpper(args[1])
		path := args[2]
		body := ""
		if len(args) >= 4 {
			body = args[3]
		}

		_, _ = fmt.Fprintf(opts.Writer, "[maker] running %d/%d: verda-api %s %s\n",
			idx+1, len(plan.Commands), method, path)
		if opts.Debug && body != "" {
			_, _ = fmt.Fprintf(opts.Writer, "[maker]   body: %s\n", body)
		}

		out, err := client.RunAPIWithContext(ctx, method, path, body)
		if err != nil {
			return fmt.Errorf("verda command %d failed (%s %s): %w", idx+1, method, path, err)
		}

		if strings.TrimSpace(out) != "" {
			_, _ = fmt.Fprintln(opts.Writer, out)
		}
		learnPlanBindingsFromProduces(cmdSpec.Produces, out, bindings)
	}

	return nil
}

// validateVerdaCommand rejects anything that isn't a well-formed verda-api
// call. The planner prompt restricts args to exactly [verda-api, METHOD, path,
// body?] and blocks CLI-style verbs like `verda vm create` that would require
// shelling out. Destructive actions are gated behind --destroyer.
func validateVerdaCommand(args []string, allowDestructive bool) error {
	if len(args) < 3 {
		return fmt.Errorf("verda plan commands require at least 3 args [verb, method, path], got %d", len(args))
	}
	if len(args) > 4 {
		return fmt.Errorf("verda plan commands take at most 4 args [verb, method, path, body], got %d", len(args))
	}

	verb := strings.ToLower(strings.TrimSpace(args[0]))
	if verb != "verda-api" {
		return fmt.Errorf("only verda-api verb is supported (got %q); see the planner prompt for the schema", args[0])
	}

	method := strings.ToUpper(strings.TrimSpace(args[1]))
	switch method {
	case "GET", "POST", "PUT", "PATCH", "DELETE":
	default:
		return fmt.Errorf("unsupported HTTP method %q; use GET|POST|PUT|PATCH|DELETE", args[1])
	}

	path := args[2]
	if !strings.HasPrefix(path, "/v1/") {
		return fmt.Errorf("verda paths must start with /v1/, got %q", path)
	}
	// Reject shell metacharacters in every token so a compromised planner
	// can't smuggle an injection into the path or body.
	for _, a := range args {
		if strings.ContainsAny(a, "\n\r") {
			return fmt.Errorf("newlines in args are not allowed")
		}
	}

	if !allowDestructive {
		if isVerdaDestructive(method, path, verdaBodyOrEmpty(args)) {
			return fmt.Errorf("destructive verda operation blocked (use --destroyer to allow): %s %s", method, path)
		}
	}

	return nil
}

// verdaBodyOrEmpty returns the optional body arg or an empty string. Exists
// so validators can inspect the body without every caller doing a len check.
func verdaBodyOrEmpty(args []string) string {
	if len(args) >= 4 {
		return args[3]
	}
	return ""
}

// isVerdaDestructive returns true when a command would delete, discontinue,
// hibernate, or force-stop resources. This is the gate for --destroyer mode.
// We err on the side of caution — anything ambiguous is treated as
// destructive so users always opt in explicitly.
func isVerdaDestructive(method, path, body string) bool {
	if method == "DELETE" {
		return true
	}
	if method == "PUT" && (strings.HasPrefix(path, "/v1/instances") || strings.HasPrefix(path, "/v1/clusters") || strings.HasPrefix(path, "/v1/volumes")) {
		lower := strings.ToLower(body)
		for _, verb := range []string{
			`"action":"delete"`,
			`"action":"discontinue"`,
			`"action":"force_shutdown"`,
			`"action":"delete_stuck"`,
			`"action":"hibernate"`,
		} {
			if strings.Contains(lower, strings.ReplaceAll(verb, " ", "")) {
				return true
			}
		}
	}
	return false
}
