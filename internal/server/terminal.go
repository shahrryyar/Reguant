package server

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os/exec"
	"syscall"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
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
		appDir := fmt.Sprintf("/var/lib/reguant/apps/%s", appName)
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

	// Channel to signal output loop exits
	done := make(chan struct{})

	// Loop 1: Read from PTY terminal and write to WebSocket
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := f.Read(buf)
			if err != nil {
				if err != io.EOF {
					log.Println("PTY read error:", err)
				}
				break
			}
			err = conn.WriteMessage(websocket.BinaryMessage, buf[:n])
			if err != nil {
				break
			}
		}
		close(done)
	}()

	// Loop 2: Read from WebSocket and write to PTY terminal
	for {
		select {
		case <-done:
			return
		default:
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			_, err = f.Write(msg)
			if err != nil {
				return
			}
		}
	}
}
