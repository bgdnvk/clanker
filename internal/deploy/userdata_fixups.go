package deploy

import (
	"encoding/base64"
	"strings"

	"github.com/bgdnvk/clanker/internal/maker"
)

// Base64EncodeEC2UserDataScripts ensures any literal user-data scripts are base64 encoded.
// This avoids downstream placeholder scanners misinterpreting heredoc markers (e.g. <<EOF)
// and improves AWS CLI reliability.
func Base64EncodeEC2UserDataScripts(plan *maker.Plan) *maker.Plan {
	if plan == nil || len(plan.Commands) == 0 {
		return plan
	}

	// Copy-on-write: only allocate a new plan if we actually change something.
	var out *maker.Plan
	clone := func() {
		if out != nil {
			return
		}
		out = &maker.Plan{
			Version:   plan.Version,
			CreatedAt: plan.CreatedAt,
			Provider:  plan.Provider,
			Question:  plan.Question,
			Summary:   plan.Summary,
			Notes:     plan.Notes,
			Commands:  make([]maker.Command, len(plan.Commands)),
		}
		for i := range plan.Commands {
			out.Commands[i] = maker.Command{
				Args:     append([]string(nil), plan.Commands[i].Args...),
				Reason:   plan.Commands[i].Reason,
				Produces: plan.Commands[i].Produces,
			}
		}
	}

	looksLikeBase64 := func(v string) bool {
		v = strings.TrimSpace(v)
		if v == "" {
			return false
		}
		if len(v) < 16 {
			return false
		}
		if strings.ContainsAny(v, " \t\r\n") {
			return false
		}
		for _, ch := range v {
			if (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '+' || ch == '/' || ch == '=' {
				continue
			}
			return false
		}
		if len(v)%4 != 0 {
			return false
		}
		decoded, err := base64.StdEncoding.DecodeString(v)
		if err != nil || len(decoded) == 0 {
			return false
		}
		s := strings.TrimSpace(string(decoded))
		return strings.HasPrefix(s, "#!") || strings.HasPrefix(strings.ToLower(s), "#cloud-config")
	}

	looksLikeScript := func(v string) bool {
		v = strings.TrimSpace(v)
		if v == "" {
			return false
		}
		lv := strings.ToLower(v)
		if strings.HasPrefix(v, "#!") || strings.HasPrefix(lv, "#cloud-config") {
			return true
		}
		if strings.Contains(v, "\n") && (strings.Contains(lv, "dnf ") || strings.Contains(lv, "apt ") || strings.Contains(lv, "yum ") || strings.Contains(lv, "docker")) {
			return true
		}
		return false
	}

	for ci := range plan.Commands {
		cmd := plan.Commands[ci]
		if len(cmd.Args) < 2 {
			continue
		}
		if strings.TrimSpace(cmd.Args[0]) != "ec2" || strings.TrimSpace(cmd.Args[1]) != "run-instances" {
			continue
		}

		for ai := 0; ai < len(cmd.Args); ai++ {
			arg := strings.TrimSpace(cmd.Args[ai])
			if arg == "--user-data" && ai+1 < len(cmd.Args) {
				val := cmd.Args[ai+1]
				if looksLikeBase64(val) {
					continue
				}
				if looksLikeScript(val) {
					clone()
					out.Commands[ci].Args[ai+1] = base64.StdEncoding.EncodeToString([]byte(strings.ReplaceAll(val, "\r\n", "\n")))
				}
				continue
			}
			if strings.HasPrefix(arg, "--user-data=") {
				val := strings.TrimPrefix(cmd.Args[ai], "--user-data=")
				if looksLikeBase64(val) {
					continue
				}
				if looksLikeScript(val) {
					clone()
					out.Commands[ci].Args[ai] = "--user-data=" + base64.StdEncoding.EncodeToString([]byte(strings.ReplaceAll(val, "\r\n", "\n")))
				}
				continue
			}
		}
	}

	if out != nil {
		return out
	}
	return plan
}
