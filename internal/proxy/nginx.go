package proxy

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/shahrryyar/reguant/internal/config"
)

type NginxManager struct {
	cfg *config.Config
}

func NewNginxManager(cfg *config.Config) *NginxManager {
	return &NginxManager{cfg: cfg}
}

// ConfigureProxy writes an Nginx virtual host configuration file and reloads Nginx.
func (n *NginxManager) ConfigureProxy(appName string, domain string, port int) error {
	if domain == "" {
		return nil
	}

	configContent := fmt.Sprintf(`server {
    listen 80;
    server_name %s;

    location / {
        proxy_pass http://127.0.0.1:%d;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection 'upgrade';
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_cache_bypass $http_upgrade;
    }
}
`, domain, port)

	configPath := filepath.Join(n.cfg.NginxDir, fmt.Sprintf("reguant-%s.conf", appName))

	// Ensure the config directory exists
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		return fmt.Errorf("failed to create Nginx config directory: %w", err)
	}

	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		return fmt.Errorf("failed to write Nginx configuration file: %w", err)
	}

	return n.ReloadNginx()
}

// DeleteProxy deletes the Nginx virtual host configuration file for an application and reloads Nginx.
func (n *NginxManager) DeleteProxy(appName string) error {
	configPath := filepath.Join(n.cfg.NginxDir, fmt.Sprintf("reguant-%s.conf", appName))

	// Check if file exists before trying to delete
	if _, err := os.Stat(configPath); err == nil {
		if err := os.Remove(configPath); err != nil {
			return fmt.Errorf("failed to remove Nginx config file: %w", err)
		}
		return n.ReloadNginx()
	}

	return nil
}

// EnableSSL invokes Certbot to request a Let's Encrypt certificate for a domain and updates Nginx config automatically.
func (n *NginxManager) EnableSSL(domain string, email string) error {
	if domain == "" {
		return fmt.Errorf("cannot enable SSL for empty domain")
	}

	cmd := exec.Command("certbot", "--nginx", "-d", domain, "--non-interactive", "--agree-tos", "-m", email)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("certbot command failed: %w", err)
	}

	return n.ReloadNginx()
}

// ReloadNginx reloads Nginx configurations gracefully.
func (n *NginxManager) ReloadNginx() error {
	cmdTest := exec.Command("nginx", "-t")
	if err := cmdTest.Run(); err != nil {
		return fmt.Errorf("invalid nginx configuration: %w", err)
	}

	cmdReload := exec.Command("nginx", "-s", "reload")
	if err := cmdReload.Run(); err != nil {
		return fmt.Errorf("failed to reload nginx: %w", err)
	}

	return nil
}
