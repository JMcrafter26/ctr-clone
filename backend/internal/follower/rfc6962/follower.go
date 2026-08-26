package rfc6962

import (
	"context"
	stdx509 "crypto/x509"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"ctindex/internal/pipeline"
	"ctindex/internal/store"
	"github.com/google/certificate-transparency-go/client"
	"github.com/google/certificate-transparency-go/jsonclient"
	ctx509 "github.com/google/certificate-transparency-go/x509"
)

type LogConfig struct {
	Description string
	URL         string
	PublicKeyB64 string // base64 encoded SPKI
}

type Follower struct {
	cfg      LogConfig
	store    *store.Store
	pipeline *pipeline.Pipeline
	contact  string
}

func New(cfg LogConfig, s *store.Store, p *pipeline.Pipeline, contact string) *Follower {
	return &Follower{cfg: cfg, store: s, pipeline: p, contact: contact}
}

func (f *Follower) Run(ctx context.Context) error {
	// Decode key for ct client? ct client needs log's public key? Actually jsonclient doesn't verify, but we can pass key via options if needed.
	// For now, create client without key; verification optional.
	hc := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        10,
			MaxIdleConnsPerHost: 4,
		},
	}
	// Add UA
	origTransport := hc.Transport
	if origTransport == nil {
		origTransport = http.DefaultTransport
	}
	ua := fmt.Sprintf("ctindex/1.0 (+https://github.com/ctindex; contact %s)", f.contact)
	hc.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		req.Header.Set("User-Agent", ua)
		return origTransport.RoundTrip(req)
	})

	logClient, err := client.New(f.cfg.URL, hc, jsonclient.Options{UserAgent: ua})
	if err != nil {
		return fmt.Errorf("ct client new: %w", err)
	}
	// Try decode key to validate STH? Not required for tailing.
	if f.cfg.PublicKeyB64 != "" {
		if _, err := parseKey(f.cfg.PublicKeyB64); err != nil {
			slog.Warn("failed to parse log public key, continuing without verification", "log", f.cfg.URL, "err", err)
		}
	}

	startIdx, _, err := f.store.GetLogState(ctx, f.cfg.URL)
	if err != nil {
		slog.Error("get log state", "log", f.cfg.URL, "err", err)
		startIdx = 0
	}
	slog.Info("rfc6962 follower starting", "log", f.cfg.URL, "start", startIdx)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		sth, err := logClient.GetSTH(ctx)
		if err != nil {
			slog.Error("get sth failed", "log", f.cfg.URL, "err", err)
			sleepCtx(ctx, 10*time.Second)
			continue
		}
		size := int64(sth.TreeSize)
		if startIdx >= size {
			sleepCtx(ctx, 5*time.Second)
			continue
		}
		end := size
		if end-startIdx > 256 {
			end = startIdx + 256
		}
		entries, err := logClient.GetEntries(ctx, startIdx, end-1)
		if err != nil {
			slog.Error("get entries failed", "log", f.cfg.URL, "start", startIdx, "end", end, "err", err)
			sleepCtx(ctx, 5*time.Second)
			continue
		}
		for _, e := range entries {
			var dnsNames []string
			var ts uint64
			if e.X509Cert != nil {
				dnsNames = collectX509Names(e.X509Cert)
				ts = e.Leaf.TimestampedEntry.Timestamp
			} else if e.Precert != nil && e.Precert.TBSCertificate != nil {
				dnsNames = collectX509Names(e.Precert.TBSCertificate)
				ts = e.Leaf.TimestampedEntry.Timestamp
			} else if e.JSONData != nil {
				continue
			} else {
				continue
			}
			if len(dnsNames) == 0 {
				continue
			}
			f.pipeline.Submit(pipeline.Entry{
				DNS:       dnsNames,
				Timestamp: int64(ts),
			})
		}
		next := end
		if err := f.store.SetLogState(ctx, f.cfg.URL, next); err != nil {
			slog.Error("set log state", "log", f.cfg.URL, "err", err)
		}
		startIdx = next
		// Rate limit politely
		sleepCtx(ctx, 500*time.Millisecond)
	}
}

func collectNames(cert *stdx509.Certificate) []string {
	var out []string
	if cert.Subject.CommonName != "" {
		out = append(out, cert.Subject.CommonName)
	}
	out = append(out, cert.DNSNames...)
	return out
}

func collectX509Names(cert *ctx509.Certificate) []string {
	var out []string
	if cert.Subject.CommonName != "" {
		out = append(out, cert.Subject.CommonName)
	}
	out = append(out, cert.DNSNames...)
	return out
}

func parseKey(b64 string) (interface{}, error) {
	der, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, err
	}
	return stdx509.ParsePKIXPublicKey(der)
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
