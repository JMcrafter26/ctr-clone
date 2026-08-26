package prober

import (
	"context"
	"log/slog"
	"net"
	"strings"
	"time"

	"ctindex/internal/pipeline"
	"ctindex/internal/store"
)

// common hostnames to probe — top ~300 from SecLists + crt.name style
var commonNames = []string{
	"www", "mail", "api", "blog", "shop", "vpn", "cdn", "dev", "staging", "test",
	"admin", "secure", "login", "assets", "m", "mobile", "app", "portal", "support",
	"help", "docs", "developer", "community", "status", "manage", "beta", "demo",
	"stage", "prod", "api2", "api3", "v1", "v2", "ns1", "ns2", "ftp", "smtp", "pop",
	"imap", "webmail", "exchange", "autodiscover", "owa", "vpn2", "remote", "git",
	"github", "gitlab", "jira", "confluence", "jenkins", "ci", "cdn2", "static",
	"img", "images", "media", "video", "stream", "chat", "forum", "news", "store",
	"pay", "payment", "billing", "account", "accounts", "auth", "oauth", "sso",
	"ldap", "ad", "dc", "internal", "intranet", "extranet", "partner", "partners",
	"client", "clients", "customer", "customers", "partner", "api-dev", "api-stage",
	"admin2", "test2", "qa", "uat", "preprod", "sandbox", "preview", "new", "old",
	"legacy", "v3", "v4", "email", "mx", "mx1", "mx2", "gw", "proxy", "lb", "cache",
	"db", "mysql", "postgres", "redis", "mongo", "es", "kibana", "grafana", "prometheus",
	"monitor", "monitoring", "metrics", "logs", "sentry", "analytics", "tracking",
	"cdn1", "edge", "origin", "files", "download", "downloads", "upload", "uploads",
	"assets1", "static1", "img1", "video1", "api1", "rest", "graphql", "websocket", "ws",
	"realtime", "live", "events", "hooks", "webhooks", "notify", "push", "pubsub",
	"queue", "worker", "jobs", "task", "cron", "scheduler", "search", "elastic",
	"solr", "db1", "db2", "replica", "master", "slave", "backup", "storage", "s3",
	"bucket", "vault", "secret", "secrets", "config", "cfg", "admin-api", "manage-api",
	"internal-api", "private", "priv", "secure-api", "gateway", "edge1", "www2",
	"www1", "m2", "m1", "mobile2", "app2", "beta2", "alpha", "gamma", "delta",
	"prod1", "prod2", "dev1", "dev2", "stage1", "stage2", "test1", "qa1", "uat1",
	"smtp1", "mail1", "mail2", "owa1", "exchange1", "autodiscover1", "ns3", "ns4",
	"dns", "dns1", "dns2", "resolver", "ntp", "time", "vpn1", "vpn3", "remote1",
	"access", "connect", "portal2", "login2", "auth2", "sso2", "idp", "id", "identity",
	"user", "users", "profile", "profiles", "dashboard", "dash", "panel", "console",
	"control", "manage2", "admin3", "super", "root", "master1", "agent", "agents",
	"node", "nodes", "cluster", "k8s", "kubernetes", "docker", "registry", "repo",
	"artifacts", "artifactory", "nexus", "sonar", "vault1", "consul", "etcd", "zookeeper",
	"kafka", "rabbitmq", "activemq", "celery", "airflow", "superset", "metabase",
	"tableau", "powerbi", "looker", "grafana1", "kibana1", "prometheus1", "alertmanager",
	"pagerduty", "opsgenie", "status1", "health", "healthz", "ready", "livez",
}

type Prober struct {
	store    *store.Store
	pipeline *pipeline.Pipeline
	interval time.Duration
	workers  int
}

func New(s *store.Store, p *pipeline.Pipeline) *Prober {
	return &Prober{
		store:    s,
		pipeline: p,
		interval: 30 * time.Second,
		workers:  20,
	}
}

func (pr *Prober) Run(ctx context.Context) error {
	slog.Info("dns prober starting", "interval", pr.interval, "workers", pr.workers, "names", len(commonNames))
	ticker := time.NewTicker(pr.interval)
	defer ticker.Stop()
	// run once at start after short delay
	go func() {
		time.Sleep(5 * time.Second)
		pr.probeCycle(ctx)
	}()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			pr.probeCycle(ctx)
		}
	}
}

func (pr *Prober) probeCycle(ctx context.Context) {
	// get top apexes to probe — most promising to have many subs
	apexes, err := pr.store.Top(ctx, 50)
	if err != nil {
		slog.Error("prober top failed", "err", err)
		return
	}
	if len(apexes) == 0 {
		// fallback: use seeded apexes
		apexes = []struct {
			Apex  string
			Count int64
		}{{Apex: "example.com"}, {Apex: "google.com"}, {Apex: "github.com"}}
	}
	slog.Info("prober cycle", "apexes", len(apexes))
	// for each apex, try batch of common names with worker pool
	type job struct{ apex, sub string }
	jobs := make(chan job, 1024)
	// start workers
	workerCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	for i := 0; i < pr.workers; i++ {
		go func() {
			for j := range jobs {
				select {
				case <-workerCtx.Done():
					return
				default:
				}
				fqdn := j.sub + "." + j.apex
				// quick DNS lookup with timeout
				resolverCtx, cancel2 := context.WithTimeout(workerCtx, 2*time.Second)
				_, err := net.DefaultResolver.LookupHost(resolverCtx, fqdn)
				cancel2()
				if err != nil {
					// NXDOMAIN or timeout = not found, skip
					if strings.Contains(err.Error(), "no such host") {
						continue
					}
					continue
				}
				// found — submit
				pr.pipeline.Submit(pipeline.Entry{
					DNS:       []string{fqdn},
					Timestamp: time.Now().UnixMilli(),
				})
			}
		}()
	}
	// enqueue: for each apex try first 50 names per cycle to be polite (50 apex *50=2500 lookups/cycle)
	perApex := 50
	if len(apexes)*perApex > 3000 {
		perApex = 3000 / len(apexes)
		if perApex < 10 {
			perApex = 10
		}
	}
	for _, a := range apexes {
		for i := 0; i < perApex && i < len(commonNames); i++ {
			select {
			case jobs <- job{apex: a.Apex, sub: commonNames[i]}:
			case <-workerCtx.Done():
				close(jobs)
				return
			}
		}
	}
	close(jobs)
	// wait for workers to drain (timeout already)
	time.Sleep(2 * time.Second)
}
