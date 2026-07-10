package deployer

import (
	"context"
	"os/exec"
	"strings"
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

func TestLifecycleStopStart(t *testing.T) {
	oldExecCommand := ExecCommand
	defer func() { ExecCommand = oldExecCommand }()
	ExecCommand = func(name string, arg ...string) *exec.Cmd {
		return exec.Command("true")
	}

	database, err := db.Init(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	d := NewDeployer(database, &config.Config{AppsDir: t.TempDir()})
	_, err = database.Exec(`INSERT INTO applications (id,name,git_repo,git_branch,build_type,port,status) VALUES ('a','a','https://x/y','main','systemd',10001,'running')`)
	if err != nil {
		t.Fatal(err)
	}

	if err := d.Stop("a"); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := database.QueryRow("SELECT status FROM applications WHERE id='a'").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "stopped" {
		t.Errorf("after Stop expected stopped, got %q", status)
	}

	if err := d.Start("a"); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow("SELECT status FROM applications WHERE id='a'").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "running" {
		t.Errorf("after Start expected running, got %q", status)
	}
}
