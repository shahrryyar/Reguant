package server

import "testing"

func TestParseAllowedUsers(t *testing.T) {
	m := parseAllowedUsers(" Alice, bob ,,CAROL ")
	for _, want := range []string{"alice", "bob", "carol"} {
		if !m[want] {
			t.Errorf("expected %q allowed; map=%v", want, m)
		}
	}
	if m["dave"] {
		t.Error("dave should not be allowed")
	}
	if len(parseAllowedUsers("")) != 0 {
		t.Error("empty csv should yield empty map")
	}
}
