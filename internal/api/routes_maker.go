package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bgdnvk/clanker/internal/maker"
	"github.com/bgdnvk/clanker/internal/tencent"
)

// applyRequest is the JSON body of POST /api/v1/maker/apply.
//
// `plan` is the same Plan JSON shape that `clanker ask --maker ...` emits.
// `destroyer` gates destructive operations (Terminate*, Delete*, Reset*)
// just like the --destroyer CLI flag.
type applyRequest struct {
	Provider  string          `json:"provider"`
	Plan      json.RawMessage `json:"plan"`
	Destroyer bool            `json:"destroyer"`
}

// applyResponse summarises an apply attempt. Output is the captured writer
// the executor used so the dashboard can render it like a CLI session.
type applyResponse struct {
	Provider string `json:"provider"`
	Status   string `json:"status"` // "ok" or "error"
	Output   string `json:"output"`
	Error    string `json:"error,omitempty"`
	Duration string `json:"duration"`
	HistoryID int64 `json:"history_id,omitempty"`
}

func (s *Server) handleMakerApply(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1 MiB cap
	if err != nil {
		writeError(w, http.StatusBadRequest, "read_body", err.Error())
		return
	}
	var req applyRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	provider := strings.ToLower(strings.TrimSpace(req.Provider))
	if provider == "" {
		provider = "tencent" // default — only one wired today
	}
	if provider != "tencent" {
		writeError(w, http.StatusBadRequest, "unsupported_provider", "only provider=tencent is wired for apply via HTTP today")
		return
	}
	if len(req.Plan) == 0 {
		writeError(w, http.StatusBadRequest, "missing_plan", "plan is required")
		return
	}

	plan, err := maker.ParsePlan(string(req.Plan))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_plan", err.Error())
		return
	}
	if plan.Provider != "" && !strings.EqualFold(plan.Provider, "tencent") {
		writeError(w, http.StatusBadRequest, "provider_mismatch",
			"plan.provider="+plan.Provider+" does not match request provider=tencent")
		return
	}

	creds := tencent.ResolveCredentials()
	if creds.SecretID == "" || creds.SecretKey == "" {
		writeError(w, http.StatusUnauthorized, "tencent_credentials",
			"server is missing tencent credentials (set TENCENTCLOUD_SECRET_ID/KEY or TENCENT_SECRET_ID/KEY before starting clanker server)")
		return
	}

	var buf bytes.Buffer
	start := time.Now()
	execErr := maker.ExecuteTencentPlan(r.Context(), plan, maker.ExecOptions{
		TencentSecretID:  creds.SecretID,
		TencentSecretKey: creds.SecretKey,
		TencentRegion:    creds.Region,
		Writer:           &buf,
		Destroyer:        req.Destroyer,
		Debug:            s.cfg.Debug,
	})
	duration := time.Since(start).Round(time.Millisecond)

	// Build history record (regardless of success/failure so audit trail is
	// complete).
	rec := ApplyRecord{
		StartedAt:        start,
		Provider:         provider,
		Duration:         duration.String(),
		Destroyer:        req.Destroyer,
		CommandCount:     len(plan.Commands),
		DestructiveCount: countDestructiveCommands(plan.Commands),
		Summary:          strings.TrimSpace(plan.Summary),
		Question:         strings.TrimSpace(plan.Question),
		Output:           buf.String(),
	}
	if execErr != nil {
		rec.Status = "error"
		rec.Error = execErr.Error()
	} else {
		rec.Status = "ok"
	}
	if s.history != nil {
		rec = s.history.append(rec)
	}

	resp := applyResponse{
		Provider:  provider,
		Output:    buf.String(),
		Duration:  duration.String(),
		Status:    rec.Status,
		HistoryID: rec.ID,
	}
	if execErr != nil {
		resp.Error = execErr.Error()
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"data": resp})
}

// handleMakerHistory returns the in-memory ring buffer of past applies
// (newest first). Supports ?limit=N to cap the response size.
func (s *Server) handleMakerHistory(w http.ResponseWriter, r *http.Request) {
	limit := 0
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if s.history == nil {
		writeData(w, []ApplyRecord{})
		return
	}
	writeData(w, s.history.list(limit))
}

// countDestructiveCommands counts plan commands whose Tencent action is
// gated by --destroyer in the executor. Delegates the classification to
// maker.IsTencentDestructive so the displayed count never drifts away from
// what the executor's safety gate actually enforces — the previous local
// prefix denylist would have undercounted any CAM mutation like AddUser /
// CreateAccessKey / AttachUserPolicy (none of which match the old
// Terminate|Delete|Destroy|Reset|Release|Discontinue prefixes).
func countDestructiveCommands(cmds []maker.Command) int {
	n := 0
	for _, c := range cmds {
		if len(c.Args) < 3 {
			continue
		}
		action := c.Args[2]
		if maker.IsTencentDestructive(action) {
			n++
		}
	}
	return n
}
