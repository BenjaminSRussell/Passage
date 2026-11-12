package cube

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/analytics/internal/domain"
)

func query() domain.CubeQuery {
	return domain.CubeQuery{Measures: []string{"fin_journal_line.revenue"},
		TimeDimensions: []domain.CubeTimeDim{{Dimension: "fin_journal_line.posting_date", DateRange: []string{"2026-10-01", "2026-10-31"}}}}
}

func TestToken_IsAnHS256JWTWithTenantAndScope(t *testing.T) {
	c := New("http://cube", "s3cret", nil)
	c.now = func() time.Time { return time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC) }
	tok := c.Token("tenant-1", domain.Scope{LegalEntityIDs: []string{"le-1", "le-2"}})
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("token = %q", tok)
	}
	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write([]byte(parts[0] + "." + parts[1]))
	if want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil)); parts[2] != want {
		t.Fatal("signature does not verify with the secret")
	}
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims struct {
		TenantID       string   `json:"tenantId"`
		Unrestricted   bool     `json:"unrestricted"`
		LegalEntityIDs []string `json:"legalEntityIds"`
		Exp            int64    `json:"exp"`
	}
	if err := json.Unmarshal(raw, &claims); err != nil || claims.TenantID != "tenant-1" || claims.Unrestricted || len(claims.LegalEntityIDs) != 2 ||
		claims.Exp != time.Date(2026, 11, 11, 9, 2, 0, 0, time.UTC).Unix() {
		t.Fatalf("claims = %+v %v", claims, err)
	}
	// An unrestricted caller names no entities, as an array and never as null.
	raw, _ = base64.RawURLEncoding.DecodeString(strings.Split(c.Token("t", domain.Scope{Unrestricted: true}), ".")[1])
	if !strings.Contains(string(raw), `"legalEntityIds":[]`) || !strings.Contains(string(raw), `"unrestricted":true`) {
		t.Fatalf("unrestricted claims = %s", raw)
	}
}

func TestLoad_SendsTheQueryWithTheTokenAndReturnsRows(t *testing.T) {
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte(`{"data":[{"fin_journal_line.revenue":"1250.50"}]}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "s3cret", nil)
	rows, err := c.Load(context.Background(), "tenant-1", domain.Scope{Unrestricted: true}, query())
	if err != nil || len(rows) != 1 || *Text(rows[0]["fin_journal_line.revenue"]) != "1250.50" {
		t.Fatalf("rows = %v %v", rows, err)
	}
	if strings.Count(gotAuth, ".") != 2 || !strings.Contains(gotBody, `"dateRange":["2026-10-01","2026-10-31"]`) {
		t.Fatalf("auth %q body %s", gotAuth, gotBody)
	}
}

func TestLoad_WaitsForContinueWaitThenGivesUp(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 3 {
			_, _ = w.Write([]byte(`{"error":"Continue wait"}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "s", nil).WithWait(5, time.Millisecond)
	rows, err := c.Load(context.Background(), "t", domain.Scope{Unrestricted: true}, query())
	if err != nil || rows == nil || len(rows) != 0 || calls.Load() != 3 {
		t.Fatalf("rows %v err %v calls %d", rows, err, calls.Load())
	}
	always := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"error":"Continue wait"}`)) }))
	defer always.Close()
	if _, err := New(always.URL, "s", nil).WithWait(2, time.Millisecond).Load(context.Background(), "t", domain.Scope{Unrestricted: true}, query()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("endless wait: %v", err)
	}
}

func TestLoad_ErrorsAreUnavailable(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"cube error": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"error":"ANA-0102 no data scope"}`))
		},
		"500":      func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) },
		"bad json": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`<html>`)) },
	} {
		srv := httptest.NewServer(handler)
		_, err := New(srv.URL, "s", nil).Load(context.Background(), "t", domain.Scope{Unrestricted: true}, query())
		srv.Close()
		if !errors.Is(err, ErrUnavailable) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := New("http://127.0.0.1:1", "s", nil).Load(context.Background(), "t", domain.Scope{Unrestricted: true}, query()); !errors.Is(err, ErrUnavailable) {
		t.Errorf("connection refused: %v", err)
	}
}

func TestText(t *testing.T) {
	if Text(nil) != nil || *Text("12.5") != "12.5" || *Text(float64(3)) != "3" {
		t.Fatal("Text conversions")
	}
}
