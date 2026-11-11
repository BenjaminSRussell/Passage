package logpseudo_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/fieldcrypt"
	"github.com/BenjaminSRussell/passage/platform/logpseudo"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	tenantA = "11111111-1111-4111-8111-111111111111"
	tenantB = "22222222-2222-4222-8222-222222222222"
)

func keys(t *testing.T) *fieldcrypt.Keys {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	ks, err := fieldcrypt.NewKeys("k1", map[string][]byte{"k1": k})
	if err != nil {
		t.Fatal(err)
	}
	return ks
}

// capture writes through the wrapper into a JSON handler over a buffer, the shape Loki receives.
func capture(mac logpseudo.MACer) (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(logpseudo.New(slog.NewJSONHandler(&buf, nil), mac)), &buf
}

func lines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("log line %q: %v", l, err)
		}
		out = append(out, m)
	}
	return out
}

func ctxOf(tenantID string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: tenantID})
}

func TestNoRawEmailOrNameReachesTheOutput(t *testing.T) {
	log, buf := capture(keys(t))
	ctx := ctxOf(tenantA)
	log.InfoContext(ctx, "sign-in of jane.doe@example.com failed",
		"user", "Jane Doe", "email", "jane.doe@example.com", "displayName", "Jane Doe", "userId", "u-123",
		"actor", "jane.doe@example.com", "remoteIp", "203.0.113.9", "name", "Jane Doe",
		"detail", "contact jane.doe@example.com or JANE.DOE@EXAMPLE.COM", "err", errors.New("no mailbox jane.doe@example.com"),
		"nested", struct{ Owner string }{"jane.doe@example.com"},
		slog.Group("person", slog.String("first", "Jane"), slog.String("last", "Doe")),
		slog.Group("req", slog.String("subject", "auth0|jane"), slog.String("path", "/users/jane.doe@example.com")),
	)
	log.With("customer_name", "Acme Jane", "email", "jane.doe@example.com").WithGroup("g").InfoContext(ctx, "again", "contactEmail", "jane.doe@example.com")
	out := buf.String()
	for _, raw := range []string{"jane.doe", "Jane", "Doe", "JANE", "example.com", "203.0.113.9", "auth0", "u-123", "Acme"} {
		if strings.Contains(out, raw) {
			t.Errorf("output contains %q:\n%s", raw, out)
		}
	}
	if !strings.Contains(out, logpseudo.Prefix) {
		t.Fatalf("no pseudonym in the output: %s", out)
	}
}

func TestSamePseudonymPerTenantDifferentAcrossTenants(t *testing.T) {
	log, buf := capture(keys(t))
	log.InfoContext(ctxOf(tenantA), "a1", "email", "Jane@Example.com")
	log.InfoContext(ctxOf(tenantA), "a2", "email", "jane@example.com ", "user", "jane@example.com")
	log.InfoContext(ctxOf(tenantB), "b1", "email", "jane@example.com")
	got := lines(t, buf)
	a1, a2, b1 := got[0]["email"], got[1]["email"], got[2]["email"]
	if a1 != a2 {
		t.Errorf("same email in tenant A: %v then %v", a1, a2)
	}
	if a1 == b1 {
		t.Errorf("tenant A and B share the pseudonym %v", a1)
	}
	if got[1]["user"] != a1 {
		t.Errorf("a value under key user and under key email differ: %v %v", got[1]["user"], a1)
	}
	if !strings.HasPrefix(a1.(string), logpseudo.Prefix) {
		t.Errorf("pseudonym %v has no prefix", a1)
	}
}

func TestTenantFromAttributeWhenTheCallCarriesNoContext(t *testing.T) {
	log, buf := capture(keys(t))
	log.Info("a", "tenantId", tenantA, "email", "x@example.com")
	log.With("tenant_id", tenantA).Info("b", "email", "x@example.com")
	log.With("tenant_id", tenantB).Info("c", "email", "x@example.com")
	got := lines(t, buf)
	if got[0]["email"] != got[1]["email"] || got[0]["email"] == got[2]["email"] {
		t.Fatalf("pseudonyms = %v %v %v", got[0]["email"], got[1]["email"], got[2]["email"])
	}
	if got[0]["tenantId"] != tenantA {
		t.Fatalf("the tenant attribute itself must pass through: %v", got[0])
	}
}

func TestUnrelatedAttributesPassThrough(t *testing.T) {
	log, buf := capture(keys(t))
	log.InfoContext(ctxOf(tenantA), "http", "method", "GET", "path", "/v1/tenants", "status", 200, "durationMs", int64(12),
		"requestId", "abc-123", "realmName", "acme", "hostname", "pod-1", "ok", true, "tenantId", tenantA)
	got := lines(t, buf)[0]
	for k, want := range map[string]any{"method": "GET", "path": "/v1/tenants", "status": float64(200), "durationMs": float64(12),
		"requestId": "abc-123", "realmName": "acme", "hostname": "pod-1", "ok": true, "tenantId": tenantA, "msg": "http"} {
		if got[k] != want {
			t.Errorf("%s = %v, want %v", k, got[k], want)
		}
	}
}

func TestNoKeyOrNoTenantNeverPassesTheValueThrough(t *testing.T) {
	log, buf := capture(nil)
	log.InfoContext(ctxOf(tenantA), "mail to a@example.com", "email", "a@example.com", "user", "Jane", "note", "cc b@example.com")
	log2, buf2 := capture(keys(t))
	log2.Info("no tenant anywhere", "email", "a@example.com", "name", "Jane", "note", "cc b@example.com")
	for _, b := range []*bytes.Buffer{buf, buf2} {
		out := b.String()
		for _, raw := range []string{"example.com", "Jane"} {
			if strings.Contains(out, raw) {
				t.Errorf("output contains %q: %s", raw, out)
			}
		}
		if !strings.Contains(out, logpseudo.Redacted) || !strings.Contains(out, logpseudo.EmailRedacted) {
			t.Errorf("want the fixed markers: %s", out)
		}
		if strings.Contains(out, logpseudo.Prefix) {
			t.Errorf("a pseudonym without a key or tenant: %s", out)
		}
	}
}

func TestEmptyPersonalValueStaysEmpty(t *testing.T) {
	log, buf := capture(keys(t))
	log.InfoContext(ctxOf(tenantA), "x", "email", "")
	if got := lines(t, buf)[0]["email"]; got != "" {
		t.Fatalf("email = %v", got)
	}
}

func TestInstallFollowsTheProfileAndTheFlag(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	for _, c := range []struct {
		name string
		env  map[string]string
		want bool
	}{
		{"commercial", map[string]string{}, false},
		{"flag off", map[string]string{"LOG_PSEUDONYMIZE": "false"}, false},
		{"flag", map[string]string{"LOG_PSEUDONYMIZE": "true"}, true},
		{"eu overlay", map[string]string{"PASSAGE_CLOUD_PROFILE": "commercial+eu-data-boundary"}, true},
		{"eu overlay beats a false flag", map[string]string{"PASSAGE_CLOUD_PROFILE": "commercial+eu-data-boundary", "LOG_PSEUDONYMIZE": "false"}, true},
		{"gcc", map[string]string{"PASSAGE_CLOUD_PROFILE": "gcc"}, false},
	} {
		slog.SetDefault(prev)
		got, err := logpseudo.Install(env(c.env))
		if err != nil || got != c.want {
			t.Errorf("%s: Install = %v, %v; want %v", c.name, got, err, c.want)
		}
	}
	slog.SetDefault(prev)
	if _, err := logpseudo.Install(env(map[string]string{"PASSAGE_CLOUD_PROFILE": "nope"})); err == nil {
		t.Error("an unknown profile must be an error")
	}
	if _, err := logpseudo.Install(env(map[string]string{"LOG_PSEUDONYMIZE": "true", "FIELDCRYPT_KEYS": "k1=notbase64"})); err == nil {
		t.Error("a bad key must be an error, not raw logs")
	}
}

func TestInstalledDefaultLoggerPseudonymizes(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	if !logpseudo.InstallWith(keys(t)) {
		t.Fatal("not installed")
	}
	if logpseudo.InstallWith(keys(t)) {
		t.Fatal("wrapped twice")
	}
	slog.InfoContext(ctxOf(tenantA), "hello", "email", "z@example.com")
	if strings.Contains(buf.String(), "example.com") || !strings.Contains(buf.String(), logpseudo.Prefix) {
		t.Fatalf("output: %s", buf.String())
	}
}
