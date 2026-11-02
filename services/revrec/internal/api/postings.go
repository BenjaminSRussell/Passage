package api

import (
	"context"
	"log/slog"
	"net/http"
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

	journalSourceType = "revenue_recognition"
	allTime           = "1970-01-01"
)

type PostingStore interface {
	ClaimPostings(ctx context.Context, legalEntityID, ledgerID string, through time.Time) ([]store.Posting, error)
	CompletePosting(ctx context.Context, postingID, entryID string) error
}

func (a *API) WithLedger(ledger ledgerclient.Ledger, postings PostingStore) *API {
	a.ledger, a.postings = ledger, postings
	return a
}

type postingRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	LedgerID      string `json:"ledgerId"`
	Through       string `json:"through,omitempty"`
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
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !ids.Valid(req.LedgerID) {
		fields["ledgerId"] = "must be a UUID"
	}
	through := a.now().UTC().Truncate(24 * time.Hour)
	if req.Through != "" {
		t, err := time.Parse(dateLayout, req.Through)
		if err != nil || t.After(through) {
			fields["through"] = "must be a date as YYYY-MM-DD that is not in the future"
		}
		through = t
	}
	if len(fields) > 0 {
		writeFields(w, r, fields)
		return
	}
	if !inScope(r, req.LegalEntityID) {
		httpx.Fail(w, r, outOfScope())
		return
	}
	pending, err := a.postings.ClaimPostings(r.Context(), req.LegalEntityID, req.LedgerID, through)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out := make([]postingOut, 0, len(pending))
	for _, p := range pending {
		entryID, err := a.ledger.Post(r.Context(), ledgerclient.Entry{
			LegalEntityID: p.LegalEntityID, LedgerID: p.LedgerID, DocumentID: p.ID, PostingDate: p.PostingDate.Format(dateLayout),
			Currency: p.Currency, IdempotencyKey: "revrec-posting-" + p.ID, SourceType: journalSourceType,
			Lines: []ledgerclient.Line{
				{Account: store.DeferredAccount, Amount: p.Amount, Text: "Revenue recognized"},
				{Account: store.RevenueAccount, Credit: true, Amount: p.Amount, Text: "Revenue recognized"},
			},
		})
		if err != nil {
			httpx.Fail(w, r, ledgerProblem(r.Context(), "post", err))
			return
		}
		if err := a.postings.CompletePosting(r.Context(), p.ID, entryID); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		a.journalPostings.Add(1)
		out = append(out, postingOut{ID: p.ID, Currency: p.Currency, PostingDate: p.PostingDate.Format(dateLayout),
			Amount: fixed(p.Amount, p.Currency), LineCount: p.LineCount, JournalEntryID: entryID})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
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
	Reconciled    bool   `json:"reconciled"`
}

func (a *API) glReconciliation(w http.ResponseWriter, r *http.Request) {
	if !a.ledgerReady(w, r) {
		return
	}
	q := r.URL.Query()
	le, ledgerID, cur, period := q.Get("legalEntityId"), q.Get("ledgerId"), q.Get("currency"), q.Get("period")
	fields := map[string]string{}
	if !ids.Valid(le) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !ids.Valid(ledgerID) {
		fields["ledgerId"] = "must be a UUID"
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
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, CodeLedgerRefused, "the ledger reports "+bal.Currency+", not "+cur))
		return
	}
	diff := rf.Recognized.Sub(bal.Debit)
	httpx.JSON(w, http.StatusOK, glReconciliationOut{
		LegalEntityID: le, LedgerID: ledgerID, Currency: cur, Period: period, Account: store.DeferredAccount,
		Recognized: fixed(rf.Recognized, cur), GLDebit: fixed(bal.Debit, cur), Difference: fixed(diff, cur), Reconciled: diff.IsZero(),
	})
}
