package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"ctindex/internal/loglist"
	"ctindex/internal/norm"
	"ctindex/internal/pipeline"
	"ctindex/internal/ratelimit"
	"ctindex/internal/store"
)

type Server struct {
	store    *store.Store
	limiter  *ratelimit.Limiter
	token    string // if set, protects find/top/stream
	logsYAML string
	pipeline *pipeline.Pipeline
}

func New(s *store.Store, limiter *ratelimit.Limiter, token string) *Server {
	return &Server{store: s, limiter: limiter, token: token}
}

func NewWithLogs(s *store.Store, limiter *ratelimit.Limiter, token, logsYAML string) *Server {
	return &Server{store: s, limiter: limiter, token: token, logsYAML: logsYAML}
}

func (srv *Server) SetPipeline(p *pipeline.Pipeline) { srv.pipeline = p }

func (srv *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	// Public search with rate limit middleware
	mux.Handle("/v1/search", srv.limiter.Middleware(http.HandlerFunc(srv.handleSearch)))
	mux.Handle("/v1/stats", http.HandlerFunc(srv.handleStats))
	mux.Handle("/v1/progress", http.HandlerFunc(srv.handleProgress))
	mux.Handle("/v1/find", http.HandlerFunc(srv.handleFind))
	mux.Handle("/v1/top", http.HandlerFunc(srv.handleTop))
	mux.Handle("/v1/stream", http.HandlerFunc(srv.handleStream))
	mux.Handle("/healthz", http.HandlerFunc(srv.handleHealth))
	// CORS wrapper
	handler := corsMiddleware(mux)
	// Also handle OPTIONS
	return handler
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (srv *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (srv *Server) handleProgress(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	// total indexed
	total, _ := srv.store.CountAll(ctx)
	// log_state rows
	rows, err := srv.store.DB().QueryContext(ctx, `SELECT log_name, next_index, updated_at FROM log_state`)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	type logProg struct {
		Name           string  `json:"name"`
		NextIndex      int64   `json:"next_index"`
		UpdatedAt      string  `json:"updated_at"`
		CheckpointSize *int64  `json:"checkpoint_size,omitempty"`
		Percent        *float64 `json:"percent,omitempty"`
		Behind         *int64  `json:"behind,omitempty"`
		MonitoringURL  string  `json:"monitoring_url,omitempty"`
	}
	var logs []logProg
	state := make(map[string]logProg)
	for rows.Next() {
		var name string
		var idx, updated int64
		if err := rows.Scan(&name, &idx, &updated); err != nil {
			continue
		}
		state[name] = logProg{Name: name, NextIndex: idx, UpdatedAt: time.UnixMilli(updated).UTC().Format(time.RFC3339)}
	}
	// try to enrich with curated logs.yaml if available
	var curated *loglist.Curated
	if srv.logsYAML != "" {
		if c, err := loglist.LoadCurated(srv.logsYAML); err == nil {
			curated = c
		} else if c, err := loglist.LoadCurated("backend/" + srv.logsYAML); err == nil {
			curated = c
		}
	}
	// fetch checkpoint sizes with short timeout, best-effort
	client := &http.Client{Timeout: 4 * time.Second}
	for _, lp := range state {
		entry := lp
		// find monitoring URL if curated
		var monURL string
		if curated != nil {
			for _, e := range curated.StaticCT {
				if e.Name == lp.Name {
					monURL = e.MonitoringPrefix
					break
				}
			}
		}
		entry.MonitoringURL = monURL
		if monURL != "" {
			// fetch checkpoint
			if size, err := fetchCheckpointSize(r.Context(), client, monURL); err == nil {
				entry.CheckpointSize = &size
				pct := float64(entry.NextIndex) / float64(size) * 100
				if pct > 100 {
					pct = 100
				}
				entry.Percent = &pct
				behind := size - entry.NextIndex
				if behind < 0 {
					behind = 0
				}
				entry.Behind = &behind
			}
		}
		logs = append(logs, entry)
	}
	// include logs that are in curated but not yet in state (0%)
	if curated != nil {
		for _, e := range curated.StaticCT {
			if _, ok := state[e.Name]; !ok {
				ent := logProg{Name: e.Name, NextIndex: 0, UpdatedAt: time.Now().UTC().Format(time.RFC3339), MonitoringURL: e.MonitoringPrefix}
				if size, err := fetchCheckpointSize(r.Context(), client, e.MonitoringPrefix); err == nil {
					ent.CheckpointSize = &size
					pct := 0.0
					ent.Percent = &pct
					ent.Behind = &size
				}
				logs = append(logs, ent)
			}
		}
	}
	if logs == nil {
		logs = []logProg{}
	}
	resp := struct {
		Subdomains int64     `json:"subdomains"`
		Logs       []logProg `json:"logs"`
	}{
		Subdomains: total,
		Logs:       logs,
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(resp)
}

func fetchCheckpointSize(ctx context.Context, client *http.Client, monitoringPrefix string) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", monitoringPrefix+"checkpoint", nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "text/plain")
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("status %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, err
	}
	lines := strings.Split(string(b), "\n")
	if len(lines) < 2 {
		return 0, fmt.Errorf("malformed checkpoint")
	}
	return strconv.ParseInt(strings.TrimSpace(lines[1]), 10, 64)
}

func (srv *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	count, err := srv.store.CountAll(ctx)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	recentRows, err := srv.store.Recent(ctx, 16)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	type recent struct {
		Apex      string `json:"apex"`
		Sub       string `json:"sub"`
		FirstSeen string `json:"first_seen"`
	}
	resp := struct {
		Subdomains int64    `json:"subdomains"`
		Recent     []recent `json:"recent"`
	}{
		Subdomains: count,
	}
	for _, row := range recentRows {
		resp.Recent = append(resp.Recent, recent{
			Apex:      row.Apex,
			Sub:       row.Sub,
			FirstSeen: row.FirstSeen.Format(time.RFC3339Nano),
		})
	}
	if resp.Recent == nil {
		resp.Recent = []recent{}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(resp)
}

func (srv *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	apexRaw := r.URL.Query().Get("apex")
	if apexRaw == "" {
		srv.writeError(w, r, http.StatusBadRequest, "missing apex parameter")
		return
	}
	apex, err := norm.Apex(apexRaw)
	if err != nil {
		srv.writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	// Query params
	format := r.URL.Query().Get("format")
	includeDates := r.URL.Query().Get("dates") == "1"
	orderNew := r.URL.Query().Get("order_new") == "1" || r.URL.Query().Get("order_new") == "true"
	// Note: MCP uses include_dates, but HTTP uses dates=1

	results, err := srv.store.Search(r.Context(), apex, orderNew)
	if err != nil {
		slog.Error("search failed", "apex", apex, "err", err)
		srv.writeError(w, r, http.StatusInternalServerError, "internal error")
		return
	}
	// on-demand: if not found and pipeline available, probe common names for this apex in background
	if len(results) == 0 && srv.pipeline != nil && isOnDemandEnabled() {
		go srv.probeApex(apex)
	}

	// Rate limit remaining for response (already consumed via middleware, so we fetch remaining)
	// Middleware already set headers; for JSON we also need to include rate_limit_remaining in MCP but not here.

	if format == "json" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		// Also need X-RateLimit headers already set by middleware, but ensure they persist
		type outRow struct {
			Sub       string `json:"sub"`
			FirstSeen *string `json:"first_seen,omitempty"`
		}
		// If dates=1, include first_seen; otherwise omit
		// When dates=1 and format=json, output is [{"first_seen":"...","sub":"..."}]
		// Order: first_seen first, then sub (but json order doesn't matter; match upstream: first_seen first)
		// We'll produce custom marshaling to match order.
		if includeDates {
			// Build slice with first_seen
			arr := make([]map[string]string, 0, len(results))
			for _, res := range results {
				fs := res.FirstSeen.Format(time.RFC3339)
				arr = append(arr, map[string]string{"first_seen": fs, "sub": res.Sub})
			}
			// Need to ensure ordering of keys in JSON is first_seen then sub; using custom struct with ordered fields
			// Use struct with json tags ordered: first_seen first
			type jsonRow struct {
				FirstSeen string `json:"first_seen"`
				Sub       string `json:"sub"`
			}
			typed := make([]jsonRow, 0, len(arr))
			for _, m := range arr {
				typed = append(typed, jsonRow{FirstSeen: m["first_seen"], Sub: m["sub"]})
			}
			json.NewEncoder(w).Encode(typed)
		} else {
			type jsonRow struct {
				Sub string `json:"sub"`
			}
			typed := make([]jsonRow, 0, len(results))
			for _, res := range results {
				typed = append(typed, jsonRow{Sub: res.Sub})
			}
			json.NewEncoder(w).Encode(typed)
		}
		return
	}
	// Default text/plain, one per line, tab + date if dates=1
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if includeDates {
		for _, res := range results {
			fmt.Fprintf(w, "%s\t%s\n", res.Sub, res.FirstSeen.Format(time.RFC3339))
		}
	} else {
		for _, res := range results {
			fmt.Fprintf(w, "%s\n", res.Sub)
		}
	}
}

func (srv *Server) handleFind(w http.ResponseWriter, r *http.Request) {
	if !srv.checkAuth(w, r) {
		return
	}
	q := r.URL.Query().Get("q")
	if q == "" {
		srv.writeError(w, r, http.StatusBadRequest, "missing q parameter")
		return
	}
	limit := 100
	if v := r.URL.Query().Get("n"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}
	format := r.URL.Query().Get("format")
	includeDates := r.URL.Query().Get("dates") == "1"
	results, err := srv.store.Find(r.Context(), q, limit)
	if err != nil {
		slog.Error("find failed", "q", q, "err", err)
		srv.writeError(w, r, http.StatusInternalServerError, "internal error")
		return
	}
	if format == "json" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		type row struct {
			Sub       string  `json:"sub"`
			FirstSeen *string `json:"first_seen,omitempty"`
			Apex      string  `json:"apex,omitempty"`
		}
		var out []row
		for _, res := range results {
			if includeDates {
				fs := res.FirstSeen.Format(time.RFC3339)
				out = append(out, row{Sub: res.Sub, FirstSeen: &fs})
			} else {
				out = append(out, row{Sub: res.Sub})
			}
		}
		if out == nil {
			out = []row{}
		}
		json.NewEncoder(w).Encode(out)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if includeDates {
		for _, res := range results {
			fmt.Fprintf(w, "%s\t%s\n", res.Sub, res.FirstSeen.Format(time.RFC3339))
		}
	} else {
		for _, res := range results {
			fmt.Fprintf(w, "%s\n", res.Sub)
		}
	}
}

func (srv *Server) handleTop(w http.ResponseWriter, r *http.Request) {
	if !srv.checkAuth(w, r) {
		return
	}
	n := 100
	if v := r.URL.Query().Get("n"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 && parsed <= 1000 {
			n = parsed
		}
	}
	results, err := srv.store.Top(r.Context(), n)
	if err != nil {
		slog.Error("top failed", "err", err)
		srv.writeError(w, r, http.StatusInternalServerError, "internal error")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	type row struct {
		Apex  string `json:"apex"`
		Count int64  `json:"count"`
	}
	var out []row
	for _, res := range results {
		out = append(out, row{Apex: res.Apex, Count: res.Count})
	}
	if out == nil {
		out = []row{}
	}
	json.NewEncoder(w).Encode(out)
}

func (srv *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	if !srv.checkAuth(w, r) {
		return
	}
	// SSE
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	// Send initial comment
	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	// For demo, we poll recent and stream new entries
	seen := make(map[string]bool)
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			recent, err := srv.store.Recent(ctx, 8)
			if err != nil {
				continue
			}
			for _, row := range recent {
				key := row.Apex + "|" + row.Sub
				if seen[key] {
					continue
				}
				seen[key] = true
				data, _ := json.Marshal(map[string]string{
					"apex":       row.Apex,
					"sub":        row.Sub,
					"first_seen": row.FirstSeen.Format(time.RFC3339Nano),
				})
				fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
				// Limit recent streaming to avoid flood; only first new per tick
				if len(seen) > 200 {
					// reset
					seen = make(map[string]bool)
				}
				break
			}
		}
	}
}

func (srv *Server) checkAuth(w http.ResponseWriter, r *http.Request) bool {
	if srv.token == "" {
		return true
	}
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		http.Error(w, "missing bearer token", http.StatusUnauthorized)
		return false
	}
	tok := strings.TrimPrefix(auth, "Bearer ")
	if tok != srv.token {
		http.Error(w, "invalid bearer token", http.StatusUnauthorized)
		return false
	}
	return true
}

func (srv *Server) writeError(w http.ResponseWriter, r *http.Request, code int, msg string) {
	// Ensure rate limit headers are present even on errors for /v1/search? But handleSearch is behind middleware already.
	// For other handlers, we set generic.
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.Error(w, msg, code)
}

// RateLimitHeaders helper for MCP to manually inject
func (srv *Server) RateLimitHeaders(ip string) (limit, remaining int) {
	remaining = srv.limiter.Remaining(ip)
	limit = srv.limiter.Limit()
	return
}

func (srv *Server) Store() *store.Store { return srv.store }

func isOnDemandEnabled() bool {
	v := os.Getenv("ENABLE_ONDEMAND")
	if v == "" {
		return true // on by default for self-host
	}
	v = strings.ToLower(strings.TrimSpace(v))
	return v != "0" && v != "false" && v != "no" && v != "off"
}

var onDemandNames = []string{"www", "mail", "api", "blog", "shop", "cdn", "dev", "staging", "test", "admin", "secure", "login", "assets", "m", "app", "portal", "support", "docs", "status", "vpn", "ns1", "ns2", "ftp", "smtp", "webmail", "autodiscover", "exchange", "git", "api2", "beta", "demo", "stage", "prod", "v1", "v2", "mobile", "preview", "sandbox", "uat", "qa", "preprod"}

func (srv *Server) probeApex(apex string) {
	// always ensure apex itself is indexed
	srv.pipeline.Submit(pipeline.Entry{DNS: []string{apex}, Timestamp: time.Now().UnixMilli()})
	for _, n := range onDemandNames {
		fqdn := n + "." + apex
		// quick check with timeout
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, err := net.DefaultResolver.LookupHost(ctx, fqdn)
		cancel()
		if err == nil {
			srv.pipeline.Submit(pipeline.Entry{DNS: []string{fqdn}, Timestamp: time.Now().UnixMilli()})
		}
		// small delay to be polite to resolver 192.168.2.221
		time.Sleep(30 * time.Millisecond)
	}
	slog.Info("on-demand probe done", "apex", apex)
}

var _ = context.Background
