package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/domain"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/store"
)

const (
	EnvGLProfiles     = "TREASURY_GL_PROFILES"
	lineSourceType    = "bank_statement_line"
	lineKeyPrefix     = "treasury-line-"
	maxLineTextLength = 60
)

var accountText = regexp.MustCompile(`^[A-Za-z0-9._-]{1,20}$`)

type GLProfile struct {
	LegalEntityID   string            `json:"legalEntityId"`
	LedgerID        string            `json:"ledgerId"`
	BankAccounts    map[string]string `json:"bankAccounts"`
	FeeAccount      string            `json:"feeAccount"`
	InterestAccount string            `json:"interestAccount"`
	SuspenseAccount string            `json:"suspenseAccount"`
}

func ParseGLProfiles(raw string) (map[string]GLProfile, error) {
	var list []GLProfile
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&list); err != nil {
		return nil, fmt.Errorf("%s must be a JSON array of profiles: %w", EnvGLProfiles, err)
	}
	out := map[string]GLProfile{}
	for _, p := range list {
		le, leOK := ids.Canonical(p.LegalEntityID)
		ledger, ledgerOK := ids.Canonical(p.LedgerID)
		valid := leOK && ledgerOK && len(p.BankAccounts) > 0 && accountText.MatchString(p.FeeAccount) &&
			accountText.MatchString(p.InterestAccount) && accountText.MatchString(p.SuspenseAccount)
		banks := make(map[string]string, len(p.BankAccounts))
		for bank, account := range p.BankAccounts {
			canonical, ok := ids.Canonical(bank)
			valid = valid && ok && accountText.MatchString(account)
			banks[canonical] = account
		}
		if !valid {
			return nil, fmt.Errorf("%s entry for %q needs legalEntityId and ledgerId UUIDs, bankAccounts (bank account UUID to ledger account), feeAccount, interestAccount and suspenseAccount (at most 20 characters)", EnvGLProfiles, p.LegalEntityID)
		}
		if _, dup := out[le]; dup {
			return nil, fmt.Errorf("%s names legal entity %s twice", EnvGLProfiles, le)
		}
		p.LegalEntityID, p.LedgerID, p.BankAccounts = le, ledger, banks
		out[le] = p
	}
	return out, nil
}

func (a *API) WithLedger(ledger ledgerclient.Ledger, profiles map[string]GLProfile) *API {
	a.ledger, a.glProfiles = ledger, profiles
	return a
}

func lineEntry(p GLProfile, statementID string, t store.LineTransaction) (ledgerclient.Entry, string, string) {
	e := ledgerclient.Entry{LegalEntityID: p.LegalEntityID, LedgerID: p.LedgerID, DocumentID: statementID, PostingDate: t.PostingDate,
		Currency: t.Currency, IdempotencyKey: fmt.Sprintf("%s%s-%d", lineKeyPrefix, statementID, t.LineNo), SourceType: lineSourceType}
	bank, ok := p.BankAccounts[t.BankAccountID]
	if !ok {
		return e, CodeLedgerNoProfile, "the posting profile has no ledger account for this bank account"
	}
	amount, err := decimal.NewFromString(t.Amount)
	if err != nil {
		return e, store.CodeLineKind, "the statement line amount is not a decimal"
	}
	places := int32(money.MinorUnits(t.Currency))
	value := amount.Abs()
	if !value.Equal(value.Round(places)) {
		return e, store.CodeLineKind, "the statement line amount has more decimal places than the currency allows"
	}
	counter := map[string]string{domain.LineBankFee: p.FeeAccount, domain.LineInterest: p.InterestAccount, domain.LineUnknownReceipt: p.SuspenseAccount}[t.Kind]
	text := strings.ReplaceAll(t.Kind, "_", " ") + " statement " + statementID[:8] + " line " + fmt.Sprint(t.LineNo)
	if len(text) > maxLineTextLength {
		text = text[:maxLineTextLength]
	}
	out := amount.IsNegative()
	e.Lines = []ledgerclient.Line{
		{Account: bank, Credit: out, Amount: value, Text: text},
		{Account: counter, Credit: !out, Amount: value, Text: text},
	}
	return e, "", ""
}

func ledgerRefusalCode(err error) string {
	if code := ledgerclient.CodeOf(err); code != "" {
		return code
	}
	return CodeLedgerRefused
}

func (a *API) createLineTransaction(w http.ResponseWriter, r *http.Request) {
	if !a.reconsReady(w, r) {
		return
	}
	id, lineNo, ok := reconLine(w, r)
	if !ok {
		return
	}
	var req lineTransactionRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.Kind != domain.LineBankFee && req.Kind != domain.LineInterest && req.Kind != domain.LineUnknownReceipt {
		validationFailed(w, r, map[string]string{"kind": "bank_fee, interest or unknown_receipt"})
		return
	}
	if a.ledger == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, CodeReconNotEnabled, "the general ledger is not configured for this service"))
		return
	}
	ctx, cid := r.Context(), middleware.GetReqID(r.Context())
	rec, err := a.recons.GetReconciliation(ctx, id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	profile, ok := a.glProfiles[rec.LegalEntityID]
	if !ok {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, CodeLedgerNoProfile, "no GL posting profile is configured for this legal entity"))
		return
	}
	t, err := a.recons.BeginLineTransaction(ctx, store.NewLineTransaction{ReconciliationID: id, LineNo: lineNo, Kind: req.Kind})
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	if t.Replayed {
		httpx.JSON(w, http.StatusOK, t)
		return
	}
	refuse := func(code, detail string) {
		if err := a.recons.RefuseLineTransaction(ctx, t.ID, code); err != nil {
			logFailure(r, "release refused line transaction", err)
		}
		a.lineTxRefused.Add(1)
		p := httpx.NewProblem(http.StatusUnprocessableEntity, CodeLedgerRefused, detail)
		p.Errors = map[string]string{"ledgerCode": code}
		httpx.Fail(w, r, p)
	}
	entry, code, detail := lineEntry(profile, rec.StatementID, t)
	if code != "" {
		refuse(code, detail+"; nothing was recorded as matched")
		return
	}
	entryID, err := a.ledger.Post(ctx, entry)
	if ledgerclient.Rejected(err) || ledgerclient.CodeOf(err) == ledgerclient.CodeIdempotencyConflict {
		slog.WarnContext(ctx, "general ledger refused a bank transaction", "reconciliationId", id, "lineNo", lineNo, "ledgerCode", ledgerclient.CodeOf(err), "correlationId", cid)
		refuse(ledgerRefusalCode(err), "The general ledger refused the entry, so the statement line was not matched and nothing was booked. Fix the cause named in ledgerCode, then try again.")
		return
	}
	if err != nil {
		a.lineTxNoAnswer.Add(1)
		slog.ErrorContext(ctx, "general ledger call failed", "reconciliationId", id, "lineNo", lineNo, "ledgerCode", ledgerclient.CodeOf(err), "err", err, "correlationId", cid)
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, CodeLedgerDown, "The general ledger did not answer. Nothing was recorded as matched; repeat the request, it is safe to repeat."))
		return
	}
	done, err := a.recons.FinishLineTransaction(ctx, t.ID, entryID)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	a.lineTxPosted.Add(1)
	httpx.JSON(w, http.StatusCreated, done)
}
