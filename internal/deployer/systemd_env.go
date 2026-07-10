package deployer

import (
	"fmt"
	"regexp"
	"strings"
)

var envKeyRegex = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// SystemdEnvLine renders one systemd Environment= directive with the value
// double-quoted and dangerous characters escaped. A newline in the value is
// rejected outright because systemd is line-oriented and a newline would let a
// value inject arbitrary unit directives.
func SystemdEnvLine(key, value string) (string, error) {
	if !envKeyRegex.MatchString(key) {
		return "", fmt.Errorf("invalid env key %q", key)
	}
	if strings.ContainsAny(value, "\n\r") {
		return "", fmt.Errorf("env value for %q contains a newline", key)
	}
	// systemd double-quoted strings: escape backslash and double-quote.
	esc := strings.ReplaceAll(value, `\`, `\\`)
	esc = strings.ReplaceAll(esc, `"`, `\"`)
	return fmt.Sprintf(`Environment="%s=%s"`, key, esc), nil
}
