package maker

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/bgdnvk/clanker/internal/oracle"
)

// ExecuteOraclePlan executes an Oracle Cloud Infrastructure plan through the OCI CLI.
func ExecuteOraclePlan(ctx context.Context, plan *Plan, opts ExecOptions) error {
	if plan == nil {
		return fmt.Errorf("nil plan")
	}
	if opts.Writer == nil {
		return fmt.Errorf("missing output writer")
	}

	client, err := oracle.NewClient(opts.OracleProfile, opts.OracleCompartmentID, opts.OracleTenancyOCID, opts.Debug)
	if err != nil {
		return err
	}

	bindings := make(map[string]string)
	if client.CompartmentID() != "" {
		bindings["COMPARTMENT_OCID"] = client.CompartmentID()
		bindings["OCI_COMPARTMENT_ID"] = client.CompartmentID()
	}
	if client.TenancyOCID() != "" {
		bindings["TENANCY_OCID"] = client.TenancyOCID()
		bindings["OCI_TENANCY_OCID"] = client.TenancyOCID()
	}

	for idx, cmdSpec := range plan.Commands {
		if err := validateOracleCommand(cmdSpec.Args, opts.Destroyer); err != nil {
			return fmt.Errorf("command %d rejected: %w", idx+1, err)
		}

		args := make([]string, 0, len(cmdSpec.Args))
		args = append(args, cmdSpec.Args...)
		args = applyPlanBindings(args, bindings)

		if hasUnresolvedPlaceholders(args) {
			return fmt.Errorf("command %d has unresolved placeholders after substitutions", idx+1)
		}
		if len(args) == 0 || !strings.EqualFold(strings.TrimSpace(args[0]), "oci") {
			return fmt.Errorf("command %d rejected: expected oci command", idx+1)
		}

		_, _ = fmt.Fprintf(opts.Writer, "[maker] running %d/%d: oci %s\n", idx+1, len(plan.Commands), strings.Join(args[1:], " "))
		out, runErr := runOCICommandStreaming(ctx, client, args[1:], opts.Writer)
		if runErr != nil {
			return fmt.Errorf("oracle command %d failed: %w", idx+1, runErr)
		}

		learnPlanBindingsFromProduces(cmdSpec.Produces, out, bindings)
	}

	return nil
}

func validateOracleCommand(args []string, allowDestructive bool) error {
	if len(args) == 0 {
		return fmt.Errorf("empty args")
	}
	first := strings.ToLower(strings.TrimSpace(args[0]))
	if first != "oci" {
		blockedCommands := []string{
			"aws", "gcloud", "az", "kubectl", "helm", "eksctl", "kubeadm",
			"python", "node", "npm", "npx",
			"bash", "sh", "zsh", "fish",
			"terraform", "tofu", "make",
			"wrangler", "cloudflared", "curl",
			"doctl", "hcloud",
		}
		for _, blocked := range blockedCommands {
			if first == blocked || strings.HasPrefix(first, blocked) {
				return fmt.Errorf("non-oci command is not allowed: %q", args[0])
			}
		}
		return fmt.Errorf("non-oci command is not allowed: %q", args[0])
	}

	for _, a := range args {
		lower := strings.ToLower(a)
		if strings.Contains(lower, ";") || strings.Contains(lower, "|") || strings.Contains(lower, "&&") || strings.Contains(lower, "||") {
			return fmt.Errorf("shell operators are not allowed")
		}
		if !allowDestructive {
			for _, verb := range []string{"delete", "remove", "terminate", "destroy"} {
				if strings.Contains(lower, verb) {
					return fmt.Errorf("destructive operation blocked (use --destroyer to allow): %s", a)
				}
			}
		}
	}
	return nil
}

func runOCICommandStreaming(ctx context.Context, client *oracle.Client, args []string, w io.Writer) (string, error) {
	out, err := client.RunOCI(ctx, args...)
	if strings.TrimSpace(out) != "" {
		_, _ = fmt.Fprint(w, out)
		if !strings.HasSuffix(out, "\n") {
			_, _ = fmt.Fprintln(w)
		}
	}
	return out, err
}
