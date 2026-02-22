package wordpress

import (
	"fmt"
	"io"
	"strings"
)

const (
	DefaultPort = 80
)

func Detect(question, repoURL string) bool {
	repoURL = strings.ToLower(strings.TrimSpace(repoURL))
	q := strings.ToLower(strings.TrimSpace(question))

	// Strict: only the official repo.
	if strings.Contains(repoURL, "github.com/docker-library/wordpress") || strings.Contains(repoURL, "docker-library/wordpress") {
		return true
	}
	if strings.Contains(q, "github.com/docker-library/wordpress") || strings.Contains(q, "docker-library/wordpress") {
		return true
	}
	return false
}

func WPContainerName(bindings map[string]string) string {
	deployID := strings.TrimSpace(bindings["DEPLOY_ID"])
	if deployID == "" {
		return "wordpress"
	}
	return "wordpress-" + deployID
}

func DBContainerName(bindings map[string]string) string {
	deployID := strings.TrimSpace(bindings["DEPLOY_ID"])
	if deployID == "" {
		return "wordpress-db"
	}
	return "wordpress-db-" + deployID
}

func MaybePrintPostDeployInstructions(bindings map[string]string, w io.Writer, question, repoURL string) {
	if w == nil {
		return
	}
	if !Detect(question, repoURL) {
		return
	}

	albDNS := strings.TrimSpace(bindings["ALB_DNS"])
	if albDNS == "" {
		albDNS = strings.TrimSpace(bindings["CLOUDFRONT_DOMAIN"])
	}
	if albDNS == "" {
		return
	}

	_, _ = fmt.Fprintf(w, "[wordpress] url: http://%s/\n", albDNS)
	_, _ = fmt.Fprintf(w, "[wordpress] login: http://%s/wp-login.php\n", albDNS)
}
