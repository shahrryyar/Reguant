package db

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

type BackupConfig struct {
	S3Endpoint string // e.g. "https://<account_id>.r2.cloudflarestorage.com"
	BucketName string
	AccessKey  string
	SecretKey  string
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

// uploadBackupToS3 runs a lightweight HTTP PUT backup to the S3-compatible service using pure Go (saving ~30MB memory vs AWS SDK).
func uploadBackupToS3(dbPath string, cfg BackupConfig) error {
	tempBackupPath := dbPath + ".backup"
	defer os.Remove(tempBackupPath)

	// Execute VACUUM INTO to safely checkpoint WAL frames and create a transactionally consistent copy of the active DB
	dbConn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return fmt.Errorf("failed to open database connection for backup: %w", err)
	}
	defer dbConn.Close()

	_, err = dbConn.Exec(fmt.Sprintf("VACUUM INTO '%s';", tempBackupPath))
	if err != nil {
		return fmt.Errorf("VACUUM INTO execution failed: %w", err)
	}

	file, err := os.Open(tempBackupPath)
	if err != nil {
		return fmt.Errorf("failed to open backup database file: %w", err)
	}
	defer file.Close()

	// Get file size
	fileInfo, err := file.Stat()
	if err != nil {
		return fmt.Errorf("failed to read file size: %w", err)
	}
	fileSize := fileInfo.Size()

	targetObject := "reguant_backup.db"
	url := fmt.Sprintf("%s/%s/%s", cfg.S3Endpoint, cfg.BucketName, targetObject)

	req, err := http.NewRequest("PUT", url, file)
	if err != nil {
		return fmt.Errorf("failed to create PUT request: %w", err)
	}

	req.ContentLength = fileSize
	req.Header.Set("Content-Type", "application/octet-stream")

	// Apply light HMAC authorization if AccessKey is provided
	now := time.Now().UTC().Format(time.RFC1123)
	req.Header.Set("Date", now)

	if cfg.AccessKey != "" && cfg.SecretKey != "" {
		signature := generateHMACSignature(now, cfg.SecretKey)
		req.Header.Set("Authorization", fmt.Sprintf("ReguantAuth %s:%s", cfg.AccessKey, signature))
	}

	client := &http.Client{
		Timeout: 2 * time.Minute,
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("http upload request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("S3 upload returned bad status (%d): %s", resp.StatusCode, string(body))
	}

	return nil
}

func generateHMACSignature(message string, secret string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(message))
	return hex.EncodeToString(h.Sum(nil))
}
