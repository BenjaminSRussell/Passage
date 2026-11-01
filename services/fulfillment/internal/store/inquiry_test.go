package store_test

import (
	"context"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

// shipTo releases, picks, packs and posts one 10-unit order for the legal
// entity and customer, at unitCost, on postingDaysAgo days before today.
func shipTo(t *testing.T, s *store.Postgres, ctx context.Context, legalEntity, customer, unitCost string, postingDaysAgo int, cogsAtShipment bool) store.PackingSlip {
	t.Helper()
	line := domain.ReleaseLine{OrderID: ids.New(), OrderLineID: ids.New(), CustomerID: customer, LegalEntityID: legalEntity,
		WarehouseID: ids.New(), ProductID: ids.New(), Unit: "EA", Qty: decimal.NewFromInt(10), ConfirmedShipDate: today(), ATPOK: true}
	groups, _ := domain.Release([]domain.ReleaseLine{line}, domain.ReleaseRule{Today: today(), HorizonDays: 2, RequireFullATP: true})
	res, err := s.RunRelease(ctx, line.WarehouseID, groups, idem("rel"))
	if err != nil || len(res.Shipments) != 1 {
		t.Fatalf("RunRelease: %+v, %v", res, err)
	}
	sh := res.Shipments[0]
	setCogsPolicy(t, s, ctx, legalEntity, cogsAtShipment)
	packed := pickAndPack(t, s, ctx, sh)
	slip, err := s.PostPackingSlip(ctx, sh.ID, packed.Shipment.Version, store.PostInput{PostingDate: today().AddDate(0, 0, -postingDaysAgo),
		Currency: "USD", CogsAtShipment: cogsAtShipment, Costs: costs(sh, unitCost)}, idem("post"))
	if err != nil {
		t.Fatalf("PostPackingSlip: %v", err)
	}
	return slip
}

func dniNumbers(rows []domain.DNIRow) map[string]decimal.Decimal {
	out := map[string]decimal.Decimal{}
	for _, r := range rows {
		out[r.PackingSlipNumber] = r.Amount
	}
	return out
}

// TestDeliveredNotInvoiced_AC28_8 scales AC-28.8 down: of the slips posted with
// physical posting, the invoiced ones leave the report and the rest sum to the
// Dr 1405 they posted. A slip posted with cogs_at_shipment never touches 1405.
func TestDeliveredNotInvoiced_AC28_8(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(21, "")
	le, customer := ids.New(), ids.New()
	var slips []store.PackingSlip
	for i := 0; i < 5; i++ {
		slips = append(slips, shipTo(t, s, ctx, le, customer, "12.50", 3, false))
	}
	shipTo(t, s, ctx, le, customer, "12.50", 3, true) // PGI-style: Dr 5000, no DNI
	for _, slip := range slips[:2] {
		if _, err := admin.Exec(context.Background(), `update packing_slip set status = 'INVOICED' where id = $1`, slip.ID); err != nil {
			t.Fatal(err)
		}
	}

	rows, err := s.DeliveredNotInvoiced(ctx, store.DNIFilter{LegalEntityID: le, AsOf: today()})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("DNI rows = %d (%v), want the 3 uninvoiced physical slips", len(rows), dniNumbers(rows))
	}
	for i, r := range rows {
		if r.PackingSlipID != slips[i+2].ID || !r.Amount.Equal(decimal.NewFromInt(125)) || r.CustomerID != customer || r.Currency != "USD" {
			t.Fatalf("row %d = %+v, want slip %s at 125.00 for the customer", i, r, slips[i+2].Number)
		}
	}
	totals := domain.DeliveredNotInvoiced(rows)
	if len(totals) != 1 || !totals[0].Amount.Equal(decimal.NewFromInt(375)) {
		t.Fatalf("totals = %+v, want 375.00 USD", totals)
	}
	// Reconciliation: the report equals Σ 1405 over the journals of uninvoiced slips.
	var gl1405 string
	if err := admin.QueryRow(context.Background(), `select coalesce(sum((j->>'amount')::numeric), 0)::text
		from packing_slip p cross join lateral jsonb_array_elements(p.journal) j
		where p.legal_entity_id = $1 and p.status <> 'INVOICED' and j->>'account' = '1405'`, le).Scan(&gl1405); err != nil {
		t.Fatal(err)
	}
	if !decimal.RequireFromString(gl1405).Equal(totals[0].Amount) {
		t.Fatalf("Σ 1405 = %s, report = %s", gl1405, totals[0].Amount)
	}
}

// A cancelled slip leaves the report from the date of its reversal on; before
// that date it was still delivered and not invoiced (AC-28.2 on GL 1405).
func TestDeliveredNotInvoiced_AsOfRespectsCancellationDate(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(22, "")
	le, customer := ids.New(), ids.New()
	kept := shipTo(t, s, ctx, le, customer, "1", 10, false)
	cancelled := shipTo(t, s, ctx, le, customer, "2", 5, false)
	if _, err := s.CancelPackingSlip(ctx, cancelled.ID, idem("cancel")); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		days int
		want map[string]string
	}{
		{"before either post", 11, map[string]string{}},
		{"after the first post", 7, map[string]string{kept.Number: "10"}},
		{"before the cancel", 1, map[string]string{kept.Number: "10", cancelled.Number: "20"}},
		{"on the cancel day", 0, map[string]string{kept.Number: "10"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := s.DeliveredNotInvoiced(ctx, store.DNIFilter{LegalEntityID: le, AsOf: today().AddDate(0, 0, -tc.days)})
			if err != nil {
				t.Fatal(err)
			}
			got := dniNumbers(rows)
			if len(got) != len(tc.want) {
				t.Fatalf("rows = %v, want %v", got, tc.want)
			}
			for number, amount := range tc.want {
				if !got[number].Equal(decimal.RequireFromString(amount)) {
					t.Fatalf("rows = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// The report is per legal entity and optional customer, and never crosses the
// tenant or the caller's legal-entity scope (row-level security).
func TestDeliveredNotInvoiced_FiltersAndIsolation(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(23, "")
	le, c1, c2 := ids.New(), ids.New(), ids.New()
	a := shipTo(t, s, ctx, le, c1, "1", 1, false)
	b := shipTo(t, s, ctx, le, c2, "1", 1, false)
	shipTo(t, s, ctx, ids.New(), c1, "1", 1, false) // another legal entity

	all, err := s.DeliveredNotInvoiced(ctx, store.DNIFilter{LegalEntityID: le, AsOf: today()})
	if err != nil || len(all) != 2 {
		t.Fatalf("all = %v, %v; want slips %s and %s", dniNumbers(all), err, a.Number, b.Number)
	}
	one, err := s.DeliveredNotInvoiced(ctx, store.DNIFilter{LegalEntityID: le, CustomerID: c2, AsOf: today()})
	if err != nil || len(one) != 1 || one[0].PackingSlipID != b.ID {
		t.Fatalf("customer filter = %v, %v; want only %s", dniNumbers(one), err, b.Number)
	}
	if rows, err := s.DeliveredNotInvoiced(ctxFor(24, ""), store.DNIFilter{LegalEntityID: le, AsOf: today()}); err != nil || len(rows) != 0 {
		t.Fatalf("other tenant sees %v, %v", dniNumbers(rows), err)
	}
	scoped := ctxFor(23, ids.New()) // scoped to another legal entity
	if rows, err := s.DeliveredNotInvoiced(scoped, store.DNIFilter{LegalEntityID: le, AsOf: today()}); err != nil || len(rows) != 0 {
		t.Fatalf("out-of-scope principal sees %v, %v", dniNumbers(rows), err)
	}
}
