package deploy

import (
	"encoding/json"
	"strings"

	"github.com/bgdnvk/clanker/internal/maker"
)

func ApplyOpenClawPlanAutofix(plan *maker.Plan, profile *RepoProfile, deep *DeepAnalysis, logf func(string, ...any)) *maker.Plan {
	if plan == nil || len(plan.Commands) == 0 {
		return plan
	}
	if !IsOpenClawRepo(profile, deep) {
		return plan
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}

	removed := pruneOpenClawExactDuplicates(plan)
	if removed > 0 {
		logf("[deploy] openclaw autofix: removed %d exact duplicate command(s)", removed)
	}

	// Prune semantic SSM duplicates (multiple onboarding / start / health check variants).
	ssmRemoved := pruneOpenClawSemanticSSMDuplicates(plan)
	if ssmRemoved > 0 {
		logf("[deploy] openclaw autofix: removed %d redundant SSM command(s)", ssmRemoved)
	}

	hasCloudFrontCreate := false
	hasCloudFrontWait := false
	hasCloudFrontIDProduce := false
	hasCloudFrontDomainProduce := false
	hasHTTPSProduce := false
	cloudFrontCreateIdx := -1

	for i := range plan.Commands {
		cmd := &plan.Commands[i]
		if len(cmd.Args) >= 2 {
			svc := strings.ToLower(strings.TrimSpace(cmd.Args[0]))
			op := strings.ToLower(strings.TrimSpace(cmd.Args[1]))
			if svc == "cloudfront" && (op == "create-distribution" || op == "create-distribution-with-tags") {
				hasCloudFrontCreate = true
				if cloudFrontCreateIdx < 0 {
					cloudFrontCreateIdx = i
				}
			}
			if svc == "cloudfront" && op == "wait" && len(cmd.Args) >= 3 && strings.EqualFold(strings.TrimSpace(cmd.Args[2]), "distribution-deployed") {
				hasCloudFrontWait = true
			}
		}

		for k, v := range cmd.Produces {
			ku := strings.ToUpper(strings.TrimSpace(k))
			sv := strings.TrimSpace(v)
			svLower := strings.ToLower(sv)
			switch ku {
			case "CLOUDFRONT_ID", "CF_DISTRIBUTION_ID":
				hasCloudFrontIDProduce = true
			case "CLOUDFRONT_DOMAIN":
				hasCloudFrontDomainProduce = true
			case "HTTPS_URL":
				if strings.HasPrefix(svLower, "https://") {
					hasHTTPSProduce = true
				}
			}
		}
	}

	if hasCloudFrontCreate && cloudFrontCreateIdx >= 0 {
		cmd := &plan.Commands[cloudFrontCreateIdx]
		if cmd.Produces == nil {
			cmd.Produces = map[string]string{}
		}
		if !hasCloudFrontIDProduce {
			cmd.Produces["CLOUDFRONT_ID"] = "$.Distribution.Id"
			hasCloudFrontIDProduce = true
			logf("[deploy] openclaw autofix: added CLOUDFRONT_ID produce mapping")
		}
		if !hasCloudFrontDomainProduce {
			cmd.Produces["CLOUDFRONT_DOMAIN"] = "$.Distribution.DomainName"
			hasCloudFrontDomainProduce = true
			logf("[deploy] openclaw autofix: added CLOUDFRONT_DOMAIN produce mapping")
		}
		if !hasHTTPSProduce {
			cmd.Produces["HTTPS_URL"] = "https://<CLOUDFRONT_DOMAIN>"
			hasHTTPSProduce = true
			logf("[deploy] openclaw autofix: added HTTPS_URL produce mapping")
		}
	}

	if hasCloudFrontCreate && !hasCloudFrontWait && hasCloudFrontIDProduce {
		plan.Commands = append(plan.Commands, maker.Command{
			Args:   []string{"cloudfront", "wait", "distribution-deployed", "--id", "<CLOUDFRONT_ID>"},
			Reason: "Wait for CloudFront distribution deployment to complete before reporting pairing URL",
		})
		logf("[deploy] openclaw autofix: appended missing cloudfront wait distribution-deployed")
	}

	if hasCloudFrontCreate {
		return plan
	}

	logf("[deploy] openclaw autofix: skipped cloudfront patching because create-distribution step is missing")
	return plan
}

func pruneOpenClawExactDuplicates(plan *maker.Plan) int {
	if plan == nil || len(plan.Commands) == 0 {
		return 0
	}
	seen := make(map[string]struct{}, len(plan.Commands))
	filtered := make([]maker.Command, 0, len(plan.Commands))
	removed := 0
	for _, cmd := range plan.Commands {
		sig := openClawCommandSignature(cmd.Args)
		if sig == "" {
			filtered = append(filtered, cmd)
			continue
		}
		if _, ok := seen[sig]; ok {
			removed++
			continue
		}
		seen[sig] = struct{}{}
		filtered = append(filtered, cmd)
	}
	if removed > 0 {
		plan.Commands = filtered
	}
	return removed
}

func openClawCommandSignature(args []string) string {
	if len(args) == 0 {
		return ""
	}
	clean := make([]string, 0, len(args))
	for _, raw := range args {
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		clean = append(clean, v)
	}
	if len(clean) == 0 {
		return ""
	}
	return strings.Join(clean, "\x1f")
}

// pruneOpenClawSemanticSSMDuplicates collapses SSM send-command steps that
// repeat the same intent (onboarding, env-setup, gateway-start, diagnostics).
// For each intent category we keep only the LAST occurrence (the most refined
// version the LLM produced). Non-SSM commands and uncategorised SSM commands
// are never removed.
func pruneOpenClawSemanticSSMDuplicates(plan *maker.Plan) int {
	if plan == nil || len(plan.Commands) == 0 {
		return 0
	}

	type tagged struct {
		cmd      maker.Command
		idx      int
		category string // empty = keep unconditionally
	}

	items := make([]tagged, len(plan.Commands))
	for i, cmd := range plan.Commands {
		items[i] = tagged{cmd: cmd, idx: i, category: classifySSMIntent(cmd.Args)}
	}

	// Find the last index of each non-empty category.
	lastOfCategory := map[string]int{}
	for _, t := range items {
		if t.category != "" {
			lastOfCategory[t.category] = t.idx
		}
	}

	filtered := make([]maker.Command, 0, len(plan.Commands))
	removed := 0
	for _, t := range items {
		if t.category == "" {
			filtered = append(filtered, t.cmd)
			continue
		}
		// Keep only the last of each category.
		if t.idx == lastOfCategory[t.category] {
			filtered = append(filtered, t.cmd)
		} else {
			removed++
		}
	}
	if removed > 0 {
		plan.Commands = filtered
	}
	return removed
}

// classifySSMIntent returns a semantic category for SSM send-command steps.
// Returns "" for non-SSM commands or unrecognised SSM commands.
func classifySSMIntent(args []string) string {
	if len(args) < 4 {
		return ""
	}
	svc := strings.ToLower(strings.TrimSpace(args[0]))
	op := strings.ToLower(strings.TrimSpace(args[1]))
	if svc != "ssm" || op != "send-command" {
		return ""
	}

	// Grab the --parameters value and flatten commands array.
	script := extractSSMScriptFromArgs(args)
	if script == "" {
		return ""
	}
	l := strings.ToLower(script)

	// Classify by dominant intent.
	hasOnboard := strings.Contains(l, "docker-setup.sh") || strings.Contains(l, "openclaw-cli onboard") || strings.Contains(l, "openclaw-cli\" onboard")
	hasStart := strings.Contains(l, "docker compose up") || strings.Contains(l, "docker-compose up") || (strings.Contains(l, "docker run") && strings.Contains(l, "openclaw"))
	hasStop := (strings.Contains(l, "docker compose down") || strings.Contains(l, "docker compose stop") || strings.Contains(l, "docker-compose down")) && !hasStart
	hasEnvWrite := strings.Contains(l, "> /opt/openclaw/.env") || strings.Contains(l, ">> /opt/openclaw/.env") || strings.Contains(l, "> .env") || strings.Contains(l, "cat > /opt/openclaw/.env")
	hasECRPull := strings.Contains(l, "ecr get-login-password") || (strings.Contains(l, "docker pull") && strings.Contains(l, ".dkr.ecr."))
	hasDiag := strings.Contains(l, "docker logs") || strings.Contains(l, "docker ps") || strings.Contains(l, "curl -s") || strings.Contains(l, "health")
	hasClone := strings.Contains(l, "git clone")
	hasConfigOrigins := strings.Contains(l, "openclaw.json") && strings.Contains(l, "allowedorigins")
	hasListInvocations := strings.Contains(l, "list-command-invocations")

	// Priority order: a command may match multiple; pick the most specific.
	switch {
	case hasOnboard && !hasStart:
		return "ssm-onboard"
	case hasStart && !hasOnboard:
		return "ssm-gateway-start"
	case hasOnboard && hasStart:
		return "ssm-onboard-and-start"
	case hasStop:
		return "ssm-compose-stop"
	case hasConfigOrigins && !hasStart && !hasOnboard:
		return "ssm-config-origins"
	case hasEnvWrite && !hasStart && !hasOnboard:
		return "ssm-env-setup"
	case hasECRPull && !hasStart:
		return "ssm-ecr-pull"
	case hasClone && !hasStart && !hasOnboard:
		return "ssm-clone"
	case hasListInvocations:
		return "ssm-list-invocations"
	case hasDiag && !hasStart && !hasOnboard && !hasEnvWrite:
		return "ssm-diagnostics"
	}
	return ""
}

// extractSSMScriptFromArgs extracts the flattened shell script from
// ssm send-command --parameters. LLM output is non-deterministic so we
// handle every observed format:
//
//	{"commands":["cmd1","cmd2"]}           JSON object
//	commands=["cmd1","cmd2"]               SSM shorthand
//	commands = ["cmd1","cmd2"]             shorthand with spaces
//	["cmd1","cmd2"]                        bare JSON array
//	'{"commands":["cmd1"]}'                outer single-quotes
//	commands=['cmd1','cmd2']               single-quoted array items
//	cmd1                                   bare string (single command)
func extractSSMScriptFromArgs(args []string) string {
	for i := 0; i < len(args); i++ {
		a := strings.TrimSpace(args[i])
		var params string
		if a == "--parameters" && i+1 < len(args) {
			params = strings.TrimSpace(args[i+1])
		} else if strings.HasPrefix(a, "--parameters=") {
			params = strings.TrimSpace(strings.TrimPrefix(a, "--parameters="))
		} else if strings.HasPrefix(a, "--parameters") && strings.Contains(a, "=") {
			params = strings.TrimSpace(a[strings.Index(a, "=")+1:])
		} else {
			continue
		}
		// Strip outer single/double quotes the LLM sometimes wraps around the whole value
		params = strings.TrimSpace(params)
		if len(params) >= 2 {
			if (params[0] == '\'' && params[len(params)-1] == '\'') ||
				(params[0] == '"' && params[len(params)-1] == '"') {
				inner := params[1 : len(params)-1]
				// Only strip if inner looks like valid content
				if strings.Contains(inner, "commands") || strings.HasPrefix(strings.TrimSpace(inner), "[") || strings.HasPrefix(strings.TrimSpace(inner), "{") {
					params = inner
				}
			}
		}

		if cmds := tryExtractCommands(params); len(cmds) > 0 {
			return strings.Join(cmds, "\n")
		}
		return ""
	}
	return ""
}

// tryExtractCommands attempts multiple parsing strategies to extract the
// commands array from an SSM --parameters value.
func tryExtractCommands(params string) []string {
	params = strings.TrimSpace(params)
	if params == "" {
		return nil
	}

	// 1) JSON object: {"commands":["cmd1","cmd2"]}
	var obj struct {
		Commands []string `json:"commands"`
	}
	if json.Unmarshal([]byte(params), &obj) == nil && len(obj.Commands) > 0 {
		return obj.Commands
	}

	// 2) Bare JSON array: ["cmd1","cmd2"]
	var arr []string
	if json.Unmarshal([]byte(params), &arr) == nil && len(arr) > 0 {
		return arr
	}

	// 3) SSM shorthand: commands=["cmd1","cmd2"] or commands = [...]
	if idx := strings.Index(strings.ToLower(params), "commands"); idx >= 0 {
		rest := params[idx+len("commands"):]
		rest = strings.TrimLeft(rest, " \t")
		if len(rest) > 0 && rest[0] == '=' {
			rest = strings.TrimSpace(rest[1:])
			// Try JSON array parse
			var cmds []string
			if json.Unmarshal([]byte(rest), &cmds) == nil && len(cmds) > 0 {
				return cmds
			}
			// Single-quoted items: ['cmd1','cmd2'] → replace ' with " and retry
			if strings.HasPrefix(rest, "[") {
				normalized := singleToDoubleQuotes(rest)
				if json.Unmarshal([]byte(normalized), &cmds) == nil && len(cmds) > 0 {
					return cmds
				}
			}
			// Bare unquoted single value: commands=echo hello
			if !strings.HasPrefix(rest, "[") && !strings.HasPrefix(rest, "{") && rest != "" {
				return []string{rest}
			}
		}
	}

	// 4) Single-quoted JSON object: try replacing single quotes
	if strings.Contains(params, "'") {
		normalized := singleToDoubleQuotes(params)
		if json.Unmarshal([]byte(normalized), &obj) == nil && len(obj.Commands) > 0 {
			return obj.Commands
		}
	}

	// 5) Bare string: treat entire params as a single command (last resort)
	if !strings.HasPrefix(params, "{") && !strings.HasPrefix(params, "[") && !strings.HasPrefix(strings.ToLower(params), "commands") {
		return []string{params}
	}

	return nil
}

// singleToDoubleQuotes naively swaps outer single quotes to double quotes
// in a JSON-like string. Only swaps quotes that appear to delimit string
// values (after [, before ], around : etc.), not quotes inside shell commands.
func singleToDoubleQuotes(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inDouble := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch == '"' {
			inDouble = !inDouble
			b.WriteByte(ch)
		} else if ch == '\'' && !inDouble {
			b.WriteByte('"')
		} else {
			b.WriteByte(ch)
		}
	}
	return b.String()
}
