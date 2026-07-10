package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

func combinedStatusState(body []byte) string {
	var v struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return ""
	}
	return v.State
}

// ciSuccess reports whether the GitHub combined commit status for owner/repo@sha
// is "success". apiToken may be empty for public repos. On any error it returns
// (false, err) and the caller decides whether to block.
func ciSuccess(ctx context.Context, apiToken, owner, repo, sha string) (bool, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/commits/%s/status", owner, repo, sha)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if apiToken != "" {
		req.Header.Set("Authorization", "Bearer "+apiToken)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("github status API returned %d", resp.StatusCode)
	}
	return combinedStatusState(body) == "success", nil
}
