package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

const firmPath = "/v1/planning/planned-orders:firm"

type fakeFirm struct {
	hash  string
	batch store.FirmBatch
}

// FirmPlannedOrders keeps the store's contract: replay by key, 422 when a key
// is reused with another request, 409 PLAN-0002 for an order firmed before.
func (f *fakeStore) FirmPlannedOrders(_ context.Context, req store.FirmRequest) (store.FirmBatch, bool, error) {
	f.lastFirm = req
	if f.firms == nil {
		f.firms, f.firmed = map[string]fakeFirm{}, map[string]bool{}
	}
	if prev, ok := f.firms[req.IdempotencyKey]; ok {
		if prev.hash != req.InputHash {
			return store.FirmBatch{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeIdempotencyReuse, "reuse")
		}
		return prev.batch, true, nil
	}
	for _, id := range req.OrderIDs {
		if f.firmed[id] {
			return store.FirmBatch{}, false, httpx.NewProblem(http.StatusConflict, store.CodeAlreadyFirmed, "planned order "+id+" is already firmed")
		}
	}
	batch := store.FirmBatch{ID: ids.New(), Grouping: req.Grouping, Firmed: len(req.OrderIDs), Groups: []store.FirmedGroup{{
		TargetType: "purchase-order", TargetID: ids.New(), OrderType: "purchase", SiteID: siteID,
		DeliveryDate: "2026-11-10", Qty: "1", PlannedOrderIDs: req.OrderIDs,
	}}}
	for _, id := range req.OrderIDs {
		f.firmed[id] = true
	}
	f.firms[req.IdempotencyKey] = fakeFirm{hash: req.InputHash, batch: batch}
	return batch, false, nil
}

func firmBody(grouping string, orderIDs ...string) string {
	quoted := make([]string, len(orderIDs))
	for i, id := range orderIDs {
		quoted[i] = fmt.Sprintf("%q", id)
	}
	g := ""
	if grouping != "" {
		g = fmt.Sprintf(`,"grouping":%q`, grouping)
	}
	return fmt.Sprintf(`{"plannedOrderIds":[%s]%s}`, strings.Join(quoted, ","), g)
}

func TestFirm_RequiresDutyTenantAndKey(t *testing.T) {
	body := firmBody("", ids.New())
	if rec := do(t, &fakeStore{}, http.MethodPost, firmPath, body, "planning.order.maintain", true, "f1"); rec.Code != http.StatusForbidden {
		t.Fatalf("without planning.order.firm: status %d, want 403", rec.Code)
	}
	if rec := do(t, &fakeStore{}, http.MethodPost, firmPath, body, dutyFirmOrders, false, "f1"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("without tenant: status %d, want 401", rec.Code)
	}
	rec := do(t, &fakeStore{}, http.MethodPost, firmPath, body, dutyFirmOrders, true, "")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Idempotency-Key") {
		t.Fatalf("without key: status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestFirm_ValidatesTheBatch(t *testing.T) {
	a := "00000000-0000-0000-0000-0000000000aa"
	many := make([]string, maxFirmOrders+1)
	for i := range many {
		many[i] = ids.New()
	}
	tests := []struct {
		name, body, field string
	}{
		{"empty list", `{"plannedOrderIds":[]}`, `"plannedOrderIds"`},
		{"over the batch limit", firmBody("", many...), `"plannedOrderIds"`},
		{"not a uuid", `{"plannedOrderIds":["nope"]}`, `"plannedOrderIds[0]"`},
		{"same id twice in other case", firmBody("", a, strings.ToUpper(a)), `"plannedOrderIds[1]"`},
		{"unknown grouping", firmBody("weekly", a), `"grouping"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := &fakeStore{}
			rec := do(t, st, http.MethodPost, firmPath, tc.body, dutyFirmOrders, true, "k-"+tc.name)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.field) {
				t.Fatalf("status %d body %s, want 400 naming %s", rec.Code, rec.Body.String(), tc.field)
			}
			if st.firms != nil {
				t.Fatal("an invalid batch must not reach the store")
			}
		})
	}
}

func TestFirm_FirmsThenReplays(t *testing.T) {
	st := &fakeStore{}
	a, b := "00000000-0000-0000-0000-0000000000b2", "00000000-0000-0000-0000-0000000000A1"
	first := do(t, st, http.MethodPost, firmPath, firmBody("", a, b), dutyFirmOrders, true, "f1")
	if first.Code != http.StatusCreated {
		t.Fatalf("first firm: status %d body %s", first.Code, first.Body.String())
	}
	var batch store.FirmBatch
	if err := json.Unmarshal(first.Body.Bytes(), &batch); err != nil {
		t.Fatal(err)
	}
	if batch.Grouping != "vendor-date" || batch.Firmed != 2 {
		t.Fatalf("batch = %+v, want the default grouping vendor-date and 2 firmed", batch)
	}
	if got := strings.Join(st.lastFirm.OrderIDs, ","); got != strings.ToLower(b)+","+a {
		t.Fatalf("store got ids %s, want them lowercase and sorted", got)
	}
	// The same ids in another order and case are the same request.
	replay := do(t, st, http.MethodPost, firmPath, firmBody("vendor-date", strings.ToLower(b), a), dutyFirmOrders, true, "f1")
	if replay.Code != http.StatusOK || replay.Header().Get("Idempotent-Replayed") != "true" || !strings.Contains(replay.Body.String(), batch.ID) {
		t.Fatalf("replay: status %d header %q body %s", replay.Code, replay.Header().Get("Idempotent-Replayed"), replay.Body.String())
	}
	reuse := do(t, st, http.MethodPost, firmPath, firmBody("vendor", a, b), dutyFirmOrders, true, "f1")
	if reuse.Code != http.StatusUnprocessableEntity {
		t.Fatalf("key reused with another grouping: status %d, want 422", reuse.Code)
	}
	again := do(t, st, http.MethodPost, firmPath, firmBody("none", a), dutyFirmOrders, true, "f2")
	if again.Code != http.StatusConflict || !strings.Contains(again.Body.String(), store.CodeAlreadyFirmed) {
		t.Fatalf("firming a firmed order: status %d body %s, want 409 %s", again.Code, again.Body.String(), store.CodeAlreadyFirmed)
	}
}

func TestMessages_AcceptsCancelDecreaseAndIncrease(t *testing.T) {
	for _, action := range []string{"cancel", "decrease", "increase"} {
		if rec := do(t, &fakeStore{}, http.MethodGet, "/v1/planning/messages?planCode=MAIN&action="+action, "", dutyRead, true, ""); rec.Code != http.StatusOK {
			t.Fatalf("action %s: status %d", action, rec.Code)
		}
	}
	if rec := do(t, &fakeStore{}, http.MethodGet, "/v1/planning/messages?planCode=MAIN&action=derived", "", dutyRead, true, ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown action: status %d, want 400", rec.Code)
	}
}

// TestRun_CoverageFenceRaisesCancel sends a run whose purchase receipt nothing
// needs, beyond a 30-day coverage fence, and checks the version counts its
// Cancel message; a fence over 3650 days is refused.
func TestRun_CoverageFenceRaisesCancel(t *testing.T) {
	body := `{"asOf":"2026-11-02","siteId":"` + siteID + `","items":[
 {"itemId":"` + fgID + `","coverage":{"code":"requirement","coverageFenceDays":30},"orderType":"purchase","lot":{},
  "demand":[],"receipts":[{"ref":"PO-9","refType":"purchase-line","date":"2026-12-20","qty":"50"}],"components":[]}]}`
	st := &fakeStore{}
	rec := do(t, st, http.MethodPost, "/v1/planning/plans/MAIN:run", body, dutyRun, true, "cov-1")
	if rec.Code != http.StatusCreated {
		t.Fatalf("run: status %d body %s", rec.Code, rec.Body.String())
	}
	msgs := st.last.Result.Messages
	if len(msgs) != 1 || msgs[0].Action != "cancel" || msgs[0].SupplyRef != "PO-9" {
		t.Fatalf("messages = %+v, want one cancel of PO-9", msgs)
	}
	bad := strings.Replace(body, `"coverageFenceDays":30`, `"coverageFenceDays":3651`, 1)
	if rec := do(t, &fakeStore{}, http.MethodPost, "/v1/planning/plans/MAIN:run", bad, dutyRun, true, "cov-2"); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "coverageFenceDays") {
		t.Fatalf("fence 3651: status %d body %s, want 400", rec.Code, rec.Body.String())
	}
}
