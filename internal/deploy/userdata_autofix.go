package deploy

import (
	"encoding/base64"
	"regexp"
	"strings"

	"github.com/bgdnvk/clanker/internal/maker"
)

// FixEC2UserDataScripts decodes base64 user-data in ec2 run-instances,
// applies common LLM-generated path and command typo corrections,
// and re-encodes. Runs before validation so deterministic fixes
// prevent issues from reaching the repair LLM.
func FixEC2UserDataScripts(plan *maker.Plan, logf func(string, ...any)) *maker.Plan {
	if plan == nil || len(plan.Commands) == 0 {
		return plan
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}

	totalFixes := 0
	for ci := range plan.Commands {
		cmd := &plan.Commands[ci]
		if len(cmd.Args) < 2 {
			continue
		}
		if strings.TrimSpace(cmd.Args[0]) != "ec2" || strings.TrimSpace(cmd.Args[1]) != "run-instances" {
			continue
		}

		for ai := 0; ai < len(cmd.Args); ai++ {
			arg := strings.TrimSpace(cmd.Args[ai])
			isEquals := strings.HasPrefix(arg, "--user-data=")

			if arg == "--user-data" && ai+1 < len(cmd.Args) {
				script, ok := tryDecodeBase64UserData(cmd.Args[ai+1])
				if !ok {
					continue
				}
				fixed, n := fixUserDataScript(script)
				if n > 0 {
					cmd.Args[ai+1] = base64.StdEncoding.EncodeToString([]byte(fixed))
					totalFixes += n
				}
			} else if isEquals {
				val := strings.TrimPrefix(arg, "--user-data=")
				script, ok := tryDecodeBase64UserData(val)
				if !ok {
					continue
				}
				fixed, n := fixUserDataScript(script)
				if n > 0 {
					cmd.Args[ai] = "--user-data=" + base64.StdEncoding.EncodeToString([]byte(fixed))
					totalFixes += n
				}
			}
		}
	}

	if totalFixes > 0 {
		logf("[deploy] user-data autofix: applied %d deterministic correction(s)", totalFixes)
	}
	return plan
}

// common LLM path typos — these are paths that never exist on a real Linux
// system so replacing them cannot harm a correct script.
var pathTypoFixes = []struct {
	broken string
	fixed  string
}{
	// /sr/local/ → /usr/local/  (LLM drops the 'u')
	{"/sr/local/", "/usr/local/"},
	{"/sr/bin/", "/usr/bin/"},
	{"/sr/lib/", "/usr/lib/"},
	{"/sr/share/", "/usr/share/"},
	// /spt/ → /opt/  (LLM swaps letters; /spt is never valid)
	{"/spt/", "/opt/"},
	// /use/local/ → /usr/local/  (LLM adds an 'e')
	{"/use/local/", "/usr/local/"},
	{"/use/bin/", "/usr/bin/"},
	// /var/lig/ → /var/lib/  (anagram; /var/lig never valid)
	{"/var/lig/", "/var/lib/"},
	// /ect/ → /etc/  (anagram; /ect never valid)
	{"/ect/", "/etc/"},
}

// garbled ECR login: LLM sometimes generates non-ascii chars in base64
var garbledECRLoginRe = regexp.MustCompile(`(?m)^.*(?:awd?s?\s+ecr\s+get-login|YXDZIGV2ci|YXdz\s+ZWNy).*$`)

// fixUserDataScript applies deterministic corrections to a decoded user-data script.
// Returns the fixed script and a count of fixes applied.
func fixUserDataScript(script string) (string, int) {
	fixed := script
	count := 0

	// 0. Ensure shebang line exists — without it EC2 may default to /bin/sh
	// and bash-specific syntax (arrays, [[, process substitution) breaks.
	// Only add if script has actual content and no existing shebang.
	trimmed := strings.TrimSpace(fixed)
	if len(trimmed) > 0 && !strings.HasPrefix(trimmed, "#!") {
		fixed = "#!/bin/bash\n" + fixed
		count++
	}

	// 1. Fix common path typos
	for _, typo := range pathTypoFixes {
		if strings.Contains(fixed, typo.broken) {
			fixed = strings.ReplaceAll(fixed, typo.broken, typo.fixed)
			count++
		}
	}

	// 2. Fix garbled ECR login lines — replace with correct command.
	// Only fires when: (a) script references ECR, AND (b) the matched
	// line contains actual non-ASCII bytes (not just a typo like 'awd').
	// This avoids false positives on scripts that happen to contain
	// similar-looking ASCII strings.
	if strings.Contains(strings.ToLower(fixed), ".dkr.ecr.") {
		matches := garbledECRLoginRe.FindAllString(fixed, -1)
		for _, m := range matches {
			if hasNonASCII(m) {
				region := extractRegionFromScript(fixed)
				account := extractAccountFromScript(fixed)
				replacement := buildECRLoginLine(region, account)
				fixed = strings.Replace(fixed, m, replacement, 1)
				count++
			}
		}
	}

	// 3. Fix chmod on wrong path (often a side-effect of path typos already fixed above)
	// No-op if path typos were already fixed in step 1

	// 4. amazon-linux-extras install docker → yum install -y docker
	// Only when the script ALSO mentions AL2023 or uses dnf/yum elsewhere,
	// confirming it targets AL2023 (where amazon-linux-extras doesn't exist).
	// On AL2 this command is correct, so we don't touch it blindly.
	if strings.Contains(fixed, "amazon-linux-extras install") {
		lower := strings.ToLower(fixed)
		isAL2023 := strings.Contains(lower, "al2023") ||
			strings.Contains(lower, "amazon-linux-2023") ||
			strings.Contains(lower, "dnf ") ||
			(strings.Contains(lower, "yum ") && strings.Contains(lower, "amazon-linux-extras"))
		if isAL2023 {
			fixed = strings.ReplaceAll(fixed, "amazon-linux-extras install docker", "yum install -y docker")
			count++
		}
	}

	return fixed, count
}

// hasNonASCII returns true if the string contains bytes > 127.
// This is the only reliable signal that the LLM garbled a base64 shell line —
// ASCII-only typos like 'awd ecr' are too risky to auto-correct since they
// could match legitimate variable names or comments.
func hasNonASCII(s string) bool {
	for _, ch := range s {
		if ch > 127 {
			return true
		}
	}
	return false
}

// extractRegionFromScript tries to find AWS region in the script
func extractRegionFromScript(script string) string {
	re := regexp.MustCompile(`(?:--region|us-(?:east|west)-\d|eu-(?:west|central|north)-\d|ap-(?:south|northeast|southeast)-\d)[- ]?(\S*)`)
	m := re.FindStringSubmatch(script)
	if len(m) > 0 {
		// try to extract just the region
		regionRe := regexp.MustCompile(`((?:us|eu|ap|ca|sa|me|af)-[a-z]+-\d)`)
		rm := regionRe.FindString(script)
		if rm != "" {
			return rm
		}
	}
	return "us-east-1" // safe default
}

// extractAccountFromScript tries to find AWS account ID in the script
func extractAccountFromScript(script string) string {
	re := regexp.MustCompile(`(\d{12})\.dkr\.ecr\.`)
	m := re.FindStringSubmatch(script)
	if len(m) >= 2 {
		return m[1]
	}
	return "<ACCOUNT_ID>"
}

func buildECRLoginLine(region, account string) string {
	return "aws ecr get-login-password --region " + region +
		" | docker login --username AWS --password-stdin " +
		account + ".dkr.ecr." + region + ".amazonaws.com"
}
