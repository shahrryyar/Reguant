# LEARN.md — A Newcomer's Tour of Reguant

> This is a **learning-oriented** guide for new contributors. It explains how
> Reguant actually works under the hood — the mental model, the request lifecycle,
> and where to poke when you want to change something. For operator-facing setup,
> read [README.md](README.md) and [CONTRIBUTING.md](CONTRIBUTING.md).

Reguant is a tiny, self-hosted GitOps PaaS written in Go. Its whole job is:
*clone a repo, build it, run it, route traffic to it, and repeat on every push.*
It deliberately stays under ~40MB of RAM, which drives many of the design choices
below (pure-Go SQLite, hand-rolled HTTP, lightweight systemd/Docker isolation).

---

## 1. The mental model (read this first)

There is **one Go binary** (the *control plane*) and **four categories of moving
parts** it orchestrates:

```
                ┌─────────────────────────────────────────────┐
   GitHub  ───► │  Reguant control plane  (cmd/reguant/main)   │
   Webhook      │                                              │
                │   internal/server  (HTTP + WebSocket API)    │
                │   internal/deployer (git/build/run)          │
   Browser ───► │   internal/db (SQLite state)                │
                │   internal/proxy (Nginx generator)           │
                └───────┬───────────┬───────────┬──────────────┘
                        │           │           │
                  SQLite .db     Nginx conf   runtime
                  (state)        (routing)    (Docker or systemd)
```

Key idea: **Reguant owns no business logic of your app.** It only stores
metadata about apps in SQLite, clones their code, builds/runs them, and writes
Nginx configs so the outside world can reach them. The "brain" is a thin state
machine over two tables (see §4).

---

## 2. Repository map — what lives where

| Path | Responsibility | Start reading here |
|------|----------------|--------------------|
| `cmd/reguant/main.go` | Process entry point: load config, `--restore` flag, open DB, start background schedulers, launch HTTP server. | `main()` |
| `internal/config/config.go` | Loads every `REGUANT_*` env var with a fallback. The single source of truth for configuration. | `Load()` |
| `internal/db/` | SQLite access. `db.go` opens the DB and creates the schema; `backup.go` replicates to S3/R2; `maintenance.go` prunes logs + vacuums; `s3sign.go` builds AWS SigV4 / R2 bearer auth. | `Init()`, `createSchema()` |
| `internal/deployer/` | The heart of the system: clone/pull, build, run, and tear down apps. `monitor.go` health-checks running apps. | `Deploy()`, `runDeploymentPipeline()` |
| `internal/proxy/nginx.go` | Writes per-app Nginx virtual-host files and reloads Nginx; invokes Certbot for TLS. | `ConfigureProxy()`, `writeProxyConfig()` |
| `internal/server/` | The HTTP/WebSocket API. `server.go` wires routes + middleware; `auth.go` token/OAuth; `terminal.go` a WebSocket shell; `cgroups.go` reads per-app resource usage. | `Start()`, `handleApps` |
| `dashboard/dist/` | Prebuilt single-page dashboard, served statically. The `REGUANT_API_TOKEN_PLACEHOLDER` string is replaced at runtime with the real token. | — (built asset) |
| `scripts/install.sh` | One-line VPS bootstrapper (Go, Docker, Nginx, Certbot, systemd unit). | — |

Dependency note: there are only **three direct dependencies** — `gorilla/websocket`
(real-time channels), `modernc.org/sqlite` (a *pure-Go*, CGO-free SQLite engine),
and `creack/pty` (the terminal WebSocket). Everything else (HTTP, S3 signing,
HMAC verification, `/proc` parsing) is hand-rolled with the standard library to
protect the RAM budget.

---

## 3. Startup sequence

`cmd/reguant/main.go` does things in a strict order:

1. `config.Load()` — read env vars.
2. Parse the `--restore` flag. If set, download the S3 backup, overwrite the
   local DB, and **exit** (disaster recovery; see README "Disasters Recovery").
3. `os.MkdirAll` for the apps and logs dirs.
4. `db.Init()` — open SQLite, apply performance pragmas, create schema.
5. Launch three background goroutines, all cancelled by a context on
   `SIGINT`/`SIGTERM`:
   * `db.StartBackupScheduler` — periodic S3/R2 replication (if configured).
   * `db.StartMaintenanceScheduler` — 24h log pruning + `VACUUM`.
   * `deployer.StartAppMonitorScheduler` — 60s HTTP health checks.
6. Register `/api/status` and call `server.Start()`.

`server.Start()` then:
* builds a `Server` holding the DB, config, a `Deployer`, and an `NginxManager`;
* creates a per-IP rate limiter (200 req/min);
* injects the API token into the dashboard HTML (token auth without a build step);
* starts `statsGathererLoop` (reads `/proc/stat`, `/proc/meminfo`) and
  `appStatsPollLoop` (per-app cgroup stats);
* registers all routes and wraps them in middleware:
  `securityHeaders → CORS → rate limit → auth → router`.

---

## 4. The data model (two tables, that's it)

Defined in `internal/db/db.go` → `createSchema()`:

* **`applications`** — one row per managed app. Important columns:
  `name` (unique, `^[a-zA-Z0-9-_]+$`), `git_repo`, `git_branch`, `build_type`
  (`'docker'` or `'systemd'`), `build_command`/`run_command` (systemd only),
  `port` (unique, the internal port Nginx forwards to), `domain`, `ssl_enabled`,
  `env_vars` (a JSON string), and `status` (`idle`/`deploying`/`running`/`failed`).
* **`deployments`** — one row per deploy attempt, with streaming `logs` text and
  `status` (`queued`/`building`/`success`/`failed`). Linked to `applications` by a
  foreign key with `ON DELETE CASCADE`, so deleting an app cleans up its history.

Almost all "state" in Reguant is just these two tables. When you're debugging,
open `reguant.db` and look here first.

---

## 5. Walkthrough: what happens on a deploy

This is the single most important flow to understand.

### 5a. Trigger
A deploy starts from one of three places, all funneling into
`deployer.Deploy(appID)`:
* Dashboard "Deploy" button → `POST /api/apps/deploy?app_id=...`
* Git push → `POST /api/webhooks/github`, which performs URL normalization (lowercase, strips `.git` suffix, and handles SSH-to-HTTPS format mappings) to match private repos cloned via SSH and public repos cloned via HTTPS against the `applications` table, then calls `Deploy` for each match.
* Saving environment variables → `PUT /api/apps/env?app_id=...` automatically triggers a redeploy.

### 5b. Queue + run
`Deploy()` writes a `deployments` row (`queued`), then runs
`runDeploymentPipeline()` **in a background goroutine** with a cancelable context
(so a new deploy cancels the old one). It appends log lines straight into the
`deployments.logs` column as subprocesses emit them, which is how the dashboard
streams output live over `WS /api/ws/logs`.

### 5c. Pipeline steps
1. **Fetch code** — first time: `git clone -b <branch> <repo> <appsDir>/<name>`.
   Subsequent times: `git fetch` + `git reset --hard origin/<branch>` (fast,
   no re-clone).
2. **Build/run**, dispatched by `build_type`:
   * **`systemd`** → `deploySystemd()`: run `build_command` natively, then write a
     unit file to `/etc/systemd/system/reguant-<id>.service` with sandboxing
     (`ProtectSystem=strict`, `NoNewPrivileges`, `PrivateTmp`…) and hard limits
     (`MemoryMax=250M`, `CPUQuota=50%`). Then `daemon-reload`, `enable`, `restart`.
   * **`docker`** → `deployDocker()`: `docker build` (BuildKit inline cache), start
     a **staging** container on a *temporary* port, TCP health-check it 10×, and
     only promote it if healthy.

### 5d. Zero-downtime swap (Docker) — the subtle part
A Docker container's published port is fixed at `docker run` time and can't be
changed by renaming. So Reguant's swap is: build on `tempPort`, health-check,
then `docker rename` the staging container to the active name and **repoint the
Nginx upstream to `tempPort`** (and persist that as the app's live `port`). The
old container is removed first. Net effect: traffic keeps flowing with no gap.

### 5e. Routing
Whenever an app has a `domain`, `internal/proxy/nginx.go` writes
`reguant-<name>.conf` into `REGUANT_NGINX_DIR` (`/etc/nginx/sites-enabled` by
default) and runs `nginx -t && nginx -s reload`. The config is a plain
`proxy_pass http://127.0.0.1:<port>` server block. TLS is enabled via the `POST /api/apps/ssl?app_id=...` endpoint which calls `EnableSSL(domain, email)` (loaded from `REGUANT_SSL_EMAIL` config) to execute `certbot --nginx -d <domain> -m <email> --agree-tos --non-interactive` and rewrite the Nginx virtual host file in place.

---

## 6. Key concepts worth internalizing

* **Port allocation** — `deployer.GetFreePort()` scans 10000–20000, skipping any
  port already in the `applications` table *or* physically bound. Apps never
  collide on a port.
* **Env injection** — app env vars are stored as JSON in `env_vars`. In Docker
  they become `-e KEY=VAL` flags; in systemd they become `Environment=` lines
  (plus `PORT=<port>`). Saving env vars triggers a redeploy so they take effect.
* **Auth** — if `REGUANT_API_TOKEN` is empty, the API/terminal/webhook are
  **unauthenticated** (the server logs a warning). With a token, every API call
  and WebSocket needs `Authorization: Bearer <token>`; GitHub OAuth just mints a
  session cookie equal to that token. Webhooks are HMAC-verified
  (`X-Hub-Signature-256`, constant-time compare) when `REGUANT_GITHUB_WEBHOOK_SECRET`
  is set.
* **Webhook body cap** — the GitHub webhook reader is bounded to 5 MiB
  (`http.MaxBytesReader`) so a huge POST can't blow the RAM budget.
* **Real-time** — three WebSocket endpoints stream from in-memory caches / the DB:
  `/api/ws/logs` (deploy output), `/api/ws/stats` (host CPU/mem), and
  `/api/ws/terminal` (a remote shell into the host).
* **Backups** — `db/backup.go` uses SQLite `VACUUM INTO` to make a transactionally
  consistent copy, then uploads it to S3/R2 (SigV4 or R2 bearer token). Restore is
  `reguant --restore`.

---

## 7. Local development

From [CONTRIBUTING.md](CONTRIBUTING.md), the tl;dr:

```bash
# Requires Go 1.25+ and (optionally) Docker.
git clone https://github.com/shahrryyar/reguant.git
cd reguant

# Run the control plane locally without touching system dirs:
REGUANT_PORT=9000 \
REGUANT_DB_PATH=./reguant.db \
REGUANT_APPS_DIR=./apps \
REGUANT_LOGS_DIR=./logs \
REGUANT_NGINX_DIR=./nginx \
go run cmd/reguant/main.go
```

Open `http://localhost:9000`. Before committing:

```bash
go fmt ./...      # CI fails if gofmt finds changes
go test ./...     # CI runs `go test -v ./...`
```

CI (`.github/workflows/ci.yml`) checks formatting, runs tests, and cross-builds
Linux `amd64` + `arm64` binaries. **Systemd/Docker deploys and Nginx reloads need
root and those binaries**, so locally you can exercise the API and DB logic, but
full deploys are best tested on a VPS.

---

## 8. How to extend Reguant (where to start)

* **Add a REST endpoint** → register it in `server.Start()`'s `mux` and write a
  `handleXxx` method on `Server` in `internal/server/server.go`. Remember the
  middleware chain already handles auth/CORS/rate-limiting/security headers.
* **Add a deploy backend** (e.g. a third runtime) → add a branch in
  `runDeploymentPipeline()` next to `docker`/`systemd` and a `deployXxx()` method
  in `internal/deployer/deployer.go`. Reuse `runCmd()` so logs stream to the DB.
* **Change routing/proxy behavior** → `internal/proxy/nginx.go`. Edit
  `writeProxyConfig()`'s template, then test with `internal/proxy/nginx_test.go`
  (it asserts on rendered text without needing an nginx binary).
* **Change schema/state** → `createSchema()` in `internal/db/db.go`, plus the
  `Application` struct in `deployer` and the `SELECT`/`INSERT` column lists in
  `handleApps`. Keep the `applications`/`deployments` column lists in sync
  (they're duplicated by hand — easy to miss one).
* **Add config** → add a field to `config.Config` and a `getEnv` line in
  `internal/config/config.go`, then document it in README's env-var table.

---

## 9. Gotchas for newcomers

* **Two SQLite drivers appear** — `modernc.org/sqlite` is imported as the `"sqlite"`
  driver (CGO-free). Don't switch to `mattn/go-sqlite3`; it would break the
  cross-compilation and RAM goals.
* **Column lists are hand-maintained** — the `applications` SELECT in
  `handleApps` and `runDeploymentPipeline` must list columns in the *same order*
  as the `Scan` calls. A mismatch compiles fine but scrambles data at runtime.
* **Systemd deploys need root** — `os.WriteFile` to `/etc/systemd/system` and
  `systemctl` will fail as a normal user. The bundled unit runs as root; the
  unprivileged `reguant-apps` user only runs the *app process* itself.
* **Git SSH for private repos** — clones use `GIT_SSH_COMMAND` with
  `StrictHostKeyChecking=accept-new` and `BatchMode=yes`, and run as the daemon's
  user. Put the deploy key at that user's `~/.ssh/id_ed25519` (root for the
  bundled service).
* **No HTTPS rewrite in code** — the Nginx template only listens on `:80`. TLS is
  added out-of-band by Certbot via `EnableSSL()`. Expect plaintext routing until
  you call that.
* **Logs are append-only text** — `deployments.logs` grows with each deploy; the
  maintenance scheduler prunes it, but during a long session it's just one big
  string streamed by byte-offset over the WebSocket.

---

## 10. Suggested reading order for your first day

1. `README.md` (what it is) → this file (how it works).
2. `cmd/reguant/main.go` (entry + schedulers).
3. `internal/server/server.go` `Start()` + `handleApps` (the API surface).
4. `internal/deployer/deployer.go` `Deploy()` → `runDeploymentPipeline()` (the core).
5. `internal/proxy/nginx.go` (how traffic reaches the app).
6. `internal/db/db.go` `createSchema()` (the state).

That path takes you from "process starts" to "app is live on a domain" — the full
loop — in the fewest files.
