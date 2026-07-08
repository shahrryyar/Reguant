package server

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shahrryyar/reguant/internal/config"
	"github.com/shahrryyar/reguant/internal/deployer"
	"github.com/shahrryyar/reguant/internal/proxy"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

type Server struct {
	db            *sql.DB
	cfg           *config.Config
	deployer      *deployer.Deployer
	proxy         *proxy.NginxManager
	limiter       *rateLimiter
	dashboardHTML []byte

	// Real-time system stats cache
	statsMu   sync.RWMutex
	cpuUsage  float64
	memUsage  float64
	lastStatT time.Time

	// Application-specific resource stats cache
	appStatsMu    sync.RWMutex
	appStatsCache map[string]AppResourceStats
}

func Start(addr string, db *sql.DB) error {
	cfg := config.Load()
	srv := &Server{
		db:            db,
		cfg:           cfg,
		deployer:      deployer.NewDeployer(db, cfg),
		proxy:         proxy.NewNginxManager(cfg),
		appStatsCache: make(map[string]AppResourceStats),
	}

	// Rate limiter for API/webhook abuse protection (kept tight for the RAM budget).
	srv.limiter = newRateLimiter(200, time.Minute)

	// Pre-render the dashboard HTML with the API token injected, so the SPA can
	// authenticate without a separate build step.
	if _, err := os.Stat("dashboard/dist/index.html"); err == nil {
		if raw, rerr := os.ReadFile("dashboard/dist/index.html"); rerr == nil {
			srv.dashboardHTML = []byte(strings.ReplaceAll(string(raw), "REGUANT_API_TOKEN_PLACEHOLDER", cfg.APIToken))
		}
	}

	// Bind WebSocket origin checks to the configured policy.
	upgrader.CheckOrigin = srv.checkOrigin

	if srv.cfg.APIToken == "" {
		log.Println("WARNING: REGUANT_API_TOKEN is not set; the API, terminal, and webhook are unauthenticated. Set it for any exposed deployment.")
	}

	// Start system metrics gathering routine
	go srv.statsGathererLoop()

	// Start background applications resource polling routine
	go srv.appStatsPollLoop()

	mux := http.NewServeMux()

	// REST API Endpoints
	mux.HandleFunc("/api/apps", srv.handleApps)
	mux.HandleFunc("/api/apps/deploy", srv.handleDeploy)
	mux.HandleFunc("/api/apps/stats", srv.handleAppStats)
	mux.HandleFunc("/api/apps/env", srv.handleUpdateEnv)            // Update env vars
	mux.HandleFunc("/api/webhooks/github", srv.handleGitHubWebhook) // Auto deployment webhook

	// WebSockets Endpoints
	mux.HandleFunc("/api/ws/logs", srv.handleWSLogs)
	mux.HandleFunc("/api/ws/stats", srv.handleWSStats)
	mux.HandleFunc("/api/ws/terminal", srv.handleWSTerminal)

	// Authentication (GitHub OAuth login/logout) — public
	mux.HandleFunc("/api/auth/github", srv.handleGitHubLogin)
	mux.HandleFunc("/api/auth/github/callback", srv.handleGitHubCallback)
	mux.HandleFunc("/api/auth/logout", srv.handleLogout)

	// Health Check
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"healthy","uptime":"online"}`))
	})

	// Serve Static Dashboard Files (convenient for local debugging)
	if _, err := os.Stat("dashboard/dist"); err == nil {
		fileServer := http.FileServer(http.Dir("dashboard/dist"))
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, ".") {
				fileServer.ServeHTTP(w, r)
			} else if srv.dashboardHTML != nil {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.Write(srv.dashboardHTML)
			} else {
				http.ServeFile(w, r, "dashboard/dist/index.html")
			}
		})
	}

	// Compose middleware: security headers -> CORS -> rate limit -> auth -> routes
	handler := securityHeaders(srv.corsMiddleware(srv.rateLimitMiddleware(srv.authMiddleware(mux))))

	srvConn := &http.Server{
		Addr:    addr,
		Handler: handler,
	}

	// Run http listener in a goroutine
	go func() {
		log.Printf("Reguant API Backend listening on %s...", addr)
		if err := srvConn.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("Server ListenAndServe error: %v", err)
		}
	}()

	// Listen for operating system shutdown interrupts
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan
	log.Println("Received shutdown signal. Gracefully closing active connections...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srvConn.Shutdown(ctx)
}

// REST: /api/apps (GET list, POST create, DELETE remove)
func (s *Server) handleApps(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		rows, err := s.db.Query(`
			SELECT id, name, git_repo, git_branch, build_type, build_command, run_command, port, domain, ssl_enabled, env_vars, status 
			FROM applications`)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		var apps []deployer.Application
		for rows.Next() {
			var app deployer.Application
			var sslVal int
			err := rows.Scan(
				&app.ID, &app.Name, &app.GitRepo, &app.GitBranch, &app.BuildType, &app.BuildCommand, &app.RunCommand, &app.Port, &app.Domain, &sslVal, &app.EnvVars, &app.Status,
			)
			if err == nil {
				app.SSLEnabled = (sslVal == 1)
				apps = append(apps, app)
			}
		}

		if err := rows.Err(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(apps)

	} else if r.Method == "POST" {
		var req struct {
			Name         string            `json:"name"`
			GitRepo      string            `json:"git_repo"`
			GitBranch    string            `json:"git_branch"`
			BuildType    string            `json:"build_type"`
			BuildCommand string            `json:"build_command"`
			RunCommand   string            `json:"run_command"`
			Domain       string            `json:"domain"`
			EnvVars      map[string]string `json:"env_vars"`
		}

		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// Validate Application Name (prevent Nginx config path traversal/injection)
		nameRegex := regexp.MustCompile(`^[a-zA-Z0-9-_]+$`)
		if !nameRegex.MatchString(req.Name) {
			http.Error(w, "Invalid application name: must contain only alphanumeric, dashes, and underscores", http.StatusBadRequest)
			return
		}

		// Validate Domain if bound (prevent Nginx block injection)
		if req.Domain != "" {
			domainRegex := regexp.MustCompile(`^[a-zA-Z0-9.-]+$`)
			if !domainRegex.MatchString(req.Domain) {
				http.Error(w, "Invalid domain name format", http.StatusBadRequest)
				return
			}
		}

		// Auto allocate free port
		port, err := s.deployer.GetFreePort()
		if err != nil {
			http.Error(w, "Failed to allocate free port: "+err.Error(), http.StatusInternalServerError)
			return
		}

		appID := fmt.Sprintf("app_%d", time.Now().UnixNano())
		if req.EnvVars == nil {
			req.EnvVars = make(map[string]string)
		}
		envVarsJSON, _ := json.Marshal(req.EnvVars)

		_, err = s.db.Exec(`
			INSERT INTO applications (id, name, git_repo, git_branch, build_type, build_command, run_command, port, domain, env_vars, status)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'idle')`,
			appID, req.Name, req.GitRepo, req.GitBranch, req.BuildType, req.BuildCommand, req.RunCommand, port, req.Domain, string(envVarsJSON))

		if err != nil {
			http.Error(w, "Failed to save application: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// Configure Nginx entry in background
		if req.Domain != "" {
			_ = s.proxy.ConfigureProxy(req.Name, req.Domain, port)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(fmt.Sprintf(`{"id":"%s","port":%d,"status":"created"}`, appID, port)))

	} else if r.Method == "DELETE" {
		appID := r.URL.Query().Get("app_id")
		if appID == "" {
			http.Error(w, "Missing app_id parameter", http.StatusBadRequest)
			return
		}

		if err := s.deployer.Delete(appID); err != nil {
			http.Error(w, "Failed to delete application: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"deleted"}`))
	}
}

// REST: /api/apps/deploy?app_id=xxx (POST trigger)
func (s *Server) handleDeploy(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	appID := r.URL.Query().Get("app_id")
	if appID == "" {
		http.Error(w, "Missing app_id parameter", http.StatusBadRequest)
		return
	}

	depID, err := s.deployer.Deploy(appID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(fmt.Sprintf(`{"deployment_id":"%s","status":"queued"}`, depID)))
}

// REST: /api/apps/env?app_id=xxx (PUT update env vars)
func (s *Server) handleUpdateEnv(w http.ResponseWriter, r *http.Request) {
	if r.Method != "PUT" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	appID := r.URL.Query().Get("app_id")
	if appID == "" {
		http.Error(w, "Missing app_id parameter", http.StatusBadRequest)
		return
	}

	var req map[string]string
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	envVarsJSON, err := json.Marshal(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	_, err = s.db.Exec("UPDATE applications SET env_vars = ? WHERE id = ?", string(envVarsJSON), appID)
	if err != nil {
		http.Error(w, "Failed to update env variables: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"success","message":"Environment variables updated"}`))
}

// Webhook: /api/webhooks/github (POST automated Git push trigger)
func (s *Server) handleGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Only act on push events; ignore pings and other event types.
	if event := r.Header.Get("X-GitHub-Event"); event != "" && event != "push" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ignored","reason":"not a push event"}`))
		return
	}

	// Bound the body read so a large/unbounded POST cannot exhaust the RAM budget.
	const maxWebhookBody = 5 << 20 // 5 MiB; GitHub push payloads are far smaller
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBody))
	if err != nil {
		if strings.Contains(err.Error(), "request body too large") {
			http.Error(w, "Request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "Failed to read request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Verify the delivery signature when a webhook secret is configured.
	if s.cfg.WebhookSecret != "" {
		if !verifyGitHubSignature(s.cfg.WebhookSecret, r.Header.Get("X-Hub-Signature-256"), body) {
			http.Error(w, "Invalid webhook signature", http.StatusUnauthorized)
			return
		}
	} else {
		log.Println("WARNING: /api/webhooks/github received but REGUANT_GITHUB_WEBHOOK_SECRET is not set; deploys are unauthenticated.")
	}

	var payload struct {
		Ref        string `json:"ref"` // e.g. "refs/heads/main"
		Repository struct {
			HTMLURL  string `json:"html_url"`  // e.g. "https://github.com/user/repo"
			CloneURL string `json:"clone_url"` // e.g. "https://github.com/user/repo.git"
		} `json:"repository"`
	}

	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "Invalid payload: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Extract pushed branch name
	parts := strings.Split(payload.Ref, "/")
	branch := parts[len(parts)-1]

	// Normalize Git repository URLs (remove .git suffix, force HTTPS matching format)
	normalizeURL := func(url string) string {
		url = strings.TrimSuffix(url, ".git")
		url = strings.ToLower(url)
		return url
	}

	targetRepoNormal := normalizeURL(payload.Repository.HTMLURL)

	// Search SQLite DB for applications running this repo & branch
	rows, err := s.db.Query("SELECT id, git_repo FROM applications WHERE git_branch = ?", branch)
	if err != nil {
		http.Error(w, "Database error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	triggeredCount := 0
	for rows.Next() {
		var appID string
		var gitRepo string
		if err := rows.Scan(&appID, &gitRepo); err == nil {
			if normalizeURL(gitRepo) == targetRepoNormal {
				// Match found! Deploy in background
				_, err := s.deployer.Deploy(appID)
				if err == nil {
					triggeredCount++
				}
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(fmt.Sprintf(`{"status":"received","deployments_triggered":%d}`, triggeredCount)))
}

// verifyGitHubSignature reports whether sigHeader (e.g. "sha256=<hex>") matches the
// HMAC-SHA256 of body using secret. Comparison is constant-time.
func verifyGitHubSignature(secret, sigHeader string, body []byte) bool {
	const prefix = "sha256="
	if !strings.HasPrefix(sigHeader, prefix) {
		return false
	}
	expected, err := hex.DecodeString(strings.TrimPrefix(sigHeader, prefix))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil), expected)
}

// REST: /api/apps/stats (GET resource consumption for all apps)
func (s *Server) handleAppStats(w http.ResponseWriter, r *http.Request) {
	s.appStatsMu.RLock()
	defer s.appStatsMu.RUnlock()

	statsList := make([]AppResourceStats, 0, len(s.appStatsCache))
	for _, stats := range s.appStatsCache {
		statsList = append(statsList, stats)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(statsList)
}

// WS: /api/ws/logs?dep_id=xxx
func (s *Server) handleWSLogs(w http.ResponseWriter, r *http.Request) {
	depID := r.URL.Query().Get("dep_id")
	if depID == "" {
		http.Error(w, "Missing dep_id parameter", http.StatusBadRequest)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("WS upgrade error:", err)
		return
	}
	defer conn.Close()

	// Spawn background reader to process Close/Ping control frames
	go func() {
		for {
			if _, _, err := conn.NextReader(); err != nil {
				conn.Close()
				break
			}
		}
	}()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	var lastLogsLength int
	for {
		select {
		case <-ticker.C:
			conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		default:
			var logs string
			var status string
			err := s.db.QueryRow("SELECT status, logs FROM deployments WHERE id = ?", depID).Scan(&status, &logs)
			if err != nil {
				return
			}

			if len(logs) > lastLogsLength {
				newLogs := logs[lastLogsLength:]
				conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				err = conn.WriteMessage(websocket.TextMessage, []byte(newLogs))
				if err != nil {
					return
				}
				lastLogsLength = len(logs)
			}

			if status == "success" || status == "failed" {
				conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				_ = conn.WriteMessage(websocket.TextMessage, []byte("\n--- Deployment Finished ---\n"))
				return
			}

			time.Sleep(500 * time.Millisecond)
		}
	}
}

// WS: /api/ws/stats
func (s *Server) handleWSStats(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("WS upgrade error:", err)
		return
	}
	defer conn.Close()

	// Spawn background reader to process Close/Ping control frames
	go func() {
		for {
			if _, _, err := conn.NextReader(); err != nil {
				conn.Close()
				break
			}
		}
	}()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		default:
			s.statsMu.RLock()
			cpu := s.cpuUsage
			mem := s.memUsage
			s.statsMu.RUnlock()

			payload := fmt.Sprintf(`{"cpu":%.2f,"mem":%.2f}`, cpu, mem)
			conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
			err = conn.WriteMessage(websocket.TextMessage, []byte(payload))
			if err != nil {
				return
			}

			time.Sleep(1 * time.Second)
		}
	}
}

func (s *Server) statsGathererLoop() {
	var prevIdle, prevTotal uint64
	for {
		cpu, mem, idle, total := readSystemStats(prevIdle, prevTotal)
		prevIdle = idle
		prevTotal = total

		s.statsMu.Lock()
		s.cpuUsage = cpu
		s.memUsage = mem
		s.lastStatT = time.Now()
		s.statsMu.Unlock()

		time.Sleep(1 * time.Second)
	}
}

// readSystemStats reads /proc/stat and /proc/meminfo to calculate real system resource usage percentages.
func readSystemStats(prevIdle, prevTotal uint64) (cpuPercent, memPercent float64, idle, total uint64) {
	statFile, err := os.Open("/proc/stat")
	if err == nil {
		defer statFile.Close()
		scanner := bufio.NewScanner(statFile)
		if scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) >= 5 && fields[0] == "cpu" {
				var user, nice, system, idl, iowait, irq, softirq uint64
				_, _ = fmt.Sscanf(strings.Join(fields[1:8], " "), "%d %d %d %d %d %d %d", &user, &nice, &system, &idl, &iowait, &irq, &softirq)

				idle = idl + iowait
				total = user + nice + system + idl + iowait + irq + softirq

				totalDiff := total - prevTotal
				idleDiff := idle - prevIdle

				if totalDiff > 0 {
					cpuPercent = 100.0 * float64(totalDiff-idleDiff) / float64(totalDiff)
				}
			}
		}
	}

	memFile, err := os.Open("/proc/meminfo")
	if err == nil {
		defer memFile.Close()
		scanner := bufio.NewScanner(memFile)
		var totalMem, freeMem, buffers, cached uint64
		for scanner.Scan() {
			line := scanner.Text()
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				val, _ := strconv.ParseUint(fields[1], 10, 64)
				switch fields[0] {
				case "MemTotal:":
					totalMem = val
				case "MemFree:":
					freeMem = val
				case "Buffers:":
					buffers = val
				case "Cached:":
					cached = val
				}
			}
		}
		usedMem := totalMem - (freeMem + buffers + cached)
		if totalMem > 0 {
			memPercent = 100.0 * float64(usedMem) / float64(totalMem)
		}
	}

	return cpuPercent, memPercent, idle, total
}

func (s *Server) appStatsPollLoop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	// Initial poll on startup
	s.pollAppsStats()

	for {
		select {
		case <-ticker.C:
			s.pollAppsStats()
		}
	}
}

func (s *Server) pollAppsStats() {
	rows, err := s.db.Query("SELECT id FROM applications")
	if err != nil {
		return
	}
	defer rows.Close()

	var appIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			appIDs = append(appIDs, id)
		}
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	tempCache := make(map[string]AppResourceStats)

	for _, id := range appIDs {
		wg.Add(1)
		go func(appID string) {
			defer wg.Done()
			if stats, err := GetAppResourceStats(s.db, appID); err == nil {
				mu.Lock()
				tempCache[appID] = stats
				mu.Unlock()
			}
		}(id)
	}

	wg.Wait()

	s.appStatsMu.Lock()
	s.appStatsCache = tempCache
	s.appStatsMu.Unlock()
}
