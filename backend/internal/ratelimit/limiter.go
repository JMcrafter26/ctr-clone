package ratelimit

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type Limiter struct {
	limit int
	mu    sync.Mutex
	// keyed by IP, value is count and window day
	buckets map[string]*bucket
}

type bucket struct {
	day   string // YYYY-MM-DD UTC
	count int
}

func New(limit int) *Limiter {
	// 0 = disabled (no limiting) — self-host default. Do not coerce to 100.
	return &Limiter{
		limit:   limit,
		buckets: make(map[string]*bucket),
	}
}

func (l *Limiter) Enabled() bool { return l.limit > 0 }

func (l *Limiter) Allow(ip string) (remaining int, limited bool) {
	if l.limit <= 0 {
		return 0, false
	}
	day := time.Now().UTC().Format("2006-01-02")
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[ip]
	if !ok || b.day != day {
		b = &bucket{day: day, count: 0}
		l.buckets[ip] = b
	}
	if b.count >= l.limit {
		return 0, true
	}
	b.count++
	remaining = l.limit - b.count
	return remaining, false
}

func (l *Limiter) Remaining(ip string) int {
	if l.limit <= 0 {
		return 0
	}
	day := time.Now().UTC().Format("2006-01-02")
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[ip]
	if !ok || b.day != day {
		return l.limit
	}
	return l.limit - b.count
}

func (l *Limiter) Limit() int { return l.limit }

// Middleware wraps a handler with rate limiting and adds X-RateLimit headers.
// Only counts successful allowance; if limited returns 429.
// If limit <=0, limiting is disabled and request passes through with no headers.
func (l *Limiter) Middleware(next http.Handler) http.Handler {
	if l.limit <= 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := ClientIP(r)
		remaining, limited := l.Allow(ip)
		w.Header().Set("X-RateLimit-Limit", intStr(l.limit))
		w.Header().Set("X-RateLimit-Remaining", intStr(remaining))
		if limited {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			http.Error(w, fmt.Sprintf("rate limit exceeded: %d requests per IP per day", l.limit), http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// MaybeMiddleware for endpoints that share budget but are not counted here: use Check then manual header injection.
func (l *Limiter) Check(ip string) (remaining int, limited bool) {
	return l.Allow(ip)
}

func intStr(n int) string {
	// fast path
	return strings.TrimSpace(strings.Join([]string{itoa(n)}, ""))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

func ClientIP(r *http.Request) string {
	// Check X-Forwarded-For, then X-Real-IP, then RemoteAddr
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if ip := strings.TrimSpace(parts[0]); ip != "" {
			if parsed := net.ParseIP(ip); parsed != nil {
				return parsed.String()
			}
			return ip
		}
	}
	if xrip := r.Header.Get("X-Real-IP"); xrip != "" {
		if parsed := net.ParseIP(strings.TrimSpace(xrip)); parsed != nil {
			return parsed.String()
		}
		return strings.TrimSpace(xrip)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	if parsed := net.ParseIP(host); parsed != nil {
		return parsed.String()
	}
	return host
}
