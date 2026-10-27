package api

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/domain"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/store"
)

// Service-local problem codes raised by the handlers (see README).
const (
	CodeFileTooLarge     = "TRS-0490"
	CodeFileInvalid      = "TRS-0491"
	CodePreconditionReq  = "precondition_required"
	defaultLinePage      = 100
	forecastLookbackDays = 90
	forecastLookaheadDay = 366
)

var forecastSourceTypes = map[string]bool{
	"AP": true, "AR": true, "PO": true, "SO": true, "Recurring": true,
	"Manual": true, "Payroll": true, "Project": true,
}

type createBankAccountRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	AccountCode   string `json:"accountCode"`
	IBAN          string `json:"iban"`
	// StatementAccount is the account number the bank writes on BAI2 and
	// MT940 statements when it is not the IBAN; optional.
	StatementAccount *string `json:"statementAccount"`
	BIC              *string `json:"bic"`
	Currency         string  `json:"currency"`
	MainAccountID    string  `json:"mainAccountId"`
}

// requireKey returns the Idempotency-Key header or writes a 400.
func requireKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	k := strings.TrimSpace(r.Header.Get(idempotencyKey))
	if k == "" || len(k) > 255 {
		validationFailed(w, r, map[string]string{idempotencyKey: "required, at most 255 characters"})
		return "", false
	}
	return k, true
}

func setETag(w http.ResponseWriter, version int) {
	w.Header().Set("ETag", `"`+strconv.Itoa(version)+`"`)
}

func (a *API) createBankAccount(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key, ok := requireKey(w, r)
	if !ok {
		return
	}
	var req createBankAccountRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !ids.Valid(req.MainAccountID) {
		fields["mainAccountId"] = "must be a UUID"
	}
	if !domain.ValidAccountCode(req.AccountCode) {
		fields["accountCode"] = "1 to 20 characters of A-Z, 0-9 or -"
	}
	if !domain.ValidCurrency(req.Currency) {
		fields["currency"] = "must be an ISO 4217 code"
	}
	statementAccount := ""
	if req.StatementAccount != nil {
		statementAccount = domain.NormalizeIBAN(*req.StatementAccount)
		if statementAccount != "" && !domain.ValidStatementAccount(statementAccount) {
			fields["statementAccount"] = "1 to 35 characters of A-Z, 0-9, /, . or -"
		}
	}
	var bic *string
	if req.BIC != nil && strings.TrimSpace(*req.BIC) != "" {
		b := strings.ToUpper(strings.TrimSpace(*req.BIC))
		if !domain.ValidBIC(b) {
			fields["bic"] = "must be an ISO 9362 BIC"
		}
		bic = &b
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if outsideScope(w, r, req.LegalEntityID) {
		return
	}
	if err := domain.ValidateIBAN(req.IBAN); err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, "TRS-0401", "IBAN fails the mod-97 check or has an invalid shape"))
		return
	}
	acct, err := a.store.CreateBankAccount(r.Context(), store.NewBankAccount{
		LegalEntityID:    req.LegalEntityID,
		AccountCode:      req.AccountCode,
		IBAN:             domain.NormalizeIBAN(req.IBAN),
		StatementAccount: statementAccount,
		BIC:              bic,
		Currency:         req.Currency,
		MainAccountID:    req.MainAccountID,
	}, key)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	a.accountsCreated.Add(1)
	setETag(w, acct.Version)
	httpx.JSON(w, http.StatusCreated, acct)
}

func (a *API) listBankAccounts(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	le := r.URL.Query().Get("legalEntityId")
	if !ids.Valid(le) {
		validationFailed(w, r, map[string]string{"legalEntityId": "required UUID query parameter"})
		return
	}
	items, err := a.store.ListBankAccounts(r.Context(), le)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getBankAccount(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, store.CodeAccountNotFound, "bank account not found")
	if !ok {
		return
	}
	acct, err := a.store.GetBankAccount(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	setETag(w, acct.Version)
	httpx.JSON(w, http.StatusOK, acct)
}

// ifMatchVersion parses If-Match (required on custom methods, 428 when absent).
func ifMatchVersion(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := strings.TrimSpace(r.Header.Get("If-Match"))
	if raw == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusPreconditionRequired, CodePreconditionReq, "If-Match with the current ETag is required"))
		return 0, false
	}
	n, err := strconv.Atoi(strings.Trim(strings.TrimPrefix(raw, "W/"), `"`))
	if err != nil {
		validationFailed(w, r, map[string]string{"If-Match": "must be the ETag of the account"})
		return 0, false
	}
	return n, true
}

// transitionBankAccount serves :activate, :deactivate and :close. Each needs
// If-Match with the account ETag (428 without it, 412 when stale).
func (a *API) transitionBankAccount(to domain.AccountStatus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validTenant(w, r) {
			return
		}
		id, ok := pathID(w, r, store.CodeAccountNotFound, "bank account not found")
		if !ok {
			return
		}
		version, ok := ifMatchVersion(w, r)
		if !ok {
			return
		}
		acct, err := a.store.TransitionBankAccount(r.Context(), id, version, to)
		if err != nil {
			httpx.Fail(w, r, problemFrom(err))
			return
		}
		a.accountsTransitioned.Add(1)
		setETag(w, acct.Version)
		httpx.JSON(w, http.StatusOK, acct)
	}
}

func (a *API) listStatements(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, store.CodeAccountNotFound, "bank account not found")
	if !ok {
		return
	}
	items, err := a.store.ListStatements(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// importResponse is the stored statement plus the warnings of a new import
// (TRS-0407 sequence gap). A replay carries no warnings.
type importResponse struct {
	store.Statement
	Warnings []store.Warning `json:"warnings"`
}

// importStatement accepts a multipart upload: field "bankAccountId" and file "file".
// The same bytes replay the existing statement with 200; a new statement is 201.
func (a *API) importStatement(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, a.maxFileBytes)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		a.statementsRejected.Add(1)
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusRequestEntityTooLarge, CodeFileTooLarge, "statement file exceeds the configured upload limit"))
			return
		}
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "body must be multipart/form-data"))
		return
	}
	bankAccountID := r.FormValue("bankAccountId")
	if !ids.Valid(bankAccountID) {
		a.statementsRejected.Add(1)
		validationFailed(w, r, map[string]string{"bankAccountId": "required UUID form field"})
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		a.statementsRejected.Add(1)
		validationFailed(w, r, map[string]string{"file": "required file part"})
		return
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		a.statementsRejected.Add(1)
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, CodeFileInvalid, "statement file could not be read"))
		return
	}
	sum := sha256.Sum256(data)
	parsed, err := domain.ParseStatements(data)
	if err == nil && len(parsed) != 1 {
		err = &domain.Error{Code: domain.CodeStatementFormat, Detail: "a file must hold exactly one statement (one camt.053 Stmt, BAI2 03 account or MT940 :20: block); split multi-statement files"}
	}
	if err != nil {
		a.statementsRejected.Add(1)
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	res, err := a.store.ImportStatement(r.Context(), store.StatementImport{
		BankAccountID: bankAccountID,
		FileHash:      hex.EncodeToString(sum[:]),
		Parsed:        parsed[0],
	})
	if err != nil {
		a.statementsRejected.Add(1)
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	if res.Replayed {
		a.statementsReplayed.Add(1)
		httpx.JSON(w, http.StatusOK, importResponse{Statement: res.Statement, Warnings: []store.Warning{}})
		return
	}
	a.statementsImported.Add(1)
	warnings := res.Warnings
	if warnings == nil {
		warnings = []store.Warning{}
	}
	httpx.JSON(w, http.StatusCreated, importResponse{Statement: res.Statement, Warnings: warnings})
}

func (a *API) getStatement(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, store.CodeStatementNotFound, "statement not found")
	if !ok {
		return
	}
	st, err := a.store.GetStatement(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, st)
}

// intQuery reads a non-negative integer query parameter, or the default when absent.
func intQuery(w http.ResponseWriter, r *http.Request, name string, def int) (int, bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return def, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		validationFailed(w, r, map[string]string{name: "must be a non-negative integer"})
		return 0, false
	}
	return n, true
}

func (a *API) listStatementLines(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, store.CodeStatementNotFound, "statement not found")
	if !ok {
		return
	}
	after, ok := intQuery(w, r, "afterLineNo", 0)
	if !ok {
		return
	}
	limit, ok := intQuery(w, r, "limit", defaultLinePage)
	if !ok {
		return
	}
	if limit == 0 || limit > store.MaxLinesPage {
		limit = store.MaxLinesPage
	}
	items, err := a.store.ListStatementLines(r.Context(), id, after, limit)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var next *int
	if len(items) > 0 && len(items) == limit {
		last := items[len(items)-1].LineNo
		next = &last
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "nextAfterLineNo": next})
}

type createForecastRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	SourceType    string `json:"sourceType"`
	SourceID      string `json:"sourceId"`
	ExpectedDate  string `json:"expectedDate"`
	Amount        string `json:"amount"`
	Currency      string `json:"currency"`
	Confidence    string `json:"confidence"`
}

func (a *API) createForecastEntry(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key, ok := requireKey(w, r)
	if !ok {
		return
	}
	var req createForecastRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !forecastSourceTypes[req.SourceType] {
		fields["sourceType"] = "one of AP, AR, PO, SO, Recurring, Manual, Payroll, Project"
	}
	if len(req.SourceID) > 64 {
		fields["sourceId"] = "at most 64 characters"
	}
	if _, err := domain.Day(req.ExpectedDate); err != nil {
		fields["expectedDate"] = "must be a YYYY-MM-DD date"
	}
	amount, err := decimal.NewFromString(req.Amount)
	if err != nil {
		fields["amount"] = "must be a decimal string; inflows positive, outflows negative"
	}
	if !domain.ValidCurrency(req.Currency) {
		fields["currency"] = "must be an ISO 4217 code"
	}
	confidence := decimal.NewFromInt(1)
	if req.Confidence != "" {
		c, err := decimal.NewFromString(req.Confidence)
		if err != nil || c.IsNegative() || c.GreaterThan(decimal.NewFromInt(1)) {
			fields["confidence"] = "must be a decimal between 0 and 1"
		} else {
			confidence = c
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if outsideScope(w, r, req.LegalEntityID) {
		return
	}
	entry, err := a.store.CreateForecastEntry(r.Context(), store.ForecastEntry{
		LegalEntityID: req.LegalEntityID,
		SourceType:    req.SourceType,
		SourceID:      req.SourceID,
		ExpectedDate:  req.ExpectedDate,
		Amount:        amount.String(),
		Currency:      req.Currency,
		Confidence:    confidence.String(),
	}, key)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	a.forecastEntries.Add(1)
	httpx.JSON(w, http.StatusCreated, entry)
}

// cashForecast buckets the entries of one legal entity (FR-36.9). Entries
// dated up to 90 days before asOf are loaded so overdue items land in the first bucket.
func (a *API) cashForecast(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	le := q.Get("legalEntityId")
	if !ids.Valid(le) {
		validationFailed(w, r, map[string]string{"legalEntityId": "required UUID query parameter"})
		return
	}
	layout := domain.Layout(q.Get("layout"))
	if layout == "" {
		layout = domain.LayoutDaily
	}
	if _, err := layout.Buckets(); err != nil {
		validationFailed(w, r, map[string]string{"layout": "one of daily, weekly, monthly"})
		return
	}
	asOfRaw := q.Get("asOf")
	if asOfRaw == "" {
		asOfRaw = a.now().UTC().Format("2006-01-02")
	}
	asOf, err := domain.Day(asOfRaw)
	if err != nil {
		validationFailed(w, r, map[string]string{"asOf": "must be a YYYY-MM-DD date"})
		return
	}
	currency := q.Get("currency")
	if currency != "" && !domain.ValidCurrency(currency) {
		validationFailed(w, r, map[string]string{"currency": "must be an ISO 4217 code"})
		return
	}
	from := asOf.AddDate(0, 0, -forecastLookbackDays).Format("2006-01-02")
	to := asOf.AddDate(0, 0, forecastLookaheadDay).Format("2006-01-02")
	entries, err := a.store.ListForecastEntries(r.Context(), le, currency, from, to)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	contribs := make([]domain.Contribution, 0, len(entries))
	for _, e := range entries {
		date, err := domain.Day(e.ExpectedDate)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		amount, err := decimal.NewFromString(e.Amount)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		contribs = append(contribs, domain.Contribution{
			SourceType: e.SourceType, SourceID: e.SourceID, ExpectedDate: date, Amount: amount, Currency: e.Currency,
		})
	}
	fc, err := domain.Build(asOf, layout, contribs)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	a.forecastsBuilt.Add(1)
	httpx.JSON(w, http.StatusOK, fc)
}
