# Contributing to Reguant

Thank you for your interest in contributing to Reguant! We welcome community contributions, bug reports, and optimizations to help make self-hosted PaaS deployments lighter and faster.

---

## 🛠️ Local Development Setup

To modify and test Reguant locally, you will need a Go environment installed on your machine.

### 1. Requirements
- **Go**: Version `1.25` or higher (Required for SQLite drivers).
- **Docker**: For testing application deployments inside containers.
- **Git**: For pulling repository files.

### 2. Clones & Setup
```bash
git clone https://github.com/shahrryyar/Reguant.git
cd Reguant
```

### 3. Run unit tests
To run SQLite schema creation and validation tests, execute:
```bash
go test -v ./...
```

### 4. Running the Daemon Locally
To run the server control plane locally for development (avoiding writing to system-level root directories like `/var/lib/reguant`):
```bash
REGUANT_PORT=9000 \
REGUANT_DB_PATH=./reguant.db \
REGUANT_APPS_DIR=./apps \
REGUANT_LOGS_DIR=./logs \
REGUANT_NGINX_DIR=./nginx \
go run cmd/reguant/main.go
```
Open `http://localhost:9000` in your web browser to test modifications inside the glassmorphic Dashboard interface.

---

## 🎨 Coding standards & Guidelines

### 1. Formatting
Reguant strictly enforces standard Go formatting rules. Before staging your commits, run `go fmt` to check and format the files:
```bash
go fmt ./...
```
Any PR failing formatting checks in GitHub Actions will be blocked.

### 2. Code Safety & Resource Limits
- **Memory Optimization**: Avoid importing heavy, bloated packages (such as massive AWS/Cloud SDKs) that push idle memory usage past our 40MB limit. Use lightweight native std library HTTP streams when possible.
- **Safety checks**: Avoid raw file copies for databases; use SQLite's native `VACUUM INTO` command for safe replicas.
- **WebSocket routines**: Ensure any upgraded socket loops have active control frame readers to close sockets cleanly and prevent goroutine leaks.

---

## 📥 Pull Request Workflow

`main` is a **protected branch**: collaborators cannot push to it directly and must
contribute through pull requests. Every PR requires at least one approving review and a
passing CI run before it can be merged.

1. Fork the repository on GitHub (or push a branch to this repo if you are a collaborator).
2. Create a clean feature branch off `main`: `git checkout -b feat/my-new-feature`
3. Commit your changes with descriptive messages: `git commit -m "feat: my change"`
4. Run formatting checks: `go fmt ./...` (CI blocks PRs that fail `gofmt`)
5. Run the test suite: `go test ./...`
6. Push your branch: `git push -u origin feat/my-new-feature`
7. Open a Pull Request against `main` and fill in the PR template. Request a review from a maintainer.

> Tip: keep PRs small and focused. Link any related issue with `Closes #123` so it closes
> automatically on merge.

---

## 💬 Questions & Discussions

For general questions, ideas, and community discussion, use **GitHub Discussions**
(not Issues). Open an **Issue** only for reproducible bugs or concrete feature requests.
Discussions give collaborators a place to ask and share before opening a pull request.
