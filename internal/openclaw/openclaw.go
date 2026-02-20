package openclaw

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

const DefaultPort = 18789

func Detect(question string, repoURL string) bool {
	lq := strings.ToLower(strings.TrimSpace(question))
	lr := strings.ToLower(strings.TrimSpace(repoURL))
	return strings.Contains(lq, "openclaw") || strings.Contains(lr, "openclaw")
}

func ContainerName(bindings map[string]string) string {
	if bindings == nil {
		return "openclaw"
	}
	if v := strings.TrimSpace(bindings["OPENCLAW_CONTAINER_NAME"]); v != "" {
		return v
	}
	deployID := strings.TrimSpace(bindings["DEPLOY_ID"])
	if deployID == "" {
		return "openclaw"
	}
	name := "openclaw-" + deployID
	bindings["OPENCLAW_CONTAINER_NAME"] = name
	return name
}

func MaybePrintPostDeployInstructions(bindings map[string]string, profile, region string, w io.Writer, question, repoURL string) {
	if w == nil {
		return
	}
	if !Detect(question, repoURL) {
		return
	}

	instanceID := strings.TrimSpace(bindings["INSTANCE_ID"])
	albDNS := strings.TrimSpace(bindings["ALB_DNS"])
	httpsURL := strings.TrimSpace(bindings["HTTPS_URL"])
	if httpsURL == "" {
		if cf := strings.TrimSpace(bindings["CLOUDFRONT_DOMAIN"]); cf != "" {
			httpsURL = "https://" + cf
		}
	}

	port := 0
	if p := strings.TrimSpace(bindings["APP_PORT"]); p != "" {
		if v, err := strconv.Atoi(p); err == nil {
			port = v
		}
	}
	if port == 0 {
		port = DefaultPort
	}

	containerName := ContainerName(bindings)

	_, _ = io.WriteString(w, "\n")
	_, _ = fmt.Fprintf(w, "[openclaw] post-deploy connect + pairing\n")
	if httpsURL != "" {
		_, _ = fmt.Fprintf(w, "[openclaw] Control UI (HTTPS): %s\n", httpsURL)
	}
	if albDNS != "" {
		_, _ = fmt.Fprintf(w, "[openclaw] ALB origin (HTTP): http://%s\n", albDNS)
	}
	_, _ = fmt.Fprintf(w, "[openclaw] Gateway port: %d\n", port)

	_, _ = fmt.Fprintf(w, "[openclaw] Steps:\n")
	if httpsURL != "" {
		_, _ = fmt.Fprintf(w, "[openclaw]  1) Open %s in your browser\n", httpsURL)
	} else if albDNS != "" {
		_, _ = fmt.Fprintf(w, "[openclaw]  1) Open http://%s (note: some browsers require HTTPS for the Control UI)\n", albDNS)
	} else {
		_, _ = fmt.Fprintf(w, "[openclaw]  1) Open the HTTPS CloudFront URL (if created)\n")
	}
	_, _ = fmt.Fprintf(w, "[openclaw]  2) When prompted, enter your gateway token (env var OPENCLAW_GATEWAY_TOKEN)\n")
	_, _ = fmt.Fprintf(w, "[openclaw]  3) Click Connect\n")
	_, _ = fmt.Fprintf(w, "[openclaw] Pairing:\n")
	_, _ = fmt.Fprintf(w, "[openclaw]  - One-click deploy starts an auto-approve loop for pending pair requests for ~30 minutes (or until 2 new devices are paired) after instance boot\n")
	_, _ = fmt.Fprintf(w, "[openclaw]  - If you still see 'pairing required', use the localhost + approve loop below (this also fixes stale browser device tokens)\n")

	if instanceID != "" && strings.TrimSpace(profile) != "" && strings.TrimSpace(region) != "" {
		_, _ = fmt.Fprintf(w, "[openclaw] Localhost access (SSM port-forward):\n")
		_, _ = fmt.Fprintf(w, "[openclaw]  1) Start port-forward (requires session-manager-plugin):\n")
		_, _ = fmt.Fprintf(w, "[openclaw]     aws ssm start-session --target %s --document-name AWS-StartPortForwardingSession --parameters 'portNumber=[\"%d\"],localPortNumber=[\"%d\"]' --profile %s --region %s\n", instanceID, port, port, profile, region)
		_, _ = fmt.Fprintf(w, "[openclaw]  2) Open a Private/Incognito window and go to http://localhost:%d\n", port)
		_, _ = fmt.Fprintf(w, "[openclaw]     (Incognito avoids stale device tokens that can cause: disconnected (1008): pairing required)\n")
		_, _ = fmt.Fprintf(w, "[openclaw]  3) Enter token and click Connect\n")
		_, _ = fmt.Fprintf(w, "[openclaw]  4) If it still says pairing required, run approve+restart (repeat until connected):\n")
		_, _ = fmt.Fprintf(w, "[openclaw]     clanker openclaw approve --instance-id %s --container %s --profile %s --region %s\n", instanceID, containerName, profile, region)
		_, _ = fmt.Fprintf(w, "[openclaw]     then refresh the page and click Connect again\n")
	}

	if instanceID != "" && strings.TrimSpace(profile) != "" && strings.TrimSpace(region) != "" {
		_, _ = fmt.Fprintf(w, "[openclaw] Debug (SSM):\n")
		_, _ = fmt.Fprintf(w, "[openclaw]  aws ssm send-command --document-name AWS-RunShellScript --instance-ids %s --parameters 'commands=[\"sudo docker exec %s cat /home/node/.openclaw/devices/pending.json\",\"sudo docker exec %s cat /home/node/.openclaw/devices/paired.json\"]' --profile %s --region %s\n", instanceID, containerName, containerName, profile, region)
		_, _ = fmt.Fprintf(w, "[openclaw]  (Pending/paired files are inside the container at /home/node/.openclaw/devices/pending.json and /home/node/.openclaw/devices/paired.json)\n")
	}
}

func SSMRestartCommands(prelude []string, port int, image string, startCmd string, bindings map[string]string) []string {
	p := strconv.Itoa(port)
	img := strings.TrimSpace(image)
	if img == "" {
		img = "<missing-image>"
	}
	startCmd = strings.TrimSpace(startCmd)
	startCmd = strings.ReplaceAll(startCmd, "\"", "\\\"")

	cmds := make([]string, 0, 64)
	cmds = append(cmds, prelude...)

	containerName := ContainerName(bindings)

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
		"docker rm -f \""+containerName+"\" || true",
		"touch /tmp/clanker.env || true",
	)

	extraEnvLines := envFileLinesFromBindings(bindings)
	for _, line := range extraEnvLines {
		cmds = append(cmds, fmt.Sprintf("printf '%%s\\n' %s >> /tmp/clanker.env", shellSingleQuote(line)))
	}

	cmds = append(cmds,
		"docker run -d --restart unless-stopped --name \""+containerName+"\" -p \"$PORT:$PORT\" -v openclaw_data:/home/node/.openclaw --env-file /tmp/clanker.env --env PORT=\"$PORT\" --env HOST=0.0.0.0 --env BIND=0.0.0.0 \"$IMAGE\" sh -lc \"$START\"",
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
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
