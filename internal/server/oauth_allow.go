package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// parseAllowedUsers turns "Alice, bob" into {"alice":true,"bob":true}.
// Case-insensitive because GitHub logins are.
func parseAllowedUsers(csv string) map[string]bool {
	out := map[string]bool{}
	for _, part := range strings.Split(csv, ",") {
		u := strings.ToLower(strings.TrimSpace(part))
		if u != "" {
			out[u] = true
		}
	}
	return out
}

// fetchGitHubLogin calls the GitHub API with the user's OAuth token and returns
// their login (username). Used to enforce the allowlist after token exchange.
func fetchGitHubLogin(ctx context.Context, accessToken string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/user", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github /user returned %d", resp.StatusCode)
	}
	var u struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return "", err
	}
	if u.Login == "" {
		return "", fmt.Errorf("github /user returned empty login")
	}
	return u.Login, nil
}
