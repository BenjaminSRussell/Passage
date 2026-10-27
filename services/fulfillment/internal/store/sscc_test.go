package store_test

import (
	"context"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

func ssccs(rows []store.SSCCRow) []string {
	out := []string{}
	for _, r := range rows {
		out = append(out, r.SSCC)
	}
	return out
}

// The SSCC register (section 5.28.14, BR-28.5) lists every issued SSCC with its
// container and shipment, filtered by company prefix, customer and issue date,
// in SSCC order with a keyset cursor, inside the caller's tenant and scope.
func TestSSCCRegister(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(25, "")
	le, shippedCustomer := ids.New(), ids.New()
	slip := shipTo(t, s, ctx, le, shippedCustomer, "1", 0, false)
	open := released(t, s, ctx, 4)
	packed := pickAndPack(t, s, ctx, open.shipment)

	all, err := s.ListSSCCs(ctx, store.SSCCFilter{Limit: 10})
	if err != nil || len(all) != 2 {
		t.Fatalf("register = %v, %v; want the 2 issued SSCCs", ssccs(all), err)
	}
	if all[0].SSCC >= all[1].SSCC {
		t.Fatalf("register is not in SSCC order: %v", ssccs(all))
	}
	byContainer := map[string]store.SSCCRow{}
	for _, r := range all {
		if domain.ValidSSCC(r.SSCC) != nil {
			t.Fatalf("SSCC %s fails its check digit", r.SSCC)
		}
		byContainer[r.ContainerID] = r
	}
	var shipped store.SSCCRow
	for _, r := range all {
		if r.CustomerID == shippedCustomer {
			shipped = r
		}
	}
	if shipped.Status != "SHIPPED" || shipped.PackingSlipID == nil || *shipped.PackingSlipID != slip.ID || shipped.ShipmentID != slip.ShipmentID {
		t.Fatalf("shipped row = %+v, want SHIPPED on slip %s", shipped, slip.ID)
	}
	closed, ok := byContainer[packed.Container.ID]
	if !ok || closed.Status != "CLOSED" || closed.PackingSlipID != nil || closed.ShipmentNumber != open.shipment.ShipmentNumber ||
		closed.OrderID != open.shipment.OrderID || closed.CustomerID != open.shipment.CustomerID || closed.SSCC != packed.Container.SSCC {
		t.Fatalf("closed row = %+v, want container %s of shipment %s", closed, packed.Container.ID, open.shipment.ShipmentNumber)
	}

	t.Run("filters", func(t *testing.T) {
		cases := []struct {
			name string
			f    store.SSCCFilter
			want int
		}{
			{"own company prefix", store.SSCCFilter{CompanyPrefix: "0614141"}, 2},
			{"other company prefix", store.SSCCFilter{CompanyPrefix: "0614142"}, 0},
			{"customer", store.SSCCFilter{CustomerID: shippedCustomer}, 1},
			{"issued from today", store.SSCCFilter{From: today()}, 2},
			{"issued until yesterday", store.SSCCFilter{To: today().AddDate(0, 0, -1)}, 0},
			{"issued today only", store.SSCCFilter{From: today(), To: today()}, 2},
		}
		for _, tc := range cases {
			tc.f.Limit = 10
			rows, err := s.ListSSCCs(ctx, tc.f)
			if err != nil || len(rows) != tc.want {
				t.Fatalf("%s: register = %v, %v; want %d rows", tc.name, ssccs(rows), err, tc.want)
			}
		}
	})

	t.Run("an old SSCC falls in its own date range", func(t *testing.T) {
		if _, err := admin.Exec(context.Background(), `update container set created_at = now() - interval '400 days' where id = $1`, packed.Container.ID); err != nil {
			t.Fatal(err)
		}
		rows, err := s.ListSSCCs(ctx, store.SSCCFilter{To: today().AddDate(0, 0, -399), Limit: 10})
		if err != nil || len(rows) != 1 || rows[0].ContainerID != packed.Container.ID {
			t.Fatalf("old range = %v, %v; want only the back-dated container", ssccs(rows), err)
		}
	})

	t.Run("pages by cursor", func(t *testing.T) {
		first, err := s.ListSSCCs(ctx, store.SSCCFilter{Limit: 1})
		if err != nil || len(first) != 1 || first[0].SSCC != all[0].SSCC {
			t.Fatalf("first page = %v, %v; want %s", ssccs(first), err, all[0].SSCC)
		}
		next, err := s.ListSSCCs(ctx, store.SSCCFilter{AfterSSCC: first[0].SSCC, Limit: 1})
		if err != nil || len(next) != 1 || next[0].SSCC != all[1].SSCC {
			t.Fatalf("next page = %v, %v; want %s", ssccs(next), err, all[1].SSCC)
		}
	})

	t.Run("isolation", func(t *testing.T) {
		if rows, err := s.ListSSCCs(ctxFor(26, ""), store.SSCCFilter{Limit: 10}); err != nil || len(rows) != 0 {
			t.Fatalf("other tenant sees %v, %v", ssccs(rows), err)
		}
		scoped, err := s.ListSSCCs(ctxFor(25, le), store.SSCCFilter{Limit: 10})
		if err != nil || len(scoped) != 1 || scoped[0].CustomerID != shippedCustomer {
			t.Fatalf("principal scoped to one legal entity sees %v, %v; want only its own SSCC", ssccs(scoped), err)
		}
	})
}
