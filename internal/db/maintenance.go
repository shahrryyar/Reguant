package db

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

// StartMaintenanceScheduler starts a daily ticker to prune build logs and vacuum the database.
func StartMaintenanceScheduler(ctx context.Context, db *sql.DB, interval time.Duration) {
	log.Printf("Starting daily database maintenance scheduler. Interval: %v", interval)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("Stopping database maintenance scheduler...")
			return
		case <-ticker.C:
			if err := pruneBuildLogs(db); err != nil {
				log.Printf("[Maintenance Error]: Failed to prune logs: %v", err)
			}
			if err := vacuumDatabase(db); err != nil {
				log.Printf("[Maintenance Error]: Failed to vacuum: %v", err)
			}
		}
	}
}

// RestoreS3Backup downloads the latest database backup file from S3/R2 and overwrites the local db file.
func RestoreS3Backup(dbPath string, s3Endpoint, bucketName, accessKey, secretKey string) error {
	if s3Endpoint == "" || bucketName == "" {
		return fmt.Errorf("S3 restore requires S3 endpoint and bucket configuration")
	}

	targetObject := "reguant_backup.db"
	url := fmt.Sprintf("%s/%s/%s", s3Endpoint, bucketName, targetObject)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create GET request: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC1123)
	req.Header.Set("Date", now)
	if accessKey != "" && secretKey != "" {
		signature := generateHMACSignature(now, secretKey)
		req.Header.Set("Authorization", fmt.Sprintf("ReguantAuth %s:%s", accessKey, signature))
	}

	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to execute backup download request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("S3 download returned bad status (%d): %s", resp.StatusCode, string(body))
	}

	// Write temp file first
	tempPath := dbPath + ".tmp"
	tempFile, err := os.OpenFile(tempPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("failed to create temp database file: %w", err)
	}
	defer tempFile.Close()

	if _, err := io.Copy(tempFile, resp.Body); err != nil {
		return fmt.Errorf("failed to write database content: %w", err)
	}

	// Rename temp file to target path
	if err := os.Rename(tempPath, dbPath); err != nil {
		return fmt.Errorf("failed to replace target database: %w", err)
	}

	log.Println("Database restored from offsite S3 backup successfully.")
	return nil
}

func pruneBuildLogs(db *sql.DB) error {
	log.Println("Running log pruning routine...")

	// Delete deployments that are not in the top 50 for each application
	pruneQuery := `
	DELETE FROM deployments 
	WHERE id NOT IN (
		SELECT d.id FROM deployments d
		JOIN (
			SELECT id, ROW_NUMBER() OVER (PARTITION BY application_id ORDER BY started_at DESC) as row_num
			FROM deployments
		) r ON d.id = r.id
		WHERE r.row_num <= 50
	);`

	_, err := db.Exec(pruneQuery)
	if err != nil {
		return err
	}

	log.Println("Deployment build logs successfully pruned to last 50 records per app.")
	return nil
}

func vacuumDatabase(db *sql.DB) error {
	log.Println("Vacuuming database to reclaim disk space...")
	_, err := db.Exec("VACUUM;")
	return err
}
