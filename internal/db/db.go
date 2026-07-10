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

	// SQLite has exactly one writer; serialize through a single connection so
	// concurrent goroutines queue on the Go side instead of racing to a
	// "database is locked" error.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

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

	// Create tables / apply schema migrations if they do not exist
	if err := runMigrations(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to apply database migrations: %w", err)
	}

	log.Println("Database initialized successfully with WAL and performance optimizations.")
	return db, nil
}
