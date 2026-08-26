package backfill

import (
	"bufio"
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"ctindex/internal/pipeline"
)

// HaGeZi fetches public DNS blocklists (no key) — wildcard pro list
// https://github.com/hagezi/dns-blocklists
const hageziURL = "https://raw.githubusercontent.com/hagezi/dns-blocklists/main/wildcard/pro.txt"

type HaGeZi struct {
	pipeline *pipeline.Pipeline
	client   *http.Client
	interval time.Duration
}

func NewHaGeZi(p *pipeline.Pipeline) *HaGeZi {
	return &HaGeZi{
		pipeline: p,
		client:   &http.Client{Timeout: 60 * time.Second},
		interval: 6 * time.Hour,
	}
}

func (h *HaGeZi) Run(ctx context.Context) error {
	slog.Info("hagezi backfill starting", "url", hageziURL, "interval", h.interval)
	// initial fetch
	h.fetch(ctx)
	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			h.fetch(ctx)
		}
	}
}

func (h *HaGeZi) fetch(ctx context.Context) {
	req, err := http.NewRequestWithContext(ctx, "GET", hageziURL, nil)
	if err != nil {
		slog.Error("hagezi request", "err", err)
		return
	}
	resp, err := h.client.Do(req)
	if err != nil {
		slog.Error("hagezi fetch", "err", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		slog.Error("hagezi status", "code", resp.StatusCode)
		return
	}
	scanner := bufio.NewScanner(resp.Body)
	// large file ~500k lines, cap
	count := 0
	var batch []string
	flush := func() {
		if len(batch) == 0 {
			return
		}
		h.pipeline.Submit(pipeline.Entry{DNS: append([]string(nil), batch...), Timestamp: time.Now().UnixMilli()})
		count += len(batch)
		batch = batch[:0]
	}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// hagezi wildcard list may have format like "0.0.0.0 domain" or just domain; take last field
		fields := strings.Fields(line)
		domain := fields[len(fields)-1]
		domain = strings.ToLower(strings.TrimSpace(domain))
		if domain == "" || strings.Contains(domain, "*") || !strings.Contains(domain, ".") {
			continue
		}
		batch = append(batch, domain)
		if len(batch) >= 500 {
			flush()
		}
		// safety cap for single fetch to avoid OOM
		if count > 200000 {
			break
		}
	}
	flush()
	slog.Info("hagezi done", "domains", count)
}
