package store_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
	fulevents "github.com/BenjaminSRussell/passage/services/fulfillment/internal/events"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func strp(s string) *string { return &s }

func saveType(t *testing.T, s *store.Postgres, ctx context.Context, ct store.ContainerType) {
	t.Helper()
	if _, err := s.SaveContainerType(ctx, ct, idem("ct")); err != nil {
		t.Fatalf("SaveContainerType %s: %v", ct.Code, err)
	}
}

func saveRule(t *testing.T, s *store.Postgres, ctx context.Context, r store.PackingRule) store.PackingRule {
	t.Helper()
	out, err := s.SavePackingRule(ctx, r, idem("rule"))
	if err != nil {
		t.Fatalf("SavePackingRule: %v", err)
	}
	return out
}

// pickAll confirms every line at its planned quantity.
func pickAll(t *testing.T, s *store.Postgres, ctx context.Context, sh store.Shipment) store.Shipment {
	t.Helper()
	var picks []store.PickInput
	for _, l := range sh.Lines {
		picks = append(picks, store.PickInput{ShipmentLineID: l.ID, Picked: l.Planned})
	}
	picked, err := s.ConfirmPick(ctx, sh.ID, sh.Version, picks, idem("pick"))
	if err != nil {
		t.Fatalf("ConfirmPick: %v", err)
	}
	return picked
}

func outboxData(t *testing.T, admin *pgxpool.Pool, eventType, path string) string {
	t.Helper()
	var v *string
	if err := admin.QueryRow(context.Background(), `select payload->'data' #>> $2::text[] from outbox where type = $1 order by created_at desc limit 1`,
		eventType, path).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v == nil {
		return ""
	}
	return *v
}

// AC-28.11: a rule "C12, 12 per carton, max 15 kg" packs 30 picked units of 1 kg
// as 12, 12 and 6 with valid SSCCs and gross weights. A line without a rule
// stays unpacked with FUL-0012 and the shipment stays PICKED.
func TestAutoPack_AC28_11(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1, "")
	saveType(t, s, ctx, store.ContainerType{Code: "C12", TareKg: d("0.5")})
	f := released(t, s, ctx, 30, 5)
	ruled, unruled := f.shipment.Lines[0], f.shipment.Lines[1]
	rule := saveRule(t, s, ctx, store.PackingRule{ProductID: ruled.ProductID, ContainerType: "C12", QtyPerContainer: d("12"), MaxWeightKg: d("15")})
	picked := pickAll(t, s, ctx, f.shipment)

	// A unit weight for a line that is not on the shipment is refused.
	if _, err := s.AutoPack(ctx, picked.ID, picked.Version, map[string]decimal.Decimal{ids.New(): d("1")}, idem("ap-bad")); problemCode(err) != store.CodeLineNotFound {
		t.Fatalf("unknown line weight: %v, want FUL-0041", err)
	}

	res, err := s.AutoPack(ctx, picked.ID, picked.Version, map[string]decimal.Decimal{ruled.ID: d("1")}, idem("ap"))
	if err != nil {
		t.Fatalf("AutoPack: %v", err)
	}
	if len(res.Containers) != 3 {
		t.Fatalf("containers = %d, want 3", len(res.Containers))
	}
	wantQty, wantGross := []string{"12", "12", "6"}, []string{"12.5", "12.5", "6.5"}
	for i, c := range res.Containers {
		if err := domain.ValidSSCC(c.SSCC); err != nil {
			t.Errorf("container %d SSCC %s: %v", i, c.SSCC, err)
		}
		if len(c.Lines) != 1 || !c.Lines[0].Qty.Equal(d(wantQty[i])) || !c.GrossKg.Equal(d(wantGross[i])) || !c.TareKg.Equal(d("0.5")) {
			t.Errorf("container %d = %+v, want %s units at %s kg", i, c, wantQty[i], wantGross[i])
		}
	}
	if len(res.Unpacked) != 1 || res.Unpacked[0] != unruled.ID || len(res.Warnings) != 1 || res.Warnings[0].Code != store.CodeNoPackingRule {
		t.Fatalf("unpacked = %v warnings = %+v, want the unruled line with FUL-0012", res.Unpacked, res.Warnings)
	}
	if res.Shipment.Status != domain.StatusPicked || res.Shipment.Version != picked.Version+1 {
		t.Fatalf("shipment %s v%d, want PICKED v%d", res.Shipment.Status, res.Shipment.Version, picked.Version+1)
	}
	got, err := s.GetShipment(ctx, picked.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Containers) != 3 || !got.Lines[0].Packed.Equal(d("30")) || !got.Containers[0].GrossKg.Equal(d("12.5")) {
		t.Fatalf("stored shipment = %+v", got)
	}

	// Nothing left that a rule covers: no container, version unchanged.
	again, err := s.AutoPack(ctx, got.ID, got.Version, nil, idem("ap-again"))
	if err != nil || len(again.Containers) != 0 || again.Shipment.Version != got.Version {
		t.Fatalf("second auto-pack = %+v, %v; want no containers and v%d", again, err, got.Version)
	}
	// Manual repack of the remaining line completes the shipment.
	packed, err := s.PackContainer(ctx, got.ID, got.Version, "C12", []store.PackInput{{ShipmentLineID: unruled.ID, Qty: d("5")}}, idem("pack"))
	if err != nil || packed.Shipment.Status != domain.StatusPacked || !packed.Container.TareKg.Equal(d("0.5")) {
		t.Fatalf("manual pack = %+v, %v; want PACKED with the C12 tare", packed.Container, err)
	}

	// Saving the rule again for the same scope replaces it and keeps its id.
	replaced := saveRule(t, s, ctx, store.PackingRule{ProductID: ruled.ProductID, ContainerType: "C12", QtyPerContainer: d("6"), MaxWeightKg: d("15")})
	if replaced.ID != rule.ID {
		t.Fatalf("replaced rule id %s, want %s", replaced.ID, rule.ID)
	}
	rules, err := s.ListPackingRules(ctx, ruled.ProductID, 10)
	if err != nil || len(rules) != 1 || !rules[0].QtyPerContainer.Equal(d("6")) {
		t.Fatalf("rules = %+v, %v; want one rule of 6", rules, err)
	}
	if _, err := s.SavePackingRule(ctx, store.PackingRule{ProductID: ruled.ProductID, ContainerType: "NOPE", QtyPerContainer: d("1"), MaxWeightKg: d("1")}, idem("rule-bad")); problemCode(err) != store.CodeValidation {
		t.Fatalf("unknown container type: %v, want FUL-0050", err)
	}
}

func TestAutoPack_UnitAboveMaxWeightIsRefused(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1, "")
	saveType(t, s, ctx, store.ContainerType{Code: "C12", TareKg: d("0.5")})
	f := released(t, s, ctx, 2)
	line := f.shipment.Lines[0]
	saveRule(t, s, ctx, store.PackingRule{ProductID: line.ProductID, ContainerType: "C12", QtyPerContainer: d("12"), MaxWeightKg: d("15")})
	picked := pickAll(t, s, ctx, f.shipment)
	if _, err := s.AutoPack(ctx, picked.ID, picked.Version, map[string]decimal.Decimal{line.ID: d("20")}, idem("ap")); problemCode(err) != store.CodeWeightExceeded {
		t.Fatalf("err = %v, want FUL-0013", err)
	}
	got, _ := s.GetShipment(ctx, picked.ID)
	if len(got.Containers) != 0 || got.Version != picked.Version {
		t.Fatalf("a refused auto-pack changed the shipment: %+v", got)
	}
}

// AC-28.12: 100 units with 0.2 kg cardboard each at 0.50 EUR/kg in DE give a
// statistical fee of 10.00 EUR on the slip and 20 kg cardboard in the DE report.
// Cancelling the slip mirrors the fee lines so the month nets to zero.
func TestPackagingFee_AC28_12(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	f := released(t, s, ctx, 100)
	line := f.shipment.Lines[0]
	if _, err := s.SaveProductPackaging(ctx, store.ProductPackaging{ProductID: line.ProductID, MaterialCode: "CARDBOARD", WeightPerUnitKg: d("0.2")}, idem("pp")); err != nil {
		t.Fatal(err)
	}
	for _, r := range []store.FeeRate{
		{CountryCode: "DE", MaterialCode: "CARDBOARD", Currency: "EUR", RatePerKg: d("0.40"), ValidFrom: "2020-01-01"},
		{CountryCode: "DE", MaterialCode: "CARDBOARD", Currency: "EUR", RatePerKg: d("0.50"), ValidFrom: today().AddDate(0, 0, -1).Format("2006-01-02")},
		{CountryCode: "DE", MaterialCode: "CARDBOARD", Currency: "EUR", RatePerKg: d("9.99"), ValidFrom: today().AddDate(0, 0, 1).Format("2006-01-02")},
	} {
		if _, err := s.SaveFeeRate(ctx, r, idem("rate")); err != nil {
			t.Fatal(err)
		}
	}
	packed := pickAndPack(t, s, ctx, f.shipment)

	// A country without a rate for a shipped material refuses the post (FUL-0014).
	noRate := store.PostInput{PostingDate: today(), Currency: "EUR", CountryCode: "FR", Costs: costs(f.shipment, "1")}
	if _, err := s.PostPackingSlip(ctx, packed.Shipment.ID, packed.Shipment.Version, noRate, idem("post-fr")); problemCode(err) != store.CodeRateMissing {
		t.Fatalf("post to FR = %v, want FUL-0014", err)
	}

	in := store.PostInput{PostingDate: today(), Currency: "EUR", CountryCode: "DE", Costs: costs(f.shipment, "1")}
	slip, err := s.PostPackingSlip(ctx, packed.Shipment.ID, packed.Shipment.Version, in, idem("post"))
	if err != nil {
		t.Fatalf("PostPackingSlip: %v", err)
	}
	if len(slip.PackagingFees) != 1 {
		t.Fatalf("fees = %+v, want one line", slip.PackagingFees)
	}
	fee := slip.PackagingFees[0]
	if fee.MaterialCode != "CARDBOARD" || !fee.WeightKg.Equal(d("20")) || !fee.RatePerKg.Equal(d("0.5")) || fee.FeeAmount.StringFixed(2) != "10.00" || fee.Currency != "EUR" {
		t.Fatalf("fee = %+v, want 20 kg at 0.50 = 10.00 EUR", fee)
	}
	for _, j := range slip.Journal {
		if j.Account != domain.AccountDeliveredNotInvoiced && j.Account != domain.AccountInventory {
			t.Fatalf("journal has %s: the packaging fee must not post", j.Account)
		}
	}
	stored, err := s.GetPackingSlip(ctx, slip.ID)
	if err != nil || len(stored.PackagingFees) != 1 || !stored.PackagingFees[0].FeeAmount.Equal(d("10")) {
		t.Fatalf("stored slip fees = %+v, %v", stored.PackagingFees, err)
	}
	if got := outboxData(t, admin, fulevents.PackingSlipPosted, "{packaging_fees,0,fee_amount}"); got != "10" {
		t.Fatalf("posted event fee_amount = %q, want 10", got)
	}

	period := today().Format("2006-01")
	report, err := s.PackagingReport(ctx, store.ReportFilter{CountryCode: "DE", Period: period})
	if err != nil || len(report) != 1 || !report[0].WeightKg.Equal(d("20")) || !report[0].FeeAmount.Equal(d("10")) {
		t.Fatalf("report = %+v, %v; want 20 kg and 10.00", report, err)
	}
	if other, _ := s.PackagingReport(ctx, store.ReportFilter{CountryCode: "DE", Period: period, LegalEntityID: ids.New()}); len(other) != 0 {
		t.Fatalf("report for another legal entity = %+v, want none", other)
	}

	rev, err := s.CancelPackingSlip(ctx, slip.ID, idem("cancel"))
	if err != nil {
		t.Fatalf("CancelPackingSlip: %v", err)
	}
	if len(rev.PackagingFees) != 1 || !rev.PackagingFees[0].FeeAmount.Equal(d("-10")) || !rev.PackagingFees[0].WeightKg.Equal(d("-20")) {
		t.Fatalf("reversal fees = %+v, want -20 kg / -10.00", rev.PackagingFees)
	}
	report, _ = s.PackagingReport(ctx, store.ReportFilter{CountryCode: "DE", Period: period})
	if len(report) != 1 || !report[0].WeightKg.IsZero() || !report[0].FeeAmount.IsZero() {
		t.Fatalf("report after cancel = %+v, want net zero", report)
	}

	// Another tenant sees none of it.
	other := ctxFor(2, "")
	if rates, _ := s.ListFeeRates(other, "", 10); len(rates) != 0 {
		t.Fatalf("tenant 2 sees %d fee rates", len(rates))
	}
	if r, _ := s.PackagingReport(other, store.ReportFilter{CountryCode: "DE", Period: period}); len(r) != 0 {
		t.Fatalf("tenant 2 report = %+v", r)
	}
}

// The tare of a non-returnable container of a known material counts toward
// the packaging weight; 10 units at 0.2 kg plus a 0.5 kg box is 2.5 kg.
func TestPackagingFee_CountsContainerTare(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1, "")
	saveType(t, s, ctx, store.ContainerType{Code: "BOX", TareKg: d("0.5"), PackagingMaterial: strp("CARDBOARD")})
	f := released(t, s, ctx, 10)
	line := f.shipment.Lines[0]
	if _, err := s.SaveProductPackaging(ctx, store.ProductPackaging{ProductID: line.ProductID, MaterialCode: "CARDBOARD", WeightPerUnitKg: d("0.2")}, idem("pp")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveFeeRate(ctx, store.FeeRate{CountryCode: "DE", MaterialCode: "CARDBOARD", Currency: "EUR", RatePerKg: d("0.5"), ValidFrom: "2020-01-01"}, idem("rate")); err != nil {
		t.Fatal(err)
	}
	picked := pickAll(t, s, ctx, f.shipment)
	packed, err := s.PackContainer(ctx, picked.ID, picked.Version, "BOX", []store.PackInput{{ShipmentLineID: line.ID, Qty: d("10")}}, idem("pack"))
	if err != nil {
		t.Fatal(err)
	}
	slip, err := s.PostPackingSlip(ctx, picked.ID, packed.Shipment.Version, store.PostInput{PostingDate: today(), Currency: "EUR", CountryCode: "DE", Costs: costs(f.shipment, "1")}, idem("post"))
	if err != nil {
		t.Fatal(err)
	}
	if len(slip.PackagingFees) != 1 || !slip.PackagingFees[0].WeightKg.Equal(d("2.5")) || !slip.PackagingFees[0].FeeAmount.Equal(d("1.25")) {
		t.Fatalf("fees = %+v, want 2.5 kg = 1.25 EUR", slip.PackagingFees)
	}
}

// AC-28.13: 10 returnable pallets shipped to a customer and 4 received back
// leave a balance of 6, and a return of 7 is refused with FUL-0015. Cancelling
// the slip takes the pallets back off the balance, and is refused once the
// customer has returned some.
func TestReturnablePackaging_AC28_13(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	saveType(t, s, ctx, store.ContainerType{Code: "PALLET", TareKg: d("25"), Returnable: true, PackagingMaterial: strp("WOOD")})
	f := released(t, s, ctx, 10)
	line := f.shipment.Lines[0]
	customer := f.shipment.CustomerID
	saveRule(t, s, ctx, store.PackingRule{ProductID: line.ProductID, ContainerType: "PALLET", QtyPerContainer: d("1"), MaxWeightKg: d("1000")})
	picked := pickAll(t, s, ctx, f.shipment)
	res, err := s.AutoPack(ctx, picked.ID, picked.Version, nil, idem("ap"))
	if err != nil || len(res.Containers) != 10 || !res.Containers[0].Returnable {
		t.Fatalf("AutoPack = %d containers, %v; want 10 returnable pallets", len(res.Containers), err)
	}
	post := func(version int, key string) store.PackingSlip {
		t.Helper()
		// A WOOD rate is not loaded: returnable pallets carry no packaging fee.
		slip, err := s.PostPackingSlip(ctx, picked.ID, version, store.PostInput{PostingDate: today(), Currency: "EUR", CountryCode: "DE", Costs: costs(f.shipment, "1")}, idem(key))
		if err != nil {
			t.Fatalf("PostPackingSlip: %v", err)
		}
		return slip
	}
	balance := func() string {
		t.Helper()
		items, err := s.ListReturnableBalances(ctx, customer, 10)
		if err != nil || len(items) != 1 || items[0].ContainerType != "PALLET" {
			t.Fatalf("balances = %+v, %v", items, err)
		}
		return items[0].Balance.String()
	}

	slip := post(res.Shipment.Version, "post")
	if len(slip.PackagingFees) != 0 || balance() != "10" {
		t.Fatalf("after post: fees %+v balance %s, want none and 10", slip.PackagingFees, balance())
	}
	if got := outboxData(t, admin, fulevents.PackingSlipPosted, "{returnables,0,qty}"); got != "10" {
		t.Fatalf("posted event returnables qty = %q, want 10", got)
	}
	if _, err := s.CancelPackingSlip(ctx, slip.ID, idem("cancel")); err != nil {
		t.Fatal(err)
	}
	if balance() != "0" {
		t.Fatalf("after cancel balance = %s, want 0", balance())
	}
	sh, _ := s.GetShipment(ctx, picked.ID)
	slip = post(sh.Version, "repost")

	ret, err := s.ReturnPackaging(ctx, store.ReturnInput{CustomerID: customer, ContainerType: "PALLET", Qty: d("4")}, idem("ret"))
	if err != nil || !ret.Balance.Equal(d("6")) || ret.LastReturnedAt == nil || ret.LastShippedAt == nil {
		t.Fatalf("return 4 = %+v, %v; want balance 6 with both dates", ret, err)
	}
	if _, err := s.ReturnPackaging(ctx, store.ReturnInput{CustomerID: customer, ContainerType: "PALLET", Qty: d("7")}, idem("ret7")); problemCode(err) != store.CodeRTPNegative {
		t.Fatalf("return 7 = %v, want FUL-0015", err)
	}
	if _, err := s.CancelPackingSlip(ctx, slip.ID, idem("cancel2")); problemCode(err) != store.CodeRTPNegative {
		t.Fatalf("cancel after a return = %v, want FUL-0015", err)
	}
	if balance() != "6" {
		t.Fatalf("balance = %s, want 6 after the refused return and cancel", balance())
	}
	// A customer with no balance cannot return anything; another tenant sees no balances.
	if _, err := s.ReturnPackaging(ctx, store.ReturnInput{CustomerID: ids.New(), ContainerType: "PALLET", Qty: d("1")}, idem("ret-none")); problemCode(err) != store.CodeRTPNegative {
		t.Fatalf("return without balance = %v, want FUL-0015", err)
	}
	if items, _ := s.ListReturnableBalances(ctxFor(2, ""), "", 10); len(items) != 0 {
		t.Fatalf("tenant 2 sees %d balances", len(items))
	}
	if types, _ := s.ListContainerTypes(ctxFor(2, ""), 10); len(types) != 0 {
		t.Fatalf("tenant 2 sees %d container types", len(types))
	}
}

// A replayed return receipt returns the stored balance and moves nothing twice.
func TestReturnPackaging_IdempotentReplay(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1, "")
	saveType(t, s, ctx, store.ContainerType{Code: "CRATE", TareKg: d("2"), Returnable: true})
	f := released(t, s, ctx, 3)
	line := f.shipment.Lines[0]
	saveRule(t, s, ctx, store.PackingRule{ProductID: line.ProductID, CustomerID: strp(f.shipment.CustomerID), ContainerType: "CRATE", QtyPerContainer: d("1"), MaxWeightKg: d("50")})
	picked := pickAll(t, s, ctx, f.shipment)
	res, err := s.AutoPack(ctx, picked.ID, picked.Version, nil, idem("ap"))
	if err != nil || len(res.Containers) != 3 {
		t.Fatalf("AutoPack with a customer rule = %+v, %v", res.Containers, err)
	}
	if _, err := s.PostPackingSlip(ctx, picked.ID, res.Shipment.Version, store.PostInput{PostingDate: today(), Currency: "USD", Costs: costs(f.shipment, "1")}, idem("post")); err != nil {
		t.Fatal(err)
	}
	key := idem("ret")
	first, err := s.ReturnPackaging(ctx, store.ReturnInput{CustomerID: f.shipment.CustomerID, ContainerType: "CRATE", Qty: d("2")}, key)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.ReturnPackaging(ctx, store.ReturnInput{CustomerID: f.shipment.CustomerID, ContainerType: "CRATE", Qty: d("2")}, key)
	if err != nil || !second.Balance.Equal(first.Balance) || !first.Balance.Equal(d("1")) {
		t.Fatalf("replay = %+v, %v; first %+v; want balance 1 both times", second, err, first)
	}
}
