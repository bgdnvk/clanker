package deploy

import (
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
