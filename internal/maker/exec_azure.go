package maker

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const clankerAzureHealthZipToken = "__CLANKER_AZURE_HEALTH_ZIP__"

const (
	clankerAzureZipHTTPTokenPrefix     = "__CLANKER_AZURE_ZIP_HTTP_B64__:"
	clankerAzureZipManifestTokenPrefix = "__CLANKER_AZURE_ZIP_MANIFEST_B64__:"
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
		args = normalizeAzureAzArgs(args)
		args = ensureAzJSONOutput(args, cmdSpec.Produces)
		args = ensureAzSubscription(args, subscriptionID)
		args = ensureAzOnlyShowErrors(args)

		var cleanup func()
		args, cleanup, zipErr := materializeAzureZipDeployIfNeeded(args)
		if zipErr != nil {
			return fmt.Errorf("command %d invalid zip deploy: %w", idx+1, zipErr)
		}
		if cleanup != nil {
			defer cleanup()
		}

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
		if runErr != nil && isAzZipDeployCommand(args) && isAzTransientDeployError(out) {
			for attempt := 1; attempt <= 4; attempt++ {
				backoff := time.Duration(2*attempt) * time.Second
				_, _ = fmt.Fprintf(opts.Writer, "[maker] zipdeploy transient failure; retrying in %s (attempt %d/4)\n", backoff.String(), attempt)
				select {
				case <-ctx.Done():
					return fmt.Errorf("az command %d cancelled: %w", idx+1, ctx.Err())
				case <-time.After(backoff):
				}
				out2, runErr2 := runAzCommandStreaming(ctx, args, opts.Writer)
				out = out2
				runErr = runErr2
				if runErr == nil {
					break
				}
				if !isAzTransientDeployError(out) {
					break
				}
			}
		}
		if runErr != nil {
			// If the subscription is valid but this resource provider isn't registered yet,
			// try registering it (bounded attempts; only when it fails).
			if subscriptionID != "" && isAzProviderRegistrationError(out) {
				attempted := make(map[string]bool)
				for attempt := 0; attempt < 2 && runErr != nil && isAzProviderRegistrationError(out); attempt++ {
					ns := chooseAzureProviderNamespace(out, args, attempted)
					if ns == "" {
						break
					}
					attempted[ns] = true
					_, _ = fmt.Fprintf(opts.Writer, "[maker] azure provider not registered: %s (attempting register)\n", ns)
					if regErr := ensureAzureProviderRegistered(ctx, subscriptionID, ns, 2*time.Minute, opts.Writer); regErr != nil {
						return fmt.Errorf("azure provider registration failed: %w", regErr)
					}

					out2, runErr2 := runAzCommandStreaming(ctx, args, opts.Writer)
					out = out2
					runErr = runErr2
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

	// Resource groups / ARM.
	if lower[0] == "group" || lower[0] == "resource" || lower[0] == "deployment" {
		return "Microsoft.Resources"
	}

	// App Service / Functions.
	if lower[0] == "functionapp" || lower[0] == "webapp" || lower[0] == "appservice" {
		return "Microsoft.Web"
	}

	// Static Web Apps.
	if lower[0] == "staticwebapp" {
		return "Microsoft.Web"
	}

	// AKS.
	if lower[0] == "aks" {
		return "Microsoft.ContainerService"
	}

	// Container Registry.
	if lower[0] == "acr" {
		return "Microsoft.ContainerRegistry"
	}

	// Container Instances.
	if lower[0] == "container" {
		return "Microsoft.ContainerInstance"
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

	// App Configuration.
	if lower[0] == "appconfig" {
		return "Microsoft.AppConfiguration"
	}

	// API Management.
	if lower[0] == "apim" {
		return "Microsoft.ApiManagement"
	}

	// Monitor / Insights / Log Analytics.
	if lower[0] == "monitor" {
		return "Microsoft.Insights"
	}

	// Networking.
	if lower[0] == "network" {
		return "Microsoft.Network"
	}

	// Front Door / CDN.
	if lower[0] == "cdn" || lower[0] == "frontdoor" {
		return "Microsoft.Cdn"
	}

	// Messaging.
	if lower[0] == "servicebus" {
		return "Microsoft.ServiceBus"
	}
	if lower[0] == "eventhubs" {
		return "Microsoft.EventHub"
	}
	if lower[0] == "eventgrid" {
		return "Microsoft.EventGrid"
	}

	// Databases / cache.
	if lower[0] == "sql" {
		return "Microsoft.Sql"
	}
	if lower[0] == "postgres" {
		return "Microsoft.DBforPostgreSQL"
	}
	if lower[0] == "mysql" {
		return "Microsoft.DBforMySQL"
	}
	if lower[0] == "redis" {
		return "Microsoft.Cache"
	}

	// Identity.
	if lower[0] == "identity" {
		return "Microsoft.ManagedIdentity"
	}

	// AI.
	if lower[0] == "cognitiveservices" {
		return "Microsoft.CognitiveServices"
	}

	// Realtime.
	if lower[0] == "signalr" {
		return "Microsoft.SignalRService"
	}

	return ""
}

var azMicrosoftNamespaceRegexp = regexp.MustCompile(`(?i)\bmicrosoft\.[a-z0-9]+\b`)

func azureNamespacesFromAzOutput(output string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, m := range azMicrosoftNamespaceRegexp.FindAllString(output, -1) {
		ns := strings.TrimSpace(m)
		if ns == "" {
			continue
		}
		ns = "Microsoft." + strings.TrimPrefix(strings.TrimPrefix(ns, "microsoft."), "Microsoft.")
		if seen[ns] {
			continue
		}
		seen[ns] = true
		out = append(out, ns)
	}
	return out
}

func chooseAzureProviderNamespace(output string, args []string, attempted map[string]bool) string {
	for _, ns := range azureNamespacesFromAzOutput(output) {
		if !attempted[ns] {
			return ns
		}
	}

	if ns := azureNamespaceForAzCommand(args); ns != "" && !attempted[ns] {
		return ns
	}

	return ""
}

func ensureAzureProviderRegistered(ctx context.Context, subscriptionID string, namespace string, timeout time.Duration, w io.Writer) error {
	if strings.TrimSpace(subscriptionID) == "" || strings.TrimSpace(namespace) == "" {
		return fmt.Errorf("missing subscription or namespace")
	}

	state, _ := runAzCommandCapture(ctx, []string{"provider", "show", "--namespace", namespace, "--subscription", subscriptionID, "--query", "registrationState", "-o", "tsv", "--only-show-errors"})
	if strings.EqualFold(strings.TrimSpace(state), "Registered") {
		return nil
	}

	if w != nil {
		_, _ = fmt.Fprintf(w, "[maker] registering Azure provider %s (this can take a minute)\n", namespace)
	}

	_, _ = runAzCommandCapture(ctx, []string{"provider", "register", "--namespace", namespace, "--subscription", subscriptionID, "--only-show-errors"})

	deadline := time.Now().Add(timeout)
	nextLog := time.Now()
	for time.Now().Before(deadline) {
		state, _ := runAzCommandCapture(ctx, []string{"provider", "show", "--namespace", namespace, "--subscription", subscriptionID, "--query", "registrationState", "-o", "tsv", "--only-show-errors"})
		st := strings.TrimSpace(state)
		if strings.EqualFold(st, "Registered") {
			if w != nil {
				_, _ = fmt.Fprintf(w, "[maker] Azure provider registered: %s\n", namespace)
			}
			return nil
		}
		if w != nil && time.Now().After(nextLog) {
			_, _ = fmt.Fprintf(w, "[maker] waiting for Azure provider %s registration (state=%s)\n", namespace, st)
			nextLog = time.Now().Add(10 * time.Second)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}

	return fmt.Errorf("Azure provider %s is still registering after %s; wait a bit and click Apply again", namespace, timeout.String())
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

func normalizeAzureAzArgs(args []string) []string {
	// Keep apply resilient against stale maker plans.
	// Example: Azure Functions no longer supports Node 18; auto-bump to 24.
	if len(args) < 3 {
		return args
	}

	lower0 := strings.ToLower(strings.TrimSpace(args[0]))
	lower1 := strings.ToLower(strings.TrimSpace(args[1]))
	if lower0 != "functionapp" || lower1 != "create" {
		return args
	}

	var runtimeIsNode bool
	for i := 0; i < len(args); i++ {
		if strings.EqualFold(strings.TrimSpace(args[i]), "--runtime") && i+1 < len(args) {
			v := strings.ToLower(strings.Trim(strings.TrimSpace(args[i+1]), "\"'"))
			if v == "node" {
				runtimeIsNode = true
			}
			break
		}
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(args[i])), "--runtime=") {
			v := strings.ToLower(strings.Trim(strings.TrimSpace(strings.TrimPrefix(args[i], "--runtime=")), "\"'"))
			if v == "node" {
				runtimeIsNode = true
			}
			break
		}
	}
	if !runtimeIsNode {
		return args
	}

	for i := 0; i < len(args); i++ {
		if strings.EqualFold(strings.TrimSpace(args[i]), "--runtime-version") {
			if i+1 < len(args) {
				v := strings.Trim(strings.TrimSpace(args[i+1]), "\"'")
				if v == "18" {
					out := append([]string{}, args...)
					out[i+1] = "24"
					return out
				}
			}
			return args
		}
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(args[i])), "--runtime-version=") {
			v := strings.Trim(strings.TrimSpace(strings.TrimPrefix(args[i], "--runtime-version=")), "\"'")
			if v == "18" {
				out := append([]string{}, args...)
				out[i] = "--runtime-version=24"
				return out
			}
			return args
		}
	}

	// No runtime version specified; leave as-is.
	return args
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

func isAzZipDeployCommand(args []string) bool {
	if len(args) < 5 {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(args[0]), "functionapp") &&
		strings.EqualFold(strings.TrimSpace(args[1]), "deployment") &&
		strings.EqualFold(strings.TrimSpace(args[2]), "source") &&
		strings.EqualFold(strings.TrimSpace(args[3]), "config-zip")
}

func isAzTransientDeployError(output string) bool {
	lower := strings.ToLower(output)
	if strings.Contains(lower, "service is unavailable") {
		return true
	}
	if strings.Contains(lower, "http/2 503") || strings.Contains(lower, " 503 ") {
		return true
	}
	if strings.Contains(lower, "operation returned an invalid status") {
		return true
	}
	if strings.Contains(lower, "jsondecodeerror") || strings.Contains(lower, "expecting value: line 1 column 1") {
		return true
	}
	return false
}

func materializeAzureZipDeployIfNeeded(args []string) ([]string, func(), error) {
	if !isAzZipDeployCommand(args) {
		return args, nil, nil
	}

	srcIdx := -1
	for i := 0; i < len(args); i++ {
		a := strings.TrimSpace(args[i])
		if strings.EqualFold(a, "--src") {
			srcIdx = i + 1
			break
		}
		if strings.HasPrefix(strings.ToLower(a), "--src=") {
			srcIdx = i
			break
		}
	}
	if srcIdx == -1 {
		return args, nil, nil
	}

	val := ""
	if srcIdx < len(args) {
		if strings.EqualFold(strings.TrimSpace(args[srcIdx-1]), "--src") {
			val = strings.TrimSpace(args[srcIdx])
		} else {
			val = strings.TrimSpace(strings.TrimPrefix(args[srcIdx], "--src="))
		}
	}
	if val == "" {
		return args, nil, nil
	}
	if val != clankerAzureHealthZipToken &&
		!strings.HasPrefix(val, clankerAzureZipHTTPTokenPrefix) &&
		!strings.HasPrefix(val, clankerAzureZipManifestTokenPrefix) {
		return args, nil, nil
	}
	zipBytes, err := createAzureZipDeployPayload(val)
	if err != nil {
		return nil, nil, err
	}

	f, err := os.CreateTemp("", "clanker-azure-zipdeploy-*.zip")
	if err != nil {
		return nil, nil, err
	}
	zipPath := f.Name()
	if _, err := f.Write(zipBytes); err != nil {
		_ = f.Close()
		_ = os.Remove(zipPath)
		return nil, nil, err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(zipPath)
		return nil, nil, err
	}

	out := append([]string{}, args...)
	if strings.EqualFold(strings.TrimSpace(args[srcIdx-1]), "--src") {
		out[srcIdx] = zipPath
	} else {
		out[srcIdx] = "--src=" + zipPath
	}
	cleanup := func() { _ = os.Remove(zipPath) }
	return out, cleanup, nil
}

type azureZipHTTPSpec struct {
	FunctionName string   `json:"functionName"`
	Route        string   `json:"route"`
	Methods      []string `json:"methods"`
	Status       int      `json:"status"`
	Body         string   `json:"body"`
	ContentType  string   `json:"contentType"`
}

type azureZipManifest struct {
	FilesB64 map[string]string `json:"filesB64"`
}

func createAzureZipDeployPayload(srcToken string) ([]byte, error) {
	// Back-compat: fixed /api/health endpoint token.
	if srcToken == clankerAzureHealthZipToken {
		spec := azureZipHTTPSpec{
			FunctionName: "health",
			Route:        "health",
			Methods:      []string{"get"},
			Status:       200,
			Body:         "ok",
			ContentType:  "text/plain",
		}
		return buildAzureHTTPZip(spec)
	}

	if strings.HasPrefix(srcToken, clankerAzureZipHTTPTokenPrefix) {
		b64 := strings.TrimPrefix(srcToken, clankerAzureZipHTTPTokenPrefix)
		payload, err := base64.RawURLEncoding.DecodeString(b64)
		if err != nil {
			return nil, fmt.Errorf("invalid azure zip http spec base64: %w", err)
		}
		var spec azureZipHTTPSpec
		if err := json.Unmarshal(payload, &spec); err != nil {
			return nil, fmt.Errorf("invalid azure zip http spec json: %w", err)
		}
		return buildAzureHTTPZip(spec)
	}

	if strings.HasPrefix(srcToken, clankerAzureZipManifestTokenPrefix) {
		b64 := strings.TrimPrefix(srcToken, clankerAzureZipManifestTokenPrefix)
		payload, err := base64.RawURLEncoding.DecodeString(b64)
		if err != nil {
			return nil, fmt.Errorf("invalid azure zip manifest base64: %w", err)
		}
		var manifest azureZipManifest
		if err := json.Unmarshal(payload, &manifest); err != nil {
			return nil, fmt.Errorf("invalid azure zip manifest json: %w", err)
		}
		return buildAzureManifestZip(manifest)
	}

	return nil, fmt.Errorf("unsupported azure zip deploy token: %q", srcToken)
}

func buildAzureHTTPZip(spec azureZipHTTPSpec) ([]byte, error) {
	name := strings.TrimSpace(spec.FunctionName)
	if name == "" {
		name = "handler"
	}
	name = strings.ReplaceAll(name, "/", "-")
	name = strings.ReplaceAll(name, "\\", "-")
	route := strings.TrimSpace(spec.Route)
	if route == "" {
		route = "health"
	}
	route = strings.TrimPrefix(route, "/")
	methods := spec.Methods
	if len(methods) == 0 {
		methods = []string{"get"}
	}
	status := spec.Status
	if status == 0 {
		status = 200
	}
	contentType := strings.TrimSpace(spec.ContentType)
	if contentType == "" {
		contentType = "text/plain"
	}

	functionJSONBytes, err := json.MarshalIndent(map[string]any{
		"bindings": []any{
			map[string]any{
				"authLevel": "anonymous",
				"type":      "httpTrigger",
				"direction": "in",
				"name":      "req",
				"methods":   methods,
				"route":     route,
			},
			map[string]any{
				"type":      "http",
				"direction": "out",
				"name":      "res",
			},
		},
	}, "", "  ")
	if err != nil {
		return nil, err
	}

	host := []byte("{\n  \"version\": \"2.0\",\n  \"extensionBundle\": {\n    \"id\": \"Microsoft.Azure.Functions.ExtensionBundle\",\n    \"version\": \"[4.*, 5.0.0)\"\n  }\n}\n")
	indexJS := []byte(fmt.Sprintf(
		"module.exports = async function (context, req) {\n  context.res = { status: %d, body: %s, headers: { 'content-type': %q } };\n};\n",
		status,
		strconvQuoteJSString(spec.Body),
		contentType,
	))

	files := map[string][]byte{
		"host.json": host,
		filepath.ToSlash(filepath.Join(name, "function.json")): functionJSONBytes,
		filepath.ToSlash(filepath.Join(name, "index.js")):      indexJS,
	}
	return buildZipFromFiles(files)
}

func buildAzureManifestZip(manifest azureZipManifest) ([]byte, error) {
	if len(manifest.FilesB64) == 0 {
		return nil, fmt.Errorf("azure zip manifest missing filesB64")
	}

	files := make(map[string][]byte, len(manifest.FilesB64))
	for rawName, rawContentB64 := range manifest.FilesB64 {
		name := strings.TrimSpace(rawName)
		if name == "" {
			return nil, fmt.Errorf("azure zip manifest contains empty filename")
		}
		clean := filepath.ToSlash(filepath.Clean(name))
		if strings.HasPrefix(clean, "../") || clean == ".." || strings.HasPrefix(clean, "/") {
			return nil, fmt.Errorf("azure zip manifest filename not allowed: %q", name)
		}
		content, err := base64.StdEncoding.DecodeString(strings.TrimSpace(rawContentB64))
		if err != nil {
			return nil, fmt.Errorf("azure zip manifest invalid base64 for %q: %w", name, err)
		}
		files[clean] = content
	}

	return buildZipFromFiles(files)
}

func buildZipFromFiles(files map[string][]byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			_ = zw.Close()
			return nil, err
		}
		if _, err := w.Write(content); err != nil {
			_ = zw.Close()
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func strconvQuoteJSString(s string) string {
	// Minimal, deterministic JS string quoting. We use JSON quoting which matches JS string literal rules.
	b, _ := json.Marshal(s)
	return string(b)
}
