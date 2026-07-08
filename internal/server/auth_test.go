package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/shahrryyar/reguant/internal/config"
)

func newTestServer(token, cors string) *Server {
	return &Server{cfg: &config.Config{APIToken: token, CORSOrigin: cors}}
}

func TestAuthenticate(t *testing.T) {
	// Open mode: no token configured -> everything allowed.
	open := newTestServer("", "")
	if !open.authenticate(httptest.NewRequest("GET", "/api/apps", nil)) {
		t.Fatal("open mode should allow")
	}

	s := newTestServer("s3cr3t", "")

	if s.authenticate(httptest.NewRequest("GET", "/api/apps", nil)) {
		t.Fatal("no creds should be denied")
	}
	if s.authenticate(httptest.NewRequest("GET", "/api/apps", nil)) {
		t.Fatal("no creds should be denied")
	}

	req := httptest.NewRequest("GET", "/api/apps", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	if s.authenticate(req) {
		t.Fatal("wrong bearer should be denied")
	}

	req = httptest.NewRequest("GET", "/api/apps", nil)
	req.Header.Set("Authorization", "Bearer s3cr3t")
	if !s.authenticate(req) {
		t.Fatal("correct bearer should be allowed")
	}

	req = httptest.NewRequest("GET", "/api/apps", nil)
	req.AddCookie(&http.Cookie{Name: "reguant_session", Value: "s3cr3t"})
	if !s.authenticate(req) {
		t.Fatal("correct cookie should be allowed")
	}

	req = httptest.NewRequest("GET", "/api/ws/terminal?app_id=1&token=s3cr3t", nil)
	if !s.authenticate(req) {
		t.Fatal("correct query token should be allowed")
	}
	if s.authenticate(httptest.NewRequest("GET", "/api/ws/terminal?app_id=1&token=bad", nil)) {
		t.Fatal("wrong query token should be denied")
	}
}

func TestAuthRequired(t *testing.T) {
	s := newTestServer("x", "")
	cases := map[string]bool{
		"/api/health":               false,
		"/api/status":               false,
		"/api/webhooks/github":      false,
		"/api/auth/github":          false,
		"/api/auth/github/callback": false,
		"/api/auth/logout":          false,
		"/api/apps":                 true,
		"/api/apps/deploy?app_id=1": true,
		"/api/apps/env?app_id=1":    true,
		"/api/apps/stats":           true,
		"/api/ws/terminal?app_id=1": true,
		"/logo.jpg":                 false,
		"/":                         false,
	}
	for path, want := range cases {
		if got := s.authRequired(path); got != want {
			t.Errorf("authRequired(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestCheckOrigin(t *testing.T) {
	open := newTestServer("", "")
	r := httptest.NewRequest("GET", "/", nil)
	r.Host = "example.com"
	r.Header.Set("Origin", "https://evil.com")
	if !open.checkOrigin(r) {
		t.Fatal("open mode should allow any origin")
	}

	s := newTestServer("tok", "")
	r = httptest.NewRequest("GET", "/", nil)
	r.Host = "reguant.local"
	r.Header.Set("Origin", "https://reguant.local")
	if !s.checkOrigin(r) {
		t.Fatal("same-host origin should be allowed")
	}
	r = httptest.NewRequest("GET", "/", nil)
	r.Host = "reguant.local"
	r.Header.Set("Origin", "https://evil.com")
	if s.checkOrigin(r) {
		t.Fatal("cross-host origin should be denied when token set")
	}
	r = httptest.NewRequest("GET", "/", nil)
	r.Host = "reguant.local"
	if !s.checkOrigin(r) {
		t.Fatal("no-origin request should be allowed (token gate still applies)")
	}

	c := newTestServer("tok", "https://app.example.com")
	r = httptest.NewRequest("GET", "/", nil)
	r.Host = "reguant.local"
	r.Header.Set("Origin", "https://app.example.com")
	if !c.checkOrigin(r) {
		t.Fatal("configured CORS origin should be allowed")
	}
	r = httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Origin", "https://other.com")
	if c.checkOrigin(r) {
		t.Fatal("non-configured origin should be denied")
	}
}

func TestRateLimiter(t *testing.T) {
	rl := newRateLimiter(3, time.Minute)
	for i := 0; i < 3; i++ {
		if !rl.allow("1.2.3.4") {
			t.Fatalf("request %d should be allowed", i)
		}
	}
	if rl.allow("1.2.3.4") {
		t.Fatal("4th request should be denied")
	}
	if !rl.allow("5.6.7.8") {
		t.Fatal("different IP should be allowed")
	}
}
