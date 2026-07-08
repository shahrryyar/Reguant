package deployer

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"time"
)

// StartAppMonitorScheduler runs a periodic loop checking the HTTP health of all active deployments.
func StartAppMonitorScheduler(ctx context.Context, db *sql.DB, interval time.Duration) {
	log.Printf("Starting active application status health checks. Interval: %v", interval)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("Stopping app health monitor...")
			return
		case <-ticker.C:
			monitorHealth(db)
		}
	}
}

func monitorHealth(db *sql.DB) {
	rows, err := db.Query("SELECT id, name, port, domain, status FROM applications WHERE status IN ('running', 'offline', 'failed')")
	if err != nil {
		log.Printf("[Monitor Error]: Failed to fetch apps: %v", err)
		return
	}
	defer rows.Close()

	client := &http.Client{
		Timeout: 3 * time.Second,
	}

	for rows.Next() {
		var id, name, status string
		var port int
		var domain sql.NullString

		if err := rows.Scan(&id, &name, &port, &domain, &status); err != nil {
			continue
		}

		// Build target URL
		targetURL := fmt.Sprintf("http://127.0.0.1:%d", port)
		if domain.Valid && domain.String != "" {
			targetURL = fmt.Sprintf("http://%s", domain.String)
		}

		// Perform HTTP GET ping
		resp, err := client.Get(targetURL)
		isUp := err == nil
		if isUp {
			resp.Body.Close()
		}

		// Determine new status
		newStatus := status
		if isUp {
			if status != "running" {
				newStatus = "running"
				log.Printf("[Monitor]: Application '%s' came back online.", name)
			}
		} else {
			if status == "running" {
				newStatus = "offline"
				log.Printf("[Monitor Warning]: Application '%s' is unreachable at %s: %v", name, targetURL, err)
			}
		}

		// Update database if status changed
		if newStatus != status {
			_, err = db.Exec("UPDATE applications SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?", newStatus, id)
			if err != nil {
				log.Printf("[Monitor Error]: Failed to update DB status for '%s': %v", name, err)
			}
		}
	}
}
