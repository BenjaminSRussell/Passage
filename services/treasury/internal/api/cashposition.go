package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/domain"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/store"
)

// CashPositions is the persistence the cash position needs: camt.052 reports and the freshest figures per account.
type CashPositions interface {
	ImportIntradayReport(ctx context.Context, in store.IntradayImport) (store.IntradayResult, error)
	CashPositionAccounts(ctx context.Context, legalEntityID string) ([]store.PositionAccount, error)
}

type cashCounters struct {
	reportsImported atomic.Int64
	reportsReplayed atomic.Int64
	reportsRejected atomic.Int64
	positionsBuilt  atomic.Int64
}

// WithCashPositions mounts the camt.052 import and the cash position routes.
func (a *API) WithCashPositions(c CashPositions) *API {
	a.cash = c
	return a
}

func (a *API) registerCashPosition(r chi.Router) {
	r.With(authz.RequireDuty(dutyStatement)).Post("/v1/treasury/intraday-reports/import", a.importIntradayReport)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/treasury/cash-position", a.cashPosition)
}

func (a *API) cashReady(w http.ResponseWriter, r *http.Request) bool {
	if a.cash == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, "TRS-0526", "the cash position is not enabled in this deployment"))
		return false
	}
	return true
}

type intradayResponse struct {
	store.IntradayReport
}

// importIntradayReport accepts a multipart upload (field "bankAccountId", file "file") holding exactly one camt.052 report.
// The same bytes replay the stored report with 200; a new report is 201.
func (a *API) importIntradayReport(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) || !a.cashReady(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, a.maxFileBytes)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		a.reportsRejected.Add(1)
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusRequestEntityTooLarge, CodeFileTooLarge, "report file exceeds the configured upload limit"))
			return
		}
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "body must be multipart/form-data"))
		return
	}
	bankAccountID := r.FormValue("bankAccountId")
	if !ids.Valid(bankAccountID) {
		a.reportsRejected.Add(1)
		validationFailed(w, r, map[string]string{"bankAccountId": "required UUID form field"})
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		a.reportsRejected.Add(1)
		validationFailed(w, r, map[string]string{"file": "required file part"})
		return
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		a.reportsRejected.Add(1)
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, CodeFileInvalid, "report file could not be read"))
		return
	}
	sum := sha256.Sum256(data)
	parsed, err := domain.ParseCAMT052(data)
	if err == nil && len(parsed) != 1 {
		err = &domain.Error{Code: domain.CodeStatementFormat, Detail: "a file must hold exactly one report (one camt.052 Rpt); split multi-account files"}
	}
	if err != nil {
		a.reportsRejected.Add(1)
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	res, err := a.cash.ImportIntradayReport(r.Context(), store.IntradayImport{BankAccountID: bankAccountID, FileHash: hex.EncodeToString(sum[:]), Parsed: parsed[0]})
	if err != nil {
		a.reportsRejected.Add(1)
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	if res.Replayed {
		a.reportsReplayed.Add(1)
		httpx.JSON(w, http.StatusOK, intradayResponse{res.Report})
		return
	}
	a.reportsImported.Add(1)
	httpx.JSON(w, http.StatusCreated, intradayResponse{res.Report})
}

// positionAccount is one bank account's line of the cash position.
type positionAccount struct {
	BankAccountID string `json:"bankAccountId"`
	AccountCode   string `json:"accountCode"`
	IBANMasked    string `json:"ibanMasked"`
	domain.AccountPosition
}

type positionResponse struct {
	AsOf       time.Time              `json:"asOf"`
	StaleHours int                    `json:"staleHours"`
	Accounts   []positionAccount      `json:"accounts"`
	Totals     []domain.CurrencyTotal `json:"totals"`
}

// cashPosition answers the booked and available balance of every active bank account of a legal entity, from the
// freshest of its latest camt.052 report and its latest camt.053 statement, with totals per currency (FR-36.8).
func (a *API) cashPosition(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) || !a.cashReady(w, r) {
		return
	}
	le := r.URL.Query().Get("legalEntityId")
	if !ids.Valid(le) {
		validationFailed(w, r, map[string]string{"legalEntityId": "required UUID query parameter"})
		return
	}
	accounts, err := a.cash.CashPositionAccounts(r.Context(), le)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	now := a.now().UTC()
	resp := positionResponse{AsOf: now, StaleHours: domain.DefaultStaleHours, Accounts: []positionAccount{}}
	positions := make([]domain.AccountPosition, 0, len(accounts))
	for _, pa := range accounts {
		pos := domain.Position(domain.PositionInput{Account: pa.Account.AccountCode, Currency: pa.Account.Currency, Statement: pa.Statement, Intraday: pa.Intraday}, now, domain.DefaultStaleHours)
		positions = append(positions, pos)
		resp.Accounts = append(resp.Accounts, positionAccount{BankAccountID: pa.Account.ID, AccountCode: pa.Account.AccountCode, IBANMasked: pa.Account.IBANMasked, AccountPosition: pos})
	}
	resp.Totals = domain.Totals(positions)
	a.positionsBuilt.Add(1)
	httpx.JSON(w, http.StatusOK, resp)
}
