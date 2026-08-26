package backfill

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"time"

	"ctindex/internal/pipeline"
)

// Chaos fetches ProjectDiscovery Chaos dataset (requires CHAOS_API_KEY env)
// https://chaos.projectdiscovery.io
// If no key, it logs and skips (stub).

type Chaos struct {
	pipeline *pipeline.Pipeline
	client   *http.Client
	interval time.Duration
	apiKey   string
}

func NewChaos(p *pipeline.Pipeline) *Chaos {
	return &Chaos{
		pipeline: p,
		client:   &http.Client{Timeout: 30 * time.Second},
		interval: 12 * time.Hour,
		apiKey:   os.Getenv("CHAOS_API_KEY"),
	}
}

func (c *Chaos) Run(ctx context.Context) error {
	if c.apiKey == "" {
		slog.Info("chaos backfill skipped (no CHAOS_API_KEY)")
		return nil
	}
	slog.Info("chaos backfill starting", "interval", c.interval)
	c.fetch(ctx)
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			c.fetch(ctx)
		}
	}
}

func (c *Chaos) fetch(ctx context.Context) {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://dns.projectdiscovery.io/dns/chaos/containers", nil)
	if err != nil {
		slog.Error("chaos request", "err", err)
		return
	}
	req.Header.Set("Authorization", c.apiKey)
	resp, err := c.client.Do(req)
	if err != nil {
		slog.Error("chaos fetch", "err", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		slog.Error("chaos status", "code", resp.StatusCode)
		return
	}
	var data struct {
		Domains []string `json:"domains"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		// fallback: try line-delimited?
		slog.Error("chaos decode", "err", err)
		return
	}
	count := 0
	var batch []string
	for _, d := range data.Domains {
		batch = append(batch, d)
		if len(batch) >= 500 {
			c.pipeline.Submit(pipeline.Entry{DNS: append([]string(nil), batch...), Timestamp: time.Now().UnixMilli()})
			count += len(batch)
			batch = batch[:0]
		}
	}
	if len(batch) > 0 {
		c.pipeline.Submit(pipeline.Entry{DNS: batch, Timestamp: time.Now().UnixMilli()})
		count += len(batch)
	}
	slog.Info("chaos done", "domains", count)
}
