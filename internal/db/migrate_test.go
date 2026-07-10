package db

import "testing"

func TestRunMigrationsIsIdempotent(t *testing.T) {
	database, err := Init(t.TempDir() + "/m.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	// Init already ran migrations; running again must be a no-op.
	if err := runMigrations(database); err != nil {
		t.Fatalf("second run failed: %v", err)
	}

	var version int
	if err := database.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version < 2 {
		t.Errorf("expected at least version 2, got %d", version)
	}

	// The apps table and the branch index must exist.
	var n int
	if err := database.QueryRow("SELECT COUNT(*) FROM applications").Scan(&n); err != nil {
		t.Fatalf("applications table missing: %v", err)
	}
	if err := database.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_apps_branch'").Scan(&n); err != nil || n != 1 {
		t.Errorf("expected idx_apps_branch index, got n=%d err=%v", n, err)
	}
}
