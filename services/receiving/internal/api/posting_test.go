package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/api"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store/memstore"
)

var errCommit = errors.New("commit failed after the ledger accepted the entry")

type commitFails struct {
	*memstore.Store
	failing bool
}

func (c *commitFails) Post(ctx context.Context, id string, fn store.PostFunc) (store.Receipt, error) {
	if !c.failing {
		return c.Store.Post(ctx, id, fn)
	}
	rec, err := c.Store.GetReceipt(ctx, id)
	if err != nil {
		return store.Receipt{}, err
	}
	if _, err := fn(rec); err != nil {
		return store.Receipt{}, err
	}
	return store.Receipt{}, errCommit
}

func (c *commitFails) Correct(ctx context.Context, id string, fn store.AdjustFunc) (store.Receipt, error) {
	if !c.failing {
		return c.Store.Correct(ctx, id, fn)
	}
	rec, err := c.Store.GetReceipt(ctx, id)
	if err != nil {
		return store.Receipt{}, err
	}
	if _, err := fn(rec); err != nil {
		return store.Receipt{}, err
	}
	return store.Receipt{}, errCommit
}

func (c *commitFails) Cancel(ctx context.Context, id string, fn store.AdjustFunc) (store.Receipt, error) {
	if !c.failing {
		return c.Store.Cancel(ctx, id, fn)
	}
	rec, err := c.Store.GetReceipt(ctx, id)
	if err != nil {
		return store.Receipt{}, err
	}
	if _, err := fn(rec); err != nil {
		return store.Receipt{}, err
	}
	return store.Receipt{}, errCommit
}

func (c *commitFails) PostInvoice(ctx context.Context, in store.Invoice, fn store.InvoiceFunc) (store.Invoice, error) {
	if !c.failing {
		return c.Store.PostInvoice(ctx, in, fn)
	}
	page, err := c.Store.ListGrir(ctx, store.GrirFilter{})
	if err != nil {
		return store.Invoice{}, err
	}
	items := make([]*domain.GrirItem, len(in.Lines))
	for i, l := range in.Lines {
		for _, g := range page.Items {
			if g.ReceiptID == l.ReceiptID && g.LineNo == l.ReceiptLineNo {
				items[i] = &domain.GrirItem{
					ReceiptID: g.ReceiptID, LineNo: g.LineNo, ReceivedQty: g.ReceivedQty, InvoicedQty: g.InvoicedQty,
					ReceivedAmount: money.Amount{Value: g.ReceivedAmount, Currency: g.Currency}, ClearedAmount: money.Amount{Value: g.ClearedAmount, Currency: g.Currency},
				}
			}
		}
	}
	if _, err := fn(items); err != nil {
		return store.Invoice{}, err
	}
	return store.Invoice{}, errCommit
}

func brokenRig(t *testing.T) (*rig, *commitFails) {
	t.Helper()
	return brokenRigWith(t, time.Nanosecond, nil)
}

func brokenRigWith(t *testing.T, minAge time.Duration, ledger func(*domain.FakeLedger) domain.LedgerPoster) (*rig, *commitFails) {
	t.Helper()
	base := newRig(domain.PeriodOpen)
	broken := &commitFails{Store: base.st}
	var poster domain.LedgerPoster = base.ledger
	if ledger != nil {
		poster = ledger(base.ledger)
	}
	a := api.New(broken, api.Ports{
		Periods: domain.FakePeriods{Status: domain.PeriodOpen}, Ledger: poster,
		Profile:       domain.PostingProfile{Inventory: "1400", GRIR: "2100", PPV: "5200", VendorControl: "2000"},
		ResolveMinAge: minAge,
	})
	router := httpx.NewRouter()
	a.Register(router)
	base.h, base.api = router, a
	return base, broken
}

func correction(delta string) string {
	return `{"lines":[{"lineNo":1,"qtyDelta":` + delta + `}]}`
}

func TestPending_CorrectionThatCommittedInTheLedgerBlocksEverythingButItsOwnRepeat(t *testing.T) {
	rg, broken := brokenRig(t)
	r := rg.posted(t, "PS-1", 10, 4)
	path := receiptsPath + "/" + r.ID

	broken.failing = true
	problemCode(t, rg.call(http.MethodPost, path+":correct", correction("-1"), rg.poster(r.Version)), http.StatusInternalServerError, "COM-0500")
	broken.failing = false
	posted := len(rg.ledger.Entries)

	problemCode(t, rg.call(http.MethodPost, path+":correct", correction("-2"), rg.poster(r.Version)), http.StatusConflict, "RCV-IDM-001")
	problemCode(t, rg.call(http.MethodPost, path+":cancel", "", rg.poster(r.Version)), http.StatusConflict, "RCV-IDM-001")
	if len(rg.ledger.Entries) != posted {
		t.Fatalf("a blocked command reached the ledger: %d entries, want %d", len(rg.ledger.Entries), posted)
	}

	balance := rg.ledger.Balance("2100").String()
	got := decode[receiptJSON](t, rg.call(http.MethodPost, path+":correct", correction("-1"), rg.poster(r.Version)), http.StatusOK)
	if got.Lines[0].Qty != "3" {
		t.Fatalf("repeating the original correction left qty %s, want 3", got.Lines[0].Qty)
	}
	if len(rg.ledger.Entries) != posted || rg.ledger.Balance("2100").String() != balance {
		t.Fatalf("the repeat posted again: %d entries (want %d), GR/IR %s (want %s)", len(rg.ledger.Entries), posted, rg.ledger.Balance("2100"), balance)
	}
	decode[receiptJSON](t, rg.call(http.MethodPost, path+":cancel", "", rg.poster(got.Version)), http.StatusOK)
}

func TestPending_PostThatCommittedInTheLedgerCanBeRepeatedButNotCancelled(t *testing.T) {
	rg, broken := brokenRig(t)
	r := rg.direct(t, "PS-1", 10, 4)
	path := receiptsPath + "/" + r.ID

	broken.failing = true
	problemCode(t, rg.call(http.MethodPost, path+":post", "", rg.poster(r.Version)), http.StatusInternalServerError, "COM-0500")
	broken.failing = false

	problemCode(t, rg.call(http.MethodPost, path+":cancel", "", rg.poster(r.Version)), http.StatusConflict, "RCV-IDM-001")
	got := decode[receiptJSON](t, rg.call(http.MethodPost, path+":post", "", rg.poster(r.Version)), http.StatusOK)
	if got.State != "POSTED" {
		t.Fatalf("state %s, want POSTED", got.State)
	}
}

func TestPending_InvoiceWithTheSameNumberAndEditedLinesIsRefused(t *testing.T) {
	rg, broken := brokenRig(t)
	r := rg.posted(t, "PS-1", 10, 4)

	broken.failing = true
	problemCode(t, rg.call(http.MethodPost, invoicesPath, invoiceBody("INV-1", r.ID, 2, ""), rg.apPoster()), http.StatusInternalServerError, "COM-0500")
	broken.failing = false
	posted := len(rg.ledger.Entries)

	problemCode(t, rg.call(http.MethodPost, invoicesPath, invoiceBody("INV-1", r.ID, 3, ""), rg.apPoster()), http.StatusConflict, "RCV-IDM-001")
	if len(rg.ledger.Entries) != posted {
		t.Fatalf("an edited invoice reached the ledger")
	}
	got := decode[invoiceJSON](t, rg.call(http.MethodPost, invoicesPath, invoiceBody("INV-1", r.ID, 2, ""), rg.apPoster()), http.StatusCreated)
	if len(rg.ledger.Entries) != posted {
		t.Fatalf("the repeat posted the invoice to the ledger again: %d entries, want %d", len(rg.ledger.Entries), posted)
	}
	if got.Total != "5.00" {
		t.Fatalf("invoice total %s, want 5.00", got.Total)
	}
}

func TestPending_RejectedPostingIsReleasedSoALaterDifferentCommandWorks(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	r := rg.posted(t, "PS-1", 10, 4)
	path := receiptsPath + "/" + r.ID + ":correct"

	rg.ledger.Err = domain.ErrPeriodClosed
	problemCode(t, rg.call(http.MethodPost, path, correction("-1"), rg.poster(r.Version)), http.StatusUnprocessableEntity, "PER-AP-001")
	rg.ledger.Err = nil
	decode[receiptJSON](t, rg.call(http.MethodPost, path, correction("-2"), rg.poster(r.Version)), http.StatusOK)
}

func TestPending_LedgerIdempotencyConflictIsAConflictNotAnInternalError(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	r := rg.direct(t, "PS-1", 10, 4)
	rg.ledger.Err = domain.ErrLedgerConflict
	problemCode(t, rg.call(http.MethodPost, receiptsPath+"/"+r.ID+":post", "", rg.poster(r.Version)), http.StatusConflict, "RCV-IDM-001")
}

func TestAdjustments_PostingDateReachesTheLedger(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	r := rg.posted(t, "PS-1", 10, 4)
	path := receiptsPath + "/" + r.ID
	dated := `{"postingDate":"2026-11-02","lines":[{"lineNo":1,"qtyDelta":-1}]}`
	got := decode[receiptJSON](t, rg.call(http.MethodPost, path+":correct", dated, rg.poster(r.Version)), http.StatusOK)
	if d := rg.ledger.Entries[len(rg.ledger.Entries)-1].PostingDate.Format("2006-01-02"); d != "2026-11-02" {
		t.Fatalf("correction posted on %s, want 2026-11-02", d)
	}
	problemCode(t, rg.call(http.MethodPost, path+":correct", `{"postingDate":"2026-10-01","lines":[{"lineNo":1,"qtyDelta":-1}]}`, rg.poster(got.Version)), http.StatusUnprocessableEntity, "RCV-VAL-001")
	problemCode(t, rg.call(http.MethodPost, path+":correct", `{"postingDate":"nope","lines":[{"lineNo":1,"qtyDelta":-1}]}`, rg.poster(got.Version)), http.StatusBadRequest, "COM-0001")
	problemCode(t, rg.call(http.MethodPost, path+":cancel", `{"postingDate":"2026-10-01"}`, rg.poster(got.Version)), http.StatusUnprocessableEntity, "RCV-VAL-001")
	decode[receiptJSON](t, rg.call(http.MethodPost, path+":cancel", `{"postingDate":"2026-11-05"}`, rg.poster(got.Version)), http.StatusOK)
	if d := rg.ledger.Entries[len(rg.ledger.Entries)-1].PostingDate.Format("2006-01-02"); d != "2026-11-05" {
		t.Fatalf("cancellation posted on %s, want 2026-11-05", d)
	}
}

func TestPending_ConflictBodyNamesTheKeyAndStartTime(t *testing.T) {
	rg, broken := brokenRig(t)
	r := rg.posted(t, "PS-1", 10, 4)
	broken.failing = true
	rg.call(http.MethodPost, receiptsPath+"/"+r.ID+":correct", correction("-1"), rg.poster(r.Version))
	broken.failing = false
	rec := rg.call(http.MethodPost, receiptsPath+"/"+r.ID+":cancel", "", rg.poster(r.Version))
	problemCode(t, rec, http.StatusConflict, "RCV-IDM-001")
	var body struct {
		Errors map[string]string `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Errors["pendingKey"] != "post:receiving:productreceipt:"+r.ID+":correction:1" || body.Errors["createdAt"] == "" {
		t.Fatalf("problem errors %+v", body.Errors)
	}
}

func TestPending_AnInvoiceInLimboBlocksCorrectionAndCancellation(t *testing.T) {
	rg, broken := brokenRig(t)
	r := rg.posted(t, "PS-1", 10, 4)
	broken.failing = true
	problemCode(t, rg.call(http.MethodPost, invoicesPath, invoiceBody("INV-1", r.ID, 2, ""), rg.apPoster()), http.StatusInternalServerError, "COM-0500")
	broken.failing = false
	posted := len(rg.ledger.Entries)

	problemCode(t, rg.call(http.MethodPost, receiptsPath+"/"+r.ID+":correct", correction("-1"), rg.poster(r.Version)), http.StatusConflict, "RCV-IDM-001")
	problemCode(t, rg.call(http.MethodPost, receiptsPath+"/"+r.ID+":cancel", "", rg.poster(r.Version)), http.StatusConflict, "RCV-IDM-001")
	if len(rg.ledger.Entries) != posted {
		t.Fatalf("a blocked command reached the ledger")
	}
	decode[invoiceJSON](t, rg.call(http.MethodPost, invoicesPath, invoiceBody("INV-1", r.ID, 2, ""), rg.apPoster()), http.StatusCreated)
	decode[receiptJSON](t, rg.call(http.MethodPost, receiptsPath+"/"+r.ID+":correct", correction("-1"), rg.poster(r.Version)), http.StatusOK)
}

func TestPending_ACorrectionInLimboBlocksInvoicesOnThatReceipt(t *testing.T) {
	rg, broken := brokenRig(t)
	r := rg.posted(t, "PS-1", 10, 4)
	other := rg.posted(t, "PS-2", 10, 4)
	broken.failing = true
	rg.call(http.MethodPost, receiptsPath+"/"+r.ID+":correct", correction("-1"), rg.poster(r.Version))
	broken.failing = false
	posted := len(rg.ledger.Entries)

	problemCode(t, rg.call(http.MethodPost, invoicesPath, invoiceBody("INV-1", r.ID, 1, ""), rg.apPoster()), http.StatusConflict, "RCV-IDM-001")
	decode[invoiceJSON](t, rg.call(http.MethodPost, invoicesPath, invoiceBody("INV-2", other.ID, 1, ""), rg.apPoster()), http.StatusCreated)
	if len(rg.ledger.Entries) != posted+1 {
		t.Fatalf("only the unrelated receipt's invoice may reach the ledger: %d entries, want %d", len(rg.ledger.Entries), posted+1)
	}
}

func admin(key string) headers { return duty("ReceivingAdmin", key) }

func resolvePath(key string) string {
	return "/v1/receiving/pending-postings/" + url.PathEscape(key) + "/resolve"
}

func TestResolve_ReversesAnUnconfirmedPostingAndFreesTheReceipt(t *testing.T) {
	rg, broken := brokenRig(t)
	r := rg.posted(t, "PS-1", 10, 4)
	path := receiptsPath + "/" + r.ID
	broken.failing = true
	rg.call(http.MethodPost, path+":correct", correction("-1"), rg.poster(r.Version))
	broken.failing = false
	key := "post:receiving:productreceipt:" + r.ID + ":correction:1"

	list := decode[struct{ Items []struct{ Key string } }](t, rg.call(http.MethodGet, "/v1/receiving/pending-postings", "", admin("")), http.StatusOK)
	if len(list.Items) != 1 || list.Items[0].Key != key {
		t.Fatalf("pending list %+v", list)
	}
	problemCode(t, rg.call(http.MethodPost, resolvePath(key), "", duty("ReceivingPost", "k1")), http.StatusForbidden, "AUTH-0003")
	problemCode(t, rg.call(http.MethodPost, resolvePath("nope"), "", admin("k2")), http.StatusNotFound, "RCV-NF-003")

	rg.ledger.Err = errors.New("ledger unreachable")
	problemCode(t, rg.call(http.MethodPost, resolvePath(key), "", admin("k3")), http.StatusServiceUnavailable, "COM-0503")
	rg.ledger.Err = nil
	problemCode(t, rg.call(http.MethodPost, path+":cancel", "", rg.poster(r.Version)), http.StatusConflict, "RCV-IDM-001")

	got := decode[struct{ Outcome, LedgerEntryID string }](t, rg.call(http.MethodPost, resolvePath(key), "", admin("k4")), http.StatusOK)
	if got.Outcome != "REVERSED" || len(rg.ledger.Reversed) != 1 || rg.ledger.Reversed[0] != got.LedgerEntryID {
		t.Fatalf("resolve %+v reversed %v", got, rg.ledger.Reversed)
	}
	problemCode(t, rg.call(http.MethodPost, resolvePath(key), "", admin("k5")), http.StatusConflict, "RCV-STA-001")

	redone := decode[receiptJSON](t, rg.call(http.MethodPost, path+":correct", correction("-2"), rg.poster(r.Version)), http.StatusOK)
	if redone.Lines[0].Qty != "2" {
		t.Fatalf("redone correction left qty %s, want 2", redone.Lines[0].Qty)
	}
	if last := rg.ledger.Entries[len(rg.ledger.Entries)-1].IdempotencyKey; last != key+":r1" {
		t.Fatalf("the redo used ledger key %q, want a new generation %q", last, key+":r1")
	}
	after := decode[struct{ Items []struct{ Key string } }](t, rg.call(http.MethodGet, "/v1/receiving/pending-postings", "", admin("")), http.StatusOK)
	if len(after.Items) != 0 {
		t.Fatalf("pending list after resolve and redo: %+v", after)
	}
}

func TestResolve_ARejectedResendJustFreesTheSubjects(t *testing.T) {
	rg, broken := brokenRig(t)
	r := rg.posted(t, "PS-1", 10, 4)
	broken.failing = true
	rg.call(http.MethodPost, receiptsPath+"/"+r.ID+":correct", correction("-1"), rg.poster(r.Version))
	broken.failing = false
	key := "post:receiving:productreceipt:" + r.ID + ":correction:1"
	rg.ledger.Err = domain.ErrLedgerRejected
	got := decode[struct{ Outcome string }](t, rg.call(http.MethodPost, resolvePath(key), "", admin("k1")), http.StatusOK)
	if got.Outcome != "REJECTED" || len(rg.ledger.Reversed) != 0 {
		t.Fatalf("outcome %s reversed %v", got.Outcome, rg.ledger.Reversed)
	}
	rg.ledger.Err = nil
	decode[receiptJSON](t, rg.call(http.MethodPost, receiptsPath+"/"+r.ID+":cancel", "", rg.poster(r.Version)), http.StatusOK)
}

func TestPending_DefiniteRejectionsReleaseAndAmbiguousFailuresKeep(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	r := rg.posted(t, "PS-1", 10, 4)
	path := receiptsPath + "/" + r.ID + ":correct"
	rg.ledger.Err = domain.ErrLedgerRejected
	problemCode(t, rg.call(http.MethodPost, path, correction("-1"), rg.poster(r.Version)), http.StatusInternalServerError, "COM-0500")
	rg.ledger.Err = nil
	decode[receiptJSON](t, rg.call(http.MethodPost, path, correction("-2"), rg.poster(r.Version)), http.StatusOK)

	rg.ledger.Err = errors.New("timeout")
	problemCode(t, rg.call(http.MethodPost, path, correction("-1"), rg.poster(r.Version+1)), http.StatusInternalServerError, "COM-0500")
	rg.ledger.Err = nil
	problemCode(t, rg.call(http.MethodPost, path, correction("-1.5"), rg.poster(r.Version+1)), http.StatusConflict, "RCV-IDM-001")
}

func TestMetrics_ExposeTheOldestUnfinishedPosting(t *testing.T) {
	rg, broken := brokenRig(t)
	r := rg.posted(t, "PS-1", 10, 4)
	broken.failing = true
	rg.call(http.MethodPost, receiptsPath+"/"+r.ID+":correct", correction("-1"), rg.poster(r.Version))
	rec := httptest.NewRecorder()
	rg.api.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, want := range []string{"receiving_pending_postings_open 1", "receiving_pending_postings_oldest_age_seconds 0"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("metrics missing %q in %s", want, rec.Body.String())
		}
	}
}

type reverseFails struct {
	*domain.FakeLedger
	err   error
	dates *[]time.Time
}

func (r reverseFails) Reverse(_ context.Context, _, _ string, postingDate time.Time) error {
	*r.dates = append(*r.dates, postingDate)
	return r.err
}

type hookedStore struct {
	*commitFails
	beforeCommit func(ctx context.Context)
}

func (h hookedStore) Correct(ctx context.Context, id string, fn store.AdjustFunc) (store.Receipt, error) {
	return h.commitFails.Correct(ctx, id, func(rec store.Receipt) (store.Adjustment, error) {
		adj, err := fn(rec)
		if err == nil {
			h.beforeCommit(ctx)
		}
		return adj, err
	})
}

func TestResolve_RefusesAPostingYoungerThanTheCommandTimeouts(t *testing.T) {
	rg, broken := brokenRigWith(t, time.Hour, nil)
	r := rg.posted(t, "PS-1", 10, 4)
	broken.failing = true
	rg.call(http.MethodPost, receiptsPath+"/"+r.ID+":correct", correction("-1"), rg.poster(r.Version))
	broken.failing = false
	key := "post:receiving:productreceipt:" + r.ID + ":correction:1"

	problemCode(t, rg.call(http.MethodPost, resolvePath(key), "", admin("k1")), http.StatusConflict, "RCV-IDM-001")
	if len(rg.ledger.Reversed) != 0 {
		t.Fatalf("a posting whose command may still be running was reversed: %v", rg.ledger.Reversed)
	}
}

func TestResolve_ACommandThatCommitsFirstLeavesNothingToReverse(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	r := rg.posted(t, "PS-1", 10, 4)
	decode[receiptJSON](t, rg.call(http.MethodPost, receiptsPath+"/"+r.ID+":correct", correction("-1"), rg.poster(r.Version)), http.StatusOK)
	key := "post:receiving:productreceipt:" + r.ID + ":correction:1"

	problemCode(t, rg.call(http.MethodPost, resolvePath(key), "", admin("k1")), http.StatusConflict, "RCV-STA-001")
	if len(rg.ledger.Reversed) != 0 {
		t.Fatalf("a posting its command already completed was reversed: %v", rg.ledger.Reversed)
	}
	if got := decode[receiptJSON](t, rg.call(http.MethodGet, receiptsPath+"/"+r.ID, "", duty("ReceivingMaintain", "")), http.StatusOK); got.Lines[0].Qty != "3" {
		t.Fatalf("qty %s, want 3", got.Lines[0].Qty)
	}
}

func TestResolve_AResolveThatReversesWhileTheCommandIsInFlightFailsTheCommand(t *testing.T) {
	base := newRig(domain.PeriodOpen)
	broken := &commitFails{Store: base.st}
	hooked := &hookedStore{commitFails: broken}
	a := api.New(hooked, api.Ports{
		Periods: domain.FakePeriods{Status: domain.PeriodOpen}, Ledger: base.ledger,
		Profile:       domain.PostingProfile{Inventory: "1400", GRIR: "2100", PPV: "5200", VendorControl: "2000"},
		ResolveMinAge: time.Nanosecond,
	})
	router := httpx.NewRouter()
	a.Register(router)
	base.h, base.api = router, a
	r := base.posted(t, "PS-1", 10, 4)
	key := "post:receiving:productreceipt:" + r.ID + ":correction:1"
	hooked.beforeCommit = func(ctx context.Context) {
		if _, err := base.st.ClaimPending(ctx, key, 0); err != nil {
			t.Errorf("claim: %v", err)
		}
		if err := base.st.FinishPending(ctx, key, store.OutcomeReversed); err != nil {
			t.Errorf("finish: %v", err)
		}
	}
	problemCode(t, base.call(http.MethodPost, receiptsPath+"/"+r.ID+":correct", correction("-1"), base.poster(r.Version)), http.StatusConflict, "RCV-IDM-001")
	got := decode[receiptJSON](t, base.call(http.MethodGet, receiptsPath+"/"+r.ID, "", duty("ReceivingMaintain", "")), http.StatusOK)
	if got.Lines[0].Qty != "4" || got.Version != r.Version {
		t.Fatalf("receipt qty %s version %d after the superseded command, want 4 and %d", got.Lines[0].Qty, got.Version, r.Version)
	}
}

func TestResolve_LedgerRejectionsOnReverseAreNon422AndReleaseTheClaim(t *testing.T) {
	var dates []time.Time
	failure := errors.New("unreachable")
	rg, broken := brokenRig(t)
	r := rg.posted(t, "PS-1", 10, 4)
	broken.failing = true
	rg.call(http.MethodPost, receiptsPath+"/"+r.ID+":correct", correction("-1"), rg.poster(r.Version))
	broken.failing = false
	key := "post:receiving:productreceipt:" + r.ID + ":correction:1"

	for i, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{fmt.Errorf("ledger reverse: %w", domain.ErrPeriodClosed), http.StatusUnprocessableEntity, "PER-AP-001"},
		{fmt.Errorf("ledger reverse: %w", domain.ErrLedgerRejected), http.StatusUnprocessableEntity, "RCV-LDG-001"},
		{failure, http.StatusServiceUnavailable, "COM-0503"},
	} {
		failureLedger := reverseFails{FakeLedger: rg.ledger, err: tc.err, dates: &dates}
		a := api.New(broken, api.Ports{
			Periods: domain.FakePeriods{Status: domain.PeriodOpen}, Ledger: failureLedger,
			Profile:       domain.PostingProfile{Inventory: "1400", GRIR: "2100", PPV: "5200", VendorControl: "2000"},
			ResolveMinAge: time.Nanosecond,
		})
		router := httpx.NewRouter()
		a.Register(router)
		problemCode(t, doWith(router, http.MethodPost, resolvePath(key), "", admin("k"+strconv.Itoa(i))), tc.status, tc.code)
	}
	if len(dates) != 3 || dates[0].IsZero() {
		t.Fatalf("reverse posting dates %v, want three non-zero dates", dates)
	}
	got := decode[struct{ Outcome string }](t, rg.call(http.MethodPost, resolvePath(key), "", admin("final")), http.StatusOK)
	if got.Outcome != "REVERSED" {
		t.Fatalf("outcome %s: a failed resolve must release its claim so a later one can finish", got.Outcome)
	}
}

func TestPending_InvoiceTrackerStaysOpenUntilTheCommitSucceeds(t *testing.T) {
	rg, broken := brokenRig(t)
	r := rg.posted(t, "PS-1", 10, 4)
	metric := func() string {
		rec := httptest.NewRecorder()
		rg.api.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		return rec.Body.String()
	}
	broken.failing = true
	problemCode(t, rg.call(http.MethodPost, invoicesPath, invoiceBody("INV-1", r.ID, 2, ""), rg.apPoster()), http.StatusInternalServerError, "COM-0500")
	if !strings.Contains(metric(), "receiving_pending_postings_open 1") {
		t.Fatalf("a failed invoice commit left the gauge at zero: %s", metric())
	}
	broken.failing = false
	decode[invoiceJSON](t, rg.call(http.MethodPost, invoicesPath, invoiceBody("INV-1", r.ID, 2, ""), rg.apPoster()), http.StatusCreated)
	if !strings.Contains(metric(), "receiving_pending_postings_open 0") {
		t.Fatalf("a committed invoice left the gauge open: %s", metric())
	}
}
