package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/bgdnvk/clanker/internal/logs"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// errCollectLimit stops collection once the chat cap is reached (a deliberate
// early exit, distinct from a cancelled context).
var errCollectLimit = errors.New("collect limit reached")

// newLogsCmd builds `clanker logs`, the provider-agnostic log surface the cloud
// app drives. `query`/`tail` stream normalized JSON-lines on stdout (progress
// on stderr); `chat` runs the agentic talk-to-logs flow; `sources` discovers
// available log sources.
func newLogsCmd() *cobra.Command {
	var (
		provider  string
		resource  string
		service   string
		region    string
		since     string
		until     string
		level     string
		grep      string
		limit     int
		format    string
		profile   string
		aiProfile string
	)

	buildOpts := func() (logs.Options, error) {
		now := time.Now()
		sinceT, err := logs.ParseSince(since, now)
		if err != nil {
			return logs.Options{}, err
		}
		opts := logs.Options{
			Provider: provider,
			Resource: resource,
			Service:  service,
			Region:   region,
			Since:    sinceT,
			Level:    level,
			Grep:     grep,
			Limit:    limit,
			Profile:  profile,
		}
		if strings.TrimSpace(until) != "" {
			t, err := time.Parse(time.RFC3339, until)
			if err != nil {
				return logs.Options{}, fmt.Errorf("invalid --until %q: use an RFC3339 timestamp", until)
			}
			opts.Until = t
		}
		return opts, nil
	}

	logsCmd := &cobra.Command{
		Use:   "logs",
		Short: "Unified multi-provider logs: query, tail, and chat across clouds",
		Long: `Query, stream, and chat with logs across providers (aws, k8s, flyio, vercel,
railway) through one normalized interface. query/tail emit JSON-lines; chat runs
the talk-to-logs agent.`,
	}

	persistent := logsCmd.PersistentFlags()
	persistent.StringVar(&provider, "provider", "", "log provider: "+strings.Join(logs.Providers(), ", "))
	persistent.StringVar(&resource, "resource", "", "resource to read (log group / pod / app / deployment)")
	persistent.StringVar(&service, "service", "", "logical service (also namespace for k8s)")
	persistent.StringVar(&region, "region", "", "region (also kube context for k8s)")
	persistent.StringVar(&since, "since", "15m", "window start: 15m, 2h, 3d, or RFC3339")
	persistent.StringVar(&until, "until", "", "window end (RFC3339, default now)")
	persistent.StringVar(&level, "level", "", "minimum level: debug|info|warn|error")
	persistent.StringVar(&grep, "grep", "", "message filter (substring, or /regex/)")
	persistent.IntVar(&limit, "limit", 1000, "max entries for bounded queries")
	persistent.StringVar(&format, "format", "jsonl", "output format: jsonl (json for sources)")
	persistent.StringVar(&profile, "profile", "", "AWS profile")

	// sources
	sourcesCmd := &cobra.Command{
		Use:   "sources",
		Short: "Discover available log sources for a provider",
		RunE: func(cmd *cobra.Command, args []string) error {
			opts, err := buildOpts()
			if err != nil {
				return err
			}
			collector, err := logs.Get(provider)
			if err != nil {
				return err
			}
			sources, err := collector.Sources(cmd.Context(), opts)
			if err != nil {
				return err
			}
			out, _ := json.Marshal(map[string]any{"provider": provider, "sources": sources})
			fmt.Fprintln(os.Stdout, string(out))
			return nil
		},
	}

	emitJSONL := func(e logs.Entry) error {
		b, err := json.Marshal(e)
		if err != nil {
			return nil // skip an unmarshalable entry rather than abort the stream
		}
		_, werr := os.Stdout.Write(append(b, '\n'))
		return werr
	}

	// query
	queryCmd := &cobra.Command{
		Use:   "query",
		Short: "Fetch a bounded window of logs (JSON-lines on stdout)",
		RunE: func(cmd *cobra.Command, args []string) error {
			opts, err := buildOpts()
			if err != nil {
				return err
			}
			collector, err := logs.Get(provider)
			if err != nil {
				return err
			}
			logs.EmitProgress("collect", fmt.Sprintf("querying %s %s", provider, resource))
			return collector.Query(cmd.Context(), opts, emitJSONL)
		},
	}

	// tail
	tailCmd := &cobra.Command{
		Use:   "tail",
		Short: "Follow logs live (JSON-lines on stdout until interrupted)",
		RunE: func(cmd *cobra.Command, args []string) error {
			opts, err := buildOpts()
			if err != nil {
				return err
			}
			opts.Follow = true
			collector, err := logs.Get(provider)
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			logs.EmitProgress("collect", fmt.Sprintf("tailing %s %s", provider, resource))
			return collector.Tail(ctx, opts, emitJSONL)
		},
	}

	// chat
	chatCmd := &cobra.Command{
		Use:   "chat [question]",
		Short: "Ask questions about logs (agentic talk-to-logs)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			question := strings.Join(args, " ")
			opts, err := buildOpts()
			if err != nil {
				return err
			}
			if limit <= 0 || limit > 5000 {
				opts.Limit = 2000
			}
			return runLogsChat(cmd.Context(), opts, question, aiProfile)
		},
	}
	chatCmd.Flags().StringVar(&aiProfile, "ai-profile", "", "AI provider profile override")

	logsCmd.AddCommand(sourcesCmd, queryCmd, tailCmd, chatCmd)
	return logsCmd
}

// runLogsChat collects the scoped logs, compresses them, and streams an
// LLM answer grounded in citable lines.
func runLogsChat(ctx context.Context, opts logs.Options, question, aiProfile string) error {
	collector, err := logs.Get(opts.Provider)
	if err != nil {
		return err
	}
	// Override the AI provider for this run if requested (createAIClient reads
	// ai.default_provider when no per-command profile is set).
	if strings.TrimSpace(aiProfile) != "" {
		viper.Set("ai.default_provider", strings.TrimSpace(aiProfile))
	}

	logs.EmitProgress("collect", fmt.Sprintf("collecting %s logs for analysis", opts.Provider))
	var entries []logs.Entry
	cap := opts.Limit
	if cap <= 0 {
		cap = 2000
	}
	collectErr := collector.Query(ctx, opts, func(e logs.Entry) error {
		entries = append(entries, e)
		if len(entries) >= cap {
			return errCollectLimit // stop once we hit the cap
		}
		return nil
	})
	if collectErr != nil && !errors.Is(collectErr, errCollectLimit) {
		return collectErr
	}

	logs.EmitProgress("analyze", fmt.Sprintf("summarizing %d log lines", len(entries)))
	contextBlock, truncated := logs.BuildChatContext(entries, 60)
	patterns := logs.ErrorPatterns(entries)

	prompt := buildLogsChatPrompt(opts, question, contextBlock, patterns, truncated, len(entries))

	logs.EmitProgress("synthesize", "analyzing logs with the model")
	aiClient, err := createAIClient(false)
	if err != nil {
		return err
	}
	answer, err := aiClient.AskPrompt(ctx, prompt)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, strings.TrimSpace(answer))
	return nil
}

func buildLogsChatPrompt(opts logs.Options, question, contextBlock string, patterns map[string]int, truncated bool, total int) string {
	var b strings.Builder
	b.WriteString("You are a log analysis assistant. Answer the user's question using ONLY the log lines provided below.\n")
	b.WriteString("Rules:\n")
	b.WriteString("- Cite specific evidence with its [ref:...] marker for every factual claim.\n")
	b.WriteString("- If the answer is not present in these logs, say so plainly and name what was searched; do not invent details.\n")
	b.WriteString("- For deduplicated patterns, cite the pattern and its count (e.g. 'x42'), not invented per-line values.\n")
	b.WriteString("- Label any root-cause statement as a hypothesis with the evidence supporting it.\n\n")

	fmt.Fprintf(&b, "Scope: provider=%s resource=%s", opts.Provider, opts.Resource)
	if opts.Service != "" {
		fmt.Fprintf(&b, " service=%s", opts.Service)
	}
	if opts.Level != "" {
		fmt.Fprintf(&b, " level>=%s", opts.Level)
	}
	if opts.Grep != "" {
		fmt.Fprintf(&b, " grep=%q", opts.Grep)
	}
	fmt.Fprintf(&b, " window-from=%s\n", opts.Since.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "Signal counts: %d errors, %d timeouts, %d connection failures across %d lines.\n",
		patterns["errors"], patterns["timeouts"], patterns["connection_failures"], total)
	if truncated {
		b.WriteString("(Note: the window was large; only the top patterns by severity/frequency are shown.)\n")
	}
	b.WriteString("\n--- LOGS ---\n")
	b.WriteString(contextBlock)
	b.WriteString("\n--- QUESTION ---\n")
	b.WriteString(question)
	b.WriteString("\n")
	return b.String()
}
