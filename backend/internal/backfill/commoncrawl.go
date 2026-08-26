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

// CommonCrawl ingests hostnames from Common Crawl WAT/WARC paths if present locally.
// This is a stub that watches data/commoncrawl/*.txt (one host per line) or *.warc paths.
// For real Common Crawl, download e.g., https://data.commoncrawl.org/crawl-data/CC-MAIN-2024-10/warc.paths.gz
// and extract hosts via: zcat warc.paths.gz | head -100 | xargs ... The full pipeline is out of scope for single binary.
type CommonCrawl struct {
	pipeline *pipeline.Pipeline
	dir      string
	interval time.Duration
	seen     map[string]bool
}

func NewCommonCrawl(p *pipeline.Pipeline, dir string) *CommonCrawl {
	if dir == "" {
		dir = "data/commoncrawl"
	}
	return &CommonCrawl{pipeline: p, dir: dir, interval: 15 * time.Minute, seen: make(map[string]bool)}
}

func (c *CommonCrawl) Run(ctx context.Context) error {
	slog.Info("commoncrawl backfill starting", "dir", c.dir)
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

func (c *CommonCrawl) scan(ctx context.Context, dirs []string) {
	for _, d := range dirs {
		for _, pattern := range []string{"*.txt", "*.hosts"} {
			matches, _ := filepath.Glob(filepath.Join(d, pattern))
			for _, f := range matches {
				if c.seen[f] {
					continue
				}
				c.ingestFile(ctx, f)
				c.seen[f] = true
			}
		}
	}
}

func (c *CommonCrawl) ingestFile(ctx context.Context, path string) {
	f, err := os.Open(path)
	if err != nil {
		slog.Error("commoncrawl open", "path", path, "err", err)
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
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// support "host: example.com" or just "example.com"
		if idx := strings.LastIndex(line, " "); idx != -1 {
			line = line[idx+1:]
		}
		domain := strings.ToLower(strings.TrimSpace(line))
		if !strings.Contains(domain, ".") {
			continue
		}
		batch = append(batch, domain)
		if len(batch) >= 1000 {
			flush()
		}
	}
	flush()
	slog.Info("commoncrawl ingested", "path", path, "hosts", count)
}
