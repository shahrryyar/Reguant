package server

import (
	"crypto/hmac"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// rateLimiter is a tiny fixed-window, per-IP limiter. It is intentionally
// dependency-free and bounded: entries expire lazily on access, so memory stays
// proportional to the number of recently-seen clients (fine for a self-hosted PaaS).
type rateLimiter struct {
	mu     sync.Mutex
	hits   map[string]int
	reset  map[string]time.Time
	limit  int
	window time.Duration
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{
		hits:   make(map[string]int),
		reset:  make(map[string]time.Time),
		limit:  limit,
		window: window,
	}
}

func (rl *rateLimiter) allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	if t, ok := rl.reset[ip]; !ok || now.After(t) {
		rl.hits[ip] = 1
		rl.reset[ip] = now.Add(rl.window)
		return true
	}
	if rl.hits[ip] < rl.limit {
		rl.hits[ip]++
		return true
	}
	return false
}

// clientIP extracts the furthest upstream IP, honoring standard proxy headers.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.Split(xff, ",")[0])
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// authRequired reports whether the given path must be authenticated.
// Public: health, status, the GitHub webhook (HMAC-verified separately), the
// OAuth flow, and all non-API (dashboard) routes.
func (s *Server) authRequired(path string) bool {
	if !strings.HasPrefix(path, "/api/") {
		return false // dashboard/static assets are public
	}
	switch {
	case path == "/api/health", path == "/api/status":
		return false
	case path == "/api/webhooks/github":
		return false
	case strings.HasPrefix(path, "/api/auth/"):
		return false
	default:
		return true
	}
}

// authenticate verifies the request when an API token is configured.
// Accepts: Bearer header, the reguant_session cookie, or a ?token= query param
// (the latter is how the dashboard authenticates WebSocket upgrades, which
// cannot set custom headers).
func (s *Server) authenticate(r *http.Request) bool {
	if s.cfg.APIToken == "" {
		return true // open mode (backward-compatible); a warning is logged at startup
	}
	want := []byte(s.cfg.APIToken)

	if ah := r.Header.Get("Authorization"); strings.HasPrefix(ah, "Bearer ") {
		got := []byte(strings.TrimPrefix(ah, "Bearer "))
		return hmac.Equal(want, got)
	}
	if c, err := r.Cookie("reguant_session"); err == nil {
		return hmac.Equal(want, []byte(c.Value))
	}
	if t := r.URL.Query().Get("token"); t != "" {
		return hmac.Equal(want, []byte(t))
	}
	return false
}

// checkOrigin validates WebSocket/HTTP origins. In open mode everything is
// allowed; otherwise the origin must match the configured CORS origin or the
// server's own host.
func (s *Server) checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if s.cfg.CORSOrigin != "" {
		return origin == s.cfg.CORSOrigin
	}
	if s.cfg.APIToken == "" {
		return true
	}
	if origin == "" {
		return true // non-browser clients (still need a valid token)
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return u.Host == r.Host
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.authRequired(r.URL.Path) && !s.authenticate(r) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		allow := ""
		switch {
		case s.cfg.CORSOrigin != "":
			allow = s.cfg.CORSOrigin
		case s.cfg.APIToken == "":
			allow = "*"
		case origin != "":
			if u, err := url.Parse(origin); err == nil && u.Host == r.Host {
				allow = origin
			}
		}
		if allow != "" {
			w.Header().Set("Access-Control-Allow-Origin", allow)
			w.Header().Set("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy",
			"default-src 'self'; "+
				"script-src 'self' 'unsafe-inline'; "+
				"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; "+
				"font-src 'self' https://fonts.gstatic.com data:; "+
				"img-src 'self' data: https://placehold.co; "+
				"connect-src 'self' ws: wss:; "+
				"frame-ancestors 'none'; base-uri 'self'")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) rateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			if !s.limiter.allow(clientIP(r)) {
				http.Error(w, "Too many requests", http.StatusTooManyRequests)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// ---- GitHub OAuth (optional, browser-friendly login that issues the API token as a session cookie) ----

func isTLS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func callbackURL(r *http.Request) string {
	proto := "http"
	if isTLS(r) {
		proto = "https"
	}
	return proto + "://" + r.Host + "/api/auth/github/callback"
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand should never fail; fall back to a zeroed token unlikely to match.
		return strings.Repeat("0", 2*n)
	}
	return hex.EncodeToString(b)
}

func (s *Server) handleGitHubLogin(w http.ResponseWriter, r *http.Request) {
	if s.cfg.GitHubOAuthClientID == "" || s.cfg.GitHubOAuthClientSecret == "" || s.cfg.GitHubAllowedUsers == "" {
		http.Error(w, "GitHub OAuth is not configured (set REGUANT_GITHUB_OAUTH_CLIENT_ID, REGUANT_GITHUB_OAUTH_CLIENT_SECRET, and REGUANT_GITHUB_ALLOWED_USERS)", http.StatusNotImplemented)
		return
	}
	state := randomHex(32)
	http.SetCookie(w, &http.Cookie{
		Name:     "reguant_oauth_state",
		Value:    state,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isTLS(r),
		MaxAge:   600,
	})
	redir := "https://github.com/login/oauth/authorize?" +
		"client_id=" + url.QueryEscape(s.cfg.GitHubOAuthClientID) +
		"&redirect_uri=" + url.QueryEscape(callbackURL(r)) +
		"&scope=read%3Auser" +
		"&state=" + state
	http.Redirect(w, r, redir, http.StatusFound)
}

func (s *Server) handleGitHubCallback(w http.ResponseWriter, r *http.Request) {
	if s.cfg.GitHubOAuthClientID == "" || s.cfg.GitHubOAuthClientSecret == "" || s.cfg.GitHubAllowedUsers == "" {
		http.Error(w, "GitHub OAuth is not configured (set REGUANT_GITHUB_OAUTH_CLIENT_ID, REGUANT_GITHUB_OAUTH_CLIENT_SECRET, and REGUANT_GITHUB_ALLOWED_USERS)", http.StatusNotImplemented)
		return
	}
	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	sc, err := r.Cookie("reguant_oauth_state")
	if err != nil || sc.Value == "" || code == "" || !hmac.Equal([]byte(sc.Value), []byte(state)) {
		http.Error(w, "Invalid OAuth state", http.StatusBadRequest)
		return
	}

	form := url.Values{}
	form.Set("client_id", s.cfg.GitHubOAuthClientID)
	form.Set("client_secret", s.cfg.GitHubOAuthClientSecret)
	form.Set("code", code)
	form.Set("redirect_uri", callbackURL(r))

	req, err := http.NewRequest(http.MethodPost, "https://github.com/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		http.Error(w, "Failed to build token request", http.StatusInternalServerError)
		return
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "Token exchange failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	var tok struct {
		AccessToken string `json:"access_token"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&tok)
	if tok.AccessToken == "" {
		http.Error(w, "GitHub did not return an access token", http.StatusBadGateway)
		return
	}

	// Enforce the username allowlist: the OAuth token only proves the visitor
	// controls *some* GitHub account, not that they are authorized.
	login, err := fetchGitHubLogin(r.Context(), tok.AccessToken)
	if err != nil {
		http.Error(w, "Failed to verify GitHub identity", http.StatusBadGateway)
		return
	}
	if !parseAllowedUsers(s.cfg.GitHubAllowedUsers)[strings.ToLower(login)] {
		http.Error(w, "This GitHub account is not permitted to access Reguant", http.StatusForbidden)
		return
	}

	// Issue a session cookie whose value is the configured API token (stateless).
	http.SetCookie(w, &http.Cookie{
		Name:     "reguant_session",
		Value:    s.cfg.APIToken,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isTLS(r),
		MaxAge:   60 * 60 * 24 * 30,
	})
	// Consume the one-time state cookie.
	http.SetCookie(w, &http.Cookie{Name: "reguant_oauth_state", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: "reguant_session", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/", http.StatusFound)
}
