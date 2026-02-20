package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var openClawCmd = &cobra.Command{
	Use:   "openclaw",
	Short: "OpenClaw helpers",
}

var openClawApproveCmd = &cobra.Command{
	Use:   "approve",
	Short: "Approve OpenClaw pending device pair requests via SSM",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
		defer cancel()

		instanceID, _ := cmd.Flags().GetString("instance-id")
		containerName, _ := cmd.Flags().GetString("container")
		profile, _ := cmd.Flags().GetString("profile")
		region, _ := cmd.Flags().GetString("region")
		targetNew, _ := cmd.Flags().GetInt("target")
		timeoutMinutes, _ := cmd.Flags().GetInt("timeout-min")

		instanceID = strings.TrimSpace(instanceID)
		containerName = strings.TrimSpace(containerName)
		profile = strings.TrimSpace(profile)
		region = strings.TrimSpace(region)
		if instanceID == "" {
			return fmt.Errorf("--instance-id is required")
		}
		if containerName == "" {
			containerName = "openclaw"
		}
		if targetNew <= 0 {
			targetNew = 2
		}
		if timeoutMinutes <= 0 {
			timeoutMinutes = 30
		}

		commands := []string{
			"set -e",
			fmt.Sprintf("OC_CONTAINER=%q", containerName),
			fmt.Sprintf("TARGET_NEW=%d", targetNew),
			fmt.Sprintf("END=$(( $(date +%%s) + %d ))", timeoutMinutes*60),
			"BASE_PAIRED=$(docker exec \"$OC_CONTAINER\" node -e 'try{const fs=require(\"fs\"); const p=\"/home/node/.openclaw/devices/paired.json\"; const s=fs.readFileSync(p,\"utf8\").trim(); const o=s?JSON.parse(s):{}; console.log(Object.keys(o||{}).length)}catch(e){console.log(0)}' 2>/dev/null)",
			"if [ -z \"$BASE_PAIRED\" ]; then BASE_PAIRED=0; fi",
			fmt.Sprintf("echo \"[openclaw] approve loop: base_paired=$BASE_PAIRED target_new=$TARGET_NEW timeout_min=%d\"", timeoutMinutes),
			"while [ $(date +%s) -lt $END ]; do",
			"\tif ! docker ps --format '{{.Names}}' | grep -qx \"$OC_CONTAINER\"; then echo '[openclaw] container not running' && exit 1; fi",
			"\tJS=$(cat <<'JS'",
			"const fs = require(\"fs\");",
			"const path = require(\"path\");",
			"const pendingPath = \"/home/node/.openclaw/devices/pending.json\";",
			"const pairedPath = \"/home/node/.openclaw/devices/paired.json\";",
			"function readJSON(p) {",
			"\ttry {",
			"\t\treturn JSON.parse(fs.readFileSync(p, \"utf8\") || \"{}\");",
			"\t} catch (e) {",
			"\t\treturn {};",
			"\t}",
			"}",
			"const pending = readJSON(pendingPath);",
			"const paired = readJSON(pairedPath);",
			"const requestIds = Object.keys(pending || {});",
			"const pairedCount = Object.keys(paired || {}).length;",
			"if (requestIds.length === 0) {",
			"\tconsole.log(\"CLANKER_PAIR_NONE PAIRED=\" + pairedCount);",
			"\tprocess.exit(0);",
			"}",
			"let approved = 0;",
			"for (const rid of requestIds) {",
			"\tconst req = pending[rid];",
			"\tif (!req || !req.deviceId) continue;",
			"\tpaired[req.deviceId] = req;",
			"\tdelete pending[rid];",
			"\tapproved++;",
			"}",
			"fs.mkdirSync(path.dirname(pendingPath), { recursive: true });",
			"fs.writeFileSync(pairedPath, JSON.stringify(paired, null, 2));",
			"fs.writeFileSync(pendingPath, JSON.stringify(pending, null, 2));",
			"console.log(\"CLANKER_PAIR_APPROVED=\" + approved + \" PAIRED=\" + Object.keys(paired || {}).length);",
			"JS",
			")",
			"\tOUT=$(docker exec \"$OC_CONTAINER\" node -e \"$JS\" 2>/dev/null)",
			"\tAPPROVED=$(echo \"$OUT\" | sed -n 's/^.*CLANKER_PAIR_APPROVED=\\([0-9][0-9]*\\).*$/\\1/p')",
			"\tPAIRED=$(echo \"$OUT\" | sed -n 's/^.*PAIRED=\\([0-9][0-9]*\\).*$/\\1/p')",
			"\tif [ -n \"$APPROVED\" ] && [ \"$APPROVED\" -gt 0 ] 2>/dev/null; then",
			"\t\techo \"[openclaw] $OUT\"",
			"\t\techo '[openclaw] restarting container after approvals'",
			"\t\tdocker restart \"$OC_CONTAINER\" >/dev/null 2>&1 || true",
			"\t\tsleep 2",
			"\tfi",
			"\tif [ -n \"$PAIRED\" ] && [ \"$PAIRED\" -ge $(( BASE_PAIRED + TARGET_NEW )) ] 2>/dev/null; then",
			"\t\techo \"[openclaw] paired devices reached target ($PAIRED >= $(( BASE_PAIRED + TARGET_NEW )))\"",
			"\t\texit 0",
			"\tfi",
			"\tsleep 3",
			"done",
			"echo '[openclaw] approve loop timed out'",
			"exit 2",
		}

		params := map[string][]string{"commands": commands}
		paramsJSON, err := json.Marshal(params)
		if err != nil {
			return err
		}

		awsArgs := []string{
			"ssm", "send-command",
			"--document-name", "AWS-RunShellScript",
			"--instance-ids", instanceID,
			"--parameters", string(paramsJSON),
			"--output", "json",
		}
		if profile != "" {
			awsArgs = append(awsArgs, "--profile", profile)
		}
		if region != "" {
			awsArgs = append(awsArgs, "--region", region)
		}

		c := exec.CommandContext(ctx, "aws", awsArgs...)
		c.Stderr = os.Stderr
		out, err := c.Output()
		if len(out) > 0 {
			_, _ = os.Stdout.Write(out)
			if out[len(out)-1] != '\n' {
				_, _ = os.Stdout.Write([]byte("\n"))
			}
		}
		if err != nil {
			return err
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(openClawCmd)
	openClawCmd.AddCommand(openClawApproveCmd)

	openClawApproveCmd.Flags().String("instance-id", "", "EC2 instance id")
	openClawApproveCmd.Flags().String("container", "openclaw", "Docker container name")
	openClawApproveCmd.Flags().String("profile", "", "AWS profile")
	openClawApproveCmd.Flags().String("region", "", "AWS region")
	openClawApproveCmd.Flags().Int("target", 2, "Stop after this many NEW devices are paired")
	openClawApproveCmd.Flags().Int("timeout-min", 30, "Approve loop timeout (minutes)")
	_ = openClawApproveCmd.MarkFlagRequired("instance-id")
}
