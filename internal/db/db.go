package db

import (
	"database/sql"
	"fmt"
	"log"

	_ "modernc.org/sqlite"
)

// Init opens the SQLite database at dbPath, applies performance tuning, and creates the required schema.
func Init(dbPath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Apply SQLite speed and concurrency optimizations
	pragmas := []string{
		"PRAGMA journal_mode = WAL;",
		"PRAGMA synchronous = NORMAL;",
		"PRAGMA cache_size = -4000;", // Cache ~4MB of pages in RAM
		"PRAGMA temp_store = MEMORY;",
		"PRAGMA foreign_keys = ON;",
		"PRAGMA busy_timeout = 5000;", // Wait up to 5s for database locks to release
	}

	for _, pragma := range pragmas {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("failed to execute pragma (%s): %w", pragma, err)
		}
	}

	// Create tables if they do not exist
	if err := createSchema(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to create database schema: %w", err)
	}

	log.Println("Database initialized successfully with WAL and performance optimizations.")
	return db, nil
}

func createSchema(db *sql.DB) error {
	schema := `
	CREATE TABLE IF NOT EXISTS applications (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL UNIQUE,
		git_repo TEXT NOT NULL,
		git_branch TEXT NOT NULL DEFAULT 'main',
		build_type TEXT NOT NULL,          -- 'docker' or 'systemd'
		build_command TEXT,                 -- For systemd
		run_command TEXT,                   -- For systemd
		port INTEGER NOT NULL UNIQUE,       -- Internal port allocated for web routing
		domain TEXT,                        -- Dynamic routing domain (e.g. app.test)
		ssl_enabled INTEGER DEFAULT 0,      -- 0 = HTTP, 1 = HTTPS
		env_vars TEXT DEFAULT '{}',         -- JSON string of env key-value pairs
		status TEXT NOT NULL DEFAULT 'idle',-- 'idle', 'deploying', 'running', 'failed'
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS deployments (
		id TEXT PRIMARY KEY,
		application_id TEXT NOT NULL,
		commit_hash TEXT,
		commit_message TEXT,
		status TEXT NOT NULL,               -- 'queued', 'building', 'success', 'failed'
		logs TEXT DEFAULT '',               -- Streaming logs
		started_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		ended_at TIMESTAMP,
		FOREIGN KEY(application_id) REFERENCES applications(id) ON DELETE CASCADE
	);
	`
	_, err := db.Exec(schema)
	return err
}
