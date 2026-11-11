// Package objectstore is fieldservice's port to the object store that holds the photos and signatures technicians upload
// (FR-45.8): an S3-compatible adapter for real deployments and a directory adapter for development. Keys are chosen by the
// caller (tenant, work order and capture id), so writing the same key again replaces the object with the same bytes.
package objectstore

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// ErrNotFound is returned by Get for a key that holds nothing.
var ErrNotFound = errors.New("objectstore: object not found")

// Store keeps immutable blobs by key.
type Store interface {
	Put(ctx context.Context, key, contentType string, data []byte) error
	// Get returns the bytes of key, or ErrNotFound.
	Get(ctx context.Context, key string) ([]byte, error)
	// Delete removes key; deleting a key that holds nothing is not an error.
	Delete(ctx context.Context, key string) error
}

// Config selects an adapter. Kind is "" (no object store: captures stay in Postgres), "fs" or "s3".
type Config struct {
	Kind string
	// Dir is the root of the fs adapter.
	Dir string
	// S3 settings: the endpoint (https, or http for loopback), bucket, region and static credentials.
	Endpoint  string
	Bucket    string
	Region    string
	AccessKey string
	SecretKey string
}

// FromEnv reads FIELDSERVICE_CAPTURE_STORE (fs or s3; unset keeps captures in Postgres), FIELDSERVICE_CAPTURE_DIR for fs and
// FIELDSERVICE_S3_ENDPOINT, _BUCKET, _REGION, _ACCESS_KEY and _SECRET_KEY for s3. It returns nil, nil for no object store.
func FromEnv(getenv func(string) string) (Store, error) {
	cfg := Config{
		Kind: getenv("FIELDSERVICE_CAPTURE_STORE"), Dir: getenv("FIELDSERVICE_CAPTURE_DIR"),
		Endpoint: getenv("FIELDSERVICE_S3_ENDPOINT"), Bucket: getenv("FIELDSERVICE_S3_BUCKET"), Region: getenv("FIELDSERVICE_S3_REGION"),
		AccessKey: getenv("FIELDSERVICE_S3_ACCESS_KEY"), SecretKey: getenv("FIELDSERVICE_S3_SECRET_KEY"),
	}
	return New(cfg, nil)
}

// New builds the adapter cfg names; hc may be nil for the S3 adapter's default client.
func New(cfg Config, hc HTTPDoer) (Store, error) {
	switch cfg.Kind {
	case "":
		return nil, nil
	case "fs":
		return NewFS(cfg.Dir)
	case "s3":
		return NewS3(cfg, hc)
	}
	return nil, fmt.Errorf("FIELDSERVICE_CAPTURE_STORE must be fs or s3, got %q", cfg.Kind)
}

// validKey refuses keys that could leave the store's root or confuse a URL.
func validKey(key string) error {
	if key == "" || len(key) > 512 || strings.HasPrefix(key, "/") || strings.Contains(key, "\\") || strings.ContainsAny(key, "\x00?#") {
		return fmt.Errorf("objectstore: invalid key %q", key)
	}
	for _, part := range strings.Split(key, "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("objectstore: invalid key %q", key)
		}
	}
	return nil
}

// requireHTTPS accepts an https URL, or http only for a loopback host (a local stand-in for the store).
func requireHTTPS(what, raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return nil, fmt.Errorf("%s must be an absolute https URL", what)
	}
	switch u.Scheme {
	case "https":
		return u, nil
	case "http":
		if ip := net.ParseIP(u.Hostname()); u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback()) {
			return u, nil
		}
	}
	return nil, fmt.Errorf("%s must use https (got %q)", what, u.Scheme)
}

// Key is the object key of a capture: the tenant keeps its images apart, and the id is the device's.
func Key(tenantID, workOrderID, captureID string) string {
	return strings.ToLower(tenantID) + "/" + strings.ToLower(workOrderID) + "/" + strings.ToLower(captureID)
}

// Memory is an in-memory Store for tests.
type Memory struct {
	Objects map[string][]byte
	// FailPut, when set, makes Put return it.
	FailPut error
	Puts    int
}

func NewMemory() *Memory { return &Memory{Objects: map[string][]byte{}} }

func (m *Memory) Put(_ context.Context, key, _ string, data []byte) error {
	if m.FailPut != nil {
		return m.FailPut
	}
	m.Puts++
	m.Objects[key] = append([]byte(nil), data...)
	return nil
}

func (m *Memory) Get(_ context.Context, key string) ([]byte, error) {
	b, ok := m.Objects[key]
	if !ok {
		return nil, ErrNotFound
	}
	return append([]byte(nil), b...), nil
}

func (m *Memory) Delete(_ context.Context, key string) error {
	delete(m.Objects, key)
	return nil
}

// HTTPDoer is the part of *http.Client the S3 adapter needs.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}
