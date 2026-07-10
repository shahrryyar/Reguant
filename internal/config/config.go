package config

import (
	"fmt"
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
	SSLEmail                string
	GitHubOAuthClientID     string
	GitHubOAuthClientSecret string
	GitHubAllowedUsers      string
	RequireCISuccess        bool
	GitHubAPIToken          string
	S3Endpoint              string
	S3Bucket                string
	TrustProxyHeaders       bool
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
		SSLEmail:                getEnv("REGUANT_SSL_EMAIL", ""),
		GitHubOAuthClientID:     getEnv("REGUANT_GITHUB_OAUTH_CLIENT_ID", ""),
		GitHubOAuthClientSecret: getEnv("REGUANT_GITHUB_OAUTH_CLIENT_SECRET", ""),
		GitHubAllowedUsers:      getEnv("REGUANT_GITHUB_ALLOWED_USERS", ""),
		RequireCISuccess:        getEnv("REGUANT_REQUIRE_CI_SUCCESS", "") == "true",
		GitHubAPIToken:          getEnv("REGUANT_GITHUB_API_TOKEN", ""),
		S3Endpoint:              getEnv("REGUANT_S3_ENDPOINT", ""),
		S3Bucket:                getEnv("REGUANT_S3_BUCKET", ""),
		TrustProxyHeaders:       getEnv("REGUANT_TRUST_PROXY_HEADERS", "") == "true",
	}
}

func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}

// Validate returns non-fatal warnings about a questionable configuration.
func (c *Config) Validate() []string {
	var warns []string
	if c.APIToken != "" && len(c.APIToken) < 16 {
		warns = append(warns, "REGUANT_API_TOKEN is shorter than 16 chars; use a long random secret")
	}
	if c.APIToken == "" {
		warns = append(warns, "REGUANT_API_TOKEN is unset; API/terminal/webhook are UNAUTHENTICATED")
	}
	if c.WebhookSecret == "" {
		warns = append(warns, "REGUANT_GITHUB_WEBHOOK_SECRET is unset; webhook deploys are unauthenticated")
	}
	return warns
}

// Fatal returns an error for configurations that cannot work correctly.
func (c *Config) Fatal() error {
	if c.S3Bucket != "" && c.S3Endpoint == "" {
		return fmt.Errorf("REGUANT_S3_BUCKET is set but REGUANT_S3_ENDPOINT is empty")
	}
	if (c.GitHubOAuthClientID == "") != (c.GitHubOAuthClientSecret == "") {
		return fmt.Errorf("REGUANT_GITHUB_OAUTH_CLIENT_ID and _SECRET must be set together")
	}
	return nil
}
