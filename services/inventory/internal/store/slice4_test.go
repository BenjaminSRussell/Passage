package store_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

var reversalDate = time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)

// BR-16.12: a reversal is a new journal of the same type with negated lines;
// posted, each line takes the negated value of the transaction it corrects
// (not the running average), links it, and the GL entry mirrors the original.
func TestReversalMirrorsTheOriginalAtItsValues(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	product, other := ids.New(), ids.New()
	orig := post(t, ctx, s, domain.JournalMovement, line(product, "10", "3", dims()), line(other, "4", "1.25", dims()))
	post(t, ctx, s, domain.JournalMovement, line(product, "10", "1", dims()))

	if _, _, err := s.ReverseJournal(ctx, ids.New(), reversalDate); problemCode(err) != store.CodeJournalNotFound {
		t.Fatalf("unknown journal: %v", err)
	}
	draft, err := newJournal(ctx, s, domain.JournalMovement, line(product, "1", "1", dims()))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ReverseJournal(ctx, draft.ID, reversalDate); problemCode(err) != store.CodeJournalState {
		t.Fatalf("reversing a DRAFT: %v, want %s", err, store.CodeJournalState)
	}

	rev, inserted, err := s.ReverseJournal(ctx, orig.ID, reversalDate)
	if err != nil || !inserted {
		t.Fatalf("reverse: %v inserted=%v", err, inserted)
	}
	if rev.Status != domain.JournalDraft || rev.Type != domain.JournalMovement || rev.ReversesJournalID != orig.ID ||
		rev.OffsetAccountID != orig.OffsetAccountID || !rev.PostingDate.Equal(reversalDate) || len(rev.Lines) != 2 {
		t.Fatalf("reversal header %+v", rev)
	}
	for i, l := range rev.Lines {
		if !l.Qty.Equal(orig.Lines[i].Qty.Neg()) || l.ReversesLineID != orig.Lines[i].ID || l.ProductID != orig.Lines[i].ProductID {
			t.Fatalf("reversal line %d = %+v, want the negation of %+v", i+1, l, orig.Lines[i])
		}
	}
	if again, inserted, err := s.ReverseJournal(ctx, orig.ID, reversalDate); err != nil || inserted || again.ID != rev.ID {
		t.Fatalf("second request must return the same reversal: %v inserted=%v", err, inserted)
	}
	if _, err := s.AddJournalLines(ctx, rev.ID, []domain.JournalLine{line(product, "1", "1", dims())}); problemCode(err) != store.CodeJournalState {
		t.Fatalf("adding a line to a reversal: %v", err)
	}
	if o, err := s.GetJournal(ctx, orig.ID); err != nil || o.ReversedByJournalID != rev.ID || o.Status != domain.JournalPosted || o.Version != orig.Version {
		t.Fatalf("original after reversal: %v %+v", err, o)
	}

	posted, err := s.PostJournal(ctx, rev.ID, rev.Version, postParams())
	if err != nil {
		t.Fatal(err)
	}
	// Product: 20 on hand worth 40 (avg 2); the reversal takes out the original 30, not 20.
	if phys, value := onHandFor(t, s, ctx, product); !phys.Equal(dec("10")) || !value.Equal(dec("10")) {
		t.Fatalf("product after reversal: %s / %s, want 10 / 10", phys, value)
	}
	if phys, value := onHandFor(t, s, ctx, other); !phys.IsZero() || !value.IsZero() {
		t.Fatalf("other after reversal: %s / %s, want 0 / 0", phys, value)
	}
	if !posted.GLEntry.Balanced() || !posted.GLEntry.Net(inventoryAccount).Equal(orig.GLEntry.Net(inventoryAccount).Neg()) ||
		!posted.GLEntry.Net("500100").Equal(orig.GLEntry.Net("500100").Neg()) {
		t.Fatalf("reversal GL %+v does not mirror %+v", posted.GLEntry, orig.GLEntry)
	}
	var linked int
	if err := admin.QueryRow(context.Background(), `select count(*) from inventory_transaction r join inventory_transaction o
		on o.tenant_id = r.tenant_id and o.id = r.corrects_transaction_id
		where r.tenant_id = $1 and r.qty = -o.qty and r.cost_amount_posted = -o.cost_amount_posted`, testkit.TenantID(1)).Scan(&linked); err != nil {
		t.Fatal(err)
	}
	if linked != 2 {
		t.Fatalf("%d reversal transactions mirror and link their original, want 2", linked)
	}
	var payload []byte
	if err := admin.QueryRow(context.Background(), `select payload from outbox where type = 'dev.passage.inventory.journal.posted.v1'
		and payload->'data'->>'journalId' = $1`, rev.ID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var evt struct {
		Data struct {
			ReversesJournalID *string `json:"reversesJournalId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &evt); err != nil || evt.Data.ReversesJournalID == nil || *evt.Data.ReversesJournalID != orig.ID {
		t.Fatalf("journal.posted of the reversal: %v %s", err, payload)
	}
	reconcile(t, admin)
}

// A reversal is a real movement: stock already issued cannot be reversed out
// while the negative policy forbids it, and nothing is written.
func TestReversalRespectsTheNegativePolicy(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	receipt := post(t, ctx, s, domain.JournalAdjustment, line(product, "5", "2", dims()))
	post(t, ctx, s, domain.JournalAdjustment, line(product, "-4", "0", dims()))
	rev, _, err := s.ReverseJournal(ctx, receipt.ID, reversalDate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PostJournal(ctx, rev.ID, rev.Version, postParams()); problemCode(err) != domain.CodeNegativePhysical {
		t.Fatalf("reversing consumed stock: %v, want %s", err, domain.CodeNegativePhysical)
	}
	if phys, value := onHandFor(t, s, ctx, product); !phys.Equal(dec("1")) || !value.Equal(dec("2")) {
		t.Fatalf("refused reversal changed stock: %s / %s", phys, value)
	}
}

// tagCount enters one count on a tag at its current version.
func tagCount(t *testing.T, s *store.Postgres, ctx context.Context, tag domain.CountTag, product, qty string, d domain.Dimensions) domain.CountTag {
	t.Helper()
	used, err := s.UseTag(ctx, tag.ID, tag.Version, product, d, dec(qty))
	if err != nil {
		t.Fatalf("use %s: %v", tag.Number, err)
	}
	return used
}

// AC-16.13 at reduced scale: tags generated for warehouse W1, some used, one
// void, one open. The transfer is refused until the supervisor marks the open
// tag missing; the sheet then holds one line per product x dimension set with
// the summed tag quantities, and posts its variance as a COUNT journal.
func TestTagCountingTransfersSummedTagsToACountSheet(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product, other := ids.New(), ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "20", "2", dims()), line(other, "3", "1", dims()))
	plan := countPlan(t, s, ctx, false)

	if _, _, err := s.GenerateTags(ctx, ids.New(), "k0", 3); problemCode(err) != domain.CodeCountNotFound {
		t.Fatalf("unknown plan: %v", err)
	}
	tags, replayed, err := s.GenerateTags(ctx, plan.ID, "k1", 5)
	if err != nil || replayed || len(tags) != 5 || tags[0].Number != "TAG-000001" || tags[4].Number != "TAG-000005" || tags[0].Status != domain.TagOpen {
		t.Fatalf("generate: %v replayed=%v %+v", err, replayed, tags)
	}
	if again, replayed, err := s.GenerateTags(ctx, plan.ID, "k1", 5); err != nil || !replayed || again[0].ID != tags[0].ID {
		t.Fatalf("replay: %v replayed=%v", err, replayed)
	}
	if _, _, err := s.GenerateTags(ctx, plan.ID, "k1", 6); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("same key, other count: %v", err)
	}
	more, _, err := s.GenerateTags(ctx, plan.ID, "k2", 2)
	if err != nil || more[0].Number != "TAG-000006" {
		t.Fatalf("a second range continues the sequence: %v %+v", err, more)
	}

	site := domain.Dimensions{domain.DimSite: "S1"}
	t0 := tagCount(t, s, ctx, tags[0], product, "12", site) // warehouse W1 comes from the plan
	tagCount(t, s, ctx, tags[1], product, "7", dims())
	tagCount(t, s, ctx, tags[2], other, "0", dims())
	if t0.Dims[domain.DimWarehouse] != "W1" || t0.Status != domain.TagUsed || t0.CountedBy == "" || t0.Version != tags[0].Version+1 {
		t.Fatalf("used tag %+v", t0)
	}
	if _, err := s.UseTag(ctx, tags[0].ID, t0.Version, product, dims(), dec("1")); problemCode(err) != domain.CodeTagUsed {
		t.Fatalf("second use of a tag: %v, want %s", err, domain.CodeTagUsed)
	}
	if _, err := s.UseTag(ctx, tags[3].ID, tags[3].Version+1, product, dims(), dec("1")); problemCode(err) != store.CodeConflictVersion {
		t.Fatalf("stale version: %v", err)
	}
	if _, err := s.UseTag(ctx, tags[3].ID, tags[3].Version, product, domain.Dimensions{domain.DimSite: "S1", domain.DimWarehouse: "W2"}, dec("1")); problemCode(err) != domain.CodeInvalid {
		t.Fatalf("a tag of a W1 plan counted in W2: %v", err)
	}
	if _, err := s.UseTag(ctx, ids.New(), 1, product, dims(), dec("1")); problemCode(err) != domain.CodeTagNotFound {
		t.Fatalf("unknown tag: %v", err)
	}
	if v, err := s.VoidTag(ctx, tags[3].ID, tags[3].Version); err != nil || v.Status != domain.TagVoid {
		t.Fatalf("void: %v %+v", err, v)
	}
	for _, extra := range more {
		if _, err := s.VoidTag(ctx, extra.ID, extra.Version); err != nil {
			t.Fatal(err)
		}
	}
	// tags[4] stays OPEN.

	if _, _, err := s.TransferTags(ctx, plan.ID, "x1", false, true); problemCode(err) != domain.CodeTagOpen {
		t.Fatalf("transfer with an open tag: %v, want %s", err, domain.CodeTagOpen)
	}
	if _, _, err := s.TransferTags(ctx, plan.ID, "x1", true, false); problemCode(err) != domain.CodeTagApproval {
		t.Fatalf("marking missing without the supervisor: %v, want %s", err, domain.CodeTagApproval)
	}
	sheet, replayed, err := s.TransferTags(approverCtx(ctx), plan.ID, "x1", true, true)
	if err != nil || replayed || sheet.Status != domain.CountOpen || len(sheet.Lines) != 2 || sheet.CountedCount != 2 {
		t.Fatalf("transfer: %v replayed=%v %+v", err, replayed, sheet)
	}
	if again, replayed, err := s.TransferTags(ctx, plan.ID, "x1", true, true); err != nil || !replayed || again.ID != sheet.ID {
		t.Fatalf("transfer replay: %v replayed=%v", err, replayed)
	}
	for _, l := range sheet.Lines {
		switch l.ProductID {
		case product:
			if !l.Counted.Equal(dec("19")) || !l.Expected.Equal(dec("20")) || l.Dims[domain.DimWarehouse] != "W1" {
				t.Fatalf("product line %+v, want counted 12 + 7 against book 20", l)
			}
		case other:
			if !l.Counted.IsZero() || !l.Expected.Equal(dec("3")) {
				t.Fatalf("zero-count line %+v, want counted 0 against book 3", l)
			}
		default:
			t.Fatalf("unexpected line %+v", l)
		}
	}
	missing, err := s.ListTags(ctx, plan.ID, domain.TagMissing)
	if err != nil || len(missing) != 1 || missing[0].ID != tags[4].ID {
		t.Fatalf("missing tags: %v %+v", err, missing)
	}
	used, err := s.ListTags(ctx, plan.ID, domain.TagUsed)
	if err != nil || len(used) != 3 || used[0].SheetID != sheet.ID {
		t.Fatalf("used tags: %v %+v", err, used)
	}
	if _, err := s.VoidTag(ctx, used[0].ID, used[0].Version); problemCode(err) != domain.CodeTagState {
		t.Fatalf("voiding a transferred tag: %v", err)
	}
	if _, _, err := s.TransferTags(ctx, plan.ID, "x2", false, false); problemCode(err) != domain.CodeTagNoneUsed {
		t.Fatalf("second transfer with nothing left: %v", err)
	}

	// Variance -1 on 20 and -3 on 3: the second is above tolerance, so the supervisor posts.
	postedSheet, err := s.PostCountSheet(approverCtx(ctx), sheet.ID, sheet.Version, true, postParams())
	if err != nil || postedSheet.JournalID == "" {
		t.Fatalf("post: %v %+v", err, postedSheet)
	}
	if phys, _ := onHandFor(t, s, ctx, product); !phys.Equal(dec("19")) {
		t.Fatalf("product after the tag count: %s, want 19", phys)
	}
	if phys, _ := onHandFor(t, s, ctx, other); !phys.IsZero() {
		t.Fatalf("other after a zero count: %s, want 0", phys)
	}
}

// Tags of one tenant are invisible to another, and a legal-entity scope
// cannot see another entity's plan or tags.
func TestTagsAreTenantAndScopeIsolated(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	plan := countPlan(t, s, ctx, false)
	tags, _, err := s.GenerateTags(ctx, plan.ID, "k", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListTags(entityCtx(2, legalEntity(ctx)), plan.ID, ""); problemCode(err) != domain.CodeCountNotFound {
		t.Fatalf("another tenant lists the plan's tags: %v", err)
	}
	if _, err := s.UseTag(entityCtx(1, ids.New()), tags[0].ID, 1, ids.New(), dims(), dec("1")); problemCode(err) != domain.CodeTagNotFound {
		t.Fatalf("another legal entity uses the tag: %v", err)
	}
}

// onHandRow reads one stored projection row as the owner.
func onHandRow(t *testing.T, admin *pgxpool.Pool, legalEntity, product string) (string, string, string) {
	t.Helper()
	var qty, value, reserved string
	if err := admin.QueryRow(context.Background(), `select posted_qty::text, posted_value::text, reserved_physical::text from on_hand
		where tenant_id = $1 and legal_entity_id = $2 and product_id = $3`, testkit.TenantID(1), legalEntity, product).Scan(&qty, &value, &reserved); err != nil {
		t.Fatal(err)
	}
	return qty, value, reserved
}

// FR-16.3 / AC2: the rebuild from transactions and active reservations equals
// the live projection; drift is reported per row and field, a repair restores
// the rebuilt values, and rows of two legal entities with the same product and
// dimensions are kept apart.
func TestRebuildComparesAndRepairsTheProjection(t *testing.T) {
	s, _, admin := setup(t)
	le1, le2 := ids.New(), ids.New()
	ctx1, ctx2 := entityCtx(1, le1), entityCtx(1, le2)
	product := ids.New()
	post(t, ctx1, s, domain.JournalOpeningBalance, line(product, "10", "2", dims()))
	post(t, ctx1, s, domain.JournalAdjustment, line(product, "-3", "0", dims()))
	post(t, ctx2, s, domain.JournalOpeningBalance, line(product, "4", "5", dims()))
	if _, err := reserve(ctx1, s, product, "2", domain.PolicyFIFO, nil, "r1"); err != nil {
		t.Fatal(err)
	}
	wide := tenantWideCtx(1)

	rep, err := s.RebuildOnHand(wide, false)
	if err != nil || rep.Checked != 2 || rep.DiffCount != 0 || rep.Repaired {
		t.Fatalf("clean rebuild: %v %+v", err, rep)
	}

	if _, err := admin.Exec(context.Background(), `update on_hand set posted_qty = posted_qty + 5, reserved_physical = 0
		where tenant_id = $1 and legal_entity_id = $2`, testkit.TenantID(1), le1); err != nil {
		t.Fatal(err)
	}
	rep, err = s.RebuildOnHand(wide, false)
	if err != nil || rep.DiffCount != 1 || len(rep.Diffs) != 1 || rep.Diffs[0].LegalEntityID != le1 ||
		len(rep.Diffs[0].Fields) != 2 || rep.Diffs[0].Fields[0] != "posted_qty" || rep.Diffs[0].Fields[1] != "reserved_physical" {
		t.Fatalf("drift report: %v %+v", err, rep)
	}
	if qty, _, _ := onHandRow(t, admin, le1, product); !dec(qty).Equal(dec("12")) {
		t.Fatalf("compare-only changed the row: %s", qty)
	}
	if scoped, err := s.RebuildOnHand(ctx2, false); err != nil || scoped.Checked != 1 || scoped.DiffCount != 0 {
		t.Fatalf("a legal-entity scope sees only its rows: %v %+v", err, scoped)
	}

	rep, err = s.RebuildOnHand(wide, true)
	if err != nil || rep.DiffCount != 1 || !rep.Repaired {
		t.Fatalf("repair: %v %+v", err, rep)
	}
	if qty, value, reserved := onHandRow(t, admin, le1, product); !dec(qty).Equal(dec("7")) || !dec(value).Equal(dec("14")) || !dec(reserved).Equal(dec("2")) {
		t.Fatalf("repaired row %s / %s / reserved %s, want 7 / 14 / 2", qty, value, reserved)
	}
	if qty, value, _ := onHandRow(t, admin, le2, product); !dec(qty).Equal(dec("4")) || !dec(value).Equal(dec("20")) {
		t.Fatalf("other legal entity %s / %s, want 4 / 20 untouched", qty, value)
	}
	if rep, err := s.RebuildOnHand(wide, false); err != nil || rep.DiffCount != 0 {
		t.Fatalf("after repair: %v %+v", err, rep)
	}

	// A lost projection row is recreated from the ledger.
	if _, err := admin.Exec(context.Background(), `delete from on_hand where tenant_id = $1 and legal_entity_id = $2`, testkit.TenantID(1), le2); err != nil {
		t.Fatal(err)
	}
	if rep, err := s.RebuildOnHand(wide, true); err != nil || rep.DiffCount != 1 {
		t.Fatalf("missing row: %v %+v", err, rep)
	}
	var currency string
	if err := admin.QueryRow(context.Background(), `select currency::text from on_hand where tenant_id = $1 and legal_entity_id = $2`,
		testkit.TenantID(1), le2).Scan(&currency); err != nil || currency != "USD" {
		t.Fatalf("recreated row currency %q: %v", currency, err)
	}
	if qty, value, _ := onHandRow(t, admin, le2, product); !dec(qty).Equal(dec("4")) || !dec(value).Equal(dec("20")) {
		t.Fatalf("recreated row %s / %s, want 4 / 20", qty, value)
	}

	// An orphan row (stock with no transaction or reservation behind it) is zeroed.
	orphan := ids.New()
	if _, err := admin.Exec(context.Background(), `insert into on_hand (tenant_id, legal_entity_id, product_id, dimension_set_hash, posted_qty, posted_value, currency)
		select tenant_id, legal_entity_id, $3, dimension_set_hash, 3, 6, currency from on_hand where tenant_id = $1 and legal_entity_id = $2`,
		testkit.TenantID(1), le1, orphan); err != nil {
		t.Fatal(err)
	}
	rep, err = s.RebuildOnHand(wide, true)
	if err != nil || rep.DiffCount != 1 || rep.Diffs[0].ProductID != orphan || len(rep.Diffs[0].Fields) != 2 ||
		rep.Diffs[0].Fields[0] != "posted_qty" || rep.Diffs[0].Fields[1] != "posted_value" {
		t.Fatalf("orphan row: %v %+v", err, rep)
	}
	if qty, value, _ := onHandRow(t, admin, le1, orphan); !dec(qty).IsZero() || !dec(value).IsZero() {
		t.Fatalf("orphan row after repair %s / %s, want 0 / 0", qty, value)
	}
	if rep, err := s.RebuildOnHand(wide, false); err != nil || rep.DiffCount != 0 || rep.Checked != 3 {
		t.Fatalf("after orphan repair: %v %+v", err, rep)
	}
}
