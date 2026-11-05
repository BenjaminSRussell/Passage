package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

// quoteChangeStore records the quotation cancel and change calls.
type quoteChangeStore struct {
	fakeStore
	cancel       string
	cancelActor  string
	cancelledAt  int
	change       *store.QuotationChange
	changeRefuse error
}

func (s *quoteChangeStore) CancelQuotation(_ context.Context, id string, version int, reason, actor string) (store.Quotation, error) {
	s.cancel, s.cancelActor, s.cancelledAt = reason, actor, version
	return store.Quotation{ID: id, Version: version + 1, Status: domain.QuoteCancelled}, nil
}

func (s *quoteChangeStore) ChangeQuotation(_ context.Context, id string, in store.QuotationChange, _ time.Time) (store.Quotation, error) {
	s.change = &in
	if s.changeRefuse != nil {
		return store.Quotation{}, s.changeRefuse
	}
	return store.Quotation{ID: id, Version: in.Version + 1, Status: domain.QuoteDraft}, nil
}

func TestBuildQuotationChange_Table(t *testing.T) {
	today := time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC)
	line := func() orderLineRequest {
		return orderLineRequest{ProductID: ids.New(), Quantity: "2", Unit: "EA", SiteID: ids.New(), WarehouseID: ids.New(),
			UnitPrice: "10.00", RequestedShipDate: "2027-03-10"}
	}
	for _, tc := range []struct {
		name  string
		req   changeQuotationRequest
		field string
	}{
		{"empty body", changeQuotationRequest{}, "body"},
		{"validity in the past", changeQuotationRequest{ValidUntil: strPtr("2027-02-28")}, "validUntil"},
		{"validity beyond a year", changeQuotationRequest{ValidUntil: strPtr("2028-03-02")}, "validUntil"},
		{"validity not a date", changeQuotationRequest{ValidUntil: strPtr("soon")}, "validUntil"},
		{"probability above 100", changeQuotationRequest{ProbabilityPct: intPtr(101)}, "probabilityPct"},
		{"no lines", changeQuotationRequest{Lines: &[]orderLineRequest{}}, "lines"},
		{"bad price", changeQuotationRequest{Lines: &[]orderLineRequest{func() orderLineRequest { l := line(); l.UnitPrice = "ten"; return l }()}}, "lines[0].unitPrice"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, f := buildQuotationChange(tc.req, 3, today); f[tc.field] == "" {
				t.Fatalf("want an error on %s, got %v", tc.field, f)
			}
		})
	}
	in, f := buildQuotationChange(changeQuotationRequest{ValidUntil: strPtr("2027-04-01"), ProbabilityPct: intPtr(0),
		Lines: &[]orderLineRequest{line(), line()}}, 3, today)
	if len(f) != 0 || in.Version != 3 || in.ValidUntil == nil || in.ProbabilityPct == nil || *in.ProbabilityPct != 0 ||
		len(in.Lines) != 2 || in.Lines[1].LineNo != 2 || in.Lines[1].LineNet.String() != "20" {
		t.Fatalf("change not built: %+v %v", in, f)
	}
	// Absent lines stay nil (untouched); an empty list is refused above.
	if in, f := buildQuotationChange(changeQuotationRequest{ProbabilityPct: intPtr(80)}, 1, today); len(f) != 0 || in.Lines != nil {
		t.Fatalf("a probability-only change leaves lines nil: %+v %v", in, f)
	}
}

func TestCancelQuotationHandler(t *testing.T) {
	path := quotations + "/" + ids.New() + ":cancel"
	match := map[string]string{"If-Match": `"2"`}
	s := &quoteChangeStore{}
	if rec := serveBody(t, s, http.MethodPost, path, `{"reason":"x"}`, principal(dutyWrite), match); rec.Code != http.StatusForbidden {
		t.Fatalf("cancel needs sales.quotation.maintain: %d", rec.Code)
	}
	if rec := serveBody(t, s, http.MethodPost, path, `{"reason":"x"}`, principal(dutyQuotation), nil); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing If-Match: %d", rec.Code)
	}
	if rec := serveBody(t, s, http.MethodPost, path, `{"reason":"  "}`, principal(dutyQuotation), match); rec.Code != http.StatusBadRequest || s.cancel != "" {
		t.Fatalf("blank reason: %d", rec.Code)
	}
	if rec := serveBody(t, s, http.MethodPost, path, `{"reason":"`+strings.Repeat("x", 201)+`"}`, principal(dutyQuotation), match); rec.Code != http.StatusBadRequest || s.cancel != "" {
		t.Fatalf("a 201-character reason: %d", rec.Code)
	}
	// The limit counts characters as the column CHECK does, not bytes.
	accented := strings.Repeat("é", 200)
	if rec := serveBody(t, s, http.MethodPost, path, `{"reason":"`+accented+`"}`, principal(dutyQuotation), match); rec.Code != http.StatusOK || s.cancel != accented {
		t.Fatalf("a 200-character reason of 400 bytes: %d", rec.Code)
	}
	rec := serveBody(t, s, http.MethodPost, path, `{"reason":" customer withdrew "}`, principal(dutyQuotation), match)
	if rec.Code != http.StatusOK || s.cancel != "customer withdrew" || s.cancelActor != "u1" || s.cancelledAt != 2 || rec.Header().Get("ETag") != `"3"` {
		t.Fatalf("cancel: %d reason %q actor %q", rec.Code, s.cancel, s.cancelActor)
	}
}

func TestChangeQuotationHandler(t *testing.T) {
	path := quotations + "/" + ids.New()
	match := map[string]string{"If-Match": `"4"`}
	s := &quoteChangeStore{}
	if rec := serveBody(t, s, http.MethodPatch, path, `{"probabilityPct":70}`, principal(dutyRead), match); rec.Code != http.StatusForbidden {
		t.Fatalf("change needs sales.quotation.maintain: %d", rec.Code)
	}
	if rec := serveBody(t, s, http.MethodPatch, path, `{"probabilityPct":70}`, principal(dutyQuotation), nil); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing If-Match: %d", rec.Code)
	}
	if rec := serveBody(t, s, http.MethodPatch, path, `{}`, principal(dutyQuotation), match); rec.Code != http.StatusBadRequest || s.change != nil {
		t.Fatalf("empty change: %d", rec.Code)
	}
	rec := serveBody(t, s, http.MethodPatch, path, `{"probabilityPct":70}`, principal(dutyQuotation), match)
	if rec.Code != http.StatusOK || s.change.Version != 4 || *s.change.ProbabilityPct != 70 || s.change.Actor != "u1" || rec.Header().Get("ETag") != `"5"` {
		t.Fatalf("change: %d %+v", rec.Code, s.change)
	}
}

func intPtr(n int) *int { return &n }
