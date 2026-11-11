package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/tax/internal/domain"
	"github.com/BenjaminSRussell/passage/services/tax/internal/store"
)

func rate(from, pct string) domain.Rate {
	return domain.Rate{ValidFrom: domain.Day(from), RatePct: domain.D(pct)}
}

func seedConfig(t *testing.T, s *store.Store, ctx context.Context) {
	t.Helper()
	for _, j := range []domain.Jurisdiction{
		{Code: "US", Name: "United States", Kind: "country", Country: "US"},
		{Code: "US-OH", Name: "Ohio", Kind: "state", Country: "US", ParentCode: "US"},
		{Code: "DE", Name: "Germany", Kind: "country", Country: "DE"},
	} {
		if err := s.PutJurisdiction(ctx, j); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []domain.ConfigCode{
		{Name: "Ohio sales tax", Code: domain.Code{Code: "OH-ST", Jurisdiction: "US-OH", Rates: []domain.Rate{rate("2020-01-01", "5.75")}}},
		{Name: "German VAT 19%", Code: domain.Code{Code: "VAT19", Jurisdiction: "DE", Rates: []domain.Rate{rate("2020-01-01", "19")}}},
	} {
		if err := s.PutCode(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	for _, g := range []domain.Group{
		{Kind: "party", Code: "DOMESTIC-DE", Name: "German customers", TaxCodes: []string{"VAT19"}},
		{Kind: "item", Code: "STANDARD", Name: "Standard goods", TaxCodes: []string{"VAT19", "OH-ST"}},
	} {
		if err := s.PutGroup(ctx, g); err != nil {
			t.Fatal(err)
		}
	}
}

func asDomainError(err error) *domain.Error {
	var de *domain.Error
	if errors.As(err, &de) {
		return de
	}
	return nil
}

func TestConfigRoundTripAndDrivesTheEngine(t *testing.T) {
	s, _, ctx := setup(t)
	seedConfig(t, s, ctx)

	tree, err := s.ListJurisdictions(ctx)
	if err != nil || len(tree) != 3 || tree[2].ParentCode != "US" {
		t.Fatalf("tree = %+v, %v", tree, err)
	}
	cfg, err := s.LoadConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Codes) != 2 || len(cfg.Groups["DOMESTIC-DE"]) != 1 || len(cfg.ItemGroups["STANDARD"]) != 2 {
		t.Fatalf("assembled config = %+v", cfg)
	}
	res, err := domain.Calculate(domain.Document{
		Currency: "EUR", Direction: domain.DirOutput, DocumentDate: domain.Day("2026-03-15"), Party: domain.Party{TaxGroup: "DOMESTIC-DE"},
		Lines: []domain.Line{{LineRef: "L1", Amount: domain.D("200"), ItemTaxGroup: "STANDARD"}},
	}, cfg)
	if err != nil || res.DocumentTaxesByCode[0].TaxAmount.String() != "38" {
		t.Fatalf("calculate with the stored config: %+v, %v", res, err)
	}

	codes, err := s.ListCodes(ctx)
	if err != nil || len(codes) != 2 || codes[1].Rates[0].RatePct.String() != "5.75" && codes[0].Rates[0].RatePct.String() != "5.75" {
		t.Fatalf("codes = %+v, %v", codes, err)
	}
	// An update replaces; the rate history survives a round trip with its open end.
	updated := domain.ConfigCode{Name: "German VAT", Code: domain.Code{Code: "VAT19", Jurisdiction: "DE", Rates: []domain.Rate{
		{ValidFrom: domain.Day("2020-01-01"), ValidTo: domain.Day("2020-12-31"), RatePct: domain.D("16")}, rate("2021-01-01", "19")}}}
	if err := s.PutCode(ctx, updated); err != nil {
		t.Fatal(err)
	}
	codes, _ = s.ListCodes(ctx)
	for _, c := range codes {
		if c.Code.Code == "VAT19" && (len(c.Rates) != 2 || c.Rates[0].ValidTo.IsZero() || !c.Rates[1].ValidTo.IsZero()) {
			t.Errorf("rate history did not round trip: %+v", c.Rates)
		}
	}
}

func TestConfigRefusesBrokenReferencesAndLoops(t *testing.T) {
	s, _, ctx := setup(t)
	seedConfig(t, s, ctx)

	if err := s.PutJurisdiction(ctx, domain.Jurisdiction{Code: "US", Name: "US", Kind: "country", Country: "US", ParentCode: "US-OH"}); asDomainError(err) == nil {
		t.Errorf("moving a country below its own state was accepted: %v", err)
	}
	if err := s.PutJurisdiction(ctx, domain.Jurisdiction{Code: "X", Name: "X", Kind: "city", Country: "US", ParentCode: "NOPE"}); asDomainError(err) == nil {
		t.Errorf("a missing parent was accepted: %v", err)
	}
	if err := s.PutCode(ctx, domain.ConfigCode{Name: "Orphan", Code: domain.Code{Code: "ORPH", Jurisdiction: "NOWHERE"}}); asDomainError(err) == nil {
		t.Errorf("a tax code in a missing jurisdiction was accepted: %v", err)
	}
	if err := s.PutGroup(ctx, domain.Group{Kind: "item", Code: "BAD", Name: "Bad", TaxCodes: []string{"GONE"}}); asDomainError(err) == nil {
		t.Errorf("a group naming a missing tax code was accepted: %v", err)
	}
	for name, err := range map[string]error{
		"a jurisdiction with children": s.DeleteJurisdiction(ctx, "US"),
		"a jurisdiction with a code":   s.DeleteJurisdiction(ctx, "DE"),
		"a code used by a group":       s.DeleteCode(ctx, "VAT19"),
	} {
		if de := asDomainError(err); de == nil || de.Code != domain.CodeConfigInUse {
			t.Errorf("deleting %s: %v, want TAX-0013", name, err)
		}
	}
	if err := s.DeleteGroup(ctx, "item", "STANDARD"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteCode(ctx, "OH-ST"); err != nil {
		t.Errorf("deleting an unused code: %v", err)
	}
	if err := s.DeleteCode(ctx, "OH-ST"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("deleting twice: %v", err)
	}
}

func TestConfigIsPerTenant(t *testing.T) {
	s, _, ctx := setup(t)
	seedConfig(t, s, ctx)
	other := testkit.Ctx(ids.New(), "tax.config.read")
	p, _ := tenant.FromContext(other)
	p.LegalEntityID = ids.New()
	other = tenant.With(other, p)
	if tree, err := s.ListJurisdictions(other); err != nil || len(tree) != 0 {
		t.Errorf("another tenant sees %d jurisdictions: %v", len(tree), err)
	}
	if err := s.PutCode(other, domain.ConfigCode{Name: "X", Code: domain.Code{Code: "VAT19", Jurisdiction: "DE"}}); asDomainError(err) == nil {
		t.Errorf("another tenant could use this tenant's jurisdiction: %v", err)
	}
}

func recordTxn(t *testing.T, s *store.Store, ctx context.Context, code, dir, date, tax string, base *domain.Dec, country, vat, kind string) {
	t.Helper()
	txn := domain.Txn{ID: ids.New(), Code: code, Direction: dir, TaxDate: domain.Day(date), Amount: domain.D(tax), Base: base,
		CounterpartyCountry: country, CounterpartyVATID: vat, SupplyKind: kind}
	if n, err := s.RecordTransactions(ctx, []domain.Txn{txn}); err != nil || n != 1 {
		t.Fatalf("record: %d, %v", n, err)
	}
}

func TestVATReturnAndEUSalesListFromRecordedTransactions(t *testing.T) {
	s, _, ctx := setup(t)
	seedConfig(t, s, ctx)
	boxes := []domain.Box{
		{Box: "81", Label: "Sales at 19%", Kind: "base", Sort: 1, Codes: []domain.BoxCode{{Code: "VAT19", Direction: "output"}}},
		{Box: "81T", Label: "Output tax", Kind: "tax", Sort: 2, Codes: []domain.BoxCode{{Code: "VAT19", Direction: "output"}}},
		{Box: "66", Label: "Input tax", Kind: "tax", Sort: 3, Codes: []domain.BoxCode{{Code: "VAT19", Direction: "input"}}},
		{Box: "83", Label: "Payable", Kind: "payable", Sort: 4},
	}
	if err := s.PutBoxes(ctx, boxes); err != nil {
		t.Fatal(err)
	}
	if err := s.PutBoxes(ctx, []domain.Box{{Box: "1", Label: "x", Kind: "tax", Codes: []domain.BoxCode{{Code: "NOPE", Direction: "output"}}}}); asDomainError(err) == nil {
		t.Errorf("a box over a missing tax code was accepted: %v", err)
	}
	got, err := s.ListBoxes(ctx)
	if err != nil || len(got) != 4 || len(got[0].Codes) != 1 {
		t.Fatalf("boxes = %+v, %v", got, err)
	}
	if err := s.DeleteCode(ctx, "VAT19"); asDomainError(err) == nil {
		t.Errorf("a code used by a box was deleted: %v", err)
	}

	d := func(s string) *domain.Dec { v := domain.D(s); return &v }
	recordTxn(t, s, ctx, "VAT19", "output", "2026-01-10", "190", d("1000"), "DE", "", "")
	recordTxn(t, s, ctx, "VAT19", "output", "2026-01-20", "0", d("500"), "FR", "FR111", "goods")
	recordTxn(t, s, ctx, "VAT19", "input", "2026-01-15", "38", d("200"), "", "", "")
	recordTxn(t, s, ctx, "VAT19", "output", "2026-02-02", "19", d("100"), "", "", "")

	period := domain.Period{From: domain.Day("2026-01-01"), To: domain.Day("2026-01-31")}
	ret, err := s.VATReturn(ctx, period)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"81": "1500", "81T": "190", "66": "38", "83": "152"}
	for _, b := range ret.Boxes {
		if b.Amount.String() != want[b.Box] {
			t.Errorf("box %s = %s, want %s", b.Box, b.Amount, want[b.Box])
		}
	}
	if ret.Transactions != 3 || ret.MissingBase != 0 {
		t.Errorf("return = %+v", ret)
	}

	if _, err := s.ESL(ctx, period); !errors.Is(err, store.ErrNoEntitySettings) {
		t.Fatalf("an ESL without entity settings: %v", err)
	}
	if err := s.PutEntitySetting(ctx, store.EntitySetting{HomeCountry: "DE", VATID: "DE123456789"}); err != nil {
		t.Fatal(err)
	}
	esl, err := s.ESL(ctx, period)
	if err != nil || len(esl.Rows) != 1 || esl.Rows[0].Country != "FR" || esl.Rows[0].Base.String() != "500" || esl.Total != "500.00" {
		t.Fatalf("esl = %+v, %v", esl, err)
	}

	// A replay of the same transaction with different VAT return details is refused.
	txn := domain.Txn{ID: ids.New(), Code: "VAT19", Direction: "output", TaxDate: domain.Day("2026-01-05"), Amount: domain.D("1"), Base: d("5")}
	if _, err := s.RecordTransactions(ctx, []domain.Txn{txn}); err != nil {
		t.Fatal(err)
	}
	if n, err := s.RecordTransactions(ctx, []domain.Txn{txn}); err != nil || n != 0 {
		t.Errorf("identical replay: %d, %v", n, err)
	}
	txn.Base = d("6")
	if _, err := s.RecordTransactions(ctx, []domain.Txn{txn}); asDomainError(err) == nil {
		t.Errorf("a replay with another base amount was accepted: %v", err)
	}
}

func seedMaster(t *testing.T, s *store.Store, ctx context.Context) {
	t.Helper()
	for _, e := range []domain.MasterEntry{
		{Kind: "commodity", Code: "84137030", Description: "Centrifugal pumps", Attrs: map[string]string{"supplementary_unit": "p/st"}},
		{Kind: "commodity", Code: "73181590", Description: "Screws"},
		{Kind: "transaction_code", Code: "11", Description: "Outright purchase or sale"},
		{Kind: "transport_mode", Code: "3", Description: "Road"},
		{Kind: "delivery_terms", Code: "EXW", Description: "Ex works"},
		{Kind: "country", Code: "DE", Description: "Germany", Attrs: map[string]string{"eu_member": "true"}},
		{Kind: "country", Code: "GR", Description: "Greece", Attrs: map[string]string{"eu_member": "true", "intrastat_code": "EL"}},
	} {
		if err := s.PutMaster(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
}

func srcLine(ref, commodity, mass, qty, value string) domain.IntrastatLine {
	return domain.IntrastatLine{
		SourceRef: ref, PartnerCountry: "GR", CommodityCode: commodity, TransactionCode: "11", TransportMode: "3", DeliveryTerms: "EXW", OriginCountry: "DE",
		NetMass: domain.D(mass), SupplementaryQty: domain.D(qty), InvoiceValue: domain.D(value), StatisticalValue: domain.D(value),
	}
}

func TestIntrastatMasterData(t *testing.T) {
	s, _, ctx := setup(t)
	seedMaster(t, s, ctx)
	commodities, err := s.ListMaster(ctx, "commodity")
	if err != nil || len(commodities) != 2 || commodities[1].Attrs["supplementary_unit"] != "p/st" {
		t.Fatalf("commodities = %+v, %v", commodities, err)
	}
	all, _ := s.ListMaster(ctx, "")
	if len(all) != 7 {
		t.Errorf("all master data = %d entries", len(all))
	}
	if err := s.PutMaster(ctx, domain.MasterEntry{Kind: "commodity", Code: "84137030", Description: "Pumps, centrifugal"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ListMaster(ctx, "commodity"); got[1].Description != "Pumps, centrifugal" || len(got[1].Attrs) != 0 {
		t.Errorf("update did not replace: %+v", got[1])
	}
	if err := s.DeleteMaster(ctx, "commodity", "73181590"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteMaster(ctx, "commodity", "73181590"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("deleting twice: %v", err)
	}
}

func TestIntrastatJournalLifecycle(t *testing.T) {
	s, _, ctx := setup(t)
	seedMaster(t, s, ctx)
	period := domain.Day("2026-10-01")

	j, err := s.CreateJournal(ctx, "dispatch", period)
	if err != nil || j.Status != "draft" {
		t.Fatalf("create: %+v, %v", j, err)
	}
	if _, err := s.Validate(ctx, j.ID); !errors.Is(err, store.ErrNoEntitySettings) {
		t.Fatalf("validating before the entity country is set: %v", err)
	}
	if err := s.PutEntitySetting(ctx, store.EntitySetting{HomeCountry: "DE", VATID: "DE123456789"}); err != nil {
		t.Fatal(err)
	}

	// Transfer: lines arrive once, a repeat is skipped.
	lines := []domain.IntrastatLine{srcLine("S1", "84137030", "10", "2", "1000"), srcLine("S2", "84137030", "5.5", "1", "500"), srcLine("S3", "73181590", "1", "0", "20")}
	res, err := s.Transfer(ctx, j.ID, lines)
	if err != nil || res.Added != 3 || res.Skipped != 0 {
		t.Fatalf("transfer: %+v, %v", res, err)
	}
	if res, _ = s.Transfer(ctx, j.ID, lines); res.Added != 0 || res.Skipped != 3 {
		t.Errorf("repeat transfer: %+v", res)
	}

	// Compress, then a repeat transfer still skips what was merged away.
	got, err := s.Compress(ctx, j.ID)
	if err != nil || len(got.Lines) != 2 || got.Lines[0].CompressedFrom != 2 || got.Lines[0].NetMass.String() != "15.5" {
		t.Fatalf("compress: %+v, %v", got.Lines, err)
	}
	if res, _ = s.Transfer(ctx, j.ID, lines); res.Added != 0 || res.Skipped != 3 {
		t.Errorf("transfer after compression: %+v", res)
	}

	// An incomplete line fails validation with a plain message and blocks the export.
	if err := s.PutLine(ctx, j.ID, 2, domain.IntrastatLine{PartnerCountry: "GR", CommodityCode: "99999999", TransactionCode: "11", TransportMode: "3", DeliveryTerms: "EXW",
		NetMass: domain.D("1"), InvoiceValue: domain.D("20"), StatisticalValue: domain.D("20")}); err != nil {
		t.Fatal(err)
	}
	v, err := s.Validate(ctx, j.ID)
	if err != nil || v.Status != "draft" || v.ErrorCount != 1 || len(v.Lines[1].Errors) == 0 || !strings.Contains(v.Lines[1].Errors[0], "99999999") {
		t.Fatalf("validate: %+v, %v", v, err)
	}
	if _, err := s.Export(ctx, j.ID); asDomainError(err) == nil {
		t.Errorf("exporting an invalid journal: %v", err)
	}

	// Fix it by removing the bad line, validate, export.
	if err := s.DeleteLine(ctx, j.ID, 2); err != nil {
		t.Fatal(err)
	}
	if v, err = s.Validate(ctx, j.ID); err != nil || v.Status != "validated" || len(v.Lines) != 1 {
		t.Fatalf("validate again: %+v, %v", v, err)
	}
	if _, err := s.Correct(ctx, j.ID); asDomainError(err) == nil {
		t.Errorf("a correction of an unexported journal: %v", err)
	}
	ex, err := s.Export(ctx, j.ID)
	if err != nil || ex.Journal.Status != "exported" || ex.Journal.ExportedAt == nil || ex.HomeCountry != "DE" {
		t.Fatalf("export: %+v, %v", ex, err)
	}
	if _, err := s.Transfer(ctx, j.ID, []domain.IntrastatLine{srcLine("S9", "73181590", "1", "0", "5")}); asDomainError(err) == nil {
		t.Errorf("an exported journal accepted a transfer: %v", err)
	}
	if err := s.PutLine(ctx, j.ID, 1, lines[0]); asDomainError(err) == nil {
		t.Errorf("an exported journal accepted a line edit: %v", err)
	}
	if again, err := s.Export(ctx, j.ID); err != nil || again.Journal.Status != "exported" || !again.Journal.ExportedAt.Equal(*ex.Journal.ExportedAt) {
		t.Errorf("a second download changed the export: %+v, %v", again.Journal, err)
	}

	// Correction: a new draft copying the lines, one open correction at a time.
	c, err := s.Correct(ctx, j.ID)
	if err != nil || c.CorrectionOf != j.ID || c.Status != "draft" || c.LineCount != 1 {
		t.Fatalf("correct: %+v, %v", c, err)
	}
	if _, err := s.Correct(ctx, j.ID); asDomainError(err) == nil {
		t.Errorf("a second open correction was accepted: %v", err)
	}
	fixed := srcLine("", "73181590", "2", "0", "40")
	if err := s.PutLine(ctx, c.ID, 1, fixed); err != nil {
		t.Fatal(err)
	}
	if v, err = s.Validate(ctx, c.ID); err != nil || v.Status != "validated" {
		t.Fatalf("validate correction: %+v, %v", v, err)
	}
	if ex, err = s.Export(ctx, c.ID); err != nil || ex.Journal.CorrectionOf != j.ID {
		t.Fatalf("export correction: %+v, %v", ex.Journal, err)
	}

	list, err := s.ListJournals(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %+v, %v", list, err)
	}
	if _, err := s.GetJournal(ctx, ids.New()); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("an unknown journal: %v", err)
	}
	if _, err := s.GetJournal(ctx, "not-an-id"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a malformed journal id: %v", err)
	}
}

func TestIntrastatJournalsAreScopedToTheLegalEntity(t *testing.T) {
	s, _, ctx := setup(t)
	j, err := s.CreateJournal(ctx, "arrival", domain.Day("2026-10-01"))
	if err != nil {
		t.Fatal(err)
	}
	p, _ := tenant.FromContext(ctx)
	p.LegalEntityID = ids.New()
	elsewhere := tenant.With(ctx, p)
	if _, err := s.GetJournal(elsewhere, j.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("another legal entity read the journal: %v", err)
	}
	if list, err := s.ListJournals(elsewhere); err != nil || len(list) != 0 {
		t.Errorf("another legal entity lists %d journals: %v", len(list), err)
	}
}

func TestStoredCertificatesExemptACalculationAndProtectTheirCodes(t *testing.T) {
	s, _, ctx := setup(t)
	seedConfig(t, s, ctx)
	cert := domain.Certificate{
		Exemption: domain.Exemption{Number: "CERT/2026-1", Codes: []string{"VAT19"}, ValidFrom: domain.Day("2026-01-01"), ValidTo: domain.Day("2026-12-31")},
		PartyRef:  "customer-7", Note: "Resale certificate",
	}
	if err := s.PutCertificate(ctx, cert); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListCertificates(ctx)
	if err != nil || len(got) != 1 || got[0].Number != "CERT/2026-1" || got[0].PartyRef != "customer-7" || got[0].ValidTo.IsZero() {
		t.Fatalf("certificates = %+v, %v", got, err)
	}
	cfg, err := s.LoadConfig(ctx)
	if err != nil || len(cfg.Exemptions) != 1 {
		t.Fatalf("stored config carries %d exemptions (%v)", len(cfg.Exemptions), err)
	}
	doc := domain.Document{
		Currency: "EUR", Direction: domain.DirOutput, DocumentDate: domain.Day("2026-03-15"),
		Party: domain.Party{TaxGroup: "DOMESTIC-DE", Certificate: "CERT/2026-1"},
		Lines: []domain.Line{{LineRef: "L1", Amount: domain.D("200"), ItemTaxGroup: "STANDARD"}},
	}
	res, err := domain.Calculate(doc, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, total := range res.DocumentTaxesByCode {
		if total.Code == "VAT19" && total.TaxAmount.String() != "0" {
			t.Errorf("the certificate did not exempt VAT19: %+v", total)
		}
	}
	// An open-ended certificate round trips with no end date.
	open := cert
	open.Number, open.ValidTo = "CERT-OPEN", domain.Date{}
	if err := s.PutCertificate(ctx, open); err != nil {
		t.Fatal(err)
	}

	if err := s.PutCertificate(ctx, domain.Certificate{Exemption: domain.Exemption{Number: "BAD", Codes: []string{"GONE"}, ValidFrom: domain.Day("2026-01-01")}}); asDomainError(err) == nil {
		t.Errorf("a certificate for a missing code was accepted: %v", err)
	}
	if err := s.PutCertificate(ctx, domain.Certificate{Exemption: domain.Exemption{Number: "BAD", Codes: []string{"VAT19"}, ValidFrom: domain.Day("2026-05-01"), ValidTo: domain.Day("2026-04-01")}}); asDomainError(err) == nil {
		t.Errorf("a certificate that ends before it starts was accepted: %v", err)
	}
	if err := s.DeleteGroup(ctx, "item", "STANDARD"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteGroup(ctx, "party", "DOMESTIC-DE"); err != nil {
		t.Fatal(err)
	}
	if de := asDomainError(s.DeleteCode(ctx, "VAT19")); de == nil || de.Code != domain.CodeConfigInUse {
		t.Errorf("deleting a code a certificate names: %v, want TAX-0013", de)
	}
	if err := s.DeleteCertificate(ctx, "CERT/2026-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteCertificate(ctx, "CERT/2026-1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("deleting twice: %v", err)
	}
}

func TestSeededMasterDataKeepsTenantEditsAndDrivesAJournal(t *testing.T) {
	s, _, ctx := setup(t)
	if err := s.PutMaster(ctx, domain.MasterEntry{Kind: "transport_mode", Code: "3", Description: "Lorry"}); err != nil {
		t.Fatal(err)
	}
	seed, err := domain.SeedEntries([]string{"transport_mode", "country", "delivery_terms"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.ImportMaster(ctx, seed, false)
	if err != nil || res.Skipped != 1 || res.Added != len(seed)-1 {
		t.Fatalf("seed = %+v, %v; want one entry skipped (the edited one)", res, err)
	}
	modes, _ := s.ListMaster(ctx, "transport_mode")
	for _, m := range modes {
		if m.Code == "3" && m.Description != "Lorry" {
			t.Errorf("the seed overwrote the tenant's edit: %+v", m)
		}
	}
	if again, err := s.ImportMaster(ctx, seed, false); err != nil || again.Added != 0 || again.Skipped != len(seed) {
		t.Errorf("a repeated seed = %+v, %v", again, err)
	}
	if over, err := s.ImportMaster(ctx, seed, true); err != nil || over.Added != len(seed) {
		t.Errorf("an overwriting import = %+v, %v", over, err)
	}
	modes, _ = s.ListMaster(ctx, "transport_mode")
	for _, m := range modes {
		if m.Code == "3" && m.Description != "Road transport" {
			t.Errorf("overwrite kept the old text: %+v", m)
		}
	}
}

func TestRecoveryPoliciesDriveCalculationsAndTheReclaim(t *testing.T) {
	s, _, ctx := setup(t)
	seedConfig(t, s, ctx)
	if err := s.PutGroup(ctx, domain.Group{Kind: "item", Code: "CARS", Name: "Company cars", TaxCodes: []string{"VAT19"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRecoveryPolicy(ctx, domain.RecoveryPolicy{Code: "VAT19", ItemGroup: "CARS", RecoverablePct: domain.D("50"), ValidFrom: domain.Day("2026-01-01"), Note: "Private use"}); err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]domain.RecoveryPolicy{
		"an overlapping policy": {Code: "VAT19", ItemGroup: "CARS", RecoverablePct: domain.D("40"), ValidFrom: domain.Day("2026-06-01")},
		"an unknown code":       {Code: "NOPE", RecoverablePct: domain.D("40"), ValidFrom: domain.Day("2026-06-01")},
		"an unknown item group": {Code: "VAT19", ItemGroup: "BOATS", RecoverablePct: domain.D("40"), ValidFrom: domain.Day("2026-06-01")},
	} {
		if err := s.PutRecoveryPolicy(ctx, p); asDomainError(err) == nil {
			t.Errorf("%s was accepted: %v", name, err)
		}
	}
	// A later policy of the same group is fine once the first one ends.
	if err := s.PutRecoveryPolicy(ctx, domain.RecoveryPolicy{Code: "VAT19", ItemGroup: "CARS", RecoverablePct: domain.D("50"), ValidFrom: domain.Day("2026-01-01"), ValidTo: domain.Day("2026-12-31")}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRecoveryPolicy(ctx, domain.RecoveryPolicy{Code: "VAT19", ItemGroup: "CARS", RecoverablePct: domain.D("60"), ValidFrom: domain.Day("2027-01-01")}); err != nil {
		t.Fatal(err)
	}
	cfg, err := s.LoadConfig(ctx)
	if err != nil || len(cfg.Recovery) != 2 {
		t.Fatalf("stored config carries %d policies (%v)", len(cfg.Recovery), err)
	}
	res, err := domain.Calculate(domain.Document{
		Currency: "EUR", Direction: domain.DirInput, DocumentDate: domain.Day("2026-03-15"), Party: domain.Party{TaxGroup: "DOMESTIC-DE"},
		Lines: []domain.Line{{LineRef: "L1", Amount: domain.D("100"), ItemTaxGroup: "CARS"}},
	}, cfg)
	if err != nil || res.Lines[0].Taxes[0].Recoverable.String() != "9.5" || res.Lines[0].Taxes[0].Nonrecoverable.String() != "9.5" {
		t.Fatalf("calculate with the stored policy: %+v, %v", res, err)
	}

	// Reclaim: one transaction records its recoverable amount, one falls back on the code, an output tax is ignored.
	rec := domain.D("30")
	txns := []domain.Txn{
		{ID: ids.New(), Code: "VAT19", Direction: domain.DirInput, TaxDate: domain.Day("2026-04-10"), Amount: domain.D("100"), Recoverable: &rec},
		{ID: ids.New(), Code: "VAT19", Direction: domain.DirInput, TaxDate: domain.Day("2026-04-12"), Amount: domain.D("50")},
		{ID: ids.New(), Code: "VAT19", Direction: domain.DirOutput, TaxDate: domain.Day("2026-04-13"), Amount: domain.D("400")},
	}
	if n, err := s.RecordTransactions(ctx, txns); err != nil || n != 3 {
		t.Fatalf("record: %d %v", n, err)
	}
	if n, err := s.RecordTransactions(ctx, txns); err != nil || n != 0 {
		t.Fatalf("a repeat with the recoverable amount: %d %v", n, err)
	}
	changed := txns[0]
	other := domain.D("31")
	changed.Recoverable = &other
	if _, err := s.RecordTransactions(ctx, []domain.Txn{changed}); asDomainError(err) == nil {
		t.Errorf("a changed recoverable amount was accepted: %v", err)
	}
	got, err := s.Reclaim(ctx, domain.Period{From: domain.Day("2026-04-01"), To: domain.Day("2026-04-30")})
	if err != nil || len(got.Rows) != 1 || got.Rows[0].Transactions != 2 || got.Rows[0].Estimated != 1 ||
		got.TotalInputTax.String() != "150" || got.TotalRecoverable.String() != "80" || got.TotalNonRecoverable.String() != "70" {
		t.Fatalf("reclaim = %+v, %v", got, err)
	}

	if de := asDomainError(s.DeleteCode(ctx, "VAT19")); de == nil {
		t.Error("deleting a code with recovery policies succeeded")
	}
	if err := s.DeleteRecoveryPolicy(ctx, "VAT19", "CARS", domain.Day("2027-01-01")); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteRecoveryPolicy(ctx, "VAT19", "CARS", domain.Day("2027-01-01")); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("deleting twice: %v", err)
	}
}
