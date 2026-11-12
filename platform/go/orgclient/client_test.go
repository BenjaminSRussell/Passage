package orgclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const le = "00000000-0000-0000-0000-0000000000e1"

func testSigner(t *testing.T) *tenant.ServiceSigner {
	t.Helper()
	s, err := tenant.ServiceSignerFromEnv(func(k string) string {
		return map[string]string{"PASSAGE_SERVICE_NAME": "consolidation", "PASSAGE_SERVICE_SIGNING_KEY": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}[k]
	})
	if err != nil || s == nil {
		t.Fatalf("signer: %v %v", s, err)
	}
	return s
}

func principalCtx() context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: "00000000-0000-0000-0000-000000000001", UserID: "u", Duties: map[string]struct{}{tenant.DutyAllScope: {}}})
}

func server(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/fiscal-period"):
			if r.URL.Query().Get("date") == "2026-12-31" {
				w.WriteHeader(http.StatusUnprocessableEntity)
				return
			}
			_, _ = w.Write([]byte(`{"legalEntityId":"` + le + `","calendarId":"cal-1","source":"legalEntity","period":{"fiscalYear":2027,"periodNo":9,"name":"P9","startDate":"2026-12-01","endDate":"2026-12-31","type":"operating","week53":false}}`))
		case strings.HasSuffix(r.URL.Path, "/periods"):
			_, _ = w.Write([]byte(`{"items":[{"fiscalYear":2027,"periodNo":0,"name":"Opening","startDate":"2026-03-31","endDate":"2026-03-31","type":"opening"},` +
				`{"fiscalYear":2027,"periodNo":2,"name":"P2","startDate":"2026-05-01","endDate":"2026-05-31","type":"operating"},` +
				`{"fiscalYear":2027,"periodNo":1,"name":"P1","startDate":"2026-04-01","endDate":"2026-04-30","type":"operating"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestYears_FiscalYearStartFollowsTheCalendar(t *testing.T) {
	srv := server(t)
	defer srv.Close()
	c, err := New(srv.URL, testSigner(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	y := NewYears(c)
	got, err := y.FiscalYearStart(principalCtx(), le, "2026-11")
	if err != nil || got != "2026-04" {
		t.Fatalf("start = %q, %v", got, err)
	}
	end, err := y.PreviousYearEnd(principalCtx(), le, "2026-11")
	if err != nil || end != "2026-03" {
		t.Fatalf("previous year end = %q, %v", end, err)
	}
	if _, err := y.FiscalYearStart(principalCtx(), le, "2026-12"); !errors.Is(err, ErrNoCalendar) {
		t.Fatalf("want ErrNoCalendar, got %v", err)
	}
	if _, err := y.FiscalYearStart(principalCtx(), le, "Nov"); err == nil {
		t.Fatal("want an error for a bad period")
	}
}

func TestYears_FakeAndNextPeriod(t *testing.T) {
	y := NewYears(Fake{StartMonth: map[string]int{le: 7}})
	if got, err := y.FiscalYearStart(context.Background(), le, "2027-02"); err != nil || got != "2026-07" {
		t.Fatalf("start = %q, %v", got, err)
	}
	if got, err := y.FiscalYearStart(context.Background(), le, "2026-07"); err != nil || got != "2026-07" {
		t.Fatalf("start = %q, %v", got, err)
	}
	if _, err := y.FiscalYearStart(context.Background(), "other", "2027-02"); !errors.Is(err, ErrNoCalendar) {
		t.Fatal("want ErrNoCalendar")
	}
	next, err := y.NextPeriodStart(context.Background(), le, time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC))
	if err != nil || next.Format(time.DateOnly) != "2026-09-01" {
		t.Fatalf("next = %s, %v", next, err)
	}
}

func TestFromEnv(t *testing.T) {
	if c, err := FromEnv(func(string) string { return "" }); c != nil || err != nil {
		t.Fatalf("unset: %v %v", c, err)
	}
	if _, err := FromEnv(func(k string) string { return map[string]string{"ORG_URL": "http://org"}[k] }); err == nil {
		t.Fatal("want an error without a signing key")
	}
}
