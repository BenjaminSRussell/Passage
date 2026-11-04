package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/fieldcrypt"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/payables/internal/domain"
	"github.com/BenjaminSRussell/passage/services/payables/migrations"
)

const (
	leA     = "00000000-0000-0000-0000-0000000000a1"
	leB     = "00000000-0000-0000-0000-0000000000a2"
	vendor1 = "00000000-0000-0000-0000-0000000000c1"
	vendor2 = "00000000-0000-0000-0000-0000000000c2"
)

func ctxFor(n int, le string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(n), LegalEntityID: le, Duties: testkit.ScopeDuties(le)})
}

func setup(t *testing.T) *Postgres {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	keys, err := fieldcrypt.NewKeys("k1", map[string][]byte{"k1": bytes.Repeat([]byte{3}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	return New(app).WithFieldCrypt(keys)
}

func invoice(le, vendor, source, due string) OpenItem {
	return OpenItem{LegalEntityID: le, VendorID: vendor, SourceInvoiceID: source, InvoiceNumber: "INV-" + source[len(source)-2:],
		InvoiceDate: "2027-03-01", DueDate: due, Currency: "USD", Amount: decimal.RequireFromString("1000.00")}
}

const (
	src1 = "00000000-0000-0000-0000-0000000000f1"
	src2 = "00000000-0000-0000-0000-0000000000f2"
)

func TestCreateFromInvoice_IdempotentBySourceInvoice(t *testing.T) {
	s := setup(t)
	ctx := ctxFor(1, leA)
	first, created, err := s.CreateFromInvoice(ctx, invoice(leA, vendor1, src1, "2027-03-31"))
	if err != nil || !created {
		t.Fatalf("first create: created=%v err=%v", created, err)
	}
	again, created, err := s.CreateFromInvoice(ctx, invoice(leA, vendor1, src1, "2027-03-31"))
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("replay: created=%v id=%s want %s err=%v", created, again.ID, first.ID, err)
	}
	items, _ := s.List(ctx, Filter{}, 10)
	if len(items) != 1 || items[0].Status != "OPEN" || !items[0].OpenAmount.Equal(items[0].Amount) {
		t.Fatalf("items = %+v", items)
	}
}

func TestCreateFromInvoice_EmitsOneEvent(t *testing.T) {
	s := setup(t)
	ctx := ctxFor(1, leA)
	for range 2 {
		if _, _, err := s.CreateFromInvoice(ctx, invoice(leA, vendor1, src1, "2027-03-31")); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select count(*) from outbox where type = 'dev.passage.payables.openitem.created.v1'`).Scan(&n)
	})
	if err != nil || n != 1 {
		t.Fatalf("outbox events = %d err=%v, want 1", n, err)
	}
}

func TestIsolation_TenantAndLegalEntity(t *testing.T) {
	s := setup(t)
	a := ctxFor(1, leA)
	created, _, err := s.CreateFromInvoice(a, invoice(leA, vendor1, src1, "2027-03-31"))
	if err != nil {
		t.Fatal(err)
	}
	for name, ctx := range map[string]context.Context{"other tenant": ctxFor(2, leA), "other legal entity": ctxFor(1, leB)} {
		items, err := s.List(ctx, Filter{}, 10)
		if err != nil || len(items) != 0 {
			t.Errorf("%s list = %d err=%v, want 0", name, len(items), err)
		}
		if _, err := s.Settle(ctx, created.ID, created.Version, decimal.NewFromInt(1), ""); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s settle err = %v, want ErrNotFound", name, err)
		}
	}
	if _, _, err := s.CreateFromInvoice(ctxFor(1, leB), invoice(leA, vendor1, src2, "2027-03-31")); err == nil {
		t.Error("insert into a legal entity outside the caller's scope succeeded")
	}
	otherTenant, created2, err := s.CreateFromInvoice(ctxFor(2, leA), invoice(leA, vendor1, src1, "2027-03-31"))
	if err != nil || !created2 || otherTenant.ID == created.ID {
		t.Fatalf("same source invoice in another tenant: created=%v err=%v", created2, err)
	}
}

func TestList_FiltersByVendorAndDueDate(t *testing.T) {
	s := setup(t)
	ctx := ctxFor(1, leA)
	for _, in := range []OpenItem{
		invoice(leA, vendor1, src1, "2027-04-15"),
		invoice(leA, vendor2, src2, "2027-03-31"),
		invoice(leA, vendor1, "00000000-0000-0000-0000-0000000000f3", "2027-03-10"),
	} {
		if _, _, err := s.CreateFromInvoice(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.List(ctx, Filter{VendorID: vendor1}, 10)
	if err != nil || len(got) != 2 || got[0].DueDate != "2027-03-10" {
		t.Fatalf("vendor1 = %+v err=%v", got, err)
	}
	got, _ = s.List(ctx, Filter{DueOnOrBy: "2027-03-31"}, 10)
	if len(got) != 2 {
		t.Fatalf("due by 03-31 = %d, want 2", len(got))
	}
	if got, _ = s.List(ctx, Filter{}, 1); len(got) != 1 {
		t.Fatalf("limit not applied: %d", len(got))
	}
}

func TestSettle_ReducesOpenAmountWithVersionCheck(t *testing.T) {
	s := setup(t)
	ctx := ctxFor(1, leA)
	item, _, _ := s.CreateFromInvoice(ctx, invoice(leA, vendor1, src1, "2027-03-31"))

	partial, err := s.Settle(ctx, item.ID, item.Version, decimal.RequireFromString("400.00"), "stl-1")
	if err != nil || partial.Status != "PARTIALLY_SETTLED" || !partial.OpenAmount.Equal(decimal.RequireFromString("600")) || partial.Version != item.Version+1 {
		t.Fatalf("partial = %+v err=%v", partial, err)
	}
	var payload string
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select payload::text from outbox where type = $1`, OpenItemSettledType).Scan(&payload)
	})
	if err != nil || !strings.Contains(payload, `"settled_amount": "400"`) || !strings.Contains(payload, `"settlement_id": "stl-1"`) {
		t.Fatalf("settled event payload = %s err=%v", payload, err)
	}
	if _, err := s.Settle(ctx, item.ID, item.Version, decimal.NewFromInt(1), ""); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale version err = %v, want ErrVersionConflict", err)
	}
	if _, err := s.Settle(ctx, item.ID, partial.Version, decimal.RequireFromString("600.01"), ""); !errors.Is(err, ErrOverSettled) {
		t.Fatalf("oversettle err = %v, want ErrOverSettled", err)
	}
	if _, err := s.Settle(ctx, item.ID, partial.Version, decimal.Zero, ""); !errors.Is(err, ErrInvalidAmount) {
		t.Fatalf("zero amount err = %v, want ErrInvalidAmount", err)
	}
	done, err := s.Settle(ctx, item.ID, partial.Version, decimal.RequireFromString("600"), "")
	if err != nil || done.Status != "SETTLED" || !done.OpenAmount.IsZero() {
		t.Fatalf("final = %+v err=%v", done, err)
	}
	if open, _ := s.List(ctx, Filter{OpenOnly: true}, 10); len(open) != 0 {
		t.Fatalf("settled item listed as open: %+v", open)
	}
}

func TestCreateFromInvoice_ConflictingReplayIsRejected(t *testing.T) {
	s := setup(t)
	ctx := ctxFor(1, leA)
	first, _, err := s.CreateFromInvoice(ctx, invoice(leA, vendor1, src1, "2027-03-31"))
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*OpenItem){
		"total":    func(o *OpenItem) { o.Amount = decimal.RequireFromString("999.00") },
		"vendor":   func(o *OpenItem) { o.VendorID = vendor2 },
		"currency": func(o *OpenItem) { o.Currency = "EUR" },
	} {
		in := invoice(leA, vendor1, src1, "2027-03-31")
		mutate(&in)
		if _, created, err := s.CreateFromInvoice(ctx, in); !errors.Is(err, ErrConflictingReplay) || created {
			t.Errorf("%s: created=%v err=%v, want ErrConflictingReplay", name, created, err)
		}
	}
	again, created, err := s.CreateFromInvoice(ctx, invoice(leA, vendor1, src1, "2027-03-31"))
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("identical replay: created=%v err=%v", created, err)
	}
}

func TestList_ClampsLimit(t *testing.T) {
	s := setup(t)
	ctx := ctxFor(1, leA)
	for _, src := range []string{src1, src2} {
		if _, _, err := s.CreateFromInvoice(ctx, invoice(leA, vendor1, src, "2027-03-31")); err != nil {
			t.Fatal(err)
		}
	}
	for _, limit := range []int{-1, 0, 100000} {
		if got, err := s.List(ctx, Filter{}, limit); err != nil || len(got) != 2 {
			t.Errorf("limit %d: %d items err=%v, want 2", limit, len(got), err)
		}
	}
}

func TestCreateFromInvoice_RecordsDueDateSource(t *testing.T) {
	s := setup(t)
	ctx := ctxFor(1, leA)
	in := invoice(leA, vendor1, src1, "2027-03-01")
	in.DueDateSource = DueDateDefaulted
	if _, _, err := s.CreateFromInvoice(ctx, in); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateFromInvoice(ctx, invoice(leA, vendor1, src2, "2027-03-31")); err != nil {
		t.Fatal(err)
	}
	got, _ := s.List(ctx, Filter{}, 10)
	if len(got) != 2 || got[0].DueDateSource != DueDateDefaulted || got[1].DueDateSource != DueDateEvent {
		t.Fatalf("items = %+v", got)
	}
}

func TestAgingRows_OpenAmountsAsOfAndIsolation(t *testing.T) {
	s := setup(t)
	ctx := ctxFor(1, leA)
	mk := func(source, vendor, invoiceDate, due string, le string) OpenItem {
		in := invoice(le, vendor, source, due)
		in.InvoiceDate = invoiceDate
		o, _, err := s.CreateFromInvoice(ctxFor(1, le), in)
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	partial := mk(src1, vendor1, "2027-03-01", "2027-03-31", leA)
	settled := mk(src2, vendor1, "2027-03-01", "2027-03-31", leA)
	mk("00000000-0000-0000-0000-0000000000f3", vendor2, "2027-03-01", "2027-04-30", leA)
	mk("00000000-0000-0000-0000-0000000000f4", vendor2, "2027-07-01", "2027-07-31", leA)
	mk("00000000-0000-0000-0000-0000000000f5", vendor2, "2027-03-01", "2027-03-31", leB)
	if _, err := s.Settle(ctx, partial.ID, partial.Version, decimal.RequireFromString("250.00"), "set-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Settle(ctx, settled.ID, settled.Version, decimal.RequireFromString("1000.00"), "set-2"); err != nil {
		t.Fatal(err)
	}

	rows, err := s.AgingRows(ctx, leA, "2027-06-30")
	if err != nil {
		t.Fatal(err)
	}
	total := decimal.Zero
	for _, r := range rows {
		total = total.Add(r.OpenAmount)
	}
	if want := decimal.RequireFromString("1750.00"); !total.Equal(want) || len(rows) != 2 {
		t.Fatalf("open total = %s over %d groups, want %s over 2 (partial remainder + untouched; settled and later-dated excluded; other entity excluded)", total, len(rows), want)
	}
	for name, c := range map[string]struct {
		ctx context.Context
		le  string
	}{"other tenant": {ctxFor(2, leA), leA}, "scope excludes entity": {ctxFor(1, leB), leA}} {
		got, err := s.AgingRows(c.ctx, c.le, "2027-06-30")
		if err != nil || len(got) != 0 {
			t.Fatalf("%s: rows=%v err=%v, want none", name, got, err)
		}
	}
}

func TestAgingRows_ThousandGeneratedItemsSumToOpenAmounts(t *testing.T) {
	s := setup(t)
	ctx := ctxFor(1, leA)
	want := decimal.Zero
	for i := range 1000 {
		in := invoice(leA, []string{vendor1, vendor2}[i%2], fmt.Sprintf("00000000-0000-0000-0000-%012d", 5000+i), "2027-03-31")
		in.DueDate = time.Date(2027, 3, 31, 0, 0, 0, 0, time.UTC).AddDate(0, 0, (i*13)%300).Format("2006-01-02")
		in.Amount = decimal.New(int64(10000+i*91%500000), -2)
		o, _, err := s.CreateFromInvoice(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		open := o.Amount
		if i%5 == 0 {
			part := o.Amount.Div(decimal.NewFromInt(4)).Round(2)
			if o, err = s.Settle(ctx, o.ID, o.Version, part, "set"); err != nil {
				t.Fatal(err)
			}
			open = o.OpenAmount
		}
		want = want.Add(open)
	}
	rows, err := s.AgingRows(ctx, leA, "2028-01-31")
	if err != nil {
		t.Fatal(err)
	}
	asOf := time.Date(2028, 1, 31, 0, 0, 0, 0, time.UTC)
	got := decimal.Zero
	for _, t := range domain.Age(asOf, rows, true) {
		got = got.Add(t.Amount)
	}
	if !got.Equal(want) {
		t.Fatalf("bucket total %s, want open total %s", got, want)
	}
}

const (
	groupA = "00000000-0000-0000-0000-0000000000b1"
	groupB = "00000000-0000-0000-0000-0000000000b2"
)

func TestVendorGroup_StoredAggregatedAndBackfilledOnReplay(t *testing.T) {
	s := setup(t)
	ctx := ctxFor(1, leA)
	mk := func(source, vendor, group, amt string) OpenItem {
		in := invoice(leA, vendor, source, "2027-03-31")
		in.VendorGroupID = group
		switch group {
		case groupA:
			in.VendorGroupCode = "GRPA"
		case groupB:
			in.VendorGroupCode = "GRPB"
		}
		in.Amount = decimal.RequireFromString(amt)
		o, _, err := s.CreateFromInvoice(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	first := mk(src1, vendor1, groupA, "100.10")
	mk(src2, vendor2, groupA, "200.20")
	mk("00000000-0000-0000-0000-0000000000f3", vendor2, groupB, "7.07")
	legacy := mk("00000000-0000-0000-0000-0000000000f4", vendor1, "", "50.00")
	if first.VendorGroupID != groupA || legacy.VendorGroupID != "" {
		t.Fatalf("group on create = %q / %q", first.VendorGroupID, legacy.VendorGroupID)
	}

	rows, err := s.AgingRows(ctx, leA, "2027-06-30")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, g := range domain.ReconcileGroups(rows, "USD", nil, "no ledger") {
		got[g.VendorGroupCode] = g.SubledgerTotal.String()
	}
	if want := map[string]string{"GRPA": "300.3", "GRPB": "7.07", domain.UnassignedGroup: "50"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("breakdown = %v, want %v", got, want)
	}

	in := invoice(leA, vendor1, "00000000-0000-0000-0000-0000000000f4", "2027-03-31")
	in.Amount, in.VendorGroupID = decimal.RequireFromString("50.00"), groupB
	filled, created, err := s.CreateFromInvoice(ctx, in)
	if err != nil || created || filled.VendorGroupID != groupB || filled.ID != legacy.ID {
		t.Fatalf("backfill replay: %+v created=%v err=%v", filled, created, err)
	}
	in.VendorGroupID = groupA
	kept, _, err := s.CreateFromInvoice(ctx, in)
	if err != nil || kept.VendorGroupID != groupB {
		t.Fatalf("an assigned group must not be overwritten: %+v err=%v", kept, err)
	}
}

func TestVendorGroup_AggregationIsolatedByTenantAndEntity(t *testing.T) {
	s := setup(t)
	in := invoice(leA, vendor1, src1, "2027-03-31")
	in.VendorGroupID = groupA
	if _, _, err := s.CreateFromInvoice(ctxFor(1, leA), in); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		ctx context.Context
		le  string
	}{"other tenant": {ctxFor(2, leA), leA}, "scope excludes entity": {ctxFor(1, leB), leA}} {
		rows, err := s.AgingRows(c.ctx, c.le, "2027-06-30")
		if err != nil || len(rows) != 0 {
			t.Fatalf("%s: rows=%v err=%v, want none", name, rows, err)
		}
	}
}

func TestVendorGroupCode_StoredFilledOnReplayAndReadForARun(t *testing.T) {
	s := setup(t)
	ctx := ctxFor(1, leA)
	in := invoice(leA, vendor1, src1, "2027-03-31")
	in.VendorGroupID, in.VendorGroupCode = groupA, "DOM"
	created, _, err := s.CreateFromInvoice(ctx, in)
	if err != nil || created.VendorGroupCode != "DOM" {
		t.Fatalf("create = %+v, %v", created, err)
	}
	// An item consumed before the code existed: the group id is set, the code is not.
	old := invoice(leA, vendor2, src2, "2027-03-31")
	old.VendorGroupID = groupB
	legacy, _, err := s.CreateFromInvoice(ctx, old)
	if err != nil || legacy.VendorGroupCode != "" {
		t.Fatalf("legacy = %+v, %v", legacy, err)
	}
	old.VendorGroupCode = "IMP"
	filled, isNew, err := s.CreateFromInvoice(ctx, old)
	if err != nil || isNew || filled.VendorGroupCode != "IMP" || filled.VendorGroupID != groupB {
		t.Fatalf("a redelivery fills the missing code: %+v new=%v err=%v", filled, isNew, err)
	}
	old.VendorGroupCode = "OTHER"
	kept, _, err := s.CreateFromInvoice(ctx, old)
	if err != nil || kept.VendorGroupCode != "IMP" {
		t.Fatalf("an assigned code must not be overwritten: %+v err=%v", kept, err)
	}
	rows, err := s.AgingRows(ctx, leA, "2027-06-30")
	if err != nil {
		t.Fatal(err)
	}
	codes := map[string]bool{}
	for _, r := range rows {
		codes[r.VendorGroupCode] = true
	}
	if !codes["DOM"] || !codes["IMP"] || len(codes) != 2 {
		t.Fatalf("aging rows carry codes %v", codes)
	}
}
