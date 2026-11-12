package httpx

import (
	"bytes"
	"context"
	"encoding/base64"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/cloudprofile"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// Under the eu-data-boundary overlay Serve wraps the default logger, so the request log and a handler's own log lines
// carry no raw email or name by the time they would be exported to Loki (#605).
func TestServePseudonymizesLogsUnderTheEUDataBoundary(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	var out bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&out, nil)))
	t.Setenv(cloudprofile.EnvProfile, "commercial+eu-data-boundary")
	t.Setenv("FIELDCRYPT_KEYS", "k1="+base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)))
	t.Setenv("FIELDCRYPT_CURRENT_KEY_ID", "k1")

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := Serve(ctx, "127.0.0.1:0", http.NotFoundHandler()); err != nil {
		t.Fatal(err)
	}

	r := NewRouterWithoutTenant()
	r.Get("/v1/people/{email}", func(w http.ResponseWriter, req *http.Request) {
		slog.InfoContext(tenant.With(req.Context(), tenant.Principal{TenantID: "11111111-1111-4111-8111-111111111111"}),
			"looked up", "user", "Jane Doe", "email", "jane.doe@example.com")
		w.WriteHeader(http.StatusNoContent)
	})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/people/jane.doe@example.com", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d", rec.Code)
	}
	logged := out.String()
	for _, raw := range []string{"jane.doe", "Jane", "Doe", "example.com"} {
		if strings.Contains(logged, raw) {
			t.Errorf("log output contains %q:\n%s", raw, logged)
		}
	}
	if !strings.Contains(logged, "pn_") || !strings.Contains(logged, `"path":"/v1/people/`) {
		t.Errorf("want pseudonyms and the request log line:\n%s", logged)
	}
}

func TestServeLeavesLogsAloneOutsideTheEUDataBoundary(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	var out bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&out, nil)))
	t.Setenv(cloudprofile.EnvProfile, "commercial")
	t.Setenv("LOG_PSEUDONYMIZE", "")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := Serve(ctx, "127.0.0.1:0", http.NotFoundHandler()); err != nil {
		t.Fatal(err)
	}
	slog.Info("x", "email", "jane.doe@example.com")
	if !strings.Contains(out.String(), "jane.doe@example.com") {
		t.Fatalf("a commercial deployment's logs are not rewritten: %s", out.String())
	}
}
