package mcp

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"ctindex/internal/norm"
	"ctindex/internal/ratelimit"
	"ctindex/internal/store"
)

// Handler implements POST /mcp Streamable HTTP JSON-RPC
type Handler struct {
	store   *store.Store
	limiter *ratelimit.Limiter
}

func New(s *store.Store, limiter *ratelimit.Limiter) *Handler {
	return &Handler{store: s, limiter: limiter}
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   *rpcError   `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Rate limit: share bucket with /v1/search per IP (100/day)
	ip := ratelimit.ClientIP(r)
	remaining, limited := h.limiter.Allow(ip)
	w.Header().Set("X-RateLimit-Limit", fmt.Sprintf("%d", h.limiter.Limit()))
	w.Header().Set("X-RateLimit-Remaining", fmt.Sprintf("%d", remaining))
	if limited {
		http.Error(w, "rate limit exceeded: 100 requests per IP per day", http.StatusTooManyRequests)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeRPCError(w, nil, -32700, "parse error")
		return
	}
	// Accept header: application/json, text/event-stream
	// Respond with JSON for now (streamable HTTP allows SSE but JSON is fine)
	switch req.Method {
	case "initialize":
		h.handleInitialize(w, req)
	case "notifications/initialized":
		// No response for notifications, but per spec we should not send response? Actually client sends notification after initialize.
		// It's a notification (no id expected?). If id is nil, don't respond.
		if req.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		writeRPCResult(w, req.ID, map[string]interface{}{})
	case "tools/list":
		h.handleToolsList(w, req)
	case "tools/call":
		h.handleToolsCall(w, r, req, remaining)
	case "ping":
		writeRPCResult(w, req.ID, map[string]interface{}{})
	default:
		writeRPCError(w, req.ID, -32601, "method not found")
	}
	_ = strings.Contains
	_ = time.Now
}

func (h *Handler) handleInitialize(w http.ResponseWriter, req rpcRequest) {
	res := map[string]interface{}{
		"protocolVersion": "2025-03-26",
		"capabilities": map[string]interface{}{
			"logging": map[string]interface{}{},
			"tools":   map[string]interface{}{"listChanged": true},
		},
		"serverInfo": map[string]interface{}{
			"name":        "crt.name",
			"title":       "crt.name subdomain index",
			"description": "Passive Certificate Transparency subdomain index. Free-tier search only (100 req/IP/day).",
			"version":     "1.0.0",
			"websiteUrl":  "https://crt.name/",
		},
	}
	writeRPCResult(w, req.ID, res)
}

func (h *Handler) handleToolsList(w http.ResponseWriter, req rpcRequest) {
	tools := []map[string]interface{}{
		{
			"name": "search",
			"annotations": map[string]interface{}{
				"idempotentHint": true,
				"openWorldHint":  true,
				"readOnlyHint":   true,
				"title":          "Search subdomains by apex",
			},
			"description": "Look up every subdomain indexed for an apex domain (eTLD+1) in the public Certificate Transparency index at crt.name. Free tier: 100 requests per client IP per UTC day, shared with GET /v1/search. Returns index data only (no live DNS resolve). apex must be an eTLD+1 (example.com), not a subdomain.",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"apex": map[string]interface{}{
						"type":        "string",
						"description": "Apex domain to look up (eTLD+1), e.g. example.com — not a subdomain like www.example.com",
					},
					"order_new": map[string]interface{}{
						"type":        "boolean",
						"description": "If true, sort newest-first by first-seen date (default: oldest first)",
					},
					"include_dates": map[string]interface{}{
						"type":        "boolean",
						"description": "If true, include first_seen (RFC3339 or null) for each subdomain",
					},
				},
				"required":             []string{"apex"},
				"additionalProperties": false,
			},
			"outputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"apex": map[string]interface{}{
						"type":        "string",
						"description": "Normalized eTLD+1 that was queried",
					},
					"count": map[string]interface{}{
						"type":        "integer",
						"description": "Number of subdomains returned",
					},
					"subdomains": map[string]interface{}{
						"type": []string{"null", "array"},
						"items": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"sub": map[string]interface{}{
									"type":        "string",
									"description": "Fully-qualified subdomain hostname",
								},
								"first_seen": map[string]interface{}{
									"type":        []string{"null", "string"},
									"description": "RFC3339 first observation time, or null if unknown; only present when include_dates is true",
								},
							},
							"required": []string{"sub"},
						},
					},
				},
			},
		},
	}
	res := map[string]interface{}{
		"ttlMs":      0,
		"cacheScope": "public",
		"tools":      tools,
	}
	writeRPCResult(w, req.ID, res)
}

func (h *Handler) handleToolsCall(w http.ResponseWriter, r *http.Request, req rpcRequest, remaining int) {
	var params struct {
		Name      string `json:"name"`
		Arguments struct {
			Apex         string `json:"apex"`
			OrderNew     *bool  `json:"order_new"`
			IncludeDates *bool  `json:"include_dates"`
		} `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeRPCError(w, req.ID, -32602, "invalid params")
		return
	}
	if params.Name != "search" {
		writeRPCError(w, req.ID, -32602, "unknown tool: "+params.Name)
		return
	}
	apexRaw := params.Arguments.Apex
	if apexRaw == "" {
		writeRPCError(w, req.ID, -32602, "missing apex")
		return
	}
	apex, err := norm.Apex(apexRaw)
	if err != nil {
		// Return error as RPC error with code -32602? Upstream returns tool error? For clone, return RPC error.
		writeRPCError(w, req.ID, -32602, err.Error())
		return
	}
	orderNew := false
	if params.Arguments.OrderNew != nil {
		orderNew = *params.Arguments.OrderNew
	}
	includeDates := false
	if params.Arguments.IncludeDates != nil {
		includeDates = *params.Arguments.IncludeDates
	}
	results, err := h.store.Search(r.Context(), apex, orderNew)
	if err != nil {
		writeRPCError(w, req.ID, -32603, "internal error")
		return
	}
	// Build inner JSON payload as text content, matching upstream: content[0].text = JSON string with {apex,count,rate_limit_remaining,subdomains}
	type subRow struct {
		Sub       string  `json:"sub"`
		FirstSeen *string `json:"first_seen,omitempty"`
	}
	var subs []subRow
	for _, res := range results {
		if includeDates {
			fs := res.FirstSeen.Format(time.RFC3339)
			subs = append(subs, subRow{Sub: res.Sub, FirstSeen: &fs})
		} else {
			subs = append(subs, subRow{Sub: res.Sub})
		}
	}
	if subs == nil {
		// Ensure empty array not null for consistency with upstream? Upstream returns null? Check: subdomains may be null or array.
		// We'll return empty array.
		subs = []subRow{}
	}
	inner := map[string]interface{}{
		"apex":                 apex,
		"count":                len(subs),
		"rate_limit_remaining": remaining,
		"subdomains":           subs,
	}
	innerJSON, _ := json.Marshal(inner)
	// wrap as MCP content
	content := []map[string]interface{}{
		{
			"type": "text",
			"text": string(innerJSON),
		},
	}
	res := map[string]interface{}{
		"content": content,
		"isError": false,
	}
	writeRPCResult(w, req.ID, res)
}

func writeRPCResult(w http.ResponseWriter, id *json.RawMessage, result interface{}) {
	resp := rpcResponse{
		JSONRPC: "2.0",
		Result:  result,
	}
	if id != nil {
		var v interface{}
		json.Unmarshal(*id, &v)
		resp.ID = v
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func writeRPCError(w http.ResponseWriter, id *json.RawMessage, code int, msg string) {
	resp := rpcResponse{
		JSONRPC: "2.0",
		Error:   &rpcError{Code: code, Message: msg},
	}
	if id != nil {
		var v interface{}
		json.Unmarshal(*id, &v)
		resp.ID = v
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
