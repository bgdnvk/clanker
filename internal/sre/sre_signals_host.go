package sre

import (
	"context"
	"strings"
	"time"
)

func collectHostExtended(ctx context.Context) map[string]any {
	out := map[string]any{}

	// --- Systemd failed services ---
	if v, err := runCommandOutput(ctx, 2*time.Second,
		"sh", "-c", "systemctl --failed --no-legend 2>/dev/null || true",
	); err == nil {
		if lines := splitLinesLimited(v, 30); len(lines) > 0 {
			out["failedSystemdServices"] = lines
		}
	}

	// --- TCP connection state summary ---
	if v, err := runCommandOutput(ctx, 2*time.Second,
		"sh", "-c", "ss -s 2>/dev/null || netstat -s 2>/dev/null | head -20 || true",
	); err == nil {
		out["tcpConnectionSummary"] = splitLinesLimited(v, 20)
	}

	// --- Open file descriptors vs limit ---
	if v, err := runCommandOutput(ctx, 1500*time.Millisecond,
		"sh", "-c", "ulimit -n 2>/dev/null || true",
	); err == nil {
		out["fdLimit"] = strings.TrimSpace(v)
	}
	if v, err := runCommandOutput(ctx, 1500*time.Millisecond,
		"sh", "-c", "lsof 2>/dev/null | wc -l | tr -d ' ' || ls /proc/*/fd 2>/dev/null | wc -l | tr -d ' ' || true",
	); err == nil {
		out["openFileDescriptors"] = strings.TrimSpace(v)
	}

	// --- Top 10 memory consumers ---
	if v, err := runCommandOutput(ctx, 2*time.Second,
		"sh", "-c", "ps aux --sort=-%mem 2>/dev/null | head -11 || ps aux 2>/dev/null | sort -nrk 4 | head -11 || true",
	); err == nil {
		out["topMemoryProcesses"] = splitLinesLimited(v, 11)
	}

	// --- Top 10 CPU consumers ---
	if v, err := runCommandOutput(ctx, 2*time.Second,
		"sh", "-c", "ps aux --sort=-%cpu 2>/dev/null | head -11 || ps aux 2>/dev/null | sort -nrk 3 | head -11 || true",
	); err == nil {
		out["topCPUProcesses"] = splitLinesLimited(v, 11)
	}

	// --- NTP sync status ---
	if v, err := runCommandOutput(ctx, 2*time.Second,
		"sh", "-c", "timedatectl status 2>/dev/null || ntpstat 2>/dev/null || true",
	); err == nil {
		out["ntpStatus"] = splitLinesLimited(v, 10)
	}

	// --- Swap usage ---
	if v, err := runCommandOutput(ctx, 1500*time.Millisecond,
		"sh", "-c", "swapon --show 2>/dev/null || cat /proc/swaps 2>/dev/null || true",
	); err == nil {
		out["swapUsage"] = splitLinesLimited(v, 10)
	}

	// --- CPU load averages (parsed from uptime) ---
	if v, err := runCommandOutput(ctx, 1500*time.Millisecond,
		"sh", "-c", "uptime 2>/dev/null | grep -oE 'load average.*' || true",
	); err == nil {
		out["loadAverage"] = strings.TrimSpace(v)
	}

	// --- Active SSH sessions ---
	if v, err := runCommandOutput(ctx, 1500*time.Millisecond,
		"sh", "-c", "who 2>/dev/null || true",
	); err == nil {
		if lines := splitLinesLimited(v, 20); len(lines) > 0 {
			out["activeSessions"] = lines
		}
	}

	return out
}

// collectDockerExtended gathers extra Docker-level signals.
