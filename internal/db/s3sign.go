package db

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// newSignedRequest builds an HTTP request against an S3-compatible endpoint.
//
// Authentication preference:
//   - If apiToken is set, an R2 API-token Bearer header is used (Cloudflare R2).
//   - Otherwise AWS Signature Version 4 is computed from access/secret keys
//     (works for AWS S3, Cloudflare R2 S3 API, and Backblaze B2 S3-compatible).
//
// The bucket is addressed in virtual-hosted style (https://<bucket>.<host>/<key>),
// which is what AWS S3 requires and R2/B2 also accept.
func newSignedRequest(method, endpoint, region, bucket, objectKey, accessKey, secretKey, apiToken string, body io.Reader, contentLength int64) (*http.Request, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("invalid S3 endpoint: %w", err)
	}
	scheme := u.Scheme
	if scheme == "" {
		scheme = "https"
	}
	host := u.Host
	if host == "" {
		host = endpoint // endpoint passed without scheme
	}

	reqURL := fmt.Sprintf("%s://%s.%s/%s", scheme, bucket, host, encodeS3Path(objectKey))
	req, err := http.NewRequest(method, reqURL, body)
	if err != nil {
		return nil, fmt.Errorf("failed to build S3 request: %w", err)
	}
	req.ContentLength = contentLength
	req.Host = req.URL.Host

	if apiToken != "" {
		req.Header.Set("Authorization", "Bearer "+apiToken)
		return req, nil
	}

	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	const hashedPayload = "UNSIGNED-PAYLOAD"
	req.Header.Set("x-amz-content-sha256", hashedPayload)
	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("host", req.Host)
	req.Header.Set("Content-Type", "application/octet-stream")

	signedHeaders := []string{"host", "x-amz-content-sha256", "x-amz-date"}
	var canonicalHeaders strings.Builder
	for _, h := range signedHeaders {
		canonicalHeaders.WriteString(h)
		canonicalHeaders.WriteString(":")
		canonicalHeaders.WriteString(strings.TrimSpace(req.Header.Get(h)))
		canonicalHeaders.WriteString("\n")
	}

	canonicalURI := "/" + encodeS3Path(objectKey)
	canonicalRequest := strings.Join([]string{
		method,
		canonicalURI,
		"", // canonical query string (none)
		canonicalHeaders.String(),
		strings.Join(signedHeaders, ";"),
		hashedPayload,
	}, "\n")

	credentialScope := fmt.Sprintf("%s/%s/s3/aws4_request", dateStamp, region)
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		credentialScope,
		hexSHA256(canonicalRequest),
	}, "\n")

	signingKey := getSignatureKey(secretKey, dateStamp, region, "s3")
	signature := hex.EncodeToString(hmacSHA256(signingKey, stringToSign))

	authHeader := fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		accessKey, credentialScope, strings.Join(signedHeaders, ";"), signature)
	req.Header.Set("Authorization", authHeader)

	return req, nil
}

// encodeS3Path percent-encodes a path segment per RFC 3986 (unreserved chars pass through).
func encodeS3Path(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func hexSHA256(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key []byte, s string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(s))
	return h.Sum(nil)
}

func getSignatureKey(secret, dateStamp, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), dateStamp)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	return hmacSHA256(kService, "aws4_request")
}
