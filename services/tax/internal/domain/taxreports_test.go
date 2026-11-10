package domain_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/tax/internal/domain"
)

func jur(code, parent string) domain.Jurisdiction {
	return domain.Jurisdiction{Code: code, Name: code, Kind: "state", Country: "US", ParentCode: parent}
}

func TestValidateJurisdictionRefusesLoopsAndMissingParents(t *testing.T) {
	tree := map[string]domain.Jurisdiction{"US": jur("US", ""), "US-OH": jur("US-OH", "US"), "US-OH-FRK": jur("US-OH-FRK", "US-OH")}
	cases := []struct {
		name string
		j    domain.Jurisdiction
		ok   bool
	}{
		{"root", jur("EU", ""), true},
		{"child of an existing node", jur("US-TX", "US"), true},
		{"own parent", jur("US", "US"), false},
		{"missing parent", jur("US-TX", "NOPE"), false},
		{"moving a node below its own descendant", jur("US", "US-OH-FRK"), false},
		{"bad kind", domain.Jurisdiction{Code: "X", Name: "X", Kind: "planet", Country: "US"}, false},
		{"lowercase country", domain.Jurisdiction{Code: "X", Name: "X", Kind: "city", Country: "us"}, false},
		{"empty name", domain.Jurisdiction{Code: "X", Kind: "city", Country: "US"}, false},
	}
	for _, c := range cases {
		if err := domain.ValidateJurisdiction(c.j, tree); (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok = %v", c.name, err, c.ok)
		}
	}
	path := domain.JurisdictionPath("US-OH-FRK", tree)
	if strings.Join(path, "/") != "US/US-OH/US-OH-FRK" {
		t.Errorf("path = %v", path)
	}
}

func rate(from, to, pct string) domain.Rate {
	r := domain.Rate{ValidFrom: domain.Day(from), RatePct: domain.D(pct)}
	if to != "" {
		r.ValidTo = domain.Day(to)
	}
	return r
}

func TestValidateConfigCodeChecksRatesAndOverlaps(t *testing.T) {
	good := domain.ConfigCode{Name: "VAT 19", Code: domain.Code{Code: "VAT19", Jurisdiction: "DE", Rates: []domain.Rate{rate("2020-01-01", "2020-12-31", "16"), rate("2021-01-01", "", "19")}}}
	if err := domain.ValidateConfigCode(good); err != nil {
		t.Fatalf("a clean rate history was refused: %v", err)
	}
	for name, mutate := range map[string]func(c *domain.ConfigCode){
		"overlapping rates":    func(c *domain.ConfigCode) { c.Rates[0].ValidTo = domain.Day("2021-06-30") },
		"rate above 100":       func(c *domain.ConfigCode) { c.Rates[1].RatePct = domain.D("101") },
		"end before start":     func(c *domain.ConfigCode) { c.Rates[0].ValidTo = domain.Day("2019-01-01") },
		"unknown mechanism":    func(c *domain.ConfigCode) { c.Mechanism = "magic" },
		"recoverable over 100": func(c *domain.ConfigCode) { d := domain.D("120"); c.RecoverablePct = &d },
		"no jurisdiction":      func(c *domain.ConfigCode) { c.Jurisdiction = "" },
	} {
		c := good
		c.Rates = append([]domain.Rate{}, good.Rates...)
		mutate(&c)
		if err := domain.ValidateConfigCode(c); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	// Two brackets of the same period do not overlap when their amount ranges differ.
	from, mid := domain.D("0"), domain.D("1000")
	bracketed := good
	a, b := rate("2021-01-01", "", "5"), rate("2021-01-01", "", "8")
	a.FromAmount, a.ToAmount, b.FromAmount = &from, &mid, &mid
	bracketed.Rates = []domain.Rate{a, b}
	if err := domain.ValidateConfigCode(bracketed); err != nil {
		t.Errorf("amount brackets were refused as overlapping: %v", err)
	}
}

func TestStoredConfigDrivesTheEngine(t *testing.T) {
	codes := []domain.ConfigCode{{Name: "VAT", Code: domain.Code{Code: "VAT", Jurisdiction: "DE", Rates: []domain.Rate{rate("2020-01-01", "", "19")}}}}
	groups := []domain.Group{
		{Kind: domain.KindParty, Code: "G", Name: "Domestic", TaxCodes: []string{"VAT"}},
		{Kind: domain.KindItem, Code: "I", Name: "Standard", TaxCodes: []string{"VAT"}},
	}
	known := map[string]bool{"VAT": true}
	for _, g := range groups {
		if err := domain.ValidateGroup(g, known); err != nil {
			t.Fatal(err)
		}
	}
	if err := domain.ValidateGroup(domain.Group{Kind: "item", Code: "X", Name: "X", TaxCodes: []string{"GONE"}}, known); err == nil {
		t.Error("a group naming a missing tax code was accepted")
	}
	cfg := domain.AssembleConfig(codes, groups, "")
	res, err := domain.Calculate(domain.Document{
		Currency: "EUR", Direction: domain.DirOutput, DocumentDate: domain.Day("2026-03-15"), Party: domain.Party{TaxGroup: "G"},
		Lines: []domain.Line{{LineRef: "L1", Amount: domain.D("100"), ItemTaxGroup: "I"}},
	}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := res.DocumentTaxesByCode[0].TaxAmount.String(); got != "19" {
		t.Errorf("tax = %s, want 19", got)
	}
}

func dec(s string) *decimal.Decimal { d := decimal.RequireFromString(s); return &d }

func vtxn(code, dir, date, tax string, base *decimal.Decimal) domain.VATTxn {
	return domain.VATTxn{Code: code, Direction: dir, TaxDate: domain.Day(date).Time, Amount: decimal.RequireFromString(tax), Base: base}
}

func TestComputeVATReturn(t *testing.T) {
	boxes := []domain.Box{
		{Box: "1", Label: "Sales at 19%", Kind: domain.BoxBase, Sort: 1, Codes: []domain.BoxCode{{Code: "VAT19", Direction: "output"}}},
		{Box: "2", Label: "Output tax", Kind: domain.BoxTax, Sort: 2, Codes: []domain.BoxCode{{Code: "VAT19", Direction: "output"}}},
		{Box: "3", Label: "Input tax", Kind: domain.BoxTax, Sort: 3, Codes: []domain.BoxCode{{Code: "VAT19", Direction: "input"}}},
		{Box: "9", Label: "Payable", Kind: domain.BoxPayable, Sort: 9},
	}
	if err := domain.ValidateBoxes(boxes); err != nil {
		t.Fatal(err)
	}
	period := domain.Period{From: domain.Day("2026-01-01"), To: domain.Day("2026-01-31")}
	ret := domain.ComputeVATReturn(period, boxes, []domain.VATTxn{
		vtxn("VAT19", "output", "2026-01-10", "190.00", dec("1000.00")),
		vtxn("VAT19", "output", "2026-01-20", "95.00", dec("500.00")),
		vtxn("VAT19", "input", "2026-01-15", "38.00", dec("200.00")),
		vtxn("VAT19", "output", "2026-02-01", "999", dec("999")), // outside the period
		vtxn("OTHER", "output", "2026-01-05", "7.00", nil),       // no box
		vtxn("VAT19", "output", "2026-01-06", "19.00", nil),      // feeds box 1 without a base
	})
	want := map[string]string{"1": "1500", "2": "304", "3": "38", "9": "273"}
	for _, b := range ret.Boxes {
		if b.Amount.String() != want[b.Box] {
			t.Errorf("box %s = %s, want %s", b.Box, b.Amount, want[b.Box])
		}
	}
	if ret.Transactions != 5 || ret.Unmapped != 1 || ret.MissingBase != 1 {
		t.Errorf("counts: transactions %d unmapped %d missing base %d", ret.Transactions, ret.Unmapped, ret.MissingBase)
	}
}

func TestValidateBoxesRefusesDoubleCountsAndEmptyBoxes(t *testing.T) {
	same := []domain.BoxCode{{Code: "VAT19", Direction: "output"}}
	for name, boxes := range map[string][]domain.Box{
		"same code in two tax boxes": {{Box: "1", Label: "a", Kind: "tax", Codes: same}, {Box: "2", Label: "b", Kind: "tax", Codes: same}},
		"duplicate box":              {{Box: "1", Label: "a", Kind: "tax", Codes: same}, {Box: "1", Label: "b", Kind: "base", Codes: same}},
		"tax box without codes":      {{Box: "1", Label: "a", Kind: "tax"}},
		"payable with codes":         {{Box: "1", Label: "a", Kind: "payable", Codes: same}},
		"bad direction":              {{Box: "1", Label: "a", Kind: "tax", Codes: []domain.BoxCode{{Code: "X", Direction: "sideways"}}}},
	} {
		if err := domain.ValidateBoxes(boxes); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	// A base box and a tax box may both read the same code: that is not a double count.
	both := []domain.Box{{Box: "1", Label: "a", Kind: "base", Codes: same}, {Box: "2", Label: "b", Kind: "tax", Codes: same}}
	if err := domain.ValidateBoxes(both); err != nil {
		t.Errorf("base and tax over one code was refused: %v", err)
	}
}

func TestComputeESLGroupsEUSalesOnly(t *testing.T) {
	period := domain.Period{From: domain.Day("2026-01-01"), To: domain.Day("2026-03-31")}
	mk := func(country, vat, kind, date, base string) domain.VATTxn {
		return domain.VATTxn{Code: "RC", Direction: "output", TaxDate: domain.Day(date).Time, Amount: decimal.Zero, Base: dec(base), CounterpartyCtry: country, CounterpartyVAT: vat, SupplyKind: kind}
	}
	esl := domain.ComputeESL(period, "DE", "DE123456789", []domain.VATTxn{
		mk("FR", "FR111", "goods", "2026-01-10", "1000"),
		mk("FR", "FR111", "goods", "2026-02-10", "500"),
		mk("FR", "FR111", "services", "2026-02-11", "300"),
		mk("DE", "DE999", "goods", "2026-01-12", "700"), // home country
		mk("US", "US1", "goods", "2026-01-12", "700"),   // not EU
		mk("NL", "", "goods", "2026-01-12", "100"),      // no VAT number
		mk("IT", "IT5", "triangular", "2026-03-01", "250"),
		mk("ES", "ES1", "goods", "2026-04-01", "999"), // outside the period
		{Code: "VAT", Direction: "input", TaxDate: domain.Day("2026-01-10").Time, CounterpartyCtry: "FR", CounterpartyVAT: "FR111", Base: dec("50")},
	})
	if len(esl.Rows) != 3 {
		t.Fatalf("rows = %+v", esl.Rows)
	}
	first := esl.Rows[0]
	if first.Country != "FR" || first.Indicator != "L" || first.Base.String() != "1500" || first.Transactions != 2 {
		t.Errorf("first row = %+v", first)
	}
	if esl.Rows[1].Indicator != "S" || esl.Rows[2].Indicator != "T" {
		t.Errorf("indicators = %s %s", esl.Rows[1].Indicator, esl.Rows[2].Indicator)
	}
	if esl.Total != "2050.00" || esl.MissingVATID != 1 {
		t.Errorf("total %s, missing vat ids %d", esl.Total, esl.MissingVATID)
	}
}

func testMaster() domain.Master {
	return domain.NewMaster([]domain.MasterEntry{
		{Kind: "commodity", Code: "84137030", Description: "Centrifugal pumps", Attrs: map[string]string{"supplementary_unit": "p/st"}},
		{Kind: "commodity", Code: "73181590", Description: "Screws"},
		{Kind: "transaction_code", Code: "11", Description: "Outright purchase or sale"},
		{Kind: "transport_mode", Code: "3", Description: "Road"},
		{Kind: "delivery_terms", Code: "EXW", Description: "Ex works"},
		{Kind: "port", Code: "DEHAM", Description: "Hamburg"},
		{Kind: "country", Code: "DE", Description: "Germany", Attrs: map[string]string{"eu_member": "true"}},
		{Kind: "country", Code: "GR", Description: "Greece", Attrs: map[string]string{"eu_member": "true", "intrastat_code": "EL"}},
		{Kind: "country", Code: "US", Description: "United States", Attrs: map[string]string{"eu_member": "false"}},
	})
}

func iline(partner, commodity string, mass, qty, value string) domain.IntrastatLine {
	return domain.IntrastatLine{
		PartnerCountry: partner, CommodityCode: commodity, TransactionCode: "11", TransportMode: "3", DeliveryTerms: "EXW", OriginCountry: "DE",
		NetMass: domain.D(mass), SupplementaryQty: domain.D(qty), InvoiceValue: domain.D(value), StatisticalValue: domain.D(value),
	}
}

func TestValidateIntrastatLine(t *testing.T) {
	m := testMaster()
	if errs := domain.ValidateIntrastatLine("dispatch", "DE", iline("GR", "84137030", "12.5", "3", "4000"), m); len(errs) != 0 {
		t.Errorf("a complete line has problems: %v", errs)
	}
	cases := map[string]domain.IntrastatLine{
		"not an EU partner":         iline("US", "84137030", "1", "1", "10"),
		"unknown partner":           iline("FR", "84137030", "1", "1", "10"),
		"own country":               iline("DE", "84137030", "1", "1", "10"),
		"unknown commodity":         iline("GR", "99999999", "1", "1", "10"),
		"missing supplementary qty": iline("GR", "84137030", "1", "0", "10"),
		"no mass":                   iline("GR", "73181590", "0", "0", "10"),
		"no value":                  iline("GR", "73181590", "1", "0", "0"),
	}
	for name, l := range cases {
		if errs := domain.ValidateIntrastatLine("dispatch", "DE", l, m); len(errs) == 0 {
			t.Errorf("%s passed validation", name)
		}
	}
	noOrigin := iline("GR", "73181590", "1", "0", "10")
	noOrigin.OriginCountry = ""
	if errs := domain.ValidateIntrastatLine("arrival", "DE", noOrigin, m); len(errs) == 0 {
		t.Error("an arrival without a country of origin passed")
	}
	if errs := domain.ValidateIntrastatLine("dispatch", "DE", noOrigin, m); len(errs) != 0 {
		t.Errorf("a dispatch needs no country of origin: %v", errs)
	}
	bad := iline("GR", "73181590", "1", "0", "10")
	bad.TransportMode, bad.Port = "9", "XXXXX"
	if errs := domain.ValidateIntrastatLine("dispatch", "DE", bad, m); len(errs) != 2 {
		t.Errorf("expected a transport mode and a port problem, got %v", errs)
	}
}

func TestCompressIntrastatLinesMergesOnClassification(t *testing.T) {
	a, b, c := iline("GR", "84137030", "10", "2", "1000"), iline("GR", "84137030", "5.5", "1", "500"), iline("GR", "73181590", "1", "0", "20")
	a.SourceRef, b.SourceRef, c.SourceRef = "S1", "S2", "S3"
	d := iline("GR", "84137030", "1", "1", "10")
	d.TransportMode = "1" // a different mode keeps the line apart
	out := domain.CompressIntrastatLines([]domain.IntrastatLine{a, c, b, d})
	if len(out) != 3 {
		t.Fatalf("lines = %d, want 3", len(out))
	}
	first := out[0]
	if first.NetMass.String() != "15.5" || first.SupplementaryQty.String() != "3" || first.InvoiceValue.String() != "1500" || first.CompressedFrom != 2 || first.SourceRef != "" {
		t.Errorf("merged line = %+v", first)
	}
	for i, l := range out {
		if l.LineNo != i+1 {
			t.Errorf("line %d is numbered %d", i, l.LineNo)
		}
	}
	if out[1].SourceRef != "S3" {
		t.Error("an unmerged line lost its source reference")
	}
}

func TestIntrastatCSVUsesIntrastatCountryCodesAndWholeValues(t *testing.T) {
	m := testMaster()
	l := iline("GR", "84137030", "12.3456", "3", "4000.6")
	l.LineNo = 1
	var buf bytes.Buffer
	if err := domain.IntrastatCSV(&buf, "dispatch", "DE", true, []domain.IntrastatLine{l}, m); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("csv = %q", buf.String())
	}
	want := "1,dispatch,A,DE,EL,84137030,DE,11,3,EXW,,12.346,3.000,4001,4001"
	if lines[1] != want {
		t.Errorf("row = %s\nwant  %s", lines[1], want)
	}
}

func TestValidateTransferLinesAndMasterEntries(t *testing.T) {
	ok := domain.IntrastatLine{SourceRef: "S1"}
	if err := domain.ValidateTransferLines([]domain.IntrastatLine{ok, {SourceRef: "S1"}}); err == nil {
		t.Error("a repeated source_ref was accepted")
	}
	if err := domain.ValidateTransferLines([]domain.IntrastatLine{{}}); err == nil {
		t.Error("a line without a source_ref was accepted")
	}
	neg := domain.IntrastatLine{SourceRef: "S2", NetMass: domain.D("-1")}
	if err := domain.ValidateTransferLines([]domain.IntrastatLine{neg}); err == nil {
		t.Error("a negative mass was accepted")
	}
	for name, e := range map[string]domain.MasterEntry{
		"short commodity":     {Kind: "commodity", Code: "8413", Description: "x"},
		"transport mode of 2": {Kind: "transport_mode", Code: "10", Description: "x"},
		"lowercase country":   {Kind: "country", Code: "de", Description: "x"},
		"unknown attribute":   {Kind: "port", Code: "DEHAM", Description: "x", Attrs: map[string]string{"weight": "1"}},
		"bad eu_member":       {Kind: "country", Code: "DE", Description: "x", Attrs: map[string]string{"eu_member": "maybe"}},
		"unknown kind":        {Kind: "color", Code: "1", Description: "x"},
		"empty description":   {Kind: "commodity", Code: "84137030"},
	} {
		if err := domain.ValidateMasterEntry(e); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}
