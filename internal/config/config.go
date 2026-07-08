package config

import (
	"os"
)

type Config struct {
	ServerPort              string
	DBPath                  string
	AppsDir                 string
	LogsDir                 string
	NginxDir                string
	WebhookSecret           string
	S3Region                string
	APIToken                string
	CORSOrigin              string
	GitHubOAuthClientID     string
	GitHubOAuthClientSecret string
}

func Load() *Config {
	return &Config{
		ServerPort:              getEnv("REGUANT_PORT", "9000"),
		DBPath:                  getEnv("REGUANT_DB_PATH", "/var/lib/reguant/reguant.db"),
		AppsDir:                 getEnv("REGUANT_APPS_DIR", "/var/lib/reguant/apps"),
		LogsDir:                 getEnv("REGUANT_LOGS_DIR", "/var/lib/reguant/logs"),
		NginxDir:                getEnv("REGUANT_NGINX_DIR", "/etc/nginx/sites-enabled"),
		WebhookSecret:           getEnv("REGUANT_GITHUB_WEBHOOK_SECRET", ""),
		S3Region:                getEnv("REGUANT_S3_REGION", "auto"),
		APIToken:                getEnv("REGUANT_API_TOKEN", ""),
		CORSOrigin:              getEnv("REGUANT_CORS_ORIGIN", ""),
		GitHubOAuthClientID:     getEnv("REGUANT_GITHUB_OAUTH_CLIENT_ID", ""),
		GitHubOAuthClientSecret: getEnv("REGUANT_GITHUB_OAUTH_CLIENT_SECRET", ""),
	}
}

func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}
