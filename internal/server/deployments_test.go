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

func TestHandleRollback(t *testing.T) {
	database, err := db.Init(t.TempDir() + "/r.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	cfg := &config.Config{}
	s := &Server{db: database, cfg: cfg, deployer: deployer.NewDeployer(database, cfg)}

	_, _ = database.Exec(`INSERT INTO applications (id,name,git_repo,git_branch,build_type,port,status) VALUES ('a1','app1','https://x/y','main','docker',12001,'running')`)

	r := httptest.NewRequest(http.MethodPost, "/api/apps/rollback?app_id=a1&commit=abc1234", nil)
	w := httptest.NewRecorder()
	s.handleRollback(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out["status"] != "queued" || out["deployment_id"] == "" {
		t.Errorf("unexpected body: %v", out)
	}
}
