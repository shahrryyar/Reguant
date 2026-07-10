package server

import (
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"

	"github.com/shahrryyar/reguant/internal/config"
)

func (s *Server) handleWSTerminal(w http.ResponseWriter, r *http.Request) {
	appID := r.URL.Query().Get("app_id")
	if appID == "" {
		http.Error(w, "Missing app_id parameter", http.StatusBadRequest)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("Terminal WS upgrade error:", err)
		return
	}
	defer conn.Close()

	// Check build type and app status in SQLite
	var buildType string
	var appName string
	err = s.db.QueryRow("SELECT name, build_type FROM applications WHERE id = ?", appID).Scan(&appName, &buildType)
	if err != nil {
		_ = conn.WriteMessage(websocket.TextMessage, []byte("Application not found in database.\r\n"))
		return
	}

	var cmd *exec.Cmd
	if buildType == "docker" {
		// Run a shell inside the container
		containerName := fmt.Sprintf("reguant-%s", appID)
		cmd = exec.Command("docker", "exec", "-it", containerName, "/bin/sh")
	} else {
		// Run a bash shell on the host, scoped to the app folder
		appDir := appWorkingDir(s.cfg, appName)
		cmd = exec.Command("/bin/bash")
		cmd.Dir = appDir
	}

	// Allocate a pseudo-terminal (PTY)
	f, err := pty.Start(cmd)
	if err != nil {
		_ = conn.WriteMessage(websocket.TextMessage, []byte("Failed to spawn command execution shell: "+err.Error()+"\r\n"))
		return
	}
	defer f.Close()

	// Handle cleaning up process if websocket closes
	defer func() {
		_ = cmd.Process.Signal(syscall.SIGKILL)
		_ = cmd.Wait()
	}()

	// Loop 1: Read from PTY terminal and write to WebSocket
	go func() {
		defer conn.Close() // Close connection symmetrically if shell exits, breaking Loop 2
		buf := make([]byte, 1024)
		for {
			n, err := f.Read(buf)
			if err != nil {
				break
			}
			err = conn.WriteMessage(websocket.BinaryMessage, buf[:n])
			if err != nil {
				break
			}
		}
	}()

	// Loop 2: Read from WebSocket and write to PTY terminal
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			break // Exits immediately when the websocket is closed
		}
		_, err = f.Write(msg)
		if err != nil {
			break
		}
	}
}

// appWorkingDir returns the on-host directory an app's native shell should
// start in, honoring REGUANT_APPS_DIR instead of a hardcoded path.
func appWorkingDir(cfg *config.Config, appName string) string {
	return filepath.Join(cfg.AppsDir, appName)
}
