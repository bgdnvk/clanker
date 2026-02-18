package maker

import (
	"fmt"
	"io"
	"strconv"
	"strings"
)

func maybePrintOpenClawPostDeployInstructions(bindings map[string]string, opts ExecOptions) {
	if opts.Writer == nil {
		return
	}
	question := strings.TrimSpace(bindings["PLAN_QUESTION"])
	if question == "" {
		question = strings.TrimSpace(bindings["QUESTION"])
	}
	lq := strings.ToLower(question)
	isOpenClaw := strings.Contains(lq, "openclaw") || strings.Contains(strings.ToLower(extractRepoURLFromQuestion(question)), "openclaw")
	if !isOpenClaw {
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
		port = 18789
	}

	containerName := openClawContainerName(bindings)

	w := opts.Writer
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
	_, _ = fmt.Fprintf(w, "[openclaw]  - One-click deploy starts an auto-approve loop for pending pair requests for ~5 minutes after instance boot\n")
	_, _ = fmt.Fprintf(w, "[openclaw]  - If you still see 'pairing required', use the localhost + approve loop below (this also fixes stale browser device tokens)\n")

	if instanceID != "" && strings.TrimSpace(opts.Profile) != "" && strings.TrimSpace(opts.Region) != "" {
		_, _ = fmt.Fprintf(w, "[openclaw] Localhost access (SSM port-forward):\n")
		_, _ = fmt.Fprintf(w, "[openclaw]  1) Start port-forward (requires session-manager-plugin):\n")
		_, _ = fmt.Fprintf(w, "[openclaw]     aws ssm start-session --target %s --document-name AWS-StartPortForwardingSession --parameters 'portNumber=[\"%d\"],localPortNumber=[\"%d\"]' --profile %s --region %s\n", instanceID, port, port, opts.Profile, opts.Region)
		_, _ = fmt.Fprintf(w, "[openclaw]  2) Open a Private/Incognito window and go to http://localhost:%d\n", port)
		_, _ = fmt.Fprintf(w, "[openclaw]     (Incognito avoids stale device tokens that can cause: disconnected (1008): pairing required)\n")
		_, _ = fmt.Fprintf(w, "[openclaw]  3) Enter token and click Connect\n")
		_, _ = fmt.Fprintf(w, "[openclaw]  4) If it still says pairing required, run approve+restart (repeat until connected):\n")
		_, _ = fmt.Fprintf(w, "[openclaw]     aws ssm send-command --document-name AWS-RunShellScript --instance-ids %s --parameters 'commands=[\"sudo docker exec %s node -e \\\"const fs=require(\\\\\\\"fs\\\\\\\"); const p=\\\\\\\"/home/node/.openclaw/devices/pending.json\\\\\\\"; const q=\\\\\\\"/home/node/.openclaw/devices/paired.json\\\\\\\"; const read=(f,def)=>{try{const s=fs.readFileSync(f,\\\\\\\"utf8\\\\\\\").trim(); return s?JSON.parse(s):def}catch{return def}}; const pending=read(p,{}); const paired=read(q,{}); let n=0; for(const [k,v] of Object.entries(pending||{})){ const dk=(v&&v.deviceId)?String(v.deviceId):String(k); paired[dk]=v; n++; } fs.writeFileSync(q, JSON.stringify(paired,null,2)); fs.writeFileSync(p, JSON.stringify({},null,2)); console.log(\\\\\\\"approved\\\\\\\", n);\\\"\",\"sudo docker restart %s\"]' --profile %s --region %s\n", instanceID, containerName, containerName, opts.Profile, opts.Region)
		_, _ = fmt.Fprintf(w, "[openclaw]     then refresh the page and click Connect again\n")
	}

	if instanceID != "" && strings.TrimSpace(opts.Profile) != "" && strings.TrimSpace(opts.Region) != "" {
		_, _ = fmt.Fprintf(w, "[openclaw] Debug (SSM):\n")
		_, _ = fmt.Fprintf(w, "[openclaw]  aws ssm send-command --document-name AWS-RunShellScript --instance-ids %s --parameters 'commands=[\"sudo docker exec %s cat /home/node/.openclaw/devices/pending.json\",\"sudo docker exec %s cat /home/node/.openclaw/devices/paired.json\"]' --profile %s --region %s\n", instanceID, containerName, containerName, opts.Profile, opts.Region)
		_, _ = fmt.Fprintf(w, "[openclaw]  (Pending/paired files are inside the container at /home/node/.openclaw/devices/pending.json and /home/node/.openclaw/devices/paired.json)\n")
	}
}
