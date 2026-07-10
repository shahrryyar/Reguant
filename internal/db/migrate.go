package db

import (
	"database/sql"
	"fmt"
)

// migrations are applied in order; each runs once, tracked in schema_migrations.
// Never edit an existing entry — append a new one. Statements in one migration
// run in a single transaction.
var migrations = []struct {
	version int
	stmts   []string
}{
	{
		version: 1,
		stmts: []string{
			`CREATE TABLE IF NOT EXISTS applications (
				id TEXT PRIMARY KEY,
				name TEXT NOT NULL UNIQUE,
				git_repo TEXT NOT NULL,
				git_branch TEXT NOT NULL DEFAULT 'main',
				build_type TEXT NOT NULL,
				build_command TEXT,
				run_command TEXT,
				port INTEGER NOT NULL UNIQUE,
				domain TEXT,
				ssl_enabled INTEGER DEFAULT 0,
				env_vars TEXT DEFAULT '{}',
				status TEXT NOT NULL DEFAULT 'idle',
				created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
				updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
			);`,
			`CREATE TABLE IF NOT EXISTS deployments (
				id TEXT PRIMARY KEY,
				application_id TEXT NOT NULL,
				commit_hash TEXT,
				commit_message TEXT,
				status TEXT NOT NULL,
				logs TEXT DEFAULT '',
				started_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
				ended_at TIMESTAMP,
				FOREIGN KEY(application_id) REFERENCES applications(id) ON DELETE CASCADE
			);`,
		},
	},
	{
		version: 2,
		stmts: []string{
			`CREATE INDEX IF NOT EXISTS idx_apps_branch ON applications(git_branch);`,
			`CREATE INDEX IF NOT EXISTS idx_deploys_app ON deployments(application_id, started_at DESC);`,
		},
	},
}

func runMigrations(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	var current int
	_ = db.QueryRow("SELECT COALESCE(MAX(version),0) FROM schema_migrations").Scan(&current)

	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		for _, stmt := range m.stmts {
			if _, err := tx.Exec(stmt); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("migration %d failed: %w", m.version, err)
			}
		}
		if _, err := tx.Exec("INSERT INTO schema_migrations(version) VALUES (?)", m.version); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration %d: %w", m.version, err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
