package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// DOInfraSnapshot holds existing Digital Ocean infrastructure
type DOInfraSnapshot struct {
	Droplets       []DODropletInfo `json:"droplets,omitempty"`
	SSHKeys        []DOSSHKeyInfo  `json:"sshKeys,omitempty"`
	LocalSSHPubKey string          `json:"localSshPubKey,omitempty"` // best local ~/.ssh/*.pub path
	Registries     []string        `json:"registries,omitempty"`
	Firewalls      []DOFirewall    `json:"firewalls,omitempty"`
	ReservedIPs    []string        `json:"reservedIps,omitempty"`
	VPCs           []DOVPCInfo     `json:"vpcs,omitempty"`
	Summary        string          `json:"summary"`
}

// DODropletInfo is a droplet summary
type DODropletInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Region string `json:"region"`
	Status string `json:"status"`
}

// DOSSHKeyInfo is an SSH key summary
type DOSSHKeyInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// DOFirewall is a firewall summary
type DOFirewall struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// DOVPCInfo is a VPC summary
type DOVPCInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Region string `json:"region"`
}

// ScanDOInfra queries the DO account via doctl to see what's already there.
// Fails gracefully — returns partial snapshot on individual command failures.
func ScanDOInfra(ctx context.Context, apiToken string, logf func(string, ...any)) *DOInfraSnapshot {
	snap := &DOInfraSnapshot{}
	logf("[do-scan] scanning existing DigitalOcean infrastructure...")

	// SSH keys — critical for droplet creation
	if out := doctlCLI(ctx, apiToken, "compute", "ssh-key", "list", "--output", "json"); out != "" {
		var keys []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal([]byte(out), &keys); err == nil {
			for _, k := range keys {
				snap.SSHKeys = append(snap.SSHKeys, DOSSHKeyInfo{
					ID:   fmt.Sprintf("%d", k.ID),
					Name: k.Name,
				})
			}
		}
	}

	// Droplets
	if out := doctlCLI(ctx, apiToken, "compute", "droplet", "list", "--output", "json"); out != "" {
		var droplets []struct {
			ID     int    `json:"id"`
			Name   string `json:"name"`
			Status string `json:"status"`
			Region struct {
				Slug string `json:"slug"`
			} `json:"region"`
		}
		if err := json.Unmarshal([]byte(out), &droplets); err == nil {
			for _, d := range droplets {
				snap.Droplets = append(snap.Droplets, DODropletInfo{
					ID:     fmt.Sprintf("%d", d.ID),
					Name:   d.Name,
					Region: d.Region.Slug,
					Status: d.Status,
				})
			}
		}
	}

	// Container registry (one per account)
	if out := doctlCLI(ctx, apiToken, "registry", "get", "--output", "json"); out != "" {
		var reg struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal([]byte(out), &reg); err == nil && reg.Name != "" {
			snap.Registries = append(snap.Registries, reg.Name)
		}
	}

	// Firewalls
	if out := doctlCLI(ctx, apiToken, "compute", "firewall", "list", "--output", "json"); out != "" {
		var fws []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal([]byte(out), &fws); err == nil {
			for _, fw := range fws {
				snap.Firewalls = append(snap.Firewalls, DOFirewall{ID: fw.ID, Name: fw.Name})
			}
		}
	}

	// Reserved IPs
	if out := doctlCLI(ctx, apiToken, "compute", "reserved-ip", "list", "--output", "json"); out != "" {
		var ips []struct {
			IP string `json:"ip"`
		}
		if err := json.Unmarshal([]byte(out), &ips); err == nil {
			for _, ip := range ips {
				snap.ReservedIPs = append(snap.ReservedIPs, ip.IP)
			}
		}
	}

	// VPCs
	if out := doctlCLI(ctx, apiToken, "vpcs", "list", "--output", "json"); out != "" {
		var vpcs []struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Region string `json:"region_slug"`
		}
		if err := json.Unmarshal([]byte(out), &vpcs); err == nil {
			for _, v := range vpcs {
				snap.VPCs = append(snap.VPCs, DOVPCInfo{ID: v.ID, Name: v.Name, Region: v.Region})
			}
		}
	}

	// Detect local SSH public key when no DO keys exist
	if len(snap.SSHKeys) == 0 {
		snap.LocalSSHPubKey = detectLocalSSHPubKey()
	}

	snap.Summary = buildDOInfraSummary(snap)
	logf("[do-scan] %s", snap.Summary)
	return snap
}

// detectLocalSSHPubKey finds the first local ~/.ssh/*.pub file.
func detectLocalSSHPubKey() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	matches, _ := filepath.Glob(filepath.Join(home, ".ssh", "*.pub"))
	if len(matches) > 0 {
		return "~/.ssh/" + filepath.Base(matches[0])
	}
	return ""
}

// FormatForPrompt formats the DO infra snapshot for the LLM prompt
func (s *DOInfraSnapshot) FormatForPrompt() string {
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
		if s.LocalSSHPubKey != "" {
			b.WriteString("  → Prefer a fresh deployment-scoped SSH key import instead of reusing an existing account key or local public key\n")
			b.WriteString("  → Use 'compute ssh-key import <name> --public-key-file <path>' as the FIRST step; the executor can generate fresh local key material and then bind SSH_KEY_ID into droplet create\n")
		} else {
			b.WriteString("  → Prefer creating/importing a fresh deployment-scoped SSH key instead of reusing an unrelated account-wide key\n")
		}
	} else {
		if s.LocalSSHPubKey != "" {
			b.WriteString("- SSH Keys: NONE on DigitalOcean — start with compute ssh-key import; a fresh deployment-scoped key can be generated locally even when another local public key already exists\n")
			b.WriteString("  → Use 'compute ssh-key import <name> --public-key-file <path>' as the FIRST step\n")
		} else {
			b.WriteString("- SSH Keys: NONE — no local ~/.ssh/*.pub found either; the deployment should still start with compute ssh-key import because the executor can generate a fresh local key pair first\n")
		}
	}

	if len(s.Droplets) > 0 {
		names := make([]string, 0, len(s.Droplets))
		for _, d := range s.Droplets {
			names = append(names, fmt.Sprintf("%s (%s, %s)", d.Name, d.Region, d.Status))
		}
		b.WriteString(fmt.Sprintf("- Existing Droplets: %s\n", strings.Join(names, ", ")))
		b.WriteString("  → Avoid creating droplets with duplicate names\n")
	}

	if len(s.Registries) > 0 {
		b.WriteString(fmt.Sprintf("- Container Registry: %s\n", strings.Join(s.Registries, ", ")))
		b.WriteString("  → DigitalOcean supports only one registry per account/team; reuse this registry when present, but prefer fresh repository names inside it for each deploy\n")
	}

	if len(s.Firewalls) > 0 {
		names := make([]string, 0, len(s.Firewalls))
		for _, fw := range s.Firewalls {
			names = append(names, fmt.Sprintf("%s (id=%s)", fw.Name, fw.ID))
		}
		b.WriteString(fmt.Sprintf("- Existing Firewalls: %s\n", strings.Join(names, ", ")))
		b.WriteString("  → Avoid creating firewalls with duplicate names\n")
	}

	if len(s.ReservedIPs) > 0 {
		b.WriteString(fmt.Sprintf("- Reserved IPs: %s\n", strings.Join(s.ReservedIPs, ", ")))
	}

	if len(s.VPCs) > 0 {
		names := make([]string, 0, len(s.VPCs))
		for _, v := range s.VPCs {
			names = append(names, fmt.Sprintf("%s (%s)", v.Name, v.Region))
		}
		b.WriteString(fmt.Sprintf("- VPCs: %s\n", strings.Join(names, ", ")))
	}

	return b.String()
}

func buildDOInfraSummary(s *DOInfraSnapshot) string {
	parts := []string{}
	if len(s.SSHKeys) > 0 {
		parts = append(parts, fmt.Sprintf("%d SSH keys", len(s.SSHKeys)))
	} else {
		parts = append(parts, "no SSH keys")
	}
	if len(s.Droplets) > 0 {
		parts = append(parts, fmt.Sprintf("%d droplets", len(s.Droplets)))
	}
	if len(s.Registries) > 0 {
		parts = append(parts, fmt.Sprintf("registry: %s", strings.Join(s.Registries, ", ")))
	}
	if len(s.Firewalls) > 0 {
		parts = append(parts, fmt.Sprintf("%d firewalls", len(s.Firewalls)))
	}
	if len(s.ReservedIPs) > 0 {
		parts = append(parts, fmt.Sprintf("%d reserved IPs", len(s.ReservedIPs)))
	}
	if len(s.VPCs) > 0 {
		parts = append(parts, fmt.Sprintf("%d VPCs", len(s.VPCs)))
	}
	if len(parts) == 0 {
		return "no existing DigitalOcean infrastructure detected"
	}
	return strings.Join(parts, " • ")
}

// doctlCLI runs a doctl command with the given token, returns stdout or empty on error
func doctlCLI(ctx context.Context, token string, args ...string) string {
	tctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	fullArgs := append([]string{"--access-token", token}, args...)
	cmd := exec.CommandContext(tctx, "doctl", fullArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
