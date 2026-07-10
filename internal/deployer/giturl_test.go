package deployer

import "testing"

func TestValidateGitRepoURL(t *testing.T) {
	ok := []string{
		"https://github.com/user/repo.git",
		"http://gitlab.local/user/repo",
		"git://example.com/repo.git",
		"ssh://git@github.com/user/repo.git",
		"git@github.com:user/repo.git",
	}
	for _, u := range ok {
		if err := ValidateGitRepoURL(u); err != nil {
			t.Errorf("expected %q to be allowed, got %v", u, err)
		}
	}
	bad := []string{
		"ext::sh -c 'touch /tmp/pwned'",
		"fd::17/foo",
		"file:///etc/passwd",
		"-oProxyCommand=evil",
		"",
		"   ",
		"https://",
	}
	for _, u := range bad {
		if err := ValidateGitRepoURL(u); err == nil {
			t.Errorf("expected %q to be rejected", u)
		}
	}
}
