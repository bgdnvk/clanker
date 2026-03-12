package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// DOInfraSnapshot holds existing Digital Ocean infrastructure
type DOInfraSnapshot struct {
	Droplets    []DODropletInfo `json:"droplets,omitempty"`
	SSHKeys     []DOSSHKeyInfo  `json:"sshKeys,omitempty"`
	Registries  []string        `json:"registries,omitempty"`
	Firewalls   []DOFirewall    `json:"firewalls,omitempty"`
	ReservedIPs []string        `json:"reservedIps,omitempty"`
	VPCs        []DOVPCInfo     `json:"vpcs,omitempty"`
	Summary     string          `json:"summary"`
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

	snap.Summary = buildDOInfraSummary(snap)
	logf("[do-scan] %s", snap.Summary)
	return snap
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
		b.WriteString("  → REUSE an existing SSH key ID in droplet create; do NOT create a new one\n")
	} else {
		b.WriteString("- SSH Keys: NONE — the plan MUST create or import an SSH key before creating droplets\n")
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
		b.WriteString("  → REUSE existing registry; do NOT create a new one\n")
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
