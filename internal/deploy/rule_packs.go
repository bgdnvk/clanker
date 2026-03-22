package deploy

import (
	"strings"

	"github.com/bgdnvk/clanker/internal/maker"
)

type rulePackScope string

const (
	rulePackScopeProvider rulePackScope = "provider"
	rulePackScopeApp      rulePackScope = "app"
)

type RulePackContext struct {
	TargetProvider string
	PlanProvider   string
	Options        *DeployOptions
	Profile        *RepoProfile
	Deep           *DeepAnalysis
	Docker         *DockerAnalysis
	AppPorts       []int
}

func (c RulePackContext) normalizedTargetProvider() string {
	return strings.ToLower(strings.TrimSpace(c.TargetProvider))
}

func (c RulePackContext) normalizedPlanProvider() string {
	return strings.ToLower(strings.TrimSpace(c.PlanProvider))
}

func (c RulePackContext) effectivePlanProvider() string {
	if provider := c.normalizedPlanProvider(); provider != "" {
		return provider
	}
	if provider := c.normalizedTargetProvider(); provider != "" {
		return provider
	}
	return "aws"
}

func (c RulePackContext) requirementsProvider() string {
	if provider := c.normalizedTargetProvider(); provider != "" {
		return provider
	}
	return c.effectivePlanProvider()
}

type RulePack struct {
	Name                      string
	Scope                     rulePackScope
	Matches                   func(RulePackContext) bool
	ApplyArchitectureDefaults func(RulePackContext, *ArchitectDecision) bool
	AppendRequirements        func(RulePackContext, *strings.Builder) bool
	ApplyPlanAutofix          func(*maker.Plan, RulePackContext, func(string, ...any)) *maker.Plan
	ValidatePlan              func(*maker.Plan, RulePackContext) deterministicValidation
}

func validationFromPlanChecks(checks awsPlanChecks) deterministicValidation {
	return deterministicValidation{
		Issues:   checks.Issues,
		Fixes:    checks.Fixes,
		Warnings: checks.Warnings,
	}
}

func deployRulePacks() []RulePack {
	return []RulePack{
		{
			Name:  "aws",
			Scope: rulePackScopeProvider,
			Matches: func(ctx RulePackContext) bool {
				return ctx.effectivePlanProvider() == "aws"
			},
			ValidatePlan: func(plan *maker.Plan, ctx RulePackContext) deterministicValidation {
				if plan == nil {
					return deterministicValidation{}
				}
				return validationFromPlanChecks(validateAWSPlanCommands(plan, ctx.AppPorts, ctx.Deep))
			},
		},
		{
			Name:  "digitalocean",
			Scope: rulePackScopeProvider,
			Matches: func(ctx RulePackContext) bool {
				return ctx.effectivePlanProvider() == "digitalocean"
			},
			ApplyPlanAutofix: func(plan *maker.Plan, _ RulePackContext, logf func(string, ...any)) *maker.Plan {
				return ApplyDigitalOceanPlanAutofix(plan, logf)
			},
			ValidatePlan: func(plan *maker.Plan, ctx RulePackContext) deterministicValidation {
				if plan == nil {
					return deterministicValidation{}
				}
				return validationFromPlanChecks(validateDigitalOceanPlanCommands(plan, ctx.AppPorts, IsOpenClawRepo(ctx.Profile, ctx.Deep)))
			},
		},
		{
			Name:  "openclaw",
			Scope: rulePackScopeApp,
			Matches: func(ctx RulePackContext) bool {
				return IsOpenClawRepo(ctx.Profile, ctx.Deep)
			},
			ApplyArchitectureDefaults: func(ctx RulePackContext, arch *ArchitectDecision) bool {
				return ApplyOpenClawArchitectureDefaults(ctx.TargetProvider, ctx.Options, ctx.Profile, ctx.Deep, arch)
			},
			AppendRequirements: func(ctx RulePackContext, b *strings.Builder) bool {
				return AppendOpenClawDeploymentRequirements(b, ctx.Profile, ctx.Deep, ctx.requirementsProvider())
			},
			ApplyPlanAutofix: func(plan *maker.Plan, ctx RulePackContext, logf func(string, ...any)) *maker.Plan {
				return ApplyOpenClawPlanAutofix(plan, ctx.Profile, ctx.Deep, logf)
			},
			ValidatePlan: func(plan *maker.Plan, ctx RulePackContext) deterministicValidation {
				if plan == nil || ctx.effectivePlanProvider() != "aws" {
					return deterministicValidation{}
				}
				return validationFromPlanChecks(validateOpenClawPlanCommands(plan))
			},
		},
		{
			Name:  "wordpress",
			Scope: rulePackScopeApp,
			Matches: func(ctx RulePackContext) bool {
				return IsWordPressRepo(ctx.Profile, ctx.Deep)
			},
			ApplyArchitectureDefaults: func(ctx RulePackContext, arch *ArchitectDecision) bool {
				return ApplyWordPressArchitectureDefaults(ctx.TargetProvider, ctx.Options, ctx.Profile, ctx.Deep, arch)
			},
			AppendRequirements: func(ctx RulePackContext, b *strings.Builder) bool {
				return AppendWordPressDeploymentRequirements(b, ctx.Profile, ctx.Deep)
			},
		},
	}
}

func matchingRulePacks(ctx RulePackContext, scopes ...rulePackScope) []RulePack {
	allowed := make(map[rulePackScope]struct{}, len(scopes))
	for _, scope := range scopes {
		allowed[scope] = struct{}{}
	}
	packs := make([]RulePack, 0, len(allowed))
	for _, pack := range deployRulePacks() {
		if len(allowed) > 0 {
			if _, ok := allowed[pack.Scope]; !ok {
				continue
			}
		}
		if pack.Matches != nil && !pack.Matches(ctx) {
			continue
		}
		packs = append(packs, pack)
	}
	return packs
}

func ApplyRulePackArchitectureDefaults(ctx RulePackContext, arch *ArchitectDecision) bool {
	applied := false
	for _, pack := range matchingRulePacks(ctx, rulePackScopeApp) {
		if pack.ApplyArchitectureDefaults != nil && pack.ApplyArchitectureDefaults(ctx, arch) {
			applied = true
		}
	}
	return applied
}

func AppendRulePackDeploymentRequirements(b *strings.Builder, ctx RulePackContext) bool {
	appended := false
	for _, pack := range matchingRulePacks(ctx, rulePackScopeApp) {
		if pack.AppendRequirements != nil && pack.AppendRequirements(ctx, b) {
			appended = true
		}
	}
	return appended
}

func ApplyRulePackPlanAutofix(plan *maker.Plan, ctx RulePackContext, logf func(string, ...any)) *maker.Plan {
	patched := plan
	for _, pack := range matchingRulePacks(ctx, rulePackScopeApp, rulePackScopeProvider) {
		if pack.ApplyPlanAutofix == nil {
			continue
		}
		if next := pack.ApplyPlanAutofix(patched, ctx, logf); next != nil {
			patched = next
		}
	}
	return patched
}

func ApplyRulePackDeterministicValidation(plan *maker.Plan, ctx RulePackContext) deterministicValidation {
	var out deterministicValidation
	for _, pack := range matchingRulePacks(ctx, rulePackScopeProvider, rulePackScopeApp) {
		if pack.ValidatePlan == nil {
			continue
		}
		res := pack.ValidatePlan(plan, ctx)
		out.Issues = append(out.Issues, res.Issues...)
		out.Fixes = append(out.Fixes, res.Fixes...)
		out.Warnings = append(out.Warnings, res.Warnings...)
	}
	return out
}
