package proxy

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/shahrryyar/reguant/internal/config"
)

// TestWriteProxyConfigRepointsLivePort verifies the Nginx upstream is rendered
// with the exact port passed in. The deployer depends on this during a
// zero-downtime swap: after promoting the staging container (which keeps
// listening on the staging port) it repoints the upstream to that live port.
// Regressing this would silently route traffic to a dead port after every deploy.
func TestWriteProxyConfigRepointsLivePort(t *testing.T) {
	dir := t.TempDir()
	n := NewNginxManager(&config.Config{NginxDir: dir})

	const (
		appName  = "demo"
		domain   = "demo.example.com"
		livePort = 14257 // e.g. the staging port chosen during a deploy swap
	)

	if _, err := n.writeProxyConfig(appName, domain, livePort); err != nil {
		t.Fatalf("writeProxyConfig returned error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "reguant-"+appName+".conf"))
	if err != nil {
		t.Fatalf("nginx config file was not written: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "server_name "+domain+";") {
		t.Errorf("config missing server_name for %q:\n%s", domain, content)
	}
	if want := "127.0.0.1:" + strconv.Itoa(livePort); !strings.Contains(content, want) {
		t.Errorf("config does not proxy to live port %d (want %q):\n%s", livePort, want, content)
	}
	// Guard against a stale default port being baked into the template.
	if strings.Contains(content, "127.0.0.1:10001") {
		t.Errorf("config unexpectedly references a stale port:\n%s", content)
	}
}

// TestConfigureProxySkipsEmptyDomain ensures no config is written and no reload
// is attempted when an app has no domain (direct-IP / port access).
func TestConfigureProxySkipsEmptyDomain(t *testing.T) {
	dir := t.TempDir()
	n := NewNginxManager(&config.Config{NginxDir: dir})

	if err := n.ConfigureProxy("demo", "", 12345); err != nil {
		t.Fatalf("ConfigureProxy with empty domain returned error: %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "reguant-*.conf"))
	if len(matches) != 0 {
		t.Errorf("expected no config files for empty domain, got: %v", matches)
	}
}
