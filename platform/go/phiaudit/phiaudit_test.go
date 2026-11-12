package phiaudit_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/phiaudit"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
)

func signer(t *testing.T) (*tenant.ServiceSigner, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := tenant.NewServiceSigner("hr", priv)
	if err != nil {
		t.Fatal(err)
	}
	return s, pub
}

func caller() context.Context {
	return tenant.With(context.Background(), tenant.Principal{
		TenantID: testkit.TenantID(1), UserID: "user-7", LegalEntityID: testkit.TenantID(5),
	})
}

func TestHTTPRecorderPostsOneReadWithAServiceTokenForTheCaller(t *testing.T) {
	s, pub := signer(t)
	verifier := tenant.NewServiceVerifierKids("audit", map[string]map[string]ed25519.PublicKey{"hr": {"": pub}})
	var got struct {
		method, path, token string
		body                map[string]any
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		got.method, got.path, got.token = r.Method, r.URL.Path, r.Header.Get(tenant.HeaderServiceToken)
		_ = json.Unmarshal(raw, &got.body)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	at := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	err := phiaudit.NewHTTP(srv.URL+"/", s).RecordRead(caller(), phiaudit.Read{
		Entity: "hr.worker.benefits", RecordID: "w-1", Fields: []string{"enrollments.optionCode"}, Purpose: "benefits enquiry", TraceID: "trace-1", OccurredAt: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.method != http.MethodPost || got.path != "/v1/audit/reads" {
		t.Fatalf("request %s %s", got.method, got.path)
	}
	if _, named := got.body["userId"]; named {
		t.Fatal("the body must not name the reader; the audit service takes it from the token")
	}
	if got.body["entity"] != "hr.worker.benefits" || got.body["recordId"] != "w-1" || got.body["purpose"] != "benefits enquiry" || got.body["traceId"] != "trace-1" || got.body["occurredAt"] != "2026-11-11T09:00:00Z" {
		t.Fatalf("body %v", got.body)
	}
	claims, err := verifier.Verify(got.token)
	if err != nil {
		t.Fatalf("token does not verify: %v", err)
	}
	if claims.Act != "user-7" || claims.Iss != "hr" || claims.Aud != "audit" || len(claims.Duties) != 1 || claims.Duties[0] != phiaudit.DutyWrite {
		t.Fatalf("claims %+v: the token must carry the caller and only the read-log write duty", claims)
	}
}

// TestHTTPRecorderRecordsCallersWithAnyScope: an HR user with ALL scope, a tenant administrator and a break-glass caller
// have no legal-entity claim. Their reads must be recorded like a scoped user's, or every PHI read they make is refused
// with 503. The token names no legal entity (audit.read-log.write is tenant-level) and verifies at the audit service.
func TestHTTPRecorderRecordsCallersWithAnyScope(t *testing.T) {
	s, pub := signer(t)
	verifier := tenant.NewServiceVerifierKids("audit", map[string]map[string]ed25519.PublicKey{"hr": {"": pub}})
	var token string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token = r.Header.Get(tenant.HeaderServiceToken)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	duties := func(d ...string) map[string]struct{} {
		m := map[string]struct{}{}
		for _, x := range d {
			m[x] = struct{}{}
		}
		return m
	}
	for name, p := range map[string]tenant.Principal{
		"one legal entity":      {LegalEntityID: testkit.TenantID(5)},
		"two legal entities":    {LegalEntityID: testkit.TenantID(5) + "," + testkit.TenantID(6)},
		"ALL scope":             {Duties: duties(tenant.DutyAllScope, "hr.worker.inquire")},
		"tenant administrator":  {Duties: duties(tenant.DutyTenantAdmin)},
		"break-glass, no scope": {Duties: duties("hr.benefits.break-glass")},
	} {
		p.TenantID, p.UserID = testkit.TenantID(1), "user-7"
		token = ""
		err := phiaudit.NewHTTP(srv.URL, s).RecordRead(tenant.With(context.Background(), p), phiaudit.Read{
			Entity: "hr.worker.benefits", RecordID: "w-1", Fields: []string{"enrollments.optionCode"}, Purpose: "BREAK-GLASS: ER",
		})
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		claims, err := verifier.Verify(token)
		if err != nil {
			t.Errorf("%s: token does not verify: %v", name, err)
			continue
		}
		if claims.Le != "" || claims.Act != "user-7" || claims.Tid != testkit.TenantID(1) {
			t.Errorf("%s: claims %+v; want the caller in the tenant and no legal entity", name, claims)
		}
	}
}

func TestHTTPRecorderFailsClosed(t *testing.T) {
	s, _ := signer(t)
	read := phiaudit.Read{Entity: "e", RecordID: "r", Fields: []string{"f"}, Purpose: "p"}
	for name, c := range map[string]struct {
		rec phiaudit.Recorder
		ctx context.Context
		srv int
	}{
		"no URL":                {phiaudit.NewHTTP("", s), caller(), 0},
		"no signer":             {phiaudit.NewHTTP("http://audit.invalid", nil), caller(), 0},
		"no principal":          {phiaudit.NewHTTP("http://audit.invalid", s), context.Background(), 0},
		"server error":          {nil, caller(), http.StatusInternalServerError},
		"unexpected 200":        {nil, caller(), http.StatusOK},
		"forbidden":             {nil, caller(), http.StatusForbidden},
		"validation refused":    {nil, caller(), http.StatusBadRequest},
		"redirect not followed": {nil, caller(), http.StatusFound},
	} {
		rec := c.rec
		if c.srv != 0 {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(c.srv) }))
			defer srv.Close()
			rec = phiaudit.NewHTTP(srv.URL, s)
		}
		if err := rec.RecordRead(c.ctx, read); err == nil {
			t.Errorf("%s: want an error so the caller can refuse the read", name)
		}
	}
}

func TestFakeKeepsReadsWithTheCaller(t *testing.T) {
	f := &phiaudit.Fake{}
	if err := f.RecordRead(caller(), phiaudit.Read{Entity: "e", RecordID: "r", Purpose: "p"}); err != nil {
		t.Fatal(err)
	}
	got := f.Reads()
	if len(got) != 1 || got[0].UserID != "user-7" || got[0].TenantID != testkit.TenantID(1) || got[0].RecordID != "r" || got[0].OccurredAt.IsZero() {
		t.Fatalf("reads %+v", got)
	}
	failing := &phiaudit.Fake{Err: io.ErrUnexpectedEOF}
	if err := failing.RecordRead(caller(), phiaudit.Read{}); err == nil || len(failing.Reads()) != 0 {
		t.Fatal("a failing fake returns its error and keeps nothing")
	}
	if err := (phiaudit.Nop{}).RecordRead(caller(), phiaudit.Read{}); err != nil {
		t.Fatal(err)
	}
}

func TestEnabledFollowsTheHIPAAOverlayUnlessSwitched(t *testing.T) {
	env := func(profile, sw string) func(string) string {
		return func(k string) string {
			switch k {
			case "PASSAGE_CLOUD_PROFILE":
				return profile
			case phiaudit.EnvReadAudit:
				return sw
			}
			return ""
		}
	}
	for _, c := range []struct {
		name, profile, sw string
		want              bool
	}{
		{"unset is commercial", "", "", false},
		{"commercial", "commercial", "", false},
		{"hipaa overlay", "commercial+hipaa", "", true},
		{"hipaa on gcc high", "gcc-high+hipaa", "", true},
		{"forced on", "commercial", "true", true},
		{"forced off beats the overlay", "commercial+hipaa", "false", false},
		{"auto follows the overlay", "commercial+hipaa", "auto", true},
		{"switch is case-insensitive", "commercial", " TRUE ", true},
	} {
		got, err := phiaudit.Enabled(env(c.profile, c.sw))
		if err != nil || got != c.want {
			t.Errorf("%s: got %v %v, want %v", c.name, got, err, c.want)
		}
	}
	if _, err := phiaudit.Enabled(env("nowhere+hipaa", "")); err == nil || !strings.Contains(err.Error(), "unknown cloud profile") {
		t.Errorf("an unknown profile must fail closed, got %v", err)
	}
}

func TestIsBreakGlass(t *testing.T) {
	for purpose, want := range map[string]bool{
		"BREAK-GLASS":                    true,
		"break-glass: member in ER":      true,
		" Break-Glass ER admission":      true,
		"BREAK-GLASSES are not a reason": false,
		"benefits enquiry (BREAK-GLASS)": false,
		"":                               false,
	} {
		if got := phiaudit.IsBreakGlass(purpose); got != want {
			t.Errorf("IsBreakGlass(%q) = %v, want %v", purpose, got, want)
		}
	}
}
