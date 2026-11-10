package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/billing/internal/revrec"
	"github.com/BenjaminSRussell/passage/services/billing/internal/store"
)

const (
	CodeLedgerNotConfigured = "BIL-0200"
	CodeLedgerUnavailable   = "BIL-0201"
	CodeLedgerNotMapped     = "BIL-0202"
	CodeLedgerMismatch      = "BIL-0203"
	CodeJournalImbalance    = "BIL-0204"
	CodeLedgerRefused       = "BIL-0205"

	CodeAllocationUnavailable = "BIL-0207"
	CodeAllocationMismatch    = "BIL-0208"
	CodeGLRunInProgress       = "BIL-0209"

	defaultGLBatch = 200
	autoBookPages  = 10
)

type GLBooks interface {
	DueGLDocuments(ctx context.Context, legalEntityID string, now time.Time, limit int) ([]store.GLDocument, bool, error)
	MarkGLBooked(ctx context.Context, id, entryID string, shares map[int]store.GLShare) (bool, error)
	RecordGLRefusal(ctx context.Context, id, code string, now time.Time, outcome store.GLOutcome) (bool, error)
	UnparkGL(ctx context.Context, legalEntityID string) (int64, error)
	GLContractBalances(ctx context.Context, legalEntityID string, orderIDs []string) (map[string]store.ContractBalance, error)
	HoldGLBooking(ctx context.Context, id string, held store.GLHeld) error
	LockGLRun(ctx context.Context, legalEntityID string) (unlock func(), ok bool, err error)
}

func (a *API) WithLedger(ledger ledgerclient.Ledger, books GLBooks, profiles map[string]store.GLProfile) *API {
	a.ledger, a.glBooks, a.profiles = ledger, books, profiles
	return a
}

func (a *API) WithAllocations(reader revrec.Reader) *API {
	a.allocations = reader
	return a
}

// ParseGLProfiles reads BILLING_GL_PROFILES: a JSON array holding, per legal entity, the ledger and the accounts of the invoice journal
// and, for the auto-book worker, the optional tenantId the entity belongs to.
// The deferred revenue and contract liability account is 2400, the account revrec recognizes from; the contract asset account
// is contractAssetAccount, 1250 when omitted, and must differ from 2400 and the receivable.
func ParseGLProfiles(raw string) (map[string]store.GLProfile, error) {
	var list []store.GLProfile
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return nil, fmt.Errorf("BILLING_GL_PROFILES must be a JSON array of profiles: %w", err)
	}
	out := map[string]store.GLProfile{}
	for _, p := range list {
		le, leOK := ids.Canonical(p.LegalEntityID)
		ledger, ledgerOK := ids.Canonical(p.LedgerID)
		if p.ContractAssetAccount == "" {
			p.ContractAssetAccount = store.DefaultContractAssetGL
		}
		accounts := append([]string{p.ReceivableAccount, p.TaxAccount, p.ChargeAccount, p.ContractAssetAccount}, p.DeferredRevenueAccounts...)
		if !leOK || !ledgerOK || len(p.DeferredRevenueAccounts) == 0 || slices.ContainsFunc(accounts, func(a string) bool { return !accountText.MatchString(a) }) {
			return nil, fmt.Errorf("BILLING_GL_PROFILES entry for %q needs legalEntityId and ledgerId UUIDs, receivableAccount, taxAccount, chargeAccount and deferredRevenueAccounts", p.LegalEntityID)
		}
		if slices.Contains(append([]string{store.DeferredGL, p.ReceivableAccount, p.TaxAccount, p.ChargeAccount}, p.DeferredRevenueAccounts...), p.ContractAssetAccount) {
			return nil, fmt.Errorf("BILLING_GL_PROFILES entry for %s: contractAssetAccount %s must be an account of its own", le, p.ContractAssetAccount)
		}
		if p.TenantID != "" {
			tid, tidOK := ids.Canonical(p.TenantID)
			if !tidOK {
				return nil, fmt.Errorf("BILLING_GL_PROFILES entry for %s: tenantId must be a UUID", le)
			}
			p.TenantID = tid
		}
		p.LegalEntityID, p.LedgerID = le, ledger
		if _, dup := out[le]; dup {
			return nil, fmt.Errorf("BILLING_GL_PROFILES names legal entity %s twice", le)
		}
		out[le] = p
	}
	return out, nil
}

type glPostingRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	RetryParked   bool   `json:"retryParked,omitempty"`
}

type glBookedOut struct {
	DocumentID     string `json:"documentId"`
	Number         string `json:"number"`
	Kind           string `json:"kind"`
	Currency       string `json:"currency"`
	PostingDate    string `json:"postingDate"`
	Amount         string `json:"amount"`
	JournalEntryID string `json:"journalEntryId"`
}

type glFailedOut struct {
	DocumentID string `json:"documentId"`
	Number     string `json:"number"`
	LedgerCode string `json:"ledgerCode"`
	Parked     bool   `json:"parked"`
}

func onDeferredAccount(p store.GLProfile, l store.GLLine) bool {
	return slices.Contains(p.DeferredRevenueAccounts, l.Account)
}

var errAllocationsNotConfigured = errors.New("revrec is not configured for this service")

// allocationsFor reads the revrec allocation of every order an invoice bills. Every order is expected to have one revenue
// contract (revrec creates one per order), so an order line is never booked at its billed amount: without revrec, without a
// contract yet or with several, the invoice backs off (BIL-0207) until revrec answers with the one contract.
func (a *API) allocationsFor(ctx context.Context, le string, d store.GLDocument) (map[string]revrec.Allocation, error) {
	if d.CreditNote {
		return nil, nil
	}
	out := map[string]revrec.Allocation{}
	for _, l := range d.Lines {
		if _, done := out[l.OrderID]; done || l.OrderID == "" {
			continue
		}
		if a.allocations == nil {
			return nil, errAllocationsNotConfigured
		}
		alloc, err := a.allocations.Allocation(ctx, le, l.OrderID)
		if err != nil {
			return nil, err
		}
		out[l.OrderID] = alloc
	}
	return out, nil
}

func ordersOf(d store.GLDocument) []string {
	var out []string
	for _, l := range d.Lines {
		if l.OrderID != "" && !slices.Contains(out, l.OrderID) {
			out = append(out, l.OrderID)
		}
	}
	return out
}

type orderProduct struct{ order, product string }

func portion(whole, part, of decimal.Decimal) decimal.Decimal {
	if part.Equal(of) {
		return whole
	}
	return whole.Mul(part).DivRound(of, 12)
}

func apportion(exact []decimal.Decimal, places int32) []decimal.Decimal {
	total := decimal.Zero
	out := make([]decimal.Decimal, len(exact))
	for i, e := range exact {
		total, out[i] = total.Add(e), e.RoundFloor(places)
	}
	left := total.Round(places)
	for _, o := range out {
		left = left.Sub(o)
	}
	unit := decimal.New(1, -places)
	order := make([]int, len(exact))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int { return exact[b].Sub(out[b]).Cmp(exact[a].Sub(out[a])) })
	for _, i := range order {
		if !left.IsPositive() {
			break
		}
		out[i], left = out[i].Add(unit), left.Sub(unit)
	}
	return out
}

// glBooking is how a document books: the allocated amount and its deferred part per line, and the change it makes to the
// contract liability (2400, a credit when positive) and to the contract asset (a debit when positive).
type glBooking struct {
	shares           map[int]store.GLShare
	liability, asset decimal.Decimal
}

// glSplit books the lines of a product its remaining allocation, the allocation of the product less what earlier documents of the
// contract consumed (balances): the first invoice of a product takes it all, a later one only what credit notes gave back. The
// difference between the billed nets and the allocated amounts moves the contract position of the order (ASC 606-10-45, IFRS
// 15.105): billed above allocated is a contract liability in 2400, below a contract asset, netted per contract so the asset and
// the liability of one contract never both hold a balance.
func glSplit(p store.GLProfile, d store.GLDocument, allocs map[string]revrec.Allocation, balances map[string]store.ContractBalance) (glBooking, string) {
	shares, code := glShares(p, d, allocs, balances)
	if code != "" {
		return glBooking{}, code
	}
	moved := map[string]decimal.Decimal{}
	for _, l := range d.Lines {
		if l.OrderID == "" {
			continue
		}
		diff := l.Net.Sub(shares[l.No].Allocated)
		if d.CreditNote {
			diff = diff.Neg()
		}
		moved[l.OrderID] = moved[l.OrderID].Add(diff)
	}
	b := glBooking{shares: shares}
	for order, diff := range moved {
		before := balances[order].Position
		after := before.Add(diff)
		b.liability = b.liability.Add(decimal.Max(after, decimal.Zero).Sub(decimal.Max(before, decimal.Zero)))
		b.asset = b.asset.Add(decimal.Max(after.Neg(), decimal.Zero).Sub(decimal.Max(before.Neg(), decimal.Zero)))
	}
	return b, ""
}

func glShares(p store.GLProfile, d store.GLDocument, allocs map[string]revrec.Allocation, balances map[string]store.ContractBalance) (map[int]store.GLShare, string) {
	places := money.MinorUnits(d.Currency)
	shares := make(map[int]store.GLShare, len(d.Lines))
	billed := map[orderProduct][]store.GLLine{}
	var mirrored []store.GLLine
	for _, l := range d.Lines {
		alloc, covered := allocs[l.OrderID]
		_, inContract := allocatedFor(alloc, l.ProductID)
		switch {
		case d.CreditNote && l.Original != nil:
			o := l.Original
			if o.Net.IsZero() {
				shares[l.No] = store.GLShare{}
			} else {
				mirrored = append(mirrored, l)
			}
		case !d.CreditNote && covered && l.ProductID != "" && inContract:
			if alloc.Currency != d.Currency {
				return nil, CodeAllocationMismatch
			}
			key := orderProduct{l.OrderID, l.ProductID}
			billed[key] = append(billed[key], l)
		case onDeferredAccount(p, l):
			return nil, CodeAllocationMismatch
		default:
			shares[l.No] = store.GLShare{Allocated: l.Net}
		}
	}
	allocatedParts, deferredParts := make([]decimal.Decimal, len(mirrored)), make([]decimal.Decimal, len(mirrored))
	for i, l := range mirrored {
		allocatedParts[i], deferredParts[i] = portion(l.Net, l.Original.Share.Allocated, l.Original.Net), portion(l.Net, l.Original.Share.Deferred, l.Original.Net)
	}
	allocatedShares, deferredShares := apportion(allocatedParts, places), apportion(deferredParts, places)
	for i, l := range mirrored {
		shares[l.No] = store.GLShare{Allocated: allocatedShares[i], Deferred: deferredShares[i]}
	}
	for key, lines := range billed {
		total := decimal.Zero
		for _, l := range lines {
			total = total.Add(l.Net)
		}
		if !total.IsPositive() {
			return nil, CodeAllocationMismatch
		}
		allocated, _ := allocatedFor(allocs[key.order], key.product)
		deferred := decimal.Zero
		for _, o := range allocs[key.order].Obligations {
			if o.ProductID == key.product && o.Method != revrec.MethodPoint {
				deferred = deferred.Add(o.Allocated)
			}
		}
		consumed := balances[key.order].Consumed[key.product]
		allocated, deferred = allocated.Sub(consumed.Allocated), deferred.Sub(consumed.Deferred)
		if allocated.IsNegative() || deferred.IsNegative() || deferred.GreaterThan(allocated) {
			// Earlier documents booked more of the product than revrec now allocates (a reduced contract): never book below zero.
			return nil, CodeAllocationMismatch
		}
		allocatedParts, deferredParts := make([]decimal.Decimal, len(lines)), make([]decimal.Decimal, len(lines))
		for i, l := range lines {
			allocatedParts[i], deferredParts[i] = portion(allocated, l.Net, total), portion(deferred, l.Net, total)
		}
		allocatedShares, deferredShares := apportion(allocatedParts, places), apportion(deferredParts, places)
		for i, l := range lines {
			shares[l.No] = store.GLShare{Allocated: allocatedShares[i], Deferred: deferredShares[i]}
		}
	}
	return shares, ""
}

func allocatedFor(alloc revrec.Allocation, productID string) (decimal.Decimal, bool) {
	total, found := decimal.Zero, false
	for _, o := range alloc.Obligations {
		if o.ProductID == productID {
			total, found = total.Add(o.Allocated), true
		}
	}
	return total, found
}

func contractAssetAccount(p store.GLProfile) string {
	if p.ContractAssetAccount != "" {
		return p.ContractAssetAccount
	}
	return store.DefaultContractAssetGL
}

func glEntry(p store.GLProfile, d store.GLDocument, b glBooking) (ledgerclient.Entry, string) {
	kind, key, source := "invoice", "billing-invoice-", "billing_invoice"
	if d.CreditNote {
		kind, key, source = "credit_note", "billing-credit-note-", "billing_credit_note"
	}
	e := ledgerclient.Entry{LegalEntityID: p.LegalEntityID, LedgerID: p.LedgerID, DocumentID: d.ID, PostingDate: d.PostingDate, Currency: d.Currency,
		IdempotencyKey: key + d.ID, SourceType: source}
	text := strings.ReplaceAll(kind, "_", " ") + " " + d.Number
	e.Lines = append(e.Lines, ledgerclient.Line{Account: p.ReceivableAccount, Credit: d.CreditNote, Amount: d.Gross, Text: text})
	// credit holds the signed amount per account, positive for a credit; a document books revenue, charges and tax on its own
	// side only (an invoice credits them, a credit note debits them), the contract position moves either way.
	side := decimal.NewFromInt(1)
	if d.CreditNote {
		side = side.Neg()
	}
	credit, accounts, wrongSide := map[string]decimal.Decimal{}, []string{}, false
	add := func(account string, amount decimal.Decimal) {
		if amount.IsZero() {
			return
		}
		if _, seen := credit[account]; !seen {
			accounts = append(accounts, account)
		}
		credit[account] = credit[account].Add(amount)
	}
	addOwnSide := func(account string, amount decimal.Decimal) {
		wrongSide = wrongSide || amount.IsNegative()
		add(account, amount.Mul(side))
	}
	for _, l := range d.Lines {
		s := b.shares[l.No]
		addOwnSide(l.Account, s.Allocated.Sub(s.Deferred))
		addOwnSide(store.DeferredGL, s.Deferred)
	}
	add(store.DeferredGL, b.liability)
	add(contractAssetAccount(p), b.asset.Neg())
	addOwnSide(p.ChargeAccount, d.Charges)
	addOwnSide(p.TaxAccount, d.Tax)
	total := decimal.Zero
	for _, account := range accounts {
		amount := credit[account]
		total = total.Add(amount)
		if !amount.IsZero() {
			e.Lines = append(e.Lines, ledgerclient.Line{Account: account, Credit: amount.IsPositive(), Amount: amount.Abs(), Text: text})
		}
	}
	if wrongSide || !total.Equal(d.Gross.Mul(side)) {
		return e, CodeJournalImbalance
	}
	return e, ""
}

// postGL books the invoice journal (FR-29.3) of every posted invoice and credit note not yet booked: Dr receivable for the billed
// gross, Cr revenue per line account and Cr 2400 for the deferred part of the revrec allocation of each order line (glSplit), the
// contract position change (Cr / Dr 2400 contract liability, Dr / Cr contract asset), Cr charges, Cr tax; a credit note books the
// mirror. One run per entity at a time (BIL-0209), so two runs never consume the same allocation. Ledger, accounts and amounts come from
// the profile, the stored document and revrec, never the request, and the ledger key is derived from the document id, so a retry
// after a lost answer returns the same entry. A document the ledger refuses is recorded with a backed-off next attempt and parked
// after store.MaxGLAttempts refusals, so it cannot hold up the documents behind it. A document whose allocation revrec cannot give
// is backed off without being booked and without counting toward parking; a call the ledger fails without a verdict is recorded the
// same way and ends the run, so one document that always fails is skipped by the next call. A credit note waits for the entry of
// its original (store.DueGLDocuments).
func (a *API) postGL(w http.ResponseWriter, r *http.Request) {
	if !a.glConfigured() {
		httpx.Fail(w, r, errGLNotConfigured())
		return
	}
	var req glPostingRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	le, valid := ids.Canonical(req.LegalEntityID)
	if !valid {
		writeFields(w, r, map[string]string{"legalEntityId": "must be a UUID"})
		return
	}
	if p, _ := tenant.FromContext(r.Context()); !tenant.InLegalEntityScope(p, le) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "the caller is scoped to another legal entity"))
		return
	}
	run, err := a.runGL(r.Context(), le, req.RetryParked)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out := map[string]any{"items": run.items, "failed": run.failed, "more": run.more}
	if run.halted != "" {
		out["halted"] = run.halted
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) glConfigured() bool { return a.ledger != nil && a.glBooks != nil }

func errGLNotConfigured() error {
	return httpx.NewProblem(http.StatusServiceUnavailable, CodeLedgerNotConfigured, "the general ledger is not configured for this service")
}

type glRun struct {
	items  []glBookedOut
	failed []glFailedOut
	more   bool
	halted string
}

// AutoBook is the posting run the auto-book worker drives for one legal entity under a principal scoped to it: the same run as
// POST /v1/billing/gl-postings, with the same lock, backoff and ledger key per document, so a retry never books a document twice.
// It continues while due documents remain beyond a page, at most autoBookPages pages per call, and counts what a run that
// failed midway had already booked.
func (a *API) AutoBook(ctx context.Context, legalEntityID string) (booked, failed int, err error) {
	if !a.glConfigured() {
		return 0, 0, errGLNotConfigured()
	}
	for range autoBookPages {
		run, rerr := a.runGL(ctx, legalEntityID, false)
		booked, failed = booked+len(run.items), failed+len(run.failed)
		var problem *httpx.Problem
		if errors.As(rerr, &problem) && problem.Code == CodeGLRunInProgress {
			return booked, failed, nil
		}
		if rerr != nil || !run.more || run.halted != "" {
			return booked, failed, rerr
		}
	}
	return booked, failed, nil
}

func (a *API) runGL(ctx context.Context, le string, retryParked bool) (glRun, error) {
	profile, ok := a.profiles[le]
	if !ok {
		return glRun{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeLedgerNotMapped, "no GL posting profile is configured for this legal entity")
	}
	cid := middleware.GetReqID(ctx)
	owner, err := a.ledger.LegalEntity(ctx, le, profile.LedgerID)
	if err != nil {
		slog.ErrorContext(ctx, "general ledger lookup failed", "op", "ledger", "ledgerCode", ledgerclient.CodeOf(err), "err", err, "correlationId", cid)
		return glRun{}, httpx.NewProblem(http.StatusBadGateway, CodeLedgerUnavailable, "the general ledger did not confirm the configured ledger")
	}
	if owner != le {
		return glRun{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeLedgerMismatch, "the configured ledger belongs to another legal entity")
	}
	unlock, locked, err := a.glBooks.LockGLRun(ctx, le)
	if err != nil {
		return glRun{}, err
	}
	if !locked {
		return glRun{}, httpx.NewProblem(http.StatusConflict, CodeGLRunInProgress, "a journal posting run for this legal entity is in progress; retry when it ends")
	}
	defer unlock()
	if retryParked {
		if n, err := a.glBooks.UnparkGL(ctx, le); err != nil {
			return glRun{}, err
		} else if n > 0 {
			slog.InfoContext(ctx, "parked GL postings offered again", "legalEntityId", le, "count", n, "correlationId", cid)
		}
	}
	now := a.now().UTC()
	pending, more, err := a.glBooks.DueGLDocuments(ctx, le, now, a.glBatch)
	if err != nil {
		return glRun{}, err
	}
	items, failed, halted := make([]glBookedOut, 0, len(pending)), []glFailedOut{}, ""
	for _, d := range pending {
		err = nil
		var booking glBooking
		var imbalance string
		if d.Held != nil {
			// An earlier attempt reached the ledger without a verdict: post the same entry again.
			booking = glBooking{shares: d.Held.Shares, liability: d.Held.Contract.Liability, asset: d.Held.Contract.Asset}
		} else {
			allocs, aerr := a.allocationsFor(ctx, le, d)
			if aerr != nil {
				slog.ErrorContext(ctx, "revrec allocation not available; invoice not booked", "documentId", d.ID, "err", aerr, "correlationId", cid)
				if _, rerr := a.glBooks.RecordGLRefusal(ctx, d.ID, CodeAllocationUnavailable, now, store.GLNoVerdict); rerr != nil && !errors.Is(rerr, store.ErrGLAlreadyBooked) {
					return glRun{}, rerr
				}
				failed = append(failed, glFailedOut{DocumentID: d.ID, Number: d.Number, LedgerCode: CodeAllocationUnavailable})
				if !errors.Is(aerr, revrec.ErrNoContract) && !errors.Is(aerr, revrec.ErrAmbiguous) && !errors.Is(aerr, errAllocationsNotConfigured) {
					halted = CodeAllocationUnavailable
					break
				}
				continue
			}
			balances, berr := a.glBooks.GLContractBalances(ctx, le, ordersOf(d))
			if berr != nil {
				return glRun{}, berr
			}
			booking, imbalance = glSplit(profile, d, allocs, balances)
		}
		shares := booking.shares
		var entry ledgerclient.Entry
		if imbalance == "" {
			entry, imbalance = glEntry(profile, d, booking)
		}
		if imbalance == "" && d.Held == nil {
			held := store.GLHeld{Shares: shares, Contract: store.GLContractMove{Liability: booking.liability, Asset: booking.asset}}
			if herr := a.glBooks.HoldGLBooking(ctx, d.ID, held); herr != nil {
				return glRun{}, herr
			}
		}
		var entryID string
		if imbalance == "" {
			entryID, err = a.ledger.Post(ctx, entry)
		}
		if imbalance != "" || ledgerclient.Rejected(err) {
			code, outcome := imbalance, store.GLPark
			if code == "" {
				code, outcome = ledgerclient.CodeOf(err), store.GLRefused
			}
			if code == "" {
				code = CodeLedgerRefused
			}
			parked, rerr := a.glBooks.RecordGLRefusal(ctx, d.ID, code, now, outcome)
			if errors.Is(rerr, store.ErrGLAlreadyBooked) {
				continue
			}
			if rerr != nil {
				return glRun{}, rerr
			}
			slog.WarnContext(ctx, "general ledger refused an invoice journal", "documentId", d.ID, "ledgerCode", code, "parked", parked, "err", err, "correlationId", cid)
			failed = append(failed, glFailedOut{DocumentID: d.ID, Number: d.Number, LedgerCode: code, Parked: parked})
			continue
		}
		if err != nil {
			slog.ErrorContext(ctx, "general ledger call failed", "op", "post", "documentId", d.ID, "ledgerCode", ledgerclient.CodeOf(err), "err", err, "correlationId", cid)
			if _, rerr := a.glBooks.RecordGLRefusal(ctx, d.ID, CodeLedgerUnavailable, now, store.GLNoVerdict); rerr != nil && !errors.Is(rerr, store.ErrGLAlreadyBooked) {
				slog.ErrorContext(ctx, "general ledger failure not recorded", "documentId", d.ID, "err", rerr, "correlationId", cid)
			}
			p := httpx.NewProblem(http.StatusBadGateway, CodeLedgerUnavailable, "the general ledger did not answer")
			p.Errors = map[string]string{"booked": strings.Join(bookedIDs(items), ",")}
			return glRun{items: items, failed: failed}, p
		}
		if newly, err := a.glBooks.MarkGLBooked(ctx, d.ID, entryID, shares); err != nil {
			return glRun{}, err
		} else if newly {
			a.glPostings.Add(1)
		}
		kind := "invoice"
		if d.CreditNote {
			kind = "credit_note"
		}
		items = append(items, glBookedOut{DocumentID: d.ID, Number: d.Number, Kind: kind, Currency: d.Currency, PostingDate: d.PostingDate,
			Amount: d.Gross.StringFixed(money.MinorUnits(d.Currency)), JournalEntryID: entryID})
	}
	// more: due documents were left beyond this page; halted: the run stopped early (revrec did not answer) and the rest of
	// the page was not attempted.
	return glRun{items: items, failed: failed, more: more, halted: halted}, nil
}

func bookedIDs(items []glBookedOut) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.DocumentID
	}
	return out
}
