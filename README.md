<p align="center">
  <img src="logo.jpg" alt="Reguant Logo" width="180" height="180" style="border-radius: 24px; box-shadow: 0 10px 30px rgba(0,0,0,0.5);" />
</p>

# Reguant: Self-Hosted GitOps PaaS

Reguant is an ultra-lightweight, self-hosted Platform-as-a-Service (PaaS) built in Go, SQLite, and Nginx. It is designed to deploy and manage applications with a total idle footprint **under 40MB of RAM**, making it significantly faster, lighter, and smoother than heavy alternatives like Coolify.

---

## Technical Specifications & Architecture

### RAM Budget (Idle State)
- **Go Backend Control Plane**: ~15MB - 20MB
- **SQLite (In-Process)**: ~2MB - 5MB
- **Nginx Reverse Proxy**: ~5MB - 8MB
- **Dashboard UI**: 0MB (Served statically, runs in client browser)
- **Total Idle Footprint**: **~25MB - 35MB RAM**

### Core Design Choices
1. **Language**: Go (Golang) - Compiled, garbage-collected system language with low memory usage and high concurrency support.
2. **Database**: SQLite (`modernc.org/sqlite`) - A pure Go database engine, enabling zero-configuration deployment and simple cross-compilation without CGO dependencies.
3. **Proxy & SSL**: Nginx with automatic Certbot certificate issuance.
4. **Deployments**: Hybrid support.
   - **Docker Mode**: Containers for application isolation.
   - **Systemd Mode**: Run apps as native background systemd processes with zero container overhead (ideal for extremely low-RAM machines).
5. **Real-time Engine**: WebSockets for streaming terminal outputs, deployment logs, and server stats dynamically.

---

## 🚀 Environment Variables Config

Reguant uses standard environment variables for configuration. You can supply these inside systemd service files or define them in your environment shell:

| Environment Variable | Description | Default |
|----------------------|-------------|---------|
| `REGUANT_PORT` | The port the control plane listens on | `9000` |
| `REGUANT_DB_PATH` | Path to store the SQLite database file | `/var/lib/reguant/reguant.db` |
| `REGUANT_APPS_DIR` | Path to clone application source codes | `/var/lib/reguant/apps` |
| `REGUANT_LOGS_DIR` | Path to store build logs | `/var/lib/reguant/logs` |
| `REGUANT_NGINX_DIR` | Path to configure Nginx configurations | `/etc/nginx/sites-enabled` |
| `REGUANT_API_TOKEN` | **Required for secure deployments.** Shared secret for the API and WebSockets (Bearer token / session cookie) | _(empty = unauthenticated)_ |
| `REGUANT_CORS_ORIGIN` | Allowed CORS origin for the API (e.g. `https://dash.example.com`) | _(empty = same-origin when a token is set)_ |
| `REGUANT_GITHUB_OAUTH_CLIENT_ID` | GitHub OAuth app client ID (enables dashboard "Sign in") | _(empty = disabled)_ |
| `REGUANT_GITHUB_OAUTH_CLIENT_SECRET` | GitHub OAuth app client secret | _(empty = disabled)_ |

---

## ☁️ Offsite Database Backups (S3/R2)

Reguant features a lightweight database replicator. It continuously backs up the SQLite database to S3-compatible cloud storage (such as Cloudflare R2, AWS S3, or Backblaze B2).

### 1. Replicator Configuration
To activate background database replication, set these environment variables when starting the Reguant daemon:

```bash
export REGUANT_S3_ENDPOINT="https://<account-id>.r2.cloudflarestorage.com"
export REGUANT_S3_BUCKET="my-backup-bucket"
# Authentication (pick ONE):
#  - Cloudflare R2 API token (recommended for R2):
export REGUANT_S3_TOKEN="your-r2-api-token"
#  - OR AWS Signature V4 access/secret keys (AWS S3, R2 S3 API, Backblaze B2):
export REGUANT_S3_ACCESS_KEY="my-access-key-id"
export REGUANT_S3_SECRET_KEY="my-secret-access-key"
export REGUANT_S3_REGION="auto"          # R2 uses "auto"; AWS/B2 use e.g. us-east-1
export REGUANT_S3_INTERVAL_MINUTES="30"  # Frequency of database replication
```

When the endpoint and bucket are set, Reguant executes SQLite `VACUUM INTO` to create a transactionally consistent copy of the database, then uploads it to the S3 bucket. Uploads use **AWS Signature Version 4** (when access/secret keys are provided) or an **R2 API-token Bearer** header (when `REGUANT_S3_TOKEN` is set). Buckets are addressed in virtual-hosted style (`https://<bucket>.<host>/<key>`), which is what AWS S3 requires. On restore, the downloaded file is integrity-checked before it replaces the live database.

### 2. Disasters Recovery Restoration
If your VPS fails, you can spin up a replacement host and instantly restore your entire configuration by booting Reguant with the `--restore` flag:

```bash
reguant --restore
```

This downloads the backup copy from your S3 bucket, overwrites the local SQLite database file, and shuts down, leaving the database ready for the daemon launch.

---

## 🛠️ GitHub Integration & GitOps Webhooks

Reguant is built for automated git-push deployments.

### 1. Git Authentication (Public vs. Private Repositories)
- **Public Repositories**: No authentication parameters or tokens are required. Simply register the repository's HTTPS clone link (e.g. `https://github.com/user/repo`).
- **Private Repositories**: To keep access tokens out of the codebase, Reguant authenticates with native SSH deploy keys — **no GitHub token or GitHub App is required**.
  1. Generate an SSH keypair on the host: `ssh-keygen -t ed25519` (leave the passphrase empty so automated deploys never block on a prompt).
  2. Copy the public key: `cat ~/.ssh/id_ed25519.pub`.
  3. On GitHub, navigate to your repository -> **Settings** -> **Deploy Keys** -> **Add deploy key** and paste the key with **read-only** access.
  4. Register the repository using its SSH clone link (e.g. `git@github.com:user/repo.git`) inside the Reguant dashboard.

  **Key ownership matters:** Git runs as the same OS user that runs the Reguant daemon. The bundled systemd unit runs as **root**, so the key must live at `/root/.ssh/id_ed25519` (or run the service as your chosen user and place the key there). Reguant auto-accepts GitHub's SSH host key on first contact (`StrictHostKeyChecking=accept-new`), so you do **not** need to pre-seed `known_hosts` manually.

### 2. Automated GitOps Webhooks
To trigger deployments automatically when pushing code:
1. Go to your GitHub repository -> **Settings** -> **Webhooks** -> **Add webhook**.
2. Set the **Payload URL** to:
   ```text
   http://<YOUR_VPS_IP>:<REGUANT_PORT>/api/webhooks/github
   ```
3. Set the **Content type** to `application/json`.
4. **Recommended:** set a **Secret** (any random string). Export it on the Reguant host so deliveries are verified:
   ```bash
   export REGUANT_GITHUB_WEBHOOK_SECRET="your-webhook-secret"
   ```
   With this set, Reguant rejects any webhook whose `X-Hub-Signature-256` HMAC does not match (HTTP 401). If unset, the server logs a warning and still accepts unauthenticated webhooks — never run that way on an internet-exposed host.
5. Choose **Just the push event** and save.

When code is pushed, GitHub sends a POST request. Reguant verifies the signature (if a secret is configured), parses the repository URL and branch, checks for a match in the local SQLite database, and automatically builds/deploys the updates with zero downtime.

> **🔒 Security:** The Reguant API, terminal websocket, and webhook endpoint are protected by built-in authentication — see [Security & Authentication](#-security--authentication). At minimum set `REGUANT_API_TOKEN` and `REGUANT_GITHUB_WEBHOOK_SECRET`. Even with auth enabled, always run behind TLS (a reverse proxy such as Caddy/Nginx) and never expose the daemon directly to the public internet.

---

## 🔐 Security & Authentication

Reguant now ships with built-in authentication for its control plane. **It is off by default for backward compatibility, but you MUST enable it on any exposed deployment.**

### API Token (recommended)
Set a shared secret; every API call and WebSocket (terminal, logs, stats) then requires it. The bundled dashboard reads the token automatically, so no UI change is needed.

```bash
export REGUANT_API_TOKEN="$(openssl rand -hex 32)"   # any long random string
```

Clients authenticate with `Authorization: Bearer <token>`, or — for browser sessions — by logging in via GitHub OAuth (below), which sets an `httpOnly` session cookie. The terminal WebSocket (a remote shell into your app) is gated by this token, so it is no longer an open RCE surface.

### GitHub OAuth Login (optional, browser-friendly)
Instead of copy-pasting a token, operators can sign in with GitHub. Create an OAuth app, then:

```bash
export REGUANT_GITHUB_OAUTH_CLIENT_ID="your-github-oauth-client-id"
export REGUANT_GITHUB_OAUTH_CLIENT_SECRET="your-github-oauth-secret"
# Callback URL to register in the GitHub app:
#   https://<YOUR_DOMAIN>/api/auth/github/callback
```

Visiting **Sign in** in the dashboard redirects to GitHub; on success a session cookie is issued. Use `/api/auth/logout` to clear it. (`REGUANT_API_TOKEN` must still be set — OAuth is just a convenient way to obtain the session cookie that equals it.)

### Webhook HMAC
As above, set `REGUANT_GITHUB_WEBHOOK_SECRET` so push webhooks are HMAC-verified (`X-Hub-Signature-256`).

### Hardening extras (applied automatically)
- **CORS** defaults to same-origin when a token is set; restrict further with `REGUANT_CORS_ORIGIN=https://your-domain`.
- **Security headers**: baseline CSP, `X-Frame-Options`, `X-Content-Type-Options`, `Referrer-Policy`.
- **Rate limiting** on all `/api/*` endpoints (per-IP, dependency-free).
- **Webhook body capped** at 5 MiB to bound memory.

> Run the daemon behind TLS. The token/OAuth cookie is only as safe as the transport — terminate HTTPS at a reverse proxy and bind Reguant to localhost.

---

## ⚙️ Application Environment Variables
The **Env Vars** tab on each application card in the dashboard is for managing the environment variables *of your deployed applications* (such as database credentials, API keys, etc.), **not** for the Reguant control plane itself. 

When you save variables, they are stored securely in SQLite and dynamically injected as:
- Docker `-e` run parameters in **Docker Mode**.
- `Environment=` strings inside systemd service files in **Systemd Mode**.

Saving environment variables automatically triggers a build to redeploy the application with the updated configuration.

---

## 🔒 SSL/TLS Configurations (HTTPS)
Reguant provides native, automatic HTTPS via Certbot and Let's Encrypt.
- **SSL API Endpoint**: `POST /api/apps/ssl?app_id=<app_id>`
  - Body: `{"ssl": true}` or `{"ssl": false}`
  - Wires Certbot to request a certificate using the configured `REGUANT_SSL_EMAIL` and updates the Nginx virtual host block in-place.
- **SSL Email**: Configured via `REGUANT_SSL_EMAIL` during installation or service config. Used by Let's Encrypt for registration and renewal warning notifications.

---

## 🖥️ Directory Structure
```text
reguant/
├── .github/
│   └── workflows/
│       └── ci.yml          # GitHub Actions CI/CD pipeline configuration
├── cmd/
│   └── reguant/
│       └── main.go         # Application entry point & CLI parser
├── internal/
│   ├── config/             # Config management & env variables loader
│   ├── db/                 # SQLite migration queries, maintenance & backups
│   ├── deployer/           # Git clone, Docker client, and systemd runner
│   ├── proxy/              # Nginx reverse proxy generators & Certbot SSL
│   └── server/             # WebSocket handlers, REST APIs, and file servers
├── dashboard/
│   └── dist/               # SPA dashboard files (served statically)
├── scripts/
│   └── install.sh          # Single-line VPS installer script
├── go.mod                  # Go module definition
├── go.sum                  # Cryptographic dependency locks
└── README.md
```

---

## 🛠️ Installation

### 1. Single-Line Installer
Log in to your VPS as root and execute the installer script:
```bash
curl -fsSL https://raw.githubusercontent.com/shahrryyar/Reguant/main/scripts/install.sh | bash
```
This automatically configures Go, Docker, Nginx, Certbot, creates the unprivileged `reguant-apps` runtime user, compiles the binary, and starts the systemd service.

### 2. Manual Startup (Development)
You can run the control plane locally without root permissions:
```bash
REGUANT_PORT=9000 REGUANT_DB_PATH=./reguant.db REGUANT_APPS_DIR=./apps REGUANT_LOGS_DIR=./logs REGUANT_NGINX_DIR=./nginx go run cmd/reguant/main.go
```
Open your browser and navigate to `http://localhost:9000` to view the dashboard.

---

## License
Licensed under the MIT License. See [LICENSE](LICENSE) for details.
