package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/shahrryyar/reguant/internal/config"
	"github.com/shahrryyar/reguant/internal/db"
	"github.com/shahrryyar/reguant/internal/deployer"
	"github.com/shahrryyar/reguant/internal/server"
)

func main() {
	cfg := config.Load()

	// 1. Parse CLI flags
	restoreFlag := flag.Bool("restore", false, "Restore the SQLite database from offsite S3/R2 backup and exit")
	flag.Parse()

	// Retrieve S3 Credentials
	s3Endpoint := os.Getenv("REGUANT_S3_ENDPOINT")
	s3Bucket := os.Getenv("REGUANT_S3_BUCKET")
	s3Access := os.Getenv("REGUANT_S3_ACCESS_KEY")
	s3Secret := os.Getenv("REGUANT_S3_SECRET_KEY")

	// Trigger restore operation and exit if requested
	if *restoreFlag {
		log.Println("Initializing database restore from offsite S3/R2 backup...")
		if err := db.RestoreS3Backup(cfg.DBPath, s3Endpoint, s3Bucket, cfg.S3Region, s3Access, s3Secret, cfg.APIToken); err != nil {
			log.Fatalf("Critical Error: Database restore failed: %v", err)
		}
		log.Println("Database successfully restored! Exiting.")
		return
	}

	// Ensure system directories exist
	if err := os.MkdirAll(cfg.AppsDir, 0755); err != nil {
		log.Fatalf("Failed to create apps directory: %v", err)
	}
	if err := os.MkdirAll(cfg.LogsDir, 0755); err != nil {
		log.Fatalf("Failed to create logs directory: %v", err)
	}

	// Initialize SQLite database connection
	database, err := db.Init(cfg.DBPath)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer database.Close()

	// Parse replication settings
	intervalStr := os.Getenv("REGUANT_S3_INTERVAL_MINUTES")
	backupInterval := 10 * time.Minute
	if intervalStr != "" {
		if mins, err := strconv.Atoi(intervalStr); err == nil && mins > 0 {
			backupInterval = time.Duration(mins) * time.Minute
		}
	}

	// Root context cancelled on SIGINT/SIGTERM so background schedulers stop cleanly.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Start background S3 replication scheduler
	if s3Endpoint != "" && s3Bucket != "" {
		backupCfg := db.BackupConfig{
			S3Endpoint: s3Endpoint,
			BucketName: s3Bucket,
			AccessKey:  s3Access,
			SecretKey:  s3Secret,
			Region:     cfg.S3Region,
			APIToken:   cfg.APIToken,
			Interval:   backupInterval,
		}
		go db.StartBackupScheduler(ctx, cfg.DBPath, backupCfg)
	}

	// Start background database maintenance (log pruning and database vacuum)
	go db.StartMaintenanceScheduler(ctx, database, 24*time.Hour)

	// Start active application HTTP health monitoring
	go deployer.StartAppMonitorScheduler(ctx, database, 60*time.Second)

	// Test status endpoint
	http.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"online","framework":"go"}`))
	})

	log.Printf("Starting Reguant core backend on port %s...", cfg.ServerPort)
	if err := server.Start(":"+cfg.ServerPort, database); err != nil {
		log.Fatalf("HTTP server failure: %v", err)
	}
}
