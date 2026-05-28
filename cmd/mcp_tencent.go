package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcptransport "github.com/mark3labs/mcp-go/server"
)

// Tencent MCP tools.
//
// Unlike the Vercel / Fly.io / Railway / Verda tools (which call their
// providers' SDKs directly because those providers have no Clanker-managed
// HTTP surface), Tencent tools delegate to clanker-api over HTTP. The
// reasoning:
//
//   - clanker-api already exposes the full Tencent surface (inventory,
//     scans, monitoring, cost, Maker plan + apply, audit history) behind
//     bearer auth, validated by the dashboard.
//   - In the docker-compose deploy the MCP server runs as its own service
//     (`clanker-mcp`) reachable only via the internal network. Putting all
//     Tencent code paths behind clanker-api means just one set of Tencent
//     creds (in clanker-api) instead of duplicating them into clanker-mcp.
//   - When clanker-api gains a new endpoint, exposing it as an MCP tool is
//     a one-function addition here.
//
// Auth: the handler reads CLANKER_API_URL + CLANKER_API_TOKEN from env, or
// from --api-base / --api-token flags. On the server these live in the
// same .env that clanker-api reads.

// Note on schemas: mark3labs's WithInputSchema[T]() generic doesn't actually
// extract jsonschema struct tags in this codebase (the Vercel/Fly/Railway
// tools all advertise empty schemas too — they only work because their
// description text inlines parameter hints). We use explicit With* options
// here so the schema is what the LLM actually sees.

// ── HTTP client for clanker-api ─────────────────────────────────────────────

type clankerAPIClient struct {
	base  string
	token string
	hc    *http.Client
}

func newClankerAPIClient() *clankerAPIClient {
	base := strings.TrimSpace(os.Getenv("CLANKER_API_URL"))
	if base == "" {
		base = "http://127.0.0.1:47180"
	}
	return &clankerAPIClient{
		base:  strings.TrimRight(base, "/"),
		token: strings.TrimSpace(os.Getenv("CLANKER_API_TOKEN")),
		hc:    &http.Client{Timeout: 5 * time.Minute},
	}
}

func (c *clankerAPIClient) get(ctx context.Context, path string, q url.Values) (string, error) {
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	c.auth(req)
	return c.do(req)
}

func (c *clankerAPIClient) postJSON(ctx context.Context, path string, body []byte) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	c.auth(req)
	return c.do(req)
}

func (c *clankerAPIClient) auth(req *http.Request) {
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("Accept", "application/json")
}

func (c *clankerAPIClient) do(req *http.Request) (string, error) {
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("clanker-api %s %s: %w", req.Method, req.URL.Path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("clanker-api %s %s returned %d: %s",
			req.Method, req.URL.Path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return string(body), nil
}

// ── Registration ────────────────────────────────────────────────────────────

// strParam pulls a string arg from the MCP call. Returns "" if missing/null.
func strParam(req mcp.CallToolRequest, key string) string {
	if req.Params.Arguments == nil {
		return ""
	}
	m, ok := req.Params.Arguments.(map[string]any)
	if !ok {
		return ""
	}
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

// intParam pulls an integer arg with a default fallback.
func intParam(req mcp.CallToolRequest, key string, def int) int {
	if req.Params.Arguments == nil {
		return def
	}
	m, ok := req.Params.Arguments.(map[string]any)
	if !ok {
		return def
	}
	v, ok := m[key]
	if !ok || v == nil {
		return def
	}
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(x)); err == nil {
			return n
		}
	}
	return def
}

// boolParam pulls a bool arg with a default fallback.
func boolParam(req mcp.CallToolRequest, key string, def bool) bool {
	if req.Params.Arguments == nil {
		return def
	}
	m, ok := req.Params.Arguments.(map[string]any)
	if !ok {
		return def
	}
	v, ok := m[key]
	if !ok || v == nil {
		return def
	}
	b, ok := v.(bool)
	if !ok {
		return def
	}
	return b
}

// rawObjectParam returns a nested object as map[string]any. Used for the
// Maker plan, which is a complex JSON document.
func rawObjectParam(req mcp.CallToolRequest, key string) map[string]any {
	if req.Params.Arguments == nil {
		return nil
	}
	m, ok := req.Params.Arguments.(map[string]any)
	if !ok {
		return nil
	}
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	return obj
}

// registerTencentMCPTools adds the ten Tencent tools onto the shared MCP
// server built in newClankerMCPServer(). Called from there so all tools
// (Vercel, Fly, Railway, Verda, Tencent) live on one MCP endpoint.
//
// Schemas are declared with explicit mcp.WithString/Number/Bool/Object
// options because WithInputSchema[T]() generic with struct-tag reflection
// produces an empty schema in this version of mark3labs/mcp-go.
func registerTencentMCPTools(server *mcptransport.MCPServer) {
	api := newClankerAPIClient()

	// clanker_tencent_list ─────────────────────────────────────────────────
	server.AddTool(
		mcp.NewTool(
			"clanker_tencent_list",
			mcp.WithDescription("List Tencent Cloud resources by type in a region. "+
				"Returns the raw inventory JSON. Best tool for static-spec questions "+
				"(vCPU count, memory GB, state, IP, zone, vpc_id, tags) — those "+
				"fields are returned by Describe* and you do NOT need a separate "+
				"monitoring call."),
			mcp.WithString("resource_type",
				mcp.Required(),
				mcp.Description("One of: cvm, lighthouse, vpc, sg, mysql, postgres, cos, tke, clb, eip, cbs, ssl, cam, redis, mongodb, cynosdb, cdn, edgeone, waf, antiddos, nat, vpn, ccn, dc, monitor, cls, cloudaudit"),
			),
			mcp.WithString("region",
				mcp.DefaultString("ap-singapore"),
				mcp.Description("Tencent region code. ssl/cam/waf/cloudaudit are account-global and ignore this."),
			),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			rt := strParam(req, "resource_type")
			if rt == "" {
				return mcp.NewToolResultError("resource_type is required"), nil
			}
			region := strParam(req, "region")
			if region == "" {
				region = "ap-singapore"
			}
			body, err := api.get(ctx, "/api/v1/tencent/resources/"+url.PathEscape(rt),
				url.Values{"region": {region}})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(body), nil
		},
	)

	// clanker_tencent_scan ─────────────────────────────────────────────────
	server.AddTool(
		mcp.NewTool(
			"clanker_tencent_scan",
			mcp.WithDescription("Run one of Clanker's ten Tencent security audits."),
			mcp.WithString("kind",
				mcp.Required(),
				mcp.Description("One of: public-exposure, clb-exposure, idle-eips, unencrypted-cbs, cert-expiry, cam-hygiene, db-exposure, waf-coverage, antiddos-coverage, audit-coverage. cert-expiry/cam-hygiene/waf-coverage/audit-coverage are account-global; the rest use region."),
				mcp.Enum(
					"public-exposure", "clb-exposure", "idle-eips",
					"unencrypted-cbs", "cert-expiry", "cam-hygiene",
					"db-exposure", "waf-coverage", "antiddos-coverage",
					"audit-coverage",
				),
			),
			mcp.WithString("region",
				mcp.DefaultString("ap-singapore"),
				mcp.Description("Tencent region (used by public-exposure / clb-exposure / idle-eips / unencrypted-cbs / db-exposure / antiddos-coverage)"),
			),
			mcp.WithNumber("days",
				mcp.DefaultNumber(30),
				mcp.Description("cert-expiry: threshold in days (default 30)"),
			),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			kind := strParam(req, "kind")
			if kind == "" {
				return mcp.NewToolResultError("kind is required"), nil
			}
			region := strParam(req, "region")
			if region == "" {
				region = "ap-singapore"
			}
			q := url.Values{}
			switch kind {
			case "cert-expiry":
				q.Set("days", strconv.Itoa(intParam(req, "days", 30)))
			case "public-exposure", "clb-exposure", "idle-eips",
				"unencrypted-cbs", "db-exposure", "antiddos-coverage":
				q.Set("region", region)
			}
			body, err := api.get(ctx, "/api/v1/tencent/scan/"+url.PathEscape(kind), q)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(body), nil
		},
	)

	// clanker_tencent_metrics ──────────────────────────────────────────────
	server.AddTool(
		mcp.NewTool(
			"clanker_tencent_metrics",
			mcp.WithDescription("Cloud Monitor metrics for CVM or Lighthouse "+
				"instances over the last N minutes. Use ONLY for runtime values "+
				"(utilization, traffic, packets). For static specs (CPU count, "+
				"Memory GB, state) call clanker_tencent_list instead."),
			mcp.WithString("product",
				mcp.Required(),
				mcp.Description("cvm or lighthouse"),
				mcp.Enum("cvm", "lighthouse"),
			),
			mcp.WithString("metric",
				mcp.Required(),
				mcp.Description("CVM: CPUUsage, MemUsage, LanOuttraffic, LanIntraffic, WanOuttraffic, WanIntraffic. Lighthouse: CpuUsage, MemUsage, DiskUsage, CpuLoad1, CpuLoad5, LighthouseIntraffic, LighthouseOuttraffic, LighthouseInpkg, LighthouseOutpkg."),
			),
			mcp.WithString("region",
				mcp.DefaultString("ap-singapore"),
				mcp.Description("Tencent region"),
			),
			mcp.WithNumber("minutes",
				mcp.DefaultNumber(60),
				mcp.Description("Time window in minutes (default 60, max 1440)"),
			),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			product := strParam(req, "product")
			metric := strParam(req, "metric")
			if product == "" || metric == "" {
				return mcp.NewToolResultError("product and metric are required"), nil
			}
			region := strParam(req, "region")
			if region == "" {
				region = "ap-singapore"
			}
			minutes := intParam(req, "minutes", 60)
			body, err := api.get(ctx, "/api/v1/tencent/metrics/"+url.PathEscape(product),
				url.Values{
					"region":  {region},
					"metric":  {metric},
					"minutes": {strconv.Itoa(minutes)},
				})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(body), nil
		},
	)

	// clanker_tencent_expiry ───────────────────────────────────────────────
	server.AddTool(
		mcp.NewTool(
			"clanker_tencent_expiry",
			mcp.WithDescription("Tencent Cloud renewal alert. Walks every PREPAID-capable "+
				"resource (CVM, Lighthouse, CBS, MySQL, Postgres, Redis, MongoDB, CynosDB, "+
				"CLB, AntiDDoS — and SSL with include_ssl=true) across the requested regions "+
				"and returns items at or below the renewal threshold. Use this BEFORE asking "+
				"the user about renewals — the structured response includes counts.expired / "+
				"counts.flagged / counts.auto_renew so you can phrase a precise summary."),
			mcp.WithString("regions",
				mcp.Description("Comma-separated regions to scan (e.g. ap-singapore,ap-jakarta). Defaults to the server's configured region."),
			),
			mcp.WithNumber("threshold",
				mcp.DefaultNumber(30),
				mcp.Description("Flag items this many days from expiry or closer (default 30)"),
			),
			mcp.WithBoolean("manual_only",
				mcp.DefaultBool(true),
				mcp.Description("Only list items with auto_renew=false; auto-renewing ones are still counted in counts.auto_renew (default true)"),
			),
			mcp.WithBoolean("include_ssl",
				mcp.DefaultBool(false),
				mcp.Description("Include SSL certificate validity as additional items (different signal from subscription expiry; default false)"),
			),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			q := url.Values{}
			if v := strParam(req, "regions"); v != "" {
				q.Set("regions", v)
			}
			q.Set("threshold", strconv.Itoa(intParam(req, "threshold", 30)))
			q.Set("manual_only", strconv.FormatBool(boolParam(req, "manual_only", true)))
			q.Set("include_ssl", strconv.FormatBool(boolParam(req, "include_ssl", false)))
			body, err := api.get(ctx, "/api/v1/tencent/expiry", q)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(body), nil
		},
	)

	// clanker_tencent_cost ─────────────────────────────────────────────────
	server.AddTool(
		mcp.NewTool(
			"clanker_tencent_cost",
			mcp.WithDescription("Tencent Cloud billing for a CLOSED month. Returns "+
				"by_product (cost per product) and top_resources (most expensive "+
				"resource IDs). Current month is partial — pick a previously-closed "+
				"month for accurate numbers."),
			mcp.WithString("month",
				mcp.Required(),
				mcp.Description("YYYY-MM, e.g. 2026-04"),
			),
			mcp.WithNumber("top",
				mcp.DefaultNumber(50),
				mcp.Description("Top-N resources by spend (default 50)"),
			),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			month := strParam(req, "month")
			if month == "" {
				return mcp.NewToolResultError("month is required (YYYY-MM)"), nil
			}
			top := intParam(req, "top", 50)
			byProd, err := api.get(ctx, "/api/v1/tencent/cost/by-product",
				url.Values{"month": {month}})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			byRes, err := api.get(ctx, "/api/v1/tencent/cost/resources",
				url.Values{"month": {month}, "top": {strconv.Itoa(top)}})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			merged := fmt.Sprintf(`{"by_product":%s,"top_resources":%s}`, byProd, byRes)
			return mcp.NewToolResultText(merged), nil
		},
	)

	// clanker_tencent_vouchers ─────────────────────────────────────────────
	server.AddTool(
		mcp.NewTool(
			"clanker_tencent_vouchers",
			mcp.WithDescription("Tencent Cloud vouchers (account credits). Three modes, "+
				"checked in this order:\n"+
				"  • voucher_id set → that voucher's deduction history "+
				"(when/how much/which products).\n"+
				"  • month set (YYYY-MM) → that month's voucher DEDUCTION grouped by "+
				"the owner account UIN of each billed resource (the only "+
				"month-scoped, per-account view of voucher spend).\n"+
				"  • neither → voucher inventory plus an `owners` array breaking "+
				"voucher spend down per owner account UIN (spent = nominal − "+
				"remaining balance); pass status=unUsed for ACTIVE vouchers only.\n"+
				"Amounts are in account currency."),
			mcp.WithString("status",
				mcp.Description("Filter the inventory by voucher status: unUsed (active), used, delivered, cancel, overdue. Omit for all. Ignored when voucher_id or month is set."),
				mcp.Enum("unUsed", "used", "delivered", "cancel", "overdue"),
			),
			mcp.WithString("month",
				mcp.Description("YYYY-MM, e.g. 2026-04. If set, return that month's voucher deduction grouped by owner account UIN. Ignored when voucher_id is set."),
			),
			mcp.WithString("voucher_id",
				mcp.Description("If set, return this voucher's usage/deduction history instead of the inventory."),
			),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			voucherID := strParam(req, "voucher_id")
			if voucherID != "" {
				body, err := api.get(ctx, "/api/v1/tencent/cost/voucher-usage/"+url.PathEscape(voucherID), nil)
				if err != nil {
					return mcp.NewToolResultError(err.Error()), nil
				}
				return mcp.NewToolResultText(body), nil
			}
			if month := strParam(req, "month"); month != "" {
				body, err := api.get(ctx, "/api/v1/tencent/cost/voucher-by-owner",
					url.Values{"month": {month}})
				if err != nil {
					return mcp.NewToolResultError(err.Error()), nil
				}
				return mcp.NewToolResultText(body), nil
			}
			q := url.Values{}
			if status := strParam(req, "status"); status != "" {
				q.Set("status", status)
			}
			body, err := api.get(ctx, "/api/v1/tencent/cost/vouchers", q)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(body), nil
		},
	)

	// clanker_tencent_topology ─────────────────────────────────────────────
	server.AddTool(
		mcp.NewTool(
			"clanker_tencent_topology",
			mcp.WithDescription("Full VPC → subnet → instance/DB tree for a region "+
				"in one call. Good for 'where does X live?' and blast-radius style "+
				"questions. Returns vpcs, subnets, cvms, security_groups, mysql, "+
				"postgres, clusters, plus warnings about orphaned resources."),
			mcp.WithString("region",
				mcp.DefaultString("ap-singapore"),
				mcp.Description("Tencent region"),
			),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			region := strParam(req, "region")
			if region == "" {
				region = "ap-singapore"
			}
			body, err := api.get(ctx, "/api/v1/tencent/topology",
				url.Values{"region": {region}})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(body), nil
		},
	)

	// clanker_tencent_audit_history ────────────────────────────────────────
	server.AddTool(
		mcp.NewTool(
			"clanker_tencent_audit_history",
			mcp.WithDescription("In-memory list of recent Maker applies on this "+
				"server (newest first, capped at 50). Each entry: status, duration, "+
				"destroyer flag, command_count, destructive_count, summary, error. "+
				"Use before acting to avoid duplicating work."),
			mcp.WithNumber("limit",
				mcp.DefaultNumber(50),
				mcp.Description("Max records (default 50)"),
			),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			limit := intParam(req, "limit", 50)
			body, err := api.get(ctx, "/api/v1/maker/history",
				url.Values{"limit": {strconv.Itoa(limit)}})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(body), nil
		},
	)

	// clanker_tencent_maker_plan ───────────────────────────────────────────
	server.AddTool(
		mcp.NewTool(
			"clanker_tencent_maker_plan",
			mcp.WithDescription("Turn a natural-language Tencent ops request into a "+
				"JSON plan. Returns {plan, model, ai_profile, duration}. Review "+
				"destructive_count and any filter verbs before passing the plan to "+
				"clanker_tencent_maker_apply. For 'find X by criteria' the planner "+
				"uses Clanker's filter verb so apply returns only matching items."),
			mcp.WithString("question",
				mcp.Required(),
				mcp.Description("Natural-language Tencent operation request"),
			),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			question := strParam(req, "question")
			if question == "" {
				return mcp.NewToolResultError("question is required"), nil
			}
			body, err := json.Marshal(map[string]any{
				"provider":  "tencent",
				"question":  question,
				"destroyer": false,
			})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			resp, err := api.postJSON(ctx, "/api/v1/maker/plan", body)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(resp), nil
		},
	)

	// clanker_tencent_maker_apply ──────────────────────────────────────────
	//
	// This is the ONLY write tool in the Tencent surface. Every other
	// clanker_tencent_* tool is read-only (and marked as such via
	// WithReadOnlyHintAnnotation). The policy here:
	//
	//   1. Tool annotation declares the operation destructive — MCP-aware
	//      clients (Claude Code, Cursor, ...) surface this as a "needs
	//      confirmation" prompt natively.
	//   2. Description spells out the human-approval requirement in plain
	//      language so it lands in the LLM's tool catalog prompt.
	//   3. The schema REQUIRES human_approved=true. Calls without it are
	//      refused server-side with an explanatory error — the LLM cannot
	//      route around the policy without explicitly asserting the user
	//      gave consent in the current conversation.
	//   4. destroyer=true is still required for Terminate/Delete/Reset/
	//      Release/Discontinue on top of human_approved=true, gated by
	//      the existing validator in internal/maker/validate_tencent.go.
	server.AddTool(
		mcp.NewTool(
			"clanker_tencent_maker_apply",
			mcp.WithDescription("Execute a Maker plan against Tencent (provisioning, "+
				"modification, OR destruction). REQUIRES HUMAN APPROVAL — this is a "+
				"WRITE operation that mutates real cloud resources. POLICY: you MUST "+
				"NOT call this tool unless the human user has, in the current chat "+
				"conversation, explicitly and unambiguously approved the SPECIFIC "+
				"plan that you are about to apply. If the user has not yet seen or "+
				"approved the plan, you MUST first present the plan content to them "+
				"(commands, destructive_count) and wait for an explicit \"yes\" / "+
				"\"go ahead\" / \"apply it\" / equivalent. Saying the words \"I will "+
				"apply\" is NOT approval — the user must say it. Once approved, call "+
				"this tool with human_approved=true. Set destroyer=true ADDITIONALLY "+
				"only when the user has explicitly approved a destructive "+
				"(Terminate/Delete/Reset/Release/Discontinue) command in the plan."),
			mcp.WithObject("plan",
				mcp.Required(),
				mcp.Description("Full plan object returned by clanker_tencent_maker_plan"),
			),
			mcp.WithBoolean("human_approved",
				mcp.Required(),
				mcp.Description("MUST be true. The human user has, in the current conversation, explicitly approved this specific plan. Calls with false or missing are rejected. The agent is responsible for obtaining this approval before calling — do not invent it."),
			),
			mcp.WithBoolean("destroyer",
				mcp.DefaultBool(false),
				mcp.Description("Set true ADDITIONALLY when the user has explicitly approved a destructive command (Terminate/Delete/Reset/Release/Discontinue). Without this flag those commands are rejected by the plan validator."),
			),
			mcp.WithDestructiveHintAnnotation(true),
			mcp.WithIdempotentHintAnnotation(false),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			plan := rawObjectParam(req, "plan")
			if plan == nil {
				return mcp.NewToolResultError("plan is required (full plan object from clanker_tencent_maker_plan)"), nil
			}
			// Server-side backstop: refuse without explicit human approval.
			// The agent's prompt instructs it not to lie, but in case it
			// does, this is the last gate before the call hits Tencent.
			humanApproved := boolParam(req, "human_approved", false)
			if !humanApproved {
				return mcp.NewToolResultError(
					"refused: human_approved must be true. POLICY: clanker_tencent_maker_apply " +
						"only executes when the human owner has explicitly approved this specific " +
						"plan in the current conversation. Present the plan to the user (commands, " +
						"destructive_count, summary), wait for explicit approval, then retry with " +
						"human_approved=true.",
				), nil
			}
			destroyer := boolParam(req, "destroyer", false)
			body, err := json.Marshal(map[string]any{
				"provider":  "tencent",
				"plan":      plan,
				"destroyer": destroyer,
			})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			resp, err := api.postJSON(ctx, "/api/v1/maker/apply", body)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(resp), nil
		},
	)
}
