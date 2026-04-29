package cost

import (
	"fmt"
	"strconv"
	"strings"
)

// parseCPUQuantity converts a Kubernetes CPU quantity string to cores.
// Accepts plain integers/floats ("1", "0.5", "2.5") and milli-CPU
// suffixes ("100m" → 0.1, "1500m" → 1.5).
func parseCPUQuantity(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	if strings.HasSuffix(s, "m") {
		n, err := strconv.ParseFloat(strings.TrimSuffix(s, "m"), 64)
		if err != nil {
			return 0, fmt.Errorf("parse CPU milli %q: %w", s, err)
		}
		return n / 1000.0, nil
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("parse CPU %q: %w", s, err)
	}
	return n, nil
}

// parseMemoryQuantity converts a Kubernetes memory quantity string to MiB.
// Supports binary suffixes (Ki, Mi, Gi, Ti, Pi, Ei) and decimal suffixes
// (k, K, M, G, T, P, E). Plain integers are interpreted as bytes.
func parseMemoryQuantity(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	// Order matters: check binary (i-suffixed) before decimal so "Mi"
	// doesn't get matched as "M".
	binarySuffixes := []struct {
		suffix string
		// MiB per unit.
		mib float64
	}{
		{"Ki", 1.0 / 1024.0},
		{"Mi", 1.0},
		{"Gi", 1024.0},
		{"Ti", 1024.0 * 1024.0},
		{"Pi", 1024.0 * 1024.0 * 1024.0},
		{"Ei", 1024.0 * 1024.0 * 1024.0 * 1024.0},
	}
	for _, sx := range binarySuffixes {
		if strings.HasSuffix(s, sx.suffix) {
			n, err := strconv.ParseFloat(strings.TrimSuffix(s, sx.suffix), 64)
			if err != nil {
				return 0, fmt.Errorf("parse memory %q: %w", s, err)
			}
			return n * sx.mib, nil
		}
	}
	decimalSuffixes := []struct {
		suffix string
		mib    float64
	}{
		{"k", 1000.0 / (1024.0 * 1024.0)},
		{"K", 1000.0 / (1024.0 * 1024.0)},
		{"M", 1000.0 * 1000.0 / (1024.0 * 1024.0)},
		{"G", 1000.0 * 1000.0 * 1000.0 / (1024.0 * 1024.0)},
		{"T", 1000.0 * 1000.0 * 1000.0 * 1000.0 / (1024.0 * 1024.0)},
		{"P", 1000.0 * 1000.0 * 1000.0 * 1000.0 * 1000.0 / (1024.0 * 1024.0)},
		{"E", 1000.0 * 1000.0 * 1000.0 * 1000.0 * 1000.0 * 1000.0 / (1024.0 * 1024.0)},
	}
	for _, sx := range decimalSuffixes {
		if strings.HasSuffix(s, sx.suffix) {
			n, err := strconv.ParseFloat(strings.TrimSuffix(s, sx.suffix), 64)
			if err != nil {
				return 0, fmt.Errorf("parse memory %q: %w", s, err)
			}
			return n * sx.mib, nil
		}
	}
	// No suffix → bytes.
	n, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("parse memory %q: %w", s, err)
	}
	return n / (1024.0 * 1024.0), nil
}
