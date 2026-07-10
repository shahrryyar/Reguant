package deployer

import (
	"strings"
	"testing"
)

func TestSystemdEnvLine(t *testing.T) {
	line, err := SystemdEnvLine("API_KEY", `abc "def" \x`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Quoted, with backslashes and quotes escaped, single line.
	if !strings.HasPrefix(line, `Environment="API_KEY=`) || strings.Contains(line, "\n") {
		t.Errorf("bad rendering: %q", line)
	}
	if !strings.Contains(line, `\"def\"`) || !strings.Contains(line, `\\x`) {
		t.Errorf("quotes/backslashes not escaped: %q", line)
	}

	if _, err := SystemdEnvLine("BAD KEY", "v"); err == nil {
		t.Error("expected error for key with space")
	}
	if _, err := SystemdEnvLine("K", "line1\nExecStartPre=/bin/evil"); err == nil {
		t.Error("expected error for value with newline (injection)")
	}
	if _, err := SystemdEnvLine("", "v"); err == nil {
		t.Error("expected error for empty key")
	}
}
