package server

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/shahrryyar/reguant/internal/config"
	"github.com/shahrryyar/reguant/internal/db"
	"github.com/shahrryyar/reguant/internal/deployer"
)

// insertApp is a small helper to seed an application row for handler tests.
// A process-wide counter hands out a distinct port to every seeded app so
// parallel/sequential tests never trip the UNIQUE(port) constraint.
var insertAppPort = 11000

func insertApp(t *testing.T, database *sql.DB, id, gitRepo, domain string, ssl int) {
	t.Helper()
	insertAppPort++
	port := insertAppPort
	_, err := database.Exec(`INSERT INTO applications
		(id,name,git_repo,git_branch,build_type,build_command,run_command,port,domain,env_vars,status,ssl_enabled)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, id+"_name", gitRepo, "main", "systemd", "make", "./run", port, domain, "{}", "idle", ssl)
	if err != nil {
		t.Fatal(err)
	}
}

func newHandlerTestServer(database *sql.DB) *Server {
	cfg := &config.Config{}
	return &Server{db: database, cfg: cfg, deployer: deployer.NewDeployer(database, cfg)}
}

// T1: an app registered with an SSH clone URL must auto-deploy when a
// GitHub push webhook arrives carrying an HTTPS URL, and unrelated repos
// must not trigger.
func TestGitHubWebhookTriggersDeploySSH(t *testing.T) {
	tmp := "test_webhook_ssh.db"
	defer os.Remove(tmp)
	database, err := db.Init(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s := newHandlerTestServer(database)

	insertApp(t, database, "app_ssh", "git@github.com:user/repo.git", "", 0)
	insertApp(t, database, "app_other", "https://github.com/other/repo", "", 0)

	body := `{"ref":"refs/heads/main","repository":{"html_url":"https://github.com/user/repo","clone_url":"https://github.com/user/repo.git"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/github", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", "push")
	w := httptest.NewRecorder()
	s.handleGitHubWebhook(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}

	var sshCnt, otherCnt int
	database.QueryRow("SELECT count(*) FROM deployments WHERE application_id = ?", "app_ssh").Scan(&sshCnt)
	database.QueryRow("SELECT count(*) FROM deployments WHERE application_id = ?", "app_other").Scan(&otherCnt)
	if sshCnt == 0 {
		t.Fatal("expected a deployment to be triggered for the SSH-registered repo")
	}
	if otherCnt != 0 {
		t.Fatalf("unexpected deployment triggered for non-matching repo: %d", otherCnt)
	}
}

// T1: an app stored with an HTTPS URL must also match an HTTPS webhook.
func TestGitHubWebhookTriggersDeployHTTPS(t *testing.T) {
	tmp := "test_webhook_https.db"
	defer os.Remove(tmp)
	database, err := db.Init(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s := newHandlerTestServer(database)

	insertApp(t, database, "app_https", "https://github.com/user/repo", "", 0)

	body := `{"ref":"refs/heads/main","repository":{"html_url":"https://github.com/user/repo"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/github", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", "push")
	w := httptest.NewRecorder()
	s.handleGitHubWebhook(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var cnt int
	database.QueryRow("SELECT count(*) FROM deployments WHERE application_id = ?", "app_https").Scan(&cnt)
	if cnt == 0 {
		t.Fatal("expected deployment for HTTPS-stored repo")
	}
}

// T2: saving env vars must trigger a redeploy.
func TestHandleUpdateEnvRedeploys(t *testing.T) {
	tmp := "test_env.db"
	defer os.Remove(tmp)
	database, err := db.Init(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s := newHandlerTestServer(database)

	insertApp(t, database, "app_env", "https://github.com/u/r", "", 0)

	body := `{"KEY":"VALUE"}`
	req := httptest.NewRequest(http.MethodPut, "/api/apps/env?app_id=app_env", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleUpdateEnv(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	var cnt int
	database.QueryRow("SELECT count(*) FROM deployments WHERE application_id = ?", "app_env").Scan(&cnt)
	if cnt == 0 {
		t.Fatal("expected a redeploy to be triggered after env save")
	}
}

// T5: PUT /api/apps toggles ssl_enabled without touching Nginx.
func TestHandleUpdateAppSSLEnabledToggle(t *testing.T) {
	tmp := "test_upd.db"
	defer os.Remove(tmp)
	database, err := db.Init(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s := newHandlerTestServer(database)

	insertApp(t, database, "app_upd", "https://github.com/u/r", "example.com", 0)

	req := httptest.NewRequest(http.MethodPut, "/api/apps?app_id=app_upd", strings.NewReader(`{"ssl_enabled":true}`))
	w := httptest.NewRecorder()
	s.handleUpdateApp(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	var ssl int
	database.QueryRow("SELECT ssl_enabled FROM applications WHERE id = ?", "app_upd").Scan(&ssl)
	if ssl != 1 {
		t.Fatalf("expected ssl_enabled=1, got %d", ssl)
	}

	req2 := httptest.NewRequest(http.MethodPut, "/api/apps?app_id=app_upd", strings.NewReader(`{"ssl_enabled":false}`))
	w2 := httptest.NewRecorder()
	s.handleUpdateApp(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("status %d", w2.Code)
	}
	database.QueryRow("SELECT ssl_enabled FROM applications WHERE id = ?", "app_upd").Scan(&ssl)
	if ssl != 0 {
		t.Fatalf("expected ssl_enabled=0, got %d", ssl)
	}
}

// T5: an invalid domain in the update payload is rejected.
func TestHandleUpdateAppInvalidDomain(t *testing.T) {
	tmp := "test_updinv.db"
	defer os.Remove(tmp)
	database, err := db.Init(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s := newHandlerTestServer(database)

	insertApp(t, database, "app_inv", "https://github.com/u/r", "good.com", 0)

	req := httptest.NewRequest(http.MethodPut, "/api/apps?app_id=app_inv", strings.NewReader(`{"domain":"not a valid domain!"}`))
	w := httptest.NewRecorder()
	s.handleUpdateApp(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid domain, got %d", w.Code)
	}
}

// T4: enabling SSL without a configured domain is rejected.
func TestHandleEnableSSLRequiresDomain(t *testing.T) {
	tmp := "test_ssl_dom.db"
	defer os.Remove(tmp)
	database, err := db.Init(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s := newHandlerTestServer(database)

	insertApp(t, database, "app_nodomain", "https://github.com/u/r", "", 0)

	req := httptest.NewRequest(http.MethodPost, "/api/apps/ssl?app_id=app_nodomain&email=a@b.com", nil)
	w := httptest.NewRecorder()
	s.handleEnableSSL(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing domain, got %d", w.Code)
	}
}

// T4: enabling SSL without any email source is rejected.
func TestHandleEnableSSLRequiresEmail(t *testing.T) {
	tmp := "test_ssl_email.db"
	defer os.Remove(tmp)
	database, err := db.Init(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s := newHandlerTestServer(database)

	insertApp(t, database, "app_dom", "https://github.com/u/r", "example.com", 0)

	req := httptest.NewRequest(http.MethodPost, "/api/apps/ssl?app_id=app_dom", nil)
	w := httptest.NewRecorder()
	s.handleEnableSSL(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing email, got %d", w.Code)
	}
}

// T1: canonicalRepoURL reduces SSH/HTTPS/clone_url forms to one comparable key.
func TestCanonicalRepoURL(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"https://github.com/user/repo", "github.com/user/repo"},
		{"https://github.com/user/repo.git", "github.com/user/repo"},
		{"git@github.com:user/repo.git", "github.com/user/repo"},
		{"GIT@GitHub.com:User/Repo.git", "github.com/user/repo"},
		{"ssh://git@github.com:22/user/repo.git", "github.com/user/repo"},
		{"https://github.com/user/repo/", "github.com/user/repo"},
	}
	for _, c := range cases {
		if got := canonicalRepoURL(c.in); got != c.want {
			t.Errorf("canonicalRepoURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// T3: the on-host shell directory honors REGUANT_APPS_DIR, not a hardcoded path.
func TestAppWorkingDir(t *testing.T) {
	if got := appWorkingDir(&config.Config{AppsDir: "/srv/apps"}, "myapp"); got != "/srv/apps/myapp" {
		t.Errorf("got %q", got)
	}
	if got := appWorkingDir(&config.Config{AppsDir: "/var/lib/reguant/apps/"}, "x"); got != "/var/lib/reguant/apps/x" {
		t.Errorf("got %q", got)
	}
}
