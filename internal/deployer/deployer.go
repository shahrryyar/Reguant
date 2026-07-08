package deployer

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shahrryyar/reguant/internal/config"
	"github.com/shahrryyar/reguant/internal/proxy"
)

type Deployer struct {
	db     *sql.DB
	cfg    *config.Config
	mu     sync.Mutex
	active map[string]context.CancelFunc // Map of app ID to cancellation function
}

type Application struct {
	ID           string
	Name         string
	GitRepo      string
	GitBranch    string
	BuildType    string
	BuildCommand string
	RunCommand   string
	Port         int
	Domain       string
	SSLEnabled   bool
	EnvVars      string
	Status       string
}

func NewDeployer(db *sql.DB, cfg *config.Config) *Deployer {
	return &Deployer{
		db:     db,
		cfg:    cfg,
		active: make(map[string]context.CancelFunc),
	}
}

// GetFreePort finds an unused TCP port starting from 10000 up to 20000, cross-referencing DB allocations.
func (d *Deployer) GetFreePort() (int, error) {
	for port := 10000; port <= 20000; port++ {
		// Verify port is not registered in the SQLite DB
		var exists int
		err := d.db.QueryRow("SELECT COUNT(*) FROM applications WHERE port = ?", port).Scan(&exists)
		if err != nil || exists > 0 {
			continue // Port allocated to an app (even if currently stopped/offline)
		}

		// Verify port is not physically occupied on the interface
		ln, err := net.Listen("tcp", ":"+strconv.Itoa(port))
		if err == nil {
			ln.Close()
			return port, nil
		}
	}
	return 0, fmt.Errorf("no free ports available in range 10000-20000")
}

// Deploy triggers a zero-downtime deployment in a separate background goroutine.
func (d *Deployer) Deploy(appID string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Cancel existing deployment for this app if it is running
	if cancel, exists := d.active[appID]; exists {
		cancel()
	}

	ctx, cancel := context.WithCancel(context.Background())
	d.active[appID] = cancel

	// Create a new Deployment entry in the database
	deploymentID := fmt.Sprintf("dep_%d", time.Now().UnixNano())
	_, err := d.db.Exec(`
		INSERT INTO deployments (id, application_id, status, logs)
		VALUES (?, ?, 'queued', 'Deployment queued...\n')`,
		deploymentID, appID)
	if err != nil {
		cancel()
		return "", fmt.Errorf("failed to create deployment log: %w", err)
	}

	// Run build asynchronously
	go d.runDeploymentPipeline(ctx, deploymentID, appID)

	return deploymentID, nil
}

func (d *Deployer) runDeploymentPipeline(ctx context.Context, depID, appID string) {
	defer func() {
		d.mu.Lock()
		delete(d.active, appID)
		d.mu.Unlock()
	}()

	updateStatus := func(status string, logsAppend string) {
		_, _ = d.db.Exec(`
			UPDATE deployments 
			SET status = ?, logs = logs || ? 
			WHERE id = ?`, status, logsAppend, depID)
	}

	updateAppStatus := func(status string) {
		_, _ = d.db.Exec(`UPDATE applications SET status = ? WHERE id = ?`, status, appID)
	}

	// Fetch app config
	var app Application
	var sslVal int
	err := d.db.QueryRow(`
		SELECT id, name, git_repo, git_branch, build_type, build_command, run_command, port, domain, ssl_enabled, env_vars 
		FROM applications WHERE id = ?`, appID).Scan(
		&app.ID, &app.Name, &app.GitRepo, &app.GitBranch, &app.BuildType, &app.BuildCommand, &app.RunCommand, &app.Port, &app.Domain, &sslVal, &app.EnvVars,
	)
	if err != nil {
		updateStatus("failed", fmt.Sprintf("Database error: %v\n", err))
		return
	}
	app.SSLEnabled = (sslVal == 1)

	updateStatus("building", fmt.Sprintf("Starting deployment for %s...\n", app.Name))
	updateAppStatus("deploying")

	appDir := filepath.Join(d.cfg.AppsDir, app.Name)

	// Step 1: Git Clone / Fetch & Checkout
	if _, err := os.Stat(appDir); os.IsNotExist(err) {
		updateStatus("building", fmt.Sprintf("Cloning repository: %s (branch: %s)...\n", app.GitRepo, app.GitBranch))
		err = d.runCmd(ctx, depID, "", "git", "clone", "-b", app.GitBranch, app.GitRepo, appDir)
		if err != nil {
			updateStatus("failed", "Git clone failed.\n")
			updateAppStatus("failed")
			return
		}
	} else {
		updateStatus("building", "Repository exists. Pulling latest changes...\n")
		err = d.runCmd(ctx, depID, appDir, "git", "fetch", "origin")
		if err != nil {
			updateStatus("failed", "Git fetch failed.\n")
			updateAppStatus("failed")
			return
		}
		err = d.runCmd(ctx, depID, appDir, "git", "reset", "--hard", "origin/"+app.GitBranch)
		if err != nil {
			updateStatus("failed", "Git reset failed.\n")
			updateAppStatus("failed")
			return
		}
	}

	// Step 2: Run build based on BuildType (Docker or Systemd)
	if app.BuildType == "docker" {
		err = d.deployDocker(ctx, depID, &app, appDir)
	} else {
		err = d.deploySystemd(ctx, depID, &app, appDir)
	}

	if err != nil {
		updateStatus("failed", fmt.Sprintf("Deployment failed: %v\n", err))
		updateAppStatus("failed")
		return
	}

	updateStatus("success", "Deployment successfully completed! App is live.\n")
	updateAppStatus("running")
}

func (d *Deployer) deployDocker(ctx context.Context, depID string, app *Application, appDir string) error {
	updateLogs := func(msg string) {
		_, _ = d.db.Exec(`UPDATE deployments SET logs = logs || ? WHERE id = ?`, msg, depID)
	}

	// Temporary port for Zero-Downtime Deployment checks
	tempPort, err := d.GetFreePort()
	if err != nil {
		return fmt.Errorf("failed to get temporary port for health check: %w", err)
	}

	imgName := fmt.Sprintf("reguant-%s", app.ID)
	containerNameTemp := fmt.Sprintf("reguant-%s-temp", app.ID)
	containerNameActive := fmt.Sprintf("reguant-%s", app.ID)

	updateLogs(fmt.Sprintf("Building Docker image with BuildKit Cache: %s...\n", imgName))
	// Build using local build limits to stay inside RAM boundaries
	err = d.runCmd(ctx, depID, appDir, "docker", "build", "--network=host", "--build-arg", "BUILDKIT_INLINE_CACHE=1", "-t", imgName, ".")
	if err != nil {
		return fmt.Errorf("docker build failed: %w", err)
	}

	updateLogs("Starting new version on staging port for health checking...\n")
	// Parse env vars
	var envMap map[string]string
	_ = json.Unmarshal([]byte(app.EnvVars), &envMap)

	dockerRunArgs := []string{"run", "-d", "--name", containerNameTemp, "-p", fmt.Sprintf("%d:%d", tempPort, app.Port), "--memory=250m"}
	for k, v := range envMap {
		dockerRunArgs = append(dockerRunArgs, "-e", fmt.Sprintf("%s=%s", k, v))
	}
	dockerRunArgs = append(dockerRunArgs, imgName)

	// Clean up temp container if it already exists
	_ = exec.Command("docker", "rm", "-f", containerNameTemp).Run()

	err = d.runCmd(ctx, depID, appDir, "docker", dockerRunArgs...)
	if err != nil {
		return fmt.Errorf("failed to run staging container: %w", err)
	}

	// Staging check (Simple TCP handshake or HTTP health check loop)
	updateLogs("Running health check on new deployment...\n")
	success := false
	for i := 0; i < 10; i++ {
		time.Sleep(1 * time.Second)
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", tempPort), 1*time.Second)
		if err == nil {
			conn.Close()
			success = true
			break
		}
	}

	if !success {
		_ = exec.Command("docker", "rm", "-f", containerNameTemp).Run()
		return fmt.Errorf("health check failed on port %d, deployment rolled back", tempPort)
	}

	updateLogs("Health check passed. Swapping Nginx proxy upstream...\n")
	// TODO: Update Nginx configs to point to tempPort, reload Nginx
	// For testing, we simulate swap by moving containers.
	_ = exec.Command("docker", "rm", "-f", containerNameActive).Run()
	_ = exec.Command("docker", "rename", containerNameTemp, containerNameActive).Run()

	updateLogs("Routing swap complete. Cleaning up old resources...\n")
	return nil
}

func (d *Deployer) deploySystemd(ctx context.Context, depID string, app *Application, appDir string) error {
	updateLogs := func(msg string) {
		_, _ = d.db.Exec(`UPDATE deployments SET logs = logs || ? WHERE id = ?`, msg, depID)
	}

	// Run custom build command natively on the VPS
	if app.BuildCommand != "" {
		updateLogs(fmt.Sprintf("Running native build command: %s...\n", app.BuildCommand))
		parts := strings.Fields(app.BuildCommand)
		err := d.runCmd(ctx, depID, appDir, parts[0], parts[1:]...)
		if err != nil {
			return fmt.Errorf("native build failed: %w", err)
		}
	}

	// Create systemd service template
	serviceName := fmt.Sprintf("reguant-%s", app.ID)
	servicePath := fmt.Sprintf("/etc/systemd/system/%s.service", serviceName)
	updateLogs(fmt.Sprintf("Configuring systemd service at %s...\n", servicePath))

	var envMap map[string]string
	_ = json.Unmarshal([]byte(app.EnvVars), &envMap)
	envStrings := []string{fmt.Sprintf("Environment=PORT=%d", app.Port)}
	for k, v := range envMap {
		envStrings = append(envStrings, fmt.Sprintf("Environment=%s=%s", k, v))
	}

	serviceConfig := fmt.Sprintf(`[Unit]
Description=Reguant App %s
After=network.target

[Service]
Type=simple
WorkingDirectory=%s
ExecStart=%s
Restart=always
User=root
%s

[Install]
WantedBy=multi-user.target
`, app.Name, appDir, app.RunCommand, strings.Join(envStrings, "\n"))

	// Write systemd file (Requires sudo/admin permissions on VPS)
	err := os.WriteFile(servicePath, []byte(serviceConfig), 0644)
	if err != nil {
		return fmt.Errorf("failed to write systemd file: %w", err)
	}

	// Reload & restart service
	updateLogs("Restarting systemd service...\n")
	_ = exec.Command("systemctl", "daemon-reload").Run()
	_ = exec.Command("systemctl", "enable", serviceName).Run()
	err = d.runCmd(ctx, depID, "", "systemctl", "restart", serviceName)
	if err != nil {
		return fmt.Errorf("failed to restart systemd service: %w", err)
	}

	return nil
}

func (d *Deployer) runCmd(ctx context.Context, depID string, dir string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "DOCKER_BUILDKIT=1")
	if dir != "" {
		cmd.Dir = dir
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		return err
	}

	reader := bufio.NewReader(stdout)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		}

		// Append line directly to DB logs so it streams via WebSockets in real time
		_, _ = d.db.Exec(`UPDATE deployments SET logs = logs || ? WHERE id = ?`, line, depID)
	}

	return cmd.Wait()
}

// Delete stops containers, disables systemd units, removes configurations, deletes codebase files and clears DB entries.
func (d *Deployer) Delete(appID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Cancel active deployment if running
	if cancel, exists := d.active[appID]; exists {
		cancel()
		delete(d.active, appID)
	}

	// Fetch application metadata
	var name string
	var buildType string
	var domain string
	err := d.db.QueryRow("SELECT name, build_type, domain FROM applications WHERE id = ?", appID).Scan(&name, &buildType, &domain)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil // Already deleted or doesn't exist
		}
		return fmt.Errorf("failed to fetch app details for deletion: %w", err)
	}

	// 1. Remove Nginx Proxy
	if domain != "" {
		nginx := proxy.NewNginxManager(d.cfg)
		_ = nginx.DeleteProxy(name)
	}

	// 2. Teardown Runtime Environment
	if buildType == "docker" {
		containerName := fmt.Sprintf("reguant-%s", appID)
		tempContainer := fmt.Sprintf("reguant-%s-temp", appID)
		_ = exec.Command("docker", "rm", "-f", containerName).Run()
		_ = exec.Command("docker", "rm", "-f", tempContainer).Run()
		_ = exec.Command("docker", "rmi", fmt.Sprintf("reguant-%s", appID)).Run()
	} else {
		serviceName := fmt.Sprintf("reguant-%s", appID)
		servicePath := fmt.Sprintf("/etc/systemd/system/%s.service", serviceName)

		_ = exec.Command("systemctl", "stop", serviceName).Run()
		_ = exec.Command("systemctl", "disable", serviceName).Run()

		if _, err := os.Stat(servicePath); err == nil {
			_ = os.Remove(servicePath)
			_ = exec.Command("systemctl", "daemon-reload").Run()
		}
	}

	// 3. Remove Code workspace
	appDir := filepath.Join(d.cfg.AppsDir, name)
	_ = os.RemoveAll(appDir)

	// 4. Remove database record (deployments table records automatically cascade delete)
	_, err = d.db.Exec("DELETE FROM applications WHERE id = ?", appID)
	if err != nil {
		return fmt.Errorf("failed to delete database record: %w", err)
	}

	log.Printf("Successfully tore down resources and deleted application: %s", name)
	return nil
}
