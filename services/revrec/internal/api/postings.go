package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/services/revrec/internal/store"
)

const (
	CodeLedgerUnavailable = "REV-0200"
	CodeLedgerRefused     = "REV-0201"
	CodeLedgerNotMapped   = "REV-0202"

	journalSourceType = "revenue_recognition"
	allTime           = "1970-01-01"
)

type PostingStore interface {
	ClaimPostings(ctx context.Context, legalEntityID, ledgerID string, through time.Time, retryRejected bool) ([]store.Posting, []store.Posting, error)
	CompletePosting(ctx context.Context, postingID, entryID string) (store.Completion, error)
	RejectPosting(ctx context.Context, postingID, code string) error
	ContractAsset(ctx context.Context, legalEntityID, currency string, end time.Time) (decimal.Decimal, error)
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
	if !ok {
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
	httpx.JSON(w, http.StatusOK, map[string]any{"through": through.Format(dateLayout), "items": out, "rejected": rejected, "otherLedger": elsewhere})
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
	Deferred      string `json:"deferred"`
	GLCredit      string `json:"glCredit"`
	GLClosing     string `json:"glClosing"`
	ClosingDiff   string `json:"closingDifference"`
	ContractAsset string `json:"contractAsset"`
	Reconciled    bool   `json:"reconciled"`
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
	asset, err := a.postings.ContractAsset(r.Context(), le, cur, start.AddDate(0, 1, -1))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	diff := rf.Recognized.Sub(bal.Debit)
	glClosing := bal.Credit.Sub(bal.Debit)
	closingDiff := rf.Closing.Sub(glClosing)
	httpx.JSON(w, http.StatusOK, glReconciliationOut{
		LegalEntityID: le, LedgerID: ledgerID, Currency: cur, Period: period, Account: store.DeferredAccount,
		Recognized: fixed(rf.Recognized, cur), GLDebit: fixed(bal.Debit, cur), Difference: fixed(diff, cur),
		Deferred: fixed(rf.Closing, cur), GLCredit: fixed(bal.Credit, cur), GLClosing: fixed(glClosing, cur), ClosingDiff: fixed(closingDiff, cur), ContractAsset: fixed(asset, cur),
		Reconciled: diff.IsZero() && closingDiff.IsZero(),
	})
}
