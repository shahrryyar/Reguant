package server

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
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
	db       *sql.DB
	cfg      *config.Config
	deployer *deployer.Deployer
	proxy    *proxy.NginxManager

	// Real-time system stats cache
	statsMu   sync.RWMutex
	cpuUsage  float64
	memUsage  float64
	lastStatT time.Time
}

func Start(addr string, db *sql.DB) error {
	cfg := config.Load()
	srv := &Server{
		db:       db,
		cfg:      cfg,
		deployer: deployer.NewDeployer(db, cfg),
		proxy:    proxy.NewNginxManager(cfg),
	}

	// Start system metrics gathering routine
	go srv.statsGathererLoop()

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

	// Health Check
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"healthy","uptime":"online"}`))
	})

	// CORS middleware
	corsMux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		mux.ServeHTTP(w, r)
	})

	return http.ListenAndServe(addr, corsMux)
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

	var payload struct {
		Ref        string `json:"ref"` // e.g. "refs/heads/main"
		Repository struct {
			HTMLURL  string `json:"html_url"`  // e.g. "https://github.com/user/repo"
			CloneURL string `json:"clone_url"` // e.g. "https://github.com/user/repo.git"
		} `json:"repository"`
	}

	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
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

// REST: /api/apps/stats (GET resource consumption for all apps)
func (s *Server) handleAppStats(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query("SELECT id FROM applications")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var statsList []AppResourceStats
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			if stats, err := GetAppResourceStats(s.db, id); err == nil {
				statsList = append(statsList, stats)
			}
		}
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

	var lastLogsLength int
	for {
		var logs string
		var status string
		err := s.db.QueryRow("SELECT status, logs FROM deployments WHERE id = ?", depID).Scan(&status, &logs)
		if err != nil {
			break
		}

		if len(logs) > lastLogsLength {
			newLogs := logs[lastLogsLength:]
			err = conn.WriteMessage(websocket.TextMessage, []byte(newLogs))
			if err != nil {
				break
			}
			lastLogsLength = len(logs)
		}

		if status == "success" || status == "failed" {
			_ = conn.WriteMessage(websocket.TextMessage, []byte("\n--- Deployment Finished ---\n"))
			break
		}

		time.Sleep(500 * time.Millisecond)
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

	for {
		s.statsMu.RLock()
		cpu := s.cpuUsage
		mem := s.memUsage
		s.statsMu.RUnlock()

		payload := fmt.Sprintf(`{"cpu":%.2f,"mem":%.2f}`, cpu, mem)
		err = conn.WriteMessage(websocket.TextMessage, []byte(payload))
		if err != nil {
			break
		}

		time.Sleep(1 * time.Second)
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
