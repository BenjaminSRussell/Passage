package api

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/budgeting/internal/domain"
	"github.com/BenjaminSRussell/passage/services/budgeting/internal/store"
)

// The base fake refuses reservation commands; reservationFake records them.
func (f *fakeStore) CreateReservation(context.Context, string, string, store.ReservationRequest, bool) (int, []byte, error) {
	return 0, nil, errNotFaked
}

func (f *fakeStore) ConsumeReservation(context.Context, string, string, store.ConsumeRequest) (int, []byte, error) {
	return 0, nil, errNotFaked
}

func (f *fakeStore) CloseReservation(context.Context, string, string, string) (int, []byte, error) {
	return 0, nil, errNotFaked
}

func (f *fakeStore) ExpireReservations(context.Context, string, string, string) (int, []byte, error) {
	return 0, nil, errNotFaked
}

func (f *fakeStore) ListReservations(context.Context, store.ReservationFilter) ([]store.Reservation, error) {
	return nil, errNotFaked
}

func (f *fakeStore) GetReservation(context.Context, string) (store.Reservation, error) {
	return store.Reservation{}, errNotFaked
}

var errNotFaked = errors.New("not faked")

// reservationFake extends fakeStore with the reservation commands.
type reservationFake struct {
	fakeStore
	created  store.ReservationRequest
	override bool
	consumed store.ConsumeRequest
	closed   string
	asOf     string
	filter   store.ReservationFilter
}

func (f *reservationFake) CreateReservation(_ context.Context, _, _ string, req store.ReservationRequest, override bool) (int, []byte, error) {
	f.calls++
	f.created, f.override = req, override
	return http.StatusCreated, []byte(`{"id":"res"}`), nil
}

func (f *reservationFake) ConsumeReservation(_ context.Context, _, _ string, req store.ConsumeRequest) (int, []byte, error) {
	f.calls++
	f.consumed = req
	return http.StatusOK, []byte(`{}`), nil
}

func (f *reservationFake) CloseReservation(_ context.Context, _, _, id string) (int, []byte, error) {
	f.calls++
	f.closed = id
	return 0, nil, httpx.NewProblem(http.StatusConflict, domain.CodeReservationNotOpen, "reservation is Closed")
}

func (f *reservationFake) ExpireReservations(_ context.Context, _, _, asOf string) (int, []byte, error) {
	f.calls++
	f.asOf = asOf
	return http.StatusOK, []byte(`{"expired":[]}`), nil
}

func (f *reservationFake) ListReservations(_ context.Context, filter store.ReservationFilter) ([]store.Reservation, error) {
	f.calls++
	f.filter = filter
	return []store.Reservation{}, nil
}

func (f *reservationFake) GetReservation(context.Context, string) (store.Reservation, error) {
	f.calls++
	return store.Reservation{}, httpx.NewProblem(http.StatusNotFound, store.CodeReservationNotFound, "reservation not found")
}

const (
	reservationID = "0196a6a4-0000-7000-8000-000000000010"
	reserveBody   = `{"ruleVersionId":"` + ruleID + `","type":"FundsReservation","expiryDate":"2026-12-31",
		"lines":[{"dimensionKey":"dept-10","spanKey":"FY2026","amount":"20000.00"}]}`
	consumeBody = `{"lineId":"` + lineID + `","documentType":"PurchaseOrder","documentId":"` + docID + `","documentLineId":"` + docID + `","amount":"8000.00"}`
)

func TestCreateReservation_DutyAndOverride(t *testing.T) {
	st := &reservationFake{}
	if rec := serve(t, st, http.MethodPost, "/v1/budget/reservations", reserveBody, "budget.check", "k1"); rec.Code != http.StatusForbidden {
		t.Fatalf("without budget.reserve: status = %d, want 403", rec.Code)
	}
	rec := serve(t, st, http.MethodPost, "/v1/budget/reservations", reserveBody, "budget.reserve,budget.override", "k1")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (%s)", rec.Code, rec.Body)
	}
	if st.created.Type != domain.FundsReservation || st.created.ExpiryDate != "2026-12-31" || !st.override {
		t.Fatalf("store got %+v override=%v", st.created, st.override)
	}
	if !st.created.Lines[0].Amount.Equal(d("20000")) {
		t.Fatalf("amount = %s", st.created.Lines[0].Amount)
	}
}

func TestCreateReservation_Validation(t *testing.T) {
	tests := map[string]string{
		"unknown type":            `{"ruleVersionId":"` + ruleID + `","type":"Encumbrance","lines":[{"dimensionKey":"d","spanKey":"s","amount":"1"}]}`,
		"no lines":                `{"ruleVersionId":"` + ruleID + `","type":"FundsReservation","lines":[]}`,
		"zero amount":             `{"ruleVersionId":"` + ruleID + `","type":"FundsReservation","lines":[{"dimensionKey":"d","spanKey":"s","amount":"0"}]}`,
		"five decimals":           `{"ruleVersionId":"` + ruleID + `","type":"FundsReservation","lines":[{"dimensionKey":"d","spanKey":"s","amount":"1.00001"}]}`,
		"bad expiry":              `{"ruleVersionId":"` + ruleID + `","type":"FundsReservation","expiryDate":"31/12/2026","lines":[{"dimensionKey":"d","spanKey":"s","amount":"1"}]}`,
		"blocking with an expiry": `{"ruleVersionId":"` + ruleID + `","type":"FundsBlocking","expiryDate":"2026-12-31","lines":[{"dimensionKey":"d","spanKey":"s","amount":"1"}]}`,
		"unknown field":           `{"ruleVersionId":"` + ruleID + `","type":"FundsReservation","number":"FR-1","lines":[{"dimensionKey":"d","spanKey":"s","amount":"1"}]}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			st := &reservationFake{}
			rec := serve(t, st, http.MethodPost, "/v1/budget/reservations", body, "budget.reserve", "k")
			if rec.Code != http.StatusBadRequest || problemCode(t, rec) != "COM-0001" {
				t.Fatalf("status = %d code %s, want 400 COM-0001", rec.Code, problemCode(t, rec))
			}
			if st.calls != 0 {
				t.Fatal("invalid request reached the store")
			}
		})
	}
}

func TestConsumeReservation_RequiresCheckDuty(t *testing.T) {
	st := &reservationFake{}
	path := "/v1/budget/reservations/" + reservationID + ":consume"
	if rec := serve(t, st, http.MethodPost, path, consumeBody, "budget.reserve", "k"); rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	rec := serve(t, st, http.MethodPost, path, consumeBody, "budget.check", "k")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body)
	}
	if st.consumed.ReservationID != reservationID || st.consumed.DocumentType != "PurchaseOrder" || !st.consumed.Amount.Equal(d("8000")) {
		t.Fatalf("store got %+v", st.consumed)
	}
	bad := `{"lineId":"` + lineID + `","documentType":"Reservation","documentId":"` + docID + `","documentLineId":"x","amount":"-1"}`
	if rec := serve(t, st, http.MethodPost, path, bad, "budget.check", "k2"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad consume: status = %d, want 400", rec.Code)
	}
}

func TestCloseReservation_RelaysConflict(t *testing.T) {
	st := &reservationFake{}
	rec := serve(t, st, http.MethodPost, "/v1/budget/reservations/"+reservationID+":close", "", "budget.reserve", "k")
	if rec.Code != http.StatusConflict || problemCode(t, rec) != domain.CodeReservationNotOpen || st.closed != reservationID {
		t.Fatalf("status = %d code %s closed %q", rec.Code, problemCode(t, rec), st.closed)
	}
	if rec := serve(t, st, http.MethodPost, "/v1/budget/reservations/not-a-uuid:close", "", "budget.reserve", "k"); rec.Code != http.StatusNotFound {
		t.Fatalf("bad id: status = %d, want 404", rec.Code)
	}
}

func TestExpireReservations_ValidatesDate(t *testing.T) {
	st := &reservationFake{}
	if rec := serve(t, st, http.MethodPost, "/v1/budget/reservations:expire", `{"asOf":"yesterday"}`, "budget.reserve", "k"); rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if rec := serve(t, st, http.MethodPost, "/v1/budget/reservations:expire", `{"asOf":"2026-10-01"}`, "budget.reserve", "k"); rec.Code != http.StatusOK || st.asOf != "2026-10-01" {
		t.Fatalf("status = %d asOf %q", rec.Code, st.asOf)
	}
}

func TestListReservations_Filters(t *testing.T) {
	st := &reservationFake{}
	rec := serve(t, st, http.MethodGet, "/v1/budget/reservations?type=Precommitment&status=PartiallyConsumed&rule="+ruleID, "", "budget.view", "")
	if rec.Code != http.StatusOK || st.filter.Type != "Precommitment" || st.filter.Status != "PartiallyConsumed" || st.filter.RuleVersionID != ruleID {
		t.Fatalf("status = %d filter %+v", rec.Code, st.filter)
	}
	if rec := serve(t, st, http.MethodGet, "/v1/budget/reservations?status=Gone", "", "budget.view", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad status filter: %d, want 400", rec.Code)
	}
	if rec := serve(t, st, http.MethodGet, "/v1/budget/reservations/"+reservationID, "", "budget.view", ""); rec.Code != http.StatusNotFound || problemCode(t, rec) != store.CodeReservationNotFound {
		t.Fatalf("get missing: status = %d", rec.Code)
	}
}

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }
