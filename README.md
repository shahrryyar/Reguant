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

---

## ☁️ Offsite Database Backups (S3/R2)

Reguant features a lightweight database replicator. It continuously backs up the SQLite database to S3-compatible cloud storage (such as Cloudflare R2, AWS S3, or Backblaze B2).

### 1. Replicator Configuration
To activate background database replication, set these environment variables when starting the Reguant daemon:

```bash
export REGUANT_S3_ENDPOINT="https://<account-id>.r2.cloudflarestorage.com"
export REGUANT_S3_BUCKET="my-backup-bucket"
export REGUANT_S3_ACCESS_KEY="my-access-key-id"
export REGUANT_S3_SECRET_KEY="my-secret-access-key"
export REGUANT_S3_INTERVAL_MINUTES="30"  # Frequency of database replication
```

When these keys are active, Reguant executes SQLite `VACUUM INTO` to create a transactionally consistent copy of the database, then uploads it to the S3 bucket.

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
4. Choose **Just the push event** and save.

When code is pushed, GitHub sends a POST request. Reguant parses the repository URL and branch, checks for a match in the local SQLite database, and automatically builds/deploys the updates with zero downtime.

---

## ⚙️ Application Environment Variables
The **Env Vars** tab on each application card in the dashboard is for managing the environment variables *of your deployed applications* (such as database credentials, API keys, etc.), **not** for the Reguant control plane itself. 

When you save variables, they are stored securely in SQLite and dynamically injected as:
- Docker `-e` run parameters in **Docker Mode**.
- `Environment=` strings inside systemd service files in **Systemd Mode**.

Saving environment variables automatically triggers a staging build to redeploy the app with its updated configuration.

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
