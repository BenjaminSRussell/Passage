package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/services/revrec/internal/store"
)

const (
	CodeLedgerUnavailable = "REV-0200"
	CodeLedgerRefused     = "REV-0201"
	CodeLedgerNotMapped   = "REV-0202"
	// CodeLedgerMismatch: the ledger configured for the legal entity belongs to another legal entity (the ledger is pinned to
	// the entity before anything is posted).
	CodeLedgerMismatch = "REV-0203"
	// CodeReversalRefused is recorded on a reversal posting the ledger refused without a problem code.
	CodeReversalRefused = "REV-0204"

	journalSourceType  = "revenue_recognition"
	reversalSourceType = "revenue_reversal"
	allTime            = "1970-01-01"
)

type PostingStore interface {
	ClaimPostings(ctx context.Context, legalEntityID, ledgerID string, through time.Time, retryRejected bool) ([]store.Posting, []store.Posting, error)
	CompletePosting(ctx context.Context, postingID, entryID string) (store.Completion, error)
	RejectPosting(ctx context.Context, postingID, code string) error
	DueReversals(ctx context.Context, legalEntityID, ledgerID string, now time.Time, retryParked bool) ([]store.ReversalPosting, []store.ReversalPosting, error)
	CompleteReversal(ctx context.Context, id, entryID string) error
	RecordReversalFailure(ctx context.Context, id, code string, now time.Time, refused bool) (bool, error)
}

func (a *API) WithLedger(ledger ledgerclient.Ledger, postings PostingStore, ledgerIDs map[string]string) *API {
	a.ledger, a.postings, a.ledgerIDs = ledger, postings, ledgerIDs
	return a
}

func ParseLedgerIDs(raw string) (map[string]string, error) {
	out := map[string]string{}
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		rawLE, rawLedger, ok := strings.Cut(pair, "=")
		le, leOK := ids.Canonical(rawLE)
		ledger, ledgerOK := ids.Canonical(rawLedger)
		if !ok || !leOK || !ledgerOK {
			return nil, fmt.Errorf("REVREC_LEDGER_IDS entry %q must be <legalEntityId>=<ledgerId> with two UUIDs", pair)
		}
		out[le] = ledger
	}
	return out, nil
}

type postingRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	Through       string `json:"through,omitempty"`
	RetryRejected bool   `json:"retryRejected,omitempty"`
}

type rejectedOut struct {
	ID          string `json:"id"`
	PostingDate string `json:"postingDate"`
	Currency    string `json:"currency"`
	Amount      string `json:"amount"`
	LedgerCode  string `json:"ledgerCode"`
}

type otherLedgerOut struct {
	ID          string `json:"id"`
	LedgerID    string `json:"ledgerId"`
	PostingDate string `json:"postingDate"`
	Currency    string `json:"currency"`
	Amount      string `json:"amount"`
}

type reversalOut struct {
	ID               string `json:"id"`
	ContractID       string `json:"contractId"`
	CreditNoteID     string `json:"creditNoteId"`
	CreditNoteNumber string `json:"creditNoteNumber"`
	ModificationID   string `json:"modificationId,omitempty"`
	Currency         string `json:"currency"`
	PostingDate      string `json:"postingDate"`
	Amount           string `json:"amount"`
	JournalEntryID   string `json:"journalEntryId,omitempty"`
	LedgerCode       string `json:"ledgerCode,omitempty"`
	Parked           bool   `json:"parked,omitempty"`
	LedgerID         string `json:"ledgerId,omitempty"`
}

func toReversalOut(rp store.ReversalPosting) reversalOut {
	return reversalOut{ID: rp.ID, ContractID: rp.ContractID, CreditNoteID: rp.CreditNoteID, CreditNoteNumber: rp.CreditNoteNumber, ModificationID: rp.ModificationID,
		Currency: rp.Currency, PostingDate: rp.PostingDate.Format(dateLayout), Amount: fixed(rp.Amount, rp.Currency)}
}

type postingOut struct {
	ID             string `json:"id"`
	Currency       string `json:"currency"`
	PostingDate    string `json:"postingDate"`
	Amount         string `json:"amount"`
	LineCount      int    `json:"lineCount"`
	JournalEntryID string `json:"journalEntryId"`
}

func (a *API) ledgerReady(w http.ResponseWriter, r *http.Request) bool {
	if a.ledger == nil || a.postings == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, CodeLedgerUnavailable, "the general ledger is not configured for this service"))
		return false
	}
	return true
}

func (a *API) ledgerFor(w http.ResponseWriter, r *http.Request, legalEntityID string) (string, bool) {
	ledgerID, ok := a.ledgerIDs[legalEntityID]
	if !ok {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, CodeLedgerNotMapped, "no ledger is configured for this legal entity"))
	}
	return ledgerID, ok
}

func ledgerProblem(ctx context.Context, op string, err error) error {
	code := ledgerclient.CodeOf(err)
	slog.ErrorContext(ctx, "general ledger call failed", "op", op, "ledgerCode", code, "err", err, "correlationId", middleware.GetReqID(ctx))
	detail := "the general ledger did not answer"
	if code != "" {
		detail = "the general ledger refused the request (" + code + ")"
	}
	return httpx.NewProblem(http.StatusBadGateway, CodeLedgerRefused, detail)
}

func (a *API) postJournal(w http.ResponseWriter, r *http.Request) {
	if !a.ledgerReady(w, r) {
		return
	}
	var req postingRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	legalEntityID, valid := ids.Canonical(req.LegalEntityID)
	if !valid {
		fields["legalEntityId"] = "must be a UUID"
	}
	req.LegalEntityID = legalEntityID
	now := a.now().UTC()
	lastMonthEnd := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1)
	through := lastMonthEnd
	if req.Through != "" {
		t, err := time.Parse(dateLayout, req.Through)
		if err != nil || t.After(now.Truncate(24*time.Hour)) {
			fields["through"] = "must be a date as YYYY-MM-DD that is not in the future"
		} else if t.Before(lastMonthEnd) {
			through = t
		}
	}
	if len(fields) > 0 {
		writeFields(w, r, fields)
		return
	}
	if !inScope(r, req.LegalEntityID) {
		httpx.Fail(w, r, outOfScope())
		return
	}
	ledgerID, ok := a.ledgerFor(w, r, req.LegalEntityID)
	if !ok || !a.ledgerPinned(w, r, req.LegalEntityID, ledgerID) {
		return
	}
	pending, otherLedger, err := a.postings.ClaimPostings(r.Context(), req.LegalEntityID, ledgerID, through, req.RetryRejected)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out, rejected, elsewhere := make([]postingOut, 0, len(pending)), []rejectedOut{}, make([]otherLedgerOut, 0, len(otherLedger))
	for _, p := range otherLedger {
		slog.ErrorContext(r.Context(), "pending revenue posting belongs to another ledger than the configured one", "postingId", p.ID, "postingLedgerId", p.LedgerID, "configuredLedgerId", ledgerID, "correlationId", middleware.GetReqID(r.Context()))
		elsewhere = append(elsewhere, otherLedgerOut{ID: p.ID, LedgerID: p.LedgerID, PostingDate: p.PostingDate.Format(dateLayout), Currency: p.Currency, Amount: fixed(p.Amount, p.Currency)})
	}
	for _, p := range pending {
		entryID, err := a.ledger.Post(r.Context(), ledgerclient.Entry{
			LegalEntityID: p.LegalEntityID, LedgerID: p.LedgerID, DocumentID: p.ID, PostingDate: p.PostingDate.Format(dateLayout),
			Currency: p.Currency, IdempotencyKey: "revrec-posting-" + p.ID, SourceType: journalSourceType,
			Lines: []ledgerclient.Line{
				{Account: store.DeferredAccount, Amount: p.Amount, Text: "Revenue recognized"},
				{Account: store.RevenueAccount, Credit: true, Amount: p.Amount, Text: "Revenue recognized"},
			},
		})
		if ledgerclient.Rejected(err) {
			code := ledgerclient.CodeOf(err)
			slog.WarnContext(r.Context(), "general ledger refused a revenue posting", "postingId", p.ID, "ledgerCode", code, "err", err, "correlationId", middleware.GetReqID(r.Context()))
			if err := a.postings.RejectPosting(r.Context(), p.ID, code); err != nil {
				httpx.Fail(w, r, err)
				return
			}
			rejected = append(rejected, rejectedOut{ID: p.ID, PostingDate: p.PostingDate.Format(dateLayout), Currency: p.Currency, Amount: fixed(p.Amount, p.Currency), LedgerCode: code})
			continue
		}
		if err != nil {
			httpx.Fail(w, r, ledgerProblem(r.Context(), "post", err))
			return
		}
		done, err := a.postings.CompletePosting(r.Context(), p.ID, entryID)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		if done.Reattached {
			slog.ErrorContext(r.Context(), "ledger accepted a revenue posting that a concurrent call had marked rejected", "postingId", p.ID, "entryId", entryID, "supersededPostings", done.Superseded, "correlationId", middleware.GetReqID(r.Context()))
		}
		if len(done.DoublePosted) > 0 {
			slog.ErrorContext(r.Context(), "revenue lines were posted to the ledger twice; reverse one entry by hand", "postingId", p.ID, "entryId", entryID, "doublePostedPostings", done.DoublePosted, "correlationId", middleware.GetReqID(r.Context()))
		}
		a.journalPostings.Add(1)
		out = append(out, postingOut{ID: p.ID, Currency: p.Currency, PostingDate: p.PostingDate.Format(dateLayout),
			Amount: fixed(p.Amount, p.Currency), LineCount: p.LineCount, JournalEntryID: entryID})
	}
	reversals, ok := a.postReversals(w, r, req.LegalEntityID, ledgerID, req.RetryRejected)
	if !ok {
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"through": through.Format(dateLayout), "items": out, "rejected": rejected, "otherLedger": elsewhere,
		"reversals": reversals.posted, "failedReversals": reversals.failed, "otherLedgerReversals": reversals.elsewhere})
}

// ledgerPinned confirms with the ledger that the ledger configured for the entity belongs to it before anything is posted, so
// a wrong REVREC_LEDGER_IDS entry never books one entity's revenue into another's ledger.
func (a *API) ledgerPinned(w http.ResponseWriter, r *http.Request, legalEntityID, ledgerID string) bool {
	owner, err := a.ledger.LegalEntity(r.Context(), legalEntityID, ledgerID)
	if err != nil {
		httpx.Fail(w, r, ledgerProblem(r.Context(), "ledger", err))
		return false
	}
	if owner != legalEntityID {
		slog.ErrorContext(r.Context(), "configured ledger belongs to another legal entity", "legalEntityId", legalEntityID, "ledgerId", ledgerID,
			"ledgerLegalEntityId", owner, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, CodeLedgerMismatch, "the configured ledger belongs to another legal entity"))
		return false
	}
	return true
}

type reversalResult struct {
	posted, failed, elsewhere []reversalOut
}

// postReversals books the recognized revenue that credit notes reversed (store.ApplyCreditLine): one entry per reversal,
// Dr 4000 / Cr 2400 for the stored revenue_reversed, dated the credit note's posting date, keyed revrec-reversal-<id> so a
// retry after a lost answer returns the same entry. A refusal is recorded with a backed-off next attempt and parks the row after
// store.MaxReversalRefusals refusals, so it never holds up the rows behind it; an outage is recorded without counting toward
// parking and ends the call with 502. Amounts, accounts and ledger come from the stored row and the configuration, never the
// request, and only this subledger's reversals are posted: no amount is derived from a GL balance.
func (a *API) postReversals(w http.ResponseWriter, r *http.Request, legalEntityID, ledgerID string, retryParked bool) (reversalResult, bool) {
	ctx, cid := r.Context(), middleware.GetReqID(r.Context())
	now := a.now().UTC()
	out := reversalResult{posted: []reversalOut{}, failed: []reversalOut{}, elsewhere: []reversalOut{}}
	due, otherLedger, err := a.postings.DueReversals(ctx, legalEntityID, ledgerID, now, retryParked)
	if err != nil {
		httpx.Fail(w, r, err)
		return out, false
	}
	for _, rp := range otherLedger {
		slog.ErrorContext(ctx, "revenue reversal is pinned to another ledger than the configured one", "reversalId", rp.ID, "reversalLedgerId", rp.LedgerID,
			"configuredLedgerId", ledgerID, "correlationId", cid)
		o := toReversalOut(rp)
		o.LedgerID = rp.LedgerID
		out.elsewhere = append(out.elsewhere, o)
	}
	for _, rp := range due {
		text, key := "Revenue reversed by credit note "+rp.CreditNoteNumber, "revrec-reversal-"+rp.ID
		if rp.ModificationID != "" {
			text, key = "Revenue reduced by contract modification", "revrec-modrev-"+rp.ID
		}
		entryID, err := a.ledger.Post(ctx, ledgerclient.Entry{
			LegalEntityID: rp.LegalEntityID, LedgerID: rp.LedgerID, DocumentID: rp.ID, PostingDate: rp.PostingDate.Format(dateLayout),
			Currency: rp.Currency, IdempotencyKey: key, SourceType: reversalSourceType,
			Lines: []ledgerclient.Line{
				{Account: store.RevenueAccount, Amount: rp.Amount, Text: text},
				{Account: store.DeferredAccount, Credit: true, Amount: rp.Amount, Text: text},
			},
		})
		if ledgerclient.Rejected(err) {
			code := ledgerclient.CodeOf(err)
			if code == "" {
				code = CodeReversalRefused
			}
			parked, rerr := a.postings.RecordReversalFailure(ctx, rp.ID, code, now, true)
			if rerr != nil {
				httpx.Fail(w, r, rerr)
				return out, false
			}
			slog.WarnContext(ctx, "general ledger refused a revenue reversal", "reversalId", rp.ID, "ledgerCode", code, "parked", parked, "err", err, "correlationId", cid)
			o := toReversalOut(rp)
			o.LedgerCode, o.Parked = code, parked
			out.failed = append(out.failed, o)
			continue
		}
		if err != nil {
			if _, rerr := a.postings.RecordReversalFailure(ctx, rp.ID, CodeLedgerRefused, now, false); rerr != nil {
				slog.ErrorContext(ctx, "revenue reversal failure not recorded", "reversalId", rp.ID, "err", rerr, "correlationId", cid)
			}
			httpx.Fail(w, r, ledgerProblem(ctx, "post", err))
			return out, false
		}
		if err := a.postings.CompleteReversal(ctx, rp.ID, entryID); err != nil {
			httpx.Fail(w, r, err)
			return out, false
		}
		a.reversalPostings.Add(1)
		o := toReversalOut(rp)
		o.JournalEntryID = entryID
		out.posted = append(out.posted, o)
	}
	return out, true
}

type glReconciliationOut struct {
	LegalEntityID string `json:"legalEntityId"`
	LedgerID      string `json:"ledgerId"`
	Currency      string `json:"currency"`
	Period        string `json:"period"`
	Account       string `json:"account"`
	Recognized    string `json:"recognized"`
	GLDebit       string `json:"glDebit"`
	Difference    string `json:"difference"`
	CreditNotes   string `json:"creditNotes"`
	// ModificationCredits is the price decreases of contract modifications, which billing credits and debits to 2400.
	ModificationCredits string `json:"modificationCredits"`
	Reversed            string `json:"reversed"`
	Deferred            string `json:"deferred"`
	GLCredit            string `json:"glCredit"`
	GLClosing           string `json:"glClosing"`
	ClosingDiff         string `json:"closingDifference"`
	Reconciled          bool   `json:"reconciled"`
}

func (a *API) glReconciliation(w http.ResponseWriter, r *http.Request) {
	if !a.ledgerReady(w, r) {
		return
	}
	q := r.URL.Query()
	cur, period := q.Get("currency"), q.Get("period")
	le, valid := ids.Canonical(q.Get("legalEntityId"))
	fields := map[string]string{}
	if !valid {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !currencyCode.MatchString(cur) {
		fields["currency"] = "must be an ISO 4217 alpha-3 code"
	}
	start, err := time.Parse(periodLayout, period)
	if err != nil {
		fields["period"] = "must be a month as YYYY-MM"
	}
	if len(fields) > 0 {
		writeFields(w, r, fields)
		return
	}
	if !inScope(r, le) {
		httpx.Fail(w, r, outOfScope())
		return
	}
	ledgerID, ok := a.ledgerFor(w, r, le)
	if !ok {
		return
	}
	epoch, _ := time.Parse(dateLayout, allTime)
	rf, err := a.store.RollForward(r.Context(), le, cur, epoch, start.AddDate(0, 1, -1))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	bal, err := a.ledger.Balance(r.Context(), ledgerclient.BalanceQuery{LegalEntityID: le, LedgerID: ledgerID, Account: store.DeferredAccount, PeriodTo: period})
	if err != nil {
		httpx.Fail(w, r, ledgerProblem(r.Context(), "balance", err))
		return
	}
	if bal.Currency != cur {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadGateway, CodeLedgerRefused, "the ledger reports "+bal.Currency+", not "+cur))
		return
	}
	// 2400 is debited by revrec's recognition postings and by billing's credit notes for their deferred part, which revrec
	// mirrors as negative adjustments when it consumes creditnote.booked; a credit note revrec has not reversed shows here.
	// A modification's price decrease is credited by billing to 2400 too (its revenue part is revrec's modification reversal, on
	// the credit side).
	creditNotes := rf.Adjustments.Neg()
	diff := rf.Recognized.Add(creditNotes).Add(rf.ModificationCredits).Sub(bal.Debit)
	glClosing := bal.Credit.Sub(bal.Debit)
	closingDiff := rf.Closing.Sub(glClosing)
	httpx.JSON(w, http.StatusOK, glReconciliationOut{
		LegalEntityID: le, LedgerID: ledgerID, Currency: cur, Period: period, Account: store.DeferredAccount,
		Recognized: fixed(rf.Recognized, cur), CreditNotes: fixed(creditNotes, cur), ModificationCredits: fixed(rf.ModificationCredits, cur), Reversed: fixed(rf.Reversed, cur),
		GLDebit: fixed(bal.Debit, cur), Difference: fixed(diff, cur),
		Deferred: fixed(rf.Closing, cur), GLCredit: fixed(bal.Credit, cur), GLClosing: fixed(glClosing, cur), ClosingDiff: fixed(closingDiff, cur),
		Reconciled: diff.IsZero() && closingDiff.IsZero(),
	})
}
