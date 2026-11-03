package store_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

// A DRAFT order is announced by nothing until it is confirmed: edits stay silent, confirming
// writes transfer.changed with status CREATED, and the delivery header travels on the event.
func TestDraftTransferEditConfirmAndDeliveryHeader(t *testing.T) {
	s, _, admin := setup(t)
	le := ids.New()
	ctx := entityCtx(1, le)
	p := ids.New()
	in := transferOrder(ctx, p, "5", true)
	in.Status = domain.TransferDraft
	in.DeliveryTerms, in.ModeOfDelivery = "FCA", "ROAD"
	d, replayed, err := s.CreateTransfer(ctx, "draft-1", in)
	if err != nil || replayed || d.Status != domain.TransferDraft || d.DeliveryTerms != "FCA" || d.ModeOfDelivery != "ROAD" {
		t.Fatalf("create draft: %v replayed=%v %+v", err, replayed, d)
	}
	if again, replayed, err := s.CreateTransfer(ctx, "draft-1", in); err != nil || !replayed || again.ID != d.ID {
		t.Fatalf("replay: %v %v", err, replayed)
	}
	notDraft := in
	notDraft.Status = ""
	if _, _, err := s.CreateTransfer(ctx, "draft-1", notDraft); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("same key as a CREATED order: %v, want %s", err, store.CodeIdempotencyReuse)
	}
	// A draft cannot ship or be cancelled, and nothing is announced yet.
	if _, err := s.ShipTransfer(ctx, d.ID, d.Version, nil, shipDay, domain.NegativePolicy{}); problemCode(err) != domain.CodeTransferState {
		t.Fatalf("ship a draft: %v, want %s", err, domain.CodeTransferState)
	}
	if _, err := s.CancelTransfer(ctx, d.ID, d.Version); problemCode(err) != domain.CodeTransferState {
		t.Fatalf("cancel a draft: %v, want %s", err, domain.CodeTransferState)
	}
	terms, mode := "DAP", ""
	q := dec("9")
	edited, err := s.EditTransfer(ctx, d.ID, d.Version, domain.TransferEdit{DeliveryTerms: &terms, ModeOfDelivery: &mode,
		Update: []domain.LineEdit{{LineID: d.Lines[0].ID, Qty: &q}}})
	if err != nil || edited.Status != domain.TransferDraft || edited.DeliveryTerms != "DAP" || edited.ModeOfDelivery != "" || !edited.Lines[0].Qty.Equal(q) {
		t.Fatalf("edit draft: %v %+v", err, edited)
	}
	var n int
	count := func(id string) int {
		if err := admin.QueryRow(context.Background(), `select count(*) from outbox where type = 'dev.passage.inventory.transfer.changed.v1'
			and aggregate_id = $1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count(d.ID) != 0 {
		t.Fatalf("a draft was announced: %d events", n)
	}
	if _, err := s.ConfirmTransfer(ctx, d.ID, d.Version); problemCode(err) != store.CodeConflictVersion {
		t.Fatalf("stale confirm: %v, want %s", err, store.CodeConflictVersion)
	}
	if _, err := s.ConfirmTransfer(entityCtx(1, ids.New()), d.ID, edited.Version); problemCode(err) != domain.CodeTransferNotFound {
		t.Fatalf("confirm from another legal entity: %v", err)
	}
	got, err := s.ConfirmTransfer(ctx, d.ID, edited.Version)
	if err != nil || got.Status != domain.TransferCreated || got.Version != edited.Version+1 {
		t.Fatalf("confirm: %v %+v", err, got)
	}
	if _, err := s.ConfirmTransfer(ctx, d.ID, got.Version); problemCode(err) != domain.CodeTransferState {
		t.Fatalf("confirm twice: %v, want %s", err, domain.CodeTransferState)
	}
	if err := admin.QueryRow(context.Background(), `select count(*) from outbox where type = 'dev.passage.inventory.transfer.changed.v1'
		and aggregate_id = $1 and payload->'data'->>'status' = 'CREATED' and payload->'data'->>'deliveryTerms' = 'DAP'
		and payload->'data'->>'modeOfDelivery' is null and jsonb_array_length(payload->'data'->'lines') = 1`, d.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("confirm event: %d (%v), want 1", n, err)
	}
	if ds, err := s.ListTransfers(ctx, domain.TransferDraft); err != nil || len(ds) != 0 {
		t.Fatalf("drafts after confirm: %v %d", err, len(ds))
	}
}

// Deleting removes the order and its lines; a CREATED order announces it as DELETED, a draft
// does not; a shipped order cannot be deleted.
func TestDeleteTransferDraftCreatedAndShipped(t *testing.T) {
	s, _, admin := setup(t)
	le := ids.New()
	ctx := entityCtx(1, le)
	p := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(p, "100", "2", dims()))

	in := transferOrder(ctx, p, "5", true)
	in.Status = domain.TransferDraft
	d, _, err := s.CreateTransfer(ctx, "del-draft", in)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTransfer(ctx, d.ID, d.Version+1); problemCode(err) != store.CodeConflictVersion {
		t.Fatalf("stale delete: %v, want %s", err, store.CodeConflictVersion)
	}
	if err := s.DeleteTransfer(entityCtx(1, ids.New()), d.ID, d.Version); problemCode(err) != domain.CodeTransferNotFound {
		t.Fatalf("delete from another legal entity: %v", err)
	}
	if err := s.DeleteTransfer(ctx, d.ID, d.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetTransfer(ctx, d.ID); problemCode(err) != domain.CodeTransferNotFound {
		t.Fatalf("get a deleted draft: %v", err)
	}
	if err := s.DeleteTransfer(ctx, d.ID, d.Version); problemCode(err) != domain.CodeTransferNotFound {
		t.Fatalf("delete twice: %v", err)
	}
	var n int
	if err := admin.QueryRow(context.Background(), `select (select count(*) from transfer_order_line where transfer_order_id = $1::uuid)
		+ (select count(*) from outbox where aggregate_id = $1::text)`, d.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rows left by a deleted draft: %d (%v)", n, err)
	}

	c, _, err := s.CreateTransfer(ctx, "del-created", transferOrder(ctx, p, "5", true))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTransfer(ctx, c.ID, c.Version); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(context.Background(), `select count(*) from outbox where type = 'dev.passage.inventory.transfer.changed.v1'
		and aggregate_id = $1 and payload->'data'->>'status' = 'DELETED' and jsonb_array_length(payload->'data'->'lines') = 1`, c.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("deleted event: %d (%v), want 1", n, err)
	}
	// The key stays taken: a create retried after the delete (a client that timed out) is refused
	// with 409 instead of bringing the order back under a new id, for a draft as for a CREATED order.
	for key, want := range map[string]string{"del-created": c.Number, "del-draft": d.Number} {
		retry := transferOrder(ctx, p, "5", true)
		if key == "del-draft" {
			retry.Status = domain.TransferDraft
		}
		_, _, err := s.CreateTransfer(ctx, key, retry)
		var pr *httpx.Problem
		if !errors.As(err, &pr) || pr.Status != http.StatusConflict || pr.Code != store.CodeIdempotencyReuse || !strings.Contains(pr.Detail, want) {
			t.Fatalf("create retried under the key of deleted %s: %v, want 409 %s", want, err, store.CodeIdempotencyReuse)
		}
	}
	var deletedBy *string
	if err := admin.QueryRow(context.Background(), `select count(*) filter (where idempotency_key in ('del-draft', 'del-created')
		and ((id = $1::uuid and status = 'DRAFT') or (id = $2::uuid and status = 'CREATED'))), max(deleted_by)
		from transfer_order_deleted`, d.ID, c.ID).Scan(&n, &deletedBy); err != nil || n != 2 || deletedBy == nil {
		t.Fatalf("deleted order records: %d by %v (%v), want 2 with the deleting user", n, deletedBy, err)
	}
	// Another legal entity's caller sees neither the order nor its record.
	other := entityCtx(1, ids.New())
	if _, _, err := s.CreateTransfer(other, "del-created", transferOrder(other, p, "5", true)); err != nil {
		t.Fatalf("a key deleted in another legal entity: %v", err)
	}

	shipped, _, err := s.CreateTransfer(ctx, "del-shipped", transferOrder(ctx, p, "5", true))
	if err != nil {
		t.Fatal(err)
	}
	sh, err := s.ShipTransfer(ctx, shipped.ID, shipped.Version, nil, shipDay, domain.NegativePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTransfer(ctx, shipped.ID, sh.Version); problemCode(err) != domain.CodeTransferState {
		t.Fatalf("delete in transit: %v, want %s", err, domain.CodeTransferState)
	}
	cancelled, _, err := s.CreateTransfer(ctx, "del-cancelled", transferOrder(ctx, p, "5", true))
	if err != nil {
		t.Fatal(err)
	}
	cn, err := s.CancelTransfer(ctx, cancelled.ID, cancelled.Version)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTransfer(ctx, cancelled.ID, cn.Version); problemCode(err) != domain.CodeTransferState {
		t.Fatalf("delete cancelled: %v, want %s", err, domain.CodeTransferState)
	}
}

// A delete and a ship of the same order at the same version are serialised by the order row
// lock: exactly one wins, the other sees a stale version (412) or no order (404), and the order
// never ends up both deleted and shipped.
func TestDeleteTransferRacesShip(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	p := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(p, "100", "2", dims()))
	for i := 0; i < 5; i++ {
		o, _, err := s.CreateTransfer(ctx, "race-"+string(rune('a'+i)), transferOrder(ctx, p, "5", true))
		if err != nil {
			t.Fatal(err)
		}
		errs := make(chan error, 2)
		go func() { errs <- s.DeleteTransfer(ctx, o.ID, o.Version) }()
		go func() {
			_, err := s.ShipTransfer(ctx, o.ID, o.Version, nil, shipDay, domain.NegativePolicy{})
			errs <- err
		}()
		ok := 0
		for j := 0; j < 2; j++ {
			err := <-errs
			switch code := problemCode(err); {
			case err == nil:
				ok++
			case code == store.CodeConflictVersion || code == domain.CodeTransferNotFound:
			default:
				t.Fatalf("round %d: unexpected error %v", i, err)
			}
		}
		var live, deleted int
		if err := admin.QueryRow(context.Background(), `select (select count(*) from transfer_order where id = $1::uuid),
			(select count(*) from transfer_order_deleted where id = $1::uuid)`, o.ID).Scan(&live, &deleted); err != nil {
			t.Fatal(err)
		}
		moves := transferTxnCount(t, admin, o.ID)
		if ok != 1 || live+deleted != 1 || (deleted == 1 && moves != 0) {
			t.Fatalf("round %d: %d winners, live %d, deleted %d, transactions %d", i, ok, live, deleted, moves)
		}
	}
}
