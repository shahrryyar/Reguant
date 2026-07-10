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
