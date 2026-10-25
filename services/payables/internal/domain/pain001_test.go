package domain_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"

	"github.com/BenjaminSRussell/passage/services/payables/internal/domain"
)

var (
	idPattern       = regexp.MustCompile(`^[A-Za-z0-9/\-?:().,'+ ]{1,35}$`)
	creationPattern = regexp.MustCompile(`<CreDtTm>[^<]*</CreDtTm>`)
)

type tester interface {
	Helper()
	Fatal(args ...any)
	Fatalf(format string, args ...any)
}

type parsedAgent struct {
	BIC   string `xml:"FinInstnId>BICFI"`
	Clr   string `xml:"FinInstnId>ClrSysMmbId>ClrSysId>Cd"`
	MmbID string `xml:"FinInstnId>ClrSysMmbId>MmbId"`
}

type parsedTx struct {
	InstrID string `xml:"PmtId>InstrId"`
	E2EID   string `xml:"PmtId>EndToEndId"`
	Amt     struct {
		Ccy   string `xml:"Ccy,attr"`
		Value string `xml:",chardata"`
	} `xml:"Amt>InstdAmt"`
	Agent *parsedAgent `xml:"CdtrAgt"`
	Cdtr  struct {
		Name   string `xml:"Nm"`
		Ctry   string `xml:"PstlAdr>Ctry"`
		Street string `xml:"PstlAdr>StrtNm"`
		Town   string `xml:"PstlAdr>TwnNm"`
	} `xml:"Cdtr"`
	Purpose string `xml:"Purp>Cd"`
	Ustrd   string `xml:"RmtInf>Ustrd"`
	Strd    []struct {
		Nb  string `xml:"RfrdDocInf>Nb"`
		Amt string `xml:"RfrdDocAmt>RmtdAmt"`
	} `xml:"RmtInf>Strd"`
}

type parsedInf struct {
	ID      string     `xml:"PmtInfId"`
	Batch   string     `xml:"BtchBookg"`
	SvcLvl  string     `xml:"PmtTpInf>SvcLvl>Cd"`
	Date    string     `xml:"ReqdExctnDt>Dt"`
	ChrgBr  string     `xml:"ChrgBr"`
	DbtrAgt string     `xml:"DbtrAgt>FinInstnId>BICFI"`
	Txs     []parsedTx `xml:"CdtTrfTxInf"`
}

type parsedDoc struct {
	XMLName xml.Name    `xml:"urn:iso:std:iso:20022:tech:xsd:pain.001.001.09 Document"`
	MsgID   string      `xml:"CstmrCdtTrfInitn>GrpHdr>MsgId"`
	Created string      `xml:"CstmrCdtTrfInitn>GrpHdr>CreDtTm"`
	NbOfTxs int         `xml:"CstmrCdtTrfInitn>GrpHdr>NbOfTxs"`
	CtrlSum string      `xml:"CstmrCdtTrfInitn>GrpHdr>CtrlSum"`
	Name    string      `xml:"CstmrCdtTrfInitn>GrpHdr>InitgPty>Nm"`
	PartyID string      `xml:"CstmrCdtTrfInitn>GrpHdr>InitgPty>Id>OrgId>Othr>Id"`
	Infs    []parsedInf `xml:"CstmrCdtTrfInitn>PmtInf"`
}

func parseDoc(t tester, content []byte) parsedDoc {
	t.Helper()
	var doc parsedDoc
	if err := xml.Unmarshal(content, &doc); err != nil {
		t.Fatalf("generated XML does not parse: %v", err)
	}
	return doc
}

func profileFor(t tester, variant string, overrides map[string]string) domain.BankProfile {
	t.Helper()
	raw := map[string]json.RawMessage{}
	for k, v := range overrides {
		raw[k] = json.RawMessage(v)
	}
	profile, err := domain.ParseBankProfile(domain.BankProfileDTO{Variant: variant, Overrides: raw})
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

func methodOf(t tester, typ, format string) domain.PaymentMethod {
	t.Helper()
	m, err := domain.ParsePaymentMethod(domain.PaymentMethodDTO{Type: typ, Format: format})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

var created = time.Date(2026, 10, 25, 9, 30, 0, 0, time.UTC)

func payment(t tester, number, amount string, date time.Time) domain.Pain001Payment {
	t.Helper()
	return domain.Pain001Payment{
		Method: methodOf(t, domain.MethodTransfer, domain.FormatPain001), ExecutionDate: date,
		Number: number, Amount: decimal.RequireFromString(amount), Currency: "EUR",
		Creditor: domain.Pain001Creditor{
			Party:   domain.Pain001Party{Name: "Müller Straße GmbH", Address: domain.PostalAddress{StreetName: "Hauptstraße 1", PostCode: "10115", TownName: "Berlin", Country: "DE"}},
			Account: domain.Pain001Account{IBAN: "DE89370400440532013000"}, BIC: "COBADEFFXXX",
		},
		Invoices: []domain.Pain001Invoice{{Number: "INV-1", Amount: decimal.RequireFromString(amount)}},
	}
}

func baseInput(t tester) domain.Pain001Input {
	t.Helper()
	day := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	return domain.Pain001Input{
		FileNumber: "F1", Created: created,
		Debtor:        domain.Pain001Party{Name: "Passage Holding", Address: domain.PostalAddress{Country: "DE"}},
		DebtorAccount: domain.Pain001Account{IBAN: "GB29NWBK60161331926819"}, DebtorBIC: "NWBKGB2L",
		Profile:  profileFor(t, domain.VariantSEPA, nil),
		Payments: []domain.Pain001Payment{payment(t, "PAY-1", "100.50", day)},
	}
}

func generate(t tester, in domain.Pain001Input) (domain.PaymentFile, parsedDoc) {
	t.Helper()
	file, err := domain.GeneratePain001(in)
	if err != nil {
		t.Fatal(err)
	}
	return file, parseDoc(t, file.Content)
}

func requireCode(t tester, err error, code, detail string) {
	t.Helper()
	var e *domain.Error
	if !errors.As(err, &e) || e.Code != code || !strings.Contains(e.Detail, detail) {
		t.Fatalf("error = %v, want code %s detail containing %q", err, code, detail)
	}
}

func TestGeneratePain001_Transliteration(t *testing.T) {
	file, doc := generate(t, baseInput(t))
	if got := doc.Infs[0].Txs[0].Cdtr.Name; got != "Muller Strasse GmbH" {
		t.Fatalf("Cdtr/Nm = %q", got)
	}
	sum := sha256.Sum256(file.Content)
	if file.Format != domain.FormatPain001 || file.FileNumber != "F1" || file.PaymentCount != 1 || file.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("unexpected file metadata: %+v", file)
	}
	if doc.MsgID != "F1" || doc.NbOfTxs != 1 || doc.CtrlSum != "100.50" || doc.Created != "2026-10-25T09:30:00Z" || !file.ControlSum.Equal(decimal.RequireFromString("100.5")) {
		t.Fatalf("unexpected group header: %+v", doc)
	}
	tx := doc.Infs[0].Txs[0]
	if tx.InstrID != "PAY-1" || tx.E2EID != "PAY-1" || tx.Amt.Ccy != "EUR" || tx.Amt.Value != "100.50" || tx.Purpose != "SUPP" || tx.Ustrd != "INV-1" {
		t.Fatalf("unexpected transaction: %+v", tx)
	}
}

func TestGeneratePain001_RejectsUntransliterable(t *testing.T) {
	for _, name := range []string{"山田 Ltd", "   "} {
		in := baseInput(t)
		in.Payments[0].Creditor.Party.Name = name
		file, err := domain.GeneratePain001(in)
		requireCode(t, err, domain.CodeFileInvalid, "PAY-1")
		if file.Content != nil {
			t.Fatalf("content returned for %q", name)
		}
	}
}

func TestGeneratePain001_RejectsMethods(t *testing.T) {
	cases := []struct{ typ, format string }{
		{domain.MethodCard, ""},
		{domain.MethodCheck, domain.FormatCheckPDF},
		{domain.MethodACH, domain.FormatNacha},
	}
	for _, c := range cases {
		in := baseInput(t)
		in.Payments[0].Method = methodOf(t, c.typ, c.format)
		file, err := domain.GeneratePain001(in)
		requireCode(t, err, domain.CodeValidation, c.typ)
		if file.Content != nil {
			t.Fatalf("content returned for %s", c.typ)
		}
	}
}

func TestGeneratePain001_Profiles(t *testing.T) {
	day1 := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	day2 := day1.AddDate(0, 0, 1)
	usCreditor := domain.Pain001Creditor{
		Party:         domain.Pain001Party{Name: "Acme Supply", Address: domain.PostalAddress{Country: "US"}},
		Account:       domain.Pain001Account{OtherID: "123456789012"},
		RoutingNumber: "021000021",
	}
	cases := []struct {
		name   string
		mutate func(t *testing.T, in *domain.Pain001Input)
		check  func(t *testing.T, doc parsedDoc)
	}{
		{"sepa service level and charge bearer", nil, func(t *testing.T, d parsedDoc) {
			if inf := d.Infs[0]; inf.SvcLvl != "SEPA" || inf.ChrgBr != "SLEV" || inf.Batch != "true" || inf.DbtrAgt != "NWBKGB2L" || inf.Date != "2026-11-02" {
				t.Fatalf("%+v", inf)
			}
		}},
		{"wire is urgent", func(t *testing.T, in *domain.Pain001Input) {
			in.Profile = profileFor(t, domain.VariantUK, nil)
			in.Payments[0].Method = methodOf(t, domain.MethodWire, domain.FormatPain001)
		}, func(t *testing.T, d parsedDoc) {
			if d.Infs[0].SvcLvl != "URGP" || d.Infs[0].ChrgBr != "SHAR" {
				t.Fatalf("%+v", d.Infs[0])
			}
		}},
		{"non-sepa transfer defaults to NURG and SHAR", func(t *testing.T, in *domain.Pain001Input) {
			in.Profile = profileFor(t, domain.VariantCH, map[string]string{"batch_booking": "false"})
		}, func(t *testing.T, d parsedDoc) {
			if inf := d.Infs[0]; inf.SvcLvl != "NURG" || inf.ChrgBr != "SHAR" || inf.Batch != "false" {
				t.Fatalf("%+v", inf)
			}
		}},
		{"charge bearer override", func(t *testing.T, in *domain.Pain001Input) {
			in.Profile = profileFor(t, domain.VariantUK, map[string]string{"charge_bearer": `"DEBT"`})
		}, func(t *testing.T, d parsedDoc) {
			if d.Infs[0].ChrgBr != "DEBT" {
				t.Fatalf("%+v", d.Infs[0])
			}
		}},
		{"iban creditor omits agent when bic not required", nil, func(t *testing.T, d parsedDoc) {
			if d.Infs[0].Txs[0].Agent != nil {
				t.Fatalf("CdtrAgt emitted: %+v", d.Infs[0].Txs[0].Agent)
			}
		}},
		{"bic required emits agent", func(t *testing.T, in *domain.Pain001Input) {
			in.Profile = profileFor(t, domain.VariantSEPA, map[string]string{"bic_required": "true"})
		}, func(t *testing.T, d parsedDoc) {
			if a := d.Infs[0].Txs[0].Agent; a == nil || a.BIC != "COBADEFFXXX" {
				t.Fatalf("agent = %+v", a)
			}
		}},
		{"us creditor gets ABA clearing member", func(t *testing.T, in *domain.Pain001Input) {
			in.Profile = profileFor(t, domain.VariantUS, nil)
			in.Payments[0].Creditor = usCreditor
		}, func(t *testing.T, d parsedDoc) {
			a := d.Infs[0].Txs[0].Agent
			if a == nil || a.Clr != "USABA" || a.MmbID != "021000021" || a.BIC != "" {
				t.Fatalf("agent = %+v", a)
			}
		}},
		{"structured remittance per invoice", func(t *testing.T, in *domain.Pain001Input) {
			in.Profile = profileFor(t, domain.VariantSEPA, map[string]string{"structured_remittance": "true"})
			in.Payments[0].Invoices = []domain.Pain001Invoice{{Number: "A1", Amount: decimal.RequireFromString("40")}, {Number: "A2", Amount: decimal.RequireFromString("60.50")}}
		}, func(t *testing.T, d parsedDoc) {
			tx := d.Infs[0].Txs[0]
			if tx.Ustrd != "" || len(tx.Strd) != 2 || tx.Strd[0].Nb != "A1" || tx.Strd[0].Amt != "40.00" || tx.Strd[1].Nb != "A2" || tx.Strd[1].Amt != "60.50" {
				t.Fatalf("%+v", tx)
			}
		}},
		{"unstructured remittance truncated", func(t *testing.T, in *domain.Pain001Input) {
			in.Profile = profileFor(t, domain.VariantSEPA, map[string]string{"max_ustrd_length": "20"})
			in.Payments[0].Invoices = []domain.Pain001Invoice{{Number: "INVOICE-0000000001"}, {Number: "INVOICE-0000000002"}}
		}, func(t *testing.T, d parsedDoc) {
			if got := d.Infs[0].Txs[0].Ustrd; got != "INVOICE-0000000001,I" {
				t.Fatalf("Ustrd = %q", got)
			}
		}},
		{"no invoices emits no remittance", func(t *testing.T, in *domain.Pain001Input) { in.Payments[0].Invoices = nil }, func(t *testing.T, d parsedDoc) {
			if tx := d.Infs[0].Txs[0]; tx.Ustrd != "" || len(tx.Strd) != 0 {
				t.Fatalf("%+v", tx)
			}
		}},
		{"initiating party id only when set", func(t *testing.T, in *domain.Pain001Input) {
			in.Profile = profileFor(t, domain.VariantSEPA, map[string]string{"initiating_party_id": `"PASSAGE-01"`})
		}, func(t *testing.T, d parsedDoc) {
			if d.PartyID != "PASSAGE-01" || d.Name != "Passage Holding" {
				t.Fatalf("%+v", d)
			}
		}},
		{"initiating party id absent by default", nil, func(t *testing.T, d parsedDoc) {
			if d.PartyID != "" {
				t.Fatalf("PartyID = %q", d.PartyID)
			}
		}},
		{"structured creditor address", func(t *testing.T, in *domain.Pain001Input) {
			in.Profile = profileFor(t, domain.VariantSEPA, map[string]string{"structured_address_required": "true"})
		}, func(t *testing.T, d parsedDoc) {
			if c := d.Infs[0].Txs[0].Cdtr; c.Street != "Hauptstrasse 1" || c.Town != "Berlin" || c.Ctry != "DE" {
				t.Fatalf("%+v", c)
			}
		}},
		{"unstructured creditor address has country only", nil, func(t *testing.T, d parsedDoc) {
			if c := d.Infs[0].Txs[0].Cdtr; c.Street != "" || c.Town != "" || c.Ctry != "DE" {
				t.Fatalf("%+v", c)
			}
		}},
		{"three payments over two dates give two PmtInf", func(t *testing.T, in *domain.Pain001Input) {
			in.Payments = []domain.Pain001Payment{payment(t, "P3", "3", day2), payment(t, "P1", "1", day1), payment(t, "P2", "2", day1)}
		}, func(t *testing.T, d parsedDoc) {
			if len(d.Infs) != 2 || d.Infs[0].ID != "F1-1" || d.Infs[1].ID != "F1-2" || d.Infs[0].Date != "2026-11-02" || len(d.Infs[0].Txs) != 2 || d.Infs[0].Txs[0].InstrID != "P1" || d.Infs[1].Txs[0].InstrID != "P3" {
				t.Fatalf("%+v", d.Infs)
			}
			if d.NbOfTxs != 3 || d.CtrlSum != "6.00" {
				t.Fatalf("header = %+v", d)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := baseInput(t)
			if c.mutate != nil {
				c.mutate(t, &in)
			}
			_, doc := generate(t, in)
			c.check(t, doc)
		})
	}
}

func TestGeneratePain001_RejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, in *domain.Pain001Input)
		code   string
		detail string
	}{
		{"no payments", func(t *testing.T, in *domain.Pain001Input) { in.Payments = nil }, domain.CodeValidation, "at least one"},
		{"file number outside pattern", func(t *testing.T, in *domain.Pain001Input) { in.FileNumber = "F_1" }, domain.CodeValidation, "MsgId"},
		{"composed PmtInfId too long", func(t *testing.T, in *domain.Pain001Input) { in.FileNumber = strings.Repeat("F", 35) }, domain.CodeValidation, "PmtInfId"},
		{"payment number too long", func(t *testing.T, in *domain.Pain001Input) { in.Payments[0].Number = strings.Repeat("P", 36) }, domain.CodeValidation, "EndToEndId"},
		{"payment number outside pattern", func(t *testing.T, in *domain.Pain001Input) { in.Payments[0].Number = "P*1" }, domain.CodeValidation, "InstrId"},
		{"duplicate payment number", func(t *testing.T, in *domain.Pain001Input) { in.Payments = append(in.Payments, in.Payments[0]) }, domain.CodeValidation, "PAY-1"},
		{"zero created", func(t *testing.T, in *domain.Pain001Input) { in.Created = time.Time{} }, domain.CodeValidation, "created"},
		{"zero execution date", func(t *testing.T, in *domain.Pain001Input) { in.Payments[0].ExecutionDate = time.Time{} }, domain.CodeValidation, "PAY-1"},
		{"zero amount", func(t *testing.T, in *domain.Pain001Input) { in.Payments[0].Amount = decimal.Zero }, domain.CodeValidation, "PAY-1"},
		{"too many decimals", func(t *testing.T, in *domain.Pain001Input) {
			in.Payments[0].Amount = decimal.RequireFromString("1.005")
		}, domain.CodeValidation, "2 decimals"},
		{"lowercase currency", func(t *testing.T, in *domain.Pain001Input) { in.Payments[0].Currency = "eur" }, domain.CodeValidation, "currency"},
		{"zero bank profile", func(t *testing.T, in *domain.Pain001Input) { in.Profile = domain.BankProfile{} }, domain.CodeValidation, "bank profile"},
		{"bad creditor iban", func(t *testing.T, in *domain.Pain001Input) {
			in.Payments[0].Creditor.Account.IBAN = "DE00370400440532013000"
		}, domain.CodeRunInvalid, "iban"},
		{"bad creditor bic", func(t *testing.T, in *domain.Pain001Input) { in.Payments[0].Creditor.BIC = "XX" }, domain.CodeRunInvalid, "bic"},
		{"bad debtor bic", func(t *testing.T, in *domain.Pain001Input) { in.DebtorBIC = "XX" }, domain.CodeRunInvalid, "bic"},
		{"missing debtor bic", func(t *testing.T, in *domain.Pain001Input) { in.DebtorBIC = "" }, domain.CodeValidation, "debtor BIC"},
		{"both iban and other id", func(t *testing.T, in *domain.Pain001Input) { in.Payments[0].Creditor.Account.OtherID = "123" }, domain.CodeValidation, "exactly one"},
		{"invalid aba", func(t *testing.T, in *domain.Pain001Input) {
			in.Payments[0].Creditor.RoutingNumber = "021000022"
		}, domain.CodeRunInvalid, "aba:"},
		{"non-iban creditor without agent", func(t *testing.T, in *domain.Pain001Input) {
			in.Payments[0].Creditor.Account = domain.Pain001Account{OtherID: "123456"}
			in.Payments[0].Creditor.BIC = ""
		}, domain.CodeValidation, "routing number"},
		{"bic required but missing", func(t *testing.T, in *domain.Pain001Input) {
			in.Profile = profileFor(t, domain.VariantSEPA, map[string]string{"bic_required": "true"})
			in.Payments[0].Creditor.BIC = ""
		}, domain.CodeValidation, "PAY-1"},
		{"bad country", func(t *testing.T, in *domain.Pain001Input) { in.Payments[0].Creditor.Party.Address.Country = "Germany" }, domain.CodeValidation, "country"},
		{"structured address incomplete", func(t *testing.T, in *domain.Pain001Input) {
			in.Profile = profileFor(t, domain.VariantSEPA, map[string]string{"structured_address_required": "true"})
			in.Payments[0].Creditor.Party.Address.TownName = ""
		}, domain.CodeValidation, "town name"},
		{"invoice number outside charset", func(t *testing.T, in *domain.Pain001Input) { in.Payments[0].Invoices[0].Number = "RG-Ü1" }, domain.CodeValidation, "invoice number"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := baseInput(t)
			c.mutate(t, &in)
			file, err := domain.GeneratePain001(in)
			requireCode(t, err, c.code, c.detail)
			if file.Content != nil {
				t.Fatal("content returned alongside an error")
			}
		})
	}
}

func TestGeneratePain001_MixedCurrenciesSumPlainly(t *testing.T) {
	in := baseInput(t)
	yen := payment(t, "PAY-2", "1000", in.Payments[0].ExecutionDate)
	yen.Currency = "JPY"
	in.Payments = append(in.Payments, yen)
	file, doc := generate(t, in)
	if doc.CtrlSum != "1100.50" || !file.ControlSum.Equal(decimal.RequireFromString("1100.5")) || len(doc.Infs) != 1 {
		t.Fatalf("header = %+v", doc)
	}
	if tx := doc.Infs[0].Txs[1]; tx.Amt.Ccy != "JPY" || tx.Amt.Value != "1000" {
		t.Fatalf("yen transaction = %+v", tx)
	}
}

func normalised(content []byte) string {
	return creationPattern.ReplaceAllString(string(content), "<CreDtTm/>")
}

func TestGeneratePain001_Property(t *testing.T) {
	names := []string{"Müller Straße GmbH", "Łódź Handel", "Søren og Søn", "Café Étoile", "Peña Hermanos", "Acme Supply"}
	currencies := []string{"EUR", "USD", "JPY"}
	rapid.Check(t, func(rt *rapid.T) {
		count := rapid.IntRange(1, 8).Draw(rt, "count")
		in := baseInput(rt)
		in.FileNumber = rapid.StringMatching(`[A-Za-z0-9/ .,'+()-]{1,20}`).Draw(rt, "file")
		in.Payments = nil
		for i := 0; i < count; i++ {
			p := payment(rt, "X", "1", time.Date(2026, 11, rapid.IntRange(1, 3).Draw(rt, "day"), 0, 0, 0, 0, time.UTC))
			p.Number = fmt.Sprintf("P%d-%s", i, rapid.StringMatching(`[A-Za-z0-9/ .,'+()-]{0,10}`).Draw(rt, "number"))
			p.Currency = rapid.SampledFrom(currencies).Draw(rt, "currency")
			minor := int32(2)
			if p.Currency == "JPY" {
				minor = 0
			}
			p.Amount = decimal.New(rapid.Int64Range(1, 1_000_000_000).Draw(rt, "units"), -minor)
			p.Creditor.Party.Name = rapid.SampledFrom(names).Draw(rt, "name")
			if rapid.Bool().Draw(rt, "wire") {
				p.Method = methodOf(rt, domain.MethodWire, domain.FormatPain001)
			}
			in.Payments = append(in.Payments, p)
		}
		first, doc := generate(rt, in)

		sum := decimal.Zero
		txs := 0
		ids := []string{doc.MsgID}
		for _, inf := range doc.Infs {
			ids = append(ids, inf.ID)
			for _, tx := range inf.Txs {
				txs++
				ids = append(ids, tx.InstrID, tx.E2EID)
				sum = sum.Add(decimal.RequireFromString(tx.Amt.Value))
			}
		}
		if !sum.Equal(decimal.RequireFromString(doc.CtrlSum)) || txs != count || doc.NbOfTxs != count {
			rt.Fatalf("CtrlSum %s, NbOfTxs %d, parsed sum %s, parsed count %d, want count %d", doc.CtrlSum, doc.NbOfTxs, sum, txs, count)
		}
		for _, id := range ids {
			if !idPattern.MatchString(id) {
				rt.Fatalf("id %q breaks BR-19.9", id)
			}
		}

		same, _ := generate(rt, in)
		if same.SHA256 != first.SHA256 {
			rt.Fatal("equal timestamps gave different hashes")
		}
		in.Created = created.Add(time.Duration(rapid.IntRange(1, 100000).Draw(rt, "offset")) * time.Second)
		later, _ := generate(rt, in)
		if later.SHA256 == first.SHA256 || normalised(later.Content) != normalised(first.Content) {
			rt.Fatal("different timestamps must differ only in CreDtTm")
		}
	})
}
