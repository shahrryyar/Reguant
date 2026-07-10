package deployer

import (
	"fmt"
	"regexp"
	"strings"
)

// scpLike matches git's scp-style remote syntax: [user@]host.tld:path
var scpLike = regexp.MustCompile(`^[a-zA-Z0-9._-]+@[a-zA-Z0-9.-]+:[a-zA-Z0-9._/~-]+$`)

// ValidateGitRepoURL rejects any remote that is not a plain fetchable URL.
// git treats "ext::", "fd::", "file://", and leading-dash strings as transport
// helpers or options, which are remote-code-execution vectors when the URL is
// user-supplied. Only https/http/git/ssh URLs and scp-style git@host:path pass.
func ValidateGitRepoURL(raw string) error {
	s := strings.TrimSpace(raw)
	if s == "" {
		return fmt.Errorf("git repo URL is empty")
	}
	if strings.HasPrefix(s, "-") {
		return fmt.Errorf("git repo URL may not start with '-'")
	}
	for _, scheme := range []string{"https://", "http://", "git://", "ssh://"} {
		if strings.HasPrefix(s, scheme) {
			rest := strings.TrimPrefix(s, scheme)
			if rest == "" || strings.HasPrefix(rest, "/") {
				return fmt.Errorf("git repo URL missing host")
			}
			return nil
		}
	}
	if scpLike.MatchString(s) {
		return nil
	}
	return fmt.Errorf("unsupported git repo URL (allowed: https, http, git, ssh, or git@host:path)")
}
