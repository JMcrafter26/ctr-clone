# crt.name clone

Passive CT subdomain index — SvelteKit frontend + Go backend (SQLite). Give it `example.com`, get every subdomain + first-seen.

Original: https://crt.name

## What you get

- `GET /v1/search?apex=example.com` → one per line, `?format=json` → `[{"sub":"..."}]`, `&dates=1` → `sub<TAB>RFC3339` / `{"first_seen", "sub"}`, error strings byte-match upstream.
- `GET /v1/stats` → `{subdomains, recent[16]}`, `POST /mcp` (search tool), `GET /v1/find|top|stream` (gated by token).
- Frontend at `http://localhost:5173` — search, live counter, 8-row firehose ticker, docs.

## Prereqs

- Go 1.26+, Node 24+, pnpm 11+

## Run locally (2 terminals)

**Terminal 1 — backend API on :8080**
```bash
cd backend
go mod tidy
# demo mode (seeded 86 rows, no CT ingest, instant)
ENABLE_INGEST=0 go run ./cmd/indexd
# curl http://localhost:8080/v1/stats
# curl "http://localhost:8080/v1/search?apex=example.com&format=json&dates=1"
```

**Terminal 2 — frontend on :5173** (proxies `/v1`, `/mcp` to `:8080` via `frontend/vite.config.ts:22`)
```bash
cd frontend
pnpm install
pnpm run dev
# open http://localhost:5173
```

Stop both with `Ctrl-C`. DB file is `backend/ctindex.db` (or `DB_PATH` you set) — delete it to re-seed.

## Run with live CT ingest

Needs `CONTACT_EMAIL` for User-Agent policy (Skylight rate-limits anon `75 req/s`, identified gets `512 req/s`).

```bash
cd backend
CONTACT_EMAIL=you@example.com go run ./cmd/indexd
# or with options
ADDR=:8080 DB_PATH=/data/ctindex.db CONTACT_EMAIL=you@example.com go run ./cmd/indexd
```

Check progress:
```bash
curl -s http://localhost:8080/v1/stats | jq
sqlite3 ctindex.db "SELECT count(*) FROM names; SELECT * FROM log_state LIMIT 5;"
go run ./cmd/ctl --cmd stats --db ctindex.db
# logs: INFO fetching entries log=tuscolo2026h2 from=0 to=8192
```

If you see `pipeline channel full` (`backend/internal/pipeline/pipeline.go:42`) it means followers burst faster than SQLite batch (`500ms`/`2000` rows). Safe to ignore during catch-up, or change `Submit()` to blocking in that file.

## CT ingest vs demo

- **Demo (`ENABLE_INGEST=0`)** — no network, boots instantly, `store.IsEmpty` `backend/cmd/indexd/main.go:37` seeds `86` rows (`example.com`, `namecheap.com` etc + `demo*.com` with old timestamps `main.go:219` so live outranks them). Good for UI dev.
- **CT ingest (`ENABLE_INGEST=1`, default)** — `staticct` followers `backend/internal/follower/staticct/follower.go:49` tail `backend/logs.yaml:5` shards (`sycamore`, `tuscolo`) via `sunlight.UnauthenticatedTrimmedEntries` names tiles + `log_state` resume `store.go:275`. `rfc6962` follower `follower/rfc6962/follower.go:36` optional via `ENABLE_RFC6962=1`. They `pipeline.Submit` `pipeline.go:37` → `loop()` batch `store.go:131`. `recent` then is live `2026-*` first_seen, counter grows (you saw `239983→389672`).

If you run demo then switch to ingest, `recent` will stay demo until new live certs arrive — fixed in this commit (demo timestamps now `now-2y`).

## Deploy

Pick one:

**A) Single binary + static frontend (simplest, no deps)**
```bash
# backend
cd backend && go build -o /tmp/indexd ./cmd/indexd

# frontend static build
cd frontend
pnpm install
VITE_API_URL=https://api.yourdomain.com pnpm run build
# output is .svelte-kit/output — serve via Caddy/Nginx:
# or add @sveltejs/adapter-static and copy build/ to your web root

# run backend as service
ADDR=:8080 DB_PATH=/var/lib/ctindex/ctindex.db CONTACT_EMAIL=you@example.com CLONE_TOKEN=secret /tmp/indexd
# Caddy example:
# api.yourdomain.com reverse_proxy localhost:8080
# yourdomain.com root * /var/www/frontend/build
```

**B) Docker (backend + frontend together)**
```dockerfile
# backend stage
FROM golang:1.26 AS go
WORKDIR /app/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN go build -o /indexd ./cmd/indexd

# frontend stage
FROM node:24 AS node
WORKDIR /app/frontend
COPY frontend/package.json frontend/pnpm-lock.yaml ./
RUN npm i -g pnpm && pnpm install
COPY frontend/ ./
RUN pnpm run build

FROM debian:bookworm-slim
COPY --from=go /indexd /usr/local/bin/indexd
COPY --from=node /app/frontend/build /var/www
# serve /var/www + proxy /v1 to :8080 via Caddy (or run indexd + caddy)
```

**C) systemd**
```ini
# /etc/systemd/system/ctindex.service
[Service]
ExecStart=/usr/local/bin/indexd
Environment=ADDR=:8080 DB_PATH=/var/lib/ctindex/ctindex.db CONTACT_EMAIL=you@example.com
Restart=always
WorkingDirectory=/opt/ctr-clone/backend
```

Env vars: `ADDR` (`:8080`), `DB_PATH` (`ctindex.db`), `LOGS_YAML` (`logs.yaml` — curated shards, fallback to `backend/logs.yaml`), `CONTACT_EMAIL`, `CLONE_TOKEN` (if set, gates `find/top/stream` to `Bearer`), `RATE_LIMIT` (`100`), `ENABLE_INGEST` (`1`/`0`), `ENABLE_RFC6962` (`1` to also tail Argon/Nimbus).

## Troubleshooting

- `recent` still shows `demo*.com` — ingest not running. Check terminal 1: you used `ENABLE_INGEST=0` or `logs.yaml not found`. Run with `CONTACT_EMAIL` and ensure `backend/logs.yaml` exists.
- `rate limit exceeded` — `100 req/IP/day` on `/v1/search` + `/mcp` (`backend/internal/ratelimit/limiter.go:1`), resets UTC midnight.
- Port in use — change `ADDR=:18080` and `frontend/vite.config.ts:22` proxy target.

## Layout

```
backend/cmd/indexd/main.go      daemon
backend/internal/store/store.go  SQLite names/log_state
backend/internal/pipeline/pipeline.go chan→batch
frontend/src/routes/+page.svelte UI
frontend/src/routes/layout.css   zinc theme
```
