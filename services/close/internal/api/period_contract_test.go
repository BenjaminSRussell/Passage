package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/close/internal/domain"
)

var postingSources = []string{
	"ledger", "payables", "receiving", "assets", "eam", "production",
	"manual", "gl", "ap", "ar", "inv", "fa", "proj", "tax", "bank", "payroll", "cash", "prod", "migration", "",
}

func TestClosedPeriodRejectsEveryPostingSourceAtTheCheckEndpoint(t *testing.T) {
	closed := fakeStore{rows: []domain.Row{{Module: domain.ModuleAll, Status: domain.StatusClosed}}}
	for _, source := range postingSources {
		t.Run("source "+source, func(t *testing.T) {
			r, _ := newRouter(closed)
			rec := do(r, http.MethodPost, checkPath, body(source, ""), true, "period.check")
			if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"serviceCode":"LEDGER-0013"`) {
				t.Fatalf("got %d %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestClosedPeriodIsReportedClosedForEveryPostingSourceAtTheResolveEndpoint(t *testing.T) {
	closed := fakeStore{spans: []domain.Span{span(testkit.TenantID(8), domain.Row{Module: domain.ModuleAll, Status: domain.StatusClosed})}}
	for _, source := range postingSources {
		t.Run("source "+source, func(t *testing.T) {
			r, _ := newRouter(closed)
			target := resolvePath + "?ledgerId=" + testkit.TenantID(7) + "&postingDate=2026-01-15&sourceSystem=" + source
			rec := doResolve(r, target, testkit.TenantID(9), "period.check")
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"closed"`) {
				t.Fatalf("got %d %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestOnlyPostingSourcesOfTheClosedModuleAreRejected(t *testing.T) {
	apClosed := fakeStore{rows: []domain.Row{{Module: domain.ModuleAll, Status: domain.StatusOpen}, {Module: domain.ModuleAP, Status: domain.StatusClosed}}}
	want := map[string]int{
		"ap": http.StatusUnprocessableEntity, "payables": http.StatusUnprocessableEntity, "ledger": http.StatusUnprocessableEntity,
		"gl": http.StatusOK, "manual": http.StatusOK, "inv": http.StatusOK,
	}
	for source, status := range want {
		t.Run("source "+source, func(t *testing.T) {
			r, _ := newRouter(apClosed)
			if rec := do(r, http.MethodPost, checkPath, body(source, ""), true, "period.check"); rec.Code != status {
				t.Fatalf("got %d %s, want %d", rec.Code, rec.Body, status)
			}
		})
	}
}
