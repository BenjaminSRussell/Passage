package store

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
)

type fakeGate struct {
	status string
	err    error
	calls  []fakeGateCall
}

type fakeGateCall struct {
	legalEntityID, ledgerID, sourceSystem string
	postingDate                           time.Time
}

func (g *fakeGate) Status(_ context.Context, legalEntityID, ledgerID string, postingDate time.Time, sourceSystem string) (string, error) {
	g.calls = append(g.calls, fakeGateCall{legalEntityID, ledgerID, sourceSystem, postingDate})
	return g.status, g.err
}

func withDuty(e env, duty string) context.Context {
	duties := testkit.ScopeDuties(e.le)
	duties[duty] = struct{}{}
	return tenant.With(context.Background(), tenant.Principal{TenantID: e.tenant, LegalEntityID: e.le, UserID: "controller", Duties: duties})
}

func TestPostgres_CloseStatusRefusesEveryPostingPath(t *testing.T) {
	for _, status := range []string{"closed", "permanently_closed", "never_opened", "on_hold"} {
		t.Run(status, func(t *testing.T) {
			e := setupLedgerSet(t)
			putName(t, e.env, "GJ", JournalName{Active: true})
			orig, err := e.store.PostEntry(e.ctx, vendorInvoice(e.ledger.ID, "OPEN-1", "100.00"))
			if err != nil {
				t.Fatalf("post while open: %v", err)
			}
			draft, err := e.store.CreateBatch(e.ctx, batchOf(e.env, "GJ", "55"))
			if err != nil {
				t.Fatalf("create batch: %v", err)
			}
			validated, err := e.store.SubmitBatch(e.ctx, draft.ID, 1, testToday)
			if err != nil {
				t.Fatalf("submit batch: %v", err)
			}
			before := entryTotal(t, e.env)
			gate := &fakeGate{status: status}
			ctx := WithPeriodGate(e.ctx, gate)

			t.Run("direct post", func(t *testing.T) {
				_, err := e.store.PostEntry(ctx, vendorInvoice(e.ledger.ID, "CLOSED-1", "10.00"))
				requirePeriodClosed(t, "PostEntry", err)
			})
			t.Run("validate only", func(t *testing.T) {
				res, err := e.store.ValidateEntry(ctx, vendorInvoice(e.ledger.ID, "CLOSED-V", "10.00"))
				if err != nil || res.Valid() || res.Violations[0].Code != "LEDGER_PERIOD_CLOSED" {
					t.Fatalf("ValidateEntry: %+v %v", res, err)
				}
			})
			t.Run("ledger set post", func(t *testing.T) {
				_, err := e.store.PostSetEntry(ctx, e.set.ID, vendorInvoice("", "CLOSED-SET", "10.00"))
				requirePeriodClosed(t, "PostSetEntry", err)
			})
			t.Run("reversal", func(t *testing.T) {
				hash, _ := domain.HashPayload("reverse closed")
				_, err := e.store.ReverseEntry(ctx, ReverseInput{EntryID: orig.Entries[0].ID, Key: "REV-CLOSED", Hash: hash, Today: testToday})
				requirePeriodClosed(t, "ReverseEntry", err)
			})
			t.Run("batch post", func(t *testing.T) {
				_, err := postBatch(e.env, ctx, validated, "BATCH-CLOSED", true)
				requirePeriodClosed(t, "PostBatch", err)
			})

			if after := entryTotal(t, e.env); after != before {
				t.Fatalf("a refused posting wrote entries: %d before, %d after", before, after)
			}
			first := gate.calls[0]
			if first.legalEntityID != e.le || first.ledgerID != e.ledger.ID || first.sourceSystem != "ap" || !first.postingDate.Equal(testPostingDate) {
				t.Fatalf("gate asked about the wrong period: %+v", first)
			}
		})
	}
}

func TestPostgres_CloseStatusOpenOrUnregisteredPosts(t *testing.T) {
	cases := map[string]*fakeGate{
		"open":         {status: "open"},
		"unregistered": {err: ErrNoFiscalPeriod},
	}
	for name, gate := range cases {
		t.Run(name, func(t *testing.T) {
			e, _ := setup(t)
			if _, err := e.store.PostEntry(WithPeriodGate(e.ctx, gate), vendorInvoice(e.ledger.ID, "OK-1", "10.00")); err != nil {
				t.Fatalf("post: %v", err)
			}
			if len(gate.calls) == 0 {
				t.Fatal("the gate was never asked")
			}
		})
	}
}

func TestPostgres_ClosePeriodDutyOverridesClosedAndOnHoldOnly(t *testing.T) {
	for status, allowed := range map[string]bool{"closed": true, "on_hold": true, "permanently_closed": false, "never_opened": false} {
		t.Run(status, func(t *testing.T) {
			e, _ := setup(t)
			holder := WithPeriodGate(withDuty(e, DutyPostClosedPeriod), &fakeGate{status: status})
			_, err := e.store.PostEntry(holder, vendorInvoice(e.ledger.ID, "ADJ-1", "10.00"))
			if allowed && err != nil {
				t.Fatalf("holder post into %s: %v", status, err)
			}
			if !allowed {
				requirePeriodClosed(t, "holder PostEntry into "+status, err)
			}
			other := WithPeriodGate(e.ctx, &fakeGate{status: status})
			_, err = e.store.PostEntry(other, vendorInvoice(e.ledger.ID, "ADJ-2", "10.00"))
			requirePeriodClosed(t, "PostEntry without duty into "+status, err)
		})
	}
}

func TestPostgres_CloseIsNotAskedAboutAnInvalidEntry(t *testing.T) {
	e, _ := setup(t)
	gate := &fakeGate{status: "open"}
	in := vendorInvoice(e.ledger.ID, "BAD-1", "10.00")
	in.Lines[1].Amount = in.Lines[1].Amount.Add(decimal.NewFromInt(1))
	res, err := e.store.ValidateEntry(WithPeriodGate(e.ctx, gate), in)
	if err != nil || res.Valid() {
		t.Fatalf("want local violations, got %+v %v", res, err)
	}
	if len(gate.calls) != 0 {
		t.Fatalf("close was asked about an entry that already failed: %+v", gate.calls)
	}
}

func TestPostgres_ReversalAsksAboutTheOriginalSourceModule(t *testing.T) {
	e, _ := setup(t)
	orig, err := e.store.PostEntry(e.ctx, vendorInvoice(e.ledger.ID, "REV-SRC", "10.00"))
	if err != nil {
		t.Fatal(err)
	}
	gate := &fakeGate{status: "open"}
	hash, _ := domain.HashPayload("reverse src")
	if _, err := e.store.ReverseEntry(WithPeriodGate(e.ctx, gate), ReverseInput{EntryID: orig.Entries[0].ID, Key: "REV-SRC-1", Hash: hash, Today: testToday, TraceID: testTrace}); err != nil {
		t.Fatal(err)
	}
	for _, c := range gate.calls {
		if c.sourceSystem != "ap" {
			t.Fatalf("reversal asked about module %q, want the original's ap", c.sourceSystem)
		}
	}
	if len(gate.calls) == 0 {
		t.Fatal("the gate was never asked")
	}
}

func TestPostgres_CloseUnreachableFailsClosed(t *testing.T) {
	e, _ := setup(t)
	before := entryTotal(t, e)
	ctx := WithPeriodGate(e.ctx, &fakeGate{err: errors.New("connection refused")})
	_, err := e.store.PostEntry(ctx, vendorInvoice(e.ledger.ID, "DOWN-1", "10.00"))
	if !isProblem(err, http.StatusServiceUnavailable, CodePeriodUnavailable) {
		t.Fatalf("want 503 %s, got %v", CodePeriodUnavailable, err)
	}
	if _, err := e.store.ValidateEntry(ctx, vendorInvoice(e.ledger.ID, "DOWN-V", "10.00")); !isProblem(err, http.StatusServiceUnavailable, CodePeriodUnavailable) {
		t.Fatalf("validate: want 503 %s, got %v", CodePeriodUnavailable, err)
	}
	if after := entryTotal(t, e); after != before {
		t.Fatalf("an unconfirmed period wrote entries: %d before, %d after", before, after)
	}
}

type scriptedGate struct {
	statuses []string
	calls    int
}

func (g *scriptedGate) Status(context.Context, string, string, time.Time, string) (string, error) {
	s := g.statuses[min(g.calls, len(g.statuses)-1)]
	g.calls++
	return s, nil
}

func TestPostgres_OpenIsReusedWithinOneRequestOnly(t *testing.T) {
	e, _ := setup(t)
	gate := &scriptedGate{statuses: []string{"open", "open", "closed"}}
	ctx := WithPeriodGate(e.ctx, gate)
	if _, err := e.store.PostEntry(ctx, vendorInvoice(e.ledger.ID, "REQ-1", "10.00")); err != nil {
		t.Fatal(err)
	}
	if gate.calls != 1 {
		t.Fatalf("the warm-up open must serve the in-transaction check, calls %d", gate.calls)
	}
	next := WithPeriodGate(e.ctx, gate)
	_, err := e.store.PostEntry(next, vendorInvoice(e.ledger.ID, "REQ-2", "10.00"))
	if gate.calls != 2 {
		t.Fatalf("a new request must ask close again, calls %d", gate.calls)
	}
	if err != nil {
		t.Fatalf("second request: %v", err)
	}
	gate.statuses = []string{"closed"}
	gate.calls = 0
	_, err = e.store.PostEntry(WithPeriodGate(e.ctx, gate), vendorInvoice(e.ledger.ID, "REQ-3", "10.00"))
	requirePeriodClosed(t, "request after close", err)
}
