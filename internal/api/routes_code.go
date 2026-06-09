package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bgdnvk/clanker/internal/codeview"
)

type codeAnalyzeRequest struct {
	RepoURL string `json:"repoUrl"`
}

func (s *Server) handleCodeAnalyze(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read_body", err.Error())
		return
	}

	var req codeAnalyzeRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}

	repoURL := strings.TrimSpace(req.RepoURL)
	if repoURL == "" {
		writeError(w, http.StatusBadRequest, "missing_repo_url", "repoUrl is required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Minute)
	defer cancel()

	analysis, cleanup, err := codeview.CloneAndAnalyze(ctx, repoURL, codeview.AnalyzeOptions{})
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, "code_analysis_failed", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"data": analysis})
}
