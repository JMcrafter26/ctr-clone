package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"ctindex/internal/api"
	"ctindex/internal/backfill"
	"ctindex/internal/config"
	"ctindex/internal/loglist"
	"ctindex/internal/mcp"
	"ctindex/internal/pipeline"
	"ctindex/internal/prober"
	"ctindex/internal/ratelimit"
	"ctindex/internal/store"
	"ctindex/internal/follower/staticct"
	rfc6962 "ctindex/internal/follower/rfc6962"
)

func main() {
	cfg := config.FromEnv()
	slog.Info("starting ctindex", "addr", cfg.Addr, "db", cfg.DBPath, "logs_yaml", cfg.LogsYAML)

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		slog.Error("open store", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	// Seed if empty
	if empty, _ := st.IsEmpty(context.Background()); empty {
		slog.Info("store empty, seeding demo data")
		if err := seedDemo(st); err != nil {
			slog.Error("seed failed", "err", err)
		}
	}

	pipe := pipeline.New(st, 8192)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pipe.Start(ctx)
	defer pipe.Stop()

	limiter := ratelimit.New(cfg.RateLimit)

	// Start followers if enabled and logs.yaml exists
	if cfg.EnableIngest {
		logsPath := cfg.LogsYAML
		if _, err := os.Stat(logsPath); err != nil {
			// fallback: try backend/logs.yaml when run from repo root
			if _, err2 := os.Stat("backend/" + logsPath); err2 == nil {
				logsPath = "backend/" + logsPath
			}
		}
		if _, err := os.Stat(logsPath); err == nil {
			curated, err := loglist.LoadCurated(logsPath)
			if err != nil {
				slog.Error("load logs.yaml", "err", err)
			} else {
				for _, e := range curated.StaticCT {
					f := staticct.New(staticct.LogConfig{
						Name:             e.Name,
						MonitoringPrefix: e.MonitoringPrefix,
						PublicKeyBase64:  e.PublicKey,
					}, st, pipe, cfg.ContactEmail)
					go func(name string, follower *staticct.Follower) {
						if err := follower.Run(ctx); err != nil && err != context.Canceled {
							slog.Error("staticct follower exited", "log", name, "err", err)
						}
					}(e.Name, f)
				}
			}
		} else {
			slog.Warn("logs.yaml not found, skipping staticct followers", "path", cfg.LogsYAML)
		}
		// Optionally start a few RFC6962 followers (Argon etc) — only if wanted; skip by default to avoid rate limits
		// Could be enabled via env ENABLE_RFC6962=1
		if os.Getenv("ENABLE_RFC6962") == "1" {
			// Use curated RFC logs: try fetch Chrome list
			chrome, err := loglist.FetchChrome(nil)
			if err != nil {
				slog.Warn("fetch chrome list", "err", err)
			} else {
				logs := loglist.UsableRFC6962Logs(chrome)
				// Limit to first 3 for politeness
				if len(logs) > 3 {
					logs = logs[:3]
				}
				for _, l := range logs {
					f := rfc6962.New(rfc6962.LogConfig{
						Description:  l.Description,
						URL:          l.URL,
						PublicKeyB64: l.Key,
					}, st, pipe, cfg.ContactEmail)
					go func(url string, follower *rfc6962.Follower) {
						if err := follower.Run(ctx); err != nil && err != context.Canceled {
							slog.Error("rfc6962 follower exited", "log", url, "err", err)
						}
					}(l.URL, f)
				}
			}
		}
	}

	// Backfill + prober workers (parallel to CT followers)
	if cfg.EnableIngest {
		// DNS prober — active, no key, default on (disable with ENABLE_DNS_PROBER=0)
		if envEnabled("ENABLE_DNS_PROBER", true) {
			pr := prober.New(st, pipe)
			go func() {
				if err := pr.Run(ctx); err != nil && err != context.Canceled {
					slog.Error("dns prober exited", "err", err)
				}
			}()
		}
		// HaGeZi — public blocklist, default on (disable with ENABLE_HAGEZI=0)
		if envEnabled("ENABLE_HAGEZI", true) {
			hz := backfill.NewHaGeZi(pipe)
			go func() {
				if err := hz.Run(ctx); err != nil && err != context.Canceled {
					slog.Error("hagezi exited", "err", err)
				}
			}()
		}
		// CZDS — watches data/czds/*.txt, default on if dir exists
		if envEnabled("ENABLE_CZDS", true) {
			cz := backfill.NewCZDS(pipe, "data/czds")
			go func() {
				if err := cz.Run(ctx); err != nil && err != context.Canceled {
					slog.Error("czds exited", "err", err)
				}
			}()
		}
		// Common Crawl — watches data/commoncrawl/*.txt, default on
		if envEnabled("ENABLE_COMMONCRAWL", true) {
			cc := backfill.NewCommonCrawl(pipe, "data/commoncrawl")
			go func() {
				if err := cc.Run(ctx); err != nil && err != context.Canceled {
					slog.Error("commoncrawl exited", "err", err)
				}
			}()
		}
		// Chaos — needs CHAOS_API_KEY, default off if no key
		if envEnabled("ENABLE_CHAOS", false) || os.Getenv("CHAOS_API_KEY") != "" {
			ch := backfill.NewChaos(pipe)
			go func() {
				if err := ch.Run(ctx); err != nil && err != context.Canceled {
					slog.Error("chaos exited", "err", err)
				}
			}()
		}
		// Internet Archive retired CT replay — stub, enable via ENABLE_IA=1 and data/ia/*.jsonl
		if envEnabled("ENABLE_IA", false) {
			slog.Info("ia backfill enabled but not yet implemented (place jsonl in data/ia/)")
		}
	}

	// API server - pass logs path for /v1/progress
	logsPathForAPI := cfg.LogsYAML
	// reuse resolved logsPath from follower section if available
	if _, err := os.Stat(logsPathForAPI); err != nil {
		if _, err2 := os.Stat("backend/" + logsPathForAPI); err2 == nil {
			logsPathForAPI = "backend/" + logsPathForAPI
		}
	}
	apiSrv := api.NewWithLogs(st, limiter, cfg.Token, logsPathForAPI)
	mcpHandler := mcp.New(st, limiter)

	mux := http.NewServeMux()
	// Mount API routes
	mux.Handle("/v1/", apiSrv.Routes())
	mux.Handle("/healthz", apiSrv.Routes())
	// MCP handles its own rate limiting
	mux.Handle("/mcp", mcpHandler)
	// Also serve frontend static if exists? For now, just API.
	// Add root handler that returns API docs JSON or simple page if Accept: text/html
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><html><head><meta charset="utf-8"><title>ctindex</title></head><body><h1>ctindex API</h1><p>See <a href="/v1/stats">/v1/stats</a> or query <code>/v1/search?apex=example.com</code></p></body></html>`)
	})

	// Wrap with logging and CORS already in api routes, but mux needs CORS for root/mcp
	handler := loggingMiddleware(corsGlobal(mux))

	srv := &http.Server{
		Addr:         cfg.Addr,
		Handler:      handler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Graceful shutdown
	go func() {
		slog.Info("listening", "addr", cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("listen", "err", err)
			os.Exit(1)
		}
	}()

	// Wait for signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	slog.Info("shutting down")
	cancel()
	shutdownCtx, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown", "err", err)
	}
}

func corsGlobal(next http.Handler) http.Handler {
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

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		slog.Info("request", "method", r.Method, "path", r.URL.Path, "query", r.URL.RawQuery, "remote", r.RemoteAddr, "dur", time.Since(start))
	})
}

func seedDemo(st *store.Store) error {
	ctx := context.Background()
	// Demo apexes with plausible subdomains
	demo := []struct {
		apex string
		subs []string
	}{
		{"example.com", []string{"example.com", "www.example.com", "mail.example.com", "api.example.com", "blog.example.com", "shop.example.com", "vpn.example.com", "cdn.example.com", "dev.example.com", "staging.example.com", "test.example.com", "admin.example.com", "secure.example.com", "login.example.com", "assets.example.com"}},
		{"namecheap.com", []string{"namecheap.com", "developer.namecheap.com", "community.namecheap.com", "api.namecheap.com", "blog.namecheap.com", "support.namecheap.com", "status.namecheap.com", "www.namecheap.com"}},
		{"google.com", []string{"google.com", "www.google.com", "mail.google.com", "drive.google.com", "accounts.google.com", "maps.google.com", "docs.google.com", "play.google.com", "api.google.com", "cloud.google.com", "admin.google.com"}},
		{"github.com", []string{"github.com", "api.github.com", "gist.github.com", "raw.githubusercontent.com", "education.github.com", "docs.github.com", "support.github.com"}},
		{"cloudflare.com", []string{"cloudflare.com", "api.cloudflare.com", "dash.cloudflare.com", "www.cloudflare.com", "blog.cloudflare.com", "developers.cloudflare.com", "support.cloudflare.com"}},
		{"stripe.com", []string{"stripe.com", "api.stripe.com", "dashboard.stripe.com", "js.stripe.com", "hooks.stripe.com", "files.stripe.com"}},
	}

	var rows []store.Row
	now := time.Now().UTC()
	for _, d := range demo {
		base := now.Add(-time.Hour * 24 * 365 * 2) // 2 years ago
		for i, sub := range d.subs {
			apex, subNorm, ok := normalize(sub)
			if !ok {
				continue
			}
			_ = apex
			ts := base.Add(time.Duration(i*37) * time.Hour * 24) // spread
			if sub == d.apex {
				ts = base
			}
			rows = append(rows, store.Row{Apex: d.apex, Sub: subNorm, FirstSeen: ts})
		}
	}
	// Add some demo ticker entries with OLD timestamps so live CT (now) outranks them.
	// Previously this used now - i minutes which dominated recent and hid real ingest.
	for i := 0; i < 16; i++ {
		apex := fmt.Sprintf("demo%d.com", i)
		sub := fmt.Sprintf("www.demo%d.com", i)
		// 2 years + i days ago, so any live 2026 cert will be newer and appear in recent
		ts := now.Add(-time.Hour*24*365*2 - time.Duration(i)*24*time.Hour)
		rows = append(rows, store.Row{Apex: apex, Sub: sub, FirstSeen: ts})
		rows = append(rows, store.Row{Apex: apex, Sub: apex, FirstSeen: ts})
	}
	// Insert
	_, err := st.InsertBatchIgnore(ctx, rows)
	return err
}

func envEnabled(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	v = strings.ToLower(strings.TrimSpace(v))
	return v != "0" && v != "false" && v != "no" && v != "off"
}

func normalize(s string) (apex, sub string, ok bool) {
	// quick normalize: lowercase, trim
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimSuffix(s, ".")
	// Use norm package would be better, but avoid import cycle
	// We'll just validate via publicsuffix indirectly: if contains ., use norm
	// For seed we assume valid
	if !strings.Contains(s, ".") {
		return "", "", false
	}
	// Derive apex via simple: last two labels (not accurate for co.uk but ok for seed)
	parts := strings.Split(s, ".")
	if len(parts) < 2 {
		return "", "", false
	}
	apexGuess := strings.Join(parts[len(parts)-2:], ".")
	return apexGuess, s, true
}
