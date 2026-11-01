package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"

	"github.com/BenjaminSRussell/passage/services/processmfg/internal/domain"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/store"
)

const (
	pickItem = "44444444-0000-4000-8000-0000000000aa"
	batchOld = "55555555-0000-4000-8000-000000000001"
	batchNew = "55555555-0000-4000-8000-000000000002"
	batchOnH = "55555555-0000-4000-8000-000000000003"
)

func storedFake() *fakeStore {
	exp := func(s string) *time.Time { t, _ := time.Parse("2006-01-02", s); return &t }
	pot := func(s string) *decimal.Decimal { d := decimal.RequireFromString(s); return &d }
	return &fakeStore{potency: "potency", cands: []store.PickCandidate{
		{BatchNumber: "L-OLD", Batch: domain.Batch{ID: batchOld, Disposition: "available", Qty: decimal.NewFromInt(50),
			Values: map[string]domain.Value{"expiry": {Date: exp("2027-02-01")}, "potency": {Num: pot("50")}}}},
		{BatchNumber: "L-NEW", Batch: domain.Batch{ID: batchNew, Disposition: "available", Qty: decimal.NewFromInt(100),
			Values: map[string]domain.Value{"expiry": {Date: exp("2027-06-01")}, "potency": {Num: pot("50")}}}},
		{BatchNumber: "L-HOLD", Batch: domain.Batch{ID: batchOnH, Disposition: "on-hold", Qty: decimal.NewFromInt(100),
			Values: map[string]domain.Value{"expiry": {Date: exp("2027-01-20")}}}},
	}}
}

const storedBody = `{"itemId":"` + pickItem + `","postingDate":"2027-01-15","qty":"80",
	"onHand":[{"batchId":"` + batchOld + `","qty":"50"},{"batchId":"` + batchNew + `","qty":"100"},{"batchId":"` + batchOnH + `","qty":"100"}]}`

// The disposition and expiry come from the register: the on-hold batch is
// excluded with PMF-0016 although the caller sent nothing about it, and the
// rest is taken expiry first.
func TestProposePickingStored_ReadsRegisterState(t *testing.T) {
	f := storedFake()
	rec := post(pickStoredPath, storedBody, DutyRead).do(newRouter(f))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	got := decodeBody[pickStoredResponse](t, rec.Body.Bytes())
	if len(got.Picked) != 2 || got.Picked[0].BatchID != batchOld || got.Picked[0].Qty.String() != "50" ||
		got.Picked[1].BatchID != batchNew || got.Picked[1].Qty.String() != "30" || !got.Shortfall.IsZero() {
		t.Fatalf("picked %+v shortfall %s", got.Picked, got.Shortfall)
	}
	if len(got.Excluded) != 1 || got.Excluded[0].BatchID != batchOnH || got.Excluded[0].Code != "PMF-0016" {
		t.Fatalf("excluded %+v", got.Excluded)
	}
	if got.BatchNumbers[batchOld] != "L-OLD" || f.gotItem != pickItem || len(f.gotOnHand) != 3 {
		t.Fatalf("numbers %v item %q onhand %v", got.BatchNumbers, f.gotItem, f.gotOnHand)
	}
}

// The potency attribute defaults to the item's potency role; an item without
// one needs it named.
func TestProposePickingStored_PotencyDefaultsToItemRole(t *testing.T) {
	body := `{"itemId":"` + pickItem + `","postingDate":"2027-01-15","qty":"10","potency":{"min":"55","max":"60"},
		"onHand":[{"batchId":"` + batchOld + `","qty":"50"}]}`
	rec := post(pickStoredPath, body, DutyRead).do(newRouter(storedFake()))
	got := decodeBody[pickStoredResponse](t, rec.Body.Bytes())
	if rec.Code != http.StatusOK || len(got.Picked) != 0 || len(got.Excluded) != 1 || got.Excluded[0].Code != "PMF-0005" {
		t.Fatalf("status %d: %+v", rec.Code, got)
	}
	f := storedFake()
	f.potency = ""
	rec = post(pickStoredPath, body, DutyRead).do(newRouter(f))
	if rec.Code != http.StatusBadRequest || problemOf(t, rec).Code != "COM-0001" {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

// BR-24.10: the expired batch is eligible only with the duty and a reason.
func TestProposePickingStored_ExpiredNeedsDutyAndReason(t *testing.T) {
	body := `{"itemId":"` + pickItem + `","postingDate":"2027-03-01","qty":"10","overrideReason":"customer approved by QA",
		"onHand":[{"batchId":"` + batchOld + `","qty":"50"}]}`
	without := decodeBody[pickStoredResponse](t, post(pickStoredPath, body, DutyRead).do(newRouter(storedFake())).Body.Bytes())
	if len(without.Picked) != 0 || len(without.Excluded) != 1 || without.Excluded[0].Code != "PMF-0003" {
		t.Fatalf("without duty %+v", without)
	}
	with := decodeBody[pickStoredResponse](t, post(pickStoredPath, body, DutyRead+","+DutyConsumeExpired).do(newRouter(storedFake())).Body.Bytes())
	if len(with.Picked) != 1 || !with.Picked[0].ExpiredOverride || !with.OverrideHeld {
		t.Fatalf("with duty %+v", with)
	}
}

func TestProposePickingStored_RejectsAndPassesStoreErrors(t *testing.T) {
	for name, body := range map[string]string{
		"item not a uuid":  `{"itemId":"x","postingDate":"2027-01-15","qty":"1","onHand":[{"batchId":"` + batchOld + `","qty":"1"}]}`,
		"no batches":       `{"itemId":"` + pickItem + `","postingDate":"2027-01-15","qty":"1","onHand":[]}`,
		"duplicate batch":  `{"itemId":"` + pickItem + `","postingDate":"2027-01-15","qty":"1","onHand":[{"batchId":"` + batchOld + `","qty":"1"},{"batchId":"` + batchOld + `","qty":"1"}]}`,
		"negative on hand": `{"itemId":"` + pickItem + `","postingDate":"2027-01-15","qty":"1","onHand":[{"batchId":"` + batchOld + `","qty":"-1"}]}`,
		"bad posting date": `{"itemId":"` + pickItem + `","postingDate":"soon","qty":"1","onHand":[{"batchId":"` + batchOld + `","qty":"1"}]}`,
		"zero need":        `{"itemId":"` + pickItem + `","postingDate":"2027-01-15","qty":"0","onHand":[{"batchId":"` + batchOld + `","qty":"1"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			rec := post(pickStoredPath, body, DutyRead).do(newRouter(storedFake()))
			if rec.Code != http.StatusBadRequest || problemOf(t, rec).Code != "COM-0001" {
				t.Fatalf("got %d %s", rec.Code, rec.Body)
			}
		})
	}
	missing := &fakeStore{err: httpx.NewProblem(http.StatusNotFound, store.CodeBatchNotFound, "batch not found")}
	if rec := post(pickStoredPath, storedBody, DutyRead).do(newRouter(missing)); rec.Code != http.StatusNotFound || problemOf(t, rec).Code != "PMF-0108" {
		t.Fatalf("unknown batch: %d %s", rec.Code, rec.Body)
	}
	if rec := post(pickStoredPath, storedBody, "production.other").do(newRouter(storedFake())); rec.Code != http.StatusForbidden {
		t.Fatalf("no duty: %d", rec.Code)
	}
}

// An id in another spelling of the same UUID is the same batch: it is sent to
// the store in canonical form and cannot be listed twice.
func TestProposePickingStored_CanonicalIDs(t *testing.T) {
	f := storedFake()
	body := `{"itemId":"` + strings.ToUpper(pickItem) + `","postingDate":"2027-01-15","qty":"10",
		"onHand":[{"batchId":"` + strings.ToUpper(batchOld) + `","qty":"50"}]}`
	rec := post(pickStoredPath, body, DutyRead).do(newRouter(f))
	if rec.Code != http.StatusOK || f.gotItem != pickItem || len(f.gotOnHand) != 1 || f.gotOnHand[0].BatchID != batchOld {
		t.Fatalf("status %d item %q onhand %+v: %s", rec.Code, f.gotItem, f.gotOnHand, rec.Body)
	}
	twice := `{"itemId":"` + pickItem + `","postingDate":"2027-01-15","qty":"10",
		"onHand":[{"batchId":"` + batchOld + `","qty":"1"},{"batchId":"` + strings.ToUpper(batchOld) + `","qty":"1"}]}`
	if rec := post(pickStoredPath, twice, DutyRead).do(newRouter(storedFake())); rec.Code != http.StatusBadRequest {
		t.Fatalf("same batch in two spellings: %d %s", rec.Code, rec.Body)
	}
}

// The caller-supplied proposal cannot restate a registered batch: one on hold
// in the register, sent as available, is refused with PMF-0112 instead of
// being proposed. Ids that are not UUIDs are never looked up.
func TestProposePicking_RefusesRegisteredBatches(t *testing.T) {
	body := `{"postingDate":"2027-01-15","qty":"10","batches":[{"id":"OLD","qty":"5"},{"id":"` + strings.ToUpper(batchOnH) + `","disposition":"available","qty":"100"}]}`
	f := &fakeStore{registered: []string{batchOnH}}
	rec := post(pickingPath, body, DutyRead).do(newRouter(f))
	if rec.Code != http.StatusConflict || problemOf(t, rec).Code != CodeBatchRegistered {
		t.Fatalf("registered batch: %d %s", rec.Code, rec.Body)
	}
	if len(f.gotIDs) != 1 || f.gotIDs[0] != batchOnH {
		t.Fatalf("looked up %v", f.gotIDs)
	}
	free := &fakeStore{}
	if rec := post(pickingPath, body, DutyRead).do(newRouter(free)); rec.Code != http.StatusOK {
		t.Fatalf("unregistered batches: %d %s", rec.Code, rec.Body)
	}
	plain := &fakeStore{registered: []string{batchOnH}}
	if rec := post(pickingPath, `{"postingDate":"2027-01-15","qty":"10","batches":[{"id":"OLD","qty":"5"}]}`, DutyRead).do(newRouter(plain)); rec.Code != http.StatusOK || plain.gotIDs != nil {
		t.Fatalf("no uuid ids: %d looked up %v", rec.Code, plain.gotIDs)
	}
}
