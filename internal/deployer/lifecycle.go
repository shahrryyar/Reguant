package deployer

import (
	"database/sql"
	"fmt"
	"log"
)

// Stop halts a running app (systemctl stop, or docker stop) and marks it
// 'stopped'. A missing backing service is logged but not fatal so the record
// stays consistent.
func (d *Deployer) Stop(appID string) error {
	app, err := d.getApp(appID)
	if err != nil {
		return err
	}
	serviceName := fmt.Sprintf("reguant-%s", appID)
	if app.BuildType == "docker" {
		if out, e := ExecCommand("docker", "stop", serviceName).CombinedOutput(); e != nil {
			log.Printf("WARN: docker stop %s failed: %v (%s)", serviceName, e, out)
		}
	} else {
		if out, e := ExecCommand("systemctl", "stop", serviceName).CombinedOutput(); e != nil {
			log.Printf("WARN: systemctl stop %s failed: %v (%s)", serviceName, e, out)
		}
	}
	_, err = d.db.Exec("UPDATE applications SET status = 'stopped' WHERE id = ?", appID)
	return err
}

// Start brings a stopped app back up (systemctl start, or docker start) and
// marks it 'running'.
func (d *Deployer) Start(appID string) error {
	app, err := d.getApp(appID)
	if err != nil {
		return err
	}
	serviceName := fmt.Sprintf("reguant-%s", appID)
	if app.BuildType == "docker" {
		if out, e := ExecCommand("docker", "start", serviceName).CombinedOutput(); e != nil {
			log.Printf("WARN: docker start %s failed: %v (%s)", serviceName, e, out)
		}
	} else {
		if out, e := ExecCommand("systemctl", "start", serviceName).CombinedOutput(); e != nil {
			log.Printf("WARN: systemctl start %s failed: %v (%s)", serviceName, e, out)
		}
	}
	_, err = d.db.Exec("UPDATE applications SET status = 'running' WHERE id = ?", appID)
	return err
}

// Restart stops then starts the app.
func (d *Deployer) Restart(appID string) error {
	if err := d.Stop(appID); err != nil {
		return err
	}
	return d.Start(appID)
}

// getApp re-reads an application row from the database.
func (d *Deployer) getApp(appID string) (Application, error) {
	var app Application
	var sslVal int
	const q = `SELECT id, name, git_repo, git_branch, build_type, COALESCE(build_command, ''), COALESCE(run_command, ''), port, COALESCE(domain, ''), ssl_enabled, env_vars, status
		FROM applications WHERE id = ?`
	err := d.db.QueryRow(q, appID).Scan(
		&app.ID, &app.Name, &app.GitRepo, &app.GitBranch, &app.BuildType,
		&app.BuildCommand, &app.RunCommand, &app.Port, &app.Domain, &sslVal,
		&app.EnvVars, &app.Status,
	)
	if err == sql.ErrNoRows {
		return app, fmt.Errorf("application not found")
	}
	if err == nil {
		app.SSLEnabled = (sslVal == 1)
	}
	return app, err
}
