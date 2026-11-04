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

	defaultGLBatch = 200
)

type GLBooks interface {
	DueGLDocuments(ctx context.Context, legalEntityID string, now time.Time, limit int) ([]store.GLDocument, bool, error)
	MarkGLBooked(ctx context.Context, id, entryID string, shares map[int]store.GLShare) (bool, error)
	RecordGLRefusal(ctx context.Context, id, code string, now time.Time, outcome store.GLOutcome) (bool, error)
	UnparkGL(ctx context.Context, legalEntityID string) (int64, error)
}

func (a *API) WithLedger(ledger ledgerclient.Ledger, books GLBooks, profiles map[string]store.GLProfile) *API {
	a.ledger, a.glBooks, a.profiles = ledger, books, profiles
	return a
}

func (a *API) WithAllocations(reader revrec.Reader) *API {
	a.allocations = reader
	return a
}

// ParseGLProfiles reads BILLING_GL_PROFILES: a JSON array holding, per legal entity, the ledger and the accounts of the invoice journal.
// The deferred revenue account is 2400, the account revrec recognizes from.
func ParseGLProfiles(raw string) (map[string]store.GLProfile, error) {
	var list []store.GLProfile
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return nil, fmt.Errorf("BILLING_GL_PROFILES must be a JSON array of profiles: %w", err)
	}
	out := map[string]store.GLProfile{}
	for _, p := range list {
		le, leOK := ids.Canonical(p.LegalEntityID)
		ledger, ledgerOK := ids.Canonical(p.LedgerID)
		accounts := append([]string{p.ReceivableAccount, p.TaxAccount, p.ChargeAccount}, p.DeferredRevenueAccounts...)
		if !leOK || !ledgerOK || len(p.DeferredRevenueAccounts) == 0 || slices.ContainsFunc(accounts, func(a string) bool { return !accountText.MatchString(a) }) {
			return nil, fmt.Errorf("BILLING_GL_PROFILES entry for %q needs legalEntityId and ledgerId UUIDs, receivableAccount, taxAccount, chargeAccount and deferredRevenueAccounts", p.LegalEntityID)
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

func (a *API) allocationsFor(ctx context.Context, le string, p store.GLProfile, d store.GLDocument) (map[string]revrec.Allocation, error) {
	if d.CreditNote {
		return nil, nil
	}
	hasDeferred := slices.ContainsFunc(d.Lines, func(l store.GLLine) bool { return onDeferredAccount(p, l) })
	if a.allocations == nil && !hasDeferred {
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
		if errors.Is(err, revrec.ErrNoContract) && !slices.ContainsFunc(d.Lines, func(o store.GLLine) bool { return o.OrderID == l.OrderID && onDeferredAccount(p, o) }) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out[l.OrderID] = alloc
	}
	return out, nil
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

func glSplit(p store.GLProfile, d store.GLDocument, allocs map[string]revrec.Allocation) (map[int]store.GLShare, string) {
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
		allocatedParts, deferredParts := make([]decimal.Decimal, len(lines)), make([]decimal.Decimal, len(lines))
		for i, l := range lines {
			allocatedParts[i], deferredParts[i] = portion(allocated, l.Net, total), portion(deferred, l.Net, total)
		}
		allocatedShares, deferredShares := apportion(allocatedParts, places), apportion(deferredParts, places)
		for i, l := range lines {
			shares[l.No] = store.GLShare{Allocated: allocatedShares[i], Deferred: deferredShares[i]}
		}
	}
	allocatedTotal, billedTotal := decimal.Zero, decimal.Zero
	for _, l := range d.Lines {
		allocatedTotal, billedTotal = allocatedTotal.Add(shares[l.No].Allocated), billedTotal.Add(l.Net)
	}
	if !allocatedTotal.Equal(billedTotal) {
		return nil, CodeAllocationMismatch
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

func glEntry(p store.GLProfile, d store.GLDocument, shares map[int]store.GLShare) (ledgerclient.Entry, string) {
	kind, key, source := "invoice", "billing-invoice-", "billing_invoice"
	if d.CreditNote {
		kind, key, source = "credit_note", "billing-credit-note-", "billing_credit_note"
	}
	e := ledgerclient.Entry{LegalEntityID: p.LegalEntityID, LedgerID: p.LedgerID, DocumentID: d.ID, PostingDate: d.PostingDate, Currency: d.Currency,
		IdempotencyKey: key + d.ID, SourceType: source}
	text := strings.ReplaceAll(kind, "_", " ") + " " + d.Number
	e.Lines = append(e.Lines, ledgerclient.Line{Account: p.ReceivableAccount, Credit: d.CreditNote, Amount: d.Gross, Text: text})
	credits := decimal.Zero
	add := func(account string, amount decimal.Decimal) {
		if !amount.IsPositive() {
			return
		}
		credits = credits.Add(amount)
		if i := slices.IndexFunc(e.Lines[1:], func(l ledgerclient.Line) bool { return l.Account == account }); i >= 0 {
			e.Lines[i+1].Amount = e.Lines[i+1].Amount.Add(amount)
			return
		}
		e.Lines = append(e.Lines, ledgerclient.Line{Account: account, Credit: !d.CreditNote, Amount: amount, Text: text})
	}
	for _, l := range d.Lines {
		s := shares[l.No]
		add(l.Account, s.Allocated.Sub(s.Deferred))
		add(store.DeferredGL, s.Deferred)
	}
	add(p.ChargeAccount, d.Charges)
	add(p.TaxAccount, d.Tax)
	if !credits.Equal(d.Gross) {
		return e, CodeJournalImbalance
	}
	return e, ""
}

// postGL books the invoice journal (FR-29.3) of every posted invoice and credit note not yet booked: Dr receivable for the billed
// gross, Cr revenue per line account and Cr 2400 for the deferred part (glSplit: the revrec allocation, not the invoice price, when
// a line is on a deferred revenue account), Cr charges, Cr tax; a credit note books the mirror. Ledger, accounts and amounts come from
// the profile, the stored document and revrec, never the request, and the ledger key is derived from the document id, so a retry
// after a lost answer returns the same entry. A document the ledger refuses is recorded with a backed-off next attempt and parked
// after store.MaxGLAttempts refusals, so it cannot hold up the documents behind it. A document whose allocation revrec cannot give
// is backed off without being booked and without counting toward parking; a call the ledger fails without a verdict is recorded the
// same way and ends the run, so one document that always fails is skipped by the next call. A credit note waits for the entry of
// its original (store.DueGLDocuments).
func (a *API) postGL(w http.ResponseWriter, r *http.Request) {
	if a.ledger == nil || a.glBooks == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, CodeLedgerNotConfigured, "the general ledger is not configured for this service"))
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
	profile, ok := a.profiles[le]
	if !ok {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, CodeLedgerNotMapped, "no GL posting profile is configured for this legal entity"))
		return
	}
	ctx, cid := r.Context(), middleware.GetReqID(r.Context())
	owner, err := a.ledger.LegalEntity(ctx, le, profile.LedgerID)
	if err != nil {
		slog.ErrorContext(ctx, "general ledger lookup failed", "op", "ledger", "ledgerCode", ledgerclient.CodeOf(err), "err", err, "correlationId", cid)
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadGateway, CodeLedgerUnavailable, "the general ledger did not confirm the configured ledger"))
		return
	}
	if owner != le {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, CodeLedgerMismatch, "the configured ledger belongs to another legal entity"))
		return
	}
	if req.RetryParked {
		if n, err := a.glBooks.UnparkGL(ctx, le); err != nil {
			httpx.Fail(w, r, err)
			return
		} else if n > 0 {
			slog.InfoContext(ctx, "parked GL postings offered again", "legalEntityId", le, "count", n, "correlationId", cid)
		}
	}
	now := a.now().UTC()
	pending, more, err := a.glBooks.DueGLDocuments(ctx, le, now, a.glBatch)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	items, failed := make([]glBookedOut, 0, len(pending)), []glFailedOut{}
	for _, d := range pending {
		err = nil
		allocs, aerr := a.allocationsFor(ctx, le, profile, d)
		if aerr != nil {
			slog.ErrorContext(ctx, "revrec allocation not available; invoice not booked", "documentId", d.ID, "err", aerr, "correlationId", cid)
			if _, rerr := a.glBooks.RecordGLRefusal(ctx, d.ID, CodeAllocationUnavailable, now, store.GLNoVerdict); rerr != nil && !errors.Is(rerr, store.ErrGLAlreadyBooked) {
				httpx.Fail(w, r, rerr)
				return
			}
			failed = append(failed, glFailedOut{DocumentID: d.ID, Number: d.Number, LedgerCode: CodeAllocationUnavailable})
			if !errors.Is(aerr, revrec.ErrNoContract) && !errors.Is(aerr, revrec.ErrAmbiguous) {
				more = true
				break
			}
			continue
		}
		shares, imbalance := glSplit(profile, d, allocs)
		var entry ledgerclient.Entry
		if imbalance == "" {
			entry, imbalance = glEntry(profile, d, shares)
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
				httpx.Fail(w, r, rerr)
				return
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
			httpx.Fail(w, r, p)
			return
		}
		if newly, err := a.glBooks.MarkGLBooked(ctx, d.ID, entryID, shares); err != nil {
			httpx.Fail(w, r, err)
			return
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
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "failed": failed, "more": more})
}

func bookedIDs(items []glBookedOut) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.DocumentID
	}
	return out
}
