package cmd

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/bgdnvk/clanker/internal/api"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func init() {
	var (
		port                   int
		host                   string
		token                  string
		corsOrigin             string
		debug                  bool
		aiProfile              string
		openaiKey              string
		openaiModel            string
		anthropicKey           string
		geminiKey              string
		localModelInferenceURL string
		noThinking             bool
	)

	serverCmd := &cobra.Command{
		Use:   "server",
		Short: "Run the Clanker HTTP API server",
		Long: `Start the HTTP API server that wraps the Clanker agent.

This is the gateway for the Clanker web dashboard. Inventory + maker
apply + plan-generation endpoints all live here.

Auth: pass --token or set CLANKER_API_TOKEN. If neither is set, the
server runs in open mode (loud warning to stderr).

AI provider: pass the same --ai-profile / --openai-key / --openai-model
/ --local-model-inference-url flags you'd give to ` + "`clanker ask`" + ` so the
plan-generation endpoint can call your configured LLM. Server reads from
~/.clanker.yaml as well, so flags only override what's already there.

Examples:
  # Open server for local dev
  clanker server --port 8080

  # Token-gated server
  clanker server --port 8080 --token "$(openssl rand -hex 32)"

  # With vLLM-backed plan generation
  clanker server --port 8080 \
    --ai-profile openai \
    --openai-model qwen3.6-27b-fp8 \
    --openai-key "$VLLM_API_KEY" \
    --local-model-inference-url "$VLLM_BASE_URL/v1"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			resolved := strings.TrimSpace(token)
			if resolved == "" {
				resolved = strings.TrimSpace(os.Getenv("CLANKER_API_TOKEN"))
			}
			addr := fmt.Sprintf("%s:%d", host, port)
			api.SetVersion(Version)

			// Push AI config into viper so api handlers building ai.NewClient
			// pick the same values the CLI does. Empty flags leave existing
			// config (from ~/.clanker.yaml) untouched.
			if strings.TrimSpace(aiProfile) != "" {
				viper.Set("ai.default_provider", aiProfile)
			}
			if strings.TrimSpace(openaiKey) != "" {
				viper.Set("ai.providers.openai.api_key", openaiKey)
			}
			if strings.TrimSpace(openaiModel) != "" {
				viper.Set("ai.providers.openai.model", openaiModel)
			}
			if strings.TrimSpace(anthropicKey) != "" {
				viper.Set("ai.providers.anthropic.api_key", anthropicKey)
			}
			if strings.TrimSpace(geminiKey) != "" {
				viper.Set("ai.providers.gemini-api.api_key", geminiKey)
			}
			if strings.TrimSpace(localModelInferenceURL) != "" {
				viper.Set("ai.providers.openai.local_model_inference_url", strings.TrimSpace(localModelInferenceURL))
			}
			if noThinking {
				viper.Set("ai.providers.openai.chat_template_kwargs", map[string]interface{}{"enable_thinking": false})
			}

			srv := api.New(api.Config{
				Addr:       addr,
				Token:      resolved,
				CORSOrigin: corsOrigin,
				Debug:      debug,
			}, log.New(os.Stderr, "", log.LstdFlags))

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			go func() {
				<-sigCh
				fmt.Fprintln(os.Stderr, "[server] shutting down")
				cancel()
			}()
			return srv.Run(ctx)
		},
	}

	serverCmd.Flags().IntVar(&port, "port", 8080, "Port to listen on")
	serverCmd.Flags().StringVar(&host, "host", "127.0.0.1", "Host to bind on (use 0.0.0.0 for all interfaces)")
	serverCmd.Flags().StringVar(&token, "token", "", "Bearer token required for /api/v1/* (or set CLANKER_API_TOKEN; empty disables auth)")
	serverCmd.Flags().StringVar(&corsOrigin, "cors-origin", "*", "Value for Access-Control-Allow-Origin")
	serverCmd.Flags().BoolVar(&debug, "server-debug", false, "Log every request, not just errors")

	// LLM provider flags — push into viper so the plan-generation endpoint
	// has the same options the CLI exposes.
	serverCmd.Flags().StringVar(&aiProfile, "ai-profile", "", "AI provider profile (openai, gemini-api, anthropic, cohere, ...)")
	serverCmd.Flags().StringVar(&openaiKey, "openai-key", "", "OpenAI API key (or any OpenAI-compatible endpoint key, e.g. vLLM)")
	serverCmd.Flags().StringVar(&openaiModel, "openai-model", "", "OpenAI / OpenAI-compatible model name")
	serverCmd.Flags().StringVar(&anthropicKey, "anthropic-key", "", "Anthropic API key")
	serverCmd.Flags().StringVar(&geminiKey, "gemini-key", "", "Gemini API key")
	serverCmd.Flags().StringVar(&localModelInferenceURL, "local-model-inference-url", "", "OpenAI-compatible base URL for local/self-hosted models (e.g. https://x.runpod.net/v1)")
	serverCmd.Flags().BoolVar(&noThinking, "no-thinking", false, "Disable Qwen3-style internal reasoning trace via chat_template_kwargs.enable_thinking=false. 14x faster plan generation with reasoning-capable models.")

	rootCmd.AddCommand(serverCmd)
}
