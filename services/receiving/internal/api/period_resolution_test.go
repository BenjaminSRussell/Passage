package api_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/receiving/internal/api"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store/memstore"
)

const resolvedPeriodID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"

type scriptedPeriods struct {
	status     domain.PeriodStatus
	resolveErr error
	queries    []domain.PeriodQuery
}

func (s *scriptedPeriods) AP(context.Context, domain.PeriodQuery) (domain.PeriodStatus, error) {
	return s.status, nil
}

func (s *scriptedPeriods) Resolve(_ context.Context, q domain.PeriodQuery) (domain.ResolvedPeriod, error) {
	s.queries = append(s.queries, q)
	if s.resolveErr != nil {
		return domain.ResolvedPeriod{}, s.resolveErr
	}
	return domain.ResolvedPeriod{FiscalPeriodID: resolvedPeriodID, Status: s.status}, nil
}

func resolverRig(sp *scriptedPeriods) *rig {
	return newRigPorts(memstore.New(), domain.PeriodOpen, func(p *api.Ports) {
		p.Periods = sp
		p.PurchaseOrders = domain.FakePurchaseOrders{Orders: map[string]domain.PurchaseOrder{poID: planPO()}}
		p.InvoicingPlans = domain.FakeInvoicingPlans{Plans: map[string][]domain.InvoicingPlan{poID: plansForPO()}}
		p.OwnershipChanges = domain.FakeOwnershipChanges{Changes: ownershipChanges()}
	})
}

func withoutPeriod(body string) string {
	return strings.Replace(body, `,"fiscalPeriodId":"`+periodID+`"`, "", 1)
}

type periodEndpoint struct {
	name     string
	path     string
	okStatus int
	prepare  func(t *testing.T, rg *rig) (body string, hdr func() headers)
}

func periodEndpoints() []periodEndpoint {
	return []periodEndpoint{
		{"vendor invoice", invoicesPath, http.StatusCreated, func(t *testing.T, rg *rig) (string, func() headers) {
			r := rg.posted(t, "PS-1", 100, 100)
			return invoiceBody("INV-1", r.ID, 10, ""), func() headers { return rg.apPosterFor(legalEntityID) }
		}},
		{"invoice adjustment", adjustmentsPath, http.StatusCreated, func(t *testing.T, rg *rig) (string, func() headers) {
			r := rg.posted(t, "PS-1", 100, 100)
			body := strings.Replace(invoiceBody("INV-1", r.ID, 10, ""), periodID, resolvedPeriodID, 1)
			inv := decode[invoiceJSON](t, rg.call(http.MethodPost, invoicesPath, body, rg.apPosterFor(legalEntityID)), http.StatusCreated)
			return adjustmentBody(inv.ID, domain.AdjustmentDebit, "DN-1", adjLine(1, "1.00")), func() headers { return rg.apPosterFor(legalEntityID) }
		}},
		{"plan settlement", planRunPath, http.StatusOK, func(t *testing.T, rg *rig) (string, func() headers) {
			return planBody(), rg.settle
		}},
		{"consignment settlement", consignmentRunPath, http.StatusOK, func(t *testing.T, rg *rig) (string, func() headers) {
			return runBody(""), rg.settle
		}},
	}
}

func TestPeriodResolution_FiscalPeriodComesFromThePostingDate(t *testing.T) {
	for _, ep := range periodEndpoints() {
		t.Run(ep.name, func(t *testing.T) {
			sp := &scriptedPeriods{status: domain.PeriodOpen}
			rg := resolverRig(sp)
			body, hdr := ep.prepare(t, rg)
			sp.queries = nil

			rec := rg.call(http.MethodPost, ep.path, withoutPeriod(body), hdr())

			if rec.Code != ep.okStatus || !strings.Contains(rec.Body.String(), `"fiscalPeriodId":"`+resolvedPeriodID+`"`) {
				t.Fatalf("got %d %s, want %d carrying the resolved period", rec.Code, rec.Body.String(), ep.okStatus)
			}
			if len(sp.queries) == 0 || sp.queries[0].LegalEntityID != legalEntityID || sp.queries[0].LedgerID != ledgerID || sp.queries[0].PostingDate.IsZero() {
				t.Fatalf("resolver queries %+v, want the request's legal entity, the posting ledger and the posting date", sp.queries)
			}
		})
	}
}

func TestPeriodResolution_SuppliedPeriodIsAcceptedOnlyWhenItMatches(t *testing.T) {
	for _, ep := range periodEndpoints() {
		t.Run(ep.name+"/matching", func(t *testing.T) {
			rg := resolverRig(&scriptedPeriods{status: domain.PeriodOpen})
			body, hdr := ep.prepare(t, rg)
			rec := rg.call(http.MethodPost, ep.path, strings.Replace(body, periodID, resolvedPeriodID, 1), hdr())
			if rec.Code != ep.okStatus {
				t.Fatalf("got %d %s", rec.Code, rec.Body.String())
			}
		})
		t.Run(ep.name+"/different", func(t *testing.T) {
			rg := resolverRig(&scriptedPeriods{status: domain.PeriodOpen})
			body, hdr := ep.prepare(t, rg)
			before, events := len(rg.ledger.Entries), len(rg.st.Events())
			problemCode(t, rg.call(http.MethodPost, ep.path, body, hdr()), http.StatusUnprocessableEntity, "PER-AP-002")
			if len(rg.ledger.Entries) != before || len(rg.st.Events()) != events {
				t.Fatalf("a mismatched period posted: ledger %d->%d", before, len(rg.ledger.Entries))
			}
		})
	}
}

func TestPeriodResolution_Failures(t *testing.T) {
	down := errors.New("close unreachable")
	tests := []struct {
		name   string
		mutate func(*scriptedPeriods)
		status int
		code   string
	}{
		{"no period covers the date", func(s *scriptedPeriods) { s.resolveErr = domain.ErrFiscalPeriodNotFound }, http.StatusUnprocessableEntity, "PER-AP-002"},
		{"several periods cover the date", func(s *scriptedPeriods) { s.resolveErr = domain.ErrFiscalPeriodAmbiguous }, http.StatusUnprocessableEntity, "PER-AP-002"},
		{"close unavailable fails closed", func(s *scriptedPeriods) { s.resolveErr = down }, http.StatusInternalServerError, "COM-0500"},
		{"closed period keeps the AP period error", func(s *scriptedPeriods) { s.status = domain.PeriodClosed }, http.StatusUnprocessableEntity, "PER-AP-001"},
		{"held period keeps the AP period error", func(s *scriptedPeriods) { s.status = domain.PeriodOnHold }, http.StatusUnprocessableEntity, "PER-AP-001"},
	}
	for _, ep := range periodEndpoints() {
		for _, tc := range tests {
			t.Run(ep.name+"/"+tc.name, func(t *testing.T) {
				sp := &scriptedPeriods{status: domain.PeriodOpen}
				rg := resolverRig(sp)
				body, hdr := ep.prepare(t, rg)
				before, events := len(rg.ledger.Entries), len(rg.st.Events())
				tc.mutate(sp)

				rec := rg.call(http.MethodPost, ep.path, withoutPeriod(body), hdr())

				problemCode(t, rec, tc.status, tc.code)
				if len(rg.ledger.Entries) != before || len(rg.st.Events()) != events {
					t.Fatalf("a rejected request posted: ledger %d->%d, events %d->%d", before, len(rg.ledger.Entries), events, len(rg.st.Events()))
				}
			})
		}
	}
}

func metricValue(t *testing.T, rg *rig, name string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	rg.api.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if strings.HasPrefix(line, name+" ") {
			return strings.TrimPrefix(line, name+" ")
		}
	}
	t.Fatalf("metric %s missing in %s", name, rec.Body.String())
	return ""
}

func TestPeriodResolution_FallsBackToTheSuppliedPeriodOnlyWhenCloseHasNoDatedPeriod(t *testing.T) {
	const metric = "receiving_period_resolve_fallback_total"
	tests := []struct {
		name     string
		err      error
		supplied bool
		status   int
		code     string
		fallback string
	}{
		{"no dated period, supplied id used", domain.ErrFiscalPeriodNotFound, true, http.StatusCreated, "", "1"},
		{"no dated period, nothing supplied", domain.ErrFiscalPeriodNotFound, false, http.StatusUnprocessableEntity, "PER-AP-002", "0"},
		{"ambiguous never falls back", domain.ErrFiscalPeriodAmbiguous, true, http.StatusUnprocessableEntity, "PER-AP-002", "0"},
		{"close down never falls back", errors.New("close unreachable"), true, http.StatusInternalServerError, "COM-0500", "0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sp := &scriptedPeriods{status: domain.PeriodOpen}
			rg := resolverRig(sp)
			r := rg.posted(t, "PS-1", 100, 100)
			sp.resolveErr = tc.err
			body := invoiceBody("INV-1", r.ID, 10, "")
			if !tc.supplied {
				body = withoutPeriod(body)
			}

			rec := rg.call(http.MethodPost, invoicesPath, body, rg.apPosterFor(legalEntityID))

			if tc.code != "" {
				problemCode(t, rec, tc.status, tc.code)
			} else if got := decode[struct{ FiscalPeriodID string }](t, rec, tc.status); got.FiscalPeriodID != periodID {
				t.Fatalf("posted with %q, want the supplied %s", got.FiscalPeriodID, periodID)
			}
			if got := metricValue(t, rg, metric); got != tc.fallback {
				t.Fatalf("%s = %s, want %s", metric, got, tc.fallback)
			}
		})
	}
}

func TestPeriodResolution_FallbackStillRunsTheAPStatusCheck(t *testing.T) {
	sp := &scriptedPeriods{status: domain.PeriodOpen}
	rg := resolverRig(sp)
	r := rg.posted(t, "PS-1", 100, 100)
	sp.resolveErr, sp.status = domain.ErrFiscalPeriodNotFound, domain.PeriodClosed

	problemCode(t, rg.call(http.MethodPost, invoicesPath, invoiceBody("INV-1", r.ID, 10, ""), rg.apPosterFor(legalEntityID)), http.StatusUnprocessableEntity, "PER-AP-001")
}

func TestPeriodResolution_ErsRunAndAuditHoldResolveTheirPeriod(t *testing.T) {
	sp := &scriptedPeriods{status: domain.PeriodOpen}
	rg := ersRig("2.60", sp, testSampler("0123456789abcdef0123"))
	rg.enableErs(t, true)
	rg.posted(t, "PS-1", 10, 4)
	sp.queries = nil

	run := decode[ersRunJSON](t, rg.call(http.MethodPost, ersRunPath, withoutPeriod(runBody("", `"vendorId":"`+vendorID+`"`)), rg.settle()), http.StatusOK)

	if len(run.Settled) != 1 || len(sp.queries) == 0 || sp.queries[0].LegalEntityID != legalEntityID || !sp.queries[0].PostingDate.Equal(day(2026, 10, 15)) {
		t.Fatalf("run %+v, resolver queries %+v", run, sp.queries)
	}

	sp.resolveErr = domain.ErrFiscalPeriodNotFound
	rg.posted(t, "PS-2", 10, 4)
	before := len(rg.ledger.Entries)
	problemCode(t, rg.call(http.MethodPost, ersRunPath, withoutPeriod(runBody("", `"vendorId":"`+vendorID+`"`)), rg.settle()), http.StatusUnprocessableEntity, "PER-AP-002")
	if len(rg.ledger.Entries) != before {
		t.Fatal("an ERS run without a resolvable period posted")
	}
}

func TestPeriodResolution_AuditHeldInvoiceStoresTheResolvedPeriod(t *testing.T) {
	sp := &scriptedPeriods{status: domain.PeriodOpen}
	rg := ersRig("2.60", sp, testSampler("0123456789abcdef0123"))
	rg.sampleRate(t, "100")
	r := rg.posted(t, "PS-1", 100, 100)

	rec := rg.call(http.MethodPost, invoicesPath, withoutPeriod(invoiceBody("INV-1", r.ID, 10, "")), userDuty("receiving.vendor-invoice.post", rg.key(), "poster"))

	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"status":"HELD"`) && !strings.Contains(rec.Body.String(), `"auditReason"`) || !strings.Contains(rec.Body.String(), `"fiscalPeriodId":"`+resolvedPeriodID+`"`) {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}
