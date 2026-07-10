package server

import (
	"database/sql"
	"encoding/json"
	"net/http"
)

type deploymentSummary struct {
	ID            string         `json:"id"`
	CommitHash    sql.NullString `json:"-"`
	CommitMessage sql.NullString `json:"-"`
	Hash          string         `json:"commit_hash"`
	Message       string         `json:"commit_message"`
	Status        string         `json:"status"`
	StartedAt     string         `json:"started_at"`
}

// REST: GET /api/apps/deployments?app_id=xxx
func (s *Server) handleListDeployments(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	appID := r.URL.Query().Get("app_id")
	if appID == "" {
		http.Error(w, "Missing app_id parameter", http.StatusBadRequest)
		return
	}
	rows, err := s.db.Query(`
		SELECT id, commit_hash, commit_message, status, started_at
		FROM deployments WHERE application_id = ?
		ORDER BY started_at DESC LIMIT 50`, appID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	out := make([]deploymentSummary, 0)
	for rows.Next() {
		var d deploymentSummary
		if err := rows.Scan(&d.ID, &d.CommitHash, &d.CommitMessage, &d.Status, &d.StartedAt); err == nil {
			d.Hash = d.CommitHash.String
			d.Message = d.CommitMessage.String
			out = append(out, d)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// REST: POST /api/apps/rollback?app_id=xxx&commit=<sha>
func (s *Server) handleRollback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	appID := r.URL.Query().Get("app_id")
	commit := r.URL.Query().Get("commit")
	if appID == "" || commit == "" {
		http.Error(w, "Missing app_id or commit parameter", http.StatusBadRequest)
		return
	}
	depID, err := s.deployer.Rollback(appID, commit)
	if err != nil {
		http.Error(w, "Rollback failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"deployment_id":"` + depID + `","status":"queued"}`))
}
