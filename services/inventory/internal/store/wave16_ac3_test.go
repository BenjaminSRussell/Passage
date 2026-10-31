package store_test

// Wave 16 proof, AC3 (issue #16): every posted journal produces a balanced GL
// entry, checked per journal type, through the store.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
)

// glWant is one expected GL line: account, debit, credit (hand-derived).
type glWant struct{ account, debit, credit string }

// checkStoredGL reads the GL entry stored on the journal row and the glLines of
// its journal.posted event as raw JSON, checks both are exactly want (in
// order), and checks debits equal credits by summing the raw amounts (not
// through domain.JournalEntry.Balanced). It returns the inventory-account net.
func checkStoredGL(t *testing.T, admin *pgxpool.Pool, journalID string, want []glWant) decimal.Decimal {
	t.Helper()
	ctx := context.Background()
	var stored, event []byte
	var typ string
	if err := admin.QueryRow(ctx, `select j.journal_type, j.gl_entry::text, (select o.payload->'data'->'glLines' from outbox o
		where o.type = 'dev.passage.inventory.journal.posted.v1' and o.payload->'data'->>'journalId' = j.id::text)::text
		from inventory_journal j where j.id = $1 and j.status = 'POSTED'`, journalID).Scan(&typ, &stored, &event); err != nil {
		t.Fatalf("journal %s: %v", journalID, err)
	}
	var entry struct {
		Currency string `json:"currency"`
		Lines    []struct {
			Account string          `json:"account"`
			Debit   decimal.Decimal `json:"debit"`
			Credit  decimal.Decimal `json:"credit"`
		} `json:"lines"`
	}
	var evt []struct {
		Account string `json:"account"`
		Debit   string `json:"debit"`
		Credit  string `json:"credit"`
	}
	if err := json.Unmarshal(stored, &entry); err != nil {
		t.Fatalf("%s gl_entry %s: %v", typ, stored, err)
	}
	if err := json.Unmarshal(event, &evt); err != nil {
		t.Fatalf("%s journal.posted glLines %s: %v", typ, event, err)
	}
	if entry.Currency != "USD" || len(entry.Lines) != len(want) || len(evt) != len(want) {
		t.Fatalf("%s: stored %s, event %s; want %d lines in USD %v", typ, stored, event, len(want), want)
	}
	dr, cr, inventory := decimal.Zero, decimal.Zero, decimal.Zero
	for i, w := range want {
		l, e := entry.Lines[i], evt[i]
		if l.Account != w.account || !l.Debit.Equal(dec(w.debit)) || !l.Credit.Equal(dec(w.credit)) {
			t.Fatalf("%s line %d = %s Dr %s Cr %s, want %s Dr %s Cr %s", typ, i+1, l.Account, l.Debit, l.Credit, w.account, w.debit, w.credit)
		}
		if e.Account != w.account || !dec(e.Debit).Equal(dec(w.debit)) || !dec(e.Credit).Equal(dec(w.credit)) {
			t.Fatalf("%s event line %d = %+v, want %+v", typ, i+1, e, w)
		}
		dr, cr = dr.Add(l.Debit), cr.Add(l.Credit)
		if l.Account == inventoryAccount {
			inventory = inventory.Add(l.Debit).Sub(l.Credit)
		}
	}
	if !dr.Equal(cr) {
		t.Fatalf("%s entry does not balance: Dr %s Cr %s", typ, dr, cr)
	}
	return inventory
}

// TestAC3EveryJournalTypePostsABalancedEntry posts one journal of every type the
// service has (OPENING_BALANCE, MOVEMENT, ADJUSTMENT, TRANSFER, COUNT through a
// count sheet, OWNERSHIP_CHANGE through vendor consignment) and the reversal of
// MOVEMENT, ADJUSTMENT and OPENING_BALANCE, on one legal entity, and compares
// each stored GL entry and its journal.posted event with hand-derived lines
// (the derivation is in the comments: receipts at qty x cost, issues at the
// row's running average, every amount rounded to the cent). After each posting
// the inventory account's net must equal the change of the legal entity's
// posted on-hand value, read with SQL. Finally every POSTED journal of the
// tenant must balance by a SQL sum over its stored entry, each type must have
// been seen, and the projection must equal a rebuild.
//
// BOM journals do not exist (handoff to wave 9), and the entry is built in this
// service rather than posted through the wave 11 universal journal, so AC3 is
// not closed by this test alone (see NOT-IMPLEMENTED.md).
func TestAC3EveryJournalTypePostsABalancedEntry(t *testing.T) {
	s, _, admin := setup(t)
	bg := context.Background()
	ctx := entityCtx(1, ids.New())
	le := legalEntity(ctx)
	p, q, r, sProd := ids.New(), ids.New(), ids.New(), ids.New()
	d1, d2 := dims(), dims("location", "L2")
	params := consignmentParams()

	value := func() decimal.Decimal {
		var v string
		if err := admin.QueryRow(bg, `select coalesce(sum(posted_value), 0)::text from on_hand where tenant_id = $1 and legal_entity_id = $2`,
			testkit.TenantID(1), le).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return dec(v)
	}
	seen := map[domain.JournalType]bool{}
	expectValue := decimal.Zero
	check := func(j domain.Journal, wantValue string, want ...glWant) {
		t.Helper()
		seen[j.Type] = true
		net := checkStoredGL(t, admin, j.ID, want)
		expectValue = expectValue.Add(net)
		if got := value(); !got.Equal(dec(wantValue)) || !got.Equal(expectValue) {
			t.Fatalf("%s: posted on-hand value %s, want %s (running GL inventory net %s)", j.Type, got, wantValue, expectValue)
		}
	}
	postIt := func(typ domain.JournalType, lines ...domain.JournalLine) domain.Journal {
		t.Helper()
		j, err := newJournal(ctx, s, typ, lines...)
		if err != nil {
			t.Fatal(err)
		}
		posted, err := s.PostJournal(ctx, j.ID, j.Version, params)
		if err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		return posted
	}
	reverseIt := func(j domain.Journal) domain.Journal {
		t.Helper()
		rev, _, err := s.ReverseJournal(ctx, j.ID, time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC))
		if err != nil {
			t.Fatal(err)
		}
		posted, err := s.PostJournal(ctx, rev.ID, rev.Version, params)
		if err != nil {
			t.Fatalf("reversal of %s: %v", j.Type, err)
		}
		return posted
	}

	// OPENING_BALANCE: P 40 x 2.125 = 85.00; Q 3 x 3.333333 = 9.999999 -> 10.00. Receipts 95.00.
	opening := postIt(domain.JournalOpeningBalance, line(p, "40", "2.125", d1), line(q, "3", "3.333333", d1))
	check(opening, "95.00", glWant{"1300", "95.00", "0"}, glWant{"3900", "0", "95.00"})

	// MOVEMENT: issue P 10 at 85/40 = 2.125 -> 21.25; receive Q 2 x 1.105 = 2.21.
	// P 30 @ 63.75, Q 5 @ 12.21; value 95 + 2.21 - 21.25 = 75.96.
	movement := postIt(domain.JournalMovement, line(p, "-10", "0", d1), line(q, "2", "1.105", d1))
	check(movement, "75.96", glWant{"1300", "2.21", "0"}, glWant{"500100", "0", "2.21"},
		glWant{"500100", "21.25", "0"}, glWant{"1300", "0", "21.25"})

	// ADJUSTMENT: issue Q 1 at 12.21/5 = 2.442 -> 2.44; receive P 0.5 x 7 = 3.50.
	// P 30.5 @ 67.25, Q 4 @ 9.77; value 75.96 + 3.50 - 2.44 = 77.02.
	adjustment := postIt(domain.JournalAdjustment, line(q, "-1", "0", d1), line(p, "0.5", "7", d1))
	check(adjustment, "77.02", glWant{"1300", "3.50", "0"}, glWant{"5100", "0", "3.50"},
		glWant{"5100", "2.44", "0"}, glWant{"1300", "0", "2.44"})

	// TRANSFER: P 10.5 from L(none) to L2 at 67.25/30.5 x 10.5 = 23.1516... -> 23.15.
	// No GL lines; P@d1 20 @ 44.10, P@d2 10.5 @ 23.15; value unchanged 77.02.
	transfer := postIt(domain.JournalTransfer, transferLine(p, "10.5", d1, d2))
	check(transfer, "77.02")

	// COUNT: a W1 plan counts P@d1 18 (book 20: variance -2 at 44.10/20 = 2.205 -> 4.41)
	// and Q 4.5 (book 4: +0.5 at 9.77/4 = 2.4425 -> 1.22125 -> 1.22); P@d2 is left uncounted.
	// Both exceed the 5 % tolerance, so the sheet is posted by an approver.
	// P@d1 18 @ 39.69, Q 4.5 @ 10.99; value 77.02 + 1.22 - 4.41 = 73.83.
	plan := countPlan(t, s, ctx, false)
	sheet, _, err := s.GenerateCountSheet(ctx, plan.ID, ids.New())
	if err != nil {
		t.Fatal(err)
	}
	pd1, _ := d1.Normalize()
	version := sheet.Version
	for _, l := range sheet.Lines {
		var counted string
		switch {
		case l.ProductID == p && l.Dims.Canonical() == pd1.Canonical():
			counted = "18"
		case l.ProductID == q:
			counted = "4.5"
		default:
			continue
		}
		next, err := s.EnterCount(ctx, sheet.ID, l.ID, version, dec(counted))
		if err != nil {
			t.Fatal(err)
		}
		version = next.Version
	}
	countSheet, err := s.PostCountSheet(approverCtx(ctx), sheet.ID, version, true, params)
	if err != nil {
		t.Fatal(err)
	}
	count, err := s.GetJournal(ctx, countSheet.JournalID)
	if err != nil {
		t.Fatal(err)
	}
	check(count, "73.83", glWant{"1300", "1.22", "0"}, glWant{"5100", "0", "1.22"},
		glWant{"5100", "4.41", "0"}, glWant{"1300", "0", "4.41"})

	// OWNERSHIP_CHANGE: 50 R received from V1 on consignment (no value, no GL), then
	// 12.5 change ownership at 4.10 = 51.25: Dr inventory / Cr consignment accrual.
	// The vendor-owned issue leg is valued 0. Value 73.83 + 51.25 = 125.08.
	receiveConsignment(t, s, ctx, r, "50", "4.00")
	ownership := postIt(domain.JournalOwnershipChange, line(r, "12.5", "4.10", vendorDims()))
	check(ownership, "125.08", glWant{"1300", "51.25", "0"}, glWant{"2190", "0", "51.25"})

	// Reversal of MOVEMENT: P +10 at the original's 21.25, Q -2 at the original's 2.21
	// (not today's averages). Value 125.08 + 21.25 - 2.21 = 144.12.
	check(reverseIt(movement), "144.12", glWant{"1300", "21.25", "0"}, glWant{"500100", "0", "21.25"},
		glWant{"500100", "2.21", "0"}, glWant{"1300", "0", "2.21"})

	// Reversal of ADJUSTMENT: Q +1 at 2.44, P -0.5 at 3.50. Value 144.12 + 2.44 - 3.50 = 143.06.
	check(reverseIt(adjustment), "143.06", glWant{"1300", "2.44", "0"}, glWant{"5100", "0", "2.44"},
		glWant{"5100", "3.50", "0"}, glWant{"1300", "0", "3.50"})

	// OPENING_BALANCE of S 7 x 1.10 = 7.70 and its reversal back to 143.06.
	openS := postIt(domain.JournalOpeningBalance, line(sProd, "7", "1.10", d1))
	check(openS, "150.76", glWant{"1300", "7.70", "0"}, glWant{"3900", "0", "7.70"})
	check(reverseIt(openS), "143.06", glWant{"3900", "7.70", "0"}, glWant{"1300", "0", "7.70"})

	// Final rows by hand: P@d1 27.5 @ 57.44, P@d2 10.5 @ 23.15, Q 3.5 @ 11.22, R 12.5 @ 51.25 (V1 37.5 @ 0), S 0.
	for _, c := range []struct {
		product string
		d       domain.Dimensions
		qty     string
		value   string
	}{{p, d1, "27.5", "57.44"}, {p, d2, "10.5", "23.15"}, {q, d1, "3.5", "11.22"}, {r, d1, "12.5", "51.25"}, {r, vendorDims(), "0", "0"}, {sProd, d1, "0", "0"}} {
		n, _ := c.d.Normalize()
		var qty, val string
		if err := admin.QueryRow(bg, `select posted_qty::text, posted_value::text from on_hand where tenant_id = $1 and product_id = $2
			and dimension_set_hash = $3`, testkit.TenantID(1), c.product, n.Hash()).Scan(&qty, &val); err != nil {
			t.Fatal(err)
		}
		if !dec(qty).Equal(dec(c.qty)) || !dec(val).Equal(dec(c.value)) {
			t.Fatalf("row %v of %s = %s @ %s, want %s @ %s", c.d, c.product, qty, val, c.qty, c.value)
		}
	}

	for _, typ := range domain.JournalTypes {
		if !seen[typ] {
			t.Errorf("journal type %s was not posted", typ)
		}
	}
	var posted, unbalanced int
	if err := admin.QueryRow(bg, `select count(*), count(*) filter (where dr <> cr) from (
		select j.id, coalesce(sum((l->>'debit')::numeric), 0) dr, coalesce(sum((l->>'credit')::numeric), 0) cr
		from inventory_journal j left join lateral jsonb_array_elements(j.gl_entry->'lines') l on true
		where j.tenant_id = $1 and j.status = 'POSTED' group by j.id) x`, testkit.TenantID(1)).Scan(&posted, &unbalanced); err != nil {
		t.Fatal(err)
	}
	// Ten journals: six types, three reversals, and the second opening balance.
	if posted != 10 || unbalanced != 0 {
		t.Fatalf("%d posted journals, %d unbalanced; want 10 and 0", posted, unbalanced)
	}
	if rep, err := s.RebuildOnHand(tenantWideCtx(1), false); err != nil || rep.DiffCount != 0 {
		t.Fatalf("rebuild: %v %+v", err, rep)
	}
}
