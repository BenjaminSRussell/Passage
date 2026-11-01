package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

func TestExpireReservationsIsGuardedAndReportsWhatItReleased(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	const path = "/v1/inventory/reservations:expire"
	if rec := do(t, h, http.MethodPost, path, "", []string{"inventory.read"}, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("without the reservation duty: %d, want 403", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, path+"?limit=0", "", []string{"inventory.reservation.write"}, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("limit 0: %d, want 400", rec.Code)
	}
	rec := do(t, h, http.MethodPost, path+"?limit=25", "", []string{"inventory.reservation.write"}, nil)
	var body struct {
		Released int               `json:"released"`
		More     bool              `json:"more"`
		Items    []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusOK || body.Released != 1 || !body.More || len(body.Items) != 1 || fs.expireLimit != 25 {
		t.Fatalf("status %d body %s limit %d", rec.Code, rec.Body.String(), fs.expireLimit)
	}
}

func TestReservationTTLIsValidatedAndPassedOn(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	body := func(ttl int) string {
		return fmt.Sprintf(`{"legalEntityId":%q,"productId":%q,"qty":"1","demandType":"MANUAL","demandId":"D1","policy":"FIFO","ttlHours":%d}`, ids.New(), ids.New(), ttl)
	}
	for _, ttl := range []int{-1, 8761} {
		rec := do(t, h, http.MethodPost, "/v1/inventory/reservations", body(ttl), []string{"inventory.reservation.write"}, key("t-1"))
		if p := decodeProblem(t, rec); rec.Code != http.StatusBadRequest || p.Errors["ttlHours"] == "" {
			t.Fatalf("ttl %d: status %d errors %v", ttl, rec.Code, p.Errors)
		}
	}
	if rec := do(t, h, http.MethodPost, "/v1/inventory/reservations", body(72), []string{"inventory.reservation.write"}, key("t-2")); rec.Code != http.StatusCreated || fs.reserveIn.TTLHours != 72 {
		t.Fatalf("ttl 72: status %d ttl %d", rec.Code, fs.reserveIn.TTLHours)
	}
}
