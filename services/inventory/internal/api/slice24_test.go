package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
)

func TestReservationAgainstIsValidatedAndPassedOn(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	body := func(policy, against string) string {
		return fmt.Sprintf(`{"legalEntityId":%q,"productId":%q,"qty":"1","demandType":"MANUAL","demandId":"D1","policy":%q,"against":%q,"dimensions":{"site":"S1","warehouse":"W2"}}`,
			ids.New(), ids.New(), policy, against)
	}
	duties := []string{"inventory.reservation.write"}
	for _, c := range [][2]string{{"MANUAL", "SOON"}, {"FIFO", "ORDERED"}} {
		rec := do(t, h, http.MethodPost, "/v1/inventory/reservations", body(c[0], c[1]), duties, key("a-1"))
		if p := decodeProblem(t, rec); rec.Code != http.StatusBadRequest || p.Errors["against"] == "" {
			t.Fatalf("%v: status %d errors %v", c, rec.Code, p.Errors)
		}
	}
	if rec := do(t, h, http.MethodPost, "/v1/inventory/reservations", body("MANUAL", "ordered"), duties, key("a-2")); rec.Code != http.StatusCreated || fs.reserveIn.Kind != domain.ReserveKindOrdered {
		t.Fatalf("ordered: status %d kind %q", rec.Code, fs.reserveIn.Kind)
	}
	if rec := do(t, h, http.MethodPost, "/v1/inventory/reservations", body("MANUAL", ""), duties, key("a-3")); rec.Code != http.StatusCreated || fs.reserveIn.Kind != domain.ReserveKindPhysical {
		t.Fatalf("default: status %d kind %q", rec.Code, fs.reserveIn.Kind)
	}
}

func TestBackfillTransferSupplyNeedsAdminDutyAndPages(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	path := "/internal/v1/inventory/transfer-orders:backfill-supply"
	if rec := do(t, h, http.MethodPost, path, "", []string{"inventory.journal.post"}, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403", rec.Code)
	}
	admin := []string{"inventory.admin"}
	for _, q := range []string{"?limit=0", "?limit=501", "?after=not-an-id"} {
		if rec := do(t, h, http.MethodPost, path+q, "", admin, nil); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status %d, want 400", q, rec.Code)
		}
	}
	after := ids.New()
	rec := do(t, h, http.MethodPost, path+"?limit=50&after="+after, "", admin, nil)
	var body struct {
		Checked, Booked int
		More            bool
		Next            string
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if fs.backfillAfter != after || fs.backfillLimit != 50 || body.Checked != 2 || body.Booked != 1 || !body.More || body.Next != "next-id" {
		t.Fatalf("passed after %q limit %d, answered %+v", fs.backfillAfter, fs.backfillLimit, body)
	}
	if rec := do(t, h, http.MethodPost, path, "", admin, nil); rec.Code != http.StatusOK || fs.backfillAfter != "" || fs.backfillLimit != 200 {
		t.Fatalf("defaults: status %d after %q limit %d", rec.Code, fs.backfillAfter, fs.backfillLimit)
	}
}
