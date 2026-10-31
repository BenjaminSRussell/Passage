package fixtures

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

var update = flag.Bool("update", false, "rewrite testdata/cash-app-500 from the generator")

// TestCashApp500_FixtureIsReproducible regenerates cash-app-500 from its fixed
// seed and requires the committed files to be byte-identical, so the fixture
// and its answer key can always be re-derived from this generator. The
// generator builds each payment from the invoices it is meant to pay (that
// choice is the answer key); it never calls the matcher. Run with -update to
// rewrite the files.
func TestCashApp500_FixtureIsReproducible(t *testing.T) {
	files := generateCashApp()
	dir := Dir("cash-app-500")
	if *update {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, content := range files {
			if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s: %v (run go test ./internal/fixtures -update)", name, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s differs from the generator output (run go test ./internal/fixtures -update)", name)
		}
	}
}

// TestCashApp500_Composition checks the loaded fixture against the PRD's
// composition (§5.30.9: 300 BAI2 checks, 150 camt.053 entries, 50 remittances)
// and that every answer-key invoice exists.
func TestCashApp500_Composition(t *testing.T) {
	fx, err := LoadCashApp()
	if err != nil {
		t.Fatal(err)
	}
	bySource := map[string]int{}
	docs := map[string]bool{}
	for _, it := range fx.OpenItems {
		if docs[it.DocumentNumber] {
			t.Errorf("document %s appears twice", it.DocumentNumber)
		}
		docs[it.DocumentNumber] = true
	}
	for _, a := range fx.Key {
		bySource[a.Source]++
		if len(a.Documents) == 0 {
			t.Errorf("%s has no answer", a.PaymentKey)
		}
		for _, d := range a.Documents {
			if !docs[d] {
				t.Errorf("%s answers unknown document %s", a.PaymentKey, d)
			}
		}
	}
	if len(fx.Key) != 500 || bySource[SourceLockbox] != 300 || bySource[SourceCAMT053] != 150 || bySource[SourceRemittance] != 50 {
		t.Errorf("answer key = %d rows %v, want 500 = 300 lockbox + 150 camt.053 + 50 remittances", len(fx.Key), bySource)
	}
	if len(fx.Remittances) != 50 {
		t.Errorf("remittances = %d, want 50", len(fx.Remittances))
	}
}

// Composition per source: category and count (500 in all).
var (
	lockboxMix = []mix{{CatSingle, 215}, {CatDiscount, 30}, {CatMulti, 30}, {CatShortNoReason, 10}, {CatUnidentified, 10}, {CatWrongReference, 5}}
	camtMix    = []mix{{CatSingle, 110}, {CatDiscount, 15}, {CatMulti, 15}, {CatShortNoReason, 5}, {CatUnidentified, 5}}
	remitMix   = []mix{{CatMulti, 25}, {CatShortWithReason, 15}, {CatDiscount, 10}}
)

type mix struct {
	category string
	count    int
}

type gen struct {
	r            *rand.Rand
	items        []OpenItem
	usSeq, deSeq int
	key          []Answer
	checks       []string // BAI2 16 records
	checkTotal   int64
	entries      []string // camt.053 Ntry elements
	remittances  []Remittance
	targetsUS    []OpenItem
	targetsDE    []OpenItem
}

var received = mustDay(CashAppReceivedDate)

func generateCashApp() map[string][]byte {
	g := &gen{r: rand.New(rand.NewPCG(30, 500))}
	for i, cat := range g.shuffled(lockboxMix) {
		g.lockboxPayment(i+1, cat)
	}
	for i, cat := range g.shuffled(camtMix) {
		g.camtPayment(i+1, cat)
	}
	for i, cat := range g.shuffled(remitMix) {
		g.remittance(i+1, cat)
	}
	g.noise()
	return map[string][]byte{
		"open_items.csv":        g.openItemsCSV(),
		"lockbox.bai2":          g.bai2(),
		"statement.camt053.xml": g.camt(),
		"remittances.json":      g.remittancesJSON(),
		"answer_key.csv":        g.answerKeyCSV(),
	}
}

func (g *gen) shuffled(m []mix) []string {
	var out []string
	for _, x := range m {
		for range x.count {
			out = append(out, x.category)
		}
	}
	g.r.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

func (g *gen) customer(de bool) string {
	if de {
		return fmt.Sprintf("00000000-0000-0000-0002-%012d", 1+g.r.IntN(60))
	}
	return fmt.Sprintf("00000000-0000-0000-0001-%012d", 1+g.r.IntN(120))
}

// cents is an invoice amount between 150.00 and 25,000.00.
func (g *gen) cents() int64 { return 15000 + g.r.Int64N(2500000-15000) }

// normalDue is a due date from 45 days before to 24 days after the received date.
func (g *gen) normalDue() time.Time { return received.AddDate(0, 0, -45+g.r.IntN(70)) }

// item posts an invoice with net 30 terms; with discount it also has 2/10 terms
// and is documented within the 9 days before paidOn, so the discount is open.
func (g *gen) item(de bool, cust string, cents int64, discount bool, paidOn time.Time) OpenItem {
	due := g.normalDue()
	if discount {
		due = paidOn.AddDate(0, 0, -g.r.IntN(10)+30)
	}
	it := OpenItem{CustomerID: cust, DocumentDate: fmtDay(due.AddDate(0, 0, -30)), DueDate: fmtDay(due),
		Amount: money(cents), OpenAmount: money(cents), DiscountAmount: decimal.Zero}
	if de {
		g.deSeq++
		it.LegalEntityID, it.Currency, it.DocumentNumber = LegalEntityDE, "EUR", fmt.Sprintf("RE-%d", 40000+g.deSeq)
	} else {
		g.usSeq++
		it.LegalEntityID, it.Currency, it.DocumentNumber = LegalEntityUS, "USD", fmt.Sprintf("INV-%d", 20000+g.usSeq)
	}
	if discount {
		it.DiscountDate = fmtDay(due.AddDate(0, 0, -20))
		it.DiscountAmount = money(discountCents(cents))
	}
	g.items = append(g.items, it)
	if de {
		g.targetsDE = append(g.targetsDE, it)
	} else {
		g.targetsUS = append(g.targetsUS, it)
	}
	return it
}

// discountCents is 2% of the amount, rounded half up to the cent.
func discountCents(cents int64) int64 { return (cents*2 + 50) / 100 }

// planned is one payment: what is paid, what the remittance says, and the answer.
type planned struct {
	cents  int64
	refs   []string // invoice numbers the payer quotes
	answer []OpenItem
	short  bool
	cust   string
}

func (g *gen) plan(de bool, cat string, paidOn time.Time) planned {
	cust := g.customer(de)
	switch cat {
	case CatSingle:
		it := g.item(de, cust, g.cents(), false, paidOn)
		return planned{cents: cents(it.Amount), refs: []string{it.DocumentNumber}, answer: []OpenItem{it}, cust: cust}
	case CatDiscount:
		c := g.cents()
		it := g.item(de, cust, c, true, paidOn)
		return planned{cents: c - discountCents(c), refs: []string{it.DocumentNumber}, answer: []OpenItem{it}, cust: cust}
	case CatMulti:
		p := planned{cust: cust}
		for range 2 + g.r.IntN(3) {
			it := g.item(de, cust, g.cents(), false, paidOn)
			p.cents += cents(it.Amount)
			p.refs = append(p.refs, it.DocumentNumber)
			p.answer = append(p.answer, it)
		}
		return p
	case CatShortNoReason:
		c := g.cents()
		it := g.item(de, cust, c, false, paidOn)
		short := c * int64(1+g.r.IntN(5)) / 100
		return planned{cents: c - short, refs: []string{it.DocumentNumber}, answer: []OpenItem{it}, short: true, cust: cust}
	case CatShortWithReason:
		c := g.cents()
		it := g.item(de, cust, c, false, paidOn)
		short := 500 + g.r.Int64N(4000) // 5.00 to 44.99, under the 50.00 USD tolerance
		return planned{cents: c - short, refs: []string{it.DocumentNumber}, answer: []OpenItem{it}, short: true, cust: cust}
	case CatUnidentified:
		it := g.item(de, cust, g.cents(), false, paidOn)
		return planned{cents: cents(it.Amount), answer: []OpenItem{it}, cust: cust}
	case CatWrongReference:
		paid := g.item(de, cust, g.cents(), false, paidOn)
		cited := g.item(de, cust, g.cents(), false, paidOn)
		return planned{cents: cents(paid.Amount), refs: []string{cited.DocumentNumber}, answer: []OpenItem{paid}, cust: cust}
	}
	panic("unknown category " + cat)
}

func (g *gen) answer(key, source, cat string, p planned) {
	docs := make([]string, 0, len(p.answer))
	for _, it := range p.answer {
		docs = append(docs, it.DocumentNumber)
	}
	g.key = append(g.key, Answer{PaymentKey: key, Source: source, Category: cat, Documents: docs})
}

func (g *gen) lockboxPayment(n int, cat string) {
	p := g.plan(false, cat, received)
	key := fmt.Sprintf("LBX%04d", n)
	ref := ""
	if len(p.refs) > 0 {
		ref = strings.Join(p.refs, " ")
		switch g.r.IntN(3) {
		case 1:
			ref = "PMT " + ref
		case 2:
			ref = ref + " ACCT " + fmt.Sprint(1000+g.r.IntN(9000))
		}
	}
	g.checks = append(g.checks, fmt.Sprintf("16,116,%d,0,%s,%s,CHECK %d/", p.cents, key, ref, 5000+n))
	g.checkTotal += p.cents
	g.answer(key, SourceLockbox, cat, p)
}

func (g *gen) camtPayment(n int, cat string) {
	valueDate := received.AddDate(0, 0, -g.r.IntN(6))
	p := g.plan(true, cat, valueDate)
	key := fmt.Sprintf("NTRY%04d", n)
	var rmt string
	switch {
	case len(p.refs) == 0:
		rmt = "<Ustrd>Zahlung Maerz</Ustrd>"
	case len(p.refs) == 1 && g.r.IntN(2) == 0:
		rmt = "<Strd><CdtrRefInf><Ref>" + p.refs[0] + "</Ref></CdtrRefInf></Strd>"
	case len(p.refs) == 1:
		rmt = "<Ustrd>Rechnung " + p.refs[0] + "</Ustrd>"
	default:
		rmt = "<Ustrd>Rechnungen " + strings.Join(p.refs, " ") + "</Ustrd>"
	}
	custNo := p.cust[len(p.cust)-3:]
	g.entries = append(g.entries, fmt.Sprintf(`      <Ntry>
        <Amt Ccy="EUR">%s</Amt>
        <CdtDbtInd>CRDT</CdtDbtInd>
        <Sts><Cd>BOOK</Cd></Sts>
        <BookgDt><Dt>%s</Dt></BookgDt>
        <ValDt><Dt>%s</Dt></ValDt>
        <NtryRef>%s</NtryRef>
        <NtryDtls><TxDtls>
          <RltdPties><Dbtr><Pty><Nm>Kunde %s GmbH</Nm></Pty></Dbtr><DbtrAcct><Id><IBAN>DE0237040044%s</IBAN></Id></DbtrAcct></RltdPties>
          <RmtInf>%s</RmtInf>
        </TxDtls></NtryDtls>
      </Ntry>`, money(p.cents).StringFixed(2), CashAppReceivedDate, fmtDay(valueDate), key, custNo, p.cust[len(p.cust)-10:], rmt))
	g.answer(key, SourceCAMT053, cat, p)
}

var shortReasons = []string{"DAMAGED GOODS", "PRICING DIFFERENCE", "FREIGHT DEDUCTED", "PROMO ALLOWANCE"}

func (g *gen) remittance(n int, cat string) {
	p := g.plan(false, cat, received)
	rm := Remittance{Key: fmt.Sprintf("RMT%03d", n), LegalEntityID: LegalEntityUS, CustomerID: p.cust, Currency: "USD",
		ValueDate: CashAppReceivedDate, Amount: money(p.cents)}
	switch {
	case cat == CatMulti:
		for _, it := range p.answer {
			rm.Lines = append(rm.Lines, RemittanceLine{Reference: it.DocumentNumber, Amount: it.Amount})
		}
	default:
		rm.Lines = []RemittanceLine{{Reference: p.refs[0], Amount: money(p.cents)}}
	}
	if cat == CatShortWithReason {
		rm.ShortPayReason = shortReasons[g.r.IntN(len(shortReasons))]
	}
	g.remittances = append(g.remittances, rm)
	g.answer(rm.Key, SourceRemittance, cat, p)
}

// noise adds open invoices nobody pays in this run, including decoys: numbers
// that are a prefix of a paid invoice's number (INV-2000 inside INV-20001),
// and amounts equal to a paid invoice of another customer. The US entity then
// has more open items than the 500 a match reads.
func (g *gen) noise() {
	add := func(de bool, doc string, c int64) {
		it := OpenItem{CustomerID: g.customer(de), DueDate: fmtDay(g.normalDue()), Amount: money(c), OpenAmount: money(c),
			DocumentNumber: doc, DiscountAmount: decimal.Zero}
		it.DocumentDate = fmtDay(mustDay(it.DueDate).AddDate(0, 0, -30))
		it.LegalEntityID, it.Currency = LegalEntityUS, "USD"
		if de {
			it.LegalEntityID, it.Currency = LegalEntityDE, "EUR"
		}
		g.items = append(g.items, it)
	}
	for i := range 50 {
		add(false, fmt.Sprintf("INV-%d", 2000+i), g.cents())
	}
	for range 40 {
		add(false, g.nextDoc(false), cents(g.targetsUS[g.r.IntN(len(g.targetsUS))].Amount))
	}
	for range 110 {
		add(false, g.nextDoc(false), g.cents())
	}
	for i := range 20 {
		add(true, fmt.Sprintf("RE-%d", 4000+i), g.cents())
	}
	for range 20 {
		add(true, g.nextDoc(true), cents(g.targetsDE[g.r.IntN(len(g.targetsDE))].Amount))
	}
	for range 40 {
		add(true, g.nextDoc(true), g.cents())
	}
}

func (g *gen) nextDoc(de bool) string {
	if de {
		g.deSeq++
		return fmt.Sprintf("RE-%d", 40000+g.deSeq)
	}
	g.usSeq++
	return fmt.Sprintf("INV-%d", 20000+g.usSeq)
}

func (g *gen) openItemsCSV() []byte {
	var b strings.Builder
	b.WriteString("legal_entity_id,customer_id,document_number,document_date,due_date,currency,amount,discount_date,discount_amount\n")
	for _, it := range g.items {
		fmt.Fprintf(&b, "%s,%s,%s,%s,%s,%s,%s,%s,%s\n", it.LegalEntityID, it.CustomerID, it.DocumentNumber, it.DocumentDate,
			it.DueDate, it.Currency, it.Amount.StringFixed(2), it.DiscountDate, it.DiscountAmount.StringFixed(2))
	}
	return []byte(b.String())
}

// bai2 writes one lockbox file: one group, one account, one 16 record per check.
func (g *gen) bai2() []byte {
	var b strings.Builder
	b.WriteString("01,BANKX,LOCKBOX,270315,0600,1,,/\n")
	b.WriteString("02,LOCKBOX,BANKX,1,270315,0600,USD,2/\n")
	b.WriteString("03,987654321,USD/\n")
	for _, c := range g.checks {
		b.WriteString(c + "\n")
	}
	accountRecords := len(g.checks) + 2 // 03, the 16s, 49
	fmt.Fprintf(&b, "49,%d,%d/\n", g.checkTotal, accountRecords)
	fmt.Fprintf(&b, "98,%d,1,%d/\n", g.checkTotal, accountRecords+2)
	fmt.Fprintf(&b, "99,%d,1,%d/\n", g.checkTotal, accountRecords+4)
	return []byte(b.String())
}

func (g *gen) camt() []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<Document xmlns="urn:iso:std:iso:20022:tech:xsd:camt.053.001.08">
  <BkToCstmrStmt>
    <GrpHdr><MsgId>CASHAPP500</MsgId><CreDtTm>2027-03-15T06:00:00</CreDtTm></GrpHdr>
    <Stmt>
      <Id>STMT-20270315</Id>
      <Acct><Id><IBAN>DE89370400440532013000</IBAN></Id></Acct>
`)
	for _, e := range g.entries {
		b.WriteString(e + "\n")
	}
	b.WriteString("    </Stmt>\n  </BkToCstmrStmt>\n</Document>\n")
	return []byte(b.String())
}

func (g *gen) remittancesJSON() []byte {
	raw, err := json.MarshalIndent(g.remittances, "", "  ")
	if err != nil {
		panic(err)
	}
	return append(raw, '\n')
}

func (g *gen) answerKeyCSV() []byte {
	var b strings.Builder
	b.WriteString("payment_key,source,category,documents\n")
	for _, a := range g.key {
		fmt.Fprintf(&b, "%s,%s,%s,%s\n", a.PaymentKey, a.Source, a.Category, strings.Join(a.Documents, " "))
	}
	return []byte(b.String())
}

func money(cents int64) decimal.Decimal { return decimal.New(cents, -2) }
func cents(d decimal.Decimal) int64     { return d.Shift(2).IntPart() }
func fmtDay(t time.Time) string         { return t.Format("2006-01-02") }
func mustDay(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}
