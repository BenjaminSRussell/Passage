package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/billing/internal/store"
)

const (
	CodeLedgerNotConfigured = "BIL-0200"
	CodeLedgerUnavailable   = "BIL-0201"
	CodeLedgerNotMapped     = "BIL-0202"

	deferralSourceType = "invoice_deferral"
	deferralKeyPrefix  = "billing-deferral-"
)

type DeferralBooks interface {
	UnbookedDeferrals(ctx context.Context, p store.DeferralProfile) ([]store.Deferral, error)
	MarkDeferralBooked(ctx context.Context, invoiceID, entryID string) error
}

func (a *API) WithLedger(ledger ledgerclient.Ledger, books DeferralBooks, profiles map[string]store.DeferralProfile) *API {
	a.ledger, a.deferrals, a.profiles = ledger, books, profiles
	return a
}

// ParseGLProfiles reads BILLING_GL_PROFILES: a JSON array holding, per legal entity, the ledger and the accounts the deferral posting uses.
func ParseGLProfiles(raw string) (map[string]store.DeferralProfile, error) {
	var list []store.DeferralProfile
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return nil, fmt.Errorf("BILLING_GL_PROFILES must be a JSON array of profiles: %w", err)
	}
	out := map[string]store.DeferralProfile{}
	for _, p := range list {
		le, leOK := ids.Canonical(p.LegalEntityID)
		ledger, ledgerOK := ids.Canonical(p.LedgerID)
		if !leOK || !ledgerOK || !accountText.MatchString(p.ReceivableAccount) || !accountText.MatchString(p.DeferredAccount) || len(p.DeferredRevenueAccounts) == 0 {
			return nil, fmt.Errorf("BILLING_GL_PROFILES entry for %q needs legalEntityId and ledgerId UUIDs, receivableAccount, deferredAccount and deferredRevenueAccounts", p.LegalEntityID)
		}
		for _, acct := range p.DeferredRevenueAccounts {
			if !accountText.MatchString(acct) {
				return nil, fmt.Errorf("BILLING_GL_PROFILES entry for %q has an invalid deferred revenue account %q", p.LegalEntityID, acct)
			}
		}
		p.LegalEntityID, p.LedgerID = le, ledger
		out[le] = p
	}
	return out, nil
}

type deferralRequest struct {
	LegalEntityID string `json:"legalEntityId"`
}

type deferralOut struct {
	InvoiceID      string `json:"invoiceId"`
	Number         string `json:"number"`
	Currency       string `json:"currency"`
	PostingDate    string `json:"postingDate"`
	Amount         string `json:"amount"`
	JournalEntryID string `json:"journalEntryId"`
}

type deferralFailedOut struct {
	InvoiceID  string `json:"invoiceId"`
	Number     string `json:"number"`
	LedgerCode string `json:"ledgerCode"`
}

// postDeferrals books, for every posted invoice not yet booked, Dr receivable / Cr deferred revenue for the net of its lines on the
// profile's deferred revenue accounts. Accounts, ledger and amounts come from the profile and the stored invoice, never the request.
// The ledger key is derived from the invoice id, so a retry after a lost answer returns the same entry.
func (a *API) postDeferrals(w http.ResponseWriter, r *http.Request) {
	if a.ledger == nil || a.deferrals == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, CodeLedgerNotConfigured, "the general ledger is not configured for this service"))
		return
	}
	var req deferralRequest
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
	pending, err := a.deferrals.UnbookedDeferrals(r.Context(), profile)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	items, failed := make([]deferralOut, 0, len(pending)), []deferralFailedOut{}
	for _, d := range pending {
		entryID, err := a.ledger.Post(r.Context(), ledgerclient.Entry{
			LegalEntityID: le, LedgerID: profile.LedgerID, DocumentID: d.InvoiceID, PostingDate: d.PostingDate, Currency: d.Currency,
			IdempotencyKey: deferralKeyPrefix + d.InvoiceID, SourceType: deferralSourceType,
			Lines: []ledgerclient.Line{
				{Account: profile.ReceivableAccount, Amount: d.Amount, Text: "Invoice " + d.Number},
				{Account: profile.DeferredAccount, Credit: true, Amount: d.Amount, Text: "Deferred revenue " + d.Number},
			},
		})
		if ledgerclient.Rejected(err) {
			code := ledgerclient.CodeOf(err)
			slog.WarnContext(r.Context(), "general ledger refused an invoice deferral", "invoiceId", d.InvoiceID, "ledgerCode", code, "err", err, "correlationId", middleware.GetReqID(r.Context()))
			failed = append(failed, deferralFailedOut{InvoiceID: d.InvoiceID, Number: d.Number, LedgerCode: code})
			continue
		}
		if err != nil {
			slog.ErrorContext(r.Context(), "general ledger call failed", "op", "post", "invoiceId", d.InvoiceID, "ledgerCode", ledgerclient.CodeOf(err), "err", err, "correlationId", middleware.GetReqID(r.Context()))
			httpx.Fail(w, r, httpx.NewProblem(http.StatusBadGateway, CodeLedgerUnavailable, "the general ledger did not answer"))
			return
		}
		if err := a.deferrals.MarkDeferralBooked(r.Context(), d.InvoiceID, entryID); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		a.deferralPostings.Add(1)
		items = append(items, deferralOut{InvoiceID: d.InvoiceID, Number: d.Number, Currency: d.Currency, PostingDate: d.PostingDate,
			Amount: d.Amount.StringFixed(money.MinorUnits(d.Currency)), JournalEntryID: entryID})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "failed": failed})
}
