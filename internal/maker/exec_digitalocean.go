package maker

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bgdnvk/clanker/internal/openclaw"
	"golang.org/x/crypto/ssh"
)

const (
	doOpenClawProxyBuildContext = "__CLANKER_OPENCLAW_DO_PROXY__"
	doAppSpecFilePrefix         = "clanker-do-app-spec-*.json"
	doOpenClawProxyPlatform     = "linux/amd64"
	doDOCRPushProbePrefix       = "clanker-docr-probe"
	doDOCRPushFallbackPrefix    = "clanker-docr-proxy"
	doPartialStateErrorMarker   = "partial-state:"
)

type doDeploySSHKeyMaterial struct {
	keyName        string
	privateKeyPath string
	publicKeyPath  string
	cleanup        func()
}

type doExecutionState struct {
	hasMutableState bool
	mutationPoints  []string
}

func (s *doExecutionState) noteMutation(args []string) {
	if !isDOMutatingCommandBoundary(args) {
		return
	}
	s.hasMutableState = true
	summary := summarizeDOMutation(args)
	if summary == "" {
		return
	}
	for _, existing := range s.mutationPoints {
		if existing == summary {
			return
		}
	}
	s.mutationPoints = append(s.mutationPoints, summary)
	if len(s.mutationPoints) > 8 {
		s.mutationPoints = s.mutationPoints[len(s.mutationPoints)-8:]
	}
}

func (s *doExecutionState) summary() string {
	if s == nil || len(s.mutationPoints) == 0 {
		return "mutable DigitalOcean resources already exist"
	}
	return strings.Join(s.mutationPoints, ", ")
}

func PlanNeedsDigitalOceanRegistryPush(plan *Plan) bool {
	if plan == nil || !strings.EqualFold(strings.TrimSpace(plan.Provider), "digitalocean") {
		return false
	}
	for _, cmd := range plan.Commands {
		args := cmd.Args
		if len(args) < 2 {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(args[0]), "docker") && strings.EqualFold(strings.TrimSpace(args[1]), "push") {
			return true
		}
	}
	return false
}

func DigitalOceanRegistryPushRepository(plan *Plan) string {
	if plan == nil || !strings.EqualFold(strings.TrimSpace(plan.Provider), "digitalocean") {
		return ""
	}
	for _, cmd := range plan.Commands {
		args := cmd.Args
		if len(args) < 3 || !strings.EqualFold(strings.TrimSpace(args[0]), "docker") || !strings.EqualFold(strings.TrimSpace(args[1]), "push") {
			continue
		}
		if repo := extractDOCRRepositoryFromImageRef(args[2]); repo != "" {
			return repo
		}
	}
	return ""
}

func ProbeDigitalOceanRegistryPushPrereq(ctx context.Context, apiToken string, repositoryName string, w io.Writer) error {
	apiToken = strings.TrimSpace(apiToken)
	if apiToken == "" {
		return fmt.Errorf("missing digitalocean API token")
	}
	repositoryName = strings.TrimSpace(repositoryName)
	if repositoryName == "" {
		generatedRepo, genErr := generateDOCRProbeRepositoryName()
		if genErr != nil {
			return fmt.Errorf("generate DOCR probe repository name: %w", genErr)
		}
		repositoryName = generatedRepo
	}
	registryName, err := lookupDORegistryName(ctx, apiToken)
	if err != nil {
		return err
	}
	if registryName == "" {
		if w != nil {
			_, _ = fmt.Fprintf(w, "[maker] DOCR prereq: no existing registry found; skipping local push probe until apply creates one\n")
		}
		return nil
	}
	return probeDigitalOceanRegistryPushPrereqForRegistry(ctx, apiToken, registryName, repositoryName, w)
}

func probeDigitalOceanRegistryPushPrereqForRegistry(ctx context.Context, apiToken string, registryName string, repositoryName string, w io.Writer) error {
	apiToken = strings.TrimSpace(apiToken)
	if apiToken == "" {
		return fmt.Errorf("missing digitalocean API token")
	}
	registryName = strings.TrimSpace(registryName)
	if registryName == "" {
		return fmt.Errorf("missing digitalocean registry name")
	}
	repositoryName = strings.TrimSpace(repositoryName)
	if repositoryName == "" {
		generatedRepo, genErr := generateDOCRProbeRepositoryName()
		if genErr != nil {
			return fmt.Errorf("generate DOCR probe repository name: %w", genErr)
		}
		repositoryName = generatedRepo
	}
	configDir, err := os.MkdirTemp("", "clanker-do-docr-prereq-*")
	if err != nil {
		return fmt.Errorf("create DOCR prereq Docker config dir: %w", err)
	}
	defer os.RemoveAll(configDir)
	opts := ExecOptions{
		DigitalOceanAPIToken:        apiToken,
		DigitalOceanDockerConfigDir: configDir,
		Writer:                      w,
	}
	bindings := map[string]string{"REGISTRY_NAME": registryName}
	if w != nil {
		_, _ = fmt.Fprintf(w, "[maker] DOCR prereq: probing push access against registry %s repository %s\n", registryName, repositoryName)
	}
	if err := loginDORegistryWithDockerConfig(ctx, bindings, opts, w); err != nil {
		return fmt.Errorf("prepare DOCR push auth for registry %s: %w", registryName, err)
	}
	probeRef := fmt.Sprintf("registry.digitalocean.com/%s/%s:latest", registryName, repositoryName)
	if err := buildLocalDOCRProbeImage(ctx, probeRef, opts, w); err != nil {
		return fmt.Errorf("build DOCR prereq image: %w", err)
	}
	if out, err := runDockerCommandStreaming(ctx, []string{"docker", "push", probeRef}, opts, "", w); err != nil {
		if isDOCRPushAuthFailure([]string{"docker", "push", probeRef}, out) {
			return fmt.Errorf("DOCR push probe failed for registry %s repository %s: push credentials were accepted for API access but rejected for image upload", registryName, repositoryName)
		}
		return fmt.Errorf("DOCR push probe failed for registry %s: %w", registryName, err)
	}
	if w != nil {
		_, _ = fmt.Fprintf(w, "[maker] DOCR prereq: push probe succeeded for registry %s repository %s\n", registryName, repositoryName)
	}
	return nil
}

func PrepareDigitalOceanRegistryPushPlan(ctx context.Context, apiToken string, plan *Plan, w io.Writer) error {
	if !PlanNeedsDigitalOceanRegistryPush(plan) {
		return nil
	}
	if planCreatesDigitalOceanRegistry(plan) {
		if w != nil {
			_, _ = fmt.Fprintf(w, "[maker] DOCR prereq: skipping pre-apply push probe because this plan creates its registry during apply\n")
		}
		return nil
	}
	targetRepo := DigitalOceanRegistryPushRepository(plan)
	if err := ProbeDigitalOceanRegistryPushPrereq(ctx, apiToken, targetRepo, w); err == nil {
		return nil
	} else if !strings.Contains(strings.ToLower(err.Error()), "rejected for image upload") || strings.TrimSpace(targetRepo) == "" {
		return err
	} else {
		selectedRepo, prepErr := prepareAdaptiveDigitalOceanRepository(ctx, apiToken, "", targetRepo, w)
		if prepErr != nil {
			return fmt.Errorf("exact DOCR repo %s was rejected and no adaptive repository succeeded: %w", targetRepo, prepErr)
		}
		if !rewriteDigitalOceanRegistryPushRepository(plan, selectedRepo) {
			return fmt.Errorf("exact DOCR repo %s was rejected, adaptive repo %s succeeded, but the plan could not be rewritten", targetRepo, selectedRepo)
		}
		if w != nil {
			_, _ = fmt.Fprintf(w, "[maker] DOCR prereq: rewrote plan to use adaptive repository %s\n", selectedRepo)
		}
		return nil
	}
}

func prepareDigitalOceanRegistryPushPlanForExistingRegistry(ctx context.Context, apiToken string, registryName string, plan *Plan, w io.Writer) error {
	if !PlanNeedsDigitalOceanRegistryPush(plan) {
		return nil
	}
	if w != nil {
		_, _ = fmt.Fprintf(w, "[maker] DOCR prereq: DigitalOcean allows one registry per account/team; reusing existing registry %s for this deploy\n", registryName)
	}
	targetRepo := DigitalOceanRegistryPushRepository(plan)
	if err := probeDigitalOceanRegistryPushPrereqForRegistry(ctx, apiToken, registryName, targetRepo, w); err == nil {
		return nil
	} else if !strings.Contains(strings.ToLower(err.Error()), "rejected for image upload") || strings.TrimSpace(targetRepo) == "" {
		return err
	} else {
		selectedRepo, prepErr := prepareAdaptiveDigitalOceanRepository(ctx, apiToken, registryName, targetRepo, w)
		if prepErr != nil {
			return fmt.Errorf("exact DOCR repo %s was rejected and no adaptive repository succeeded in registry %s: %w", targetRepo, registryName, prepErr)
		}
		if !rewriteDigitalOceanRegistryPushRepository(plan, selectedRepo) {
			return fmt.Errorf("exact DOCR repo %s was rejected in existing registry %s, adaptive repo %s succeeded, but the plan could not be rewritten", targetRepo, registryName, selectedRepo)
		}
		if w != nil {
			_, _ = fmt.Fprintf(w, "[maker] DOCR prereq: rewrote plan to use adaptive repository %s in existing registry %s\n", selectedRepo, registryName)
		}
		return nil
	}
}

func planCreatesDigitalOceanRegistry(plan *Plan) bool {
	if plan == nil || !strings.EqualFold(strings.TrimSpace(plan.Provider), "digitalocean") {
		return false
	}
	for _, cmd := range plan.Commands {
		if isDORegistryCreate(cmd.Args) {
			return true
		}
	}
	return false
}

func ValidateDigitalOceanAccess(ctx context.Context, apiToken string, w io.Writer) error {
	apiToken = strings.TrimSpace(apiToken)
	if apiToken == "" {
		return fmt.Errorf("digitalocean API token is required")
	}
	if _, err := exec.LookPath("doctl"); err != nil {
		return fmt.Errorf("doctl is required for DigitalOcean deployment but was not found in PATH")
	}
	if w != nil {
		_, _ = fmt.Fprintln(w, "[deploy] prereq: checking DigitalOcean CLI access with the provided token...")
	}
	opts := ExecOptions{DigitalOceanAPIToken: apiToken, Writer: io.Discard}
	out, err := runDoctlCommandWithRetry(ctx, []string{"account", "get", "--output", "json"}, opts, io.Discard)
	if err != nil {
		return fmt.Errorf("digitalocean token check failed: %w", err)
	}
	if w != nil {
		var obj any
		if jsonErr := json.Unmarshal([]byte(strings.TrimSpace(out)), &obj); jsonErr == nil {
			accountEmail, _ := jsonPathString(obj, "$.email")
			accountUUID, _ := jsonPathString(obj, "$.uuid")
			switch {
			case strings.TrimSpace(accountEmail) != "":
				_, _ = fmt.Fprintf(w, "[deploy] prereq: DigitalOcean token OK (account: %s)\n", accountEmail)
			case strings.TrimSpace(accountUUID) != "":
				_, _ = fmt.Fprintf(w, "[deploy] prereq: DigitalOcean token OK (account uuid: %s)\n", accountUUID)
			default:
				_, _ = fmt.Fprintln(w, "[deploy] prereq: DigitalOcean token OK")
			}
		} else {
			_, _ = fmt.Fprintln(w, "[deploy] prereq: DigitalOcean token OK")
		}
		registryName, regErr := lookupDORegistryName(ctx, apiToken)
		switch {
		case regErr != nil:
			_, _ = fmt.Fprintf(w, "[deploy] prereq: DigitalOcean registry lookup warning: %v\n", regErr)
		case strings.TrimSpace(registryName) == "":
			_, _ = fmt.Fprintln(w, "[deploy] prereq: no existing DigitalOcean registry visible to this token")
		default:
			_, _ = fmt.Fprintf(w, "[deploy] prereq: existing DigitalOcean registry visible to this token: %s\n", registryName)
		}
	}
	return nil
}

// ExecuteDigitalOceanPlan executes a Digital Ocean infrastructure plan
func ExecuteDigitalOceanPlan(ctx context.Context, plan *Plan, opts ExecOptions) error {
	if plan == nil {
		return fmt.Errorf("nil plan")
	}
	if opts.Writer == nil {
		return fmt.Errorf("missing output writer")
	}
	if opts.DigitalOceanAPIToken == "" {
		return fmt.Errorf("missing digitalocean API token")
	}
	if planNeedsDODockerAuthIsolation(plan) {
		dockerConfigDir, err := os.MkdirTemp("", "clanker-do-docker-config-*")
		if err != nil {
			return fmt.Errorf("create DigitalOcean Docker config dir: %w", err)
		}
		defer os.RemoveAll(dockerConfigDir)
		opts.DigitalOceanDockerConfigDir = dockerConfigDir
		fmt.Fprintf(opts.Writer, "[maker] using isolated Docker auth config for DigitalOcean registry operations: %s\n", dockerConfigDir)
	}

	// Clone the repo if any step is a docker build — docker needs a build context
	var cloneDir string
	if planHasDockerBuild(plan) {
		repoURL := extractRepoURLFromQuestion(plan.Question)
		if repoURL != "" {
			path, cleanup, err := cloneRepoForImageBuild(ctx, repoURL)
			if err != nil {
				return fmt.Errorf("clone for docker build: %w", err)
			}
			defer cleanup()
			cloneDir = path
			fmt.Fprintf(opts.Writer, "[maker] cloned %s for docker build context\n", repoURL)
		}
	}
	isOpenClaw := openclaw.Detect(strings.TrimSpace(plan.Question), extractRepoURLFromQuestion(plan.Question))

	bindings := make(map[string]string)
	if strings.TrimSpace(plan.Question) != "" {
		bindings["PLAN_QUESTION"] = strings.TrimSpace(plan.Question)
	}
	sshPrivateKeyPath := ""
	var sshKeyMaterial doDeploySSHKeyMaterial
	sshKeyCleanupDeferred := false
	registryCreatedThisRun := false
	skippedDockerPushRefs := map[string]struct{}{}
	openClawGatewayWaitDone := false
	execState := &doExecutionState{}

	// Import secret-like env vars into bindings so user-data placeholder substitution works.
	// Mirrors AWS executor: clanker-cloud passes user-provided env vars to the CLI process.
	importSecretLikeEnvVarsIntoBindings(bindings)

	// DIGITALOCEAN_ACCESS_TOKEN is needed inside user-data for doctl auth/docker login
	if _, ok := bindings["DIGITALOCEAN_ACCESS_TOKEN"]; !ok {
		bindings["DIGITALOCEAN_ACCESS_TOKEN"] = opts.DigitalOceanAPIToken
	}

	for idx, cmdSpec := range plan.Commands {
		if err := validateDoctlCommand(cmdSpec.Args, opts.Destroyer); err != nil {
			return fmt.Errorf("command %d rejected: %w", idx+1, err)
		}

		args := make([]string, 0, len(cmdSpec.Args)+4)
		args = append(args, cmdSpec.Args...)
		if len(args) > 0 && strings.EqualFold(strings.TrimSpace(args[0]), "doctl") {
			args = args[1:]
		}
		args = applyPlanBindings(args, bindings)
		args = injectOpenClawDOBynamicUserDataAtExec(args, bindings)
		args = expandTildeInArgs(args)
		if generatedArgs, err := ensureDOSSHImportKeyMaterial(args, &sshKeyMaterial, opts.Writer); err != nil {
			return fmt.Errorf("command %d rejected: %w", idx+1, err)
		} else {
			args = generatedArgs
			if sshKeyMaterial.cleanup != nil && !sshKeyCleanupDeferred {
				defer sshKeyMaterial.cleanup()
				sshKeyCleanupDeferred = true
			}
			if keyPath := detectDOSSHPrivateKeyPath(args); keyPath != "" {
				sshPrivateKeyPath = keyPath
				bindings["SSH_PRIVATE_KEY_FILE"] = keyPath
			}
		}
		if generatedArgs, cleanup, err := materializeDOAppSpecArg(args, opts.Writer); err != nil {
			return fmt.Errorf("command %d rejected: %w", idx+1, err)
		} else {
			args = generatedArgs
			if cleanup != nil {
				defer cleanup()
			}
		}
		args = normalizeDOFirewallCreateNameAtExec(args)
		args = normalizeFirewallRuleFlagsAtExec(args)
		args = stripInvalidICMPPortsAtExec(args)
		args = fixFirewallEmptyAddressAtExec(args)
		args = ensureDODropletCreateWaitAtExec(args)
		if resolvedArgs, err := normalizeDODropletVPCUUIDAtExec(ctx, args, opts, opts.Writer); err != nil {
			return fmt.Errorf("command %d rejected: %w", idx+1, err)
		} else {
			args = resolvedArgs
		}
		if err := validateDOFirewallRulesAtExec(args); err != nil {
			return fmt.Errorf("command %d rejected: %w", idx+1, err)
		}

		if err := validateDOUserDataAtExec(args); err != nil {
			return fmt.Errorf("command %d rejected: %w", idx+1, err)
		}

		if hasUnresolvedPlaceholders(args) {
			return fmt.Errorf("command %d has unresolved placeholders after substitutions", idx+1)
		}

		if len(args) >= 2 && strings.EqualFold(args[0], "git") && strings.EqualFold(args[1], "clone") {
			_, _ = fmt.Fprintf(opts.Writer, "[maker] skipping %d/%d: git clone (handled by executor)\n", idx+1, len(plan.Commands))
			continue
		}

		if isDockerCommand(args) {
			if isOpenClawDOProxyBuildCommand(args) {
				_, _ = fmt.Fprintf(opts.Writer, "[maker] running %d/%d: docker %s\n", idx+1, len(plan.Commands), strings.Join(dockerArgs(args), " "))
				imageRef := dockerBuildImageRef(args)
				out, runErr := runOpenClawDOProxyBuildAndPush(ctx, args, opts, opts.Writer)
				if runErr != nil && registryCreatedThisRun && outputLooksLikeDOCRPushAuthFailure(out) {
					out, runErr = retryOpenClawDOProxyBuildAndPushAfterFreshRegistryCreate(ctx, args, opts, bindings, opts.Writer)
				}
				if runErr != nil {
					if strings.Contains(out, "Cannot connect to the Docker daemon") || strings.Contains(out, "docker daemon running") {
						return fmt.Errorf("docker command %d failed (local-env: Docker Desktop not running): %w", idx+1, runErr)
					}
					if outputLooksLikeDOCRPushAuthFailure(out) {
						return wrapDOPartialStateError(ctx, execState, bindings, sshPrivateKeyPath, opts, fmt.Errorf("docker command %d failed (registry-auth: DOCR push authorization failed during buildx --push; verify the active DigitalOcean token/context has registry write access): %w", idx+1, runErr))
					}
					return wrapDOPartialStateError(ctx, execState, bindings, sshPrivateKeyPath, opts, fmt.Errorf("docker command %d failed: %w", idx+1, runErr))
				}
				if imageRef != "" {
					skippedDockerPushRefs[imageRef] = struct{}{}
				}
				learnDockerProducesLiteral(args, cmdSpec.Produces, bindings)
				learnPlanBindingsFromProduces(cmdSpec.Produces, out, bindings)
				computeDORuntimeBindings(bindings)
				continue
			}
			if imageRef := dockerPushImageRef(args); imageRef != "" {
				if _, ok := skippedDockerPushRefs[imageRef]; ok {
					_, _ = fmt.Fprintf(opts.Writer, "[maker] skipping %d/%d: docker push %s (already pushed during buildx step)\n", idx+1, len(plan.Commands), imageRef)
					continue
				}
			}
			_, _ = fmt.Fprintf(opts.Writer, "[maker] running %d/%d: docker %s\n", idx+1, len(plan.Commands), strings.Join(dockerArgs(args), " "))
			out, runErr := runDockerCommandStreaming(ctx, args, opts, cloneDir, opts.Writer)
			if runErr != nil && shouldRetryFreshRegistryPush(args, out, registryCreatedThisRun) {
				out, runErr = retryDOCRPushAfterFreshRegistryCreate(ctx, args, opts, cloneDir, bindings, opts.Writer)
			}
			if runErr != nil {
				if strings.Contains(out, "Cannot connect to the Docker daemon") || strings.Contains(out, "docker daemon running") {
					return fmt.Errorf("docker command %d failed (local-env: Docker Desktop not running): %w", idx+1, runErr)
				}
				if isDOCRPushAuthFailure(args, out) {
					return wrapDOPartialStateError(ctx, execState, bindings, sshPrivateKeyPath, opts, fmt.Errorf("docker command %d failed (registry-auth: DOCR push authorization failed; verify the active DigitalOcean token/context has registry write access, rerun doctl registry login, and clean up or reuse any partial DigitalOcean resources created earlier in the apply): %w", idx+1, runErr))
				}
				return wrapDOPartialStateError(ctx, execState, bindings, sshPrivateKeyPath, opts, fmt.Errorf("docker command %d failed: %w", idx+1, runErr))
			}
			learnDockerProducesLiteral(args, cmdSpec.Produces, bindings)
			learnPlanBindingsFromProduces(cmdSpec.Produces, out, bindings)
			computeDORuntimeBindings(bindings)
			continue
		}

		args = normalizeDoctlOutputFlags(args)
		if isOpenClaw && !openClawGatewayWaitDone && isDOAppsCreate(args) {
			if err := maybePrepareOpenClawDORuntimeOverSSH(ctx, bindings, sshPrivateKeyPath, opts); err != nil {
				return wrapDOPartialStateError(ctx, execState, bindings, sshPrivateKeyPath, opts, fmt.Errorf("digitalocean command %d pre-check failed (openclaw-bootstrap): %w", idx+1, err))
			}
			if err := waitForOpenClawDOGatewayReady(ctx, bindings, opts.Writer); err != nil {
				return wrapDOPartialStateError(ctx, execState, bindings, sshPrivateKeyPath, opts, fmt.Errorf("digitalocean command %d pre-check failed (openclaw-gateway): %w", idx+1, err))
			}
			openClawGatewayWaitDone = true
		}
		logDOResourceStrategy(args, opts.Writer)
		_, _ = fmt.Fprintf(opts.Writer, "[maker] running %d/%d: doctl %s\n", idx+1, len(plan.Commands), strings.Join(redactDOCommandArgsForLog(args), " "))
		if isDORegistryLogin(args) && strings.TrimSpace(opts.DigitalOceanDockerConfigDir) != "" {
			if err := loginDORegistryWithDockerConfig(ctx, bindings, opts, opts.Writer); err != nil {
				return wrapDOPartialStateError(ctx, execState, bindings, sshPrivateKeyPath, opts, fmt.Errorf("digitalocean command %d failed (registry-auth): %w", idx+1, err))
			}
			continue
		}

		out, runErr := runDoctlCommandWithRetry(ctx, args, opts, opts.Writer)
		if runErr != nil {
			failure := classifyDOFailure(args, out)
			_, _ = fmt.Fprintf(opts.Writer, "[maker] DO error: category=%s service=%s op=%s\n", failure.Category, failure.Service, failure.Op)

			// Ignorable errors (e.g. "already exists" on create)
			if shouldIgnoreDOFailure(args, failure) {
				_, _ = fmt.Fprintf(opts.Writer, "[maker] note: ignoring non-fatal DO error for command %d (%s)\n", idx+1, failure.Category)
				// Error output won't have useful data — recover bindings via GET
				recoverDOBindingsAfterSkip(ctx, args, cmdSpec.Produces, bindings, opts, opts.Writer)
				computeDORuntimeBindings(bindings)
				execState.noteMutation(args)
				if isDORegistryCreate(args) {
					if prepErr := prepareDigitalOceanRegistryPushPlanForExistingRegistry(ctx, opts.DigitalOceanAPIToken, bindings["REGISTRY_NAME"], plan, opts.Writer); prepErr != nil {
						_, _ = fmt.Fprintf(opts.Writer, "[maker] warning: DigitalOcean registry reuse preparation failed; continuing and deferring to the actual docker push: %v\n", prepErr)
					}
				}
				continue
			}

			return wrapDOPartialStateError(ctx, execState, bindings, sshPrivateKeyPath, opts, fmt.Errorf("digitalocean command %d failed (%s): %w", idx+1, failure.Category, runErr))
		}

		learnPlanBindingsFromProduces(cmdSpec.Produces, out, bindings)
		extractSSHKeyBindingsDirect(args, out, bindings)
		extractDODropletBindingsDirect(args, out, bindings)
		if isDOComputeDropletCreate(args) {
			recoverDODropletIPAfterCreate(ctx, bindings, opts, opts.Writer)
		}
		extractFirewallBindingsDirect(args, out, bindings)
		extractDOAppBindingsDirect(args, out, bindings)
		if isDOAppsCreate(args) {
			recoverDOAppBindingsAfterCreate(ctx, bindings, opts, opts.Writer)
		}
		computeDORuntimeBindings(bindings)
		execState.noteMutation(args)
		if isDORegistryCreate(args) {
			registryCreatedThisRun = true
			if opts.Writer != nil {
				_, _ = fmt.Fprintf(opts.Writer, "[maker] DOCR prereq: skipping immediate push probe after fresh registry creation; continuing to the actual image push step\n")
			}
		}
		if err := postCheckDOCommand(args, out); err != nil {
			return wrapDOPartialStateError(ctx, execState, bindings, sshPrivateKeyPath, opts, fmt.Errorf("digitalocean command %d post-check failed: %w", idx+1, err))
		}
	}

	if isOpenClaw {
		if err := ensureOpenClawDOHTTPSURL(ctx, bindings, opts); err != nil {
			return wrapDOPartialStateError(ctx, execState, bindings, sshPrivateKeyPath, opts, fmt.Errorf("openclaw post-deploy failed (https-url): %w", err))
		}
		if err := maybePatchOpenClawDOHTTPSOriginOverSSH(ctx, bindings, sshPrivateKeyPath, opts); err != nil {
			return wrapDOPartialStateError(ctx, execState, bindings, sshPrivateKeyPath, opts, fmt.Errorf("openclaw post-deploy failed (allowed-origins): %w", err))
		}
		if err := maybeEnforceOpenClawDOSecretOverSSH(ctx, bindings, sshPrivateKeyPath, opts); err != nil {
			return wrapDOPartialStateError(ctx, execState, bindings, sshPrivateKeyPath, opts, fmt.Errorf("openclaw post-deploy failed (gateway-secret): %w", err))
		}
	}
	if isOpenClaw {
		if err := maybeStartOpenClawDOPairApproveWindowOverSSH(ctx, bindings, sshPrivateKeyPath, opts); err != nil {
			_, _ = fmt.Fprintf(opts.Writer, "[openclaw] warning: failed to start DigitalOcean auto-pair approval window: %v\n", err)
		}
		openclaw.MaybePrintPostDeployInstructions(bindings, opts.Profile, opts.Region, opts.Writer, strings.TrimSpace(plan.Question), extractRepoURLFromQuestion(plan.Question))
	}

	return nil
}

func injectOpenClawDOBynamicUserDataAtExec(args []string, bindings map[string]string) []string {
	if !isDOComputeDropletCreate(args) {
		return args
	}
	userDataIdx := -1
	for i := 0; i < len(args)-1; i++ {
		if strings.TrimSpace(args[i]) == "--user-data" {
			userDataIdx = i + 1
			break
		}
	}
	if userDataIdx < 0 || userDataIdx >= len(args) {
		return args
	}
	script := args[userDataIdx]
	lower := strings.ToLower(script)
	if !strings.Contains(lower, "/opt/openclaw/.env") || !strings.Contains(lower, "/opt/openclaw/data/openclaw.json") {
		return args
	}
	updated := ensureOpenClawDOCoreEnvLines(script, bindings)
	updated = appendOpenClawDOEnvLines(updated, requestedOpenClawDOPassThroughEnvKeys(bindings), bindings)
	updated = ensureOpenClawDOAuthProfilesScript(updated, bindings)
	if updated == script {
		return args
	}
	newArgs := make([]string, len(args))
	copy(newArgs, args)
	newArgs[userDataIdx] = updated
	return newArgs
}

func requestedOpenClawDOPassThroughEnvKeys(bindings map[string]string) []string {
	keys := make([]string, 0, 16)
	seen := map[string]struct{}{}
	add := func(key string) {
		key = strings.ToUpper(strings.TrimSpace(key))
		if key == "" {
			return
		}
		if _, ok := seen[key]; ok {
			return
		}
		if !secretLikeEnvKeyRe.MatchString(key) || !strings.Contains(key, "_") {
			return
		}
		if strings.HasPrefix(key, "AWS_") || strings.HasPrefix(key, "GOOGLE_") || strings.HasPrefix(key, "GCP_") || strings.HasPrefix(key, "AZURE_") || strings.HasPrefix(key, "CLOUDFLARE_") || strings.HasPrefix(key, "DIGITALOCEAN_") || strings.HasPrefix(key, "CLANKER_") || strings.HasPrefix(key, "SSH_") {
			return
		}
		switch key {
		case "OPENCLAW_CONFIG_DIR", "OPENCLAW_WORKSPACE_DIR", "OPENCLAW_GATEWAY_PORT", "OPENCLAW_BRIDGE_PORT", "OPENCLAW_GATEWAY_BIND", "OPENCLAW_IMAGE", "OPENCLAW_GATEWAY_TOKEN", "OPENCLAW_GATEWAY_PASSWORD", "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY", "DISCORD_BOT_TOKEN", "TELEGRAM_BOT_TOKEN":
			return
		}
		if strings.TrimSpace(bindings[key]) == "" && strings.TrimSpace(bindings["ENV_"+key]) == "" {
			return
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	for _, raw := range splitEnvKeyManifest(os.Getenv("CLANKER_PASSTHROUGH_ENV_KEYS")) {
		add(raw)
	}
	for key := range bindings {
		if strings.HasPrefix(key, "ENV_") {
			add(strings.TrimPrefix(key, "ENV_"))
		}
	}
	sort.Strings(keys)
	return keys
}

func splitEnvKeyManifest(raw string) []string {
	return strings.FieldsFunc(raw, func(r rune) bool {
		switch r {
		case ',', '\n', '\r', '\t', ' ':
			return true
		default:
			return false
		}
	})
}

func ensureOpenClawDOCoreEnvLines(script string, bindings map[string]string) string {
	if !strings.Contains(script, "\nENVEOF") {
		return script
	}
	type envPair struct {
		key   string
		value string
	}
	pairs := []envPair{
		{key: "OPENCLAW_GATEWAY_TOKEN", value: strings.TrimSpace(bindings["OPENCLAW_GATEWAY_TOKEN"])},
		{key: "OPENCLAW_GATEWAY_PASSWORD", value: strings.TrimSpace(bindings["OPENCLAW_GATEWAY_PASSWORD"])},
		{key: "ANTHROPIC_API_KEY", value: strings.TrimSpace(bindings["ANTHROPIC_API_KEY"])},
		{key: "OPENAI_API_KEY", value: strings.TrimSpace(bindings["OPENAI_API_KEY"])},
		{key: "GEMINI_API_KEY", value: strings.TrimSpace(bindings["GEMINI_API_KEY"])},
		{key: "DISCORD_BOT_TOKEN", value: strings.TrimSpace(bindings["DISCORD_BOT_TOKEN"])},
		{key: "TELEGRAM_BOT_TOKEN", value: strings.TrimSpace(bindings["TELEGRAM_BOT_TOKEN"])},
	}
	updated := script
	for _, pair := range pairs {
		if pair.value == "" {
			continue
		}
		updated = upsertOpenClawDOEnvLine(updated, pair.key, pair.value)
	}
	return updated
}

func upsertOpenClawDOEnvLine(script string, key string, value string) string {
	key = strings.ToUpper(strings.TrimSpace(key))
	value = strings.TrimSpace(value)
	if key == "" || value == "" || !strings.Contains(script, "\nENVEOF") {
		return script
	}
	line := key + "=" + value
	pattern := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(key) + `=.*$`)
	if pattern.MatchString(script) {
		return pattern.ReplaceAllString(script, line)
	}
	return strings.Replace(script, "\nENVEOF", "\n"+line+"\nENVEOF", 1)
}

func appendOpenClawDOEnvLines(script string, extraKeys []string, bindings map[string]string) string {
	if len(extraKeys) == 0 || !strings.Contains(script, "\nENVEOF") {
		return script
	}
	insert := make([]string, 0, len(extraKeys))
	for _, key := range extraKeys {
		if strings.Contains(script, "\n"+key+"=") || strings.HasPrefix(script, key+"=") {
			continue
		}
		value := strings.TrimSpace(bindings[key])
		if value == "" {
			value = strings.TrimSpace(bindings["ENV_"+key])
		}
		if value == "" {
			continue
		}
		insert = append(insert, key+"="+value)
	}
	if len(insert) == 0 {
		return script
	}
	block := strings.Join(insert, "\n") + "\n"
	return strings.Replace(script, "\nENVEOF", "\n"+block+"ENVEOF", 1)
}

func ensureOpenClawDOAuthProfilesScript(script string, bindings map[string]string) string {
	if strings.Contains(script, "/opt/openclaw/data/agents/main/agent/auth-profiles.json") {
		if strings.Contains(script, "chmod 600 /opt/openclaw/data/openclaw.json") && !strings.Contains(script, "chmod 600 /opt/openclaw/data/agents/main/agent/auth-profiles.json") {
			return strings.Replace(script, "chmod 600 /opt/openclaw/data/openclaw.json", "chmod 600 /opt/openclaw/data/openclaw.json\nif [ -f /opt/openclaw/data/agents/main/agent/auth-profiles.json ]; then chmod 600 /opt/openclaw/data/agents/main/agent/auth-profiles.json; fi", 1)
		}
		return script
	}
	type providerProfile struct {
		provider string
		envKey   string
	}
	providers := make([]providerProfile, 0, 3)
	if strings.TrimSpace(bindings["ANTHROPIC_API_KEY"]) != "" {
		providers = append(providers, providerProfile{provider: "anthropic", envKey: "ANTHROPIC_API_KEY"})
	}
	if strings.TrimSpace(bindings["OPENAI_API_KEY"]) != "" {
		providers = append(providers, providerProfile{provider: "openai", envKey: "OPENAI_API_KEY"})
	}
	if strings.TrimSpace(bindings["GEMINI_API_KEY"]) != "" {
		providers = append(providers, providerProfile{provider: "gemini", envKey: "GEMINI_API_KEY"})
	}
	if len(providers) == 0 || !strings.Contains(script, "\nchown -R 1000:1000 /opt/openclaw/data /opt/openclaw/workspace") {
		return script
	}
	lines := []string{
		"cat > /opt/openclaw/data/agents/main/agent/auth-profiles.json << 'JSONEOF'",
		"{",
		"  \"version\": 1,",
		"  \"order\": {",
	}
	for i, provider := range providers {
		comma := ","
		if i == len(providers)-1 {
			comma = ""
		}
		lines = append(lines, fmt.Sprintf("    %q: [%q]%s", provider.provider, provider.provider+":default", comma))
	}
	lines = append(lines, "  },", "  \"profiles\": {")
	for i, provider := range providers {
		profileID := provider.provider + ":default"
		lines = append(lines,
			fmt.Sprintf("    %q: {", profileID),
			"      \"type\": \"api_key\",",
			fmt.Sprintf("      \"provider\": %q,", provider.provider),
			"      \"keyRef\": {",
			"        \"source\": \"env\",",
			"        \"provider\": \"default\",",
			fmt.Sprintf("        \"id\": %q", provider.envKey),
			"      }",
		)
		closing := "    }"
		if i < len(providers)-1 {
			closing += ","
		}
		lines = append(lines, closing)
	}
	lines = append(lines, "  }", "}", "JSONEOF", "")
	block := strings.Join(lines, "\n") + "\n"
	updated := strings.Replace(script, "\nchown -R 1000:1000 /opt/openclaw/data /opt/openclaw/workspace", "\n"+block+"chown -R 1000:1000 /opt/openclaw/data /opt/openclaw/workspace", 1)
	if updated != script && strings.Contains(updated, "chmod 600 /opt/openclaw/data/openclaw.json") && !strings.Contains(updated, "chmod 600 /opt/openclaw/data/agents/main/agent/auth-profiles.json") {
		updated = strings.Replace(updated, "chmod 600 /opt/openclaw/data/openclaw.json", "chmod 600 /opt/openclaw/data/openclaw.json\nif [ -f /opt/openclaw/data/agents/main/agent/auth-profiles.json ]; then chmod 600 /opt/openclaw/data/agents/main/agent/auth-profiles.json; fi", 1)
	}
	return updated
}

func isDOComputeDropletCreate(args []string) bool {
	if len(args) < 3 {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(args[0]), "compute") && strings.EqualFold(strings.TrimSpace(args[1]), "droplet") && strings.EqualFold(strings.TrimSpace(args[2]), "create")
}

func ensureDODropletCreateWaitAtExec(args []string) []string {
	if !isDOComputeDropletCreate(args) {
		return args
	}
	for _, arg := range args[3:] {
		if strings.EqualFold(strings.TrimSpace(arg), "--wait") {
			return args
		}
	}
	updated := make([]string, 0, len(args)+1)
	updated = append(updated, args...)
	updated = append(updated, "--wait")
	return updated
}

func logDOResourceStrategy(args []string, w io.Writer) {
	if w == nil || len(args) < 2 {
		return
	}
	joined := strings.ToLower(strings.Join(args, " "))
	switch {
	case strings.HasPrefix(joined, "compute ssh-key import"):
		_, _ = fmt.Fprintln(w, "[maker] strategy: create a fresh deployment-scoped SSH key for this deploy")
	case strings.HasPrefix(joined, "compute firewall create"):
		_, _ = fmt.Fprintln(w, "[maker] strategy: create a fresh firewall for this deploy")
	case strings.HasPrefix(joined, "compute droplet create"):
		_, _ = fmt.Fprintln(w, "[maker] strategy: create a fresh droplet for this deploy")
	case strings.HasPrefix(joined, "registry create"):
		_, _ = fmt.Fprintln(w, "[maker] strategy: try to create a fresh registry; reuse an existing registry only if DigitalOcean account constraints require it")
	case strings.HasPrefix(joined, "apps create"):
		_, _ = fmt.Fprintln(w, "[maker] strategy: create a fresh App Platform app for this deploy")
	}
}

func isDOMutatingCommandBoundary(args []string) bool {
	if len(args) < 2 {
		return false
	}
	joined := strings.ToLower(strings.TrimSpace(strings.Join(args, " ")))
	switch {
	case strings.HasPrefix(joined, "compute ssh-key import"):
		return true
	case strings.HasPrefix(joined, "compute firewall create"):
		return true
	case strings.HasPrefix(joined, "compute firewall update"):
		return true
	case strings.HasPrefix(joined, "compute firewall add-"):
		return true
	case strings.HasPrefix(joined, "compute droplet create"):
		return true
	case strings.HasPrefix(joined, "compute reserved-ip create"):
		return true
	case strings.HasPrefix(joined, "compute reserved-ip-action assign"):
		return true
	case strings.HasPrefix(joined, "registry create"):
		return true
	case strings.HasPrefix(joined, "apps create"):
		return true
	case strings.HasPrefix(joined, "apps update"):
		return true
	default:
		return false
	}
}

func summarizeDOMutation(args []string) string {
	if len(args) == 0 {
		return ""
	}
	redacted := redactDOCommandArgsForLog(args)
	limit := len(redacted)
	if limit > 4 {
		limit = 4
	}
	return strings.Join(redacted[:limit], " ")
}

func wrapDOPartialStateError(ctx context.Context, execState *doExecutionState, bindings map[string]string, sshPrivateKeyPath string, opts ExecOptions, cause error) error {
	if cause == nil {
		return nil
	}
	if execState == nil || !execState.hasMutableState {
		return cause
	}
	if opts.Writer != nil {
		_, _ = fmt.Fprintf(opts.Writer, "[maker] partial-state: DigitalOcean apply already mutated remote resources; automatic full-plan retry is unsafe\n")
		_, _ = fmt.Fprintf(opts.Writer, "[maker] partial-state: mutation boundary: %s\n", execState.summary())
		if snapshot := formatDOPartialStateBindings(bindings); snapshot != "" {
			_, _ = fmt.Fprintf(opts.Writer, "[maker] partial-state: bindings snapshot: %s\n", snapshot)
		}
		if diag := collectDOPartialStateDiagnostics(ctx, bindings, sshPrivateKeyPath); diag != "" {
			_, _ = io.WriteString(opts.Writer, diag)
			if !strings.HasSuffix(diag, "\n") {
				_, _ = io.WriteString(opts.Writer, "\n")
			}
		}
	}
	return fmt.Errorf("%s DigitalOcean resources already exist after successful mutable steps (%s); inspect or clean up partial state before retrying: %w", doPartialStateErrorMarker, execState.summary(), cause)
}

func formatDOPartialStateBindings(bindings map[string]string) string {
	if len(bindings) == 0 {
		return ""
	}
	keys := []string{"SSH_KEY_ID", "FIREWALL_ID", "DROPLET_ID", "DROPLET_IP", "REGISTRY_NAME", "APP_ID", "HTTPS_URL"}
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		if value := strings.TrimSpace(bindings[key]); value != "" {
			parts = append(parts, fmt.Sprintf("%s=%s", key, value))
		}
	}
	return strings.Join(parts, " ")
}

func collectDOPartialStateDiagnostics(ctx context.Context, bindings map[string]string, sshPrivateKeyPath string) string {
	dropletIP := strings.TrimSpace(bindings["DROPLET_IP"])
	if dropletIP == "" {
		return ""
	}
	if sshPrivateKeyPath == "" {
		sshPrivateKeyPath = strings.TrimSpace(bindings["SSH_PRIVATE_KEY_FILE"])
	}
	if sshPrivateKeyPath == "" {
		return "[maker] partial-state: droplet diagnostics skipped (missing SSH private key path)"
	}
	if _, err := os.Stat(sshPrivateKeyPath); err != nil {
		return fmt.Sprintf("[maker] partial-state: droplet diagnostics skipped (ssh key unavailable: %v)", err)
	}
	diagCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
	defer cancel()
	script := strings.Join([]string{
		"set +e",
		`echo "[maker] partial-state: collecting droplet bootstrap diagnostics"`,
		`echo "== cloud-init status =="`,
		`cloud-init status 2>/dev/null || true`,
		`echo "== recent user-data log =="`,
		`tail -n 120 /var/log/user-data.log 2>/dev/null || true`,
		`echo "== docker ps =="`,
		`docker ps --format '{{.Names}}\t{{.Status}}\t{{.Ports}}' 2>/dev/null || true`,
		`echo "== listening ports 18789/18790 =="`,
		`ss -ltnp 2>/dev/null | grep -E ':(18789|18790)\b' || true`,
		`if [ -d /opt/openclaw ]; then`,
		`  echo "== openclaw compose ps =="`,
		`  cd /opt/openclaw && docker compose ps 2>/dev/null || true`,
		`  echo "== openclaw gateway logs =="`,
		`  cd /opt/openclaw && docker compose logs --tail=120 openclaw-gateway 2>/dev/null || true`,
		`fi`,
	}, "\n")
	out, err := runDOSSHScript(diagCtx, dropletIP, sshPrivateKeyPath, script)
	if err != nil {
		trimmed := strings.TrimSpace(out)
		if trimmed == "" {
			return fmt.Sprintf("[maker] partial-state: droplet diagnostics failed: %v", err)
		}
		return fmt.Sprintf("[maker] partial-state: droplet diagnostics failed: %v\n%s", err, trimmed)
	}
	return strings.TrimSpace(out)
}

func buildLocalDOCRProbeImage(ctx context.Context, probeRef string, opts ExecOptions, w io.Writer) error {
	buildDir, err := os.MkdirTemp("", "clanker-do-docr-probe-build-*")
	if err != nil {
		return fmt.Errorf("create DOCR probe build dir: %w", err)
	}
	defer os.RemoveAll(buildDir)
	dockerfilePath := filepath.Join(buildDir, "Dockerfile")
	if err := os.WriteFile(dockerfilePath, []byte("FROM scratch\nCOPY probe.txt /probe.txt\n"), 0600); err != nil {
		return fmt.Errorf("write DOCR probe Dockerfile: %w", err)
	}
	if err := os.WriteFile(filepath.Join(buildDir, "probe.txt"), []byte("probe\n"), 0600); err != nil {
		return fmt.Errorf("write DOCR probe payload: %w", err)
	}
	if _, err := runDockerCommandStreaming(ctx, []string{"docker", "build", "-t", probeRef, buildDir}, opts, "", w); err != nil {
		return err
	}
	return nil
}

func isOpenClawDOProxyBuildCommand(args []string) bool {
	dArgs := dockerArgs(args)
	if len(dArgs) == 0 || !strings.EqualFold(strings.TrimSpace(dArgs[0]), "build") {
		return false
	}
	return strings.TrimSpace(dArgs[len(dArgs)-1]) == doOpenClawProxyBuildContext
}

func dockerBuildImageRef(args []string) string {
	dArgs := dockerArgs(args)
	if len(dArgs) == 0 || !strings.EqualFold(strings.TrimSpace(dArgs[0]), "build") {
		return ""
	}
	for i := 0; i < len(dArgs)-1; i++ {
		if strings.TrimSpace(dArgs[i]) == "-t" {
			return strings.TrimSpace(dArgs[i+1])
		}
	}
	return ""
}

func dockerPushImageRef(args []string) string {
	dArgs := dockerArgs(args)
	if len(dArgs) < 2 || !strings.EqualFold(strings.TrimSpace(dArgs[0]), "push") {
		return ""
	}
	return strings.TrimSpace(dArgs[1])
}

func runOpenClawDOProxyBuildAndPush(ctx context.Context, args []string, opts ExecOptions, w io.Writer) (string, error) {
	bin, err := exec.LookPath("docker")
	if err != nil {
		return "", fmt.Errorf("docker not found in PATH: %w", err)
	}
	dArgs := ensureDOProxyBuildPlatform(dockerArgs(args))
	ctxDir, cleanup, ok, err := prepareSpecialDockerBuildContext(dArgs, w)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("missing OpenClaw DigitalOcean proxy build context")
	}
	defer cleanup()
	rewritten := rewriteDockerBuildArgsForMaterializedContext(dArgs)
	requiredPlatforms := []string{doOpenClawProxyPlatform}
	needBuildx, buildxReason, buildxErr := dockerBuildNeedsBuildx(ctx, requiredPlatforms)
	if buildxErr != nil {
		return "", buildxErr
	}
	if needBuildx && !hasBuildxAvailableWithConfig(ctx, opts.DigitalOceanDockerConfigDir) {
		return "", fmt.Errorf("docker buildx is required for the OpenClaw proxy image because %s", buildxReason)
	}
	if !needBuildx {
		if w != nil {
			_, _ = fmt.Fprintln(w, "[maker] OpenClaw proxy target matches the local Docker platform; using plain docker build + push")
		}
		buildArgs := append([]string{"docker"}, rewritten...)
		buildOut, buildErr := runDockerCommandStreaming(ctx, buildArgs, opts, ctxDir, w)
		if buildErr != nil {
			return buildOut, buildErr
		}
		imageRef := dockerBuildImageRef(args)
		if imageRef == "" {
			return buildOut, fmt.Errorf("missing image ref for OpenClaw DigitalOcean proxy push")
		}
		pushOut, pushErr := runDockerCommandStreaming(ctx, []string{"docker", "push", imageRef}, opts, "", w)
		return buildOut + pushOut, pushErr
	}
	if err := ensureDockerBuildxReadyWithConfig(ctx, w, opts.DigitalOceanDockerConfigDir); err != nil {
		return "", err
	}
	buildxArgs := make([]string, 0, len(rewritten)+6)
	buildxArgs = append(buildxArgs, "buildx", "build", "--progress", "plain", "--provenance=false", "--sbom=false", "--push")
	buildxArgs = append(buildxArgs, rewritten[1:]...)
	cmd := exec.CommandContext(ctx, bin, buildxArgs...)
	cmd.Dir = ctxDir
	cmd.Env = dockerBuildxEnv(os.Environ(), opts.DigitalOceanDockerConfigDir)
	var buf bytes.Buffer
	mw := io.MultiWriter(w, &buf)
	cmd.Stdout = mw
	cmd.Stderr = mw
	err = cmd.Run()
	return buf.String(), err
}

// validateDoctlCommand validates a doctl or docker command
func validateDoctlCommand(args []string, allowDestructive bool) error {
	if len(args) == 0 {
		return fmt.Errorf("empty args")
	}

	first := strings.ToLower(strings.TrimSpace(args[0]))

	// Allow docker build/push alongside doctl
	if first == "docker" {
		if len(args) < 2 {
			return fmt.Errorf("docker command missing subcommand")
		}
		sub := strings.ToLower(strings.TrimSpace(args[1]))
		allowed := map[string]bool{"build": true, "push": true, "tag": true, "login": true}
		if !allowed[sub] {
			return fmt.Errorf("docker subcommand %q is not allowed (only build/push/tag/login)", sub)
		}
		return nil
	}

	// Allow git clone (executor skips it — cloneRepoForImageBuild handles cloning)
	if first == "git" {
		if len(args) < 2 || !strings.EqualFold(args[1], "clone") {
			return fmt.Errorf("only 'git clone' is allowed, got 'git %s'", strings.Join(args[1:], " "))
		}
		return nil
	}

	// Only allow doctl commands
	if first != "doctl" {
		blockedCommands := []string{
			"aws", "gcloud", "az", "kubectl", "helm", "eksctl", "kubeadm",
			"python", "node", "npm", "npx",
			"bash", "sh", "zsh", "fish",
			"terraform", "tofu", "make",
			"wrangler", "cloudflared", "curl",
		}

		for _, blocked := range blockedCommands {
			if first == blocked || strings.HasPrefix(first, blocked) {
				return fmt.Errorf("non-doctl command is not allowed: %q", args[0])
			}
		}

		// If it doesn't start with "doctl" but isn't a blocked command,
		// treat it as a doctl subcommand (normalize)
	}

	// Flags whose values are freeform script/content — exempt from shell operator checks
	scriptFlags := map[string]bool{"--user-data": true}

	// Check for shell operators (skip freeform content args)
	skipNext := false
	for _, a := range args {
		if skipNext {
			skipNext = false
			continue
		}
		if scriptFlags[strings.ToLower(strings.TrimSpace(a))] {
			skipNext = true // next arg is script content
			continue
		}
		lower := strings.ToLower(a)
		if strings.Contains(lower, ";") || strings.Contains(lower, "|") || strings.Contains(lower, "&&") || strings.Contains(lower, "||") {
			return fmt.Errorf("shell operators are not allowed")
		}

		if !allowDestructive {
			destructiveVerbs := []string{"delete", "remove", "destroy"}
			for _, verb := range destructiveVerbs {
				if strings.Contains(lower, verb) {
					return fmt.Errorf("destructive verbs are blocked (use --destroyer to allow)")
				}
			}
		}
	}

	return nil
}

// isDockerCommand returns true if args represent a docker CLI command
func isDockerCommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	return strings.ToLower(strings.TrimSpace(args[0])) == "docker"
}

func isDOCRPushAuthFailure(args []string, output string) bool {
	if !isDockerCommand(args) {
		return false
	}
	dArgs := dockerArgs(args)
	if len(dArgs) == 0 || !strings.EqualFold(strings.TrimSpace(dArgs[0]), "push") {
		return false
	}
	return outputLooksLikeDOCRPushAuthFailure(output)
}

func outputLooksLikeDOCRPushAuthFailure(output string) bool {
	lower := strings.ToLower(output)
	return strings.Contains(lower, "insufficient_scope") ||
		strings.Contains(lower, "failed to authorize") ||
		strings.Contains(lower, "failed to fetch oauth token") ||
		strings.Contains(lower, "authorization failed") ||
		strings.Contains(lower, "403 forbidden") ||
		strings.Contains(lower, "requested access to the resource is denied") ||
		strings.Contains(lower, "unauthorized")
}

func shouldRetryFreshRegistryPush(args []string, output string, registryCreatedThisRun bool) bool {
	return registryCreatedThisRun && isDOCRPushAuthFailure(args, output)
}

func isDORegistryCreate(args []string) bool {
	if len(args) < 2 {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(args[0]), "registry") && strings.EqualFold(strings.TrimSpace(args[1]), "create")
}

func isDORegistryGet(args []string) bool {
	if len(args) < 2 {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(args[0]), "registry") && strings.EqualFold(strings.TrimSpace(args[1]), "get")
}

func isDORegistryLogin(args []string) bool {
	if len(args) < 2 {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(args[0]), "registry") && strings.EqualFold(strings.TrimSpace(args[1]), "login")
}

func isDOAppsCreate(args []string) bool {
	if len(args) < 2 {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(args[0]), "apps") && strings.EqualFold(strings.TrimSpace(args[1]), "create")
}

func waitForOpenClawDOGatewayReady(ctx context.Context, bindings map[string]string, w io.Writer) error {
	dropletIP := strings.TrimSpace(bindings["DROPLET_IP"])
	if dropletIP == "" {
		return fmt.Errorf("missing DROPLET_IP before App Platform create")
	}
	port := openclaw.DefaultPort
	if rawPort := strings.TrimSpace(bindings["APP_PORT"]); rawPort != "" {
		if parsedPort, err := strconv.Atoi(rawPort); err == nil && parsedPort > 0 {
			port = parsedPort
		}
	}
	targetURL := fmt.Sprintf("http://%s:%d/", dropletIP, port)
	deadline := time.Now().Add(12 * time.Minute)
	client := &http.Client{Timeout: 3 * time.Second}
	var lastErr error
	for attempt := 1; time.Now().Before(deadline); attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if w != nil {
			_, _ = fmt.Fprintf(w, "[openclaw] waiting for droplet gateway to become reachable at %s (attempt %d)\n", targetURL, attempt)
		}
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", dropletIP, port), 3*time.Second)
		if err == nil {
			_ = conn.Close()
			req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
			if reqErr == nil {
				resp, respErr := client.Do(req)
				if respErr == nil {
					_ = resp.Body.Close()
					if w != nil {
						_, _ = fmt.Fprintf(w, "[openclaw] droplet gateway is reachable at %s (status=%d); continuing to App Platform create\n", targetURL, resp.StatusCode)
					}
					return nil
				}
				lastErr = respErr
			} else {
				lastErr = reqErr
			}
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Second):
		}
	}
	if lastErr != nil {
		return fmt.Errorf("gateway at %s did not become reachable within 12m: %w", targetURL, lastErr)
	}
	return fmt.Errorf("gateway at %s did not become reachable within 12m", targetURL)
}

func retryDOCRPushAfterFreshRegistryCreate(ctx context.Context, args []string, opts ExecOptions, workDir string, bindings map[string]string, w io.Writer) (string, error) {
	backoffs := []time.Duration{10 * time.Second, 20 * time.Second}
	var lastOut string
	var lastErr error
	for attempt, backoff := range backoffs {
		if w != nil {
			_, _ = fmt.Fprintf(w, "[maker] DOCR push auth failed immediately after registry creation; waiting %s, refreshing registry login, and retrying (%d/%d)\n", backoff, attempt+1, len(backoffs))
		}
		select {
		case <-ctx.Done():
			return lastOut, ctx.Err()
		case <-time.After(backoff):
		}
		if loginErr := loginDORegistryWithDockerConfig(ctx, bindings, opts, w); loginErr != nil {
			if w != nil {
				_, _ = fmt.Fprintf(w, "[maker] warning: DOCR relogin before retry failed: %v\n", loginErr)
			}
		}
		lastOut, lastErr = runDockerCommandStreaming(ctx, args, opts, workDir, w)
		if lastErr == nil || !isDOCRPushAuthFailure(args, lastOut) {
			return lastOut, lastErr
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("docker push failed after fresh-registry retry attempts")
	}
	return lastOut, lastErr
}

func retryOpenClawDOProxyBuildAndPushAfterFreshRegistryCreate(ctx context.Context, args []string, opts ExecOptions, bindings map[string]string, w io.Writer) (string, error) {
	backoffs := []time.Duration{10 * time.Second, 20 * time.Second}
	var lastOut string
	var lastErr error
	for attempt, backoff := range backoffs {
		if w != nil {
			_, _ = fmt.Fprintf(w, "[maker] DOCR buildx push auth failed immediately after registry creation; waiting %s, refreshing registry login, and retrying (%d/%d)\n", backoff, attempt+1, len(backoffs))
		}
		select {
		case <-ctx.Done():
			return lastOut, ctx.Err()
		case <-time.After(backoff):
		}
		if loginErr := loginDORegistryWithDockerConfig(ctx, bindings, opts, w); loginErr != nil {
			if w != nil {
				_, _ = fmt.Fprintf(w, "[maker] warning: DOCR relogin before buildx retry failed: %v\n", loginErr)
			}
		}
		lastOut, lastErr = runOpenClawDOProxyBuildAndPush(ctx, args, opts, w)
		if lastErr == nil || !outputLooksLikeDOCRPushAuthFailure(lastOut) {
			return lastOut, lastErr
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("docker buildx push failed after fresh-registry retry attempts")
	}
	return lastOut, lastErr
}

func loginDORegistryWithDockerConfig(ctx context.Context, bindings map[string]string, opts ExecOptions, w io.Writer) error {
	registryName := strings.TrimSpace(bindings["REGISTRY_NAME"])
	if registryName == "" || strings.TrimSpace(opts.DigitalOceanDockerConfigDir) == "" {
		_, err := runDoctlCommandWithRetry(ctx, []string{"registry", "login"}, opts, w)
		return err
	}
	configJSON, err := fetchDORegistryDockerConfig(ctx, registryName, opts)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(opts.DigitalOceanDockerConfigDir, 0700); err != nil {
		return fmt.Errorf("create Docker config dir: %w", err)
	}
	configPath := filepath.Join(opts.DigitalOceanDockerConfigDir, "config.json")
	if err := os.WriteFile(configPath, configJSON, 0600); err != nil {
		return fmt.Errorf("write Docker config: %w", err)
	}
	if err := ensureDockerConfigPluginDirs(opts.DigitalOceanDockerConfigDir); err != nil {
		return fmt.Errorf("augment Docker config with plugin dirs: %w", err)
	}
	if w != nil {
		_, _ = fmt.Fprintf(w, "[maker] wrote read-write DOCR auth config for registry %s\n", registryName)
	}
	return nil
}

func fetchDORegistryDockerConfig(ctx context.Context, registryName string, opts ExecOptions) ([]byte, error) {
	bin, err := exec.LookPath("doctl")
	if err != nil {
		return nil, fmt.Errorf("doctl not found in PATH: %w", err)
	}
	cmdArgs := []string{"registry", "docker-config", registryName, "--read-write"}
	fullArgs := append([]string{"--access-token", opts.DigitalOceanAPIToken}, cmdArgs...)
	cmd := exec.CommandContext(ctx, bin, fullArgs...)
	cmd.Env = doctlEnvForExec(cmdArgs, opts)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("fetch DOCR docker-config: %w", err)
	}
	raw := bytes.TrimSpace(buf.Bytes())
	if len(raw) == 0 {
		return nil, fmt.Errorf("empty DOCR docker-config output")
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("invalid DOCR docker-config JSON: %w", err)
	}
	return raw, nil
}

func lookupDORegistryName(ctx context.Context, apiToken string) (string, error) {
	opts := ExecOptions{DigitalOceanAPIToken: strings.TrimSpace(apiToken)}
	out, err := runDoctlCommandWithRetry(ctx, []string{"registry", "get", "--output", "json"}, opts, io.Discard)
	if err != nil {
		lower := strings.ToLower(out + "\n" + err.Error())
		if strings.Contains(lower, "not found") || strings.Contains(lower, "404") || strings.Contains(lower, "no registry") {
			return "", nil
		}
		return "", fmt.Errorf("lookup DigitalOcean registry: %w", err)
	}
	var entries []map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &entries); err != nil {
		return "", fmt.Errorf("parse DigitalOcean registry lookup: %w", err)
	}
	if len(entries) == 0 {
		return "", nil
	}
	return doJSONStringValue(entries[0]["name"]), nil
}

func extractDOCRRepositoryFromImageRef(ref string) string {
	ref = strings.TrimSpace(ref)
	const prefix = "registry.digitalocean.com/"
	if !strings.HasPrefix(ref, prefix) {
		return ""
	}
	remainder := strings.TrimPrefix(ref, prefix)
	_, repoWithTag, ok := strings.Cut(remainder, "/")
	if !ok || strings.TrimSpace(repoWithTag) == "" {
		return ""
	}
	if idx := strings.Index(repoWithTag, "@"); idx >= 0 {
		repoWithTag = repoWithTag[:idx]
	}
	if idx := strings.LastIndex(repoWithTag, ":"); idx >= 0 {
		repoWithTag = repoWithTag[:idx]
	}
	return strings.Trim(strings.TrimSpace(repoWithTag), "/")
}

func rewriteDOCRImageRefRepository(ref string, repositoryName string) string {
	ref = strings.TrimSpace(ref)
	repositoryName = strings.Trim(strings.TrimSpace(repositoryName), "/")
	const prefix = "registry.digitalocean.com/"
	if repositoryName == "" || !strings.HasPrefix(ref, prefix) {
		return ref
	}
	remainder := strings.TrimPrefix(ref, prefix)
	registryName, repoWithTag, ok := strings.Cut(remainder, "/")
	if !ok || strings.TrimSpace(registryName) == "" || strings.TrimSpace(repoWithTag) == "" {
		return ref
	}
	suffix := ""
	if idx := strings.Index(repoWithTag, "@"); idx >= 0 {
		suffix = repoWithTag[idx:]
	} else if idx := strings.LastIndex(repoWithTag, ":"); idx >= 0 {
		suffix = repoWithTag[idx:]
	}
	return prefix + registryName + "/" + repositoryName + suffix
}

func rewriteDigitalOceanRegistryPushRepository(plan *Plan, repositoryName string) bool {
	if plan == nil || strings.TrimSpace(repositoryName) == "" {
		return false
	}
	changed := false
	for idx := range plan.Commands {
		cmd := &plan.Commands[idx]
		for argIdx, arg := range cmd.Args {
			updated := rewriteDOCRImageRefRepository(arg, repositoryName)
			if updated != arg {
				cmd.Args[argIdx] = updated
				changed = true
			}
		}
		if len(cmd.Args) >= 2 && strings.EqualFold(strings.TrimSpace(cmd.Args[0]), "apps") && strings.EqualFold(strings.TrimSpace(cmd.Args[1]), "create") {
			if rewriteDOAppSpecRepository(cmd, repositoryName) {
				changed = true
			}
		}
		for key, value := range cmd.Produces {
			updated := rewriteDOCRImageRefRepository(value, repositoryName)
			if updated != value {
				cmd.Produces[key] = updated
				changed = true
			}
		}
	}
	return changed
}

func rewriteDOAppSpecRepository(cmd *Command, repositoryName string) bool {
	if cmd == nil || len(cmd.Args) == 0 {
		return false
	}
	for idx := 0; idx < len(cmd.Args); idx++ {
		arg := strings.TrimSpace(cmd.Args[idx])
		var specRaw string
		var valueIdx int
		switch {
		case arg == "--spec" && idx+1 < len(cmd.Args):
			specRaw = cmd.Args[idx+1]
			valueIdx = idx + 1
		case strings.HasPrefix(arg, "--spec="):
			specRaw = strings.TrimPrefix(cmd.Args[idx], "--spec=")
			valueIdx = idx
		default:
			continue
		}
		var spec map[string]any
		if err := json.Unmarshal([]byte(specRaw), &spec); err != nil {
			return false
		}
		services, ok := spec["services"].([]any)
		if !ok || len(services) == 0 {
			return false
		}
		service, ok := services[0].(map[string]any)
		if !ok {
			return false
		}
		image, ok := service["image"].(map[string]any)
		if !ok {
			return false
		}
		if strings.TrimSpace(doJSONStringValue(image["repository"])) == repositoryName {
			return false
		}
		image["repository"] = repositoryName
		encoded, err := json.Marshal(spec)
		if err != nil {
			return false
		}
		if valueIdx == idx {
			cmd.Args[idx] = "--spec=" + string(encoded)
		} else {
			cmd.Args[valueIdx] = string(encoded)
		}
		return true
	}
	return false
}

func prepareAdaptiveDigitalOceanRepository(ctx context.Context, apiToken string, registryName string, targetRepo string, w io.Writer) (string, error) {
	candidates, err := generateAdaptiveDOCRRepositoryCandidates(targetRepo, 3)
	if err != nil {
		return "", err
	}
	var failures []string
	for _, candidate := range candidates {
		if strings.EqualFold(candidate, strings.TrimSpace(targetRepo)) {
			continue
		}
		if w != nil {
			if strings.TrimSpace(registryName) == "" {
				_, _ = fmt.Fprintf(w, "[maker] DOCR prereq: repository %s was rejected; retrying with adaptive repository %s\n", targetRepo, candidate)
			} else {
				_, _ = fmt.Fprintf(w, "[maker] DOCR prereq: repository %s was rejected in existing registry %s; retrying with adaptive repository %s\n", targetRepo, registryName, candidate)
			}
		}
		var probeErr error
		if strings.TrimSpace(registryName) == "" {
			probeErr = ProbeDigitalOceanRegistryPushPrereq(ctx, apiToken, candidate, w)
		} else {
			probeErr = probeDigitalOceanRegistryPushPrereqForRegistry(ctx, apiToken, registryName, candidate, w)
		}
		if probeErr == nil {
			return candidate, nil
		}
		failures = append(failures, fmt.Sprintf("%s: %v", candidate, probeErr))
	}
	if strings.TrimSpace(registryName) != "" {
		return "", fmt.Errorf("none of the fresh repository names were writable in existing registry %s; verify the active DigitalOcean token/context has image-push permission for that registry: %s", registryName, strings.Join(failures, "; "))
	}
	return "", errors.New(strings.Join(failures, "; "))
}

func generateDOCRProbeRepositoryName() (string, error) {
	return generateDOCRRepositoryName(doDOCRPushProbePrefix)
}

func generateDOCRFallbackRepositoryName() (string, error) {
	return generateDOCRRepositoryName(doDOCRPushFallbackPrefix)
}

func generateAdaptiveDOCRRepositoryCandidates(targetRepo string, count int) ([]string, error) {
	base := sanitizeDOCRRepositoryBase(targetRepo)
	if base == "" {
		base = doDOCRPushFallbackPrefix
	}
	candidates := make([]string, 0, count)
	seen := map[string]struct{}{}
	for len(candidates) < count {
		candidate, err := generateDOCRRepositoryName(base)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		candidates = append(candidates, candidate)
	}
	return candidates, nil
}

func generateDOCRRepositoryName(prefix string) (string, error) {
	prefix = sanitizeDOCRRepositoryBase(prefix)
	if prefix == "" {
		prefix = "clanker-docr"
	}
	suffix := make([]byte, 3)
	if _, err := rand.Read(suffix); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%x", prefix, suffix), nil
}

func sanitizeDOCRRepositoryBase(value string) string {
	trimmed := strings.ToLower(strings.TrimSpace(value))
	if trimmed == "" {
		return ""
	}
	trimmed = strings.ReplaceAll(trimmed, "/", "-")
	trimmed = strings.ReplaceAll(trimmed, "_", "-")
	var builder strings.Builder
	lastDash := false
	for _, r := range trimmed {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			builder.WriteRune(r)
			lastDash = false
		case r == '-':
			if !lastDash {
				builder.WriteRune(r)
				lastDash = true
			}
		}
	}
	out := strings.Trim(builder.String(), "-")
	if len(out) > 48 {
		out = strings.Trim(out[:48], "-")
	}
	return out
}

// dockerArgs strips the leading "docker" from args
func dockerArgs(args []string) []string {
	if len(args) > 1 && strings.ToLower(strings.TrimSpace(args[0])) == "docker" {
		return args[1:]
	}
	return args
}

// runDockerCommandStreaming executes a docker CLI command with streaming output.
// workDir is set as cmd.Dir for build commands so the "." context resolves to the cloned repo.
// Push commands get a 15-min timeout to avoid indefinite hangs (e.g. DOCR storage quota exceeded).
func runDockerCommandStreaming(ctx context.Context, args []string, opts ExecOptions, workDir string, w io.Writer) (string, error) {
	bin, err := exec.LookPath("docker")
	if err != nil {
		return "", fmt.Errorf("docker not found in PATH: %w", err)
	}

	cmdArgs := dockerArgs(args)
	cmdArgs = ensureDOProxyBuildPlatform(cmdArgs)

	// Apply a 5-min timeout for docker push — DOCR silently stalls when storage quota is exceeded
	execCtx := ctx
	if len(cmdArgs) > 0 && strings.EqualFold(strings.TrimSpace(cmdArgs[0]), "push") {
		var cancel context.CancelFunc
		execCtx, cancel = context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
	}

	cmd := exec.CommandContext(execCtx, bin, cmdArgs...)
	cmd.Env = dockerEnvForExec(cmdArgs, opts)

	// Set working dir for build/tag — the "." build context needs to point at the repo
	if workDir != "" && isDockerCommand(args) {
		if len(cmdArgs) > 0 {
			sub := strings.ToLower(strings.TrimSpace(cmdArgs[0]))
			if sub == "build" || sub == "tag" {
				cmd.Dir = workDir
			}
		}
	}
	if isDockerCommand(args) {
		if len(cmdArgs) > 0 && strings.EqualFold(strings.TrimSpace(cmdArgs[0]), "build") {
			if ctxDir, cleanup, ok, err := prepareSpecialDockerBuildContext(cmdArgs, w); err != nil {
				return "", err
			} else if ok {
				defer cleanup()
				cmd.Dir = ctxDir
				cmd.Args = append([]string{bin}, rewriteDockerBuildArgsForMaterializedContext(cmdArgs)...)
			}
		}
	}

	var buf bytes.Buffer
	mw := io.MultiWriter(w, &buf)
	cmd.Stdout = mw
	cmd.Stderr = mw

	err = cmd.Run()
	out := buf.String()
	if err != nil {
		// Detect push timeout — likely DOCR storage quota exceeded
		if execCtx.Err() == context.DeadlineExceeded && len(cmdArgs) > 0 && strings.EqualFold(cmdArgs[0], "push") {
			return out, fmt.Errorf("docker push timed out after 5m (DOCR storage quota may be exceeded — ensure registry uses 'basic' tier or higher): %w", err)
		}
		return out, err
	}
	return out, nil
}

// planHasDockerBuild returns true if any command in the plan is a docker build
func planHasDockerBuild(plan *Plan) bool {
	for _, cmd := range plan.Commands {
		if isDockerCommand(cmd.Args) {
			dArgs := dockerArgs(cmd.Args)
			if len(dArgs) > 0 && strings.EqualFold(dArgs[0], "build") {
				return true
			}
		}
	}
	return false
}

// normalizeDoctlOutputFlags rewrites --format json → --output json.
// doctl uses --format for column selection (e.g. --format ID,Name) and
// --output for format type (json/text). LLMs sometimes mix them up.
func normalizeDoctlOutputFlags(args []string) []string {
	for i := 0; i < len(args)-1; i++ {
		if strings.EqualFold(strings.TrimSpace(args[i]), "--format") &&
			strings.EqualFold(strings.TrimSpace(args[i+1]), "json") {
			args[i] = "--output"
		}
	}
	return args
}

func normalizeDOFirewallCreateNameAtExec(args []string) []string {
	if len(args) < 4 {
		return args
	}
	if !strings.EqualFold(strings.TrimSpace(args[0]), "compute") || !strings.EqualFold(strings.TrimSpace(args[1]), "firewall") || !strings.EqualFold(strings.TrimSpace(args[2]), "create") {
		return args
	}
	if strings.TrimSpace(flagValue(args, "--name")) != "" {
		return args
	}
	positionalName := strings.TrimSpace(args[3])
	if positionalName == "" || strings.HasPrefix(positionalName, "-") {
		return args
	}
	updated := make([]string, 0, len(args)+1)
	updated = append(updated, args[:3]...)
	updated = append(updated, "--name", positionalName)
	updated = append(updated, args[4:]...)
	return updated
}

// normalizeFirewallRuleFlagsAtExec merges repeated --inbound-rules/--outbound-rules
// into the single doctl form DigitalOcean expects. Repeated flags cause only the
// last rule set to survive, silently dropping SSH/app ports.
func normalizeFirewallRuleFlagsAtExec(args []string) []string {
	if len(args) < 3 {
		return args
	}
	if !strings.EqualFold(strings.TrimSpace(args[0]), "compute") || !strings.EqualFold(strings.TrimSpace(args[1]), "firewall") {
		return args
	}
	verb := strings.ToLower(strings.TrimSpace(args[2]))
	if verb != "create" && verb != "update" {
		return args
	}

	var inboundVals, outboundVals []string
	cleaned := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		trimmed := strings.TrimSpace(args[i])
		switch {
		case trimmed == "--inbound-rules" && i+1 < len(args):
			if v := strings.TrimSpace(args[i+1]); v != "" {
				inboundVals = append(inboundVals, v)
			}
			i++
		case strings.HasPrefix(trimmed, "--inbound-rules="):
			if v := strings.TrimSpace(strings.TrimPrefix(trimmed, "--inbound-rules=")); v != "" {
				inboundVals = append(inboundVals, v)
			}
		case trimmed == "--outbound-rules" && i+1 < len(args):
			if v := strings.TrimSpace(args[i+1]); v != "" {
				outboundVals = append(outboundVals, v)
			}
			i++
		case strings.HasPrefix(trimmed, "--outbound-rules="):
			if v := strings.TrimSpace(strings.TrimPrefix(trimmed, "--outbound-rules=")); v != "" {
				outboundVals = append(outboundVals, v)
			}
		default:
			cleaned = append(cleaned, args[i])
		}
	}

	insertAt := len(cleaned)
	for i, a := range cleaned {
		if strings.EqualFold(strings.TrimSpace(a), "--output") || strings.HasPrefix(strings.TrimSpace(a), "--output=") {
			insertAt = i
			break
		}
	}
	prefix := append([]string{}, cleaned[:insertAt]...)
	suffix := append([]string{}, cleaned[insertAt:]...)
	if len(inboundVals) > 0 {
		prefix = append(prefix, "--inbound-rules", strings.Join(inboundVals, " "))
	}
	if len(outboundVals) > 0 {
		prefix = append(prefix, "--outbound-rules", strings.Join(outboundVals, " "))
	}
	return append(prefix, suffix...)
}

// runDoctlCommandStreaming executes a doctl command with streaming output
func runDoctlCommandStreaming(ctx context.Context, args []string, opts ExecOptions, w io.Writer) (string, error) {
	bin, err := exec.LookPath("doctl")
	if err != nil {
		return "", fmt.Errorf("doctl not found in PATH: %w", err)
	}

	// Strip "doctl" from args if present
	cmdArgs := args
	if len(args) > 0 && strings.ToLower(strings.TrimSpace(args[0])) == "doctl" {
		cmdArgs = args[1:]
	}

	// Inject access token
	fullArgs := append([]string{"--access-token", opts.DigitalOceanAPIToken}, cmdArgs...)

	cmd := exec.CommandContext(ctx, bin, fullArgs...)
	cmd.Env = doctlEnvForExec(cmdArgs, opts)

	var buf bytes.Buffer
	if shouldSanitizeDOOutputForLog(args) {
		cmd.Stdout = &buf
		cmd.Stderr = &buf
	} else {
		mw := io.MultiWriter(w, &buf)
		cmd.Stdout = mw
		cmd.Stderr = mw
	}

	err = cmd.Run()
	out := buf.String()
	if shouldSanitizeDOOutputForLog(args) && w != nil {
		_, _ = io.WriteString(w, sanitizeDOOutputForLog(out))
	}
	if err != nil {
		return out, err
	}
	return out, nil
}

func shouldSanitizeDOOutputForLog(args []string) bool {
	if len(args) < 3 {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(args[0]), "compute") &&
		strings.EqualFold(strings.TrimSpace(args[1]), "ssh-key") &&
		(strings.EqualFold(strings.TrimSpace(args[2]), "list") ||
			strings.EqualFold(strings.TrimSpace(args[2]), "get") ||
			strings.EqualFold(strings.TrimSpace(args[2]), "import"))
}

func sanitizeDOOutputForLog(out string) string {
	publicKeyRe := regexp.MustCompile(`"public_key"\s*:\s*"[^"]*"`)
	return publicKeyRe.ReplaceAllString(out, `"public_key":"<redacted>"`)
}

// ---------------------------------------------------------------------------
// Tilde expansion — doctl/exec.Command don't do shell expansion
// ---------------------------------------------------------------------------

// expandTildeInArgs replaces leading ~ with absolute home dir in all args.
func expandTildeInArgs(args []string) []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return args
	}
	for i, a := range args {
		if strings.HasPrefix(a, "~/") {
			args[i] = filepath.Join(home, a[2:])
		}
	}
	return args
}

// ---------------------------------------------------------------------------
// DO error classification
// ---------------------------------------------------------------------------

// DOFailureCategory classifies a doctl failure
type DOFailureCategory string

const (
	DOFailureUnknown       DOFailureCategory = "unknown"
	DOFailureAlreadyExists DOFailureCategory = "already-exists"
	DOFailureNotFound      DOFailureCategory = "not-found"
	DOFailureRateLimit     DOFailureCategory = "rate-limit"
	DOFailureAuth          DOFailureCategory = "auth"
	DOFailureQuota         DOFailureCategory = "quota"
	DOFailureValidation    DOFailureCategory = "validation"
)

type DOFailure struct {
	Service  string
	Op       string
	Category DOFailureCategory
	Message  string
}

func classifyDOFailure(args []string, output string) DOFailure {
	f := DOFailure{Category: DOFailureUnknown}
	if len(args) >= 1 {
		f.Service = strings.TrimSpace(args[0])
	}
	if len(args) >= 2 {
		f.Op = strings.TrimSpace(args[1])
	}
	msg := strings.TrimSpace(output)
	if len(msg) > 600 {
		msg = msg[:600]
	}
	f.Message = msg

	lower := strings.ToLower(output)

	// Already exists / already in use / already has
	if strings.Contains(lower, "already exists") ||
		strings.Contains(lower, "is already in use") ||
		strings.Contains(lower, "already has a") ||
		strings.Contains(lower, "ssh key already exists") ||
		strings.Contains(lower, "duplicate") {
		f.Category = DOFailureAlreadyExists
		return f
	}

	// Not found
	if strings.Contains(lower, "not found") ||
		strings.Contains(lower, "404") ||
		strings.Contains(lower, "does not exist") ||
		strings.Contains(lower, "no registry") ||
		strings.Contains(lower, "no such file") ||
		strings.Contains(lower, "could not find") {
		f.Category = DOFailureNotFound
		return f
	}

	// Validation / usage errors
	if strings.Contains(lower, "unknown flag") ||
		strings.Contains(lower, "invalid value") ||
		strings.Contains(lower, "accepts ") ||
		strings.Contains(lower, "help for create") {
		f.Category = DOFailureValidation
		return f
	}

	// Rate limit
	if strings.Contains(lower, "rate limit") ||
		strings.Contains(lower, "too many requests") {
		f.Category = DOFailureRateLimit
		return f
	}

	// Auth
	if strings.Contains(lower, "unauthorized") ||
		strings.Contains(lower, "unable to authenticate") ||
		strings.Contains(lower, "access denied") ||
		strings.Contains(lower, "invalid token") {
		f.Category = DOFailureAuth
		return f
	}

	// Quota
	if strings.Contains(lower, "droplet limit") ||
		strings.Contains(lower, "quota") ||
		strings.Contains(lower, "limit reached") ||
		strings.Contains(lower, "will exceed your reserved ip limit") ||
		strings.Contains(lower, "exceed your reserved ip limit") {
		f.Category = DOFailureQuota
		return f
	}

	// Validation
	if strings.Contains(lower, "unprocessable") ||
		strings.Contains(lower, "invalid") {
		f.Category = DOFailureValidation
		return f
	}

	return f
}

// shouldIgnoreDOFailure returns true for non-fatal errors on create commands.
func shouldIgnoreDOFailure(args []string, failure DOFailure) bool {
	if failure.Category == DOFailureQuota && len(args) >= 3 && strings.EqualFold(args[0], "compute") && strings.EqualFold(args[1], "reserved-ip") && strings.EqualFold(args[2], "create") {
		return true
	}
	if failure.Category == DOFailureNotFound && isDORegistryGet(args) {
		return true
	}
	if failure.Category != DOFailureAlreadyExists {
		return false
	}
	// Safe to ignore "already exists" on create/import operations
	lower := strings.ToLower(strings.Join(args, " "))
	return strings.Contains(lower, "create") ||
		strings.Contains(lower, "import")
}

// runDoctlCommandWithRetry wraps runDoctlCommandStreaming with retry for transient errors.
func runDoctlCommandWithRetry(ctx context.Context, args []string, opts ExecOptions, w io.Writer) (string, error) {
	const maxRetries = 3
	var out string
	var err error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		out, err = runDoctlCommandStreaming(ctx, args, opts, w)
		if err == nil {
			return out, nil
		}

		failure := classifyDOFailure(args, out)
		if failure.Category != DOFailureRateLimit {
			return out, err // not transient, don't retry
		}

		if attempt < maxRetries {
			backoff := time.Duration(1<<uint(attempt)) * 2 * time.Second // 2s, 4s, 8s
			_, _ = fmt.Fprintf(w, "[maker] rate limited, retrying in %s (attempt %d/%d)\n", backoff, attempt+1, maxRetries)
			select {
			case <-ctx.Done():
				return out, ctx.Err()
			case <-time.After(backoff):
			}
		}
	}
	return out, err
}

// ---------------------------------------------------------------------------
// Binding recovery & runtime computation
// ---------------------------------------------------------------------------

// recoverDOBindingsAfterSkip fetches existing resource data when a create/import
// is skipped (already exists). Runs the corresponding GET/LIST to populate bindings.
func recoverDOBindingsAfterSkip(ctx context.Context, args []string, produces map[string]string, bindings map[string]string, opts ExecOptions, w io.Writer) {
	if len(args) < 2 {
		return
	}

	lower := strings.ToLower(strings.Join(args, " "))

	var getArgs []string
	switch {
	case strings.HasPrefix(lower, "registry create"):
		getArgs = []string{"registry", "get", "--output", "json"}
	case strings.HasPrefix(lower, "apps create"):
		getArgs = []string{"apps", "list", "--output", "json"}
	case strings.Contains(lower, "ssh-key import"):
		getArgs = []string{"compute", "ssh-key", "list", "--output", "json"}
	case strings.Contains(lower, "firewall create"):
		getArgs = []string{"compute", "firewall", "list", "--output", "json"}
	default:
		return
	}

	_, _ = fmt.Fprintf(w, "[maker] recovering bindings via: doctl %s\n", strings.Join(getArgs, " "))
	out, err := runDoctlCommandStreaming(ctx, getArgs, opts, w)
	if err != nil {
		_, _ = fmt.Fprintf(w, "[maker] warning: binding recovery failed: %v\n", err)
		return
	}

	// Standard produce extraction (works when autofix has fixed the paths)
	if len(produces) > 0 {
		learnPlanBindingsFromProduces(produces, out, bindings)
	}

	// Direct extraction for registry (produce paths may be hallucinated)
	if strings.HasPrefix(lower, "registry create") {
		extractRegistryBindingsDirect(out, bindings)
		return
	}
	if strings.HasPrefix(lower, "apps create") {
		extractDOAppBindingsFromList(args, out, bindings)
		return
	}
	if strings.Contains(lower, "ssh-key import") {
		extractSSHKeyBindingsDirect(args, out, bindings)
		return
	}
	if strings.Contains(lower, "firewall create") {
		extractFirewallBindingsDirect(args, out, bindings)
	}
}

// extractRegistryBindingsDirect parses doctl registry get output and extracts
// REGISTRY_NAME directly, bypassing potentially hallucinated produce paths.
func extractRegistryBindingsDirect(output string, bindings map[string]string) {
	output = strings.TrimSpace(output)
	if output == "" {
		return
	}
	if extracted := extractLeadingJSONPayload(output); extracted != "" {
		output = extracted
	}

	var obj any
	if err := json.Unmarshal([]byte(output), &obj); err != nil {
		return
	}

	// Handle array-wrapped response [{ ... }]
	if arr, ok := obj.([]any); ok && len(arr) > 0 {
		obj = arr[0]
	}

	m, ok := obj.(map[string]any)
	if !ok {
		return
	}

	if name, ok := m["name"].(string); ok && name != "" {
		if strings.TrimSpace(bindings["REGISTRY_NAME"]) == "" {
			bindings["REGISTRY_NAME"] = name
		}
	}
}

func extractSSHKeyBindingsDirect(args []string, output string, bindings map[string]string) {
	if len(args) < 3 || !strings.EqualFold(strings.TrimSpace(args[0]), "compute") || !strings.EqualFold(strings.TrimSpace(args[1]), "ssh-key") {
		return
	}
	verb := strings.ToLower(strings.TrimSpace(args[2]))
	if verb != "import" && verb != "list" && verb != "get" {
		return
	}
	output = strings.TrimSpace(output)
	if output == "" {
		return
	}
	keyName := ""
	if len(args) >= 4 {
		keyName = strings.TrimSpace(args[3])
	}
	publicKeyPath := expandHomePath(strings.TrimSpace(flagValue(args, "--public-key-file")))
	publicKeyText := ""
	if publicKeyPath != "" {
		if blob, err := os.ReadFile(publicKeyPath); err == nil {
			publicKeyText = strings.TrimSpace(string(blob))
		}
	}

	var entries []map[string]any
	if err := json.Unmarshal([]byte(output), &entries); err != nil {
		return
	}
	for _, entry := range entries {
		name := doJSONStringValue(entry["name"])
		pub := doJSONStringValue(entry["public_key"])
		if publicKeyText != "" && pub == publicKeyText {
			if id := doJSONStringValue(entry["id"]); id != "" {
				bindings["SSH_KEY_ID"] = id
			}
			return
		}
		if keyName != "" && name == keyName {
			if id := doJSONStringValue(entry["id"]); id != "" {
				bindings["SSH_KEY_ID"] = id
			}
			return
		}
	}
}

func extractFirewallBindingsDirect(args []string, output string, bindings map[string]string) {
	output = strings.TrimSpace(output)
	if output == "" {
		return
	}
	firewallName := strings.TrimSpace(flagValue(args, "--name"))
	if firewallName == "" {
		return
	}
	var entries []map[string]any
	if err := json.Unmarshal([]byte(output), &entries); err != nil {
		return
	}
	for _, entry := range entries {
		name := doJSONStringValue(entry["name"])
		if name != firewallName {
			continue
		}
		if id := doJSONStringValue(entry["id"]); id != "" {
			bindings["FIREWALL_ID"] = id
		}
		return
	}
}

func extractDODropletBindingsDirect(args []string, output string, bindings map[string]string) {
	if len(args) < 3 || !strings.EqualFold(strings.TrimSpace(args[0]), "compute") || !strings.EqualFold(strings.TrimSpace(args[1]), "droplet") || !strings.EqualFold(strings.TrimSpace(args[2]), "create") {
		return
	}
	output = strings.TrimSpace(output)
	if output == "" {
		return
	}
	var entries []map[string]any
	if err := json.Unmarshal([]byte(output), &entries); err != nil {
		return
	}
	if len(entries) == 0 {
		return
	}
	entry := entries[0]
	if id := doJSONStringValue(entry["id"]); id != "" && strings.TrimSpace(bindings["DROPLET_ID"]) == "" {
		bindings["DROPLET_ID"] = id
	}
	networks, ok := entry["networks"].(map[string]any)
	if !ok {
		return
	}
	v4, ok := networks["v4"].([]any)
	if !ok || len(v4) == 0 {
		return
	}
	fallbackIP := ""
	for _, raw := range v4 {
		netEntry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		ip := doJSONStringValue(netEntry["ip_address"])
		if ip == "" {
			continue
		}
		if fallbackIP == "" {
			fallbackIP = ip
		}
		if strings.EqualFold(doJSONStringValue(netEntry["type"]), "public") {
			bindings["DROPLET_IP"] = ip
			return
		}
	}
	if fallbackIP != "" && strings.TrimSpace(bindings["DROPLET_IP"]) == "" {
		bindings["DROPLET_IP"] = fallbackIP
	}
}

func recoverDODropletIPAfterCreate(ctx context.Context, bindings map[string]string, opts ExecOptions, w io.Writer) {
	if strings.TrimSpace(bindings["DROPLET_IP"]) != "" {
		return
	}
	dropletID := strings.TrimSpace(bindings["DROPLET_ID"])
	if dropletID == "" {
		return
	}
	if w != nil {
		_, _ = fmt.Fprintf(w, "[maker] recovering droplet networking via: doctl compute droplet get %s --output json\n", dropletID)
	}
	out, err := runDoctlCommandWithRetry(ctx, []string{"compute", "droplet", "get", dropletID, "--output", "json"}, opts, io.Discard)
	if err != nil {
		if w != nil {
			_, _ = fmt.Fprintf(w, "[maker] warning: failed to recover droplet networking for %s: %v\n", dropletID, err)
		}
		return
	}
	extractDODropletBindingsFromGetOutput(out, bindings)
}

func extractDODropletBindingsFromGetOutput(output string, bindings map[string]string) {
	output = strings.TrimSpace(output)
	if output == "" {
		return
	}
	var entry map[string]any
	if err := json.Unmarshal([]byte(output), &entry); err == nil && len(entry) > 0 {
		extractDODropletBindingEntry(entry, bindings)
		return
	}
	var entries []map[string]any
	if err := json.Unmarshal([]byte(output), &entries); err != nil || len(entries) == 0 {
		return
	}
	extractDODropletBindingEntry(entries[0], bindings)
}

func extractDODropletBindingEntry(entry map[string]any, bindings map[string]string) {
	if len(entry) == 0 {
		return
	}
	if id := doJSONStringValue(entry["id"]); id != "" && strings.TrimSpace(bindings["DROPLET_ID"]) == "" {
		bindings["DROPLET_ID"] = id
	}
	networks, ok := entry["networks"].(map[string]any)
	if !ok {
		return
	}
	v4, ok := networks["v4"].([]any)
	if !ok || len(v4) == 0 {
		return
	}
	fallbackIP := ""
	for _, raw := range v4 {
		netEntry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		ip := doJSONStringValue(netEntry["ip_address"])
		if ip == "" {
			continue
		}
		if fallbackIP == "" {
			fallbackIP = ip
		}
		if strings.EqualFold(doJSONStringValue(netEntry["type"]), "public") {
			bindings["DROPLET_IP"] = ip
			return
		}
	}
	if fallbackIP != "" && strings.TrimSpace(bindings["DROPLET_IP"]) == "" {
		bindings["DROPLET_IP"] = fallbackIP
	}
}

type doVPCInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Region  string `json:"region"`
	Default bool   `json:"default"`
}

func normalizeDODropletVPCUUIDAtExec(ctx context.Context, args []string, opts ExecOptions, w io.Writer) ([]string, error) {
	if len(args) < 3 || !strings.EqualFold(strings.TrimSpace(args[0]), "compute") || !strings.EqualFold(strings.TrimSpace(args[1]), "droplet") || !strings.EqualFold(strings.TrimSpace(args[2]), "create") {
		return args, nil
	}
	vpcValue := strings.TrimSpace(flagValue(args, "--vpc-uuid"))
	if vpcValue == "" || strings.Contains(vpcValue, "<") || isDigitalOceanUUID(vpcValue) {
		return args, nil
	}
	vpcs, err := listDigitalOceanVPCs(ctx, opts)
	if err != nil {
		return args, fmt.Errorf("resolve digitalocean vpc %q: %w", vpcValue, err)
	}
	region := strings.TrimSpace(flagValue(args, "--region"))
	resolvedID, ok := resolveDigitalOceanVPCID(vpcValue, region, vpcs)
	if !ok {
		return args, fmt.Errorf("resolve digitalocean vpc %q: no matching VPC found in account%s", vpcValue, formatDORegionSuffix(region))
	}
	if w != nil {
		_, _ = fmt.Fprintf(w, "[maker] normalized DigitalOcean VPC %s -> %s for droplet create\n", vpcValue, resolvedID)
	}
	return setFlagValue(args, "--vpc-uuid", resolvedID), nil
}

func listDigitalOceanVPCs(ctx context.Context, opts ExecOptions) ([]doVPCInfo, error) {
	out, err := runDoctlCommandWithRetry(ctx, []string{"vpcs", "list", "--output", "json"}, opts, io.Discard)
	if err != nil {
		return nil, err
	}
	var vpcs []doVPCInfo
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &vpcs); err != nil {
		return nil, fmt.Errorf("parse digitalocean vpcs list: %w", err)
	}
	return vpcs, nil
}

func resolveDigitalOceanVPCID(rawValue string, region string, vpcs []doVPCInfo) (string, bool) {
	value := strings.TrimSpace(rawValue)
	region = strings.TrimSpace(region)
	if value == "" {
		return "", false
	}
	matchRegion := func(vpc doVPCInfo) bool {
		return region == "" || strings.EqualFold(strings.TrimSpace(vpc.Region), region)
	}
	for _, vpc := range vpcs {
		if strings.EqualFold(strings.TrimSpace(vpc.ID), value) {
			return strings.TrimSpace(vpc.ID), true
		}
	}
	for _, vpc := range vpcs {
		if matchRegion(vpc) && strings.EqualFold(strings.TrimSpace(vpc.Name), value) {
			return strings.TrimSpace(vpc.ID), true
		}
	}
	if strings.EqualFold(value, "default") || strings.HasPrefix(strings.ToLower(value), "default-") {
		for _, vpc := range vpcs {
			if matchRegion(vpc) && vpc.Default {
				return strings.TrimSpace(vpc.ID), true
			}
		}
	}
	if len(vpcs) == 1 && strings.EqualFold(strings.TrimSpace(vpcs[0].Name), value) {
		return strings.TrimSpace(vpcs[0].ID), true
	}
	return "", false
}

func isDigitalOceanUUID(value string) bool {
	matched, _ := regexp.MatchString("(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$", strings.TrimSpace(value))
	return matched
}

func formatDORegionSuffix(region string) string {
	region = strings.TrimSpace(region)
	if region == "" {
		return ""
	}
	return fmt.Sprintf(" for region %s", region)
}

func extractDOAppBindingsDirect(args []string, output string, bindings map[string]string) {
	if len(args) < 2 || !strings.EqualFold(strings.TrimSpace(args[0]), "apps") {
		return
	}
	verb := strings.ToLower(strings.TrimSpace(args[1]))
	if verb != "create" && verb != "get" && verb != "update" {
		return
	}
	output = strings.TrimSpace(output)
	if output == "" {
		return
	}
	if extracted := extractLeadingJSONPayload(output); extracted != "" {
		output = extracted
	}
	var obj any
	if err := json.Unmarshal([]byte(output), &obj); err != nil {
		return
	}
	if arr, ok := obj.([]any); ok && len(arr) > 0 {
		obj = arr[0]
	}
	m, ok := obj.(map[string]any)
	if !ok {
		return
	}
	if id := doJSONStringValue(m["id"]); id != "" && strings.TrimSpace(bindings["APP_ID"]) == "" {
		bindings["APP_ID"] = id
	}
	ingress := firstNonEmptyDOJSONString(
		m["default_ingress"],
		m["DefaultIngress"],
		m["live_url"],
		m["LiveURL"],
	)
	if ingress == "" {
		if domain := firstNonEmptyDOJSONString(m["live_domain"], m["LiveDomain"]); domain != "" {
			ingress = domain
		}
	}
	if ingress != "" {
		if !strings.HasPrefix(ingress, "https://") && !strings.HasPrefix(ingress, "http://") {
			ingress = "https://" + ingress
		}
		if strings.TrimSpace(bindings["APP_URL"]) == "" {
			bindings["APP_URL"] = ingress
		}
		if strings.TrimSpace(bindings["HTTPS_URL"]) == "" {
			bindings["HTTPS_URL"] = ingress
		}
	}
}

func extractDOAppBindingsFromList(args []string, output string, bindings map[string]string) {
	output = strings.TrimSpace(output)
	if output == "" {
		return
	}
	if extracted := extractLeadingJSONPayload(output); extracted != "" {
		output = extracted
	}
	appName := strings.TrimSpace(flagValue(args, "--name"))
	if appName == "" && len(args) >= 4 && strings.EqualFold(strings.TrimSpace(args[0]), "apps") && strings.EqualFold(strings.TrimSpace(args[1]), "create") {
		for i := 0; i < len(args)-1; i++ {
			if strings.TrimSpace(args[i]) == "--spec" {
				var spec struct {
					Name string `json:"name"`
				}
				if err := json.Unmarshal([]byte(args[i+1]), &spec); err == nil {
					appName = strings.TrimSpace(spec.Name)
				}
				break
			}
		}
	}
	if appName == "" {
		return
	}
	var entries []map[string]any
	if err := json.Unmarshal([]byte(output), &entries); err != nil {
		return
	}
	for _, entry := range entries {
		name := doJSONStringValue(entry["name"])
		if name == "" {
			if spec, ok := entry["spec"].(map[string]any); ok {
				name = doJSONStringValue(spec["name"])
			}
		}
		if strings.TrimSpace(name) != appName {
			continue
		}
		blob, err := json.Marshal(entry)
		if err != nil {
			return
		}
		extractDOAppBindingsDirect([]string{"apps", "get"}, string(blob), bindings)
		return
	}
}

func extractLeadingJSONPayload(output string) string {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return ""
	}
	if json.Valid([]byte(trimmed)) {
		return trimmed
	}
	for idx, r := range trimmed {
		if r != '{' && r != '[' {
			continue
		}
		candidate := strings.TrimSpace(trimmed[idx:])
		if json.Valid([]byte(candidate)) {
			return candidate
		}
	}
	return ""
}

func firstNonEmptyDOJSONString(values ...any) string {
	for _, value := range values {
		if s := doJSONStringValue(value); s != "" {
			return s
		}
	}
	return ""
}

func recoverDOAppBindingsAfterCreate(ctx context.Context, bindings map[string]string, opts ExecOptions, w io.Writer) {
	if strings.TrimSpace(bindings["APP_URL"]) != "" || strings.TrimSpace(bindings["HTTPS_URL"]) != "" {
		return
	}
	appID := strings.TrimSpace(bindings["APP_ID"])
	if appID == "" {
		return
	}
	getArgs := []string{"apps", "get", appID, "--output", "json"}
	_, _ = fmt.Fprintf(w, "[maker] recovering app URL via: doctl %s\n", strings.Join(getArgs, " "))
	out, err := runDoctlCommandWithRetry(ctx, getArgs, opts, w)
	if err != nil {
		_, _ = fmt.Fprintf(w, "[maker] warning: app URL recovery failed: %v\n", err)
		return
	}
	extractDOAppBindingsDirect([]string{"apps", "get"}, out, bindings)
}

func ensureOpenClawDOHTTPSURL(ctx context.Context, bindings map[string]string, opts ExecOptions) error {
	if strings.TrimSpace(bindings["HTTPS_URL"]) != "" {
		return nil
	}
	appID := strings.TrimSpace(bindings["APP_ID"])
	if appID == "" {
		return fmt.Errorf("missing DigitalOcean APP_ID; cannot resolve managed HTTPS URL")
	}
	const maxAttempts = 24
	const retryDelay = 5 * time.Second
	getArgs := []string{"apps", "get", appID, "--output", "json"}
	var lastErr error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if opts.Writer != nil {
			_, _ = fmt.Fprintf(opts.Writer, "[openclaw] resolving managed HTTPS URL for app %s (attempt %d/%d)\n", appID, attempt, maxAttempts)
		}
		out, err := runDoctlCommandWithRetry(ctx, getArgs, opts, io.Discard)
		if err != nil {
			lastErr = err
		} else {
			extractDOAppBindingsDirect([]string{"apps", "get"}, out, bindings)
			computeDORuntimeBindings(bindings)
			if httpsURL := strings.TrimSpace(bindings["HTTPS_URL"]); httpsURL != "" {
				if opts.Writer != nil {
					_, _ = fmt.Fprintf(opts.Writer, "[openclaw] managed HTTPS URL resolved: %s\n", httpsURL)
				}
				return nil
			}
		}
		if attempt == maxAttempts {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(retryDelay):
		}
	}
	if lastErr != nil {
		return fmt.Errorf("timed out waiting for DigitalOcean managed HTTPS URL: %w", lastErr)
	}
	return fmt.Errorf("timed out waiting for DigitalOcean managed HTTPS URL")
}

func doJSONStringValue(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(x)
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strings.TrimSpace(strconv.FormatFloat(x, 'f', -1, 64))
	case json.Number:
		return strings.TrimSpace(x.String())
	case bool:
		if x {
			return "true"
		}
		return "false"
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", v))
	}
}

// computeDORuntimeBindings infers computed bindings that aren't in API output.
func computeDORuntimeBindings(bindings map[string]string) {
	// REGISTRY_ENDPOINT = registry.digitalocean.com/<REGISTRY_NAME>
	if name := strings.TrimSpace(bindings["REGISTRY_NAME"]); name != "" {
		if strings.TrimSpace(bindings["REGISTRY_ENDPOINT"]) == "" {
			bindings["REGISTRY_ENDPOINT"] = "registry.digitalocean.com/" + name
		}
	}
	if appURL := strings.TrimSpace(bindings["APP_URL"]); appURL != "" {
		if !strings.HasPrefix(appURL, "https://") && !strings.HasPrefix(appURL, "http://") {
			appURL = "https://" + appURL
		}
		bindings["APP_URL"] = appURL
		if strings.TrimSpace(bindings["HTTPS_URL"]) == "" {
			bindings["HTTPS_URL"] = appURL
		}
	}
}

func dockerEnvForExec(cmdArgs []string, opts ExecOptions) []string {
	env := os.Environ()
	if !dockerCommandNeedsIsolatedConfig(cmdArgs) {
		return env
	}
	return envWithDockerConfig(env, opts.DigitalOceanDockerConfigDir)
}

func doctlEnvForExec(cmdArgs []string, opts ExecOptions) []string {
	env := os.Environ()
	if !doctlCommandNeedsIsolatedConfig(cmdArgs) {
		return env
	}
	return envWithDockerConfig(env, opts.DigitalOceanDockerConfigDir)
}

func envWithDockerConfig(env []string, dockerConfigDir string) []string {
	if strings.TrimSpace(dockerConfigDir) == "" {
		return env
	}
	_ = ensureDockerConfigPluginDirs(dockerConfigDir)
	filtered := env[:0]
	for _, kv := range env {
		if strings.HasPrefix(kv, "DOCKER_CONFIG=") {
			continue
		}
		filtered = append(filtered, kv)
	}
	filtered = append(filtered, "DOCKER_CONFIG="+dockerConfigDir)
	return filtered
}

func dockerCommandNeedsIsolatedConfig(cmdArgs []string) bool {
	if len(cmdArgs) == 0 {
		return false
	}
	sub := strings.ToLower(strings.TrimSpace(cmdArgs[0]))
	return sub == "push" || sub == "login"
}

func doctlCommandNeedsIsolatedConfig(cmdArgs []string) bool {
	if len(cmdArgs) < 2 {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(cmdArgs[0]), "registry") && strings.EqualFold(strings.TrimSpace(cmdArgs[1]), "login")
}

func planNeedsDODockerAuthIsolation(plan *Plan) bool {
	if plan == nil {
		return false
	}
	for _, cmd := range plan.Commands {
		args := cmd.Args
		if len(args) >= 2 && strings.EqualFold(strings.TrimSpace(args[0]), "registry") && strings.EqualFold(strings.TrimSpace(args[1]), "login") {
			return true
		}
		if len(args) >= 2 && strings.EqualFold(strings.TrimSpace(args[0]), "docker") {
			sub := strings.ToLower(strings.TrimSpace(args[1]))
			if sub == "build" || sub == "push" || sub == "login" {
				return true
			}
		}
	}
	return false
}

func materializeDOAppSpecArg(args []string, w io.Writer) ([]string, func(), error) {
	if len(args) < 2 || !strings.EqualFold(strings.TrimSpace(args[0]), "apps") {
		return args, nil, nil
	}
	if !strings.EqualFold(strings.TrimSpace(args[1]), "create") && !strings.EqualFold(strings.TrimSpace(args[1]), "update") {
		return args, nil, nil
	}
	for i := 0; i < len(args); i++ {
		trimmed := strings.TrimSpace(args[i])
		var value string
		valueIndex := -1
		inlineEquals := false
		switch {
		case trimmed == "--spec":
			if i+1 >= len(args) {
				return args, nil, fmt.Errorf("DigitalOcean apps command is missing --spec value")
			}
			value = strings.TrimSpace(args[i+1])
			valueIndex = i + 1
		case strings.HasPrefix(trimmed, "--spec="):
			value = strings.TrimSpace(strings.TrimPrefix(trimmed, "--spec="))
			valueIndex = i
			inlineEquals = true
		default:
			continue
		}
		if value == "" {
			return args, nil, fmt.Errorf("DigitalOcean apps command is missing --spec value")
		}
		if looksLikeInlineDOAppSpec(value) {
			file, err := os.CreateTemp("", doAppSpecFilePrefix)
			if err != nil {
				return args, nil, fmt.Errorf("create temp App Platform spec: %w", err)
			}
			if _, err := file.WriteString(value); err != nil {
				file.Close()
				os.Remove(file.Name())
				return args, nil, fmt.Errorf("write temp App Platform spec: %w", err)
			}
			if err := file.Close(); err != nil {
				os.Remove(file.Name())
				return args, nil, fmt.Errorf("close temp App Platform spec: %w", err)
			}
			if inlineEquals {
				args[valueIndex] = "--spec=" + file.Name()
			} else {
				args[valueIndex] = file.Name()
			}
			if w != nil {
				_, _ = fmt.Fprintf(w, "[maker] materialized inline App Platform spec to %s\n", file.Name())
			}
			return args, func() { _ = os.Remove(file.Name()) }, nil
		}
	}
	return args, nil, nil
}

func looksLikeInlineDOAppSpec(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		return true
	}
	return strings.Contains(trimmed, "services:") || strings.Contains(trimmed, "name:") || strings.Contains(trimmed, "ingress:")
}

func prepareSpecialDockerBuildContext(args []string, w io.Writer) (string, func(), bool, error) {
	if len(args) == 0 {
		return "", nil, false, nil
	}
	contextArg := strings.TrimSpace(args[len(args)-1])
	if contextArg != doOpenClawProxyBuildContext {
		return "", nil, false, nil
	}
	dir, err := os.MkdirTemp("", "clanker-openclaw-do-proxy-*")
	if err != nil {
		return "", nil, false, fmt.Errorf("create temp DigitalOcean proxy build context: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(doOpenClawProxyDockerfile), 0644); err != nil {
		cleanup()
		return "", nil, false, fmt.Errorf("write proxy Dockerfile: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(doOpenClawProxyMainGo), 0644); err != nil {
		cleanup()
		return "", nil, false, fmt.Errorf("write proxy main.go: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(doOpenClawProxyGoMod), 0644); err != nil {
		cleanup()
		return "", nil, false, fmt.Errorf("write proxy go.mod: %w", err)
	}
	if w != nil {
		_, _ = fmt.Fprintf(w, "[maker] prepared OpenClaw DigitalOcean proxy build context: %s\n", dir)
	}
	return dir, cleanup, true, nil
}

func rewriteDockerBuildArgsForMaterializedContext(args []string) []string {
	if len(args) == 0 {
		return args
	}
	rewritten := append([]string{}, args...)
	rewritten[len(rewritten)-1] = "."
	return rewritten
}

func ensureDOProxyBuildPlatform(args []string) []string {
	if len(args) == 0 {
		return args
	}
	if !strings.EqualFold(strings.TrimSpace(args[0]), "build") {
		return args
	}
	if strings.TrimSpace(args[len(args)-1]) != doOpenClawProxyBuildContext {
		return args
	}
	hasPlatform := false
	hasNoCache := false
	for i := 0; i < len(args); i++ {
		trimmed := strings.TrimSpace(args[i])
		if trimmed == "--platform" || strings.HasPrefix(trimmed, "--platform=") {
			hasPlatform = true
		}
		if trimmed == "--no-cache" {
			hasNoCache = true
		}
	}
	updated := make([]string, 0, len(args)+4)
	updated = append(updated, args[0])
	if !hasPlatform {
		updated = append(updated, "--platform", doOpenClawProxyPlatform)
	}
	if !hasNoCache {
		updated = append(updated, "--no-cache")
	}
	updated = append(updated, args[1:]...)
	return updated
}

func detectDOSSHPrivateKeyPath(args []string) string {
	if len(args) < 3 {
		return ""
	}
	if !strings.EqualFold(strings.TrimSpace(args[0]), "compute") || !strings.EqualFold(strings.TrimSpace(args[1]), "ssh-key") || !strings.EqualFold(strings.TrimSpace(args[2]), "import") {
		return ""
	}
	for i := 0; i < len(args); i++ {
		if strings.TrimSpace(args[i]) != "--public-key-file" || i+1 >= len(args) {
			continue
		}
		pubPath := expandHomePath(strings.TrimSpace(args[i+1]))
		return strings.TrimSuffix(pubPath, ".pub")
	}
	return ""
}

func maybePatchOpenClawDOHTTPSOriginOverSSH(ctx context.Context, bindings map[string]string, sshPrivateKeyPath string, opts ExecOptions) error {
	httpsURL := strings.TrimSpace(bindings["HTTPS_URL"])
	dropletIP := strings.TrimSpace(bindings["DROPLET_IP"])
	if httpsURL == "" || dropletIP == "" {
		return nil
	}
	if sshPrivateKeyPath == "" {
		sshPrivateKeyPath = strings.TrimSpace(bindings["SSH_PRIVATE_KEY_FILE"])
	}
	if sshPrivateKeyPath == "" {
		return fmt.Errorf("missing SSH private key path")
	}
	if _, err := os.Stat(sshPrivateKeyPath); err != nil {
		return fmt.Errorf("ssh private key unavailable: %w", err)
	}
	origin := strings.TrimRight(httpsURL, "/")
	port := openclaw.DefaultPort
	if rawPort := strings.TrimSpace(bindings["APP_PORT"]); rawPort != "" {
		if parsedPort, err := strconv.Atoi(rawPort); err == nil && parsedPort > 0 {
			port = parsedPort
		}
	}
	allowedOriginsJSON, err := json.Marshal([]string{
		fmt.Sprintf("http://localhost:%d", port),
		fmt.Sprintf("http://127.0.0.1:%d", port),
		origin,
	})
	if err != nil {
		return fmt.Errorf("marshal OpenClaw allowed origins: %w", err)
	}
	script := strings.Join([]string{
		"set -euo pipefail",
		fmt.Sprintf("export OPENCLAW_ORIGIN=%s", shellSingleQuoteDO(origin)),
		fmt.Sprintf("export OPENCLAW_ALLOWED_ORIGINS=%s", shellSingleQuoteDO(string(allowedOriginsJSON))),
		"cd /opt/openclaw",
		`docker compose run --rm openclaw-cli config set gateway.mode local >/dev/null`,
		`docker compose run --rm -e OPENCLAW_ALLOWED_ORIGINS="$OPENCLAW_ALLOWED_ORIGINS" openclaw-cli config set gateway.controlUi.allowedOrigins "$OPENCLAW_ALLOWED_ORIGINS" --strict-json >/dev/null`,
		`grep -F "$OPENCLAW_ORIGIN" /opt/openclaw/data/openclaw.json >/dev/null`,
		`for _w in 1 2 3 4 5 6 7 8 9 10 11 12; do if docker compose ps --status running openclaw-gateway >/dev/null 2>&1; then exit 0; fi; sleep 5; done`,
		`echo "[openclaw] allowedOrigins patch did not converge; recent gateway logs:"`,
		`docker compose logs --tail=60 openclaw-gateway || true`,
		`exit 1`,
	}, "\n")
	out, err := runDOSSHScript(ctx, dropletIP, sshPrivateKeyPath, script)
	if opts.Writer != nil {
		_, _ = fmt.Fprintf(opts.Writer, "[openclaw] patching DigitalOcean allowedOrigins with managed HTTPS URL %s\n", origin)
		if strings.TrimSpace(out) != "" {
			_, _ = io.WriteString(opts.Writer, out)
			if !strings.HasSuffix(out, "\n") {
				_, _ = io.WriteString(opts.Writer, "\n")
			}
		}
	}
	return err
}

func maybePrepareOpenClawDORuntimeOverSSH(ctx context.Context, bindings map[string]string, sshPrivateKeyPath string, opts ExecOptions) error {
	dropletIP := strings.TrimSpace(bindings["DROPLET_IP"])
	if dropletIP == "" {
		return nil
	}
	if sshPrivateKeyPath == "" {
		sshPrivateKeyPath = strings.TrimSpace(bindings["SSH_PRIVATE_KEY_FILE"])
	}
	if sshPrivateKeyPath == "" {
		return fmt.Errorf("missing SSH private key path")
	}
	if _, err := os.Stat(sshPrivateKeyPath); err != nil {
		return fmt.Errorf("ssh private key unavailable: %w", err)
	}

	type envPair struct {
		key   string
		value string
	}
	envPairs := []envPair{
		{key: "OPENCLAW_CONFIG_DIR", value: "/opt/openclaw/data"},
		{key: "OPENCLAW_WORKSPACE_DIR", value: "/opt/openclaw/workspace"},
		{key: "OPENCLAW_GATEWAY_PORT", value: strconv.Itoa(openclaw.DefaultPort)},
		{key: "OPENCLAW_BRIDGE_PORT", value: "18790"},
		{key: "OPENCLAW_GATEWAY_BIND", value: "lan"},
		{key: "OPENCLAW_IMAGE", value: "ghcr.io/openclaw/openclaw:latest"},
	}
	for _, key := range []string{"OPENCLAW_GATEWAY_TOKEN", "OPENCLAW_GATEWAY_PASSWORD", "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY", "DISCORD_BOT_TOKEN", "TELEGRAM_BOT_TOKEN"} {
		if value := strings.TrimSpace(bindings[key]); value != "" {
			envPairs = append(envPairs, envPair{key: key, value: value})
		}
	}
	for _, key := range requestedOpenClawDOPassThroughEnvKeys(bindings) {
		value := strings.TrimSpace(bindings[key])
		if value == "" {
			value = strings.TrimSpace(bindings["ENV_"+key])
		}
		if value == "" {
			continue
		}
		envPairs = append(envPairs, envPair{key: key, value: value})
	}
	providerAuthJSON := openClawDOAuthProfilesJSON(bindings)

	scriptLines := []string{
		"set -euo pipefail",
		"for _w in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 22 23 24; do if [ -d /opt/openclaw ]; then break; fi; sleep 5; done",
		"mkdir -p /opt/openclaw /opt/openclaw/data /opt/openclaw/workspace /opt/openclaw/data/identity /opt/openclaw/data/agents/main/agent /opt/openclaw/data/agents/main/sessions /opt/openclaw/data/canvas /opt/openclaw/data/cron",
		"touch /opt/openclaw/.env",
		"upsert_env() { key=\"$1\"; value=\"$2\"; tmp=$(mktemp); awk -F= -v k=\"$key\" -v v=\"$value\" 'BEGIN{done=0} $1==k{print k\"=\"v; done=1; next} {print} END{if(!done) print k\"=\"v}' /opt/openclaw/.env > \"$tmp\"; mv \"$tmp\" /opt/openclaw/.env; }",
	}
	for _, pair := range envPairs {
		scriptLines = append(scriptLines,
			fmt.Sprintf("export %s=%s", pair.key, shellSingleQuoteDO(pair.value)),
			fmt.Sprintf("upsert_env %s \"$%s\"", shellSingleQuoteDO(pair.key), pair.key),
		)
	}
	scriptLines = append(scriptLines,
		"chmod 600 /opt/openclaw/.env",
	)
	if providerAuthJSON != "" {
		scriptLines = append(scriptLines,
			fmt.Sprintf("export OPENCLAW_AUTH_PROFILES_JSON=%s", shellSingleQuoteDO(providerAuthJSON)),
			"printf '%s\n' \"$OPENCLAW_AUTH_PROFILES_JSON\" > /opt/openclaw/data/agents/main/agent/auth-profiles.json",
			"chmod 600 /opt/openclaw/data/agents/main/agent/auth-profiles.json",
		)
	}
	scriptLines = append(scriptLines,
		"chown -R 1000:1000 /opt/openclaw/data /opt/openclaw/workspace || true",
		"cd /opt/openclaw",
		"docker compose up -d --force-recreate openclaw-gateway >/dev/null",
		"docker compose ps || true",
	)
	script := strings.Join(scriptLines, "\n")
	out, err := runDOSSHScript(ctx, dropletIP, sshPrivateKeyPath, script)
	if opts.Writer != nil {
		_, _ = fmt.Fprintf(opts.Writer, "[openclaw] preparing DigitalOcean droplet runtime before gateway wait\n")
		if strings.TrimSpace(out) != "" {
			_, _ = io.WriteString(opts.Writer, out)
			if !strings.HasSuffix(out, "\n") {
				_, _ = io.WriteString(opts.Writer, "\n")
			}
		}
	}
	return err
}

func openClawDOAuthProfilesJSON(bindings map[string]string) string {
	type providerProfile struct {
		provider string
		envKey   string
	}
	providers := make([]providerProfile, 0, 3)
	if strings.TrimSpace(bindings["ANTHROPIC_API_KEY"]) != "" {
		providers = append(providers, providerProfile{provider: "anthropic", envKey: "ANTHROPIC_API_KEY"})
	}
	if strings.TrimSpace(bindings["OPENAI_API_KEY"]) != "" {
		providers = append(providers, providerProfile{provider: "openai", envKey: "OPENAI_API_KEY"})
	}
	if strings.TrimSpace(bindings["GEMINI_API_KEY"]) != "" {
		providers = append(providers, providerProfile{provider: "gemini", envKey: "GEMINI_API_KEY"})
	}
	if len(providers) == 0 {
		return ""
	}
	order := make(map[string][]string, len(providers))
	profiles := make(map[string]any, len(providers))
	for _, provider := range providers {
		profileID := provider.provider + ":default"
		order[provider.provider] = []string{profileID}
		profiles[profileID] = map[string]any{
			"type":     "api_key",
			"provider": provider.provider,
			"keyRef": map[string]any{
				"source":   "env",
				"provider": "default",
				"id":       provider.envKey,
			},
		}
	}
	payload := map[string]any{
		"version":  1,
		"order":    order,
		"profiles": profiles,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func maybeEnforceOpenClawDOSecretOverSSH(ctx context.Context, bindings map[string]string, sshPrivateKeyPath string, opts ExecOptions) error {
	dropletIP := strings.TrimSpace(bindings["DROPLET_IP"])
	if dropletIP == "" {
		return nil
	}
	if sshPrivateKeyPath == "" {
		sshPrivateKeyPath = strings.TrimSpace(bindings["SSH_PRIVATE_KEY_FILE"])
	}
	if sshPrivateKeyPath == "" {
		return fmt.Errorf("missing SSH private key path")
	}
	if _, err := os.Stat(sshPrivateKeyPath); err != nil {
		return fmt.Errorf("ssh private key unavailable: %w", err)
	}

	secretKey := ""
	secretValue := ""
	for _, candidate := range []string{"OPENCLAW_GATEWAY_TOKEN", "OPENCLAW_GATEWAY_PASSWORD"} {
		if value := strings.TrimSpace(bindings[candidate]); value != "" {
			secretKey = candidate
			secretValue = value
			break
		}
	}
	if secretKey == "" || secretValue == "" {
		return nil
	}

	script := strings.Join([]string{
		"set -euo pipefail",
		fmt.Sprintf("export OPENCLAW_SECRET_KEY=%s", shellSingleQuoteDO(secretKey)),
		fmt.Sprintf("export OPENCLAW_SECRET_VALUE=%s", shellSingleQuoteDO(secretValue)),
		"cd /opt/openclaw",
		"mkdir -p /opt/openclaw",
		"touch /opt/openclaw/.env",
		`CURRENT_ENV=$(awk -F= -v k="$OPENCLAW_SECRET_KEY" '$1==k {sub(/^[^=]*=/, ""); print; exit}' /opt/openclaw/.env || true)`,
		`CURRENT_ENV_HASH=$(printf %s "$CURRENT_ENV" | sha256sum | awk '{print substr($1,1,8)}')`,
		`EXPECTED_HASH=$(printf %s "$OPENCLAW_SECRET_VALUE" | sha256sum | awk '{print substr($1,1,8)}')`,
		`if [ "$CURRENT_ENV_HASH" != "$EXPECTED_HASH" ]; then echo "[openclaw] droplet .env secret hash mismatch: current=${CURRENT_ENV_HASH:-missing} expected=$EXPECTED_HASH; rewriting"; else echo "[openclaw] droplet .env secret already matches requested value (hash=$EXPECTED_HASH)"; fi`,
		`awk -F= -v k="$OPENCLAW_SECRET_KEY" -v v="$OPENCLAW_SECRET_VALUE" 'BEGIN{done=0} $1==k{print k"="v; done=1; next} {print} END{if(!done) print k"="v}' /opt/openclaw/.env > /opt/openclaw/.env.clanker.tmp`,
		"mv /opt/openclaw/.env.clanker.tmp /opt/openclaw/.env",
		"chmod 600 /opt/openclaw/.env",
		"docker compose up -d --force-recreate openclaw-gateway >/dev/null",
		`ACTUAL_CONTAINER=$(docker compose exec -T openclaw-gateway env 2>/dev/null | awk -F= -v k="$OPENCLAW_SECRET_KEY" '$1==k {sub(/^[^=]*=/, ""); print; exit}' || true)`,
		`ACTUAL_HASH=$(printf %s "$ACTUAL_CONTAINER" | sha256sum | awk '{print substr($1,1,8)}')`,
		`if [ "$ACTUAL_HASH" != "$EXPECTED_HASH" ]; then echo "[openclaw] warning: running container secret hash differs after recreate: actual=${ACTUAL_HASH:-missing} expected=$EXPECTED_HASH"; exit 1; fi`,
		`echo "[openclaw] enforced requested gateway secret on droplet and running container (hash=$EXPECTED_HASH)"`,
	}, "\n")
	out, err := runDOSSHScript(ctx, dropletIP, sshPrivateKeyPath, script)
	if opts.Writer != nil && strings.TrimSpace(out) != "" {
		_, _ = io.WriteString(opts.Writer, out)
		if !strings.HasSuffix(out, "\n") {
			_, _ = io.WriteString(opts.Writer, "\n")
		}
	}
	return err
}

func maybeStartOpenClawDOPairApproveWindowOverSSH(ctx context.Context, bindings map[string]string, sshPrivateKeyPath string, opts ExecOptions) error {
	dropletIP := strings.TrimSpace(bindings["DROPLET_IP"])
	if dropletIP == "" {
		return nil
	}
	if sshPrivateKeyPath == "" {
		sshPrivateKeyPath = strings.TrimSpace(bindings["SSH_PRIVATE_KEY_FILE"])
	}
	if sshPrivateKeyPath == "" {
		return fmt.Errorf("missing SSH private key path")
	}
	if _, err := os.Stat(sshPrivateKeyPath); err != nil {
		return fmt.Errorf("ssh private key unavailable: %w", err)
	}

	script := strings.Join([]string{
		"set -euo pipefail",
		"cd /opt/openclaw",
		"mkdir -p /var/log",
		`PAIR_SCRIPT=/tmp/clanker-openclaw-do-pair-window.sh`,
		`PAIR_PID_FILE=/var/run/clanker-openclaw-auto-pair.pid`,
		`cat > "$PAIR_SCRIPT" <<'SCRIPT'`,
		`#!/bin/bash
set +e
echo "[openclaw] auto-pair approval window started for 20 minutes"
for _w in 1 2 3 4 5 6 7 8 9 10 11 12; do
	if docker compose ps --status running openclaw-gateway >/dev/null 2>&1; then
		break
	fi
	sleep 5
done
END=$(( $(date +%s) + 1200 ))
while [ $(date +%s) -lt $END ]; do
	if ! docker compose ps --status running openclaw-gateway >/dev/null 2>&1; then
		sleep 5
		continue
	fi
	JS=$(cat <<'JS'
const fs = require("fs");
const path = require("path");
const pendingPath = "/home/node/.openclaw/devices/pending.json";
const pairedPath = "/home/node/.openclaw/devices/paired.json";

function readJSON(filePath) {
	try {
		return JSON.parse(fs.readFileSync(filePath, "utf8") || "{}");
	} catch (error) {
		return {};
	}
}

const pending = readJSON(pendingPath);
const paired = readJSON(pairedPath);
const requestIds = Object.keys(pending || {});

if (requestIds.length === 0) {
	console.log("DEPLOY_PAIR_NONE");
	process.exit(0);
}

let approved = 0;
for (const requestId of requestIds) {
	const request = pending[requestId];
	if (!request || !request.deviceId) continue;
	paired[String(request.deviceId)] = request;
	delete pending[requestId];
	approved++;
}

fs.mkdirSync(path.dirname(pendingPath), { recursive: true });
fs.writeFileSync(pairedPath, JSON.stringify(paired, null, 2));
fs.writeFileSync(pendingPath, JSON.stringify(pending, null, 2));
console.log("DEPLOY_PAIR_APPROVED=" + approved);
JS
)
	OUT=$(docker compose exec -T openclaw-gateway node -e "$JS" 2>/dev/null || true)
	APPROVED=$(echo "$OUT" | sed -n 's/^.*DEPLOY_PAIR_APPROVED=\([0-9][0-9]*\).*$/\1/p')
	if [ -n "$APPROVED" ] && [ "$APPROVED" -gt 0 ] 2>/dev/null; then
		echo "[openclaw] approved $APPROVED pending pair request(s)"
		docker compose restart openclaw-gateway >/dev/null 2>&1 || true
		sleep 2
	fi
	sleep 3
done
echo "[openclaw] auto-pair approval window finished"`,
		`SCRIPT`,
		`chmod +x "$PAIR_SCRIPT"`,
		`if [ -f "$PAIR_PID_FILE" ]; then OLD_PID=$(cat "$PAIR_PID_FILE" 2>/dev/null || true); if [ -n "$OLD_PID" ] && kill -0 "$OLD_PID" >/dev/null 2>&1; then kill "$OLD_PID" >/dev/null 2>&1 || true; fi; rm -f "$PAIR_PID_FILE"; fi`,
		`nohup "$PAIR_SCRIPT" >/var/log/openclaw-auto-pair.log 2>&1 </dev/null & PAIR_PID=$!`,
		`echo "$PAIR_PID" > "$PAIR_PID_FILE"`,
		`disown "$PAIR_PID" >/dev/null 2>&1 || true`,
		`echo "$PAIR_PID"`,
	}, "\n")
	out, err := runDOSSHScript(ctx, dropletIP, sshPrivateKeyPath, script)
	if opts.Writer != nil {
		_, _ = fmt.Fprintf(opts.Writer, "[openclaw] starting DigitalOcean auto-pair approval window for 20 minutes\n")
		if strings.TrimSpace(out) != "" {
			_, _ = io.WriteString(opts.Writer, out)
			if !strings.HasSuffix(out, "\n") {
				_, _ = io.WriteString(opts.Writer, "\n")
			}
		}
	}
	return err
}

func runDOSSHScript(ctx context.Context, host, privateKeyPath, script string) (string, error) {
	keyBytes, err := os.ReadFile(privateKeyPath)
	if err != nil {
		return "", fmt.Errorf("read ssh private key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(keyBytes)
	if err != nil {
		return "", fmt.Errorf("parse ssh private key: %w", err)
	}
	config := &ssh.ClientConfig{
		User:            "root",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
	}
	dialer := net.Dialer{Timeout: 30 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, "22"))
	if err != nil {
		return "", fmt.Errorf("connect ssh: %w", err)
	}
	defer conn.Close()
	clientConn, chans, reqs, err := ssh.NewClientConn(conn, net.JoinHostPort(host, "22"), config)
	if err != nil {
		return "", fmt.Errorf("establish ssh client: %w", err)
	}
	client := ssh.NewClient(clientConn, chans, reqs)
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("create ssh session: %w", err)
	}
	defer session.Close()
	var buf bytes.Buffer
	session.Stdout = &buf
	session.Stderr = &buf
	command := "bash -lc " + shellSingleQuoteDO(script)
	if err := session.Run(command); err != nil {
		return buf.String(), fmt.Errorf("run remote script: %w", err)
	}
	return buf.String(), nil
}

func shellSingleQuoteDO(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

const doOpenClawProxyGoMod = `module clanker/openclawdoproxy

go 1.24
`

const doOpenClawProxyDockerfile = `FROM golang:1.24 AS build
WORKDIR /src
COPY go.mod go.mod
COPY main.go main.go
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/proxy ./main.go

FROM scratch
COPY --from=build /out/proxy /proxy
EXPOSE 8080
ENTRYPOINT ["/proxy"]
`

const doOpenClawProxyMainGo = `package main

import (
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
)

func main() {
	targetRaw := strings.TrimSpace(os.Getenv("UPSTREAM_URL"))
	if targetRaw == "" {
		log.Fatal("UPSTREAM_URL is required")
	}
	target, err := url.Parse(targetRaw)
	if err != nil {
		log.Fatalf("parse UPSTREAM_URL: %v", err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	proxy.Director = func(r *http.Request) {
		originalDirector(r)
		r.Header.Set("X-Forwarded-Host", r.Host)
		r.Header.Set("X-Forwarded-Proto", "https")
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok\n"))
			return
		}
		proxy.ServeHTTP(w, r)
	})
	log.Fatal(http.ListenAndServe(":8080", handler))
}
`

// learnDockerProducesLiteral sets produces bindings from docker command args.
// Docker build/push output is NOT JSON so learnPlanBindingsFromProduces won't work.
// Instead, we extract the image tag from the -t flag and set it as a literal binding.
func learnDockerProducesLiteral(args []string, produces map[string]string, bindings map[string]string) {
	// Find the -t tag value from docker build args
	tag := ""
	for i, a := range args {
		if (a == "-t" || a == "--tag") && i+1 < len(args) {
			tag = strings.TrimSpace(args[i+1])
			break
		}
	}
	if tag == "" {
		return
	}

	// Set any IMAGE-related produce to the tag value
	for k := range produces {
		upper := strings.ToUpper(strings.TrimSpace(k))
		if strings.Contains(upper, "IMAGE") {
			if strings.TrimSpace(bindings[k]) == "" {
				bindings[k] = tag
			}
		}
	}

	// Always set IMAGE_URI and IMAGE_TAG as fallback (even without produces)
	// so downstream commands referencing these placeholders work
	if strings.TrimSpace(bindings["IMAGE_URI"]) == "" {
		bindings["IMAGE_URI"] = tag
	}
	if strings.TrimSpace(bindings["IMAGE_TAG"]) == "" {
		bindings["IMAGE_TAG"] = tag
	}
}

func validateDOUserDataAtExec(args []string) error {
	if len(args) < 3 {
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(args[0]), "compute") ||
		!strings.EqualFold(strings.TrimSpace(args[1]), "droplet") ||
		!strings.EqualFold(strings.TrimSpace(args[2]), "create") {
		return nil
	}

	script := extractDOUserDataArg(args)
	if strings.TrimSpace(script) == "" {
		return nil
	}
	lower := strings.ToLower(script)
	isOpenClaw := strings.Contains(lower, "openclaw") || strings.Contains(lower, "docker-setup.sh")
	if !isOpenClaw {
		return nil
	}
	if strings.Contains(lower, "cloud-init status --wait") {
		return fmt.Errorf("OpenClaw user-data must not run 'cloud-init status --wait' inside cloud-init")
	}
	if strings.Contains(lower, "docker compose build") || strings.Contains(lower, "docker-compose build") {
		return fmt.Errorf("OpenClaw user-data must use OPENCLAW_IMAGE=ghcr.io/openclaw/openclaw:latest plus 'docker compose pull openclaw-gateway' instead of 'docker compose build'")
	}
	if strings.Contains(lower, "docker build -t openclaw:local") || strings.Contains(lower, "openclaw_image=openclaw:local") {
		return fmt.Errorf("OpenClaw user-data must use the upstream GHCR image ghcr.io/openclaw/openclaw:latest instead of a local openclaw:local build")
	}
	if !strings.Contains(lower, "openclaw_image=ghcr.io/openclaw/openclaw:latest") {
		return fmt.Errorf("OpenClaw user-data must pin OPENCLAW_IMAGE=ghcr.io/openclaw/openclaw:latest in .env")
	}
	if !strings.Contains(lower, "openclaw_gateway_token") && !strings.Contains(lower, "openclaw_gateway_password") {
		return fmt.Errorf("OpenClaw user-data is missing OPENCLAW_GATEWAY_TOKEN or OPENCLAW_GATEWAY_PASSWORD in .env")
	}
	if strings.Contains(lower, "digitalocean_access_token=") {
		return fmt.Errorf("OpenClaw user-data must not write DIGITALOCEAN_ACCESS_TOKEN into .env")
	}
	cloneSoftFailRe := regexp.MustCompile(`(?im)^\s*git\s+clone[^\n]*\s*\|\|[^\n]*$`)
	if cloneSoftFailRe.MatchString(script) {
		return fmt.Errorf("OpenClaw user-data must not ignore git clone failure with a shell fallback ('|| ...')")
	}
	setupSoftFailRe := regexp.MustCompile(`(?im)^\s*\./docker-setup\.sh[^\n]*\s*\|\|[^\n]*$`)
	if setupSoftFailRe.MatchString(script) {
		return fmt.Errorf("OpenClaw user-data must not ignore docker-setup.sh failure with a shell fallback ('|| ...')")
	}
	if strings.Contains(lower, "docker compose up -d openclaw-gateway --wait") || strings.Contains(lower, "docker-compose up -d openclaw-gateway --wait") || strings.Contains(lower, "docker compose up -d openclaw-gateway --output") || strings.Contains(lower, "docker-compose up -d openclaw-gateway --output") {
		return fmt.Errorf("OpenClaw user-data must not include outer doctl flags like --wait/--output on the docker compose up line")
	}
	if strings.Contains(lower, "placeholder_replace_me") || strings.Contains(lower, "changeme") || strings.Contains(lower, "replace_me") {
		return fmt.Errorf("OpenClaw user-data contains dummy secret values like placeholder_replace_me/changeme")
	}
	if strings.Contains(lower, "openssl rand") || strings.Contains(lower, "gateway_token=$(") || strings.Contains(lower, "openclaw_gateway_token=${gateway_token}") || strings.Contains(lower, "openclaw_gateway_token=$gateway_token") {
		return fmt.Errorf("OpenClaw user-data must use the provided gateway token, not generate a random one in user-data")
	}
	if !strings.Contains(lower, "anthropic_api_key") && !strings.Contains(lower, "openai_api_key") && !strings.Contains(lower, "gemini_api_key") {
		return fmt.Errorf("OpenClaw user-data is missing all AI provider keys in .env")
	}
	return nil
}

func ensureDOSSHImportKeyMaterial(args []string, material *doDeploySSHKeyMaterial, w io.Writer) ([]string, error) {
	if len(args) < 3 {
		return args, nil
	}
	if !strings.EqualFold(strings.TrimSpace(args[0]), "compute") ||
		!strings.EqualFold(strings.TrimSpace(args[1]), "ssh-key") ||
		!strings.EqualFold(strings.TrimSpace(args[2]), "import") {
		return args, nil
	}
	if err := ensureDODeploySSHKeyMaterial(material, w); err != nil {
		return args, err
	}
	return rewriteDOSSHImportArgs(args, material.keyName, material.publicKeyPath), nil
}

func ensureDODeploySSHKeyMaterial(material *doDeploySSHKeyMaterial, w io.Writer) error {
	if material == nil {
		return fmt.Errorf("missing DigitalOcean SSH key material state")
	}
	if strings.TrimSpace(material.privateKeyPath) != "" && strings.TrimSpace(material.publicKeyPath) != "" && strings.TrimSpace(material.keyName) != "" {
		return nil
	}
	tmpDir, err := os.MkdirTemp("", "clanker-do-ssh-*")
	if err != nil {
		return fmt.Errorf("create DigitalOcean deploy SSH key directory: %w", err)
	}
	privateKeyPath := filepath.Join(tmpDir, "id_rsa")
	if err := generateLocalSSHKeyPair(privateKeyPath); err != nil {
		_ = os.RemoveAll(tmpDir)
		return fmt.Errorf("generate DigitalOcean SSH key pair: %w", err)
	}
	token, err := newDODeploySSHKeyToken()
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		return err
	}
	material.keyName = "clanker-do-" + token
	material.privateKeyPath = privateKeyPath
	material.publicKeyPath = privateKeyPath + ".pub"
	material.cleanup = func() {
		_ = os.RemoveAll(tmpDir)
	}
	if w != nil {
		_, _ = fmt.Fprintf(w, "[maker] generated fresh deploy-scoped SSH key pair for DigitalOcean import: %s\n", material.publicKeyPath)
	}
	return nil
}

func newDODeploySSHKeyToken() (string, error) {
	randBytes := make([]byte, 4)
	if _, err := rand.Read(randBytes); err != nil {
		return "", fmt.Errorf("generate DigitalOcean SSH key token: %w", err)
	}
	return time.Now().UTC().Format("20060102-150405") + fmt.Sprintf("-%x", randBytes), nil
}

func rewriteDOSSHImportArgs(args []string, keyName, publicKeyPath string) []string {
	trimmedName := strings.TrimSpace(keyName)
	trimmedPubPath := strings.TrimSpace(publicKeyPath)
	if trimmedName == "" || trimmedPubPath == "" {
		return args
	}
	trimmedPubPath = expandHomePath(trimmedPubPath)
	if filepath.Ext(trimmedPubPath) != ".pub" {
		trimmedPubPath += ".pub"
	}

	rewritten := []string{"compute", "ssh-key", "import", trimmedName}
	start := 3
	if len(args) > 3 && strings.HasPrefix(strings.TrimSpace(args[3]), "-") {
		start = 3
	} else if len(args) > 3 {
		start = 4
	}
	for i := start; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		if arg == "--public-key-file" {
			i++
			continue
		}
		if strings.HasPrefix(arg, "--public-key-file=") {
			continue
		}
		rewritten = append(rewritten, args[i])
	}
	rewritten = append(rewritten, "--public-key-file", trimmedPubPath)
	return rewritten
}

func generateLocalSSHKeyPair(privateKeyPath string) error {
	sshDir := filepath.Dir(privateKeyPath)
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		return err
	}
	privateKey, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return err
	}
	privateKeyFile, err := os.OpenFile(privateKeyPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer privateKeyFile.Close()
	privateKeyPEM := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)}
	if err := pem.Encode(privateKeyFile, privateKeyPEM); err != nil {
		return err
	}
	publicKey, err := ssh.NewPublicKey(&privateKey.PublicKey)
	if err != nil {
		return err
	}
	publicKeyPath := privateKeyPath + ".pub"
	if err := os.WriteFile(publicKeyPath, ssh.MarshalAuthorizedKey(publicKey), 0644); err != nil {
		return err
	}
	return nil
}

func expandHomePath(p string) string {
	if strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

func redactDOCommandArgsForLog(args []string) []string {
	if len(args) == 0 {
		return args
	}
	masked := append([]string(nil), args...)
	for i := 0; i < len(masked); i++ {
		trimmed := strings.TrimSpace(masked[i])
		if trimmed == "--user-data" && i+1 < len(masked) {
			masked[i+1] = "<redacted-user-data>"
			i++
			continue
		}
		if strings.HasPrefix(trimmed, "--user-data=") {
			masked[i] = "--user-data=<redacted-user-data>"
		}
	}
	return masked
}

func extractDOUserDataArg(args []string) string {
	for i := 0; i < len(args); i++ {
		trimmed := strings.TrimSpace(args[i])
		if trimmed == "--user-data" && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(trimmed, "--user-data=") {
			return strings.TrimPrefix(trimmed, "--user-data=")
		}
	}
	return ""
}

func postCheckDOCommand(args []string, output string) error {
	if len(args) < 3 {
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(args[0]), "compute") || !strings.EqualFold(strings.TrimSpace(args[1]), "firewall") {
		return nil
	}
	verb := strings.ToLower(strings.TrimSpace(args[2]))
	if verb != "create" && verb != "update" {
		return nil
	}
	expectedInbound := expectedFirewallPortsFromArgs(args, "--inbound-rules", "tcp")
	expectedOutbound := expectedFirewallAllProtocolsFromArgs(args)
	if len(expectedInbound) == 0 && len(expectedOutbound) == 0 {
		return nil
	}
	actual, err := parseDOFirewallOutput(output)
	if err != nil {
		return nil // don't fail if doctl changed output shape; exec success still stands
	}
	for port := range expectedInbound {
		if !actual.InboundTCPPorts[port] {
			return fmt.Errorf("firewall result is missing inbound TCP port %s after create/update", port)
		}
	}
	for proto := range expectedOutbound {
		if !actual.OutboundAllProtocols[proto] {
			return fmt.Errorf("firewall result is missing outbound %s all rule after create/update", proto)
		}
	}
	return nil
}

type doFirewallObserved struct {
	InboundTCPPorts      map[string]bool
	OutboundAllProtocols map[string]bool
}

type doFirewallOutput struct {
	InboundRules  []doFirewallRule `json:"inbound_rules"`
	OutboundRules []doFirewallRule `json:"outbound_rules"`
}

type doFirewallRule struct {
	Protocol string `json:"protocol"`
	Ports    string `json:"ports"`
	Sources  struct {
		Addresses []string `json:"addresses"`
	} `json:"sources"`
	Destinations struct {
		Addresses []string `json:"addresses"`
	} `json:"destinations"`
}

func parseDOFirewallOutput(output string) (doFirewallObserved, error) {
	obs := doFirewallObserved{
		InboundTCPPorts:      map[string]bool{},
		OutboundAllProtocols: map[string]bool{},
	}
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return obs, fmt.Errorf("empty output")
	}
	var single doFirewallOutput
	if err := json.Unmarshal([]byte(trimmed), &single); err == nil && (len(single.InboundRules) > 0 || len(single.OutboundRules) > 0) {
		return observeDOFirewall(single), nil
	}
	var arr []doFirewallOutput
	if err := json.Unmarshal([]byte(trimmed), &arr); err == nil && len(arr) > 0 {
		return observeDOFirewall(arr[0]), nil
	}
	return obs, fmt.Errorf("unrecognized firewall output")
}

func observeDOFirewall(fw doFirewallOutput) doFirewallObserved {
	obs := doFirewallObserved{
		InboundTCPPorts:      map[string]bool{},
		OutboundAllProtocols: map[string]bool{},
	}
	for _, rule := range fw.InboundRules {
		if strings.EqualFold(strings.TrimSpace(rule.Protocol), "tcp") {
			if port := strings.TrimSpace(rule.Ports); port != "" {
				obs.InboundTCPPorts[port] = true
			}
		}
	}
	for _, rule := range fw.OutboundRules {
		proto := strings.ToLower(strings.TrimSpace(rule.Protocol))
		ports := strings.ToLower(strings.TrimSpace(rule.Ports))
		if proto != "" && (ports == "all" || ports == "0") {
			obs.OutboundAllProtocols[proto] = true
		}
	}
	return obs
}

func expectedFirewallPortsFromArgs(args []string, flagName string, protocol string) map[string]bool {
	out := map[string]bool{}
	for _, value := range extractFirewallRuleValuesAtExec(args, flagName) {
		for _, rule := range strings.Fields(value) {
			parts := parseFirewallRuleAtExec(rule)
			if !strings.EqualFold(parts["protocol"], protocol) {
				continue
			}
			if port := strings.TrimSpace(parts["ports"]); port != "" {
				out[port] = true
			}
		}
	}
	return out
}

func expectedFirewallAllProtocolsFromArgs(args []string) map[string]bool {
	out := map[string]bool{}
	for _, value := range extractFirewallRuleValuesAtExec(args, "--outbound-rules") {
		for _, rule := range strings.Fields(value) {
			parts := parseFirewallRuleAtExec(rule)
			proto := strings.ToLower(strings.TrimSpace(parts["protocol"]))
			ports := strings.ToLower(strings.TrimSpace(parts["ports"]))
			if proto != "" && (ports == "all" || ports == "0") {
				out[proto] = true
			}
		}
	}
	return out
}

func extractFirewallRuleValuesAtExec(args []string, flagName string) []string {
	var values []string
	for i := 0; i < len(args); i++ {
		trimmed := strings.TrimSpace(args[i])
		if trimmed == flagName && i+1 < len(args) {
			values = append(values, strings.TrimSpace(args[i+1]))
			i++
			continue
		}
		if strings.HasPrefix(trimmed, flagName+"=") {
			values = append(values, strings.TrimSpace(strings.TrimPrefix(trimmed, flagName+"=")))
		}
	}
	return values
}

func parseFirewallRuleAtExec(rule string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(rule, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), ":")
		if !ok {
			continue
		}
		out[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
	}
	return out
}

// emptyFWAddrExecRe matches "address:" followed by whitespace or end-of-string.
var emptyFWAddrExecRe = regexp.MustCompile(`address:(\s|$)`)

// fixFirewallEmptyAddressAtExec fixes "address:" with no CIDR right before doctl exec.
// Belt-and-suspenders: autofix does this during planning, but LLM repair can reintroduce it.
func fixFirewallEmptyAddressAtExec(args []string) []string {
	if len(args) < 3 {
		return args
	}
	s0 := strings.ToLower(strings.TrimSpace(args[0]))
	s1 := strings.ToLower(strings.TrimSpace(args[1]))
	if s0 != "compute" || s1 != "firewall" {
		return args
	}
	for i, arg := range args {
		if strings.Contains(arg, "address:") {
			args[i] = emptyFWAddrExecRe.ReplaceAllString(arg, "address:0.0.0.0/0${1}")
		}
	}
	return args
}

func stripInvalidICMPPortsAtExec(args []string) []string {
	if len(args) < 3 {
		return args
	}
	s0 := strings.ToLower(strings.TrimSpace(args[0]))
	s1 := strings.ToLower(strings.TrimSpace(args[1]))
	if s0 != "compute" || s1 != "firewall" {
		return args
	}
	for i, arg := range args {
		trimmed := strings.TrimSpace(arg)
		if !strings.Contains(trimmed, "protocol:icmp") || !strings.Contains(trimmed, "ports:") {
			continue
		}
		trimmed = strings.ReplaceAll(trimmed, "protocol:icmp,ports:all,", "protocol:icmp,")
		trimmed = strings.ReplaceAll(trimmed, "protocol:icmp,ports:0,", "protocol:icmp,")
		trimmed = strings.ReplaceAll(trimmed, ",ports:all,address:", ",address:")
		trimmed = strings.ReplaceAll(trimmed, ",ports:0,address:", ",address:")
		trimmed = strings.ReplaceAll(trimmed, "protocol:icmp,ports:all", "protocol:icmp")
		trimmed = strings.ReplaceAll(trimmed, "protocol:icmp,ports:0", "protocol:icmp")
		args[i] = trimmed
	}
	return args
}

var doFirewallPortSpecRe = regexp.MustCompile(`^(?:all|0|\d+|\d+-\d+)$`)

func validateDOFirewallRulesAtExec(args []string) error {
	if len(args) < 3 {
		return nil
	}
	s0 := strings.ToLower(strings.TrimSpace(args[0]))
	s1 := strings.ToLower(strings.TrimSpace(args[1]))
	if s0 != "compute" || s1 != "firewall" {
		return nil
	}
	verb := strings.ToLower(strings.TrimSpace(args[2]))
	if verb != "create" && verb != "update" {
		return nil
	}
	for _, spec := range []struct {
		flag      string
		direction string
	}{
		{flag: "--inbound-rules", direction: "inbound"},
		{flag: "--outbound-rules", direction: "outbound"},
	} {
		for _, value := range extractFirewallRuleValuesAtExec(args, spec.flag) {
			for idx, rawRule := range strings.Fields(value) {
				parts := parseFirewallRuleAtExec(rawRule)
				if err := validateDOFirewallRuleAtExec(spec.direction, idx+1, rawRule, parts); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func validateDOFirewallRuleAtExec(direction string, index int, rawRule string, parts map[string]string) error {
	protocol := strings.ToLower(strings.TrimSpace(parts["protocol"]))
	ports := strings.ToLower(strings.TrimSpace(parts["ports"]))
	address := strings.TrimSpace(parts["address"])
	label := fmt.Sprintf("%s rule %d (%s)", direction, index, strings.TrimSpace(rawRule))

	if protocol == "" {
		return fmt.Errorf("firewall %s is missing protocol", label)
	}
	switch protocol {
	case "tcp", "udp":
		if ports == "" {
			return fmt.Errorf("firewall %s is missing ports", label)
		}
		if !doFirewallPortSpecRe.MatchString(ports) {
			return fmt.Errorf("firewall %s has invalid ports value %q", label, ports)
		}
	case "icmp":
		if ports != "" {
			return fmt.Errorf("firewall %s cannot set ports for icmp", label)
		}
	default:
		return fmt.Errorf("firewall %s has unsupported protocol %q", label, protocol)
	}

	if address == "" {
		return fmt.Errorf("firewall %s is missing address", label)
	}
	if _, _, err := net.ParseCIDR(address); err != nil {
		return fmt.Errorf("firewall %s has invalid CIDR %q", label, address)
	}
	return nil
}
