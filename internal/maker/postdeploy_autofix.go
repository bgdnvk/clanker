package maker

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

type postDeployFixConfig struct {
	Aggressive bool
}

func maybeAutoFixUnhealthyALBTargets(ctx context.Context, bindings map[string]string, opts ExecOptions, cfg postDeployFixConfig) error {
	if opts.Destroyer {
		return nil
	}
	if strings.TrimSpace(opts.Profile) == "" || strings.TrimSpace(opts.Region) == "" {
		return nil
	}

	instanceID := strings.TrimSpace(bindings["INSTANCE_ID"])
	tgARN := strings.TrimSpace(bindings["TG_ARN"])
	albDNS := strings.TrimSpace(bindings["ALB_DNS"])
	question := strings.TrimSpace(bindings["PLAN_QUESTION"])

	// Scope: EC2 + ALB one-click deployments.
	if instanceID == "" || tgARN == "" || albDNS == "" || question == "" {
		return nil
	}

	appPortRaw := strings.TrimSpace(bindings["APP_PORT"])
	if appPortRaw == "" {
		appPortRaw = "3000"
	}
	appPort, err := strconv.Atoi(appPortRaw)
	if err != nil || appPort < 1 || appPort > 65535 {
		return nil
	}

	// First wait: allow user-data to finish and container to start.
	if err := WaitForALBHealthy(ctx, tgARN, opts.Profile, opts.Region, opts.Writer, 2*time.Minute); err == nil {
		return nil
	}

	_, _ = fmt.Fprintf(opts.Writer, "[health] no healthy targets yet; running automatic runtime remediation via SSM...\n")

	image := strings.TrimSpace(bindings["IMAGE_URI"])
	if image == "" {
		ecrURI := strings.TrimSpace(bindings["ECR_URI"])
		if ecrURI != "" {
			tag := strings.TrimSpace(bindings["IMAGE_TAG"])
			if tag == "" {
				tag = "latest"
			}
			image = ecrURI + ":" + tag
		}
	}
	if image == "" {
		// Can't safely restart without a known image.
		return fmt.Errorf("auto-fix skipped: missing IMAGE_URI/ECR_URI")
	}

	accountID := strings.TrimSpace(bindings["ACCOUNT_ID"])
	if accountID == "" {
		accountID = strings.TrimSpace(bindings["AWS_ACCOUNT_ID"])
	}
	if parsed := strings.TrimSpace(extractAccountFromECR(image)); parsed != "" {
		accountID = parsed
	}

	lq := strings.ToLower(question)
	isOpenClaw := strings.Contains(lq, "openclaw") || strings.Contains(strings.ToLower(extractRepoURLFromQuestion(question)), "openclaw")

	diagOut, diagErr := runSSMShellScript(ctx, instanceID, opts.Profile, opts.Region, ssmDiagnosticCommands(appPort, opts.Region, accountID, image), opts.Writer)
	if diagErr != nil {
		return fmt.Errorf("auto-fix diagnostics failed: %w", diagErr)
	}
	_, _ = io.WriteString(opts.Writer, "[health][ssm] diagnostics output:\n"+diagOut+"\n")

	loopbackOnly := strings.Contains(diagOut, "CLANKER_LOOPBACK_ONLY=1")
	curlOK := strings.Contains(diagOut, "CLANKER_CURL_OK=1")

	// Decide whether to apply.
	if !loopbackOnly && !cfg.Aggressive {
		return fmt.Errorf("auto-fix skipped: loopback-only bind not detected")
	}

	_, _ = fmt.Fprintf(opts.Writer, "[health] applying container restart with bind-to-0.0.0.0 env fix (loopbackOnly=%v curlOK=%v openclaw=%v)\n", loopbackOnly, curlOK, isOpenClaw)

	restartCmds := ssmRestartCommands(appPort, opts.Region, accountID, image)
	if isOpenClaw {
		startCmd := strings.TrimSpace(bindings["START_COMMAND"])
		if startCmd == "" {
			startCmd = fmt.Sprintf("node openclaw.mjs gateway --allow-unconfigured --bind lan --port %d", appPort)
		}
		restartCmds = ssmRestartCommandsOpenClaw(appPort, opts.Region, accountID, image, startCmd, bindings)
	}

	restartOut, restartErr := runSSMShellScript(ctx, instanceID, opts.Profile, opts.Region, restartCmds, opts.Writer)
	if restartOut != "" {
		_, _ = io.WriteString(opts.Writer, "[health][ssm] restart output:\n"+restartOut+"\n")
	}
	if restartErr != nil {
		return fmt.Errorf("auto-fix restart failed: %w", restartErr)
	}

	// Second wait: allow ALB health checks to pass.
	if err := WaitForALBHealthy(ctx, tgARN, opts.Profile, opts.Region, opts.Writer, 5*time.Minute); err != nil {
		return fmt.Errorf("auto-fix attempted but targets still unhealthy (alb=%s): %w", albDNS, err)
	}

	return nil
}

func ssmEnsureDockerCommands() []string {
	return []string{
		"set -euo pipefail",
		"if command -v docker >/dev/null 2>&1; then echo '[bootstrap] docker present'; else echo '[bootstrap] docker missing; installing...'; . /etc/os-release || true; if [ \"${ID:-}\" = \"amzn\" ]; then yum install -y docker; elif command -v apt-get >/dev/null 2>&1; then apt-get update -y && apt-get install -y docker.io; else echo 'unsupported OS for docker install' && exit 1; fi; fi",
		". /etc/os-release || true",
		"systemctl enable docker || true",
		"systemctl start docker || true",
		"docker version >/dev/null 2>&1 || true",
	}
}

func ssmEnsureECRLoginAndPullCommands(region, accountID, image string) []string {
	region = strings.TrimSpace(region)
	accountID = strings.TrimSpace(accountID)
	image = strings.TrimSpace(image)
	lower := strings.ToLower(image)
	if region == "" || accountID == "" || !strings.Contains(lower, ".dkr.ecr.") {
		return []string{"echo '[bootstrap] skipping ECR login/pull'"}
	}
	registry := fmt.Sprintf("%s.dkr.ecr.%s.amazonaws.com", accountID, region)
	return []string{
		"echo '[bootstrap] ECR login + pull'",
		fmt.Sprintf("aws ecr get-login-password --region %s | docker login --username AWS --password-stdin %s", region, registry),
		fmt.Sprintf("docker pull %s || true", image),
	}
}

func ssmDiagnosticCommands(port int, region, accountID, image string) []string {
	p := strconv.Itoa(port)
	cmds := make([]string, 0, 32)
	cmds = append(cmds, ssmEnsureDockerCommands()...)
	cmds = append(cmds, ssmEnsureECRLoginAndPullCommands(region, accountID, image)...)
	cmds = append(cmds,
		"PORT="+p,
		"echo CLANKER_PORT=$PORT",
		"echo '== ss =='",
		"ss -ltnp | sed 's/^/[ss] /' || true",
		"echo '== docker ps =='",
		"docker ps --format '{{.ID}} {{.Image}} {{.Ports}} {{.Names}}' | sed 's/^/[ps] /' || true",
		"echo '== curl =='",
		"if curl -fsS --max-time 2 http://127.0.0.1:$PORT/health >/dev/null 2>&1; then echo CLANKER_CURL_OK=1; else if curl -fsS --max-time 2 http://127.0.0.1:$PORT/ >/dev/null 2>&1; then echo CLANKER_CURL_OK=1; else echo CLANKER_CURL_OK=0; fi; fi",
		"LOOP=0; ANY=0; if ss -ltnH \"sport = :$PORT\" 2>/dev/null | awk '{print $4}' | grep -q '^127\\.0\\.0\\.1:'; then LOOP=1; fi; if ss -ltnH \"sport = :$PORT\" 2>/dev/null | awk '{print $4}' | grep -Eq '^(0\\.0\\.0\\.0:|\\[::\\]:|:::)'; then ANY=1; fi; echo CLANKER_LISTEN_LOOPBACK=$LOOP; echo CLANKER_LISTEN_ANY=$ANY; if [ \"$LOOP\" = \"1\" ] && [ \"$ANY\" = \"0\" ]; then echo CLANKER_LOOPBACK_ONLY=1; else echo CLANKER_LOOPBACK_ONLY=0; fi",
		"CID=$(docker ps --format '{{.ID}} {{.Ports}}' | awk -v p=\":$PORT->\" '$0 ~ p {print $1; exit}'); if [ -z \"${CID}\" ]; then CID=$(docker ps -q | head -n 1 || true); fi; echo CLANKER_CID=${CID:-none}",
		"if [ -n \"${CID:-}\" ]; then docker logs --tail 120 \"$CID\" 2>&1 | sed 's/^/[log] /' || true; fi",
	)
	return cmds
}

func ssmRestartCommands(port int, region, accountID, image string) []string {
	p := strconv.Itoa(port)
	img := strings.TrimSpace(image)
	if img == "" {
		img = "<missing-image>"
	}
	// Keep it self-contained and non-interactive.
	cmds := make([]string, 0, 32)
	cmds = append(cmds, ssmEnsureDockerCommands()...)
	cmds = append(cmds, ssmEnsureECRLoginAndPullCommands(region, accountID, img)...)
	cmds = append(cmds,
		"PORT="+p,
		"IMAGE=\""+strings.ReplaceAll(img, "\"", "\\\"")+"\"",
		"CID=$(docker ps --format '{{.ID}} {{.Ports}}' | awk -v p=\":$PORT->\" '$0 ~ p {print $1; exit}'); if [ -z \"${CID}\" ]; then CID=$(docker ps -q | head -n 1 || true); fi",
		"if [ -z \"${CID:-}\" ]; then echo 'no running container found'; exit 0; fi",
		"NAME=$(docker inspect --format '{{.Name}}' \"$CID\" 2>/dev/null | sed 's#^/##' || true); if [ -z \"${NAME:-}\" ]; then NAME=app; fi",
		"docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' \"$CID\" | grep -vE '^(HOST|BIND|PORT)=' > /tmp/clanker.env || true",
		"docker rm -f \"$CID\" || true",
		"docker run -d --restart unless-stopped --name \"$NAME\" -p \"$PORT:$PORT\" --env-file /tmp/clanker.env --env PORT=\"$PORT\" --env HOST=0.0.0.0 --env BIND=0.0.0.0 \"$IMAGE\"",
		"sleep 2",
		"docker ps --format '{{.ID}} {{.Image}} {{.Ports}} {{.Names}}' | sed 's/^/[ps] /' || true",
	)
	return cmds
}

func ssmRestartCommandsOpenClaw(port int, region, accountID, image string, startCmd string, bindings map[string]string) []string {
	p := strconv.Itoa(port)
	img := strings.TrimSpace(image)
	if img == "" {
		img = "<missing-image>"
	}
	startCmd = strings.TrimSpace(startCmd)
	startCmd = strings.ReplaceAll(startCmd, "\"", "\\\"")
	cmds := make([]string, 0, 32)
	cmds = append(cmds, ssmEnsureDockerCommands()...)
	cmds = append(cmds, ssmEnsureECRLoginAndPullCommands(region, accountID, img)...)

	cmds = append(cmds,
		"PORT="+p,
		"IMAGE=\""+strings.ReplaceAll(img, "\"", "\\\"")+"\"",
		"START=\""+startCmd+"\"",
		"CID=$(docker ps --format '{{.ID}} {{.Ports}}' | awk -v p=\":$PORT->\" '$0 ~ p {print $1; exit}'); if [ -z \"${CID}\" ]; then CID=$(docker ps -q | head -n 1 || true); fi",
		"if [ -n \"${CID:-}\" ]; then docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' \"$CID\" | grep -vE '^(HOST|BIND|PORT)=' > /tmp/clanker.env || true; fi",
		"if [ -n \"${CID:-}\" ]; then docker rm -f \"$CID\" || true; fi",
		"docker volume create openclaw_data || true",
		"docker run --rm -v openclaw_data:/home/node/.openclaw alpine:3.20 sh -lc 'chown -R 1000:1000 /home/node/.openclaw' || true",
		"docker rm -f openclaw || true",
		"touch /tmp/clanker.env || true",
	)

	// Extract any ENV_* bindings (passed by clanker-cloud via process env → maker bindings) so the
	// restarted container keeps required secrets like OPENCLAW_GATEWAY_TOKEN.
	extraEnvLines := envFileLinesFromBindings(bindings)
	for _, line := range extraEnvLines {
		cmds = append(cmds, fmt.Sprintf("printf '%%s\\n' %s >> /tmp/clanker.env", shellSingleQuote(line)))
	}

	cmds = append(cmds,
		"docker run -d --restart unless-stopped --name openclaw -p \"$PORT:$PORT\" -v openclaw_data:/home/node/.openclaw --env-file /tmp/clanker.env --env PORT=\"$PORT\" --env HOST=0.0.0.0 --env BIND=0.0.0.0 \"$IMAGE\" sh -lc \"$START\"",
		"sleep 2",
		"docker ps --format '{{.ID}} {{.Image}} {{.Ports}} {{.Names}}' | sed 's/^/[ps] /' || true",
	)
	return cmds
}

func envFileLinesFromBindings(bindings map[string]string) []string {
	if len(bindings) == 0 {
		return nil
	}
	lines := make([]string, 0, 16)
	for k, v := range bindings {
		key := strings.TrimSpace(k)
		if !strings.HasPrefix(key, "ENV_") {
			continue
		}
		envName := strings.TrimSpace(strings.TrimPrefix(key, "ENV_"))
		val := strings.TrimSpace(v)
		if envName == "" || val == "" {
			continue
		}
		lines = append(lines, envName+"="+val)
	}
	sort.Strings(lines)
	return lines
}

func shellSingleQuote(s string) string {
	// Produces a single-quoted shell literal, escaping embedded single quotes.
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
