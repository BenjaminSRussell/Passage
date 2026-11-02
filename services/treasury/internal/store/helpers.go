package store

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/domain"
)

// Problem codes raised by the check, facility and loan stores (see README).
// They sit in gaps of the PRD's TRS-04xx catalogue (7.36.18) so no PRD code is reused.
const (
	CodeCheckNotFound      = "TRS-0423"
	CodeFacilityDuplicate  = "TRS-0427"
	CodeFacilityNotFound   = "TRS-0428"
	CodeFacilityState      = "TRS-0429"
	CodeInstrumentCurrency = "TRS-0430"
	CodeInstrumentState    = "TRS-0434"
	CodeInstrumentNotFound = "TRS-0435"
	CodeLoanNotFound       = "TRS-0447"
	MaxChecksListed        = 200
)

// unprocessable is a 422 problem with a Passage code.
func unprocessable(code, detail string) error {
	return httpx.NewProblem(http.StatusUnprocessableEntity, code, detail)
}

func parseAmount(code, what, s string) (decimal.Decimal, error) {
	d, err := decimal.NewFromString(strings.TrimSpace(s))
	if err != nil {
		return decimal.Zero, unprocessable(code, what+" is not a decimal amount")
	}
	return d, nil
}

// versionMismatch is the 412 answer to a stale If-Match.
func versionMismatch() error {
	return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "the resource changed; reload and retry with the current ETag")
}

// allocateCheckNumber takes the next gap-free check number of an active
// account inside the caller's transaction and returns the account's legal
// entity. The account row stays locked until the transaction ends, so two
// concurrent issues on one account get consecutive numbers and a rollback
// gives the number back (FR-36.6 gap-free numbering).
func allocateCheckNumber(ctx context.Context, tx pgx.Tx, tenantID, bankAccountID, currency string) (number int64, legalEntityID string, err error) {
	var status, accountCurrency string
	var next int64
	err = tx.QueryRow(ctx, `select legal_entity_id, status, currency, next_check_number from bank_account
		where tenant_id = $1 and id = $2 for update`, tenantID, bankAccountID).Scan(&legalEntityID, &status, &accountCurrency, &next)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", notFound(CodeAccountNotFound, "bank account not found")
	}
	if err != nil {
		return 0, "", err
	}
	if status != string(domain.AccountActive) {
		return 0, "", unprocessable(CodeAccountInactive, "checks can only be issued on an active bank account")
	}
	if accountCurrency != currency {
		return 0, "", unprocessable(domain.CodeCheckCurrency, "check currency "+currency+" differs from the bank account currency "+accountCurrency)
	}
	if _, err := tx.Exec(ctx, `update bank_account set next_check_number = next_check_number + 1 where tenant_id = $1 and id = $2`, tenantID, bankAccountID); err != nil {
		return 0, "", err
	}
	return next, legalEntityID, nil
}
