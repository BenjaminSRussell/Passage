package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/store"
)

const (
	deskEntrySourceType = "cash_desk_entry"
	deskDiffSourceType  = "cash_desk_difference"
	deskKeyPrefix       = "treasury-desk-"
	deskDiffKeyPrefix   = "treasury-deskdiff-"
)

// deskPosting is the ledger side of one desk: its profile and the ledger account of its cash.
type deskPosting struct {
	profile GLProfile
	cash    string
}

// deskLedger finds the posting setup of a desk. ok is false when the desk is not posted to the ledger: no ledger client,
// or the legal entity's profile has no cashAccounts. A profile that has them but not this desk's cash account is a
// refusal (mapped is false), so a desk is never booked silently outside the ledger.
func (a *API) deskLedger(desk store.CashDesk) (dp deskPosting, ok, mapped bool) {
	if a.ledger == nil {
		return dp, false, true
	}
	profile, has := a.glProfiles[desk.LegalEntityID]
	if !has || len(profile.CashAccounts) == 0 {
		return dp, false, true
	}
	cash, has := profile.CashAccounts[desk.MainAccountID]
	return deskPosting{profile: profile, cash: cash}, true, has
}

func deskEntryLedger(desk store.CashDesk, dp deskPosting, e store.CashDeskEntry) (ledgerclient.Entry, string) {
	counter := e.CounterAccount
	if e.BankAccountID != nil {
		counter = dp.profile.BankAccounts[*e.BankAccountID]
	}
	if counter == "" {
		return ledgerclient.Entry{}, "the posting profile has no ledger account for the bank account of this line"
	}
	value, err := decimal.NewFromString(e.Amount)
	if err != nil {
		return ledgerclient.Entry{}, "the amount is not a decimal"
	}
	text := fmt.Sprintf("cash desk %s %s line %d", desk.Code, e.BusinessDate, e.LineNo)
	if len(text) > maxLineTextLength {
		text = text[:maxLineTextLength]
	}
	payment := e.Kind == "payment"
	return ledgerclient.Entry{LegalEntityID: dp.profile.LegalEntityID, LedgerID: dp.profile.LedgerID, DocumentID: desk.ID, PostingDate: e.BusinessDate,
		Currency: desk.Currency, IdempotencyKey: deskKeyPrefix + e.ID, SourceType: deskEntrySourceType,
		Lines: []ledgerclient.Line{{Account: dp.cash, Credit: payment, Amount: value, Text: text}, {Account: counter, Credit: !payment, Amount: value, Text: text}}}, ""
}

// deskDifferenceLedger books the count's difference: a shortage debits the difference account, a surplus credits it.
func deskDifferenceLedger(desk store.CashDesk, dp deskPosting, date string, diff decimal.Decimal) ledgerclient.Entry {
	text := fmt.Sprintf("cash desk %s %s count difference", desk.Code, date)
	if len(text) > maxLineTextLength {
		text = text[:maxLineTextLength]
	}
	short := diff.IsNegative()
	value := diff.Abs()
	return ledgerclient.Entry{LegalEntityID: dp.profile.LegalEntityID, LedgerID: dp.profile.LedgerID, DocumentID: desk.ID, PostingDate: date,
		Currency: desk.Currency, IdempotencyKey: deskDiffKeyPrefix + desk.ID + "-" + date, SourceType: deskDiffSourceType,
		Lines: []ledgerclient.Line{{Account: dp.cash, Credit: short, Amount: value, Text: text}, {Account: dp.profile.CashDifferenceAccount, Credit: !short, Amount: value, Text: text}}}
}

// flushDeskPostings posts what a day still owes the ledger, oldest line first, then the count difference. Every post
// carries a stable idempotency key, so a repeat after a lost answer cannot book twice. It returns the problem to send,
// or nil when nothing is left pending.
func (a *API) flushDeskPostings(ctx context.Context, deskID, date string) error {
	pend, err := a.desks.PendingDeskPostings(ctx, deskID, date)
	if err != nil {
		return err
	}
	if len(pend.Entries) == 0 && pend.Difference == nil {
		return nil
	}
	dp, ok, mapped := a.deskLedger(pend.Desk)
	if !ok || !mapped {
		return httpx.NewProblem(http.StatusUnprocessableEntity, CodeLedgerNoProfile, "no GL posting profile maps this cash desk's cash account")
	}
	cid := middleware.GetReqID(ctx)
	for _, e := range pend.Entries {
		entry, detail := deskEntryLedger(pend.Desk, dp, e)
		if detail != "" {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeLedgerNoProfile, detail)
		}
		id, err := a.ledger.Post(ctx, entry)
		if err != nil {
			return deskLedgerProblem(ctx, err, "line", e.LineNo, cid)
		}
		if err := a.desks.MarkDeskEntryPosted(ctx, deskID, e.ID, id); err != nil {
			return err
		}
	}
	if pend.Difference != nil {
		diff, _ := decimal.NewFromString(*pend.Difference)
		id, err := a.ledger.Post(ctx, deskDifferenceLedger(pend.Desk, dp, date, diff))
		if err != nil {
			return deskLedgerProblem(ctx, err, "difference", 0, cid)
		}
		return a.desks.MarkDeskDifferencePosted(ctx, deskID, date, id)
	}
	return nil
}

func deskLedgerProblem(ctx context.Context, err error, what string, lineNo int, cid string) error {
	switch {
	case ledgerclient.Rejected(err) && !authRefusal(err):
		slog.WarnContext(ctx, "general ledger refused a cash desk posting", "what", what, "line", lineNo, "ledgerCode", ledgerclient.CodeOf(err), "correlationId", cid)
		p := httpx.NewProblem(http.StatusUnprocessableEntity, CodeLedgerRefused, "The general ledger refused the entry, so it stays waiting. Fix the cause named in ledgerCode, then post the day again.")
		p.Errors = map[string]string{"ledgerCode": ledgerRefusalCode(err)}
		return p
	default:
		var le *ledgerclient.Error
		slog.ErrorContext(ctx, "general ledger call failed for a cash desk posting", "what", what, "line", lineNo, "err", err, "known", errors.As(err, &le), "correlationId", cid)
		return httpx.NewProblem(http.StatusServiceUnavailable, CodeLedgerDown, "The general ledger did not answer. The line is saved and waits; post the day again, it will not post twice.")
	}
}
