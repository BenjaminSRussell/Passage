package api_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

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
	base := newRig(domain.PeriodOpen)
	broken := &commitFails{Store: base.st}
	a := api.New(broken, api.Ports{
		Periods: domain.FakePeriods{Status: domain.PeriodOpen}, Ledger: base.ledger,
		Profile: domain.PostingProfile{Inventory: "1400", GRIR: "2100", PPV: "5200", VendorControl: "2000"},
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

	got := decode[receiptJSON](t, rg.call(http.MethodPost, path+":correct", correction("-1"), rg.poster(r.Version)), http.StatusOK)
	if got.Lines[0].Qty != "3" {
		t.Fatalf("repeating the original correction left qty %s, want 3", got.Lines[0].Qty)
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
