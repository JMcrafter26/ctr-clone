package pipeline

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"ctindex/internal/norm"
	"ctindex/internal/store"
)

type Entry struct {
	DNS       []string
	IP        []string
	Timestamp int64 // ms since epoch
}

// Pipeline batches entries into store.
type Pipeline struct {
	store  *store.Store
	ch     chan Entry
	wg     sync.WaitGroup
	mu     sync.Mutex
	recent []store.Row
	maxRec int
}

func New(s *store.Store, buf int) *Pipeline {
	if buf < 8192 {
		buf = 8192
	}
	// larger default for catch-up bursts (follower does 8192 per tick)
	if buf < 65536 {
		// allow up to 64k queued entries without blocking too early, still back-pressures
		buf = 65536
	}
	return &Pipeline{
		store:  s,
		ch:     make(chan Entry, buf),
		maxRec: 32,
	}
}

func (p *Pipeline) Submit(e Entry) {
	// blocking with context-aware back-pressure; followers will slow down instead of dropping
	p.ch <- e
}

func (p *Pipeline) TrySubmit(e Entry) bool {
	select {
	case p.ch <- e:
		return true
	default:
		slog.Warn("pipeline channel full, dropping entry")
		return false
	}
}

func (p *Pipeline) Start(ctx context.Context) {
	p.wg.Add(1)
	go p.loop(ctx)
}

func (p *Pipeline) Stop() {
	close(p.ch)
	p.wg.Wait()
}

func (p *Pipeline) Wait() { p.wg.Wait() }

func (p *Pipeline) loop(ctx context.Context) {
	defer p.wg.Done()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	var batch []store.Row
	flush := func() {
		if len(batch) == 0 {
			return
		}
		// Normalize and dedupe within batch before insert
		// We use InsertBatchIgnore to keep earliest first_seen
		// Normalize DNS names to apex/sub
		var rows []store.Row
		for _, r := range batch {
			rows = append(rows, r)
		}
		if _, err := p.store.InsertBatchIgnore(ctx, rows); err != nil {
			slog.Error("pipeline flush failed", "err", err)
		} else {
			// update recent ring (keep last N)
			p.mu.Lock()
			for _, r := range rows {
				p.recent = append(p.recent, r)
				if len(p.recent) > p.maxRec {
					p.recent = p.recent[len(p.recent)-p.maxRec:]
				}
			}
			p.mu.Unlock()
		}
		batch = batch[:0]
	}
	for {
		select {
		case <-ctx.Done():
			flush()
			return
		case e, ok := <-p.ch:
			if !ok {
				flush()
				return
			}
			ts := time.UnixMilli(e.Timestamp).UTC()
			if e.Timestamp == 0 {
				ts = time.Now().UTC()
			}
			for _, name := range e.DNS {
				apex, sub, ok := norm.Subdomain(name)
				if !ok {
					continue
				}
				batch = append(batch, store.Row{Apex: apex, Sub: sub, FirstSeen: ts})
				// also ensure apex itself is indexed? crt.name indexes apex as sub==apex
				// The norm will produce apex==sub for apex itself, so it's already covered.
			}
			// IP SANs are ignored for subdomain index (they're not hostnames)
			if len(batch) >= 2000 {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

func (p *Pipeline) Recent() []store.Row {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]store.Row, len(p.recent))
	copy(out, p.recent)
	return out
}
