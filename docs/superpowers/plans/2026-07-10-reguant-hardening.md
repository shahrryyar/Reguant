# Reguant Hardening & Feature-Completion Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** Close the critical security holes, fix the correctness/lifecycle bugs, and add the missing operational features (app lifecycle control, deployment history, rollback, schema migrations, CI-status gating) across the whole Reguant PaaS, without breaking its <40MB RAM budget or dependency-light design.

**Architecture:** Reguant is a single Go binary (`cmd/reguant`) with five internal packages: `config` (env loader), `db` (SQLite + S3 backup), `deployer` (git clone → docker/systemd build → nginx swap), `proxy` (nginx vhost + certbot), and `server` (REST + WebSocket + auth/oauth). We keep that structure and every change stays dependency-free (stdlib + the already-present `gorilla/websocket`, `creack/pty`, `modernc.org/sqlite`).

**Tech Stack:** Go 1.25, SQLite (modernc.org/sqlite, CGO-free), Nginx, Certbot, systemd, Docker, GitHub OAuth + webhooks.

## Global Constraints

- **No new third-party dependencies.** stdlib first; only `gorilla/websocket`, `creack/pty`, `modernc.org/sqlite` are allowed (already in `go.mod`). Copy verbatim into every task's mental checklist.
- **RAM budget:** idle footprint stays ~25–35MB. No in-memory buffering of full build logs, no unbounded caches.
- **Backward compatible:** open mode (no `REGUANT_API_TOKEN`) must keep working; every new env var defaults to empty/off.
- **Every change keeps `gofmt -l .` clean and `go vet ./...` passing** — CI enforces both.
- **Module path:** `github.com/shahrryyar/reguant`. Import internal packages from there.
- **Test style:** table-ish, stdlib `testing` only, `httptest` for handlers, a temp-file SQLite DB via `db.Init(t.TempDir()+"/test.db")`. Match existing `internal/server/handlers_test.go` and `internal/server/auth_test.go`.
- **DB writes:** SQLite has one writer. Never hold a transaction across a subprocess. Prefer batched appends over per-line UPDATEs.

---

## File Structure

**New files:**
- `internal/deployer/giturl.go` — git remote URL scheme validation (shared by create + deploy).
- `internal/deployer/giturl_test.go`
- `internal/deployer/systemd_env.go` — systemd `Environment=` value escaping/validation.
- `internal/deployer/systemd_env_test.go`
- `internal/db/migrate.go` — versioned schema migrations (replaces bare `createSchema` growth).
- `internal/db/migrate_test.go`
- `internal/server/oauth_allow.go` — GitHub username allowlist check for OAuth login.
- `internal/server/oauth_allow_test.go`
- `internal/server/lifecycle.go` — app stop/start/restart handlers + deployer control methods bridge.
- `internal/server/lifecycle_test.go`
- `internal/server/deployments.go` — deployment history list + rollback handlers.
- `internal/server/deployments_test.go`
- `internal/server/logging.go` — request-logging middleware.

**Modified files:**
- `internal/config/config.go` — add `GitHubAllowedUsers`, `TrustProxyHeaders`; add `Validate()`.
- `internal/config/config_test.go` (create) — validation tests.
- `internal/deployer/deployer.go` — fix `active` map race, capture commit hash, use git-URL validation, use systemd env escaping, add `Stop`/`Start`/`Restart`/`Rollback`, batch log writes.
- `internal/deployer/monitor.go` — respect a stop signal (already ctx-driven; verify).
- `internal/db/db.go` — `SetMaxOpenConns(1)`, call migrations instead of `createSchema`.
- `internal/db/backup.go` — (only if migrate touches it; likely untouched).
- `internal/server/server.go` — thread `ctx` into `statsGathererLoop`/`appStatsPollLoop`, remove the duplicate signal handler, wire new routes + logging middleware, capture-and-store commit on webhook deploy, add CI-status gate.
- `internal/server/auth.go` — OAuth callback must fetch the GitHub user and enforce the allowlist; honor `TrustProxyHeaders` in `clientIP`.
- `cmd/reguant/main.go` — pass the root `ctx` into `server.Start`; call `cfg.Validate()`.
- `.github/workflows/ci.yml` — add `go test -race`.
- `README.md` — document new env vars and endpoints.

---

## Phase 1 — Critical Security

### Task 1: Validate Git remote URL scheme (block `ext::`/`file://` RCE)

`git clone -- <url>` still executes transport helpers named in the URL itself
(`ext::sh -c ...`, `fd::`, `file://` with hooks). Any authenticated user who
can create an app currently reaches command execution as the daemon (root).
Restrict to `https://`, `http://`, `git://`, `ssh://`, and scp-style
`git@host:path`.

**Files:**
- Create: `internal/deployer/giturl.go`
- Test: `internal/deployer/giturl_test.go`

**Interfaces:**
- Produces: `func ValidateGitRepoURL(raw string) error` — nil if allowed, error otherwise.

- [x] **Step 1: Write the failing test**

```go
package deployer

import "testing"

func TestValidateGitRepoURL(t *testing.T) {
	ok := []string{
		"https://github.com/user/repo.git",
		"http://gitlab.local/user/repo",
		"git://example.com/repo.git",
		"ssh://git@github.com/user/repo.git",
		"git@github.com:user/repo.git",
	}
	for _, u := range ok {
		if err := ValidateGitRepoURL(u); err != nil {
			t.Errorf("expected %q to be allowed, got %v", u, err)
		}
	}
	bad := []string{
		"ext::sh -c 'touch /tmp/pwned'",
		"fd::17/foo",
		"file:///etc/passwd",
		"-oProxyCommand=evil",
		"",
		"   ",
		"https://",
	}
	for _, u := range bad {
		if err := ValidateGitRepoURL(u); err == nil {
			t.Errorf("expected %q to be rejected", u)
		}
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/deployer/ -run TestValidateGitRepoURL -v`
Expected: FAIL (`ValidateGitRepoURL` undefined).

- [x] **Step 3: Write minimal implementation**

```go
package deployer

import (
	"fmt"
	"regexp"
	"strings"
)

// scpLike matches git's scp-style remote syntax: [user@]host.tld:path
var scpLike = regexp.MustCompile(`^[a-zA-Z0-9._-]+@[a-zA-Z0-9.-]+:[a-zA-Z0-9._/~-]+$`)

// ValidateGitRepoURL rejects any remote that is not a plain fetchable URL.
// git treats "ext::", "fd::", "file://", and leading-dash strings as transport
// helpers or options, which are remote-code-execution vectors when the URL is
// user-supplied. Only https/http/git/ssh URLs and scp-style git@host:path pass.
func ValidateGitRepoURL(raw string) error {
	s := strings.TrimSpace(raw)
	if s == "" {
		return fmt.Errorf("git repo URL is empty")
	}
	if strings.HasPrefix(s, "-") {
		return fmt.Errorf("git repo URL may not start with '-'")
	}
	for _, scheme := range []string{"https://", "http://", "git://", "ssh://"} {
		if strings.HasPrefix(s, scheme) {
			rest := strings.TrimPrefix(s, scheme)
			if rest == "" || strings.HasPrefix(rest, "/") {
				return fmt.Errorf("git repo URL missing host")
			}
			return nil
		}
	}
	if scpLike.MatchString(s) {
		return nil
	}
	return fmt.Errorf("unsupported git repo URL (allowed: https, http, git, ssh, or git@host:path)")
}
```

- [x] **Step 4: Run test to verify it passes**

Run: `go test ./internal/deployer/ -run TestValidateGitRepoURL -v`
Expected: PASS

- [x] **Step 5: Enforce at both entry points**

In `internal/server/server.go`, inside `handleApps` POST, right after the
`build_type` validation block and before `GetFreePort`:

```go
		// Reject transport-helper URLs (ext::, file://, leading-dash) — RCE vector.
		if err := deployer.ValidateGitRepoURL(req.GitRepo); err != nil {
			http.Error(w, "Invalid git repo: "+err.Error(), http.StatusBadRequest)
			return
		}
```

In `internal/deployer/deployer.go`, at the top of `runDeploymentPipeline`
immediately after the app row is loaded (after `app.SSLEnabled = ...`), add a
defense-in-depth re-check so a row edited out-of-band can't deploy a bad URL:

```go
	if err := ValidateGitRepoURL(app.GitRepo); err != nil {
		updateStatus("failed", fmt.Sprintf("Refusing to deploy invalid git repo: %v\n", err))
		updateAppStatus("failed")
		return
	}
```

- [x] **Step 6: Run the package tests**

Run: `go test ./internal/deployer/ ./internal/server/ -v`
Expected: PASS

- [x] **Step 7: Commit**

```bash
git add internal/deployer/giturl.go internal/deployer/giturl_test.go internal/server/server.go internal/deployer/deployer.go
git commit -m "security: validate git repo URL scheme to block ext:: RCE"
```

---

### Task 2: Escape systemd `Environment=` values (unit-file injection)

`deploySystemd` builds the unit with `fmt.Sprintf("Environment=%s=%s", k, v)`.
An env value containing a newline injects arbitrary unit directives
(`ExecStartPre=`, `User=root`, …). Values must be single-line and quoted.

**Files:**
- Create: `internal/deployer/systemd_env.go`
- Test: `internal/deployer/systemd_env_test.go`
- Modify: `internal/deployer/deployer.go:284-289` (the env-string build in `deploySystemd`)

**Interfaces:**
- Produces:
  - `func SystemdEnvLine(key, value string) (string, error)` — one `Environment="KEY=value"` line, error on illegal key or a value containing a newline.

- [x] **Step 1: Write the failing test**

```go
package deployer

import (
	"strings"
	"testing"
)

func TestSystemdEnvLine(t *testing.T) {
	line, err := SystemdEnvLine("API_KEY", `abc "def" \x`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Quoted, with backslashes and quotes escaped, single line.
	if !strings.HasPrefix(line, `Environment="API_KEY=`) || strings.Contains(line, "\n") {
		t.Errorf("bad rendering: %q", line)
	}
	if !strings.Contains(line, `\"def\"`) || !strings.Contains(line, `\\x`) {
		t.Errorf("quotes/backslashes not escaped: %q", line)
	}

	if _, err := SystemdEnvLine("BAD KEY", "v"); err == nil {
		t.Error("expected error for key with space")
	}
	if _, err := SystemdEnvLine("K", "line1\nExecStartPre=/bin/evil"); err == nil {
		t.Error("expected error for value with newline (injection)")
	}
	if _, err := SystemdEnvLine("", "v"); err == nil {
		t.Error("expected error for empty key")
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/deployer/ -run TestSystemdEnvLine -v`
Expected: FAIL (`SystemdEnvLine` undefined).

- [x] **Step 3: Write minimal implementation**

```go
package deployer

import (
	"fmt"
	"regexp"
	"strings"
)

var envKeyRegex = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// SystemdEnvLine renders one systemd Environment= directive with the value
// double-quoted and dangerous characters escaped. A newline in the value is
// rejected outright because systemd is line-oriented and a newline would let a
// value inject arbitrary unit directives.
func SystemdEnvLine(key, value string) (string, error) {
	if !envKeyRegex.MatchString(key) {
		return "", fmt.Errorf("invalid env key %q", key)
	}
	if strings.ContainsAny(value, "\n\r") {
		return "", fmt.Errorf("env value for %q contains a newline", key)
	}
	// systemd double-quoted strings: escape backslash and double-quote.
	esc := strings.ReplaceAll(value, `\`, `\\`)
	esc = strings.ReplaceAll(esc, `"`, `\"`)
	return fmt.Sprintf(`Environment="%s=%s"`, key, esc), nil
}
```

- [x] **Step 4: Run test to verify it passes**

Run: `go test ./internal/deployer/ -run TestSystemdEnvLine -v`
Expected: PASS

- [x] **Step 5: Use it in `deploySystemd`**

Replace the env-string loop in `internal/deployer/deployer.go` (the block that
currently does `envStrings = append(envStrings, fmt.Sprintf("Environment=%s=%s", k, v))`) with:

```go
	envStrings := []string{fmt.Sprintf("Environment=PORT=%d", app.Port)}
	for k, v := range envMap {
		line, eerr := SystemdEnvLine(k, v)
		if eerr != nil {
			return fmt.Errorf("invalid environment variable: %w", eerr)
		}
		envStrings = append(envStrings, line)
	}
```

- [x] **Step 6: Run the deployer tests**

Run: `go test ./internal/deployer/ -v`
Expected: PASS

- [x] **Step 7: Commit**

```bash
git add internal/deployer/systemd_env.go internal/deployer/systemd_env_test.go internal/deployer/deployer.go
git commit -m "security: escape systemd Environment= values to prevent unit injection"
```

---

### Task 3: Restrict GitHub OAuth login to an allowlist

`handleGitHubCallback` exchanges the code for a token but never checks *who*
logged in — any GitHub account that completes the flow receives the session
cookie, which equals the full API token. Add a required username allowlist:
OAuth login is only enabled when both the client credentials **and**
`REGUANT_GITHUB_ALLOWED_USERS` are set.

**Files:**
- Create: `internal/server/oauth_allow.go`
- Test: `internal/server/oauth_allow_test.go`
- Modify: `internal/config/config.go` (add field), `internal/server/auth.go` (`handleGitHubCallback`)

**Interfaces:**
- Consumes: `Config.GitHubAllowedUsers string` (comma-separated logins).
- Produces:
  - `func parseAllowedUsers(csv string) map[string]bool`
  - `func fetchGitHubLogin(ctx context.Context, accessToken string) (string, error)`

- [x] **Step 1: Add the config field**

In `internal/config/config.go`, add to the struct and `Load()`:

```go
	GitHubAllowedUsers string
```
```go
		GitHubAllowedUsers: getEnv("REGUANT_GITHUB_ALLOWED_USERS", ""),
```

- [x] **Step 2: Write the failing test**

```go
package server

import "testing"

func TestParseAllowedUsers(t *testing.T) {
	m := parseAllowedUsers(" Alice, bob ,,CAROL ")
	for _, want := range []string{"alice", "bob", "carol"} {
		if !m[want] {
			t.Errorf("expected %q allowed; map=%v", want, m)
		}
	}
	if m["dave"] {
		t.Error("dave should not be allowed")
	}
	if len(parseAllowedUsers("")) != 0 {
		t.Error("empty csv should yield empty map")
	}
}
```

- [x] **Step 3: Run test to verify it fails**

Run: `go test ./internal/server/ -run TestParseAllowedUsers -v`
Expected: FAIL (`parseAllowedUsers` undefined).

- [x] **Step 4: Write minimal implementation**

```go
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// parseAllowedUsers turns "Alice, bob" into {"alice":true,"bob":true}.
// Case-insensitive because GitHub logins are.
func parseAllowedUsers(csv string) map[string]bool {
	out := map[string]bool{}
	for _, part := range strings.Split(csv, ",") {
		u := strings.ToLower(strings.TrimSpace(part))
		if u != "" {
			out[u] = true
		}
	}
	return out
}

// fetchGitHubLogin calls the GitHub API with the user's OAuth token and returns
// their login (username). Used to enforce the allowlist after token exchange.
func fetchGitHubLogin(ctx context.Context, accessToken string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/user", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github /user returned %d", resp.StatusCode)
	}
	var u struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return "", err
	}
	if u.Login == "" {
		return "", fmt.Errorf("github /user returned empty login")
	}
	return u.Login, nil
}
```

- [x] **Step 5: Run test to verify it passes**

Run: `go test ./internal/server/ -run TestParseAllowedUsers -v`
Expected: PASS

- [x] **Step 6: Enforce the allowlist in the callback**

In `internal/server/auth.go`:

Gate `handleGitHubLogin` and `handleGitHubCallback` so OAuth is "configured"
only when the allowlist is also set. Change the guard at the top of **both**
handlers from the current `ClientID == "" || ClientSecret == ""` check to:

```go
	if s.cfg.GitHubOAuthClientID == "" || s.cfg.GitHubOAuthClientSecret == "" || s.cfg.GitHubAllowedUsers == "" {
		http.Error(w, "GitHub OAuth is not configured (set REGUANT_GITHUB_OAUTH_CLIENT_ID, REGUANT_GITHUB_OAUTH_CLIENT_SECRET, and REGUANT_GITHUB_ALLOWED_USERS)", http.StatusNotImplemented)
		return
	}
```

In `handleGitHubCallback`, after `tok.AccessToken` is confirmed non-empty and
**before** the session cookie is set, insert:

```go
	// Enforce the username allowlist: the OAuth token only proves the visitor
	// controls *some* GitHub account, not that they are authorized.
	login, err := fetchGitHubLogin(r.Context(), tok.AccessToken)
	if err != nil {
		http.Error(w, "Failed to verify GitHub identity", http.StatusBadGateway)
		return
	}
	if !parseAllowedUsers(s.cfg.GitHubAllowedUsers)[strings.ToLower(login)] {
		http.Error(w, "This GitHub account is not permitted to access Reguant", http.StatusForbidden)
		return
	}
```

Also update the `/api/health` payload in `server.go` so `github_oauth` reports
`true` only when the allowlist is present:

```go
		oauthEnabled := srv.cfg.GitHubOAuthClientID != "" && srv.cfg.GitHubOAuthClientSecret != "" && srv.cfg.GitHubAllowedUsers != ""
```

- [x] **Step 7: Run tests**

Run: `go test ./internal/server/ ./internal/config/ -v`
Expected: PASS

- [x] **Step 8: Commit**

```bash
git add internal/server/oauth_allow.go internal/server/oauth_allow_test.go internal/server/auth.go internal/config/config.go internal/server/server.go
git commit -m "security: restrict GitHub OAuth login to REGUANT_GITHUB_ALLOWED_USERS allowlist"
```

---

## Phase 2 — Correctness & Lifecycle

### Task 4: Fix the `active[appID]` deployment-map race

`Deploy` registers `active[appID]=cancel`; the pipeline's `defer` unconditionally
`delete(d.active, appID)`. If a second deploy for the same app starts while the
first is still finishing, the first goroutine's defer deletes the *second*
goroutine's cancel entry, so the second can no longer be cancelled and a third
deploy won't pre-empt it. Fix by only deleting the entry if it is still ours.

**Files:**
- Modify: `internal/deployer/deployer.go` (`Deploy`, `runDeploymentPipeline`)
- Test: `internal/deployer/deployer_test.go` (create)

**Interfaces:**
- The cancel map becomes keyed to a per-deployment token so a stale defer is a no-op. `Deploy` stays `func (d *Deployer) Deploy(appID string) (string, error)`.

- [x] **Step 1: Write the failing test**

```go
package deployer

import (
	"testing"

	"github.com/shahrryyar/reguant/internal/config"
	"github.com/shahrryyar/reguant/internal/db"
)

func TestActiveMapCleanupIsOwnershipScoped(t *testing.T) {
	database, err := db.Init(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	d := NewDeployer(database, &config.Config{AppsDir: t.TempDir()})

	// Simulate: gen 1 registered, then gen 2 replaces it, then gen 1's defer runs.
	d.mu.Lock()
	d.active["app_x"] = deployHandle{gen: 1, cancel: func() {}}
	d.mu.Unlock()

	d.finishDeploy("app_x", 1) // gen-1 defer
	d.mu.Lock()
	_, still := d.active["app_x"]
	d.mu.Unlock()
	if still {
		t.Fatal("gen-1 finish should have removed its own entry")
	}

	// Now gen 2 registers, gen 1 (stale) tries to finish again -> must NOT delete.
	d.mu.Lock()
	d.active["app_x"] = deployHandle{gen: 2, cancel: func() {}}
	d.mu.Unlock()
	d.finishDeploy("app_x", 1) // stale defer
	d.mu.Lock()
	_, still = d.active["app_x"]
	d.mu.Unlock()
	if !still {
		t.Fatal("stale gen-1 finish must not remove gen-2's entry")
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/deployer/ -run TestActiveMapCleanup -v`
Expected: FAIL (`deployHandle`, `finishDeploy` undefined).

- [x] **Step 3: Implement generation-scoped handles**

In `internal/deployer/deployer.go`, change the `active` field type and add a
counter + helper:

```go
type deployHandle struct {
	gen    uint64
	cancel context.CancelFunc
}

type Deployer struct {
	db     *sql.DB
	cfg    *config.Config
	mu     sync.Mutex
	active map[string]deployHandle
	genSeq uint64
}
```

Update `NewDeployer` to `active: make(map[string]deployHandle)`.

Rewrite `Deploy`'s registration section:

```go
	if h, exists := d.active[appID]; exists {
		h.cancel()
	}
	d.genSeq++
	gen := d.genSeq
	ctx, cancel := context.WithCancel(context.Background())
	d.active[appID] = deployHandle{gen: gen, cancel: cancel}
```

Change the goroutine launch to pass `gen`:

```go
	go d.runDeploymentPipeline(ctx, deploymentID, appID, gen)
```

Change `runDeploymentPipeline`'s signature and defer:

```go
func (d *Deployer) runDeploymentPipeline(ctx context.Context, depID, appID string, gen uint64) {
	defer d.finishDeploy(appID, gen)
```

Add the ownership-scoped finalizer:

```go
// finishDeploy removes an app's active handle only if it still belongs to the
// generation that is finishing, so a slow older deploy's defer cannot evict a
// newer one that has already replaced it.
func (d *Deployer) finishDeploy(appID string, gen uint64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if h, ok := d.active[appID]; ok && h.gen == gen {
		delete(d.active, appID)
	}
}
```

Update `Delete` (it reads `active`): change `if cancel, exists := d.active[appID]; exists { cancel(); ... }` to:

```go
	if h, exists := d.active[appID]; exists {
		h.cancel()
		delete(d.active, appID)
	}
```

- [x] **Step 4: Run test to verify it passes**

Run: `go test ./internal/deployer/ -run TestActiveMapCleanup -v`
Expected: PASS

- [x] **Step 5: Run the whole package with the race detector**

Run: `go test -race ./internal/deployer/ -v`
Expected: PASS

- [x] **Step 6: Commit**

```bash
git add internal/deployer/deployer.go internal/deployer/deployer_test.go
git commit -m "fix: scope deploy active-map cleanup to its own generation (race)"
```

---

### Task 5: Give background loops a lifecycle and remove the duplicate signal handler

`statsGathererLoop` and `appStatsPollLoop` are bare `for {}` loops that ignore
context and never stop. `main.go` installs a `signal.NotifyContext`, and
`server.Start` *also* installs its own `signal.Notify` — two handlers race for
SIGTERM and the stats loops leak. Thread the root context through `Start`.

**Files:**
- Modify: `cmd/reguant/main.go`, `internal/server/server.go`

**Interfaces:**
- Change: `func Start(addr string, db *sql.DB) error` → `func Start(ctx context.Context, addr string, db *sql.DB) error`.
- `statsGathererLoop`/`appStatsPollLoop` take `ctx context.Context` and return on `ctx.Done()`.

- [x] **Step 1: Change `Start`'s signature and drop the local signal handler**

In `internal/server/server.go`:

```go
func Start(ctx context.Context, addr string, db *sql.DB) error {
```

Replace the `go srv.statsGathererLoop()` / `go srv.appStatsPollLoop()` calls with:

```go
	go srv.statsGathererLoop(ctx)
	go srv.appStatsPollLoop(ctx)
```

Delete the entire trailing block that builds `sigChan`, calls `signal.Notify`,
and blocks on `<-sigChan`. Replace it with a wait on the passed context:

```go
	<-ctx.Done()
	log.Println("Received shutdown signal. Gracefully closing active connections...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srvConn.Shutdown(shutdownCtx)
```

Remove the now-unused `os/signal` and `syscall` imports from `server.go`.

- [x] **Step 2: Make the two loops honor ctx**

`statsGathererLoop`:

```go
func (s *Server) statsGathererLoop(ctx context.Context) {
	var prevIdle, prevTotal uint64
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cpu, mem, idle, total := readSystemStats(prevIdle, prevTotal)
			prevIdle, prevTotal = idle, total
			s.statsMu.Lock()
			s.cpuUsage, s.memUsage, s.lastStatT = cpu, mem, time.Now()
			s.statsMu.Unlock()
		}
	}
}
```

`appStatsPollLoop`:

```go
func (s *Server) appStatsPollLoop(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	s.pollAppsStats()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.pollAppsStats()
		}
	}
}
```

- [x] **Step 3: Update the caller in `main.go`**

In `cmd/reguant/main.go`, the `ctx` from `signal.NotifyContext` already exists.
Change the final call:

```go
	if err := server.Start(ctx, ":"+cfg.ServerPort, database); err != nil {
		log.Fatalf("HTTP server failure: %v", err)
	}
```

- [x] **Step 4: Build and vet**

Run: `go build ./... && go vet ./...`
Expected: no output (success).

- [x] **Step 5: Run the server tests**

Run: `go test ./internal/server/ -v`
Expected: PASS (existing tests construct `Server` directly and are unaffected).

- [x] **Step 6: Commit**

```bash
git add cmd/reguant/main.go internal/server/server.go
git commit -m "fix: single shutdown context; background stat loops stop on ctx"
```

---

### Task 6: Batch deployment-log writes (kill the O(n²) DB churn)

`runCmd` runs `UPDATE deployments SET logs = logs || ?` for **every stdout
line**, rewriting the whole `logs` column each time. A large build is quadratic
write amplification and hammers the single SQLite writer. Buffer lines and flush
on a short interval (and at the end).

**Files:**
- Modify: `internal/deployer/deployer.go` (`runCmd`)
- Test: `internal/deployer/deployer_test.go` (add a case)

**Interfaces:**
- Internal change only; `runCmd` signature unchanged.

- [x] **Step 1: Write the failing test**

```go
func TestRunCmdBatchesLogs(t *testing.T) {
	database, err := db.Init(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	d := NewDeployer(database, &config.Config{})

	_, err = database.Exec(`INSERT INTO applications (id,name,git_repo,git_branch,build_type,port,status) VALUES ('a','a','https://x/y','main','systemd',10001,'idle')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.Exec(`INSERT INTO deployments (id, application_id, status) VALUES ('d','a','building')`)
	if err != nil {
		t.Fatal(err)
	}

	// Emit 200 lines; all must land in the logs column.
	if err := d.runCmd(context.Background(), "d", "", "sh", "-c", "for i in $(seq 1 200); do echo line$i; done"); err != nil {
		t.Fatal(err)
	}
	var logs string
	if err := database.QueryRow("SELECT logs FROM deployments WHERE id='d'").Scan(&logs); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs, "line1\n") || !strings.Contains(logs, "line200\n") {
		t.Errorf("missing lines; got %d bytes", len(logs))
	}
}
```

- [x] **Step 2: Run test to verify it fails or passes against current code**

Run: `go test ./internal/deployer/ -run TestRunCmdBatchesLogs -v`
Expected: PASS on current code (behaviour preserved) — this test is the safety
net for the refactor. If it fails, the DB seeding is wrong; fix the test first.

- [x] **Step 3: Rewrite `runCmd` to buffer and flush**

Replace the read loop in `runCmd` (the `for { line, err := reader.ReadString('\n') ... }`
block) with a buffered flusher:

```go
	reader := bufio.NewReader(stdout)
	var buf strings.Builder
	var bufMu sync.Mutex

	flush := func() {
		bufMu.Lock()
		if buf.Len() == 0 {
			bufMu.Unlock()
			return
		}
		chunk := buf.String()
		buf.Reset()
		bufMu.Unlock()
		_, _ = d.db.Exec(`UPDATE deployments SET logs = logs || ? WHERE id = ?`, chunk, depID)
	}

	// Flush at most ~4x/sec so the WS tail stays live without per-line writes.
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(250 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				flush()
			}
		}
	}()

	for {
		line, rerr := reader.ReadString('\n')
		if len(line) > 0 {
			bufMu.Lock()
			buf.WriteString(line)
			bufMu.Unlock()
		}
		if rerr != nil {
			break // EOF or read error ends streaming
		}
	}
	close(done)
	flush() // final drain

	return cmd.Wait()
```

Ensure `sync` and `strings` are imported (both already are in the file).

- [x] **Step 4: Run test to verify it still passes**

Run: `go test -race ./internal/deployer/ -run TestRunCmdBatchesLogs -v`
Expected: PASS

- [x] **Step 5: Commit**

```bash
git add internal/deployer/deployer.go internal/deployer/deployer_test.go
git commit -m "perf: batch deployment log writes instead of one UPDATE per line"
```

---

### Task 7: Cap SQLite to a single writer connection

`db.Init` enables WAL but leaves `MaxOpenConns` unbounded; concurrent
goroutines (deploy flusher, stats loops, webhook, WS pollers) can trip
`database is locked` despite `busy_timeout`. modernc's sqlite is safe with one
open connection serializing writes.

**Files:**
- Modify: `internal/db/db.go`

- [x] **Step 1: Set the connection cap in `Init`**

In `internal/db/db.go`, immediately after `sql.Open` succeeds and before the
pragmas loop:

```go
	// SQLite has exactly one writer; serialize through a single connection so
	// concurrent goroutines queue on the Go side instead of racing to a
	// "database is locked" error.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
```

- [x] **Step 2: Build and run db tests**

Run: `go test ./internal/db/ -v`
Expected: PASS

- [x] **Step 3: Commit**

```bash
git add internal/db/db.go
git commit -m "fix: pin SQLite to a single connection to avoid lock contention"
```

---

### Task 8: Capture the deployed commit hash

`deployments.commit_hash`/`commit_message` columns exist but are always NULL.
After the git checkout, read `HEAD` and persist it, so history and rollback
(Task 11) have something to show and target.

**Files:**
- Modify: `internal/deployer/deployer.go` (`runDeploymentPipeline`, after checkout)

**Interfaces:**
- Produces: `func (d *Deployer) recordCommit(depID, appDir string)` — best-effort, logs on failure.

- [x] **Step 1: Add the helper**

In `internal/deployer/deployer.go`:

```go
// recordCommit reads the checked-out HEAD sha + subject and stores them on the
// deployment row. Best-effort: a missing git or detached state just leaves the
// columns null.
func (d *Deployer) recordCommit(depID, appDir string) {
	out, err := exec.Command("git", "-C", appDir, "log", "-1", "--pretty=%H%n%s").Output()
	if err != nil {
		return
	}
	parts := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)
	hash := parts[0]
	msg := ""
	if len(parts) > 1 {
		msg = parts[1]
	}
	_, _ = d.db.Exec(`UPDATE deployments SET commit_hash = ?, commit_message = ? WHERE id = ?`, hash, msg, depID)
}
```

- [x] **Step 2: Call it after the checkout succeeds**

In `runDeploymentPipeline`, immediately after the git clone/fetch+reset block
(right before `// Step 2: Run build based on BuildType`):

```go
	d.recordCommit(depID, appDir)
```

- [x] **Step 3: Build**

Run: `go build ./...`
Expected: success.

- [x] **Step 4: Commit**

```bash
git add internal/deployer/deployer.go
git commit -m "feat: record deployed commit hash and message on each deployment"
```

---

## Phase 3 — Schema Migrations & Operational Features

### Task 9: Introduce a versioned migration runner

`createSchema` uses `CREATE TABLE IF NOT EXISTS` only, so the schema can never
evolve (new columns for later tasks, indexes). Add a tiny `schema_migrations`
version table and an ordered list of migration statements applied in a
transaction. Migration 1 = the current schema; migration 2 adds the index the
webhook/list queries need.

**Files:**
- Create: `internal/db/migrate.go`, `internal/db/migrate_test.go`
- Modify: `internal/db/db.go` (call `runMigrations` instead of `createSchema`)

**Interfaces:**
- Produces: `func runMigrations(db *sql.DB) error`.

- [x] **Step 1: Write the failing test**

```go
package db

import "testing"

func TestRunMigrationsIsIdempotent(t *testing.T) {
	database, err := Init(t.TempDir() + "/m.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	// Init already ran migrations; running again must be a no-op.
	if err := runMigrations(database); err != nil {
		t.Fatalf("second run failed: %v", err)
	}

	var version int
	if err := database.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version < 2 {
		t.Errorf("expected at least version 2, got %d", version)
	}

	// The apps table and the branch index must exist.
	var n int
	if err := database.QueryRow("SELECT COUNT(*) FROM applications").Scan(&n); err != nil {
		t.Fatalf("applications table missing: %v", err)
	}
	if err := database.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_apps_branch'").Scan(&n); err != nil || n != 1 {
		t.Errorf("expected idx_apps_branch index, got n=%d err=%v", n, err)
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/db/ -run TestRunMigrations -v`
Expected: FAIL (`runMigrations` undefined; `Init` still calls `createSchema`).

- [x] **Step 3: Implement `runMigrations`**

Create `internal/db/migrate.go`:

```go
package db

import (
	"database/sql"
	"fmt"
)

// migrations are applied in order; each runs once, tracked in schema_migrations.
// Never edit an existing entry — append a new one. Statements in one migration
// run in a single transaction.
var migrations = []struct {
	version int
	stmts   []string
}{
	{
		version: 1,
		stmts: []string{
			`CREATE TABLE IF NOT EXISTS applications (
				id TEXT PRIMARY KEY,
				name TEXT NOT NULL UNIQUE,
				git_repo TEXT NOT NULL,
				git_branch TEXT NOT NULL DEFAULT 'main',
				build_type TEXT NOT NULL,
				build_command TEXT,
				run_command TEXT,
				port INTEGER NOT NULL UNIQUE,
				domain TEXT,
				ssl_enabled INTEGER DEFAULT 0,
				env_vars TEXT DEFAULT '{}',
				status TEXT NOT NULL DEFAULT 'idle',
				created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
				updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
			);`,
			`CREATE TABLE IF NOT EXISTS deployments (
				id TEXT PRIMARY KEY,
				application_id TEXT NOT NULL,
				commit_hash TEXT,
				commit_message TEXT,
				status TEXT NOT NULL,
				logs TEXT DEFAULT '',
				started_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
				ended_at TIMESTAMP,
				FOREIGN KEY(application_id) REFERENCES applications(id) ON DELETE CASCADE
			);`,
		},
	},
	{
		version: 2,
		stmts: []string{
			`CREATE INDEX IF NOT EXISTS idx_apps_branch ON applications(git_branch);`,
			`CREATE INDEX IF NOT EXISTS idx_deploys_app ON deployments(application_id, started_at DESC);`,
		},
	},
}

func runMigrations(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	var current int
	_ = db.QueryRow("SELECT COALESCE(MAX(version),0) FROM schema_migrations").Scan(&current)

	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		for _, stmt := range m.stmts {
			if _, err := tx.Exec(stmt); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("migration %d failed: %w", m.version, err)
			}
		}
		if _, err := tx.Exec("INSERT INTO schema_migrations(version) VALUES (?)", m.version); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration %d: %w", m.version, err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
```

- [x] **Step 4: Swap `Init` to call it**

In `internal/db/db.go`, replace the `createSchema(db)` call with `runMigrations(db)`
and delete the now-unused `createSchema` function.

- [x] **Step 5: Run test to verify it passes**

Run: `go test ./internal/db/ -v`
Expected: PASS

- [x] **Step 6: Commit**

```bash
git add internal/db/migrate.go internal/db/migrate_test.go internal/db/db.go
git commit -m "feat: versioned schema migrations with tracking table + indexes"
```

---

### Task 10: App lifecycle control — stop / start / restart

Today you can only deploy or delete; a running app can't be stopped. Add
deployer methods and a REST endpoint.

**Files:**
- Modify: `internal/deployer/deployer.go` (add `Stop`, `Start`, `Restart`)
- Create: `internal/server/lifecycle.go`, `internal/server/lifecycle_test.go`
- Modify: `internal/server/server.go` (route registration)

**Interfaces:**
- Produces (deployer):
  - `func (d *Deployer) Stop(appID string) error`
  - `func (d *Deployer) Start(appID string) error`
  - `func (d *Deployer) Restart(appID string) error`
- Produces (server): `func (s *Server) handleLifecycle(w http.ResponseWriter, r *http.Request)` bound to `/api/apps/lifecycle`.

- [x] **Step 1: Add deployer control methods**

In `internal/deployer/deployer.go`:

```go
// controlTarget fetches the app's name + build type for lifecycle ops.
func (d *Deployer) controlTarget(appID string) (name, buildType string, err error) {
	err = d.db.QueryRow("SELECT name, build_type FROM applications WHERE id = ?", appID).Scan(&name, &buildType)
	return
}

// Stop halts a running app without deleting it (docker stop / systemctl stop).
func (d *Deployer) Stop(appID string) error {
	name, buildType, err := d.controlTarget(appID)
	if err != nil {
		return err
	}
	if buildType == "docker" {
		_ = exec.Command("docker", "stop", fmt.Sprintf("reguant-%s", appID)).Run()
	} else {
		_ = exec.Command("systemctl", "stop", fmt.Sprintf("reguant-%s", appID)).Run()
	}
	_, err = d.db.Exec("UPDATE applications SET status = 'stopped', updated_at = CURRENT_TIMESTAMP WHERE id = ?", appID)
	log.Printf("Stopped application %s", name)
	return err
}

// Start brings a stopped app back up (docker start / systemctl start).
func (d *Deployer) Start(appID string) error {
	name, buildType, err := d.controlTarget(appID)
	if err != nil {
		return err
	}
	if buildType == "docker" {
		if e := exec.Command("docker", "start", fmt.Sprintf("reguant-%s", appID)).Run(); e != nil {
			return fmt.Errorf("docker start failed (has the app been deployed?): %w", e)
		}
	} else {
		if e := exec.Command("systemctl", "start", fmt.Sprintf("reguant-%s", appID)).Run(); e != nil {
			return fmt.Errorf("systemctl start failed: %w", e)
		}
	}
	_, err = d.db.Exec("UPDATE applications SET status = 'running', updated_at = CURRENT_TIMESTAMP WHERE id = ?", appID)
	log.Printf("Started application %s", name)
	return err
}

// Restart is stop+start; for docker it uses `docker restart` to avoid a gap.
func (d *Deployer) Restart(appID string) error {
	_, buildType, err := d.controlTarget(appID)
	if err != nil {
		return err
	}
	if buildType == "docker" {
		if e := exec.Command("docker", "restart", fmt.Sprintf("reguant-%s", appID)).Run(); e != nil {
			return fmt.Errorf("docker restart failed: %w", e)
		}
	} else {
		if e := exec.Command("systemctl", "restart", fmt.Sprintf("reguant-%s", appID)).Run(); e != nil {
			return fmt.Errorf("systemctl restart failed: %w", e)
		}
	}
	_, err = d.db.Exec("UPDATE applications SET status = 'running', updated_at = CURRENT_TIMESTAMP WHERE id = ?", appID)
	return err
}
```

- [x] **Step 2: Write the failing handler test**

Create `internal/server/lifecycle_test.go`:

```go
package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shahrryyar/reguant/internal/config"
	"github.com/shahrryyar/reguant/internal/db"
	"github.com/shahrryyar/reguant/internal/deployer"
)

func TestHandleLifecycleValidation(t *testing.T) {
	database, err := db.Init(t.TempDir() + "/l.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	cfg := &config.Config{}
	s := &Server{db: database, cfg: cfg, deployer: deployer.NewDeployer(database, cfg)}

	// Missing app_id -> 400
	r := httptest.NewRequest(http.MethodPost, "/api/apps/lifecycle?action=stop", nil)
	w := httptest.NewRecorder()
	s.handleLifecycle(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("missing app_id: want 400, got %d", w.Code)
	}

	// Bad action -> 400
	r = httptest.NewRequest(http.MethodPost, "/api/apps/lifecycle?app_id=x&action=explode", nil)
	w = httptest.NewRecorder()
	s.handleLifecycle(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("bad action: want 400, got %d", w.Code)
	}

	// GET -> 405
	r = httptest.NewRequest(http.MethodGet, "/api/apps/lifecycle?app_id=x&action=stop", nil)
	w = httptest.NewRecorder()
	s.handleLifecycle(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: want 405, got %d", w.Code)
	}
}
```

- [x] **Step 3: Run test to verify it fails**

Run: `go test ./internal/server/ -run TestHandleLifecycle -v`
Expected: FAIL (`handleLifecycle` undefined).

- [x] **Step 4: Implement the handler**

Create `internal/server/lifecycle.go`:

```go
package server

import (
	"fmt"
	"net/http"
)

// REST: POST /api/apps/lifecycle?app_id=xxx&action=stop|start|restart
func (s *Server) handleLifecycle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	appID := r.URL.Query().Get("app_id")
	action := r.URL.Query().Get("action")
	if appID == "" {
		http.Error(w, "Missing app_id parameter", http.StatusBadRequest)
		return
	}

	var err error
	switch action {
	case "stop":
		err = s.deployer.Stop(appID)
	case "start":
		err = s.deployer.Start(appID)
	case "restart":
		err = s.deployer.Restart(appID)
	default:
		http.Error(w, "Invalid action: must be stop, start, or restart", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, "Lifecycle action failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(fmt.Sprintf(`{"status":"%s"}`, action)))
}
```

- [x] **Step 5: Register the route**

In `internal/server/server.go`, in the REST endpoints block:

```go
	mux.HandleFunc("/api/apps/lifecycle", srv.handleLifecycle)
```

- [x] **Step 6: Run tests**

Run: `go test ./internal/server/ ./internal/deployer/ -v`
Expected: PASS

- [x] **Step 7: Commit**

```bash
git add internal/deployer/deployer.go internal/server/lifecycle.go internal/server/lifecycle_test.go internal/server/server.go
git commit -m "feat: app lifecycle control (stop/start/restart)"
```

---

### Task 11: Deployment history list + rollback

Expose the `deployments` table via a list endpoint, and add rollback: redeploy
the app checked out at a previous successful commit.

**Files:**
- Create: `internal/server/deployments.go`, `internal/server/deployments_test.go`
- Modify: `internal/deployer/deployer.go` (add `Rollback`), `internal/server/server.go` (routes)

**Interfaces:**
- Produces (server):
  - `func (s *Server) handleListDeployments(w, r)` — `GET /api/apps/deployments?app_id=xxx`, returns JSON array (id, commit_hash, commit_message, status, started_at).
  - `func (s *Server) handleRollback(w, r)` — `POST /api/apps/rollback?app_id=xxx&commit=<sha>`.
- Produces (deployer): `func (d *Deployer) Rollback(appID, commitHash string) (string, error)` — checks out the sha, then runs the normal build; returns a new deployment id.

- [x] **Step 1: Write the failing list test**

Create `internal/server/deployments_test.go`:

```go
package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shahrryyar/reguant/internal/config"
	"github.com/shahrryyar/reguant/internal/db"
	"github.com/shahrryyar/reguant/internal/deployer"
)

func TestHandleListDeployments(t *testing.T) {
	database, err := db.Init(t.TempDir() + "/d.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	cfg := &config.Config{}
	s := &Server{db: database, cfg: cfg, deployer: deployer.NewDeployer(database, cfg)}

	_, _ = database.Exec(`INSERT INTO applications (id,name,git_repo,git_branch,build_type,port,status) VALUES ('a1','app1','https://x/y','main','docker',12001,'running')`)
	_, _ = database.Exec(`INSERT INTO deployments (id,application_id,commit_hash,status) VALUES ('dep1','a1','abc123','success')`)

	r := httptest.NewRequest(http.MethodGet, "/api/apps/deployments?app_id=a1", nil)
	w := httptest.NewRecorder()
	s.handleListDeployments(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	var out []map[string]any
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0]["commit_hash"] != "abc123" {
		t.Errorf("unexpected body: %v", out)
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/server/ -run TestHandleListDeployments -v`
Expected: FAIL (`handleListDeployments` undefined).

- [x] **Step 3: Implement list + rollback handlers**

Create `internal/server/deployments.go`:

```go
package server

import (
	"database/sql"
	"encoding/json"
	"net/http"
)

type deploymentSummary struct {
	ID            string         `json:"id"`
	CommitHash    sql.NullString `json:"-"`
	CommitMessage sql.NullString `json:"-"`
	Hash          string         `json:"commit_hash"`
	Message       string         `json:"commit_message"`
	Status        string         `json:"status"`
	StartedAt     string         `json:"started_at"`
}

// REST: GET /api/apps/deployments?app_id=xxx
func (s *Server) handleListDeployments(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	appID := r.URL.Query().Get("app_id")
	if appID == "" {
		http.Error(w, "Missing app_id parameter", http.StatusBadRequest)
		return
	}
	rows, err := s.db.Query(`
		SELECT id, commit_hash, commit_message, status, started_at
		FROM deployments WHERE application_id = ?
		ORDER BY started_at DESC LIMIT 50`, appID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	out := make([]deploymentSummary, 0)
	for rows.Next() {
		var d deploymentSummary
		if err := rows.Scan(&d.ID, &d.CommitHash, &d.CommitMessage, &d.Status, &d.StartedAt); err == nil {
			d.Hash = d.CommitHash.String
			d.Message = d.CommitMessage.String
			out = append(out, d)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// REST: POST /api/apps/rollback?app_id=xxx&commit=<sha>
func (s *Server) handleRollback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	appID := r.URL.Query().Get("app_id")
	commit := r.URL.Query().Get("commit")
	if appID == "" || commit == "" {
		http.Error(w, "Missing app_id or commit parameter", http.StatusBadRequest)
		return
	}
	depID, err := s.deployer.Rollback(appID, commit)
	if err != nil {
		http.Error(w, "Rollback failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"deployment_id":"` + depID + `","status":"queued"}`))
}
```

- [x] **Step 4: Implement `Rollback` in the deployer**

The current `runDeploymentPipeline` always resets to `origin/<branch>`. Add an
optional checkout override. Simplest non-invasive approach: a dedicated method
that checks out the sha in the existing working copy, then reuses the build
steps by setting a per-app "pinned ref". Implement it as a thin wrapper that
records the intent and calls `Deploy`, with the pipeline honoring a pin.

In `internal/deployer/deployer.go` add a pinned-ref map and the method:

```go
// pinnedRefs holds a one-shot git ref (commit sha) to check out on the next
// deploy of an app, used by Rollback. Consumed and cleared by the pipeline.
// Guarded by mu.
func (d *Deployer) Rollback(appID, commitHash string) (string, error) {
	// Validate the sha shape to keep it out of argv option territory.
	if !isHexSha(commitHash) {
		return "", fmt.Errorf("invalid commit hash")
	}
	d.mu.Lock()
	if d.pinned == nil {
		d.pinned = make(map[string]string)
	}
	d.pinned[appID] = commitHash
	d.mu.Unlock()
	return d.Deploy(appID)
}

func isHexSha(s string) bool {
	if len(s) < 7 || len(s) > 40 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}
```

Add the `pinned map[string]string` field to the `Deployer` struct.

In `runDeploymentPipeline`, in the "Repository exists" branch, after the
`git fetch origin` succeeds, consume any pin:

```go
		d.mu.Lock()
		pin := d.pinned[appID]
		delete(d.pinned, appID)
		d.mu.Unlock()

		resetTarget := "origin/" + app.GitBranch
		if pin != "" {
			resetTarget = pin
			updateStatus("building", fmt.Sprintf("Rolling back to commit %s...\n", pin))
		}
		err = d.runCmd(ctx, depID, appDir, "git", "reset", "--hard", resetTarget)
```

(Replace the existing `git reset --hard origin/`+branch call with this block.)
For the fresh-clone branch a rollback can't apply (no working copy yet); that's
fine — rollback targets an already-deployed app.

- [x] **Step 5: Register routes**

In `internal/server/server.go`:

```go
	mux.HandleFunc("/api/apps/deployments", srv.handleListDeployments)
	mux.HandleFunc("/api/apps/rollback", srv.handleRollback)
```

- [x] **Step 6: Run tests with race**

Run: `go test -race ./internal/server/ ./internal/deployer/ -v`
Expected: PASS

- [x] **Step 7: Commit**

```bash
git add internal/server/deployments.go internal/server/deployments_test.go internal/deployer/deployer.go internal/server/server.go
git commit -m "feat: deployment history list + commit rollback"
```

---

### Task 12: Persist the webhook commit + optional CI-status gate

The webhook ignores the pushed commit and deploys unconditionally. (1) Pass the
head sha into the deploy so history is accurate; (2) add an optional
`REGUANT_REQUIRE_CI_SUCCESS` mode that, when set, checks the GitHub commit
status API and only deploys if the combined status is `success`. This is the
"don't ship untested pushes to prod" gate.

**Files:**
- Modify: `internal/server/server.go` (`handleGitHubWebhook`), `internal/config/config.go`
- Create: `internal/server/ci_status.go`, `internal/server/ci_status_test.go`

**Interfaces:**
- Consumes: `Config.RequireCISuccess bool`, `Config.GitHubAPIToken string` (PAT for status reads on private repos).
- Produces: `func combinedStatusState(body []byte) string` — parses the GitHub combined-status JSON and returns its `state`.

- [x] **Step 1: Add config fields**

In `internal/config/config.go` struct + `Load`:

```go
	RequireCISuccess bool
	GitHubAPIToken   string
```
```go
		RequireCISuccess: getEnv("REGUANT_REQUIRE_CI_SUCCESS", "") == "true",
		GitHubAPIToken:   getEnv("REGUANT_GITHUB_API_TOKEN", ""),
```

- [x] **Step 2: Write the failing parser test**

Create `internal/server/ci_status_test.go`:

```go
package server

import "testing"

func TestCombinedStatusState(t *testing.T) {
	if got := combinedStatusState([]byte(`{"state":"success","total_count":3}`)); got != "success" {
		t.Errorf("want success, got %q", got)
	}
	if got := combinedStatusState([]byte(`{"state":"pending"}`)); got != "pending" {
		t.Errorf("want pending, got %q", got)
	}
	if got := combinedStatusState([]byte(`not json`)); got != "" {
		t.Errorf("want empty on bad json, got %q", got)
	}
}
```

- [x] **Step 3: Run test to verify it fails**

Run: `go test ./internal/server/ -run TestCombinedStatusState -v`
Expected: FAIL (`combinedStatusState` undefined).

- [x] **Step 4: Implement the CI-status helper**

Create `internal/server/ci_status.go`:

```go
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

func combinedStatusState(body []byte) string {
	var v struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return ""
	}
	return v.State
}

// ciSuccess reports whether the GitHub combined commit status for owner/repo@sha
// is "success". apiToken may be empty for public repos. On any error it returns
// (false, err) and the caller decides whether to block.
func ciSuccess(ctx context.Context, apiToken, owner, repo, sha string) (bool, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/commits/%s/status", owner, repo, sha)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if apiToken != "" {
		req.Header.Set("Authorization", "Bearer "+apiToken)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("github status API returned %d", resp.StatusCode)
	}
	return combinedStatusState(body) == "success", nil
}
```

- [x] **Step 5: Capture head sha + owner/repo in the webhook and gate**

In `handleGitHubWebhook`, extend the payload struct to capture the head commit
and the repo owner/name:

```go
	var payload struct {
		Ref        string `json:"ref"`
		After      string `json:"after"` // head sha of the push
		Repository struct {
			HTMLURL  string `json:"html_url"`
			CloneURL string `json:"clone_url"`
			FullName string `json:"full_name"` // "owner/repo"
		} `json:"repository"`
	}
```

After computing `branch` and before the DB lookup, when the gate is on, resolve
CI status once for this push:

```go
	if s.cfg.RequireCISuccess && payload.After != "" && payload.Repository.FullName != "" {
		parts := strings.SplitN(payload.Repository.FullName, "/", 2)
		if len(parts) == 2 {
			ok, err := ciSuccess(r.Context(), s.cfg.GitHubAPIToken, parts[0], parts[1], payload.After)
			if err != nil || !ok {
				log.Printf("[Webhook] CI gate blocked deploy for %s@%s (ok=%v err=%v)", payload.Repository.FullName, payload.After, ok, err)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"status":"skipped","reason":"ci_not_successful"}`))
				return
			}
		}
	}
```

(Ensure `strings` and `log` are imported — both already are in `server.go`.)

- [x] **Step 6: Run tests**

Run: `go test ./internal/server/ ./internal/config/ -v`
Expected: PASS

- [x] **Step 7: Commit**

```bash
git add internal/server/ci_status.go internal/server/ci_status_test.go internal/server/server.go internal/config/config.go
git commit -m "feat: optional CI-success gate before webhook deploys"
```

---

## Phase 4 — Robustness, Config Validation, Observability

### Task 13: Config validation at startup

Catch mis-configuration early: an API token that is too short to be safe, an S3
setup with a bucket but no endpoint, OAuth client id without secret. Warn or
fail fast rather than at first use.

**Files:**
- Modify: `internal/config/config.go` (add `Validate`)
- Create: `internal/config/config_test.go`
- Modify: `cmd/reguant/main.go` (call it)

**Interfaces:**
- Produces: `func (c *Config) Validate() []string` — returns a slice of human-readable warnings (empty = clean). Fatal conditions return an error via a separate `func (c *Config) Fatal() error`.

- [x] **Step 1: Write the failing test**

Create `internal/config/config_test.go`:

```go
package config

import "testing"

func TestValidateWarnsAndFails(t *testing.T) {
	// Short token is a warning, not fatal.
	c := &Config{APIToken: "short"}
	if len(c.Validate()) == 0 {
		t.Error("expected a warning for short API token")
	}

	// Bucket without endpoint is fatal.
	c = &Config{S3Bucket: "b"}
	if c.Fatal() == nil {
		t.Error("expected fatal error for S3 bucket without endpoint")
	}

	// OAuth client id without secret is fatal.
	c = &Config{GitHubOAuthClientID: "id"}
	if c.Fatal() == nil {
		t.Error("expected fatal error for OAuth client id without secret")
	}

	// Fully empty (open mode) is not fatal.
	c = &Config{}
	if err := c.Fatal(); err != nil {
		t.Errorf("empty config should not be fatal, got %v", err)
	}
}
```

- [x] **Step 2: Add `S3Bucket`/`S3Endpoint` to Config**

These are read from env in `main.go` today, not in `Config`. Add them so
validation can see them. In `internal/config/config.go` struct + `Load`:

```go
	S3Endpoint string
	S3Bucket   string
```
```go
		S3Endpoint: getEnv("REGUANT_S3_ENDPOINT", ""),
		S3Bucket:   getEnv("REGUANT_S3_BUCKET", ""),
```

- [x] **Step 3: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestValidate -v`
Expected: FAIL (`Validate`, `Fatal` undefined).

- [x] **Step 4: Implement `Validate` and `Fatal`**

Append to `internal/config/config.go`:

```go
import "fmt" // add to the existing import block

// Validate returns non-fatal warnings about a questionable configuration.
func (c *Config) Validate() []string {
	var warns []string
	if c.APIToken != "" && len(c.APIToken) < 16 {
		warns = append(warns, "REGUANT_API_TOKEN is shorter than 16 chars; use a long random secret")
	}
	if c.APIToken == "" {
		warns = append(warns, "REGUANT_API_TOKEN is unset; API/terminal/webhook are UNAUTHENTICATED")
	}
	if c.WebhookSecret == "" {
		warns = append(warns, "REGUANT_GITHUB_WEBHOOK_SECRET is unset; webhook deploys are unauthenticated")
	}
	return warns
}

// Fatal returns an error for configurations that cannot work correctly.
func (c *Config) Fatal() error {
	if c.S3Bucket != "" && c.S3Endpoint == "" {
		return fmt.Errorf("REGUANT_S3_BUCKET is set but REGUANT_S3_ENDPOINT is empty")
	}
	if (c.GitHubOAuthClientID == "") != (c.GitHubOAuthClientSecret == "") {
		return fmt.Errorf("REGUANT_GITHUB_OAUTH_CLIENT_ID and _SECRET must be set together")
	}
	return nil
}
```

- [x] **Step 5: Wire into `main.go`**

In `cmd/reguant/main.go`, right after `cfg := config.Load()`:

```go
	if err := cfg.Fatal(); err != nil {
		log.Fatalf("Invalid configuration: %v", err)
	}
	for _, w := range cfg.Validate() {
		log.Printf("WARNING: %s", w)
	}
```

Also update the S3 reads in `main.go` to use the config fields
(`cfg.S3Endpoint`, `cfg.S3Bucket`) instead of re-reading env, for consistency.

- [x] **Step 6: Run tests**

Run: `go test ./internal/config/ -v && go build ./...`
Expected: PASS + build success.

- [x] **Step 7: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go cmd/reguant/main.go
git commit -m "feat: startup config validation (fatal errors + warnings)"
```

---

### Task 14: Request-logging middleware + honor a trusted-proxy setting

Add lightweight access logging (method, path, status, duration, client IP) and
make `clientIP` only trust `X-Forwarded-For`/`X-Real-IP` when
`REGUANT_TRUST_PROXY_HEADERS=true` — otherwise a direct client can spoof its IP
and defeat the rate limiter.

**Files:**
- Create: `internal/server/logging.go`
- Modify: `internal/config/config.go` (`TrustProxyHeaders`), `internal/server/auth.go` (`clientIP`), `internal/server/server.go` (middleware chain)

**Interfaces:**
- Consumes: `Config.TrustProxyHeaders bool`.
- Produces: `func (s *Server) loggingMiddleware(next http.Handler) http.Handler`.

- [x] **Step 1: Add the config field**

`internal/config/config.go` struct + `Load`:

```go
	TrustProxyHeaders bool
```
```go
		TrustProxyHeaders: getEnv("REGUANT_TRUST_PROXY_HEADERS", "") == "true",
```

- [x] **Step 2: Make `clientIP` respect the setting**

Change `clientIP` in `internal/server/auth.go` to a method (it needs config):

```go
func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustProxyHeaders {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			return strings.TrimSpace(strings.Split(xff, ",")[0])
		}
		if xri := r.Header.Get("X-Real-IP"); xri != "" {
			return strings.TrimSpace(xri)
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
```

Update the one caller in `rateLimitMiddleware` from `clientIP(r)` to `s.clientIP(r)`.

- [x] **Step 3: Add the logging middleware**

Create `internal/server/logging.go`:

```go
package server

import (
	"log"
	"net/http"
	"time"
)

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// loggingMiddleware logs one line per request. WebSocket upgrades are logged at
// start only (they hijack the connection and never call WriteHeader normally).
func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r)
		log.Printf("%s %s %d %s ip=%s", r.Method, r.URL.Path, rec.status, time.Since(start).Round(time.Millisecond), s.clientIP(r))
	})
}
```

Note: the `statusRecorder` does not implement `http.Hijacker`, which the
WebSocket upgrader needs. To avoid breaking WS, only wrap non-WS paths.

- [x] **Step 4: Wire it into the chain, skipping WS paths**

In `internal/server/server.go`, change the middleware composition so logging
wraps the outermost layer but passes WS upgrades straight through. Replace:

```go
	handler := securityHeaders(srv.corsMiddleware(srv.rateLimitMiddleware(srv.authMiddleware(mux))))
```

with:

```go
	core := securityHeaders(srv.corsMiddleware(srv.rateLimitMiddleware(srv.authMiddleware(mux))))
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The status-recording wrapper is not a Hijacker, so let WS upgrades
		// bypass it; log them at connect time instead.
		if strings.HasPrefix(r.URL.Path, "/api/ws/") {
			log.Printf("%s %s (ws) ip=%s", r.Method, r.URL.Path, srv.clientIP(r))
			core.ServeHTTP(w, r)
			return
		}
		srv.loggingMiddleware(core).ServeHTTP(w, r)
	})
```

- [x] **Step 5: Build, vet, test with race**

Run: `go build ./... && go vet ./... && go test -race ./... `
Expected: success + PASS.

- [x] **Step 6: Commit**

```bash
git add internal/server/logging.go internal/server/auth.go internal/config/config.go internal/server/server.go
git commit -m "feat: request logging + trusted-proxy-gated client IP"
```

---

### Task 15: Add `-race` to CI and document the new surface

**Files:**
- Modify: `.github/workflows/ci.yml`, `README.md`

- [x] **Step 1: Run tests under the race detector in CI**

In `.github/workflows/ci.yml`, change the unit-test step:

```yaml
    - name: Run unit tests
      run: go test -race -v ./...
```

- [x] **Step 2: Document new env vars and endpoints in README**

Add to the environment-variables table in `README.md`:

```markdown
| `REGUANT_GITHUB_ALLOWED_USERS` | Comma-separated GitHub logins allowed to sign in via OAuth. **Required** to enable OAuth login. | _(empty = OAuth disabled)_ |
| `REGUANT_REQUIRE_CI_SUCCESS` | When `true`, webhook deploys only fire if the pushed commit's GitHub combined status is `success`. | `false` |
| `REGUANT_GITHUB_API_TOKEN` | PAT used to read commit status on private repos (for the CI gate). | _(empty)_ |
| `REGUANT_TRUST_PROXY_HEADERS` | Trust `X-Forwarded-For`/`X-Real-IP` for client IP + rate limiting. Only set `true` behind a trusted reverse proxy. | `false` |
```

Add an "Application lifecycle & history" subsection documenting:
- `POST /api/apps/lifecycle?app_id=&action=stop|start|restart`
- `GET /api/apps/deployments?app_id=`
- `POST /api/apps/rollback?app_id=&commit=<sha>`

And note under GitOps that OAuth now requires `REGUANT_GITHUB_ALLOWED_USERS`.

- [x] **Step 3: Commit**

```bash
git add .github/workflows/ci.yml README.md
git commit -m "ci: run tests with -race; docs: document new env vars and endpoints"
```

---

## Self-Review

**Spec coverage** (the "cover almost everything" survey of the whole source):
- `internal/deployer/deployer.go`: git-URL RCE (T1), systemd injection (T2), active-map race (T4), log write amplification (T6), commit capture (T8), lifecycle (T10), rollback (T11) — covered.
- `internal/server/server.go`: git-URL enforcement (T1), OAuth health flag (T3), loop lifecycle + signal dedup (T5), new routes (T10/T11), webhook commit + CI gate (T12), logging chain (T14) — covered.
- `internal/server/auth.go`: OAuth allowlist (T3), trusted-proxy clientIP (T14) — covered.
- `internal/config/config.go`: new fields + validation (T3/T12/T13/T14) — covered.
- `internal/db/db.go`: single-conn cap (T7), migrations (T9) — covered.
- `cmd/reguant/main.go`: ctx into Start (T5), config validation (T13) — covered.
- `.github/workflows/ci.yml` + `README.md`: T15 — covered.
- **Deliberately out of scope** (note for the operator, not tasked): the terminal WebSocket is a root shell by design (T-none) — acceptable given token gating; `GetFreePort` TOCTOU is low-risk on a single-tenant host; the dashboard SPA (`dashboard/dist/index.html`) is unminified vendored assets and untouched. If you want these addressed, say so and I'll add tasks.

**Placeholder scan:** no TBD/TODO/"handle errors appropriately" — every code step is complete.

**Type consistency:** `deployHandle{gen,cancel}` used consistently in T4; `pinned map[string]string` + `isHexSha` in T11 match their uses; `s.clientIP` method form updated at its one caller in T14; `Config` fields added before the tasks that read them.

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-07-10-reguant-hardening.md`. Two execution options:

**1. Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration.

**2. Inline Execution** — Execute tasks in this session using executing-plans, batch execution with checkpoints.

Which approach?
