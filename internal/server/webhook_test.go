package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestVerifyGitHubSignature(t *testing.T) {
	secret := "topsecret"
	body := []byte(`{"ref":"refs/heads/main"}`)

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	valid := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	if !verifyGitHubSignature(secret, valid, body) {
		t.Fatal("expected a correctly signed body to verify")
	}
	if verifyGitHubSignature(secret, valid, []byte(`{"ref":"refs/heads/other"}`)) {
		t.Fatal("a tampered body must not verify")
	}
	if verifyGitHubSignature(secret, "sha1=deadbeef", body) {
		t.Fatal("a wrong algorithm prefix must not verify")
	}
	if verifyGitHubSignature(secret, "", body) {
		t.Fatal("a missing signature must not verify")
	}
	if verifyGitHubSignature("wrong-secret", valid, body) {
		t.Fatal("a wrong secret must not verify")
	}
}
