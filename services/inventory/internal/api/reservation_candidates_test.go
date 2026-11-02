package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

var lastCandidateQuery store.CandidateQuery

func (f *fakeStore) ReservationCandidates(_ context.Context, q store.CandidateQuery) ([]domain.Candidate, error) {
	lastCandidateQuery = q
	exp := time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC)
	return []domain.Candidate{
		{BatchStock: domain.BatchStock{BatchID: "B1", Dims: domain.Dimensions{"site": "S1", "batch": "B1"}, Available: decimal.NewFromInt(4), Status: "AVAILABLE", Expiry: &exp}, Eligible: true},
		{BatchStock: domain.BatchStock{BatchID: "B2", Dims: domain.Dimensions{"site": "S1", "batch": "B2"}, Available: decimal.NewFromInt(2), Status: "BLOCKED"}, Reason: domain.CandidateRestricted},
	}, nil
}

func TestReservationCandidatesAreReadOnlyValidatedAndPassedOn(t *testing.T) {
	h := newServer(t, &fakeStore{})
	le, product := ids.New(), ids.New()
	base := fmt.Sprintf("/v1/inventory/reservation-candidates?legalEntityId=%s&productId=%s", le, product)
	if rec := do(t, h, http.MethodGet, base, "", []string{"inventory.reservation.write"}, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("without inventory.read: %d, want 403", rec.Code)
	}
	for _, bad := range []string{"", "?legalEntityId=x&productId=" + product, base + "&policy=RANDOM", base + "&requiredDate=soon", base + "&shelfLifeMarginDays=-1",
		base + "&segmentType=SALES_ORDER"} {
		path := bad
		if bad == "" || bad[0] == '?' {
			path = "/v1/inventory/reservation-candidates" + bad
		}
		if rec := do(t, h, http.MethodGet, path, "", []string{"inventory.read"}, nil); rec.Code != http.StatusBadRequest {
			t.Fatalf("%q: %d, want 400 (%s)", bad, rec.Code, rec.Body.String())
		}
	}
	rec := do(t, h, http.MethodGet, base+"&policy=fefo&requiredDate=2027-02-01&shelfLifeMarginDays=7&site=S1&batch=B1", "", []string{"inventory.read"}, nil)
	var body struct {
		Items []struct {
			BatchID    string `json:"batchId"`
			Available  string `json:"available"`
			ExpiryDate string `json:"expiryDate"`
			Eligible   bool   `json:"eligible"`
			Reason     string `json:"reason"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusOK || len(body.Items) != 2 {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if a, b := body.Items[0], body.Items[1]; a.BatchID != "B1" || a.Available != "4" || a.ExpiryDate != "2027-03-01" || !a.Eligible || b.Eligible || b.Reason != domain.CandidateRestricted {
		t.Fatalf("items %+v", body.Items)
	}
	q := lastCandidateQuery
	if q.Policy != domain.PolicyFEFO || q.ShelfLifeMarginDays != 7 || q.Dims["site"] != "S1" || q.Dims["batch"] != "B1" || q.LegalEntityID != le || !q.SegmentFallback ||
		q.RequiredDate.Format("2006-01-02") != "2027-02-01" {
		t.Fatalf("query %+v", q)
	}
}
