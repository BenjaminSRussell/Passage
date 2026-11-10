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
	Halted string        `json:"halted"`
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

func TestGLPostings_BilledBelowTheAllocationBooksAContractAsset(t *testing.T) {
	g := newGLRig(t, true)
	id := g.invoice(t, gridLine{"1450.00", "4100", "", ""})
	order, product := ids.New(), ids.New()
	g.shipLine(t, id, 1, order, product)
	g.allocate(order, revrec.Obligation{ProductID: product, Allocated: decimal.RequireFromString("2250.00"), Method: "RATABLE"})
	free := g.invoice(t, gridLine{"300.00", "4100", "", ""})

	out := g.book(t, http.StatusOK, "")
	if len(out.Items) != 1 || out.Items[0].DocumentID != id || len(out.Failed) != 1 || out.Failed[0].DocumentID != free {
		t.Fatalf("booked %+v, want the order invoice booked and the deferred line without obligation refused", out)
	}
	want := map[string]string{"Dr 1200": "1450.00", "Cr 2400": "2250.00", "Dr 1250": "800.00"}
	if got := lineAmounts(g.entryFor(t, id)); !sameAmounts(got, want) {
		t.Errorf("entry %v, want %v (2,250.00 allocated deferred, 1,450.00 billed, 800.00 contract asset)", got, want)
	}
	if g.parkedCode(t, free) != CodeAllocationMismatch {
		t.Errorf("deferred line with no obligation parked %q, want %s", g.parkedCode(t, free), CodeAllocationMismatch)
	}
}

func TestGLPostings_HardwareShippedFirstBooksTheExcessAsAContractLiability(t *testing.T) {
	g := newGLRig(t, true)
	hardwareInvoice := g.invoice(t, gridLine{"8000.00", "4000", "", ""})
	order, hardware, service := ids.New(), ids.New(), ids.New()
	g.shipLine(t, hardwareInvoice, 1, order, hardware)
	g.allocate(order, revrec.Obligation{ProductID: hardware, Allocated: decimal.RequireFromString("7200.00"), Method: revrec.MethodPoint},
		revrec.Obligation{ProductID: service, Allocated: decimal.RequireFromString("3600.00"), Method: "RATABLE"})
	if out := g.book(t, http.StatusOK, ""); len(out.Items) != 1 || len(out.Failed) != 0 {
		t.Fatalf("hardware first: %+v, want it booked", out)
	}
	want := map[string]string{"Dr 1200": "8000.00", "Cr 4000": "7200.00", "Cr 2400": "800.00"}
	if got := lineAmounts(g.entryFor(t, hardwareInvoice)); !sameAmounts(got, want) {
		t.Fatalf("hardware entry %v, want %v (7,200.00 allocated is revenue, 800.00 billed above it is a contract liability)", got, want)
	}

	serviceInvoice := g.invoice(t, gridLine{"2800.00", "4100", "", ""})
	g.shipLine(t, serviceInvoice, 1, order, service)
	g.book(t, http.StatusOK, "")
	want = map[string]string{"Dr 1200": "2800.00", "Cr 2400": "2800.00"}
	if got := lineAmounts(g.entryFor(t, serviceInvoice)); !sameAmounts(got, want) {
		t.Fatalf("service entry %v, want %v (3,600.00 deferred less the 800.00 contract liability it releases; no contract asset)", got, want)
	}
	if b2400, b1250 := g.balance(t, "2400"), g.balance(t, "1250"); b2400.Credit.Sub(b2400.Debit).StringFixed(2) != "3600.00" || !b1250.Net().IsZero() {
		t.Errorf("2400 holds %s, 1250 nets %s; want the 3,600.00 revrec recognizes and no contract asset", b2400.Credit.Sub(b2400.Debit), b1250.Net())
	}

	cn := g.creditNote(t, hardwareInvoice)
	g.book(t, http.StatusOK, "")
	want = map[string]string{"Cr 1200": "8000.00", "Dr 4000": "7200.00", "Dr 1250": "800.00"}
	if got := lineAmounts(g.entryFor(t, cn)); !sameAmounts(got, want) {
		t.Errorf("hardware credit note %v, want %v (the service left is billed 800.00 below its allocation)", got, want)
	}
	again := g.invoice(t, gridLine{"8000.00", "4000", "", ""})
	g.shipLine(t, again, 1, order, hardware)
	g.book(t, http.StatusOK, "")
	want = map[string]string{"Dr 1200": "8000.00", "Cr 4000": "7200.00", "Cr 1250": "800.00"}
	if got := lineAmounts(g.entryFor(t, again)); !sameAmounts(got, want) {
		t.Errorf("hardware invoiced again after its credit note %v, want %v (the credit note gave the allocation back)", got, want)
	}
}

func TestGLPostings_SecondInvoiceOfAProductConsumesNoAllocation(t *testing.T) {
	g := newGLRig(t, true)
	order, product := ids.New(), ids.New()
	g.allocate(order, revrec.Obligation{ProductID: product, Allocated: decimal.RequireFromString("1000.00"), Method: revrec.MethodPoint})
	first := g.invoice(t, gridLine{"600.00", "4000", "", ""})
	g.shipLine(t, first, 1, order, product)
	g.book(t, http.StatusOK, "")
	second := g.invoice(t, gridLine{"400.00", "4000", "", ""})
	g.shipLine(t, second, 1, order, product)
	g.book(t, http.StatusOK, "")

	if got, want := lineAmounts(g.entryFor(t, first)), map[string]string{"Dr 1200": "600.00", "Cr 4000": "1000.00", "Dr 1250": "400.00"}; !sameAmounts(got, want) {
		t.Errorf("first invoice %v, want %v", got, want)
	}
	if got, want := lineAmounts(g.entryFor(t, second)), map[string]string{"Dr 1200": "400.00", "Cr 1250": "400.00"}; !sameAmounts(got, want) {
		t.Errorf("second invoice %v, want %v (the allocation is consumed: no revenue booked twice)", got, want)
	}
	if b := g.balance(t, "4000"); b.Credit.StringFixed(2) != "1000.00" || !g.balance(t, "1250").Net().IsZero() {
		t.Errorf("4000 credited %s, want the 1,000.00 allocation once and 1250 back at 0", b.Credit)
	}

	g.allocate(order, revrec.Obligation{ProductID: product, Allocated: decimal.RequireFromString("900.00"), Method: revrec.MethodPoint})
	third := g.invoice(t, gridLine{"50.00", "4000", "", ""})
	g.shipLine(t, third, 1, order, product)
	if out := g.book(t, http.StatusOK, ""); len(out.Failed) != 1 || g.parkedCode(t, third) != CodeAllocationMismatch {
		t.Errorf("allocation reduced below what was booked: %+v parked %q, want %s", out, g.parkedCode(t, third), CodeAllocationMismatch)
	}
}

func TestGLPostings_OrderInvoiceWithoutAContractBacksOff(t *testing.T) {
	g := newGLRig(t, true)
	id := g.invoice(t, gridLine{"500.00", "4000", "", ""})
	g.shipLine(t, id, 1, ids.New(), ids.New())
	out := g.book(t, http.StatusOK, "")
	if len(out.Failed) != 1 || out.Failed[0].LedgerCode != CodeAllocationUnavailable || out.Failed[0].Parked || len(g.fake.Entries()) != 0 || out.Halted != "" {
		t.Errorf("order invoice before its contract: %+v with %d entries, want it backed off with %s and never booked at its billed amount", out, len(g.fake.Entries()), CodeAllocationUnavailable)
	}
}

func TestGLPostings_ConcurrentRunIsRefused(t *testing.T) {
	g := newGLRig(t, true)
	g.invoice(t, gridLine{"10.00", "4000", "", ""})
	books := g.api.glBooks.(*store.Postgres)
	unlock, ok, err := books.LockGLRun(testkit.Ctx(testkit.TenantID(1)), leID)
	if err != nil || !ok {
		t.Fatalf("lock: ok %v err %v", ok, err)
	}
	rec := do(g.handler, http.MethodPost, "/v1/billing/gl-postings", "", `{"legalEntityId":"`+leID+`"}`)
	if p := decodeProblemOf(t, rec); rec.Code != http.StatusConflict || p.Code != CodeGLRunInProgress || len(g.fake.Entries()) != 0 {
		t.Fatalf("run while another holds the lock: %d %s, want 409 %s and nothing booked", rec.Code, p.Code, CodeGLRunInProgress)
	}
	unlock()
	if out := g.book(t, http.StatusOK, ""); len(out.Items) != 1 {
		t.Fatalf("after the other run ended %+v, want the invoice booked", out)
	}
}

func TestGLPostings_RevrecOutageEndsTheBatchAfterTheFirstFailure(t *testing.T) {
	g := newGLRig(t, true)
	for i := 0; i < 3; i++ {
		g.deferredInvoice(t, "100.00")
	}
	g.allocs.err = errors.New("status 503")
	out := g.book(t, http.StatusOK, "")
	if g.allocs.reads != 1 || len(out.Failed) != 1 || out.More || out.Halted != CodeAllocationUnavailable {
		t.Errorf("outage: %d reads, %d failed, more %v, halted %q; want one read, one failure, halted %s and more kept for a full page", g.allocs.reads, len(out.Failed), out.More, out.Halted, CodeAllocationUnavailable)
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

func TestGLPostings_RevrecNotConfiguredHoldsEveryOrderInvoice(t *testing.T) {
	g := newGLRig(t, true)
	g.api.allocations = nil
	deferred := g.invoice(t, gridLine{"900.00", "4100", "", ""})
	g.shipLine(t, deferred, 1, ids.New(), ids.New())
	pointInTime := g.invoice(t, gridLine{"400.00", "4000", "", ""})
	g.shipLine(t, pointInTime, 1, ids.New(), ids.New())
	free := g.invoice(t, gridLine{"50.00", "4000", "", ""})
	out := g.book(t, http.StatusOK, "")
	if len(out.Failed) != 2 || out.Failed[0].LedgerCode != CodeAllocationUnavailable || out.Failed[1].LedgerCode != CodeAllocationUnavailable || len(out.Items) != 1 || out.Items[0].DocumentID != free || out.Halted != "" {
		t.Errorf("without revrec %+v, want both order invoices held with %s (none booked at its billed amounts) and the free-text invoice booked", out, CodeAllocationUnavailable)
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

func TestGLPostings_LostAnswerHoldsTheBookingSoTheRetryPostsTheSameEntry(t *testing.T) {
	g := newGLRig(t, true)
	order, product := ids.New(), ids.New()
	g.allocate(order, revrec.Obligation{ProductID: product, Allocated: decimal.RequireFromString("1000.00"), Method: revrec.MethodPoint})
	first := g.invoice(t, gridLine{"1000.00", "4000", "", ""})
	g.shipLine(t, first, 1, order, product)
	g.fake.LoseAnswers(errors.New("timeout"))
	g.book(t, http.StatusBadGateway, "")
	g.fake.LoseAnswers(nil)

	second := g.invoice(t, gridLine{"1000.00", "4000", "", ""})
	g.shipLine(t, second, 1, order, product)
	if out := g.book(t, http.StatusOK, ""); len(out.Items) != 1 || out.Items[0].DocumentID != second {
		t.Fatalf("while the first is backed off %+v, want the second booked", out)
	}
	if got, want := lineAmounts(g.entryFor(t, second)), map[string]string{"Dr 1200": "1000.00", "Cr 2400": "1000.00"}; !sameAmounts(got, want) {
		t.Errorf("second invoice %v, want %v (the held first invoice consumed the allocation)", got, want)
	}
	g.allocs.err = errors.New("status 503")
	g.clock = g.clock.Add(time.Hour)
	if out := g.book(t, http.StatusOK, ""); len(out.Items) != 1 || out.Items[0].DocumentID != first {
		t.Fatalf("retry %+v, want the held first invoice booked without asking revrec again", out)
	}
	if b := g.balance(t, "4000"); len(g.fake.Entries()) != 2 || b.Credit.StringFixed(2) != "1000.00" {
		t.Errorf("%d entries, 4000 credited %s; want 2 entries and the allocation recognized once", len(g.fake.Entries()), b.Credit)
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
	shares, code := glSplit(p, d, nil, nil)
	if code != "" {
		t.Fatalf("split code %q", code)
	}
	if _, code := glEntry(p, d, shares); code != CodeJournalImbalance {
		t.Errorf("code %q, want %s for a gross that is not lines plus charges plus tax", code, CodeJournalImbalance)
	}
	d.Lines[0].Net = decimal.NewFromInt(100)
	shares, _ = glSplit(p, d, nil, nil)
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
	if err != nil || got[le].LedgerID != ledger || got[le].TaxAccount != "2300" || got[le].DeferredRevenueAccounts[0] != "4100" || got[le].ContractAssetAccount != store.DefaultContractAssetGL {
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
		"asset is 2400":     entry(le, ledger, `"4100"],"contractAssetAccount":"2400","x":[`),
		"asset is the AR":   entry(le, ledger, `"4100"],"contractAssetAccount":"1200","x":[`),
		"bad asset text":    entry(le, ledger, `"4100"],"contractAssetAccount":"12 50","x":[`),
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

func (g *glRig) autoBook(t *testing.T) (booked, failed int, err error) {
	t.Helper()
	ctx := tenant.With(testkit.Ctx(testkit.TenantID(1)), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: leID, UserID: "system:billing-autobook"})
	return g.api.AutoBook(ctx, leID)
}

func TestAutoBook_BooksPostedInvoicesOnceWithoutARequest(t *testing.T) {
	g := newGLRig(t, true)
	first := g.invoice(t, gridLine{"10.00", "4000", "", ""})
	second := g.invoice(t, gridLine{"20.00", "4000", "", ""})
	if booked, failed, err := g.autoBook(t); err != nil || booked != 2 || failed != 0 {
		t.Fatalf("auto-book booked %d, failed %d, err %v; want both invoices", booked, failed, err)
	}
	if got, want := lineAmounts(g.entryFor(t, first)), map[string]string{"Dr 1200": "10.00", "Cr 4000": "10.00"}; !sameAmounts(got, want) {
		t.Errorf("first entry %v, want %v", got, want)
	}
	if got, want := lineAmounts(g.entryFor(t, second)), map[string]string{"Dr 1200": "20.00", "Cr 4000": "20.00"}; !sameAmounts(got, want) {
		t.Errorf("second entry %v, want %v", got, want)
	}
	if booked, _, err := g.autoBook(t); err != nil || booked != 0 || len(g.fake.Entries()) != 2 {
		t.Errorf("second tick booked %d (err %v) with %d entries, want nothing more", booked, err, len(g.fake.Entries()))
	}
	if out := g.book(t, http.StatusOK, ""); len(out.Items) != 0 || len(g.fake.Entries()) != 2 {
		t.Errorf("manual run after auto-book booked %d, entries %d; want none more", len(out.Items), len(g.fake.Entries()))
	}
}

func TestAutoBook_LostLedgerAnswerRetriesWithoutPostingTwice(t *testing.T) {
	g := newGLRig(t, true)
	id := g.invoice(t, gridLine{"10.00", "4000", "", ""})
	g.fake.LoseAnswers(errors.New("timeout"))
	if _, _, err := g.autoBook(t); err == nil {
		t.Fatal("a lost ledger answer must surface as an error")
	}
	if len(g.fake.Entries()) != 1 {
		t.Fatalf("%d entries after the lost answer, want the one the ledger accepted", len(g.fake.Entries()))
	}
	g.fake.LoseAnswers(nil)
	if booked, _, err := g.autoBook(t); err != nil || booked != 0 {
		t.Fatalf("tick inside the backoff booked %d (err %v), want it skipped", booked, err)
	}
	g.clock = g.clock.Add(time.Hour)
	if booked, _, err := g.autoBook(t); err != nil || booked != 1 {
		t.Fatalf("retry booked %d (err %v), want the invoice", booked, err)
	}
	if b := g.balance(t, "1200"); len(g.fake.Entries()) != 1 || b.Debit.StringFixed(2) != "10.00" {
		t.Errorf("%d entries, 1200 debited %s; want one entry and the gross debited once", len(g.fake.Entries()), b.Debit)
	}
	if got, want := lineAmounts(g.entryFor(t, id)), map[string]string{"Dr 1200": "10.00", "Cr 4000": "10.00"}; !sameAmounts(got, want) {
		t.Errorf("entry %v, want %v", got, want)
	}
}

func TestAutoBook_SkipsARunAnotherReplicaHolds(t *testing.T) {
	g := newGLRig(t, true)
	g.invoice(t, gridLine{"10.00", "4000", "", ""})
	unlock, ok, err := g.api.glBooks.(*store.Postgres).LockGLRun(testkit.Ctx(testkit.TenantID(1)), leID)
	if err != nil || !ok {
		t.Fatalf("lock: ok %v err %v", ok, err)
	}
	if booked, failed, err := g.autoBook(t); err != nil || booked != 0 || failed != 0 || len(g.fake.Entries()) != 0 {
		t.Errorf("auto-book against a held run: booked %d failed %d err %v entries %d; want a quiet skip", booked, failed, err, len(g.fake.Entries()))
	}
	unlock()
	if booked, _, err := g.autoBook(t); err != nil || booked != 1 {
		t.Errorf("after the other run ended booked %d (err %v), want the invoice", booked, err)
	}
}

func TestAutoBook_NotConfiguredAndUnmappedEntityAreErrors(t *testing.T) {
	if _, _, err := newGLRig(t, false).autoBook(t); err == nil {
		t.Error("auto-book without a ledger must fail")
	}
	g := newGLRig(t, true)
	if _, _, err := g.api.AutoBook(testkit.Ctx(testkit.TenantID(1)), ids.New()); err == nil {
		t.Error("auto-book for an entity without a profile must fail")
	}
}

func TestAutoBook_ContinuesPastAPageAndCountsWhatAFailedRunBooked(t *testing.T) {
	g := newGLRig(t, true)
	g.api.glBatch = 1
	g.invoice(t, gridLine{"10.00", "4000", "", ""})
	g.invoice(t, gridLine{"20.00", "4000", "", ""})
	if booked, _, err := g.autoBook(t); err != nil || booked != 2 {
		t.Fatalf("booked %d (err %v) with a page of 1, want both documents in one call", booked, err)
	}
	g.api.glBatch = defaultGLBatch
	g.invoice(t, gridLine{"30.00", "4000", "", ""})
	g.invoice(t, gridLine{"40.00", "4000", "", ""})
	third := g.invoice(t, gridLine{"50.00", "4000", "", ""})
	g.ledger.refuse[third] = &ledgerclient.Error{Op: "post", Status: http.StatusServiceUnavailable}
	if booked, _, err := g.autoBook(t); err == nil || booked != 2 {
		t.Errorf("outage midway: booked %d, err %v; want the 2 already booked reported with the error", booked, err)
	}
}

func TestParseGLProfiles_TenantIDIsOptionalAndMustBeAUUID(t *testing.T) {
	entry := func(tenantField string) string {
		return `[{"legalEntityId":"` + leID + `","ledgerId":"` + ids.New() + `","receivableAccount":"1200","taxAccount":"2300","chargeAccount":"4800","deferredRevenueAccounts":["4100"]` + tenantField + `}]`
	}
	if got, err := ParseGLProfiles(entry(``)); err != nil || got[leID].TenantID != "" {
		t.Errorf("no tenantId: %+v %v", got, err)
	}
	if got, err := ParseGLProfiles(entry(`,"tenantId":"` + testkit.TenantID(1) + `"`)); err != nil || got[leID].TenantID != testkit.TenantID(1) {
		t.Errorf("tenantId: %+v %v", got, err)
	}
	if _, err := ParseGLProfiles(entry(`,"tenantId":"acme"`)); err == nil {
		t.Error("a tenantId that is not a UUID was accepted")
	}
}
