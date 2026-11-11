package store_test

import (
	"errors"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/tax/internal/domain"
	"github.com/BenjaminSRussell/passage/services/tax/internal/store"
)

func entry(party, code, date, gross, base, withheld string) domain.WithholdingEntry {
	return domain.WithholdingEntry{ID: ids.New(), PartyType: "vendor", PartyRef: party, Code: code, PaymentDate: domain.Day(date), Currency: "USD",
		Gross: domain.D(gross), TaxableBase: domain.D(base), Withheld: domain.D(withheld)}
}

func TestWithholdingRemittance_NumbersGapFreeCertifiesPerPayeeAndRemitsOnce(t *testing.T) {
	s, _, ctx := setup(t)
	a1, a2 := entry("vendor-a", "NEC", "2026-01-10", "1000", "1000", "240"), entry("vendor-a", "NEC", "2026-01-20", "500", "500", "120")
	b1 := entry("vendor-b", "NEC", "2026-01-15", "700", "100", "24")
	c1 := entry("vendor-a", "ROY", "2026-02-03", "200", "200", "20")
	late := entry("vendor-a", "NEC", "2026-02-20", "100", "100", "24")
	all := []domain.WithholdingEntry{a1, a2, b1, c1, late}
	if n, err := s.RecordWithholding(ctx, all); err != nil || n != 5 {
		t.Fatalf("record: %d %v", n, err)
	}
	if n, err := s.RecordWithholding(ctx, all[:2]); err != nil || n != 0 {
		t.Fatalf("an identical replay: %d %v", n, err)
	}
	changed := a1
	changed.Withheld = domain.D("241")
	if _, err := s.RecordWithholding(ctx, []domain.WithholdingEntry{changed}); asDomainError(err) == nil {
		t.Errorf("a replay with other content was accepted: %v", err)
	}

	run, err := s.RunRemittance(ctx, domain.Period{From: domain.Day("2026-01-01"), To: domain.Day("2026-02-15")})
	if err != nil {
		t.Fatal(err)
	}
	if run.Number != "WHR-2026-0001" || len(run.Lines) != 2 || len(run.Certificates) != 3 {
		t.Fatalf("run = %+v", run)
	}
	nec, roy := run.Lines[0], run.Lines[1]
	if nec.Code != "NEC" || nec.Withheld.String() != "384" || nec.Gross.String() != "2200" || nec.Entries != 3 || nec.Payees != 2 || roy.Code != "ROY" || roy.Withheld.String() != "20" {
		t.Fatalf("lines = %+v", run.Lines)
	}
	numbers := map[string]bool{}
	for _, c := range run.Certificates {
		numbers[c.Number] = true
		if c.PartyRef == "vendor-a" && c.Code == "NEC" && (c.Withheld.String() != "360" || c.Entries != 2) {
			t.Errorf("vendor-a NEC certificate = %+v", c)
		}
	}
	for _, want := range []string{"WHC-2026-000001", "WHC-2026-000002", "WHC-2026-000003"} {
		if !numbers[want] {
			t.Errorf("certificate numbers = %v, want %s among them", numbers, want)
		}
	}
	// What was remitted is not remitted again; the entry outside the period waits for its own run, numbered next.
	if _, err := s.RunRemittance(ctx, domain.Period{From: domain.Day("2026-01-01"), To: domain.Day("2026-02-15")}); asDomainError(err) == nil || asDomainError(err).Code != domain.CodeNothingToRemit {
		t.Fatalf("a second run for the same period: %v", err)
	}
	second, err := s.RunRemittance(ctx, domain.Period{From: domain.Day("2026-02-16"), To: domain.Day("2026-02-28")})
	if err != nil || second.Number != "WHR-2026-0002" || len(second.Certificates) != 1 || second.Certificates[0].Number != "WHC-2026-000004" {
		t.Fatalf("second run = %+v, %v", second, err)
	}

	got, err := s.GetRemittanceRun(ctx, run.ID)
	if err != nil || len(got.Certificates) != 3 || got.Lines[0].Withheld.String() != "384" {
		t.Fatalf("get = %+v, %v", got, err)
	}
	if _, err := s.GetRemittanceRun(ctx, ids.New()); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("an unknown run: %v", err)
	}
	if runs, err := s.ListRemittanceRuns(ctx); err != nil || len(runs) != 2 || runs[0].Number != "WHR-2026-0002" {
		t.Fatalf("list = %+v, %v", runs, err)
	}
	if certs, err := s.ListWithholdingCertificates(ctx, "vendor-b"); err != nil || len(certs) != 1 || certs[0].PartyRef != "vendor-b" {
		t.Fatalf("certificates of vendor-b = %+v, %v", certs, err)
	}
	if certs, err := s.ListWithholdingCertificates(ctx, ""); err != nil || len(certs) != 4 {
		t.Fatalf("all certificates = %d, %v", len(certs), err)
	}
	// A new year starts its own sequence.
	if _, err := s.RecordWithholding(ctx, []domain.WithholdingEntry{entry("vendor-a", "NEC", "2027-01-05", "100", "100", "24")}); err != nil {
		t.Fatal(err)
	}
	if next, err := s.RunRemittance(ctx, domain.Period{From: domain.Day("2027-01-01"), To: domain.Day("2027-01-31")}); err != nil || next.Number != "WHR-2027-0001" || next.Certificates[0].Number != "WHC-2027-000001" {
		t.Fatalf("a new year = %+v, %v", next, err)
	}
}

const (
	pump  = "0190f0a0-0000-7000-8000-0000000000a1"
	valve = "0190f0a0-0000-7000-8000-0000000000a2"
)

func TestIntrastatFeed_StagesTransfersAndCompletesOnceTheProductIsMapped(t *testing.T) {
	s, _, ctx := setup(t)
	if err := s.PutEntitySetting(ctx, store.EntitySetting{HomeCountry: "DE", VATID: "DE123456789"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutIntrastatProduct(ctx, domain.IntrastatProduct{ProductID: pump, CommodityCode: "84137030", OriginCountry: "US", NetMassPerUnit: domain.D("12.5"), SupplementaryPerUnit: domain.D("1")}); err != nil {
		t.Fatal(err)
	}
	line := func(no int, product, qty, value string) domain.MovementLine {
		return domain.MovementLine{LineNo: no, ProductID: product, Qty: domain.D(qty), Value: domain.D(value)}
	}
	movements := []domain.Movement{
		{Flow: "dispatch", SourceRef: "SHIP-1", MovementDate: domain.Day("2026-03-04"), PartnerCountry: "FR", Lines: []domain.MovementLine{line(1, pump, "4", "2000")}},
		{Flow: "dispatch", SourceRef: "SHIP-2", MovementDate: domain.Day("2026-03-09"), PartnerCountry: "FR", Lines: []domain.MovementLine{line(1, pump, "1", "500"), line(2, valve, "2", "80")}},
		{Flow: "dispatch", SourceRef: "SHIP-3", MovementDate: domain.Day("2026-03-10"), PartnerCountry: "DE", Lines: []domain.MovementLine{line(1, pump, "1", "500")}},
		{Flow: "arrival", SourceRef: "RCV-1", MovementDate: domain.Day("2026-03-05"), PartnerCountry: "FR", Lines: []domain.MovementLine{line(1, pump, "9", "900")}},
	}
	if n, err := s.StageMovements(ctx, movements); err != nil || n != 4 {
		t.Fatalf("stage: %d %v", n, err)
	}
	if n, err := s.StageMovements(ctx, movements[:2]); err != nil || n != 0 {
		t.Fatalf("an identical replay: %d %v", n, err)
	}
	changed := movements[0]
	changed.Lines = []domain.MovementLine{line(1, pump, "5", "2500")}
	if _, err := s.StageMovements(ctx, []domain.Movement{changed}); asDomainError(err) == nil {
		t.Errorf("a changed replay was accepted: %v", err)
	}

	j, err := s.CreateJournal(ctx, "dispatch", domain.Day("2026-03-01"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.TransferMovements(ctx, j.ID, domain.MovementDefaults{TransactionCode: "11", TransportMode: "3"})
	if err != nil || res.Added != 2 || res.Ignored != 1 || res.Complete != 1 || len(res.Unmapped) != 1 || res.Unmapped[0].ProductID != valve {
		t.Fatalf("first transfer = %+v, %v", res, err)
	}
	got, _ := s.GetJournal(ctx, j.ID)
	if len(got.Lines) != 2 || got.Lines[0].NetMass.String() != "50" || got.Lines[0].TransactionCode != "11" || got.Lines[1].SourceRef != "SHIP-2:1" {
		t.Fatalf("journal lines = %+v", got.Lines)
	}
	staged, _ := s.ListMovements(ctx, "staged")
	transferred, _ := s.ListMovements(ctx, "transferred")
	ignored, _ := s.ListMovements(ctx, "ignored")
	if len(staged) != 2 || len(transferred) != 1 || len(ignored) != 1 {
		t.Fatalf("staged %d (SHIP-2 and the arrival), transferred %d, ignored %d", len(staged), len(transferred), len(ignored))
	}

	// Map the valve and transfer again: only the missing line is added, and the movement is complete.
	if err := s.PutIntrastatProduct(ctx, domain.IntrastatProduct{ProductID: valve, CommodityCode: "84818099", OriginCountry: "CN", NetMassPerUnit: domain.D("2")}); err != nil {
		t.Fatal(err)
	}
	again, err := s.TransferMovements(ctx, j.ID, domain.MovementDefaults{TransactionCode: "11", TransportMode: "3"})
	if err != nil || again.Added != 1 || again.Skipped != 1 || again.Complete != 1 || len(again.Unmapped) != 0 {
		t.Fatalf("second transfer = %+v, %v", again, err)
	}
	if third, err := s.TransferMovements(ctx, j.ID, domain.MovementDefaults{}); err != nil || third.Added != 0 || third.Complete != 0 {
		t.Fatalf("third transfer = %+v, %v", third, err)
	}
	if products, err := s.ListIntrastatProducts(ctx); err != nil || len(products) != 2 {
		t.Fatalf("products = %+v, %v", products, err)
	}
	if err := s.DeleteIntrastatProduct(ctx, valve); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteIntrastatProduct(ctx, valve); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("deleting twice: %v", err)
	}
}
