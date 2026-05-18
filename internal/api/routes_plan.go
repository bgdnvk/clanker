package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bgdnvk/clanker/internal/ai"
	"github.com/bgdnvk/clanker/internal/maker"
	"github.com/spf13/viper"
)

// planRequest is the JSON body of POST /api/v1/maker/plan.
type planRequest struct {
	Provider  string `json:"provider"`
	Question  string `json:"question"`
	Destroyer bool   `json:"destroyer"`
}

// planResponse wraps the generated plan plus diagnostic metadata so the
// dashboard can show timings + which model produced the plan.
type planResponse struct {
	Provider string          `json:"provider"`
	Plan     json.RawMessage `json:"plan"`
	Model    string          `json:"model,omitempty"`
	AIProfile string         `json:"ai_profile,omitempty"`
	Duration string          `json:"duration"`
}

// handleMakerPlan generates a maker plan via the configured AI provider.
// Mirrors the cmd/ask.go --maker path but runs entirely server-side so the
// dashboard can drive the full agent loop in browser.
func (s *Server) handleMakerPlan(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10)) // 64 KiB cap for a question
	if err != nil {
		writeError(w, http.StatusBadRequest, "read_body", err.Error())
		return
	}
	var req planRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	provider := strings.ToLower(strings.TrimSpace(req.Provider))
	if provider == "" {
		provider = "tencent"
	}
	if provider != "tencent" {
		writeError(w, http.StatusBadRequest, "unsupported_provider", "only provider=tencent is wired for plan generation via HTTP today")
		return
	}
	if strings.TrimSpace(req.Question) == "" {
		writeError(w, http.StatusBadRequest, "missing_question", "question is required")
		return
	}

	// Resolve AI provider from viper (cmd/server.go pushes flag values in
	// before api.New so they're available here).
	aiProfile := strings.TrimSpace(viper.GetString("ai.default_provider"))
	if aiProfile == "" {
		aiProfile = "openai"
	}
	model := strings.TrimSpace(viper.GetString(fmt.Sprintf("ai.providers.%s.model", aiProfile)))

	apiKey := resolveAPIKeyForProvider(aiProfile)
	aiClient := ai.NewClient(aiProfile, apiKey, s.cfg.Debug, aiProfile)

	prompt := maker.TencentPlanPromptWithMode(req.Question, req.Destroyer)

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()

	start := time.Now()
	raw, err := aiClient.AskPrompt(ctx, prompt)
	if err != nil {
		writeError(w, http.StatusBadGateway, "llm_error", err.Error())
		return
	}
	cleaned := aiClient.CleanJSONResponse(raw)
	cleaned = strings.TrimSpace(cleaned)
	if cleaned == "" {
		writeError(w, http.StatusBadGateway, "empty_plan", "LLM returned an empty plan")
		return
	}

	// Validate by parsing — we don't return ParsedPlan because the
	// dashboard wants the raw JSON for display in the editor.
	if _, parseErr := maker.ParsePlan(cleaned); parseErr != nil {
		// Surface the bad JSON to the user so they can hand-edit if needed.
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"data": planResponse{
				Provider: provider,
				Plan:     json.RawMessage(cleaned),
				Model:    model,
				AIProfile: aiProfile,
				Duration: time.Since(start).Round(time.Millisecond).String(),
			},
			"warning": "generated plan did not parse cleanly: " + parseErr.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"data": planResponse{
			Provider:  provider,
			Plan:      json.RawMessage(cleaned),
			Model:     model,
			AIProfile: aiProfile,
			Duration:  time.Since(start).Round(time.Millisecond).String(),
		},
	})
}

// resolveAPIKeyForProvider picks the right viper key for the given provider.
// Mirrors the resolution that cmd/ask.go does so the server reads keys from
// the same config slots.
func resolveAPIKeyForProvider(provider string) string {
	switch provider {
	case "openai":
		return viper.GetString("ai.providers.openai.api_key")
	case "gemini", "gemini-api":
		return viper.GetString("ai.providers.gemini-api.api_key")
	case "anthropic":
		return viper.GetString("ai.providers.anthropic.api_key")
	case "cohere":
		return viper.GetString("ai.providers.cohere.api_key")
	case "deepseek":
		return viper.GetString("ai.providers.deepseek.api_key")
	case "minimax":
		return viper.GetString("ai.providers.minimax.api_key")
	default:
		return viper.GetString("ai.api_key")
	}
}
