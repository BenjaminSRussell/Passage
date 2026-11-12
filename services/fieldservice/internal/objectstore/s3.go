package objectstore

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	s3Timeout   = 30 * time.Second
	maxS3Answer = 8 << 20
	sigAlg      = "AWS4-HMAC-SHA256"
)

// S3 talks to an S3-compatible service (AWS S3, MinIO, Ceph) with path-style requests signed with AWS Signature Version 4.
// It uses plain net/http, so no SDK is linked in; credentials are static (a Secret in the chart).
type S3 struct {
	base      *url.URL
	bucket    string
	region    string
	accessKey string
	secretKey string
	hc        HTTPDoer
	now       func() time.Time
}

// NewS3 validates cfg and returns the adapter; hc may be nil.
func NewS3(cfg Config, hc HTTPDoer) (*S3, error) {
	base, err := requireHTTPS("FIELDSERVICE_S3_ENDPOINT", cfg.Endpoint)
	if err != nil {
		return nil, err
	}
	if cfg.Bucket == "" || cfg.Region == "" || cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, errors.New("FIELDSERVICE_S3_BUCKET, _REGION, _ACCESS_KEY and _SECRET_KEY are required when FIELDSERVICE_CAPTURE_STORE is s3")
	}
	if hc == nil {
		hc = &http.Client{Timeout: s3Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return &S3{base: base, bucket: cfg.Bucket, region: cfg.Region, accessKey: cfg.AccessKey, secretKey: cfg.SecretKey, hc: hc, now: time.Now}, nil
}

func (s *S3) objectURL(key string) (*url.URL, error) {
	if err := validKey(key); err != nil {
		return nil, err
	}
	u := *s.base
	u.Path = strings.TrimRight(s.base.Path, "/") + "/" + s.bucket + "/" + key
	u.RawPath = ""
	u.RawQuery = ""
	return &u, nil
}

func (s *S3) do(ctx context.Context, method, key, contentType string, data []byte) (*http.Response, error) {
	u, err := s.objectURL(key)
	if err != nil {
		return nil, err
	}
	var body io.Reader
	if data != nil {
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	sum := sha256.Sum256(data)
	payloadHash := hex.EncodeToString(sum[:])
	amzDate := s.now().UTC().Format("20060102T150405Z")
	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", payloadHash)
	headers := map[string]string{"host": req.URL.Host, "x-amz-date": amzDate, "x-amz-content-sha256": payloadHash}
	if contentType != "" {
		headers["content-type"] = contentType
	}
	signature, signed, scope := signV4(method, canonicalPath(req.URL), req.URL.RawQuery, headers, payloadHash, s.region, "s3", s.secretKey, amzDate)
	req.Header.Set("Authorization", fmt.Sprintf("%s Credential=%s/%s, SignedHeaders=%s, Signature=%s", sigAlg, s.accessKey, scope, signed, signature))
	return s.hc.Do(req)
}

// canonicalPath is the URI-encoded path of the request, the single encoding S3 expects.
func canonicalPath(u *url.URL) string {
	var b strings.Builder
	for i, part := range strings.Split(u.Path, "/") {
		if i > 0 {
			b.WriteByte('/')
		}
		b.WriteString(uriEscape(part))
	}
	return b.String()
}

func uriEscape(s string) string {
	const unreserved = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_.~"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(unreserved, s[i]) >= 0 {
			b.WriteByte(s[i])
		} else {
			fmt.Fprintf(&b, "%%%02X", s[i])
		}
	}
	return b.String()
}

// signV4 computes the AWS Signature Version 4 of a request: headers holds the lower-case names and values to sign, which must
// include host and x-amz-date. It returns the signature, the signed header list and the credential scope.
func signV4(method, path, rawQuery string, headers map[string]string, payloadHash, region, service, secret, amzDate string) (signature, signedHeaders, scope string) {
	names := make([]string, 0, len(headers))
	for n := range headers {
		names = append(names, n)
	}
	sort.Strings(names)
	var canonicalHeaders strings.Builder
	for _, n := range names {
		canonicalHeaders.WriteString(n + ":" + strings.Join(strings.Fields(headers[n]), " ") + "\n")
	}
	signedHeaders = strings.Join(names, ";")
	query := canonicalQuery(rawQuery)
	canonicalRequest := strings.Join([]string{method, path, query, canonicalHeaders.String(), signedHeaders, payloadHash}, "\n")
	day := amzDate[:8]
	scope = day + "/" + region + "/" + service + "/aws4_request"
	crHash := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := strings.Join([]string{sigAlg, amzDate, scope, hex.EncodeToString(crHash[:])}, "\n")
	key := hmacSHA256([]byte("AWS4"+secret), day)
	key = hmacSHA256(key, region)
	key = hmacSHA256(key, service)
	key = hmacSHA256(key, "aws4_request")
	return hex.EncodeToString(hmacSHA256(key, stringToSign)), signedHeaders, scope
}

func canonicalQuery(raw string) string {
	if raw == "" {
		return ""
	}
	pairs := strings.Split(raw, "&")
	for i, p := range pairs {
		k, v, _ := strings.Cut(p, "=")
		if dk, err := url.QueryUnescape(k); err == nil {
			k = dk
		}
		if dv, err := url.QueryUnescape(v); err == nil {
			v = dv
		}
		pairs[i] = uriEscape(k) + "=" + uriEscape(v)
	}
	sort.Strings(pairs)
	return strings.Join(pairs, "&")
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

func (s *S3) fail(op, key string, resp *http.Response) error {
	detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("s3 %s %s: status %d: %s", op, key, resp.StatusCode, strings.TrimSpace(string(detail)))
}

func (s *S3) Put(ctx context.Context, key, contentType string, data []byte) error {
	resp, err := s.do(ctx, http.MethodPut, key, contentType, data)
	if err != nil {
		return fmt.Errorf("s3 put %s: %w", key, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return s.fail("put", key, resp)
	}
	return nil
}

func (s *S3) Get(ctx context.Context, key string) ([]byte, error) {
	resp, err := s.do(ctx, http.MethodGet, key, "", nil)
	if err != nil {
		return nil, fmt.Errorf("s3 get %s: %w", key, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if resp.StatusCode/100 != 2 {
		return nil, s.fail("get", key, resp)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxS3Answer))
}

func (s *S3) Delete(ctx context.Context, key string) error {
	resp, err := s.do(ctx, http.MethodDelete, key, "", nil)
	if err != nil {
		return fmt.Errorf("s3 delete %s: %w", key, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 && resp.StatusCode != http.StatusNotFound {
		return s.fail("delete", key, resp)
	}
	return nil
}
