package store_test

// Wave 16 proof, AC4 (issue #16, PRD AC-16.4, fixture inv-fefo-100): a FEFO
// reservation selects the earliest-expiry eligible batch across 100 batches.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

type fefoFixture struct {
	RequiredDate string `json:"requiredDate"`
	FilterSite   string `json:"filterSite"`
	Rows         []struct {
		Batch        string  `json:"batch"`
		Site         string  `json:"site"`
		Warehouse    string  `json:"warehouse"`
		Location     string  `json:"location"`
		Qty          string  `json:"qty"`
		ReceiptDate  string  `json:"receiptDate"`
		Expiry       *string `json:"expiry"`
		ExpiryRowNil bool    `json:"expiryRowNull"`
		Segment      *string `json:"segment"`
		LaterReceipt *struct {
			Qty  string `json:"qty"`
			Date string `json:"date"`
		} `json:"laterReceipt"`
		Block *struct {
			Qty    string `json:"qty"`
			Target string `json:"target"`
		} `json:"block"`
		ReservedBefore string `json:"reservedBefore"`
	} `json:"rows"`
	Reservations []struct {
		Qty          string `json:"qty"`
		MarginDays   int    `json:"marginDays"`
		AllowPartial bool   `json:"allowPartial"`
	} `json:"reservations"`
	Expected []struct {
		Code        string `json:"code"`
		Allocations []struct {
			Batch string `json:"batch"`
			Qty   string `json:"qty"`
		} `json:"allocations"`
		Shortfall string `json:"shortfall"`
	} `json:"expected"`
}

func isoDate(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// TestAC4FEFOAcross100BatchesMatchesTheReference loads testdata/inv-fefo-100.json
// (written by testdata/gen_fefo_100.py, an independent reference of FR-16.7 /
// BR-16.7 / FR-16.20 run with python3 -I), builds its stock through the store
// (dated opening-balance receipts, batch expiries, blockings and quarantines,
// MANUAL pre-reservations, decoys at another site and in a sales-order segment)
// and runs its four FEFO reservations in sequence through Reserve. Each must
// return exactly the reference's allocations in pick order and its shortfall;
// the first is AC-16.4's 50 units, the last finds nothing eligible (INV-RES-002).
func TestAC4FEFOAcross100BatchesMatchesTheReference(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "inv-fefo-100.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fx fefoFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	batches := 0
	for _, r := range fx.Rows {
		if r.Site == fx.FilterSite && r.Segment == nil && r.Batch != "" {
			batches++
		}
	}
	if batches != 100 {
		t.Fatalf("fixture has %d candidate batches, want 100", batches)
	}

	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	rowDims := func(i int) domain.Dimensions {
		r := fx.Rows[i]
		d := domain.Dimensions{domain.DimSite: r.Site, domain.DimWarehouse: r.Warehouse, domain.DimLocation: r.Location}
		if r.Batch != "" {
			d[domain.DimBatch] = r.Batch
		}
		if r.Segment != nil {
			d[domain.DimSegment] = *r.Segment
		}
		return d
	}
	// Receipts grouped per posting date: one journal per date, so each row's
	// earliest receipt date is the fixture's.
	byDate := map[string][]domain.JournalLine{}
	for i, r := range fx.Rows {
		byDate[r.ReceiptDate] = append(byDate[r.ReceiptDate], line(product, r.Qty, "3", rowDims(i)))
		if r.LaterReceipt != nil {
			byDate[r.LaterReceipt.Date] = append(byDate[r.LaterReceipt.Date], line(product, r.LaterReceipt.Qty, "3", rowDims(i)))
		}
		if r.Batch == "" {
			continue
		}
		switch {
		case r.Expiry != nil:
			at := isoDate(t, *r.Expiry)
			if err := s.UpsertBatchExpiry(ctx, product, r.Batch, &at); err != nil {
				t.Fatal(err)
			}
		case r.ExpiryRowNil:
			if err := s.UpsertBatchExpiry(ctx, product, r.Batch, nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	dates := make([]string, 0, len(byDate))
	for d := range byDate {
		dates = append(dates, d)
	}
	sort.Strings(dates)
	for _, d := range dates {
		postDated(t, ctx, s, isoDate(t, d), domain.JournalOpeningBalance, byDate[d]...)
	}
	required := isoDate(t, fx.RequiredDate)
	for i, r := range fx.Rows {
		if r.Block != nil {
			if _, _, err := s.CreateBlocking(ctx, store.BlockingInput{IdempotencyKey: fmt.Sprintf("blk-%d", i), LegalEntityID: legalEntity(ctx),
				ProductID: product, Dims: rowDims(i), Qty: dec(r.Block.Qty), Unit: "EA", Target: r.Block.Target, Reason: "QUALITY",
				PhysicalDate: required}); err != nil {
				t.Fatalf("block row %d: %v", i, err)
			}
		}
		if q := dec(r.ReservedBefore); q.Sign() > 0 {
			if _, err := reserve(ctx, s, product, r.ReservedBefore, domain.PolicyManual, rowDims(i), fmt.Sprintf("pre-%d", i)); err != nil {
				t.Fatalf("pre-reserve row %d: %v", i, err)
			}
		}
	}

	for n, rq := range fx.Reservations {
		want := fx.Expected[n]
		res, err := s.Reserve(ctx, store.ReserveInput{LegalEntityID: legalEntity(ctx), ProductID: product, Qty: dec(rq.Qty),
			DemandType: domain.DemandSalesOrder, DemandID: fmt.Sprintf("SO-FEFO-%d", n), Policy: domain.PolicyFEFO,
			Dims: domain.Dimensions{domain.DimSite: fx.FilterSite}, RequiredDate: required, ShelfLifeMarginDays: rq.MarginDays,
			AllowPartial: rq.AllowPartial, IdempotencyKey: fmt.Sprintf("fefo-%d", n)})
		if want.Code != "" {
			if problemCode(err) != want.Code {
				t.Fatalf("reservation %d: %v, want %s", n, err, want.Code)
			}
			continue
		}
		if err != nil {
			t.Fatalf("reservation %d: %v", n, err)
		}
		if len(res.Reservations) != len(want.Allocations) {
			t.Fatalf("reservation %d: %d allocations, want %d\n got  %v\n want %v", n, len(res.Reservations), len(want.Allocations),
				describeAllocations(res.Reservations), want.Allocations)
		}
		for k, a := range want.Allocations {
			got := res.Reservations[k]
			if got.Dims[domain.DimBatch] != a.Batch || !got.Qty.Equal(dec(a.Qty)) {
				t.Fatalf("reservation %d pick %d = %s:%s, want %s:%s\n got  %v", n, k, got.Dims[domain.DimBatch], got.Qty, a.Batch, a.Qty,
					describeAllocations(res.Reservations))
			}
		}
		if !res.Shortfall.Equal(dec(want.Shortfall)) {
			t.Fatalf("reservation %d shortfall %s, want %s", n, res.Shortfall, want.Shortfall)
		}
		if n == 0 {
			t.Logf("AC-16.4: 50 units over %d batches, first pick %s (expiry on the shelf-life cutoff)", len(res.Reservations),
				res.Reservations[0].Dims[domain.DimBatch])
		}
	}
	if rep, err := s.RebuildOnHand(tenantWideCtx(1), false); err != nil || rep.DiffCount != 0 {
		t.Fatalf("rebuild after the fixture: %v %+v", err, rep)
	}
}

func describeAllocations(rs []domain.Reservation) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Dims[domain.DimBatch] + ":" + r.Qty.String()
	}
	return out
}
