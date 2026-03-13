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

// GenerateMissingUserData adds user-data to ec2 run-instances commands that have
// empty or placeholder user-data but are part of a Docker/ECR deployment.
// This prevents validation failures that would trigger expensive paged fallback.
func GenerateMissingUserData(plan *maker.Plan, logf func(string, ...any)) *maker.Plan {
	if plan == nil || len(plan.Commands) == 0 {
		return plan
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}

	// Check if this is a Docker/ECR deployment
	hasECR := false
	region := "us-east-1"
	for _, cmd := range plan.Commands {
		argsJoined := strings.ToLower(strings.Join(cmd.Args, " "))
		if strings.Contains(argsJoined, "ecr") {
			hasECR = true
		}
		// Extract region from any command
		for i, arg := range cmd.Args {
			if arg == "--region" && i+1 < len(cmd.Args) {
				region = cmd.Args[i+1]
			}
		}
	}

	if !hasECR {
		return plan
	}

	generated := 0
	for ci := range plan.Commands {
		cmd := &plan.Commands[ci]
		if len(cmd.Args) < 2 {
			continue
		}
		if strings.TrimSpace(cmd.Args[0]) != "ec2" || strings.TrimSpace(cmd.Args[1]) != "run-instances" {
			continue
		}

		// Find user-data argument
		userDataIdx := -1
		userDataVal := ""
		for ai := 0; ai < len(cmd.Args); ai++ {
			arg := strings.TrimSpace(cmd.Args[ai])
			if arg == "--user-data" && ai+1 < len(cmd.Args) {
				userDataIdx = ai + 1
				userDataVal = cmd.Args[ai+1]
				break
			}
			if strings.HasPrefix(arg, "--user-data=") {
				userDataIdx = ai
				userDataVal = strings.TrimPrefix(arg, "--user-data=")
				break
			}
		}

		// Check if user-data is empty or a placeholder
		needsGeneration := false
		if userDataIdx < 0 {
			// No user-data flag at all - need to add one
			needsGeneration = true
		} else {
			decoded, _ := tryDecodeBase64UserData(userDataVal)
			trimmed := strings.TrimSpace(decoded)
			if trimmed == "" || isUserDataPlaceholder(trimmed) || isUserDataPlaceholder(userDataVal) {
				needsGeneration = true
			}
		}

		if !needsGeneration {
			continue
		}

		// Generate a fallback Docker bootstrap script
		script := generateDockerBootstrapScript(region)
		encoded := base64.StdEncoding.EncodeToString([]byte(script))

		if userDataIdx < 0 {
			// Insert --user-data before --profile (or at end)
			insertIdx := len(cmd.Args)
			for ai, arg := range cmd.Args {
				if arg == "--profile" {
					insertIdx = ai
					break
				}
			}
			newArgs := make([]string, 0, len(cmd.Args)+2)
			newArgs = append(newArgs, cmd.Args[:insertIdx]...)
			newArgs = append(newArgs, "--user-data", encoded)
			newArgs = append(newArgs, cmd.Args[insertIdx:]...)
			cmd.Args = newArgs
		} else if strings.HasPrefix(cmd.Args[userDataIdx], "--user-data=") {
			cmd.Args[userDataIdx] = "--user-data=" + encoded
		} else {
			cmd.Args[userDataIdx] = encoded
		}
		generated++
	}

	if generated > 0 {
		logf("[deploy] user-data autofix: generated Docker bootstrap script for %d run-instances command(s)", generated)
	}
	return plan
}

func isUserDataPlaceholder(s string) bool {
	s = strings.TrimSpace(strings.ToUpper(s))
	return s == "" ||
		s == "<USER_DATA>" ||
		s == "$USER_DATA" ||
		s == "<USER-DATA>" ||
		strings.Contains(s, "<USER_DATA>") ||
		strings.Contains(s, "$USER_DATA")
}

func generateDockerBootstrapScript(region string) string {
	return `#!/bin/bash
set -e
exec > /var/log/user-data.log 2>&1

echo '[bootstrap] Docker bootstrap script (auto-generated)'

# Install AWS CLI if needed
if ! command -v aws >/dev/null 2>&1; then
    . /etc/os-release || true
    if [ "${ID:-}" = "amzn" ]; then
        if command -v dnf >/dev/null 2>&1; then dnf install -y awscli; else yum install -y awscli; fi
    elif command -v apt-get >/dev/null 2>&1; then
        apt-get update -y && apt-get install -y awscli
    fi
fi

# Install Docker
. /etc/os-release || true
if [ "${ID:-}" = "amzn" ]; then
    if command -v dnf >/dev/null 2>&1; then
        dnf install -y docker
    else
        yum install -y docker
    fi
elif command -v apt-get >/dev/null 2>&1; then
    apt-get update -y && apt-get install -y docker.io
fi

systemctl enable docker
systemctl start docker

# Get instance metadata
REGION=$(curl -s http://169.254.169.254/latest/meta-data/placement/region 2>/dev/null || echo "` + region + `")
INSTANCE_ID=$(curl -s http://169.254.169.254/latest/meta-data/instance-id 2>/dev/null || echo "")
ACCOUNT=$(aws sts get-caller-identity --query Account --output text 2>/dev/null || echo "")

# ECR authentication
if [ -n "$ACCOUNT" ]; then
    echo "[bootstrap] Authenticating with ECR in $REGION"
    for i in 1 2 3 4 5; do
        if aws ecr get-login-password --region "$REGION" | docker login --username AWS --password-stdin "$ACCOUNT.dkr.ecr.$REGION.amazonaws.com" 2>/dev/null; then
            echo "[bootstrap] ECR login succeeded (attempt=$i)"
            break
        fi
        echo "[bootstrap] ECR login attempt $i failed; retrying..."
        sleep $((i*3))
    done
fi

# Try to discover image URI from instance tags
IMAGE_URI=""
if [ -n "$INSTANCE_ID" ]; then
    IMAGE_URI=$(aws ec2 describe-tags --region "$REGION" --filters "Name=resource-id,Values=$INSTANCE_ID" "Name=key,Values=ImageUri" --query 'Tags[0].Value' --output text 2>/dev/null || echo "")
    [ "$IMAGE_URI" = "None" ] && IMAGE_URI=""
fi

# If no tag, try to find latest image in clanker-app repo
if [ -z "$IMAGE_URI" ] && [ -n "$ACCOUNT" ]; then
    echo "[bootstrap] No ImageUri tag found, discovering from ECR..."
    for REPO in clanker-app app; do
        LATEST=$(aws ecr describe-images --region "$REGION" --repository-name "$REPO" --query 'sort_by(imageDetails,&imagePushedAt)[-1].imageTags[0]' --output text 2>/dev/null || echo "")
        if [ -n "$LATEST" ] && [ "$LATEST" != "None" ]; then
            IMAGE_URI="$ACCOUNT.dkr.ecr.$REGION.amazonaws.com/$REPO:$LATEST"
            echo "[bootstrap] Discovered image: $IMAGE_URI"
            break
        fi
    done
fi

# If still no image, wait for one to appear (max 10 minutes)
if [ -z "$IMAGE_URI" ] && [ -n "$ACCOUNT" ]; then
    echo "[bootstrap] Waiting for image to be pushed to ECR..."
    for attempt in $(seq 1 20); do
        for REPO in clanker-app app; do
            LATEST=$(aws ecr describe-images --region "$REGION" --repository-name "$REPO" --query 'sort_by(imageDetails,&imagePushedAt)[-1].imageTags[0]' --output text 2>/dev/null || echo "")
            if [ -n "$LATEST" ] && [ "$LATEST" != "None" ]; then
                IMAGE_URI="$ACCOUNT.dkr.ecr.$REGION.amazonaws.com/$REPO:$LATEST"
                echo "[bootstrap] Found image after waiting: $IMAGE_URI"
                break 2
            fi
        done
        echo "[bootstrap] No image yet (attempt $attempt/20), waiting 30s..."
        sleep 30
    done
fi

if [ -z "$IMAGE_URI" ]; then
    echo "[bootstrap] ERROR: No image found in ECR after waiting. Container not started."
    echo "[bootstrap] To start manually: docker pull <IMAGE_URI> && docker run -d -p 3000:3000 <IMAGE_URI>"
    exit 0
fi

# Get app name from instance tags for SSM parameter lookup
APP_NAME=""
if [ -n "$INSTANCE_ID" ]; then
    APP_NAME=$(aws ec2 describe-tags --region "$REGION" --filters "Name=resource-id,Values=$INSTANCE_ID" "Name=key,Values=AppName" --query 'Tags[0].Value' --output text 2>/dev/null || echo "")
    [ "$APP_NAME" = "None" ] && APP_NAME=""
    # Fallback to Name tag
    if [ -z "$APP_NAME" ]; then
        APP_NAME=$(aws ec2 describe-tags --region "$REGION" --filters "Name=resource-id,Values=$INSTANCE_ID" "Name=key,Values=Name" --query 'Tags[0].Value' --output text 2>/dev/null || echo "")
        [ "$APP_NAME" = "None" ] && APP_NAME=""
    fi
fi

# Get environment variables from instance tags (ENV_* pattern)
ENV_FLAGS=""
if [ -n "$INSTANCE_ID" ]; then
    echo "[bootstrap] Loading environment variables from instance tags..."
    ENV_TAGS=$(aws ec2 describe-tags --region "$REGION" --filters "Name=resource-id,Values=$INSTANCE_ID" --query 'Tags[?starts_with(Key, ` + "`ENV_`" + `)].{Key:Key,Value:Value}' --output text 2>/dev/null || echo "")
    while IFS=$'\t' read -r KEY VALUE; do
        if [ -n "$KEY" ]; then
            # Strip ENV_ prefix
            ENV_NAME="${KEY#ENV_}"
            ENV_FLAGS="$ENV_FLAGS -e $ENV_NAME=\"$VALUE\""
            echo "[bootstrap] Loaded env: $ENV_NAME"
        fi
    done <<< "$ENV_TAGS"
fi

# Load secrets from SSM Parameter Store
# Tries multiple paths: /clanker/<app>/, /<app>/, /app/<app>/
echo "[bootstrap] Loading secrets from SSM Parameter Store..."
for SSM_PATH in "/clanker/${APP_NAME:-app}/" "/${APP_NAME:-app}/" "/app/${APP_NAME:-app}/" "/clanker/"; do
    PARAMS=$(aws ssm get-parameters-by-path --region "$REGION" --path "$SSM_PATH" --with-decryption --query 'Parameters[*].[Name,Value]' --output text 2>/dev/null || echo "")
    if [ -n "$PARAMS" ]; then
        echo "[bootstrap] Found parameters in $SSM_PATH"
        while IFS=$'\t' read -r PARAM_NAME PARAM_VALUE; do
            if [ -n "$PARAM_NAME" ]; then
                # Extract just the parameter name (last part of path)
                ENV_NAME=$(basename "$PARAM_NAME")
                # Convert to uppercase and replace dashes with underscores
                ENV_NAME=$(echo "$ENV_NAME" | tr '[:lower:]-' '[:upper:]_')
                # Escape special characters for shell
                ESCAPED_VALUE=$(printf '%s' "$PARAM_VALUE" | sed 's/"/\\"/g; s/\$/\\$/g; s/` + "`" + `/\\` + "`" + `/g')
                ENV_FLAGS="$ENV_FLAGS -e $ENV_NAME=\"$ESCAPED_VALUE\""
                echo "[bootstrap] Loaded secret: $ENV_NAME"
            fi
        done <<< "$PARAMS"
        break
    fi
done

# Load secrets from AWS Secrets Manager
# The LLM creates individual secrets with names like: <app>/DATABASE_URL, <app>/API_KEY
# We need to list all secrets with the app prefix and fetch each one
echo "[bootstrap] Loading secrets from AWS Secrets Manager..."

# Derive app prefix from Name tag (e.g., "myapp-server" -> "myapp")
SECRET_PREFIX="${APP_NAME%%-*}"
[ -z "$SECRET_PREFIX" ] && SECRET_PREFIX="$APP_NAME"

# List and fetch secrets with matching prefixes
for PREFIX in "$SECRET_PREFIX" "$APP_NAME" "clanker-app"; do
    if [ -z "$PREFIX" ]; then continue; fi

    echo "[bootstrap] Checking for secrets with prefix: $PREFIX/"
    SECRET_LIST=$(aws secretsmanager list-secrets --region "$REGION" --filters "Key=name,Values=$PREFIX/" --query 'SecretList[*].Name' --output text 2>/dev/null || echo "")

    if [ -n "$SECRET_LIST" ] && [ "$SECRET_LIST" != "None" ]; then
        echo "[bootstrap] Found secrets with prefix $PREFIX/"
        for SECRET_NAME in $SECRET_LIST; do
            [ -z "$SECRET_NAME" ] && continue
            [ "$SECRET_NAME" = "None" ] && continue

            # Fetch the secret value
            SECRET_VALUE=$(aws secretsmanager get-secret-value --region "$REGION" --secret-id "$SECRET_NAME" --query 'SecretString' --output text 2>/dev/null || echo "")
            if [ -n "$SECRET_VALUE" ] && [ "$SECRET_VALUE" != "None" ]; then
                # Extract env var name from secret name (e.g., "myapp/DATABASE_URL" -> "DATABASE_URL")
                ENV_NAME=$(basename "$SECRET_NAME")
                ENV_NAME=$(echo "$ENV_NAME" | tr '[:lower:]-' '[:upper:]_')

                # Check if secret is JSON (starts with {) or plain string
                if echo "$SECRET_VALUE" | grep -q '^{'; then
                    # JSON secret - extract all key-value pairs
                    if command -v jq >/dev/null 2>&1; then
                        for KEY in $(echo "$SECRET_VALUE" | jq -r 'keys[]' 2>/dev/null); do
                            VALUE=$(echo "$SECRET_VALUE" | jq -r --arg k "$KEY" '.[$k]' 2>/dev/null)
                            K_ENV=$(echo "$KEY" | tr '[:lower:]-' '[:upper:]_')
                            ESCAPED=$(printf '%s' "$VALUE" | sed 's/"/\\"/g; s/\$/\\$/g; s/` + "`" + `/\\` + "`" + `/g')
                            ENV_FLAGS="$ENV_FLAGS -e $K_ENV=\"$ESCAPED\""
                            echo "[bootstrap] Loaded secret (json): $K_ENV"
                        done
                    fi
                else
                    # Plain string secret
                    ESCAPED_VALUE=$(printf '%s' "$SECRET_VALUE" | sed 's/"/\\"/g; s/\$/\\$/g; s/` + "`" + `/\\` + "`" + `/g')
                    ENV_FLAGS="$ENV_FLAGS -e $ENV_NAME=\"$ESCAPED_VALUE\""
                    echo "[bootstrap] Loaded secret: $ENV_NAME"
                fi
            fi
        done
        break
    fi
done

# Fallback: check for a single JSON secret named after the app (legacy pattern)
if [ -z "$ENV_FLAGS" ] && [ -n "$APP_NAME" ]; then
    for SECRET_NAME in "$APP_NAME" "clanker/$APP_NAME" "$SECRET_PREFIX"; do
        [ -z "$SECRET_NAME" ] && continue
        SECRET_JSON=$(aws secretsmanager get-secret-value --region "$REGION" --secret-id "$SECRET_NAME" --query 'SecretString' --output text 2>/dev/null || echo "")
        if [ -n "$SECRET_JSON" ] && [ "$SECRET_JSON" != "None" ] && echo "$SECRET_JSON" | grep -q '^{'; then
            echo "[bootstrap] Found JSON secret: $SECRET_NAME"
            if command -v jq >/dev/null 2>&1; then
                for KEY in $(echo "$SECRET_JSON" | jq -r 'keys[]' 2>/dev/null); do
                    VALUE=$(echo "$SECRET_JSON" | jq -r --arg k "$KEY" '.[$k]' 2>/dev/null)
                    ENV_NAME=$(echo "$KEY" | tr '[:lower:]-' '[:upper:]_')
                    ESCAPED_VALUE=$(printf '%s' "$VALUE" | sed 's/"/\\"/g; s/\$/\\$/g; s/` + "`" + `/\\` + "`" + `/g')
                    ENV_FLAGS="$ENV_FLAGS -e $ENV_NAME=\"$ESCAPED_VALUE\""
                    echo "[bootstrap] Loaded secret: $ENV_NAME"
                done
            fi
            break
        fi
    done
fi

# Get app port from tags or default to 3000
APP_PORT=$(aws ec2 describe-tags --region "$REGION" --filters "Name=resource-id,Values=$INSTANCE_ID" "Name=key,Values=AppPort" --query 'Tags[0].Value' --output text 2>/dev/null || echo "3000")
[ "$APP_PORT" = "None" ] && APP_PORT="3000"

echo "[bootstrap] Pulling image: $IMAGE_URI"
docker pull "$IMAGE_URI"

echo "[bootstrap] Starting container on port $APP_PORT"
eval "docker run -d --restart unless-stopped -p $APP_PORT:$APP_PORT $ENV_FLAGS $IMAGE_URI"

echo "[bootstrap] Container started successfully"
docker ps
`
}
