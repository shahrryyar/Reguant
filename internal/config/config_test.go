package config

import "testing"

func TestValidateWarnsAndFails(t *testing.T) {
	// Short token is a warning, not fatal.
	c := &Config{APIToken: "short"}
	if len(c.Validate()) == 0 {
		t.Error("expected a warning for short API token")
	}

	// Bucket without endpoint is fatal.
	c = &Config{S3Bucket: "b"}
	if c.Fatal() == nil {
		t.Error("expected fatal error for S3 bucket without endpoint")
	}

	// OAuth client id without secret is fatal.
	c = &Config{GitHubOAuthClientID: "id"}
	if c.Fatal() == nil {
		t.Error("expected fatal error for OAuth client id without secret")
	}

	// Fully empty (open mode) is not fatal.
	c = &Config{}
	if err := c.Fatal(); err != nil {
		t.Errorf("empty config should not be fatal, got %v", err)
	}
}
