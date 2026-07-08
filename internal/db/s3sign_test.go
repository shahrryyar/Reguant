package db

import (
	"strings"
	"testing"
)

func TestNewSignedRequestBearer(t *testing.T) {
	req, err := newSignedRequest("GET", "https://abc.r2.cloudflarestorage.com", "auto", "mybucket", "reguant_backup.db", "", "", "r2-token", nil, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer r2-token" {
		t.Fatalf("expected Bearer auth, got %q", got)
	}
	if req.URL.Host != "mybucket.abc.r2.cloudflarestorage.com" {
		t.Fatalf("expected virtual-hosted host, got %q", req.URL.Host)
	}
}

func TestNewSignedRequestSigV4(t *testing.T) {
	req, err := newSignedRequest("PUT", "https://s3.us-east-1.amazonaws.com", "us-east-1", "mybucket", "reguant_backup.db", "AKID", "secret", "", nil, 123)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.URL.Host != "mybucket.s3.us-east-1.amazonaws.com" {
		t.Fatalf("expected virtual-hosted host, got %q", req.URL.Host)
	}
	auth := req.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 ") {
		t.Fatalf("expected AWS4 auth, got %q", auth)
	}
	for _, need := range []string{"Credential=AKID/", "SignedHeaders=host;x-amz-content-sha256;x-amz-date", "Signature="} {
		if !strings.Contains(auth, need) {
			t.Fatalf("auth header missing %q: %q", need, auth)
		}
	}
	if req.Header.Get("x-amz-date") == "" {
		t.Fatal("x-amz-date header missing")
	}
	if req.Header.Get("x-amz-content-sha256") == "" {
		t.Fatal("x-amz-content-sha256 header missing")
	}
	// The same headers must be signed (host value matches the request host).
	if req.Host != req.URL.Host {
		t.Fatalf("host header %q does not match request host %q", req.Host, req.URL.Host)
	}
}
