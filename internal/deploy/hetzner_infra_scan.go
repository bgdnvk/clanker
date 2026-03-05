package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// HetznerInfraSnapshot holds existing Hetzner Cloud infrastructure
type HetznerInfraSnapshot struct {
	Servers     []HetznerServerInfo  `json:"servers,omitempty"`
	SSHKeys     []HetznerSSHKeyInfo  `json:"sshKeys,omitempty"`
	Firewalls   []HetznerFirewall    `json:"firewalls,omitempty"`
	FloatingIPs []string             `json:"floatingIps,omitempty"`
	Networks    []HetznerNetworkInfo `json:"networks,omitempty"`
	Volumes     []HetznerVolumeInfo  `json:"volumes,omitempty"`
	Summary     string               `json:"summary"`
}

// HetznerServerInfo is a server summary
type HetznerServerInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Location string `json:"location"`
	Status   string `json:"status"`
}

// HetznerSSHKeyInfo is an SSH key summary
type HetznerSSHKeyInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// HetznerFirewall is a firewall summary
type HetznerFirewall struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// HetznerNetworkInfo is a network summary
type HetznerNetworkInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	IPRange string `json:"ipRange"`
}

// HetznerVolumeInfo is a volume summary
type HetznerVolumeInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Size     int    `json:"size"`
	Location string `json:"location"`
}

// ScanHetznerInfra queries the Hetzner account via hcloud CLI to see what's already there.
// Fails gracefully: returns partial snapshot on individual command failures.
func ScanHetznerInfra(ctx context.Context, apiToken string, logf func(string, ...any)) *HetznerInfraSnapshot {
	snap := &HetznerInfraSnapshot{}
	logf("[hetzner-scan] scanning existing Hetzner Cloud infrastructure...")

	// SSH keys: critical for server creation
	if out := hcloudCLI(ctx, apiToken, "ssh-key", "list", "-o", "json"); out != "" {
		var keys []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal([]byte(out), &keys); err == nil {
			for _, k := range keys {
				snap.SSHKeys = append(snap.SSHKeys, HetznerSSHKeyInfo{
					ID:   fmt.Sprintf("%d", k.ID),
					Name: k.Name,
				})
			}
		}
	}

	// Servers
	if out := hcloudCLI(ctx, apiToken, "server", "list", "-o", "json"); out != "" {
		var servers []struct {
			ID         int    `json:"id"`
			Name       string `json:"name"`
			Status     string `json:"status"`
			Datacenter struct {
				Name     string `json:"name"`
				Location struct {
					Name string `json:"name"`
				} `json:"location"`
			} `json:"datacenter"`
		}
		if err := json.Unmarshal([]byte(out), &servers); err == nil {
			for _, s := range servers {
				snap.Servers = append(snap.Servers, HetznerServerInfo{
					ID:       fmt.Sprintf("%d", s.ID),
					Name:     s.Name,
					Location: s.Datacenter.Location.Name,
					Status:   s.Status,
				})
			}
		}
	}

	// Firewalls
	if out := hcloudCLI(ctx, apiToken, "firewall", "list", "-o", "json"); out != "" {
		var fws []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal([]byte(out), &fws); err == nil {
			for _, fw := range fws {
				snap.Firewalls = append(snap.Firewalls, HetznerFirewall{
					ID:   fmt.Sprintf("%d", fw.ID),
					Name: fw.Name,
				})
			}
		}
	}

	// Floating IPs
	if out := hcloudCLI(ctx, apiToken, "floating-ip", "list", "-o", "json"); out != "" {
		var ips []struct {
			IP string `json:"ip"`
		}
		if err := json.Unmarshal([]byte(out), &ips); err == nil {
			for _, ip := range ips {
				snap.FloatingIPs = append(snap.FloatingIPs, ip.IP)
			}
		}
	}

	// Networks
	if out := hcloudCLI(ctx, apiToken, "network", "list", "-o", "json"); out != "" {
		var nets []struct {
			ID      int    `json:"id"`
			Name    string `json:"name"`
			IPRange string `json:"ip_range"`
		}
		if err := json.Unmarshal([]byte(out), &nets); err == nil {
			for _, n := range nets {
				snap.Networks = append(snap.Networks, HetznerNetworkInfo{
					ID:      fmt.Sprintf("%d", n.ID),
					Name:    n.Name,
					IPRange: n.IPRange,
				})
			}
		}
	}

	// Volumes
	if out := hcloudCLI(ctx, apiToken, "volume", "list", "-o", "json"); out != "" {
		var vols []struct {
			ID       int    `json:"id"`
			Name     string `json:"name"`
			Size     int    `json:"size"`
			Location struct {
				Name string `json:"name"`
			} `json:"location"`
		}
		if err := json.Unmarshal([]byte(out), &vols); err == nil {
			for _, v := range vols {
				snap.Volumes = append(snap.Volumes, HetznerVolumeInfo{
					ID:       fmt.Sprintf("%d", v.ID),
					Name:     v.Name,
					Size:     v.Size,
					Location: v.Location.Name,
				})
			}
		}
	}

	snap.Summary = buildHetznerInfraSummary(snap)
	logf("[hetzner-scan] %s", snap.Summary)
	return snap
}

// FormatForPrompt formats the Hetzner infra snapshot for the LLM prompt
func (s *HetznerInfraSnapshot) FormatForPrompt() string {
	if s == nil {
		return ""
	}

	var b strings.Builder

	if len(s.SSHKeys) > 0 {
		names := make([]string, 0, len(s.SSHKeys))
		for _, k := range s.SSHKeys {
			names = append(names, fmt.Sprintf("%s (id=%s)", k.Name, k.ID))
		}
		b.WriteString(fmt.Sprintf("- SSH Keys: %s\n", strings.Join(names, ", ")))
		b.WriteString("  -> REUSE an existing SSH key ID in server create; do NOT create a new one\n")
	} else {
		b.WriteString("- SSH Keys: NONE - the plan MUST create or import an SSH key before creating servers\n")
	}

	if len(s.Servers) > 0 {
		names := make([]string, 0, len(s.Servers))
		for _, srv := range s.Servers {
			names = append(names, fmt.Sprintf("%s (%s, %s)", srv.Name, srv.Location, srv.Status))
		}
		b.WriteString(fmt.Sprintf("- Existing Servers: %s\n", strings.Join(names, ", ")))
		b.WriteString("  -> Avoid creating servers with duplicate names\n")
	}

	if len(s.Firewalls) > 0 {
		names := make([]string, 0, len(s.Firewalls))
		for _, fw := range s.Firewalls {
			names = append(names, fmt.Sprintf("%s (id=%s)", fw.Name, fw.ID))
		}
		b.WriteString(fmt.Sprintf("- Existing Firewalls: %s\n", strings.Join(names, ", ")))
		b.WriteString("  -> Avoid creating firewalls with duplicate names\n")
	}

	if len(s.FloatingIPs) > 0 {
		b.WriteString(fmt.Sprintf("- Floating IPs: %s\n", strings.Join(s.FloatingIPs, ", ")))
	}

	if len(s.Networks) > 0 {
		names := make([]string, 0, len(s.Networks))
		for _, n := range s.Networks {
			names = append(names, fmt.Sprintf("%s (%s)", n.Name, n.IPRange))
		}
		b.WriteString(fmt.Sprintf("- Networks: %s\n", strings.Join(names, ", ")))
	}

	if len(s.Volumes) > 0 {
		names := make([]string, 0, len(s.Volumes))
		for _, v := range s.Volumes {
			names = append(names, fmt.Sprintf("%s (%dGB, %s)", v.Name, v.Size, v.Location))
		}
		b.WriteString(fmt.Sprintf("- Volumes: %s\n", strings.Join(names, ", ")))
	}

	return b.String()
}

func buildHetznerInfraSummary(s *HetznerInfraSnapshot) string {
	parts := []string{}
	if len(s.SSHKeys) > 0 {
		parts = append(parts, fmt.Sprintf("%d SSH keys", len(s.SSHKeys)))
	} else {
		parts = append(parts, "no SSH keys")
	}
	if len(s.Servers) > 0 {
		parts = append(parts, fmt.Sprintf("%d servers", len(s.Servers)))
	}
	if len(s.Firewalls) > 0 {
		parts = append(parts, fmt.Sprintf("%d firewalls", len(s.Firewalls)))
	}
	if len(s.FloatingIPs) > 0 {
		parts = append(parts, fmt.Sprintf("%d floating IPs", len(s.FloatingIPs)))
	}
	if len(s.Networks) > 0 {
		parts = append(parts, fmt.Sprintf("%d networks", len(s.Networks)))
	}
	if len(s.Volumes) > 0 {
		parts = append(parts, fmt.Sprintf("%d volumes", len(s.Volumes)))
	}
	if len(parts) == 0 {
		return "no existing Hetzner Cloud infrastructure detected"
	}
	return strings.Join(parts, " - ")
}

// hcloudCLI runs an hcloud command with the given token, returns stdout or empty on error
func hcloudCLI(ctx context.Context, token string, args ...string) string {
	tctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(tctx, "hcloud", args...)
	cmd.Env = append(cmd.Environ(), "HCLOUD_TOKEN="+token)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
