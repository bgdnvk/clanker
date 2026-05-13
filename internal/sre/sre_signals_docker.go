package sre

import (
	"context"
	"time"
)

func collectDockerExtended(ctx context.Context) map[string]any {
	out := map[string]any{}

	// --- Volumes ---
	if v, err := runCommandOutput(ctx, 2*time.Second,
		"docker", "volume", "ls", "--format", "{{.Name}}|{{.Driver}}|{{.Mountpoint}}",
	); err == nil {
		out["volumes"] = splitLinesLimited(v, 40)
	}

	// --- Networks ---
	if v, err := runCommandOutput(ctx, 2*time.Second,
		"docker", "network", "ls", "--format", "{{.Name}}|{{.Driver}}|{{.Scope}}",
	); err == nil {
		out["networks"] = splitLinesLimited(v, 40)
	}

	// --- Images (name, tag, size, age) ---
	if v, err := runCommandOutput(ctx, 2*time.Second,
		"docker", "images", "--format", "{{.Repository}}:{{.Tag}}|{{.Size}}|{{.CreatedAt}}",
	); err == nil {
		out["images"] = splitLinesLimited(v, 60)
	}

	// --- Container inspect for exit-code != 0 (exited containers) ---
	if v, err := runCommandOutput(ctx, 2*time.Second,
		"docker", "ps", "-a", "--filter", "status=exited",
		"--format", "{{.Names}}|{{.Image}}|{{.Status}}|{{.ExitCode}}",
	); err == nil {
		if lines := splitLinesLimited(v, 30); len(lines) > 0 {
			out["exitedContainers"] = lines
		}
	}

	return out
}

// --- time helpers ---
