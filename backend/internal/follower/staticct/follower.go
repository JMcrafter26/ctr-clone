package staticct

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"ctindex/internal/pipeline"
	"ctindex/internal/store"
	"filippo.io/sunlight"
)

type LogConfig struct {
	Name              string
	MonitoringPrefix  string
	PublicKeyBase64   string
}

type Follower struct {
	cfg      LogConfig
	store    *store.Store
	pipeline *pipeline.Pipeline
	client   *http.Client
	contact  string
}

func New(cfg LogConfig, s *store.Store, p *pipeline.Pipeline, contact string) *Follower {
	ua := fmt.Sprintf("ctindex/1.0 (+https://github.com/ctindex; contact %s)", contact)
	return &Follower{
		cfg:      cfg,
		store:    s,
		pipeline: p,
		client: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        20,
				MaxIdleConnsPerHost: 5,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		contact: ua,
	}
}

func (f *Follower) Run(ctx context.Context) error {
	pubKey, err := parsePublicKey(f.cfg.PublicKeyBase64)
	if err != nil {
		return fmt.Errorf("parse public key for %s: %w", f.cfg.Name, err)
	}
	hc := f.client
	// Wrap transport to add Accept-Encoding gzip (torchwood does already but ensure)
	if hc.Transport == nil {
		hc.Transport = http.DefaultTransport
	}
	client, err := sunlight.NewClient(&sunlight.ClientConfig{
		MonitoringPrefix: f.cfg.MonitoringPrefix,
		PublicKey:        pubKey,
		HTTPClient:       hc,
		UserAgent:        f.contact,
		Timeout:          2 * time.Minute,
		ConcurrencyLimit: 8,
		Logger:           slog.Default(),
	})
	if err != nil {
		return err
	}
	// Resume position
	startIdx, _, err := f.store.GetLogState(ctx, f.cfg.Name)
	if err != nil {
		slog.Error("get log state", "log", f.cfg.Name, "err", err)
		startIdx = 0
	}
	slog.Info("staticct follower starting", "log", f.cfg.Name, "start", startIdx)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		// Get checkpoint to know current size
		cp, _, err := client.Checkpoint(ctx)
		if err != nil {
			slog.Error("checkpoint fetch failed", "log", f.cfg.Name, "err", err)
			sleepCtx(ctx, 10*time.Second)
			continue
		}
		size := cp.N
		if startIdx >= size {
			// caught up, poll
			sleepCtx(ctx, 5*time.Second)
			continue
		}
		end := size
		// Cap batch to 8192 entries per iteration to avoid huge memory
		if end-startIdx > 8192 {
			end = startIdx + 8192
		}
		slog.Info("fetching entries", "log", f.cfg.Name, "from", startIdx, "to", end)
		// Use UnauthenticatedTrimmedEntries which auto uses names tiles
		count := 0
		for _, entry := range client.UnauthenticatedTrimmedEntries(ctx, startIdx, end) {
			count++
			f.pipeline.Submit(pipeline.Entry{
				DNS:       entry.DNS,
				IP:        nil,
				Timestamp: entry.Timestamp,
			})
		}
		if err := client.Err(); err != nil {
			slog.Error("entries iteration error", "log", f.cfg.Name, "err", err)
			sleepCtx(ctx, 5*time.Second)
			// don't advance cursor on error; retry same range
			continue
		}
		if count == 0 {
			// no entries yielded but size advanced? update state anyway
		}
		next := end
		if err := f.store.SetLogState(ctx, f.cfg.Name, next); err != nil {
			slog.Error("set log state", "log", f.cfg.Name, "err", err)
		}
		startIdx = next
		// Small throttle to respect rate limits
		sleepCtx(ctx, 500*time.Millisecond)
	}
}

func parsePublicKey(b64 string) (interface{}, error) {
	der, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, err
	}
	return x509.ParsePKIXPublicKey(der)
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
