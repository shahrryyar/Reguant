package db

import (
	"os"
	"testing"
)

func TestInitDatabase(t *testing.T) {
	tempFile := "test_reguant.db"
	defer os.Remove(tempFile)

	db, err := Init(tempFile)
	if err != nil {
		t.Fatalf("Failed to initialize database: %v", err)
	}
	defer db.Close()

	// Verify tables are created
	var count int
	err = db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='applications'").Scan(&count)
	if err != nil {
		t.Fatalf("Failed to query applications table existence: %v", err)
	}
	if count != 1 {
		t.Errorf("Expected 'applications' table to exist, got %d", count)
	}

	err = db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='deployments'").Scan(&count)
	if err != nil {
		t.Fatalf("Failed to query deployments table existence: %v", err)
	}
	if count != 1 {
		t.Errorf("Expected 'deployments' table to exist, got %d", count)
	}
}
