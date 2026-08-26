package backfill

import (
	"bufio"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ctindex/internal/pipeline"
)

// CZDS ingests ICANN CZDS zone files from local directory (per-TLD, requires auth externally)
// Place files as data/czds/*.txt (one domain per line, e.g., example.com). The watcher polls the dir.
type CZDS struct {
	pipeline *pipeline.Pipeline
	dir      string
	interval time.Duration
	seen     map[string]bool
}

func NewCZDS(p *pipeline.Pipeline, dir string) *CZDS {
	if dir == "" {
		dir = "data/czds"
	}
	return &CZDS{pipeline: p, dir: dir, interval: 10 * time.Minute, seen: make(map[string]bool)}
}

func (c *CZDS) Run(ctx context.Context) error {
	slog.Info("czds backfill starting", "dir", c.dir)
	// also try backend/data/czds when run from root
	dirs := []string{c.dir, "backend/" + c.dir}
	c.scan(ctx, dirs)
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			c.scan(ctx, dirs)
		}
	}
}

func (c *CZDS) scan(ctx context.Context, dirs []string) {
	for _, d := range dirs {
		matches, _ := filepath.Glob(filepath.Join(d, "*.txt"))
		for _, f := range matches {
			if c.seen[f] {
				continue
			}
			c.ingestFile(ctx, f)
			c.seen[f] = true
		}
		// also check .gz? skip
		if len(matches) == 0 {
			slog.Debug("czds no files", "dir", d)
		}
	}
}

func (c *CZDS) ingestFile(ctx context.Context, path string) {
	f, err := os.Open(path)
	if err != nil {
		slog.Error("czds open", "path", path, "err", err)
		return
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	count := 0
	var batch []string
	flush := func() {
		if len(batch) == 0 {
			return
		}
		c.pipeline.Submit(pipeline.Entry{DNS: append([]string(nil), batch...), Timestamp: time.Now().UnixMilli()})
		count += len(batch)
		batch = batch[:0]
	}
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		// zone file may have "example.com. 3600 IN A ..." -> take first field without trailing dot
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		domain := strings.TrimSuffix(fields[0], ".")
		domain = strings.ToLower(domain)
		if !strings.Contains(domain, ".") {
			continue
		}
		batch = append(batch, domain)
		if len(batch) >= 1000 {
			flush()
		}
	}
	flush()
	slog.Info("czds ingested", "path", path, "domains", count)
}
