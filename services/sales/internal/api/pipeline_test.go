package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

// pipelineStore records the expiry and summary calls.
type pipelineStore struct {
	fakeStore
	expireLE    *string
	expireDay   time.Time
	expireActor string
	summary     *store.SummaryFilter
}

func (s *pipelineStore) ExpireQuotations(_ context.Context, le string, today time.Time, actor string) (store.ExpireResult, error) {
	s.expireLE, s.expireDay, s.expireActor = &le, today, actor
	return store.ExpireResult{Expired: []store.ExpiredQuotation{{ID: ids.New(), QuotationNumber: "SQ-0000001", PreviousStatus: "SENT", Version: 3}}, Count: 1}, nil
}

func (s *pipelineStore) Summary(_ context.Context, f store.SummaryFilter) (store.Summary, error) {
	s.summary = &f
	rate := decimal.RequireFromString("42.9")
	return store.Summary{ActiveHolds: 2, Pipeline: []store.PipelineCurrency{{Currency: "USD", OpenCount: 1, OpenNet: decimal.RequireFromString("60"),
		WeightedNet: decimal.RequireFromString("36")}}, WindowDays: f.WindowDays, Won: 3, Lost: 3, Expired: 1, ConversionRatePct: &rate}, nil
}

func TestExpireQuotations_DutyScopeAndActor(t *testing.T) {
	s := &pipelineStore{}
	if rec := serveBody(t, s, http.MethodPost, expirationRun, "", principal(dutyWrite), nil); rec.Code != http.StatusForbidden || s.expireLE != nil {
		t.Fatalf("sales.order.write alone must not run the expiry: status %d", rec.Code)
	}
	if rec := serveBody(t, s, http.MethodPost, expirationRun+"?legalEntityId="+ids.New(), "", principal(dutyQuotation), nil); rec.Code != http.StatusForbidden || s.expireLE != nil {
		t.Fatalf("another legal entity: status %d", rec.Code)
	}
	rec := serveBody(t, s, http.MethodPost, expirationRun, "", principal(dutyQuotation), nil)
	if rec.Code != http.StatusOK || s.expireLE == nil || *s.expireLE != testLE || s.expireActor != "u1" || s.expireDay.IsZero() {
		t.Fatalf("status %d le %v actor %q", rec.Code, s.expireLE, s.expireActor)
	}
	if !strings.Contains(rec.Body.String(), `"count":1`) || !strings.Contains(rec.Body.String(), `"more":false`) {
		t.Fatalf("body %s", rec.Body.String())
	}
}

func TestGetSummary_WindowAndMoneyAsStrings(t *testing.T) {
	s := &pipelineStore{}
	if rec := serveBody(t, s, http.MethodGet, summaryPath, "", principal(), nil); rec.Code != http.StatusForbidden {
		t.Fatalf("read duty required: status %d", rec.Code)
	}
	for _, bad := range []string{"0", "366", "x"} {
		if rec := serveBody(t, s, http.MethodGet, summaryPath+"?windowDays="+bad, "", principal(dutyRead), nil); rec.Code != http.StatusBadRequest {
			t.Fatalf("windowDays=%s: status %d", bad, rec.Code)
		}
	}
	rec := serveBody(t, s, http.MethodGet, summaryPath, "", principal(dutyRead), nil)
	if rec.Code != http.StatusOK || s.summary == nil || s.summary.WindowDays != 90 || s.summary.LegalEntityID != testLE {
		t.Fatalf("status %d filter %+v", rec.Code, s.summary)
	}
	for _, want := range []string{`"openNet":"60"`, `"weightedNet":"36"`, `"conversionRatePct":"42.9"`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("body lacks %s: %s", want, rec.Body.String())
		}
	}
	if rec := serveBody(t, s, http.MethodGet, summaryPath+"?windowDays=30", "", principal(dutyRead), nil); rec.Code != http.StatusOK || s.summary.WindowDays != 30 {
		t.Fatalf("windowDays=30: status %d", rec.Code)
	}
}

func TestGetSummary_PipelineFilters(t *testing.T) {
	s := &pipelineStore{}
	for _, bad := range []string{"status=LOST", "status=draft", "minProbabilityPct=101", "minProbabilityPct=-1", "expiringWithinDays=0", "expiringWithinDays=366"} {
		if rec := serveBody(t, s, http.MethodGet, summaryPath+"?"+bad, "", principal(dutyRead), nil); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status %d", bad, rec.Code)
		}
	}
	s.summary = nil
	rec := serveBody(t, s, http.MethodGet, summaryPath+"?status=SENT&minProbabilityPct=60&expiringWithinDays=14", "", principal(dutyRead), nil)
	if rec.Code != http.StatusOK || s.summary == nil || s.summary.Status != "SENT" || s.summary.MinProbabilityPct != 60 || s.summary.ExpiringWithinDays != 14 {
		t.Fatalf("status %d filter %+v", rec.Code, s.summary)
	}
	if rec := serveBody(t, s, http.MethodGet, summaryPath, "", principal(dutyRead), nil); rec.Code != http.StatusOK ||
		s.summary.Status != "" || s.summary.MinProbabilityPct != 0 || s.summary.ExpiringWithinDays != 0 {
		t.Fatalf("no filters: status %d filter %+v", rec.Code, s.summary)
	}
}
