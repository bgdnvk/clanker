package aws

import (
	"context"
	"crypto/sha1"
	"fmt"
	"io"
	"strings"
)

type CLIRunner func(ctx context.Context, args []string, stdinBytes []byte, w io.Writer) (string, error)

type CLIExecOptions struct {
	Profile   string
	Region    string
	Writer    io.Writer
	Destroyer bool
}

func ShortStableHash(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	sum := sha1.Sum([]byte(s))
	return fmt.Sprintf("%x", sum)[:6]
}

func EscapeJMES(s string) string {
	// We only embed this inside single quotes in JMESPath.
	return strings.ReplaceAll(s, "'", "\\'")
}
