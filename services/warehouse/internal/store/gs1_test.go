package store

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/warehouse/internal/domain"
)

func blankASN(wh, ref string, pallets int) domain.Receipt {
	r := domain.Receipt{ID: ids.New(), WarehouseID: wh, ASNRef: ref, TolerancePct: domain.DefaultASNTolerance()}
	for n := 1; n <= pallets; n++ {
		r.Lines = append(r.Lines, domain.ReceiptLine{LineNo: n, ItemID: "BOLT", ExpectedQty: dq("10"), Lot: "L-" + ref, ExpiryDate: "2027-03-31"})
	}
	return r
}

// #32 (32-3): a pallet with no license plate on the ASN gets an SSCC from the tenant's GS1 counter when it is
// received, and its license plate id is that SSCC; the put-away work carries both with the lot and expiry the label
// needs, so the device prints from the work and scans nothing a second time.
func TestStore_ReceiveGeneratesTheLicensePlateAndSSCC(t *testing.T) {
	st, ctx, wh := setup(t)
	mustLocations(t, st, ctx, wh, loc("A-01", "A", 1, domain.Capacity{AllowMixedItems: true}))
	mustDirective(t, st, ctx, directive(wh, "PURCHASE_PUTAWAY", domain.DirectionPut, domain.StrategyConsolidate, "A"))
	r, _, err := st.RegisterASN(ctx, key("asn-blank"), blankASN(wh, "ASN-B", 3))
	if err != nil {
		t.Fatal(err)
	}
	recv := func(line int, k string) (Received, error) {
		got, _, err := st.ReceiveLine(ctx, key(k), ReceiveInput{ReceiptID: r.ID, LineNo: line, Qty: dq("10"), User: "clerk", Now: time.Now()})
		return got, err
	}
	// Before a prefix is set, a pallet with no license plate cannot be labelled.
	_, err = recv(1, "r1")
	assertCode(t, err, domain.CodeGS1NotSet)
	if _, err := st.GetGS1Company(ctx); !errors.Is(err, ErrGS1NotSet) {
		t.Fatalf("settings before they are set: %v", err)
	}

	company, err := st.PutGS1Company(ctx, "0614141", 3, "admin")
	if err != nil || company.NextSerial != 1 || company.Issued != 0 {
		t.Fatalf("set the prefix: %+v %v", company, err)
	}
	first, err := recv(1, "r1b")
	if err != nil {
		t.Fatal(err)
	}
	l1 := first.Receipt.Lines[0]
	if want := "306141410000000013"; l1.SSCC != want || l1.LPID != want || !domain.ValidSSCC(l1.SSCC) {
		t.Fatalf("first pallet SSCC %q LP %q, want %q", l1.SSCC, l1.LPID, want)
	}
	attrs := first.Work.Lines[0].Attributes
	if attrs["sscc"] != l1.SSCC || attrs["lpId"] != l1.SSCC || attrs["lot"] != "L-ASN-B" || attrs["expiry"] != "2027-03-31" {
		t.Fatalf("the work line carries %v", attrs)
	}
	// The count overrides the lot and expiry of the ASN.
	second, _, err := st.ReceiveLine(ctx, key("r2"), ReceiveInput{ReceiptID: r.ID, LineNo: 2, Qty: dq("10"), Lot: "L-FOUND", ExpiryDate: "2027-06-30", User: "clerk", Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	l2 := second.Receipt.Lines[1]
	if l2.SSCC != "306141410000000020" || l2.Lot != "L-FOUND" || l2.ExpiryDate != "2027-06-30" {
		t.Fatalf("second pallet %+v", l2)
	}
	// A replay of the same receive draws no new SSCC.
	if again, replayed, err := st.ReceiveLine(ctx, key("r2"), ReceiveInput{ReceiptID: r.ID, LineNo: 2, Qty: dq("10"), Lot: "L-FOUND", ExpiryDate: "2027-06-30", User: "clerk", Now: time.Now()}); err != nil || !replayed || again.Receipt.Lines[1].SSCC != l2.SSCC {
		t.Fatalf("replay: %v %v", replayed, err)
	}
	stored, _ := st.GetReceipt(ctx, r.ID)
	if stored.Lines[0].SSCC != l1.SSCC || stored.Lines[1].LPID != l2.LPID || stored.Lines[2].SSCC != "" {
		t.Fatalf("stored lines %+v", stored.Lines)
	}
	if c, _ := st.GetGS1Company(ctx); c.Issued != 2 {
		t.Fatalf("issued %d, want 2", c.Issued)
	}
	// The prefix is locked once SSCCs exist; the extension digit is not.
	_, err = st.PutGS1Company(ctx, "0614142", 3, "admin")
	assertCode(t, err, domain.CodeGS1Locked)
	if c, err := st.PutGS1Company(ctx, "0614141", 5, "admin"); err != nil || c.Extension != 5 || c.NextSerial != 3 {
		t.Fatalf("extension change: %+v %v", c, err)
	}
}

// An ASN that names its license plates keeps them; one that names an SSCC uses it as the pallet's SSCC; neither draws
// from the counter, so a warehouse with no GS1 prefix keeps receiving as before.
func TestStore_ReceiveKeepsTheLicensePlateTheASNNamed(t *testing.T) {
	st, ctx, wh := setup(t)
	sscc := "306141410000000013"
	r := domain.Receipt{ID: ids.New(), WarehouseID: wh, ASNRef: "ASN-N", TolerancePct: domain.DefaultASNTolerance(), Lines: []domain.ReceiptLine{
		{LineNo: 1, ItemID: "BOLT", LPID: "LP-77", ExpectedQty: dq("10")},
		{LineNo: 2, ItemID: "BOLT", LPID: sscc, ExpectedQty: dq("10")},
	}}
	if _, _, err := st.RegisterASN(ctx, key("asn-n"), r); err != nil {
		t.Fatal(err)
	}
	got, _, err := st.ReceiveLine(ctx, key("n1"), ReceiveInput{ReceiptID: r.ID, LineNo: 1, Qty: dq("10"), User: "clerk", Now: time.Now()})
	if err != nil || got.Receipt.Lines[0].LPID != "LP-77" || got.Receipt.Lines[0].SSCC != "" {
		t.Fatalf("named plate without a GS1 prefix: %+v %v", got.Receipt.Lines[0], err)
	}
	got, _, err = st.ReceiveLine(ctx, key("n2"), ReceiveInput{ReceiptID: r.ID, LineNo: 2, Qty: dq("10"), User: "clerk", Now: time.Now()})
	if err != nil || got.Receipt.Lines[1].SSCC != sscc || got.Receipt.Lines[1].LPID != sscc {
		t.Fatalf("an SSCC as the plate: %+v %v", got.Receipt.Lines[1], err)
	}
}

// Receipts racing for SSCCs never share one: the counter row is locked, so every pallet gets its own.
func TestStore_ConcurrentReceiptsDrawDistinctSSCCs(t *testing.T) {
	st, ctx, wh := setup(t)
	if _, err := st.PutGS1Company(ctx, "0614141", 0, "admin"); err != nil {
		t.Fatal(err)
	}
	const pallets = 10
	r, _, err := st.RegisterASN(ctx, key("asn-race"), blankASN(wh, "ASN-R", pallets))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	got := make([]string, pallets)
	errs := make([]error, pallets)
	for i := 0; i < pallets; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, _, err := st.ReceiveLine(ctx, key("race-"+ids.New()), ReceiveInput{ReceiptID: r.ID, LineNo: i + 1, Qty: dq("10"), User: "clerk", Now: time.Now()})
			errs[i] = err
			if err == nil {
				got[i] = res.Receipt.Lines[i].SSCC
			}
		}(i)
	}
	wg.Wait()
	seen := map[string]bool{}
	for i, err := range errs {
		if err != nil && !strings.Contains(err.Error(), "version") {
			t.Fatalf("pallet %d: %v", i+1, err)
		}
		if got[i] != "" {
			if seen[got[i]] {
				t.Fatalf("SSCC %s issued twice", got[i])
			}
			seen[got[i]] = true
		}
	}
}

func TestSSCCForSerial(t *testing.T) {
	if got, err := domain.SSCCForSerial(3, "0614141", 1); err != nil || got != "306141410000000013" {
		t.Fatalf("first: %q %v", got, err)
	}
	if got, err := domain.SSCCForSerial(0, "12345678901", 99999); err != nil || len(got) != 18 || !domain.ValidSSCC(got) {
		t.Fatalf("an 11-digit prefix: %q %v", got, err)
	}
	if _, err := domain.SSCCForSerial(0, "12345678901", 100000); err == nil {
		t.Fatal("serial past the capacity of an 11-digit prefix")
	}
	if _, err := domain.SSCCForSerial(0, "123", 1); err == nil {
		t.Fatal("a short prefix")
	}
	if domain.SerialCapacity("0614141") != 1_000_000_000 {
		t.Fatalf("capacity %d", domain.SerialCapacity("0614141"))
	}
}
