package objectstore

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The published example of the AWS Signature Version 4 documentation (GET https://iam.amazonaws.com/?Action=ListUsers&Version=2010-05-08).
func TestSignV4MatchesTheAWSDocumentationExample(t *testing.T) {
	headers := map[string]string{
		"content-type": "application/x-www-form-urlencoded; charset=utf-8",
		"host":         "iam.amazonaws.com",
		"x-amz-date":   "20150830T123600Z",
	}
	const emptyHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	sig, signed, scope := signV4("GET", "/", "Action=ListUsers&Version=2010-05-08", headers, emptyHash, "us-east-1", "iam", "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", "20150830T123600Z")
	if sig != "5d672d79c15b13162d9279b0855cfba6789a8edb4c82c400e06b5924a6f2b5d7" || signed != "content-type;host;x-amz-date" || scope != "20150830/us-east-1/iam/aws4_request" {
		t.Fatalf("signature %s signed %s scope %s", sig, signed, scope)
	}
}

func contract(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.Get(ctx, "t1/wo1/c1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing key: %v", err)
	}
	if err := s.Put(ctx, "t1/wo1/c1", "image/png", []byte("png-bytes")); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, "t1/wo1/c1", "image/png", []byte("png-bytes")); err != nil {
		t.Fatalf("writing the same key again: %v", err)
	}
	if b, err := s.Get(ctx, "t1/wo1/c1"); err != nil || string(b) != "png-bytes" {
		t.Fatalf("get %q %v", b, err)
	}
	if err := s.Delete(ctx, "t1/wo1/c1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "t1/wo1/c1"); err != nil {
		t.Fatalf("deleting a missing key: %v", err)
	}
	if _, err := s.Get(ctx, "t1/wo1/c1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}

func TestMemoryAndFSKeepTheSameContract(t *testing.T) {
	contract(t, NewMemory())
	dir := t.TempDir()
	fsStore, err := NewFS(filepath.Join(dir, "captures"))
	if err != nil {
		t.Fatal(err)
	}
	contract(t, fsStore)
	for _, bad := range []string{"", "/abs", "a/../b", "a//b", "a/./b", `a\b`, "a?b", strings.Repeat("k", 513)} {
		if err := fsStore.Put(context.Background(), bad, "", []byte("x")); err == nil {
			t.Errorf("key %q was accepted", bad)
		}
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "captures")); len(entries) != 1 {
		// only the t1 directory: nothing escaped the root and no temp file is left behind
		t.Fatalf("root holds %v", entries)
	}
}

type fakeS3 struct {
	objects map[string][]byte
	auth    []string
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	body, _ := io.ReadAll(r.Body)
	if r.Header.Get("x-amz-content-sha256") == "" || r.Header.Get("x-amz-date") == "" {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	switch r.Method {
	case http.MethodPut:
		f.objects[r.URL.Path] = body
	case http.MethodGet:
		b, ok := f.objects[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(b)
	case http.MethodDelete:
		delete(f.objects, r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	}
}

func TestS3SignsPathStyleRequestsAndKeepsTheContract(t *testing.T) {
	f := &fakeS3{objects: map[string][]byte{}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	s, err := NewS3(Config{Kind: "s3", Endpoint: srv.URL, Bucket: "captures", Region: "us-east-1", AccessKey: "AKID", SecretKey: "secret"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC) }
	contract(t, s)
	if len(f.auth) == 0 || !strings.HasPrefix(f.auth[0], "AWS4-HMAC-SHA256 Credential=AKID/20261111/us-east-1/s3/aws4_request, SignedHeaders=") || !strings.Contains(f.auth[0], "Signature=") {
		t.Fatalf("authorization %q", f.auth)
	}
	if err := s.Put(context.Background(), "t/wo/c", "image/png", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.objects["/captures/t/wo/c"]; !ok {
		t.Fatalf("objects %v; want the path-style key under the bucket", f.objects)
	}
}

func TestS3SurfacesAServerFailureAndRefusesPlainHTTPToAHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer srv.Close()
	s, err := NewS3(Config{Endpoint: srv.URL, Bucket: "b", Region: "r", AccessKey: "a", SecretKey: "s"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(context.Background(), "k/a", "", []byte("x")); err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("put: %v", err)
	}
	if _, err := s.Get(context.Background(), "k/a"); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("a 500 is not a missing object: %v", err)
	}
	for _, endpoint := range []string{"http://s3.example.com", "s3.example.com", "https://", "https://u:p@s3.example.com"} {
		if _, err := NewS3(Config{Endpoint: endpoint, Bucket: "b", Region: "r", AccessKey: "a", SecretKey: "s"}, nil); err == nil {
			t.Errorf("endpoint %q was accepted", endpoint)
		}
	}
	if _, err := NewS3(Config{Endpoint: "https://s3.example.com", Bucket: "b"}, nil); err == nil {
		t.Error("missing credentials were accepted")
	}
}

func TestFromEnv(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if s, err := FromEnv(env(nil)); s != nil || err != nil {
		t.Fatalf("unset keeps captures in Postgres: %v %v", s, err)
	}
	if _, err := FromEnv(env(map[string]string{"FIELDSERVICE_CAPTURE_STORE": "gcs"})); err == nil {
		t.Fatal("an unknown kind must be refused")
	}
	if _, err := FromEnv(env(map[string]string{"FIELDSERVICE_CAPTURE_STORE": "fs"})); err == nil {
		t.Fatal("fs without a directory must be refused")
	}
	if s, err := FromEnv(env(map[string]string{"FIELDSERVICE_CAPTURE_STORE": "fs", "FIELDSERVICE_CAPTURE_DIR": t.TempDir()})); err != nil || s == nil {
		t.Fatalf("fs: %v %v", s, err)
	}
	if got := Key("T1", "WO", "C"); got != "t1/wo/c" {
		t.Fatalf("key %q", got)
	}
}
