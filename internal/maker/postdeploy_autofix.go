package maker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/bgdnvk/clanker/internal/openclaw"
	"github.com/bgdnvk/clanker/internal/wordpress"
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

	// Scope: EC2-based deployments. ALB is optional (non-ALB support).
	if instanceID == "" || question == "" {
		return nil
	}
	hasALB := tgARN != "" && albDNS != ""

	appPortRaw := strings.TrimSpace(bindings["APP_PORT"])
	if appPortRaw == "" {
		appPortRaw = "3000"
	}
	appPort, err := strconv.Atoi(appPortRaw)
	if err != nil || appPort < 1 || appPort > 65535 {
		return nil
	}

	// First wait: allow user-data to finish and container to start.
	if hasALB {
		if err := WaitForALBHealthy(ctx, tgARN, opts.Profile, opts.Region, opts.Writer, 2*time.Minute); err == nil {
			return nil
		}
	} else {
		// No ALB: wait a fixed period for user-data to finish.
		_, _ = fmt.Fprintf(opts.Writer, "[health] no ALB detected; waiting 90s for user-data + container startup...\n")
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(90 * time.Second):
		}
	}

	_, _ = fmt.Fprintf(opts.Writer, "[health] no healthy targets yet; running automatic runtime remediation via SSM...\n")

	accountID := strings.TrimSpace(bindings["ACCOUNT_ID"])
	if accountID == "" {
		accountID = strings.TrimSpace(bindings["AWS_ACCOUNT_ID"])
	}

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
	// Attempt to discover from ECR if we still don't have an image.
	if image == "" {
		if discovered := maybeDiscoverImageFromECR(ctx, opts, accountID); discovered != "" {
			_, _ = fmt.Fprintf(opts.Writer, "[health] discovered image from ECR: %s\n", discovered)
			image = discovered
		}
	}
	// Try reading from the instance itself as last resort.
	if image == "" {
		if discovered := maybeDiscoverImageFromInstance(ctx, instanceID, opts); discovered != "" {
			_, _ = fmt.Fprintf(opts.Writer, "[health] discovered image from instance: %s\n", discovered)
			image = discovered
		}
	}
	if image == "" {
		return fmt.Errorf("auto-fix skipped: missing IMAGE_URI/ECR_URI and could not discover image")
	}

	if parsed := strings.TrimSpace(extractAccountFromECR(image)); parsed != "" {
		accountID = parsed
	}

	isOpenClaw := openclaw.Detect(question, extractRepoURLFromQuestion(question))
	isWordPress := wordpress.Detect(question, extractRepoURLFromQuestion(question))
	if isWordPress {
		wpName := wordpress.WPContainerName(bindings)
		dbName := wordpress.DBContainerName(bindings)

		// First check: are WP containers running at all?
		wpDiagCmds := make([]string, 0, 16)
		wpDiagCmds = append(wpDiagCmds, ssmEnsureDockerCommands()...)
		wpDiagCmds = append(wpDiagCmds,
			"docker ps --format '{{.ID}} {{.Image}} {{.Ports}} {{.Names}}' || true",
			"RUNNING=$(docker ps -q 2>/dev/null | wc -l | tr -d ' '); echo DEPLOY_CONTAINERS_RUNNING=$RUNNING",
		)

		wpDiagOut, wpDiagErr := runSSMShellScript(ctx, instanceID, opts.Profile, opts.Region, wpDiagCmds, opts.Writer)
		if wpDiagErr != nil {
			// SSM agent may be down; try reboot fallback.
			_, _ = fmt.Fprintf(opts.Writer, "[health] SSM diagnostics failed; attempting reboot fallback...\n")
			if rebootErr := maybeRebootAndRetrySSM(ctx, instanceID, opts); rebootErr != nil {
				return fmt.Errorf("auto-fix wordpress: SSM unreachable even after reboot: %w", rebootErr)
			}
			wpDiagOut, wpDiagErr = runSSMShellScript(ctx, instanceID, opts.Profile, opts.Region, wpDiagCmds, opts.Writer)
			if wpDiagErr != nil {
				return fmt.Errorf("auto-fix wordpress: SSM still failing after reboot: %w", wpDiagErr)
			}
		}

		wpNoContainers := strings.Contains(wpDiagOut, "DEPLOY_CONTAINERS_RUNNING=0")

		cmds := make([]string, 0, 24)
		cmds = append(cmds, ssmEnsureDockerCommands()...)
		if wpNoContainers {
			// No containers at all: full WordPress bootstrap from scratch.
			_, _ = fmt.Fprintf(opts.Writer, "[health] no WordPress containers running; full re-bootstrap...\n")
			cmds = append(cmds, ssmEnsureAWSCLICommands()...)
			cmds = append(cmds,
				"docker rm -f $(docker ps -aq) 2>/dev/null || true",
				"docker network create wp_net 2>/dev/null || true",
				fmt.Sprintf("docker run -d --restart unless-stopped --name %s --network wp_net -e MYSQL_ROOT_PASSWORD=wordpress -e MYSQL_DATABASE=wordpress -e MYSQL_USER=wordpress -e MYSQL_PASSWORD=wordpress mysql:8.0", dbName),
				"sleep 10",
				fmt.Sprintf("docker run -d --restart unless-stopped --name %s --network wp_net -p 80:80 -e WORDPRESS_DB_HOST=%s:3306 -e WORDPRESS_DB_USER=wordpress -e WORDPRESS_DB_PASSWORD=wordpress -e WORDPRESS_DB_NAME=wordpress wordpress:latest", wpName, dbName),
				"sleep 5",
			)
		} else {
			cmds = append(cmds,
				"echo '[wordpress] ssm remediation: restart containers'",
				fmt.Sprintf("docker restart %s || true", dbName),
				fmt.Sprintf("docker restart %s || true", wpName),
				"sleep 3",
			)
		}
		cmds = append(cmds,
			"echo '[wordpress] curl localhost'",
			"curl -fsS --max-time 2 http://127.0.0.1/wp-login.php >/dev/null 2>&1 && echo DEPLOY_WP_CURL_OK=1 || echo DEPLOY_WP_CURL_OK=0",
			"docker ps --format '{{.ID}} {{.Image}} {{.Ports}} {{.Names}}' | sed 's/^/[ps] /' || true",
			fmt.Sprintf("docker logs --tail 60 %s 2>&1 | sed 's/^/[db] /' || true", dbName),
			fmt.Sprintf("docker logs --tail 60 %s 2>&1 | sed 's/^/[wp] /' || true", wpName),
		)

		restartOut, restartErr := runSSMShellScript(ctx, instanceID, opts.Profile, opts.Region, cmds, opts.Writer)
		if restartOut != "" {
			_, _ = io.WriteString(opts.Writer, "[health][ssm] wordpress restart output:\n"+restartOut+"\n")
		}
		if restartErr != nil {
			return fmt.Errorf("auto-fix wordpress restart failed: %w", restartErr)
		}

		if hasALB {
			if err := WaitForALBHealthy(ctx, tgARN, opts.Profile, opts.Region, opts.Writer, 6*time.Minute); err != nil {
				// SG or TG health path may be wrong.
				_ = maybeFixSecurityGroupForALB(ctx, bindings, opts, 80)
				_ = maybeFixTargetGroupHealthCheck(ctx, tgARN, opts, "", true)
				if err2 := WaitForALBHealthy(ctx, tgARN, opts.Profile, opts.Region, opts.Writer, 3*time.Minute); err2 != nil {
					return fmt.Errorf("auto-fix attempted but wordpress targets still unhealthy (alb=%s): %w", albDNS, err2)
				}
			}
		}
		return nil
	}

	diagOut, diagErr := runSSMShellScript(ctx, instanceID, opts.Profile, opts.Region, ssmDiagnosticCommands(appPort, opts.Region, accountID, image), opts.Writer)
	if diagErr != nil {
		// SSM agent may be down; try rebooting the instance to kick it.
		_, _ = fmt.Fprintf(opts.Writer, "[health] SSM diagnostics failed (%v); attempting reboot fallback...\n", diagErr)
		if rebootErr := maybeRebootAndRetrySSM(ctx, instanceID, opts); rebootErr != nil {
			return fmt.Errorf("auto-fix diagnostics failed and reboot did not help: %w", diagErr)
		}
		// Retry diagnostics after reboot.
		diagOut, diagErr = runSSMShellScript(ctx, instanceID, opts.Profile, opts.Region, ssmDiagnosticCommands(appPort, opts.Region, accountID, image), opts.Writer)
		if diagErr != nil {
			return fmt.Errorf("auto-fix diagnostics still failing after reboot: %w", diagErr)
		}
	}
	_, _ = io.WriteString(opts.Writer, "[health][ssm] diagnostics output:\n"+diagOut+"\n")

	loopbackOnly := strings.Contains(diagOut, "DEPLOY_LOOPBACK_ONLY=1")
	curlOK := strings.Contains(diagOut, "DEPLOY_CURL_OK=1")
	curlHealthOK := strings.Contains(diagOut, "DEPLOY_CURL_HEALTH_OK=1")
	curlRootOK := strings.Contains(diagOut, "DEPLOY_CURL_ROOT_OK=1")
	noContainers := strings.Contains(diagOut, "DEPLOY_CONTAINERS_RUNNING=0")
	userDataAccessDenied := strings.Contains(diagOut, "DEPLOY_USERDATA_ACCESS_DENIED=1")
	cloudInitOK := strings.Contains(diagOut, "DEPLOY_CLOUDINIT_OK=1")
	diskHigh := strings.Contains(diagOut, "DEPLOY_DISK_HIGH=1")
	oomDetected := strings.Contains(diagOut, "DEPLOY_OOM_DETECTED=1")
	deniedServices := parseDeniedServices(diagOut)

	// Case: disk nearly full — docker prune to reclaim space before anything else.
	if diskHigh {
		_, _ = fmt.Fprintf(opts.Writer, "[health] disk usage high; running docker prune...\n")
		pruneCmds := []string{"docker system prune -af --volumes 2>/dev/null || true", "df -h / | sed 's/^/[df] /' || true"}
		pruneOut, _ := runSSMShellScript(ctx, instanceID, opts.Profile, opts.Region, pruneCmds, opts.Writer)
		if pruneOut != "" {
			_, _ = io.WriteString(opts.Writer, "[health][prune] "+pruneOut+"\n")
		}
	}

	// Case: OOM detected — log it for visibility (not much we can auto-fix besides restarting).
	if oomDetected {
		_, _ = fmt.Fprintf(opts.Writer, "[health] OOM killer fired on this instance — container may have been killed due to memory pressure\n")
	}

	// Case: user-data crashed before Docker started (IAM race / AccessDenied).
	// Detect which AWS services were denied, attach the right policies, re-bootstrap.
	if noContainers && !cloudInitOK && (userDataAccessDenied || len(deniedServices) > 0) {
		_, _ = fmt.Fprintf(opts.Writer, "[health] user-data crashed (accessDenied=%v deniedServices=%v); attempting IAM + bootstrap remediation...\n", userDataAccessDenied, deniedServices)
		if fixErr := remediateUserDataCrashAndRebootstrap(ctx, instanceID, appPort, opts, accountID, image, isOpenClaw, bindings, deniedServices); fixErr != nil {
			_, _ = fmt.Fprintf(opts.Writer, "[health] user-data remediation failed: %v\n", fixErr)
			// fall through to normal restart path
		} else {
			if err := WaitForALBHealthy(ctx, tgARN, opts.Profile, opts.Region, opts.Writer, 5*time.Minute); err == nil {
				return nil
			}
			_, _ = fmt.Fprintf(opts.Writer, "[health] user-data remediation ran but ALB still unhealthy; continuing to normal restart path\n")
		}
	}

	// Case: no containers at all but cloud-init finished (user-data completed but container exited/crashed).
	// Force-start the container from scratch instead of bailing out.
	if noContainers && !loopbackOnly {
		_, _ = fmt.Fprintf(opts.Writer, "[health] no containers running; forcing fresh container start (openclaw=%v)\n", isOpenClaw)
		cfg.Aggressive = true
	}

	// Decide whether to apply.
	if !loopbackOnly && !cfg.Aggressive {
		return fmt.Errorf("auto-fix skipped: loopback-only bind not detected")
	}

	_, _ = fmt.Fprintf(opts.Writer, "[health] applying container restart with bind-to-0.0.0.0 env fix (loopbackOnly=%v curlOK=%v openclaw=%v)\n", loopbackOnly, curlOK, isOpenClaw)

	restartCmds := ssmRestartCommands(appPort, opts.Region, accountID, image)
	if isOpenClaw {
		startCmd := strings.TrimSpace(bindings["START_COMMAND"])
		lowerStart := strings.ToLower(startCmd)
		if startCmd == "" || strings.Contains(lowerStart, "docker compose") || strings.Contains(lowerStart, "docker-compose") || strings.Contains(lowerStart, "docker run") {
			startCmd = fmt.Sprintf("node openclaw.mjs gateway --allow-unconfigured --bind lan --port %d", appPort)
		}
		// Strip legacy dangerous flag if present.
		startCmd = strings.ReplaceAll(startCmd, " --dangerously-allow-host-header-origin-fallback", "")
		prelude := make([]string, 0, 16)
		prelude = append(prelude, ssmEnsureDockerCommands()...)
		prelude = append(prelude, ssmEnsureAWSCLICommands()...)
		prelude = append(prelude, ssmEnsureECRLoginAndPullCommands(opts.Region, accountID, strings.TrimSpace(image))...)
		restartCmds = openclaw.SSMRestartCommands(prelude, appPort, image, startCmd, bindings)
	}

	restartOut, restartErr := runSSMShellScript(ctx, instanceID, opts.Profile, opts.Region, restartCmds, opts.Writer)
	if restartOut != "" {
		_, _ = io.WriteString(opts.Writer, "[health][ssm] restart output:\n"+restartOut+"\n")
	}
	if restartErr != nil {
		return fmt.Errorf("auto-fix restart failed: %w", restartErr)
	}

	// Second wait: allow ALB health checks to pass.
	if !hasALB {
		// No ALB: verify container is healthy via SSM curl.
		_, _ = fmt.Fprintf(opts.Writer, "[health] no ALB; verifying container responds on port %d via SSM...\n", appPort)
		checkCmds := []string{
			fmt.Sprintf("sleep 5 && curl -fsS --max-time 3 http://127.0.0.1:%d/ >/dev/null 2>&1 && echo DEPLOY_VERIFY_OK=1 || echo DEPLOY_VERIFY_OK=0", appPort),
		}
		checkOut, _ := runSSMShellScript(ctx, instanceID, opts.Profile, opts.Region, checkCmds, opts.Writer)
		if strings.Contains(checkOut, "DEPLOY_VERIFY_OK=1") {
			_, _ = fmt.Fprintf(opts.Writer, "[health] container responding on port %d\n", appPort)
			return nil
		}
		_, _ = fmt.Fprintf(opts.Writer, "[health] container not responding on port %d after restart\n", appPort)
		return fmt.Errorf("auto-fix attempted but container still not responding on port %d", appPort)
	}

	if err := WaitForALBHealthy(ctx, tgARN, opts.Profile, opts.Region, opts.Writer, 5*time.Minute); err != nil {
		// Container restart worked but ALB still unhealthy: try SG + TG fixes.
		_, _ = fmt.Fprintf(opts.Writer, "[health] container restarted but ALB still unhealthy; checking SG and TG health path...\n")
		sgFixed := maybeFixSecurityGroupForALB(ctx, bindings, opts, appPort) == nil
		tgFixed := maybeFixTargetGroupHealthCheck(ctx, tgARN, opts, diagOut, curlRootOK && !curlHealthOK) == nil
		if sgFixed || tgFixed {
			_, _ = fmt.Fprintf(opts.Writer, "[health] SG/TG fixes applied (sg=%v tg=%v); waiting for ALB health...\n", sgFixed, tgFixed)
			if err2 := WaitForALBHealthy(ctx, tgARN, opts.Profile, opts.Region, opts.Writer, 3*time.Minute); err2 != nil {
				return fmt.Errorf("auto-fix attempted (including SG/TG) but targets still unhealthy (alb=%s): %w", albDNS, err2)
			}
			return nil
		}
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

func ssmEnsureAWSCLICommands() []string {
	return []string{
		"echo '[bootstrap] ensure aws cli'",
		"if command -v aws >/dev/null 2>&1; then echo '[bootstrap] aws cli present'; else echo '[bootstrap] aws cli missing; installing...'; . /etc/os-release || true; if [ \"${ID:-}\" = \"amzn\" ]; then (dnf -y install awscli || yum -y install awscli); elif command -v apt-get >/dev/null 2>&1; then apt-get update -y && apt-get install -y awscli; else echo 'unsupported OS for aws cli install' && exit 1; fi; fi",
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
		"# Ensure AWS CLI exists for aws ecr login",
		"if command -v aws >/dev/null 2>&1; then true; else . /etc/os-release || true; if [ \"${ID:-}\" = \"amzn\" ]; then (dnf -y install awscli || yum -y install awscli); elif command -v apt-get >/dev/null 2>&1; then apt-get update -y && apt-get install -y awscli; else echo 'unsupported OS for aws cli install' && exit 1; fi; fi",
		fmt.Sprintf("if aws ecr get-login-password --region %s | docker login --username AWS --password-stdin %s; then echo '[bootstrap] ECR login ok'; else echo '[bootstrap] ECR login failed (continuing with local image cache)'; fi", region, registry),
		fmt.Sprintf("if docker pull %s; then echo '[bootstrap] docker pull ok'; else echo '[bootstrap] docker pull failed (continuing with local image cache)'; fi", image),
	}
}

func ssmDiagnosticCommands(port int, region, accountID, image string) []string {
	p := strconv.Itoa(port)
	cmds := make([]string, 0, 48)
	cmds = append(cmds, ssmEnsureDockerCommands()...)
	cmds = append(cmds, ssmEnsureAWSCLICommands()...)
	cmds = append(cmds, ssmEnsureECRLoginAndPullCommands(region, accountID, image)...)
	cmds = append(cmds,
		"PORT="+p,
		"echo DEPLOY_PORT=$PORT",

		// cloud-init / user-data logs — surface why boot-time script may have crashed
		"echo '== cloud-init =='",
		"if [ -f /var/log/cloud-init-output.log ]; then tail -80 /var/log/cloud-init-output.log | sed 's/^/[ci] /' || true; else echo '[ci] no cloud-init-output.log'; fi",
		// signal: did user-data finish successfully?
		"if grep -qi 'Cloud-init .* finished' /var/log/cloud-init-output.log 2>/dev/null; then echo DEPLOY_CLOUDINIT_OK=1; else echo DEPLOY_CLOUDINIT_OK=0; fi",
		// signal: did user-data hit any AccessDenied?
		"if grep -qiE 'AccessDenied|Access Denied|UnauthorizedAccess|not authorized' /var/log/cloud-init-output.log 2>/dev/null; then echo DEPLOY_USERDATA_ACCESS_DENIED=1; else echo DEPLOY_USERDATA_ACCESS_DENIED=0; fi",
		// signal: which AWS services had AccessDenied? (comma-separated)
		"DENIED_SVCS=''; for svc in secretsmanager s3 dynamodb sqs sns ecr ssm lambda rds; do if grep -iE 'AccessDenied|not authorized' /var/log/cloud-init-output.log 2>/dev/null | grep -qi \"$svc\"; then DENIED_SVCS=\"$DENIED_SVCS,$svc\"; fi; done; DENIED_SVCS=$(echo \"$DENIED_SVCS\" | sed 's/^,//'); echo DEPLOY_DENIED_SERVICES=$DENIED_SVCS",

		// disk + memory — detect resource exhaustion
		"echo '== disk =='",
		"df -h / | sed 's/^/[df] /' || true",
		"DISK_PCT=$(df / --output=pcent 2>/dev/null | tail -1 | tr -dc '0-9' || echo 0); if [ \"${DISK_PCT:-0}\" -ge 90 ] 2>/dev/null; then echo DEPLOY_DISK_HIGH=1; else echo DEPLOY_DISK_HIGH=0; fi",
		"echo '== memory =='",
		"free -m 2>/dev/null | sed 's/^/[mem] /' || true",
		"OOM_COUNT=$(dmesg 2>/dev/null | grep -ciE 'Out of memory|oom-kill|oom_reaper' || echo 0); if [ \"${OOM_COUNT:-0}\" -gt 0 ] 2>/dev/null; then echo DEPLOY_OOM_DETECTED=1; else echo DEPLOY_OOM_DETECTED=0; fi",

		"echo '== ss =='",
		"ss -ltnp | sed 's/^/[ss] /' || true",
		"echo '== docker ps =='",
		"docker ps --format '{{.ID}} {{.Image}} {{.Ports}} {{.Names}}' | sed 's/^/[ps] /' || true",
		// signal: are ANY containers running?
		"RUNNING=$(docker ps -q 2>/dev/null | wc -l | tr -d ' '); echo DEPLOY_CONTAINERS_RUNNING=$RUNNING",
		// curl: test /health and / separately for TG health check path detection
		"echo '== curl =='",
		"HEALTH_OK=0; ROOT_OK=0",
		"if curl -fsS --max-time 2 http://127.0.0.1:$PORT/health >/dev/null 2>&1; then HEALTH_OK=1; fi",
		"if curl -fsS --max-time 2 http://127.0.0.1:$PORT/ >/dev/null 2>&1; then ROOT_OK=1; fi",
		"echo DEPLOY_CURL_HEALTH_OK=$HEALTH_OK; echo DEPLOY_CURL_ROOT_OK=$ROOT_OK",
		"if [ $HEALTH_OK = 1 ] || [ $ROOT_OK = 1 ]; then echo DEPLOY_CURL_OK=1; else echo DEPLOY_CURL_OK=0; fi",
		"LOOP=0; ANY=0; if ss -ltnH \"sport = :$PORT\" 2>/dev/null | awk '{print $4}' | grep -q '^127\\.0\\.0\\.1:'; then LOOP=1; fi; if ss -ltnH \"sport = :$PORT\" 2>/dev/null | awk '{print $4}' | grep -Eq '^(0\\.0\\.0\\.0:|\\[::\\]:|:::)'; then ANY=1; fi; echo DEPLOY_LISTEN_LOOPBACK=$LOOP; echo DEPLOY_LISTEN_ANY=$ANY; if [ \"$LOOP\" = \"1\" ] && [ \"$ANY\" = \"0\" ]; then echo DEPLOY_LOOPBACK_ONLY=1; else echo DEPLOY_LOOPBACK_ONLY=0; fi",
		"CID=$(docker ps --format '{{.ID}} {{.Ports}}' | awk -v p=\":$PORT->\" '$0 ~ p {print $1; exit}'); if [ -z \"${CID}\" ]; then CID=$(docker ps -q | head -n 1 || true); fi; echo DEPLOY_CID=${CID:-none}",
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
	cmds = append(cmds, ssmEnsureAWSCLICommands()...)
	cmds = append(cmds, ssmEnsureECRLoginAndPullCommands(region, accountID, img)...)
	cmds = append(cmds,
		"PORT="+p,
		"IMAGE=\""+strings.ReplaceAll(img, "\"", "\\\"")+"\"",
		"CID=$(docker ps --format '{{.ID}} {{.Ports}}' | awk -v p=\":$PORT->\" '$0 ~ p {print $1; exit}'); if [ -z \"${CID}\" ]; then CID=$(docker ps -q | head -n 1 || true); fi",
		// If no container at all, start fresh instead of bailing out.
		"if [ -z \"${CID:-}\" ]; then echo '[restart] no running container; starting fresh'; touch /tmp/deploy.env; docker run -d --restart unless-stopped --name app -p \"$PORT:$PORT\" --env-file /tmp/deploy.env --env PORT=\"$PORT\" --env HOST=0.0.0.0 --env BIND=0.0.0.0 \"$IMAGE\"; sleep 2; docker ps --format '{{.ID}} {{.Image}} {{.Ports}} {{.Names}}' | sed 's/^/[ps] /' || true; exit 0; fi",
		"NAME=$(docker inspect --format '{{.Name}}' \"$CID\" 2>/dev/null | sed 's#^/##' || true); if [ -z \"${NAME:-}\" ]; then NAME=app; fi",
		"docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' \"$CID\" | grep -vE '^(HOST|BIND|PORT)=' > /tmp/deploy.env || true",
		"docker rm -f \"$CID\" || true",
		"docker run -d --restart unless-stopped --name \"$NAME\" -p \"$PORT:$PORT\" --env-file /tmp/deploy.env --env PORT=\"$PORT\" --env HOST=0.0.0.0 --env BIND=0.0.0.0 \"$IMAGE\"",
		"sleep 2",
		"docker ps --format '{{.ID}} {{.Image}} {{.Ports}} {{.Names}}' | sed 's/^/[ps] /' || true",
	)
	return cmds
}

// awsServicePolicyMap maps AWS service keywords (as found in AccessDenied errors)
// to the corresponding AWS managed policy ARN.
var awsServicePolicyMap = map[string]string{
	"secretsmanager": "arn:aws:iam::aws:policy/SecretsManagerReadWrite",
	"s3":             "arn:aws:iam::aws:policy/AmazonS3FullAccess",
	"dynamodb":       "arn:aws:iam::aws:policy/AmazonDynamoDBFullAccess",
	"sqs":            "arn:aws:iam::aws:policy/AmazonSQSFullAccess",
	"sns":            "arn:aws:iam::aws:policy/AmazonSNSFullAccess",
	"ecr":            "arn:aws:iam::aws:policy/AmazonEC2ContainerRegistryReadOnly",
	"ssm":            "arn:aws:iam::aws:policy/AmazonSSMFullAccess",
	"lambda":         "arn:aws:iam::aws:policy/AWSLambda_FullAccess",
	"rds":            "arn:aws:iam::aws:policy/AmazonRDSFullAccess",
}

// parseDeniedServices extracts the DEPLOY_DENIED_SERVICES=... value from diagnostic output.
func parseDeniedServices(diagOut string) []string {
	const prefix = "DEPLOY_DENIED_SERVICES="
	for _, line := range strings.Split(diagOut, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			val := strings.TrimPrefix(line, prefix)
			val = strings.TrimSpace(val)
			if val == "" {
				return nil
			}
			return strings.Split(val, ",")
		}
	}
	return nil
}

// remediateUserDataCrashAndRebootstrap handles the case where EC2 user-data crashed
// because IAM policies hadn't propagated yet. It detects which services failed,
// attaches the right policies, waits for propagation, then re-runs the bootstrap.
func remediateUserDataCrashAndRebootstrap(ctx context.Context, instanceID string, appPort int, opts ExecOptions, accountID, image string, isOpenClaw bool, bindings map[string]string, deniedServices []string) error {
	_, _ = fmt.Fprintf(opts.Writer, "[health] step 1: discovering instance IAM role...\n")

	// Discover IAM role from the instance metadata.
	roleName, roleErr := discoverEC2InstanceRoleName(ctx, instanceID, opts)
	if roleErr != nil || roleName == "" {
		return fmt.Errorf("cannot discover IAM role for instance %s: %v", instanceID, roleErr)
	}
	_, _ = fmt.Fprintf(opts.Writer, "[health] found IAM role: %s\n", roleName)

	// Determine which policies to attach based on detected denied services.
	policiesToAttach := make(map[string]string) // policyARN -> service name (for logging)
	for _, svc := range deniedServices {
		svc = strings.TrimSpace(strings.ToLower(svc))
		if arn, ok := awsServicePolicyMap[svc]; ok {
			policiesToAttach[arn] = svc
		}
	}
	// Fallback: if we detected AccessDenied but couldn't identify the service, attach SM (most common).
	if len(policiesToAttach) == 0 {
		policiesToAttach["arn:aws:iam::aws:policy/SecretsManagerReadWrite"] = "secretsmanager"
	}

	_, _ = fmt.Fprintf(opts.Writer, "[health] step 2: attaching %d missing policies to role %s...\n", len(policiesToAttach), roleName)
	for arn, svc := range policiesToAttach {
		attachArgs := []string{"iam", "attach-role-policy",
			"--role-name", roleName,
			"--policy-arn", arn,
			"--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
		_, _ = fmt.Fprintf(opts.Writer, "[health]   attaching %s (%s)\n", svc, arn)
		if _, err := runAWSCommandStreaming(ctx, attachArgs, nil, opts.Writer); err != nil {
			_, _ = fmt.Fprintf(opts.Writer, "[health]   attach failed (may already exist): %v\n", err)
		}
	}

	// Wait for IAM propagation.
	_, _ = fmt.Fprintf(opts.Writer, "[health] step 3: waiting 15s for IAM propagation...\n")
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(15 * time.Second):
	}

	// Re-run user-data script via SSM.
	_, _ = fmt.Fprintf(opts.Writer, "[health] step 4: re-running user-data bootstrap via SSM...\n")
	bootstrapCmds := ssmRerunUserDataCommands(appPort, opts.Region, accountID, image, isOpenClaw, bindings)
	bootOut, bootErr := runSSMShellScript(ctx, instanceID, opts.Profile, opts.Region, bootstrapCmds, opts.Writer)
	if bootOut != "" {
		_, _ = io.WriteString(opts.Writer, "[health][ssm] bootstrap output:\n"+bootOut+"\n")
	}
	if bootErr != nil {
		return fmt.Errorf("re-bootstrap failed: %w", bootErr)
	}

	return nil
}

// discoverEC2InstanceRoleName gets the IAM role name attached to an EC2 instance
// by describing the instance, extracting the instance profile, then listing its roles.
func discoverEC2InstanceRoleName(ctx context.Context, instanceID string, opts ExecOptions) (string, error) {
	// Use SSM to query the instance metadata for the IAM role.
	// This is more reliable than describe-instances -> iam get-instance-profile chain
	// because the instance can always query its own metadata.
	cmds := []string{
		"curl -fsS --max-time 2 -H 'X-aws-ec2-metadata-token-ttl-seconds: 21600' -X PUT http://169.254.169.254/latest/api/token 2>/dev/null | { read TOKEN; curl -fsS --max-time 2 -H \"X-aws-ec2-metadata-token: $TOKEN\" http://169.254.169.254/latest/meta-data/iam/security-credentials/ 2>/dev/null; } || echo 'DEPLOY_NO_ROLE'",
	}
	out, err := runSSMShellScript(ctx, instanceID, opts.Profile, opts.Region, cmds, opts.Writer)
	if err != nil {
		return "", err
	}
	out = strings.TrimSpace(out)
	if out == "" || strings.Contains(out, "DEPLOY_NO_ROLE") {
		return "", fmt.Errorf("no IAM role attached to instance")
	}
	// The metadata endpoint returns the role name directly.
	// If there are multiple lines, take the first non-empty one.
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "[") && !strings.HasPrefix(line, "{") {
			return line, nil
		}
	}
	return "", fmt.Errorf("could not parse role name from metadata")
}

// ssmRerunUserDataCommands rebuilds the bootstrap sequence that user-data would have run,
// so we can re-execute it after fixing IAM permissions.
func ssmRerunUserDataCommands(port int, region, accountID, image string, isOpenClaw bool, bindings map[string]string) []string {
	p := strconv.Itoa(port)
	cmds := make([]string, 0, 48)
	cmds = append(cmds, "set -euxo pipefail")
	cmds = append(cmds, ssmEnsureDockerCommands()...)
	cmds = append(cmds, ssmEnsureAWSCLICommands()...)
	cmds = append(cmds, ssmEnsureECRLoginAndPullCommands(region, accountID, image)...)

	img := strings.TrimSpace(image)
	if img == "" {
		img = "<missing-image>"
	}

	// Clean up any dead containers from the failed boot.
	cmds = append(cmds,
		"echo '[rebootstrap] cleaning up failed containers'",
		"docker rm -f $(docker ps -aq) 2>/dev/null || true",
	)

	if isOpenClaw {
		// Write .env file with all required OpenClaw vars.
		cmds = append(cmds,
			"mkdir -p /opt/openclaw/data /opt/openclaw/workspace",
			"cat > /opt/openclaw/.env << 'ENVEOF'"+
				"\nOPENCLAW_CONFIG_DIR=/opt/openclaw/data"+
				"\nOPENCLAW_WORKSPACE_DIR=/opt/openclaw/workspace"+
				"\nENVEOF",
		)
		// Re-fetch secrets from Secrets Manager into .env if they exist.
		cmds = append(cmds, ssmFetchSecretsIntoEnvFile(region, bindings, "/opt/openclaw/.env")...)

		startCmd := fmt.Sprintf("node openclaw.mjs gateway --allow-unconfigured --bind lan --port %s", p)
		containerName := openclaw.ContainerName(bindings)
		// Use CloudFront domain for allowedOrigins if available.
		cfDomain := strings.TrimSpace(bindings["CLOUDFRONT_DOMAIN"])
		portNum, _ := strconv.Atoi(p)
		if portNum == 0 {
			portNum = openclaw.DefaultPort
		}
		cmds = append(cmds,
			"docker volume create openclaw_data || true",
			// Write openclaw.json with allowedOrigins (localhost + CloudFront if known).
			openclaw.ConfigWriteShellCmd(cfDomain, portNum),
			fmt.Sprintf("docker rm -f %s 2>/dev/null || true", containerName),
			fmt.Sprintf("docker run -d --restart unless-stopped --name %s -p %s:%s -v openclaw_data:/home/node/.openclaw --env-file /opt/openclaw/.env --env PORT=%s --env HOST=0.0.0.0 --env BIND=0.0.0.0 %s sh -lc %q",
				containerName, p, p, p, img, startCmd),
			"sleep 3",
			"docker ps --format '{{.ID}} {{.Image}} {{.Ports}} {{.Names}}' | sed 's/^/[ps] /' || true",
		)
	} else {
		// Generic: just start the container with basic env.
		cmds = append(cmds,
			"touch /tmp/deploy.env",
		)
		cmds = append(cmds, ssmFetchSecretsIntoEnvFile(region, bindings, "/tmp/deploy.env")...)
		cmds = append(cmds,
			fmt.Sprintf("docker run -d --restart unless-stopped --name app -p %s:%s --env-file /tmp/deploy.env --env PORT=%s --env HOST=0.0.0.0 --env BIND=0.0.0.0 %s", p, p, p, img),
			"sleep 3",
			"docker ps --format '{{.ID}} {{.Image}} {{.Ports}} {{.Names}}' | sed 's/^/[ps] /' || true",
		)
	}

	return cmds
}

// ssmFetchSecretsIntoEnvFile generates SSM commands that read secrets from
// Secrets Manager and append them as KEY=VALUE lines to the given env file path.
func ssmFetchSecretsIntoEnvFile(region string, bindings map[string]string, envFilePath string) []string {
	if region == "" || len(bindings) == 0 {
		return nil
	}
	cmds := make([]string, 0, 8)
	// Look for bindings that reference SM secret ARNs or names.
	for k, v := range bindings {
		if !strings.HasPrefix(k, "ENV_") && !strings.HasPrefix(k, "SECRET_") {
			continue
		}
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		// Determine the env var name.
		envName := k
		if strings.HasPrefix(k, "ENV_") {
			envName = strings.TrimPrefix(k, "ENV_")
		} else if strings.HasPrefix(k, "SECRET_") {
			envName = strings.TrimPrefix(k, "SECRET_")
		}
		// If the value looks like a SM ARN or a SM secret name path, fetch it.
		if strings.HasPrefix(v, "arn:aws:secretsmanager:") || strings.Contains(v, "/") {
			cmds = append(cmds, fmt.Sprintf(
				"SM_VAL=$(aws secretsmanager get-secret-value --secret-id %q --region %s --query SecretString --output text 2>/dev/null || echo ''); if [ -n \"$SM_VAL\" ]; then printf '%%s=%%s\\n' %q \"$SM_VAL\" >> %s; echo '[bootstrap] fetched secret %s'; fi",
				v, region, envName, envFilePath, envName))
		}
	}
	return cmds
}

// maybeRebootAndRetrySSM reboots an EC2 instance when SSM agent appears down,
// waits for it to come back, then returns nil if SSM is now reachable.
func maybeRebootAndRetrySSM(ctx context.Context, instanceID string, opts ExecOptions) error {
	_, _ = fmt.Fprintf(opts.Writer, "[health] rebooting instance %s to restart SSM agent...\n", instanceID)
	rebootArgs := []string{
		"ec2", "reboot-instances",
		"--instance-ids", instanceID,
		"--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager",
	}
	if _, err := runAWSCommandStreaming(ctx, rebootArgs, nil, opts.Writer); err != nil {
		return fmt.Errorf("reboot failed: %w", err)
	}

	// Wait for instance to come back + SSM agent to register.
	_, _ = fmt.Fprintf(opts.Writer, "[health] waiting up to 3 minutes for instance to reboot + SSM agent...\\n")
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(15 * time.Second):
		}
		// Probe SSM agent with a trivial command.
		probe, err := runSSMShellScript(ctx, instanceID, opts.Profile, opts.Region, []string{"echo DEPLOY_SSM_OK=1"}, opts.Writer)
		if err == nil && strings.Contains(probe, "DEPLOY_SSM_OK=1") {
			_, _ = fmt.Fprintf(opts.Writer, "[health] SSM agent is back after reboot\\n")
			return nil
		}
	}
	return fmt.Errorf("SSM agent did not come back within 3 minutes after reboot")
}

// maybeFixSecurityGroupForALB checks if the instance SG allows inbound traffic
// from the ALB SG on the app port, and adds the rule if missing.
func maybeFixSecurityGroupForALB(ctx context.Context, bindings map[string]string, opts ExecOptions, appPort int) error {
	// Try to get SGs from bindings first.
	instanceSG := strings.TrimSpace(bindings["SG_ID"])
	if instanceSG == "" {
		instanceSG = strings.TrimSpace(bindings["WEB_SG_ID"])
	}
	if instanceSG == "" {
		instanceSG = strings.TrimSpace(bindings["SG_WEB_ID"])
	}
	albSG := strings.TrimSpace(bindings["ALB_SG_ID"])
	if albSG == "" {
		albSG = strings.TrimSpace(bindings["SG_ALB_ID"])
	}

	instanceID := strings.TrimSpace(bindings["INSTANCE_ID"])

	// If we don't have instance SG, discover it.
	if instanceSG == "" && instanceID != "" {
		out, err := runAWSCommandStreaming(ctx, []string{
			"ec2", "describe-instances",
			"--instance-ids", instanceID,
			"--query", "Reservations[0].Instances[0].SecurityGroups[0].GroupId",
			"--output", "text",
			"--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager",
		}, nil, opts.Writer)
		if err == nil {
			instanceSG = strings.TrimSpace(out)
		}
	}

	// If we don't have ALB SG, discover from TG -> LB -> SG.
	if albSG == "" {
		tgARN := strings.TrimSpace(bindings["TG_ARN"])
		if tgARN != "" {
			// Get load balancer ARN from TG.
			lbOut, err := runAWSCommandStreaming(ctx, []string{
				"elbv2", "describe-target-groups",
				"--target-group-arns", tgARN,
				"--query", "TargetGroups[0].LoadBalancerArns[0]",
				"--output", "text",
				"--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager",
			}, nil, opts.Writer)
			if err == nil {
				lbARN := strings.TrimSpace(lbOut)
				if lbARN != "" && lbARN != "None" {
					sgOut, err2 := runAWSCommandStreaming(ctx, []string{
						"elbv2", "describe-load-balancers",
						"--load-balancer-arns", lbARN,
						"--query", "LoadBalancers[0].SecurityGroups[0]",
						"--output", "text",
						"--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager",
					}, nil, opts.Writer)
					if err2 == nil {
						albSG = strings.TrimSpace(sgOut)
					}
				}
			}
		}
	}

	if instanceSG == "" || instanceSG == "None" {
		_, _ = fmt.Fprintf(opts.Writer, "[health][sg] could not determine instance security group; skipping SG fix\n")
		return fmt.Errorf("could not determine instance SG")
	}

	_, _ = fmt.Fprintf(opts.Writer, "[health][sg] instance SG=%s ALB SG=%s; checking ingress on port %d...\n", instanceSG, albSG, appPort)

	// Check existing rules by describing the SG.
	rulesOut, err := runAWSCommandStreaming(ctx, []string{
		"ec2", "describe-security-groups",
		"--group-ids", instanceSG,
		"--query", "SecurityGroups[0].IpPermissions",
		"--output", "json",
		"--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager",
	}, nil, opts.Writer)
	if err == nil {
		// Quick check: is the app port already open?
		portStr := strconv.Itoa(appPort)
		if strings.Contains(rulesOut, portStr) || strings.Contains(rulesOut, "\"FromPort\": 0") {
			_, _ = fmt.Fprintf(opts.Writer, "[health][sg] port %d appears already open in SG %s\n", appPort, instanceSG)
			return nil
		}
	}

	// Add ingress rule. Use ALB SG as source if known, otherwise open from VPC (0.0.0.0/0).
	addArgs := []string{
		"ec2", "authorize-security-group-ingress",
		"--group-id", instanceSG,
		"--protocol", "tcp",
		"--port", strconv.Itoa(appPort),
	}
	if albSG != "" && albSG != "None" {
		addArgs = append(addArgs, "--source-group", albSG)
	} else {
		addArgs = append(addArgs, "--cidr", "0.0.0.0/0")
	}
	addArgs = append(addArgs, "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")

	_, _ = fmt.Fprintf(opts.Writer, "[health][sg] adding ingress rule: port %d from %s\n", appPort, albSG)
	if _, err := runAWSCommandStreaming(ctx, addArgs, nil, opts.Writer); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "duplicate") || strings.Contains(strings.ToLower(err.Error()), "already exists") {
			_, _ = fmt.Fprintf(opts.Writer, "[health][sg] rule already exists\n")
			return nil
		}
		return fmt.Errorf("authorize-security-group-ingress failed: %w", err)
	}
	_, _ = fmt.Fprintf(opts.Writer, "[health][sg] ingress rule added successfully\n")
	return nil
}

// maybeFixTargetGroupHealthCheck adjusts the TG health check path if the container
// doesn't serve the expected endpoint. e.g. TG checks /health but app only has /.
func maybeFixTargetGroupHealthCheck(ctx context.Context, tgARN string, opts ExecOptions, diagOut string, rootOnlyDetected bool) error {
	if tgARN == "" {
		return fmt.Errorf("no TG ARN")
	}

	// Get current TG config.
	out, err := runAWSCommandStreaming(ctx, []string{
		"elbv2", "describe-target-groups",
		"--target-group-arns", tgARN,
		"--query", "TargetGroups[0].{Path:HealthCheckPath,Port:HealthCheckPort,Protocol:HealthCheckProtocol}",
		"--output", "json",
		"--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager",
	}, nil, opts.Writer)
	if err != nil {
		return err
	}

	var tgConfig struct {
		Path     string `json:"Path"`
		Port     string `json:"Port"`
		Protocol string `json:"Protocol"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &tgConfig); err != nil {
		_, _ = fmt.Fprintf(opts.Writer, "[health][tg] could not parse TG config: %v\n", err)
		return err
	}

	_, _ = fmt.Fprintf(opts.Writer, "[health][tg] current health check: path=%s port=%s protocol=%s\n", tgConfig.Path, tgConfig.Port, tgConfig.Protocol)

	// If health check path is not /, and the container only responds on /, fix it.
	if tgConfig.Path == "/" {
		_, _ = fmt.Fprintf(opts.Writer, "[health][tg] health check already on /; no change needed\n")
		return nil
	}
	if !rootOnlyDetected {
		_, _ = fmt.Fprintf(opts.Writer, "[health][tg] root-only not detected; keeping current health check path %s\n", tgConfig.Path)
		return nil
	}

	_, _ = fmt.Fprintf(opts.Writer, "[health][tg] app serves / but not %s; updating health check to /\n", tgConfig.Path)
	_, err = runAWSCommandStreaming(ctx, []string{
		"elbv2", "modify-target-group",
		"--target-group-arn", tgARN,
		"--health-check-path", "/",
		"--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager",
	}, nil, opts.Writer)
	if err != nil {
		return fmt.Errorf("modify-target-group failed: %w", err)
	}
	_, _ = fmt.Fprintf(opts.Writer, "[health][tg] health check path updated to /\n")
	return nil
}

// maybeDiscoverImageFromECR tries to find the most recently pushed ECR image
// by listing repositories and images. Returns the full image URI or empty string.
func maybeDiscoverImageFromECR(ctx context.Context, opts ExecOptions, accountID string) string {
	if accountID == "" || opts.Region == "" {
		return ""
	}

	// List ECR repos (usually only 1 for one-click deploys).
	out, err := runAWSCommandStreaming(ctx, []string{
		"ecr", "describe-repositories",
		"--query", "repositories[*].repositoryUri",
		"--output", "json",
		"--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager",
	}, nil, io.Discard)
	if err != nil {
		return ""
	}

	var repos []string
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &repos); err != nil || len(repos) == 0 {
		return ""
	}

	// Try the first repo with :latest tag.
	for _, repo := range repos {
		repo = strings.TrimSpace(repo)
		if repo == "" {
			continue
		}
		// Verify the image exists by describing it.
		check, err := runAWSCommandStreaming(ctx, []string{
			"ecr", "describe-images",
			"--repository-name", repoNameFromURI(repo),
			"--image-ids", "imageTag=latest",
			"--query", "imageDetails[0].imageTags[0]",
			"--output", "text",
			"--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager",
		}, nil, io.Discard)
		if err == nil && strings.TrimSpace(check) != "" && strings.TrimSpace(check) != "None" {
			return repo + ":latest"
		}
	}
	return ""
}

// repoNameFromURI extracts the repository name from an ECR URI.
// e.g. "123456.dkr.ecr.us-east-1.amazonaws.com/my-app" -> "my-app"
func repoNameFromURI(uri string) string {
	if idx := strings.LastIndex(uri, "/"); idx >= 0 {
		return uri[idx+1:]
	}
	return uri
}

// maybeDiscoverImageFromInstance queries the instance via SSM for any pulled docker images.
func maybeDiscoverImageFromInstance(ctx context.Context, instanceID string, opts ExecOptions) string {
	cmds := []string{
		"docker images --format '{{.Repository}}:{{.Tag}}' 2>/dev/null | grep -v '<none>' | head -5 || true",
	}
	out, err := runSSMShellScript(ctx, instanceID, opts.Profile, opts.Region, cmds, opts.Writer)
	if err != nil {
		return ""
	}
	// Pick the first non-base image (skip alpine, ubuntu, etc.).
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		// Skip common base images.
		if strings.HasPrefix(lower, "alpine") || strings.HasPrefix(lower, "ubuntu") ||
			strings.HasPrefix(lower, "mysql") || strings.HasPrefix(lower, "postgres") ||
			strings.HasPrefix(lower, "nginx") || strings.HasPrefix(lower, "node:") {
			continue
		}
		if strings.Contains(lower, ".dkr.ecr.") || strings.Contains(lower, "/") {
			return line
		}
	}
	// If nothing matched the filter, return the first image.
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "[") {
			return line
		}
	}
	return ""
}
