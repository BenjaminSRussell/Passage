package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/billing/internal/revrec"
	"github.com/BenjaminSRussell/passage/services/billing/internal/store"
	"github.com/BenjaminSRussell/passage/services/billing/migrations"
)

type gridLine struct{ price, account, taxCode, discount string }

type refusingLedger struct {
	*ledgerclient.Fake
	refuse map[string]error
	onFail func(documentID string)
}

func (l *refusingLedger) Post(ctx context.Context, e ledgerclient.Entry) (string, error) {
	if err := l.refuse[e.DocumentID]; err != nil {
		if l.onFail != nil {
			l.onFail(e.DocumentID)
		}
		return "", err
	}
	return l.Fake.Post(ctx, e)
}

type fakeAllocations struct {
	byOrder map[string]revrec.Allocation
	err     error
	reads   int
}

func (f *fakeAllocations) Allocation(_ context.Context, _, orderID string) (revrec.Allocation, error) {
	f.reads++
	if f.err != nil {
		return revrec.Allocation{}, f.err
	}
	a, ok := f.byOrder[orderID]
	if !ok {
		return revrec.Allocation{}, revrec.ErrNoContract
	}
	return a, nil
}

type glRig struct {
	handler  http.Handler
	api      *API
	admin    *pgxpool.Pool
	fake     *ledgerclient.Fake
	ledger   *refusingLedger
	allocs   *fakeAllocations
	ledgerID string
	clock    time.Time
	seq      int
}

func (g *glRig) shipLine(t *testing.T, invoiceID string, lineNo int, orderID, productID string) {
	t.Helper()
	if _, err := g.admin.Exec(context.Background(), `update invoice_line set order_id = $3::uuid, product_id = $4::uuid, order_line_id = $5::uuid,
		packing_slip_id = $5::uuid, packing_slip_line_id = $5::uuid, packing_slip_date = '2027-03-08'
		where invoice_id = $1::uuid and line_no = $2`, invoiceID, lineNo, orderID, productID, ids.New()); err != nil {
		t.Fatal(err)
	}
}

func (g *glRig) allocate(orderID string, obligations ...revrec.Obligation) {
	g.allocs.byOrder[orderID] = revrec.Allocation{Currency: "USD", Obligations: obligations}
}

func (g *glRig) deferredInvoice(t *testing.T, price string) string {
	t.Helper()
	id := g.invoice(t, gridLine{price, "4100", "", ""})
	order, product := ids.New(), ids.New()
	g.shipLine(t, id, 1, order, product)
	g.allocate(order, revrec.Obligation{ProductID: product, Allocated: decimal.RequireFromString(price), Method: "RATABLE"})
	return id
}

func (g *glRig) bundleInvoice(t *testing.T) string {
	t.Helper()
	var lines []gridLine
	for _, l := range testkit.BundleInvoiceLines {
		lines = append(lines, gridLine{l.UnitPrice, l.RevenueAccount, "", l.DiscountPct})
	}
	id := g.invoice(t, lines...)
	order := ids.New()
	var obligations []revrec.Obligation
	for i, l := range testkit.BundleInvoiceLines {
		product := ids.New()
		g.shipLine(t, id, i+1, order, product)
		obligations = append(obligations, revrec.Obligation{ProductID: product, Allocated: decimal.RequireFromString(l.Allocated), Method: l.Method})
	}
	g.allocate(order, obligations...)
	return id
}

func newGLRig(t *testing.T, configured bool) *glRig {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(app)
	fake := ledgerclient.NewFake()
	g := &glRig{admin: admin, fake: fake, allocs: &fakeAllocations{byOrder: map[string]revrec.Allocation{}}, ledger: &refusingLedger{Fake: fake, refuse: map[string]error{}}, ledgerID: ids.New(), clock: time.Date(2027, 3, 12, 15, 4, 0, 0, time.UTC)}
	fake.AddLedger(g.ledgerID, leID)
	tax, err := ParseTaxRates("EXEMPT=0,DE19=19")
	if err != nil {
		t.Fatal(err)
	}
	g.api = New(st, tax, 2000).WithAllocations(g.allocs)
	if configured {
		g.api.WithLedger(g.ledger, st, map[string]store.GLProfile{leID: {
			LegalEntityID: leID, LedgerID: g.ledgerID, ReceivableAccount: "1200", TaxAccount: "2300", ChargeAccount: "4800", DeferredRevenueAccounts: []string{"4100"},
		}})
	}
	g.api.now = func() time.Time { return g.clock }
	r := httpx.NewRouter()
	g.api.Register(r)
	g.handler = r
	return g
}

func (g *glRig) invoiceAs(t *testing.T, headers map[string]string, lines ...gridLine) string {
	t.Helper()
	g.seq++
	parts := make([]string, len(lines))
	for i, l := range lines {
		tc, disc := l.taxCode, l.discount
		if tc == "" {
			tc = "EXEMPT"
		}
		if disc == "" {
			disc = "0"
		}
		parts[i] = fmt.Sprintf(`{"description":"Line","qty":"1","unitPrice":%q,"discountPct":%q,"revenueAccount":%q,"taxCode":%q}`, l.price, disc, l.account, tc)
	}
	body := fmt.Sprintf(`{"legalEntityId":%q,"customerId":%q,"invoiceAccountId":%q,"currency":"USD","invoiceDate":"2027-03-10","paymentTermDays":30,"lines":[%s]}`, leID, custID, acctID, strings.Join(parts, ","))
	var created struct {
		ID string `json:"id"`
	}
	rec := doAs(g.handler, http.MethodPost, "/v1/billing/free-text-invoices", fmt.Sprintf("gl-%d-%d", g.seq, len(headers)), body, allDuties, headers)
	decode(t, rec, &created)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create invoice: %d %s", rec.Code, rec.Body)
	}
	if rec := doAs(g.handler, http.MethodPost, "/v1/billing/invoices/"+created.ID+":post", "", "", allDuties, headers); rec.Code != http.StatusOK {
		t.Fatalf("post invoice: %d %s", rec.Code, rec.Body)
	}
	return created.ID
}

func (g *glRig) invoice(t *testing.T, lines ...gridLine) string { return g.invoiceAs(t, nil, lines...) }

type glResult struct {
	Items  []glBookedOut `json:"items"`
	Failed []glFailedOut `json:"failed"`
	More   bool          `json:"more"`
}

func (g *glRig) book(t *testing.T, want int, extra string) glResult {
	t.Helper()
	return g.bookAs(t, want, extra, nil)
}

func (g *glRig) bookAs(t *testing.T, want int, extra string, headers map[string]string) glResult {
	t.Helper()
	rec := doAs(g.handler, http.MethodPost, "/v1/billing/gl-postings", "", `{"legalEntityId":"`+leID+`"`+extra+`}`, allDuties, headers)
	if rec.Code != want {
		t.Fatalf("gl-postings status %d, want %d: %s", rec.Code, want, rec.Body)
	}
	var out glResult
	if want == http.StatusOK {
		decode(t, rec, &out)
	}
	return out
}

func (g *glRig) balance(t *testing.T, account string) ledgerclient.Balance {
	t.Helper()
	b, err := g.fake.Balance(context.Background(), ledgerclient.BalanceQuery{LegalEntityID: leID, LedgerID: g.ledgerID, Account: account})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (g *glRig) entryFor(t *testing.T, id string) ledgerclient.Stored {
	t.Helper()
	for _, e := range g.fake.Entries() {
		if e.DocumentID == id {
			return e
		}
	}
	t.Fatalf("no ledger entry for document %s", id)
	return ledgerclient.Stored{}
}

func lineAmounts(e ledgerclient.Stored) map[string]string {
	out := map[string]string{}
	for _, l := range e.Lines {
		side := "Dr "
		if l.Credit {
			side = "Cr "
		}
		out[side+l.Account] = l.Amount.StringFixed(2)
	}
	return out
}

func sameAmounts(got, want map[string]string) bool {
	if len(got) != len(want) {
		return false
	}
	for k, v := range want {
		if got[k] != v {
			return false
		}
	}
	return true
}

func TestGLPostings_InvoiceJournalIsOneBalancedEntryAndCreditNoteMirrorsIt(t *testing.T) {
	g := newGLRig(t, true)
	id := g.invoice(t, gridLine{"3000.00", "4100", "", ""}, gridLine{"500.00", "4000", "DE19", ""})
	order, product := ids.New(), ids.New()
	g.shipLine(t, id, 1, order, product)
	g.allocate(order, revrec.Obligation{ProductID: product, Allocated: decimal.RequireFromString("3000.00"), Method: "RATABLE"})
	zero := g.invoice(t, gridLine{"0", "4000", "", ""})

	out := g.book(t, http.StatusOK, "")
	if len(out.Items) != 1 || out.Items[0].DocumentID != id || out.Items[0].Kind != "invoice" || out.More || len(out.Failed) != 0 {
		t.Fatalf("booked %+v, want the one invoice with an amount to post (zero invoice %s is skipped)", out, zero)
	}
	e := g.entryFor(t, id)
	want := map[string]string{"Dr 1200": "3595.00", "Cr 2400": "3000.00", "Cr 4000": "500.00", "Cr 2300": "95.00"}
	if e.IdempotencyKey != "billing-invoice-"+id || e.SourceType != "billing_invoice" || e.LedgerID != g.ledgerID || e.PostingDate != "2027-03-12" || !sameAmounts(lineAmounts(e), want) {
		t.Errorf("entry %+v amounts %v, want %v keyed billing-invoice-%s", e, lineAmounts(e), want, id)
	}

	rec := do(g.handler, http.MethodPost, "/v1/billing/credit-notes", "cn-gl", creditBody(id, "2027-03-20", ""))
	var cn struct {
		ID string `json:"id"`
	}
	decode(t, rec, &cn)
	if rec.Code != http.StatusCreated || do(g.handler, http.MethodPost, "/v1/billing/credit-notes/"+cn.ID+":post", "", "").Code != http.StatusOK {
		t.Fatalf("credit note %d %s", rec.Code, rec.Body)
	}
	if out := g.book(t, http.StatusOK, ""); len(out.Items) != 1 || out.Items[0].Kind != "credit_note" {
		t.Fatalf("second call %+v, want only the credit note", out)
	}
	mirror := g.entryFor(t, cn.ID)
	wantMirror := map[string]string{"Cr 1200": "3595.00", "Dr 2400": "3000.00", "Dr 4000": "500.00", "Dr 2300": "95.00"}
	if mirror.IdempotencyKey != "billing-credit-note-"+cn.ID || !sameAmounts(lineAmounts(mirror), wantMirror) {
		t.Errorf("credit note entry %+v amounts %v, want %v", mirror, lineAmounts(mirror), wantMirror)
	}
	if b := g.balance(t, "2400"); !b.Net().IsZero() {
		t.Errorf("2400 nets %s after the full credit note, want 0", b.Net())
	}
	if again := g.book(t, http.StatusOK, ""); len(again.Items) != 0 || len(g.fake.Entries()) != 2 || g.api.glPostings.Load() != 2 {
		t.Errorf("third call booked %d, ledger holds %d entries, counter %d; want nothing new", len(again.Items), len(g.fake.Entries()), g.api.glPostings.Load())
	}
}

func TestGLPostings_BundleInvoiceBooksTheAllocationNotTheContractualLinePrices(t *testing.T) {
	g := newGLRig(t, true)
	id := g.bundleInvoice(t)
	g.book(t, http.StatusOK, "")
	want := map[string]string{"Dr 1200": testkit.BundleInvoiceGross, "Cr 4000": testkit.BundleInvoiceRevenue, "Cr 2400": testkit.BundleInvoiceDeferred}
	if got := lineAmounts(g.entryFor(t, id)); !sameAmounts(got, want) {
		t.Fatalf("bundle entry %v, want %v (BR-29.1: Cr 4000 7,200.00, Cr 2400 3,600.00 although the lines bill 8,000.00 / 1,350.00 / 1,450.00)", got, want)
	}
	rows, err := g.admin.Query(context.Background(), `select line_net::text from invoice_line where invoice_id = $1::uuid order by line_no`, id)
	if err != nil {
		t.Fatal(err)
	}
	nets, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || len(nets) != 3 {
		t.Fatalf("invoice lines %v, err %v", nets, err)
	}
	for i, want := range []string{"8000", "1350", "1450"} {
		if !decimal.RequireFromString(nets[i]).Equal(decimal.RequireFromString(want)) {
			t.Errorf("line %d bills %s, want the contractual %s", i+1, nets[i], want)
		}
	}

	for _, recognized := range []string{"1350.00", "2250.00"} {
		if _, err := g.fake.Post(context.Background(), ledgerclient.Entry{LegalEntityID: leID, LedgerID: g.ledgerID, DocumentID: ids.New(), PostingDate: "2027-04-30", Currency: "USD",
			IdempotencyKey: "recognize-" + recognized, SourceType: "revenue_recognition",
			Lines: []ledgerclient.Line{{Account: "2400", Amount: decimal.RequireFromString(recognized)}, {Account: "4000", Credit: true, Amount: decimal.RequireFromString(recognized)}}}); err != nil {
			t.Fatal(err)
		}
	}
	if b := g.balance(t, "2400"); !b.Net().IsZero() || b.Credit.StringFixed(2) != testkit.BundleInvoiceDeferred {
		t.Errorf("2400 holds credits %s and nets %s after the term, want 3,600.00 credited and 0.00 left", b.Credit, b.Net())
	}

	cn := g.creditNote(t, id)
	if out := g.book(t, http.StatusOK, ""); len(out.Items) != 1 || out.Items[0].DocumentID != cn {
		t.Fatalf("credit note call %+v, want the note booked", out)
	}
	wantMirror := map[string]string{"Cr 1200": testkit.BundleInvoiceGross, "Dr 4000": testkit.BundleInvoiceRevenue, "Dr 2400": testkit.BundleInvoiceDeferred}
	if got := lineAmounts(g.entryFor(t, cn)); !sameAmounts(got, wantMirror) {
		t.Errorf("credit note entry %v, want the allocation mirrored %v", got, wantMirror)
	}
}

func TestGLPostings_InvoiceWhoseBilledNetsDifferFromTheAllocationIsParkedNotBooked(t *testing.T) {
	g := newGLRig(t, true)
	id := g.invoice(t, gridLine{"1450.00", "4100", "", ""})
	order, product := ids.New(), ids.New()
	g.shipLine(t, id, 1, order, product)
	g.allocate(order, revrec.Obligation{ProductID: product, Allocated: decimal.RequireFromString("2250.00"), Method: "RATABLE"})
	free := g.invoice(t, gridLine{"300.00", "4100", "", ""})

	out := g.book(t, http.StatusOK, "")
	if len(out.Items) != 0 || len(out.Failed) != 2 || len(g.fake.Entries()) != 0 {
		t.Fatalf("booked %+v with %d entries, want both refused locally and nothing in the ledger", out, len(g.fake.Entries()))
	}
	for _, doc := range []string{id, free} {
		if g.parkedCode(t, doc) != CodeAllocationMismatch {
			t.Errorf("document %s parked %q, want %s (billed 1,450.00 against 2,250.00 allocated; a deferred line with no obligation)", doc, g.parkedCode(t, doc), CodeAllocationMismatch)
		}
	}
}

func TestGLPostings_HardwareShippedFirstIsParkedNotBookedAtTheBilledPrice(t *testing.T) {
	g := newGLRig(t, true)
	id := g.invoice(t, gridLine{"8000.00", "4000", "", ""})
	order, hardware := ids.New(), ids.New()
	g.shipLine(t, id, 1, order, hardware)
	g.allocate(order, revrec.Obligation{ProductID: hardware, Allocated: decimal.RequireFromString("7200.00"), Method: revrec.MethodPoint},
		revrec.Obligation{ProductID: ids.New(), Allocated: decimal.RequireFromString("3600.00"), Method: "RATABLE"})
	out := g.book(t, http.StatusOK, "")
	if len(out.Items) != 0 || len(out.Failed) != 1 || out.Failed[0].LedgerCode != CodeAllocationMismatch || len(g.fake.Entries()) != 0 || g.parkedCode(t, id) != CodeAllocationMismatch {
		t.Fatalf("hardware first: %+v with %d entries, parked %q; want BIL-0208 and nothing booked (billed 8,000.00 against 7,200.00 allocated)", out, len(g.fake.Entries()), g.parkedCode(t, id))
	}
}

func TestGLPostings_RevrecOutageEndsTheBatchAfterTheFirstFailure(t *testing.T) {
	g := newGLRig(t, true)
	for i := 0; i < 3; i++ {
		g.deferredInvoice(t, "100.00")
	}
	g.allocs.err = errors.New("status 503")
	out := g.book(t, http.StatusOK, "")
	if g.allocs.reads != 1 || len(out.Failed) != 1 || !out.More {
		t.Errorf("outage: %d reads, %d failed, more %v; want one read, one failure and more", g.allocs.reads, len(out.Failed), out.More)
	}
}

func TestApportion_PartsSumToTheRoundedWhole(t *testing.T) {
	third := decimal.RequireFromString("3.333333333333")
	got := apportion([]decimal.Decimal{third, third, third}, 2)
	sum := decimal.Zero
	for _, p := range got {
		sum = sum.Add(p)
	}
	if !sum.Equal(decimal.RequireFromString("10.00")) {
		t.Errorf("parts %v sum to %s, want 10.00", got, sum)
	}
}

func TestGLPostings_AllocationNotAvailableBacksOffAndNeverBooksTheBilledAmounts(t *testing.T) {
	g := newGLRig(t, true)
	id := g.deferredInvoice(t, "900.00")
	ok := g.invoice(t, gridLine{"40.00", "4000", "", ""})
	for _, c := range []struct {
		name string
		err  error
	}{{"no contract", revrec.ErrNoContract}, {"revrec down", errors.New("status 503")}, {"two contracts", revrec.ErrAmbiguous}} {
		name := c.name
		g.allocs.err = c.err
		for attempt := 0; attempt < store.MaxGLAttempts+2; attempt++ {
			g.clock = g.clock.AddDate(0, 0, 2)
			out := g.book(t, http.StatusOK, "")
			if len(out.Failed) != 1 || out.Failed[0].DocumentID != id || out.Failed[0].LedgerCode != CodeAllocationUnavailable || out.Failed[0].Parked {
				t.Fatalf("%s attempt %d: %+v, want the deferred invoice backed off with %s and never parked", name, attempt, out, CodeAllocationUnavailable)
			}
			if attempt == 0 && name == "no contract" {
				g.entryFor(t, ok)
			}
		}
		if g.parkedCode(t, id) != "" || len(g.fake.Entries()) != 1 {
			t.Fatalf("%s: parked %q with %d entries, want an unparked invoice and only the invoice without deferral booked", name, g.parkedCode(t, id), len(g.fake.Entries()))
		}
	}
	g.allocs.err = nil
	g.clock = g.clock.AddDate(0, 0, 2)
	if out := g.book(t, http.StatusOK, ""); len(out.Items) != 1 || out.Items[0].DocumentID != id {
		t.Fatalf("once revrec answers %+v, want the invoice booked", out)
	}
	if b := g.balance(t, "2400"); b.Credit.StringFixed(2) != "900.00" {
		t.Errorf("2400 credited %s, want 900.00", b.Credit)
	}
}

func TestGLPostings_RevrecNotConfiguredHoldsDeferredInvoices(t *testing.T) {
	g := newGLRig(t, true)
	g.api.allocations = nil
	id := g.invoice(t, gridLine{"900.00", "4100", "", ""})
	g.shipLine(t, id, 1, ids.New(), ids.New())
	if out := g.book(t, http.StatusOK, ""); len(out.Failed) != 1 || out.Failed[0].LedgerCode != CodeAllocationUnavailable || len(g.fake.Entries()) != 0 {
		t.Errorf("without revrec %+v with %d entries, want the invoice held with %s", out, len(g.fake.Entries()), CodeAllocationUnavailable)
	}
}

func TestGLPostings_LostLedgerAnswerNeverBooksADocumentTwice(t *testing.T) {
	g := newGLRig(t, true)
	g.deferredInvoice(t, "1000.00")
	g.deferredInvoice(t, "250.00")
	g.fake.LoseAnswers(errors.New("timeout"))
	if g.book(t, http.StatusBadGateway, ""); len(g.fake.Entries()) != 1 {
		t.Fatalf("%d entries after the lost answer, want the one the ledger accepted", len(g.fake.Entries()))
	}
	g.fake.LoseAnswers(nil)
	g.clock = g.clock.Add(time.Hour)
	if out := g.book(t, http.StatusOK, ""); len(out.Items) != 2 {
		t.Fatalf("retry booked %d documents, want both", len(out.Items))
	}
	if b := g.balance(t, "2400"); len(g.fake.Entries()) != 2 || b.Credit.StringFixed(2) != "1250.00" {
		t.Errorf("%d entries and %s credited on 2400, want 2 and 1250.00", len(g.fake.Entries()), b.Credit)
	}
}

func TestGLPostings_MidRunFailureReportsTheEntriesAlreadyBooked(t *testing.T) {
	g := newGLRig(t, true)
	first := g.invoice(t, gridLine{"10.00", "4000", "", ""})
	second := g.invoice(t, gridLine{"20.00", "4000", "", ""})
	third := g.invoice(t, gridLine{"30.00", "4000", "", ""})
	g.ledger.refuse[third] = &ledgerclient.Error{Op: "post", Status: http.StatusServiceUnavailable}
	rec := do(g.handler, http.MethodPost, "/v1/billing/gl-postings", "", `{"legalEntityId":"`+leID+`"}`)
	p := decodeProblemOf(t, rec)
	if rec.Code != http.StatusBadGateway || p.Code != CodeLedgerUnavailable || p.Errors["booked"] != first+","+second {
		t.Errorf("mid-run failure %d %s %v, want 502 listing %s,%s as booked", rec.Code, p.Code, p.Errors, first, second)
	}
}

func TestGLPostings_RefusedDocumentsNeverStarveTheOnesBehindThem(t *testing.T) {
	g := newGLRig(t, true)
	refused := &ledgerclient.Error{Op: "post", Status: http.StatusUnprocessableEntity, Code: "LEDGER_CURRENCY_MISMATCH"}
	for i := 0; i < defaultGLBatch+1; i++ {
		g.ledger.refuse[g.invoice(t, gridLine{"5.00", "4000", "", ""})] = refused
	}
	var good []string
	for i := 0; i < 3; i++ {
		good = append(good, g.invoice(t, gridLine{"7.00", "4000", "", ""}))
	}

	first := g.book(t, http.StatusOK, "")
	if len(first.Items) != 0 || len(first.Failed) != defaultGLBatch || !first.More {
		t.Fatalf("first call booked %d and refused %d (more %v), want the batch of %d refused and more", len(first.Items), len(first.Failed), first.More, defaultGLBatch)
	}
	second := g.book(t, http.StatusOK, "")
	if len(second.Items) != 3 || len(second.Failed) != 1 || second.More {
		t.Fatalf("second call booked %d and refused %d, want the 3 good invoices booked behind the refused ones", len(second.Items), len(second.Failed))
	}
	for _, id := range good {
		g.entryFor(t, id)
	}
	if third := g.book(t, http.StatusOK, ""); len(third.Items)+len(third.Failed) != 0 {
		t.Errorf("third call %+v, want refused documents held back by their backoff", third)
	}
}

func TestGLPostings_RefusedDocumentIsParkedAfterTheAttemptLimitUntilRetried(t *testing.T) {
	g := newGLRig(t, true)
	id := g.invoice(t, gridLine{"5.00", "4000", "", ""})
	g.ledger.refuse[id] = &ledgerclient.Error{Op: "post", Status: http.StatusUnprocessableEntity, Code: ledgerclient.CodePeriodClosed}
	for attempt := 1; attempt <= store.MaxGLAttempts; attempt++ {
		g.clock = g.clock.AddDate(0, 0, 2)
		out := g.book(t, http.StatusOK, "")
		if len(out.Failed) != 1 || out.Failed[0].LedgerCode != ledgerclient.CodePeriodClosed || out.Failed[0].Parked != (attempt == store.MaxGLAttempts) {
			t.Fatalf("attempt %d: %+v, want a refusal parked only on attempt %d", attempt, out, store.MaxGLAttempts)
		}
	}
	g.clock = g.clock.AddDate(0, 1, 0)
	if out := g.book(t, http.StatusOK, ""); len(out.Failed)+len(out.Items) != 0 {
		t.Fatalf("parked document offered again: %+v", out)
	}
	delete(g.ledger.refuse, id)
	if out := g.book(t, http.StatusOK, `,"retryParked":true`); len(out.Items) != 1 || out.Items[0].DocumentID != id {
		t.Errorf("retryParked %+v, want the parked document booked", out)
	}
}

func TestGLPostings_CrossTenantDocumentsAreNeverBooked(t *testing.T) {
	g := newGLRig(t, true)
	other := map[string]string{tenant.HeaderTenant: testkit.TenantID(2)}
	foreign := g.invoiceAs(t, other, gridLine{"40.00", "4000", "", ""})
	mine := g.invoice(t, gridLine{"60.00", "4000", "", ""})
	if out := g.book(t, http.StatusOK, ""); len(out.Items) != 1 || out.Items[0].DocumentID != mine {
		t.Fatalf("tenant 1 booked %+v, want only its own invoice %s", out, mine)
	}
	if out := g.bookAs(t, http.StatusOK, "", other); len(out.Items) != 1 || out.Items[0].DocumentID != foreign {
		t.Fatalf("tenant 2 booked %+v, want only its own invoice %s", out, foreign)
	}
}

func TestGLPostings_RequestCannotNameAmountAccountOrLedger(t *testing.T) {
	g := newGLRig(t, true)
	g.invoice(t, gridLine{"100.00", "4100", "", ""})
	rec := do(g.handler, http.MethodPost, "/v1/billing/gl-postings", "", `{"legalEntityId":"`+leID+`","amount":"999999.00","deferredAccount":"9999","ledgerId":"`+ids.New()+`"}`)
	if rec.Code != http.StatusBadRequest || len(g.fake.Entries()) != 0 {
		t.Errorf("status %d with %d entries, want 400 and nothing booked", rec.Code, len(g.fake.Entries()))
	}
}

func TestGLPostings_Guards(t *testing.T) {
	g := newGLRig(t, true)
	body := `{"legalEntityId":"` + leID + `"}`
	if rec := doAs(g.handler, http.MethodPost, "/v1/billing/gl-postings", "", body, "billing.invoice.view,billing.invoice.maintain", nil); rec.Code != http.StatusForbidden {
		t.Errorf("without billing.invoice.post %d, want 403", rec.Code)
	}
	if rec := doAs(g.handler, http.MethodPost, "/v1/billing/gl-postings", "", body, allDuties, map[string]string{tenant.HeaderLegalEntity: ids.New()}); rec.Code != http.StatusForbidden {
		t.Errorf("caller scoped to another entity %d, want 403", rec.Code)
	}
	if rec := do(g.handler, http.MethodPost, "/v1/billing/gl-postings", "", `{"legalEntityId":"x"}`); decodeProblemOf(t, rec).Errors["legalEntityId"] == "" || rec.Code != http.StatusBadRequest {
		t.Errorf("invalid entity %d, want 400 with a field error", rec.Code)
	}
	other := ids.New()
	if rec := doAs(g.handler, http.MethodPost, "/v1/billing/gl-postings", "", `{"legalEntityId":"`+other+`"}`, allDuties, map[string]string{tenant.HeaderLegalEntity: other}); decodeProblemOf(t, rec).Code != CodeLedgerNotMapped || rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("entity without a profile %d, want 422 %s", rec.Code, CodeLedgerNotMapped)
	}
	if p := decodeProblemOf(t, do(newGLRig(t, false).handler, http.MethodPost, "/v1/billing/gl-postings", "", body)); p.Code != CodeLedgerNotConfigured {
		t.Errorf("without a ledger %s, want %s", p.Code, CodeLedgerNotConfigured)
	}
}

func TestGLPostings_ProfileLedgerMustBelongToTheEntity(t *testing.T) {
	g := newGLRig(t, true)
	g.invoice(t, gridLine{"10.00", "4000", "", ""})
	foreign := ledgerclient.NewFake()
	foreign.AddLedger(g.ledgerID, ids.New())
	g.api.ledger = foreign
	rec := do(g.handler, http.MethodPost, "/v1/billing/gl-postings", "", `{"legalEntityId":"`+leID+`"}`)
	if p := decodeProblemOf(t, rec); rec.Code != http.StatusUnprocessableEntity || p.Code != CodeLedgerMismatch || len(foreign.Entries()) != 0 {
		t.Errorf("foreign ledger %d %s with %d entries, want 422 %s and nothing booked", rec.Code, p.Code, len(foreign.Entries()), CodeLedgerMismatch)
	}
	g.api.ledger = ledgerclient.NewFake()
	if rec := do(g.handler, http.MethodPost, "/v1/billing/gl-postings", "", `{"legalEntityId":"`+leID+`"}`); rec.Code != http.StatusBadGateway {
		t.Errorf("unknown ledger %d, want 502", rec.Code)
	}
}

func TestGLEntry_UnbalancedDocumentIsNamedNotPosted(t *testing.T) {
	p := store.GLProfile{LegalEntityID: leID, LedgerID: ids.New(), ReceivableAccount: "1200", TaxAccount: "2300", ChargeAccount: "4800", DeferredRevenueAccounts: []string{"4100"}}
	d := store.GLDocument{ID: ids.New(), Currency: "USD", PostingDate: "2027-03-12", Gross: decimal.NewFromInt(100), Lines: []store.GLLine{{No: 1, Account: "4000", Net: decimal.NewFromInt(90)}}}
	shares, code := glSplit(p, d, nil)
	if code != "" {
		t.Fatalf("split code %q", code)
	}
	if _, code := glEntry(p, d, shares); code != CodeJournalImbalance {
		t.Errorf("code %q, want %s for a gross that is not lines plus charges plus tax", code, CodeJournalImbalance)
	}
	d.Lines[0].Net = decimal.NewFromInt(100)
	shares, _ = glSplit(p, d, nil)
	if e, code := glEntry(p, d, shares); code != "" || len(e.Lines) != 2 {
		t.Errorf("balanced document: code %q lines %d, want an entry of two lines", code, len(e.Lines))
	}
}

func TestParseGLProfiles(t *testing.T) {
	le, ledger := ids.New(), ids.New()
	entry := func(le, ledger, extra string) string {
		return fmt.Sprintf(`[{"legalEntityId":%q,"ledgerId":%q,"receivableAccount":"1200","taxAccount":"2300","chargeAccount":"4800","deferredRevenueAccounts":[%s]}]`, le, ledger, extra)
	}
	got, err := ParseGLProfiles(entry(le, ledger, `"4100"`))
	if err != nil || got[le].LedgerID != ledger || got[le].TaxAccount != "2300" || got[le].DeferredRevenueAccounts[0] != "4100" {
		t.Fatalf("good profile %+v, err %v", got, err)
	}
	twice := strings.TrimSuffix(entry(le, ledger, `"4100"`), "]") + "," + strings.TrimPrefix(entry(le, ids.New(), `"4100"`), "[")
	for name, raw := range map[string]string{
		"same entity twice": twice,
		"empty":             "",
		"not an array":      `{}`,
		"bad entity":        entry("x", ledger, `"4100"`),
		"no revenue":        entry(le, ledger, ``),
		"bad account text":  entry(le, ledger, `"4 100"`),
		"no tax account":    `[{"legalEntityId":"` + le + `","ledgerId":"` + ledger + `","receivableAccount":"1200","chargeAccount":"4800","deferredRevenueAccounts":["4100"]}]`,
	} {
		if _, err := ParseGLProfiles(raw); err == nil {
			t.Errorf("%s accepted, want an error", name)
		}
	}
}

func (g *glRig) parkedCode(t *testing.T, id string) string {
	t.Helper()
	var code *string
	if err := g.admin.QueryRow(context.Background(), `select gl_parked_code from invoice where id = $1::uuid`, id).Scan(&code); err != nil {
		t.Fatal(err)
	}
	if code == nil {
		return ""
	}
	return *code
}

func (g *glRig) creditNote(t *testing.T, original string) string {
	t.Helper()
	var cn struct {
		ID string `json:"id"`
	}
	rec := do(g.handler, http.MethodPost, "/v1/billing/credit-notes", "cn-"+original, creditBody(original, "2027-03-20", ""))
	decode(t, rec, &cn)
	if rec.Code != http.StatusCreated || do(g.handler, http.MethodPost, "/v1/billing/credit-notes/"+cn.ID+":post", "", "").Code != http.StatusOK {
		t.Fatalf("credit note %d %s", rec.Code, rec.Body)
	}
	return cn.ID
}

func TestGLPostings_CreditNoteWaitsForItsOriginalAndIsParkedWithIt(t *testing.T) {
	g := newGLRig(t, true)
	original := g.deferredInvoice(t, "1000.00")
	refused := &ledgerclient.Error{Op: "post", Status: http.StatusUnprocessableEntity, Code: ledgerclient.CodePeriodClosed}
	g.ledger.refuse[original] = refused
	cn := g.creditNote(t, original)

	if out := g.book(t, http.StatusOK, ""); len(out.Items) != 0 || len(out.Failed) != 1 || out.Failed[0].DocumentID != original {
		t.Fatalf("first call %+v, want only the original refused and the credit note held", out)
	}
	if code := g.parkedCode(t, cn); code != "" {
		t.Fatalf("credit note parked %q while its original is only backed off, want held", code)
	}
	for attempt := 2; attempt <= store.MaxGLAttempts; attempt++ {
		g.clock = g.clock.AddDate(0, 0, 2)
		g.book(t, http.StatusOK, "")
	}
	g.book(t, http.StatusOK, "")
	if g.parkedCode(t, original) == "" || g.parkedCode(t, cn) != store.OriginalParkedCode || len(g.fake.Entries()) != 0 {
		t.Fatalf("original parked %q, credit note %q, %d entries; want both parked, the note with %s, and nothing booked", g.parkedCode(t, original), g.parkedCode(t, cn), len(g.fake.Entries()), store.OriginalParkedCode)
	}

	delete(g.ledger.refuse, original)
	if out := g.book(t, http.StatusOK, `,"retryParked":true`); len(out.Items) != 1 || out.Items[0].DocumentID != original {
		t.Fatalf("retryParked %+v, want the original booked and the credit note still waiting for it", out)
	}
	if out := g.book(t, http.StatusOK, ""); len(out.Items) != 1 || out.Items[0].DocumentID != cn {
		t.Fatalf("next call %+v, want the credit note booked after its original", out)
	}
	if b := g.balance(t, "2400"); !b.Net().IsZero() {
		t.Errorf("2400 nets %s, want 0 with the original and its credit note booked", b.Net())
	}
}

func TestGLPostings_LedgerFailureWithoutVerdictIsRecordedAndSkippedNextCall(t *testing.T) {
	g := newGLRig(t, true)
	broken := g.invoice(t, gridLine{"10.00", "4000", "", ""})
	good := g.invoice(t, gridLine{"20.00", "4000", "", ""})
	g.ledger.refuse[broken] = &ledgerclient.Error{Op: "post", Status: http.StatusServiceUnavailable}

	if rec := do(g.handler, http.MethodPost, "/v1/billing/gl-postings", "", `{"legalEntityId":"`+leID+`"}`); rec.Code != http.StatusBadGateway {
		t.Fatalf("first call %d, want 502", rec.Code)
	}
	if out := g.book(t, http.StatusOK, ""); len(out.Items) != 1 || out.Items[0].DocumentID != good {
		t.Fatalf("second call %+v, want the failing document backed off and the good one booked", out)
	}
}

func TestGLPostings_AnOutageWithoutVerdictNeverParksADocumentOrItsCreditNotes(t *testing.T) {
	g := newGLRig(t, true)
	original := g.invoice(t, gridLine{"10.00", "4000", "", ""})
	cn := g.creditNote(t, original)
	g.ledger.refuse[original] = &ledgerclient.Error{Op: "post", Status: http.StatusServiceUnavailable}
	for attempt := 0; attempt < store.MaxGLAttempts+3; attempt++ {
		g.clock = g.clock.AddDate(0, 0, 2)
		g.book(t, http.StatusBadGateway, "")
	}
	if g.parkedCode(t, original) != "" || g.parkedCode(t, cn) != "" {
		t.Fatalf("original parked %q and credit note %q after an outage, want neither parked", g.parkedCode(t, original), g.parkedCode(t, cn))
	}
	delete(g.ledger.refuse, original)
	g.clock = g.clock.AddDate(0, 0, 2)
	if out := g.book(t, http.StatusOK, ""); len(out.Items) != 1 || out.Items[0].DocumentID != original {
		t.Fatalf("after the outage %+v, want the original booked", out)
	}
}

func TestGLPostings_RefusalOfADocumentBookedMeanwhileIsNotAnError(t *testing.T) {
	g := newGLRig(t, true)
	id := g.invoice(t, gridLine{"10.00", "4000", "", ""})
	g.ledger.refuse[id] = &ledgerclient.Error{Op: "post", Status: http.StatusUnprocessableEntity, Code: "LEDGER_CURRENCY_MISMATCH"}
	g.ledger.onFail = func(documentID string) {
		if _, err := g.admin.Exec(context.Background(), `update invoice set journal_posting_id = $2::uuid where id = $1::uuid`, documentID, ids.New()); err != nil {
			t.Error(err)
		}
	}
	if out := g.book(t, http.StatusOK, ""); len(out.Items) != 0 || len(out.Failed) != 0 {
		t.Errorf("%+v, want a document booked by a concurrent call skipped, not reported", out)
	}
}
