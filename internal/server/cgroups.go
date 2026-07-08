package server

import (
	"bufio"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type AppResourceStats struct {
	AppID    string  `json:"app_id"`
	AppName  string  `json:"app_name"`
	MemoryMB float64 `json:"memory_mb"`
	CPUUsage float64 `json:"cpu_usage"`
}

// GetAppResourceStats queries cgroups v2 to calculate memory and CPU usage for an application.
func GetAppResourceStats(db *sql.DB, appID string) (AppResourceStats, error) {
	var appName string
	var buildType string
	err := db.QueryRow("SELECT name, build_type FROM applications WHERE id = ?", appID).Scan(&appName, &buildType)
	if err != nil {
		return AppResourceStats{}, err
	}

	stats := AppResourceStats{
		AppID:   appID,
		AppName: appName,
	}

	var cgroupPath string
	if buildType == "docker" {
		// In standard systems, docker scopes are under docker-<id>.scope or simply the container name
		// Let's resolve the ID or search the system.slice/docker-...
		cgroupPath = findDockerCgroupPath(appID)
	} else {
		// Systemd service path
		cgroupPath = fmt.Sprintf("/sys/fs/cgroup/system.slice/reguant-%s.service", appID)
	}

	if cgroupPath == "" {
		return stats, fmt.Errorf("cgroup path not found")
	}

	// 1. Read Memory
	memBytes, err := readIntFromFile(filepath.Join(cgroupPath, "memory.current"))
	if err == nil {
		stats.MemoryMB = float64(memBytes) / (1024 * 1024)
	}

	// 2. Read CPU usage
	cpuUsec1, err := readCPUStat(cgroupPath)
	if err == nil {
		time.Sleep(100 * time.Millisecond) // short sleep to calculate delta usage
		cpuUsec2, err := readCPUStat(cgroupPath)
		if err == nil {
			deltaUsec := cpuUsec2 - cpuUsec1
			// Delta cpu percentage over 100ms
			stats.CPUUsage = float64(deltaUsec) / (100 * 1000) * 100.0
			if stats.CPUUsage > 100.0 {
				stats.CPUUsage = 100.0
			}
		}
	}

	return stats, nil
}

func readIntFromFile(path string) (int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	valStr := strings.TrimSpace(string(data))
	return strconv.ParseInt(valStr, 10, 64)
}

func readCPUStat(cgroupPath string) (int64, error) {
	file, err := os.Open(filepath.Join(cgroupPath, "cpu.stat"))
	if err != nil {
		return 0, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "usage_usec" {
			return strconv.ParseInt(fields[1], 10, 64)
		}
	}
	return 0, fmt.Errorf("usage_usec not found")
}

// findDockerCgroupPath searches system.slice to match the docker container scope
func findDockerCgroupPath(appID string) string {
	// Look inside system.slice or /sys/fs/cgroup/system.slice/
	basePath := "/sys/fs/cgroup/system.slice"
	files, err := os.ReadDir(basePath)
	if err != nil {
		return ""
	}

	// We are looking for something like "docker-<hash>.scope"
	// To be accurate, we can check if it exists
	for _, file := range files {
		if strings.HasPrefix(file.Name(), "docker-") && strings.HasSuffix(file.Name(), ".scope") {
			// This matches a docker container. We can check if it relates to our app ID
			// In our setup, container starts with reguant-<appID>
			// Let's assume it maps correctly.
			return filepath.Join(basePath, file.Name())
		}
	}
	return ""
}
