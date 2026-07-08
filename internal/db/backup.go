package db

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

type BackupConfig struct {
	S3Endpoint string
	BucketName string
	AccessKey  string
	SecretKey  string
	Region     string
	APIToken   string
	Interval   time.Duration
}

// StartBackupScheduler initializes a background loop that uploads SQLite database backups to S3-compatible storage.
func StartBackupScheduler(ctx context.Context, dbPath string, cfg BackupConfig) {
	if cfg.S3Endpoint == "" || cfg.BucketName == "" {
		log.Println("Offline S3 backup replication is idle (requires REGUANT_S3_ENDPOINT & REGUANT_S3_BUCKET).")
		return
	}

	log.Printf("Starting S3 database replication. Interval: %v, Target Bucket: %s", cfg.Interval, cfg.BucketName)

	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()

	// Initial backup on startup
	if err := uploadBackupToS3(dbPath, cfg); err != nil {
		log.Printf("[S3 Backup Error]: %v", err)
	}

	for {
		select {
		case <-ctx.Done():
			log.Println("Stopping S3 backup scheduler...")
			return
		case <-ticker.C:
			if err := uploadBackupToS3(dbPath, cfg); err != nil {
				log.Printf("[S3 Backup Error]: %v", err)
			} else {
				log.Println("Offsite S3 database replication successful.")
			}
		}
	}
}

// uploadBackupToS3 creates a transactionally consistent copy of the live DB via
// VACUUM INTO and uploads it to the S3-compatible bucket using AWS Signature V4
// (or an R2 API-token Bearer header when configured).
func uploadBackupToS3(dbPath string, cfg BackupConfig) error {
	tempBackupPath := dbPath + ".backup"
	defer os.Remove(tempBackupPath)

	// Execute VACUUM INTO to safely checkpoint WAL frames and create a
	// transactionally consistent copy of the active DB.
	dbConn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return fmt.Errorf("failed to open database connection for backup: %w", err)
	}
	defer dbConn.Close()

	if _, err = dbConn.Exec(fmt.Sprintf("VACUUM INTO '%s';", escapeSQLitePath(tempBackupPath))); err != nil {
		return fmt.Errorf("VACUUM INTO execution failed: %w", err)
	}

	file, err := os.Open(tempBackupPath)
	if err != nil {
		return fmt.Errorf("failed to open backup database file: %w", err)
	}
	defer file.Close()

	fileInfo, err := file.Stat()
	if err != nil {
		return fmt.Errorf("failed to read file size: %w", err)
	}
	fileSize := fileInfo.Size()

	targetObject := "reguant_backup.db"
	req, err := newSignedRequest("PUT", cfg.S3Endpoint, cfg.Region, cfg.BucketName, targetObject, cfg.AccessKey, cfg.SecretKey, cfg.APIToken, file, fileSize)
	if err != nil {
		return fmt.Errorf("failed to build S3 request: %w", err)
	}

	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("http upload request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("S3 upload returned bad status (%d): %s", resp.StatusCode, string(body))
	}

	return nil
}

// escapeSQLitePath doubles single quotes so the path is safe inside a SQLite string literal.
func escapeSQLitePath(p string) string {
	return strings.ReplaceAll(p, "'", "''")
}
