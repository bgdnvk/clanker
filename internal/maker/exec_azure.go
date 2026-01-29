package maker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

func ExecuteAzurePlan(ctx context.Context, plan *Plan, opts ExecOptions) error {
	if plan == nil {
		return fmt.Errorf("nil plan")
	}
	if opts.Writer == nil {
		return fmt.Errorf("missing output writer")
	}

	subscriptionID := strings.TrimSpace(opts.AzureSubscriptionID)
	if subscriptionID != "" {
		if err := preflightAzureSubscription(ctx, subscriptionID); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(opts.Writer, "[maker] azure subscription set: %s\n", subscriptionID)
	}

	bindings := make(map[string]string)

	for idx, cmdSpec := range plan.Commands {
		if err := validateAzCommand(cmdSpec.Args, opts.Destroyer); err != nil {
			return fmt.Errorf("command %d rejected: %w", idx+1, err)
		}

		args := make([]string, 0, len(cmdSpec.Args)+10)
		args = append(args, cmdSpec.Args...)
		args = applyPlanBindings(args, bindings)
		args = ensureAzJSONOutput(args, cmdSpec.Produces)
		args = ensureAzSubscription(args, subscriptionID)
		args = ensureAzOnlyShowErrors(args)

		// Similar to how we pin --project for gcloud: keep az's active subscription stable.
		if subscriptionID != "" {
			if err := setAzureSubscription(ctx, subscriptionID); err != nil {
				return fmt.Errorf("azure subscription set failed: %w%s", err, azErrorHint(err.Error()))
			}
		}

		if hasUnresolvedPlaceholders(args) {
			return fmt.Errorf("command %d has unresolved placeholders after substitutions", idx+1)
		}

		_, _ = fmt.Fprintf(opts.Writer, "[maker] running %d/%d: %s\n", idx+1, len(plan.Commands), formatAzArgsForLog(args))

		out, runErr := runAzCommandStreaming(ctx, args, opts.Writer)
		if runErr != nil {
			// If the subscription is valid but this resource provider isn't registered yet,
			// try registering it once (similar spirit to gcloud auth hints, but action-based).
			if subscriptionID != "" && isAzProviderRegistrationError(out) {
				if ns := azureNamespaceForAzCommand(args); ns != "" {
					_, _ = fmt.Fprintf(opts.Writer, "[maker] azure provider not registered: %s (attempting register)\n", ns)
					if regErr := ensureAzureProviderRegistered(ctx, subscriptionID, ns, 90*time.Second); regErr == nil {
						out2, runErr2 := runAzCommandStreaming(ctx, args, opts.Writer)
						if runErr2 == nil {
							out = out2
							runErr = nil
						} else {
							hint := azErrorHint(out2)
							if hint != "" {
								return fmt.Errorf("az command %d failed: %w%s", idx+1, runErr2, hint)
							}
							return fmt.Errorf("az command %d failed: %w", idx+1, runErr2)
						}
					}
				}
			}
			if runErr == nil {
				learnPlanBindingsFromProduces(cmdSpec.Produces, out, bindings)
				continue
			}

			// If Azure CLI loses track of subscription mid-run, try resetting once and retry.
			if subscriptionID != "" && isAzSubscriptionNotFound(out) {
				_ = setAzureSubscription(ctx, subscriptionID)
				out2, runErr2 := runAzCommandStreaming(ctx, args, opts.Writer)
				if runErr2 == nil {
					out = out2
				} else {
					hint := azErrorHint(out2)
					if hint != "" {
						return fmt.Errorf("az command %d failed: %w%s", idx+1, runErr2, hint)
					}
					return fmt.Errorf("az command %d failed: %w", idx+1, runErr2)
				}
			} else {
				hint := azErrorHint(out)
				if hint != "" {
					return fmt.Errorf("az command %d failed: %w%s", idx+1, runErr, hint)
				}
				return fmt.Errorf("az command %d failed: %w", idx+1, runErr)
			}
		}

		learnPlanBindingsFromProduces(cmdSpec.Produces, out, bindings)
	}

	return nil
}

func preflightAzureSubscription(ctx context.Context, subscriptionID string) error {
	// 1) Validate the subscription exists for the current az login context.
	_, err := runAzCommandCapture(ctx, []string{"account", "show", "--subscription", subscriptionID, "--output", "json", "--only-show-errors"})
	if err != nil {
		// Provide a friendlier hint, similar in spirit to gcloudErrorHint.
		hint := azErrorHint(err.Error())
		if hint == "" {
			hint = " (hint: run 'az login' and verify your subscription with 'az account list')"
		}
		return fmt.Errorf("azure subscription preflight failed: %w%s", err, hint)
	}

	// 2) Explicitly set active subscription for the duration of this process.
	if err := setAzureSubscription(ctx, subscriptionID); err != nil {
		hint := azErrorHint(err.Error())
		return fmt.Errorf("azure subscription set failed: %w%s", err, hint)
	}
	return nil
}

func setAzureSubscription(ctx context.Context, subscriptionID string) error {
	_, err := runAzCommandCapture(ctx, []string{"account", "set", "--subscription", subscriptionID, "--only-show-errors"})
	return err
}

func runAzCommandCapture(ctx context.Context, args []string) (string, error) {
	bin, err := exec.LookPath("az")
	if err != nil {
		return "", fmt.Errorf("az not found in PATH: %w", err)
	}

	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("az %s failed: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func isAzSubscriptionNotFound(output string) bool {
	lower := strings.ToLower(output)
	return strings.Contains(lower, "subscriptionnotfound") || strings.Contains(lower, "subscription was not found")
}

func isAzProviderRegistrationError(output string) bool {
	lower := strings.ToLower(output)
	// Common variants.
	if strings.Contains(lower, "missingsubscriptionregistration") {
		return true
	}
	if strings.Contains(lower, "noregisteredproviderfound") {
		return true
	}
	if strings.Contains(lower, "notregistered") && strings.Contains(lower, "microsoft.") {
		return true
	}
	// Azure sometimes surfaces provider issues as SubscriptionNotFound for specific RPs.
	if strings.Contains(lower, "subscriptionnotfound") {
		return true
	}
	return false
}

func azureNamespaceForAzCommand(args []string) string {
	// args are the az subcommand args (no leading "az").
	if len(args) == 0 {
		return ""
	}

	lower := make([]string, 0, len(args))
	for _, a := range args {
		lower = append(lower, strings.ToLower(strings.TrimSpace(a)))
	}

	// Storage.
	if lower[0] == "storage" {
		return "Microsoft.Storage"
	}

	// App Service / Functions.
	if lower[0] == "functionapp" || lower[0] == "webapp" || lower[0] == "appservice" {
		return "Microsoft.Web"
	}

	// AKS.
	if lower[0] == "aks" {
		return "Microsoft.ContainerService"
	}

	// Key Vault.
	if lower[0] == "keyvault" {
		return "Microsoft.KeyVault"
	}

	// Cosmos.
	if lower[0] == "cosmosdb" {
		return "Microsoft.DocumentDB"
	}

	// Container Apps.
	if lower[0] == "containerapp" {
		return "Microsoft.App"
	}

	// Networking.
	if lower[0] == "network" {
		return "Microsoft.Network"
	}

	return ""
}

func ensureAzureProviderRegistered(ctx context.Context, subscriptionID string, namespace string, timeout time.Duration) error {
	if strings.TrimSpace(subscriptionID) == "" || strings.TrimSpace(namespace) == "" {
		return fmt.Errorf("missing subscription or namespace")
	}

	state, _ := runAzCommandCapture(ctx, []string{"provider", "show", "--namespace", namespace, "--subscription", subscriptionID, "--query", "registrationState", "-o", "tsv", "--only-show-errors"})
	if strings.EqualFold(strings.TrimSpace(state), "Registered") {
		return nil
	}

	_, _ = runAzCommandCapture(ctx, []string{"provider", "register", "--namespace", namespace, "--subscription", subscriptionID, "--only-show-errors"})

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		state, _ := runAzCommandCapture(ctx, []string{"provider", "show", "--namespace", namespace, "--subscription", subscriptionID, "--query", "registrationState", "-o", "tsv", "--only-show-errors"})
		st := strings.TrimSpace(state)
		if strings.EqualFold(st, "Registered") {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}

	return fmt.Errorf("provider %s still not registered after %s", namespace, timeout.String())
}

func azErrorHint(output string) string {
	lower := strings.ToLower(output)
	switch {
	case strings.Contains(lower, "az login") || strings.Contains(lower, "please run 'az login'"):
		return " (hint: run 'az login' in the same environment running clanker-cloud)"
	case strings.Contains(lower, "subscriptionnotfound") || strings.Contains(lower, "subscription was not found"):
		return " (hint: verify subscription ID with 'az account list' and ensure you're logged into the correct tenant)"
	case strings.Contains(lower, "expired") && strings.Contains(lower, "token"):
		return " (hint: re-authenticate with 'az login' or refresh your credentials)"
	case strings.Contains(lower, "authorizationfailed") || (strings.Contains(lower, "authorization") && strings.Contains(lower, "failed")):
		return " (hint: missing Azure RBAC permissions for this operation)"
	case strings.Contains(lower, "permission") && (strings.Contains(lower, "denied") || strings.Contains(lower, "forbidden")):
		return " (hint: missing Azure RBAC permissions for this operation)"
	default:
		return ""
	}
}

func validateAzCommand(args []string, allowDestructive bool) error {
	if len(args) == 0 {
		return fmt.Errorf("empty args")
	}

	first := strings.ToLower(strings.TrimSpace(args[0]))
	// Reject obvious non-az plans.
	switch {
	case first == "aws" || first == "gcloud" || first == "kubectl" || first == "helm" || first == "eksctl" || first == "kubeadm":
		return fmt.Errorf("non-az command is not allowed: %q", args[0])
	case first == "python" || strings.HasPrefix(first, "python"):
		return fmt.Errorf("non-az command is not allowed: %q", args[0])
	case first == "node" || first == "npm" || first == "npx":
		return fmt.Errorf("non-az command is not allowed: %q", args[0])
	case first == "bash" || first == "sh" || first == "zsh" || first == "fish":
		return fmt.Errorf("non-az command is not allowed: %q", args[0])
	case first == "curl" || first == "wget":
		return fmt.Errorf("non-az command is not allowed: %q", args[0])
	case first == "terraform" || first == "tofu" || first == "make":
		return fmt.Errorf("non-az command is not allowed: %q", args[0])
	}

	for _, a := range args {
		lower := strings.ToLower(a)
		if strings.Contains(lower, ";") || strings.Contains(lower, "|") || strings.Contains(lower, "&&") || strings.Contains(lower, "||") {
			return fmt.Errorf("shell operators are not allowed")
		}
		if allowDestructive {
			continue
		}
		if strings.Contains(lower, "delete") || strings.Contains(lower, "remove") || strings.Contains(lower, "purge") || strings.Contains(lower, "destroy") {
			return fmt.Errorf("destructive verbs are blocked")
		}
	}

	return nil
}

func ensureAzOnlyShowErrors(args []string) []string {
	for i := 0; i < len(args); i++ {
		if strings.EqualFold(strings.TrimSpace(args[i]), "--only-show-errors") {
			return args
		}
	}
	return append(append([]string{}, args...), "--only-show-errors")
}

func ensureAzSubscription(args []string, subscriptionID string) []string {
	if subscriptionID == "" {
		return args
	}
	for i := 0; i < len(args); i++ {
		a := strings.TrimSpace(args[i])
		lower := strings.ToLower(a)
		if lower == "--subscription" {
			return args
		}
		if strings.HasPrefix(lower, "--subscription=") {
			return args
		}
	}
	return append(append([]string{}, args...), "--subscription", subscriptionID)
}

func ensureAzJSONOutput(args []string, produces map[string]string) []string {
	if len(produces) == 0 {
		return args
	}
	if hasAzJSONOutput(args) {
		return args
	}
	// Azure CLI supports -o/--output.
	return append(append([]string{}, args...), "--output", "json")
}

func hasAzJSONOutput(args []string) bool {
	for i := 0; i < len(args); i++ {
		a := strings.TrimSpace(args[i])
		lower := strings.ToLower(a)
		if lower == "-o" || lower == "--output" {
			if i+1 < len(args) {
				v := strings.ToLower(strings.Trim(strings.TrimSpace(args[i+1]), "\"'"))
				return strings.Contains(v, "json")
			}
			continue
		}
		if strings.HasPrefix(lower, "--output=") {
			v := strings.ToLower(strings.Trim(strings.TrimSpace(strings.TrimPrefix(a, "--output=")), "\"'"))
			return strings.Contains(v, "json")
		}
		if strings.HasPrefix(lower, "-o=") {
			v := strings.ToLower(strings.Trim(strings.TrimSpace(strings.TrimPrefix(a, "-o=")), "\"'"))
			return strings.Contains(v, "json")
		}
	}
	return false
}

func formatAzArgsForLog(args []string) string {
	out := make([]string, 0, len(args)+1)
	out = append(out, "az")
	out = append(out, args...)
	return strings.Join(out, " ")
}

func runAzCommandStreaming(ctx context.Context, args []string, w io.Writer) (string, error) {
	bin, err := exec.LookPath("az")
	if err != nil {
		return "", fmt.Errorf("az not found in PATH: %w", err)
	}

	cmd := exec.CommandContext(ctx, bin, args...)
	var buf bytes.Buffer
	mw := io.MultiWriter(w, &buf)
	cmd.Stdout = mw
	cmd.Stderr = mw

	err = cmd.Run()
	out := buf.String()
	if err != nil {
		return out, err
	}
	return out, nil
}
