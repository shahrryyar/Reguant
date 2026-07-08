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

## Directory Structure

```text
reguant/
├── cmd/
│   └── reguant/
│       └── main.go         # Application entry point
├── internal/
│   ├── config/             # Config management & env variables
│   ├── db/                 # SQLite connection and migration queries
│   ├── deployer/           # Git runner, Docker client, and systemd orchestrator
│   ├── proxy/              # Nginx template writers & Certbot runners
│   └── server/             # HTTP router, WebSockets handler, and middleware
├── dashboard/              # Next.js / Vite Static UI (Dashboard)
├── scripts/
│   └── install.sh          # Single-line VPS installer script
├── go.mod                  # Go module definition
└── README.md
```

---

## License

Licensed under the MIT License. See [LICENSE](LICENSE) for details.
