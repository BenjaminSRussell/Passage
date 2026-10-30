package api_test

import (
	"context"
	"errors"
	"net/http"
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
