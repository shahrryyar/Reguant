package config

import (
	"os"
)

type Config struct {
	ServerPort string
	DBPath     string
	AppsDir    string
	LogsDir    string
	NginxDir   string
}

func Load() *Config {
	return &Config{
		ServerPort: getEnv("REGUANT_PORT", "9000"),
		DBPath:     getEnv("REGUANT_DB_PATH", "/var/lib/reguant/reguant.db"),
		AppsDir:    getEnv("REGUANT_APPS_DIR", "/var/lib/reguant/apps"),
		LogsDir:    getEnv("REGUANT_LOGS_DIR", "/var/lib/reguant/logs"),
		NginxDir:   getEnv("REGUANT_NGINX_DIR", "/etc/nginx/sites-enabled"),
	}
}

func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}
