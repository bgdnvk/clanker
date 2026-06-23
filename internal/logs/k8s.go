package logs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func init() { register("k8s", func() Collector { return &k8sCollector{} }) }

// k8sCollector reads pod logs via `kubectl`. Mapping of the generic Options:
//
//	Resource -> kubectl target: a pod name or "deployment/<name>"
//	Service  -> namespace (-n)
//	Region   -> kube context (--context)
//
// Tail is a true live follow (`kubectl logs -f`).
type k8sCollector struct{}

func (c *k8sCollector) Provider() string { return "k8s" }

func (c *k8sCollector) base(opts Options, extra ...string) []string {
	args := append([]string{}, extra...)
	ns := opts.Service
	if ns == "" {
		ns = "default"
	}
	args = append(args, "-n", ns)
	if opts.Region != "" {
		args = append(args, "--context", opts.Region)
	}
	return args
}

func (c *k8sCollector) Sources(ctx context.Context, opts Options) ([]Source, error) {
	out, err := runJSON(ctx, "kubectl", c.base(opts, "get", "pods", "-o", "json"), opts.Env)
	if err != nil {
		return nil, err
	}
	var data struct {
		Items []struct {
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal(out, &data); err != nil {
		return nil, fmt.Errorf("parse pods: %w", err)
	}
	sources := make([]Source, 0, len(data.Items))
	for _, it := range data.Items {
		sources = append(sources, Source{ID: it.Metadata.Name, Kind: "pod", Service: it.Metadata.Namespace, Region: opts.Region})
	}
	return sources, nil
}

func (c *k8sCollector) logArgs(opts Options, follow bool) []string {
	if opts.Resource == "" {
		opts.Resource = "" // validated by caller
	}
	args := c.base(opts, "logs", opts.Resource, "--timestamps")
	if !opts.Since.IsZero() {
		if d := time.Since(opts.Since); d > 0 {
			args = append(args, fmt.Sprintf("--since=%ds", int64(d.Seconds())))
		}
	}
	if opts.Limit > 0 && !follow {
		args = append(args, fmt.Sprintf("--tail=%d", opts.Limit))
	} else if follow {
		args = append(args, "--tail=200")
	}
	if follow {
		args = append(args, "-f")
	}
	return args
}

// parseLine splits a `--timestamps` line ("2026-06-23T12:00:00.000Z message")
// into a timestamp and message. Falls back to now if the prefix isn't a time.
func (c *k8sCollector) parseLine(opts Options, line string) Entry {
	ts := time.Now()
	msg := line
	if idx := strings.IndexByte(line, ' '); idx > 0 {
		if t, err := time.Parse(time.RFC3339Nano, line[:idx]); err == nil {
			ts = t
			msg = line[idx+1:]
		}
	}
	e := NewEntry("k8s", opts.Resource, opts.Resource, msg, ts)
	e.Service = opts.Service
	if opts.Region != "" {
		e.AddLabel("context", opts.Region)
	}
	return e
}

func (c *k8sCollector) Query(ctx context.Context, opts Options, emit Emit) error {
	if opts.Resource == "" {
		return fmt.Errorf("k8s logs require --resource <pod|deployment/name>")
	}
	out, err := runJSON(ctx, "kubectl", c.logArgs(opts, false), opts.Env)
	if err != nil {
		return err
	}
	matcher := newMatcher(opts)
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		e := c.parseLine(opts, line)
		if !matcher.Match(e) {
			continue
		}
		if err := emit(e); err != nil {
			return err
		}
	}
	return nil
}

func (c *k8sCollector) Tail(ctx context.Context, opts Options, emit Emit) error {
	if opts.Resource == "" {
		return fmt.Errorf("k8s logs require --resource <pod|deployment/name>")
	}
	matcher := newMatcher(opts)
	return streamLines(ctx, "kubectl", c.logArgs(opts, true), opts.Env, func(line string) error {
		if strings.TrimSpace(line) == "" {
			return nil
		}
		e := c.parseLine(opts, line)
		if !matcher.Match(e) {
			return nil
		}
		return emit(e)
	})
}
