package store

import (
	"context"
	"fmt"
	"regexp"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
)

// CodeBankAccountNotFound is returned when a bank account mapping does not exist.
const CodeBankAccountNotFound = "AR-BANK-NOT-FOUND"

var glAccountPattern = regexp.MustCompile(`^[0-9A-Za-z._-]{1,20}$`)

// BankAccount maps one bank account of a legal entity to the ledger account its cash is booked to (30-2). AccountRef is the
// statement's account id as domain.NormalizeBankAccount writes it; empty makes this the entity's default account for the currency.
type BankAccount struct {
	LegalEntityID string `json:"legalEntityId"`
	Currency      string `json:"currency"`
	AccountRef    string `json:"accountRef"`
	GLAccount     string `json:"glAccount"`
	Name          string `json:"name"`
}

// ValidateBankAccount checks a mapping; the account ref is normalized in place.
func ValidateBankAccount(b *BankAccount) error {
	b.AccountRef = domain.NormalizeBankAccount(b.AccountRef)
	switch {
	case !currencyRE.MatchString(b.Currency):
		return fieldError("currency", "must be an ISO 4217 code such as USD")
	case len(b.AccountRef) > 40:
		return fieldError("accountRef", "must be at most 40 letters and digits")
	case !glAccountPattern.MatchString(b.GLAccount):
		return fieldError("glAccount", "must be a ledger account number of at most 20 letters, digits, dots, dashes or underscores")
	case len(b.Name) > 100:
		return fieldError("name", "must be at most 100 characters")
	}
	return nil
}

var currencyRE = regexp.MustCompile(`^[A-Z]{3}$`)

func fieldError(field, rule string) error {
	p := httpx.NewProblem(400, "COM-0001", "invalid bank account")
	p.Errors = map[string]string{field: rule}
	return p
}

// ListBankAccounts lists the mappings of the caller's legal-entity scope, by entity, currency and account.
func (s *Postgres) ListBankAccounts(ctx context.Context, legalEntityID string) ([]BankAccount, error) {
	out := []BankAccount{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select legal_entity_id::text, currency::text, account_ref, gl_account, name from ar_bank_account
			where ($1::text = '' or legal_entity_id::text = $1::text) order by legal_entity_id, currency, account_ref`, legalEntityID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var b BankAccount
			if err := rows.Scan(&b.LegalEntityID, &b.Currency, &b.AccountRef, &b.GLAccount, &b.Name); err != nil {
				return err
			}
			out = append(out, b)
		}
		return rows.Err()
	})
	return out, err
}

// PutBankAccount creates or replaces a mapping.
func (s *Postgres) PutBankAccount(ctx context.Context, b BankAccount) error {
	p, _ := tenant.FromContext(ctx)
	if err := ValidateBankAccount(&b); err != nil {
		return err
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into ar_bank_account (tenant_id, legal_entity_id, currency, account_ref, gl_account, name)
			values ($1, $2, $3, $4, $5, $6)
			on conflict (tenant_id, legal_entity_id, currency, account_ref) do update set gl_account = excluded.gl_account, name = excluded.name, updated_at = now()`,
			p.TenantID, b.LegalEntityID, b.Currency, b.AccountRef, b.GLAccount, b.Name)
		return err
	})
}

// DeleteBankAccount removes a mapping; the cash it covered books to 1000 again.
func (s *Postgres) DeleteBankAccount(ctx context.Context, legalEntityID, currency, accountRef string) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `delete from ar_bank_account where tenant_id = $1 and legal_entity_id = $2 and currency = $3 and account_ref = $4`,
			p.TenantID, legalEntityID, currency, domain.NormalizeBankAccount(accountRef))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.NewProblem(404, CodeBankAccountNotFound, "no such bank account mapping")
		}
		return nil
	})
}

// bankLedgerAccount is the ledger account the cash of a legal entity in a currency books to: the mapping of the bank account the
// money came to, else the entity's default for the currency, else the fixed 1000. ref may be empty.
func bankLedgerAccount(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID, currency, ref string) (string, error) {
	var account string
	err := tx.QueryRow(ctx, `select gl_account from ar_bank_account where tenant_id = $1 and legal_entity_id = $2 and currency = $3
		and account_ref in ($4, '') order by account_ref desc limit 1`, tenantID, legalEntityID, currency, ref).Scan(&account)
	if err == pgx.ErrNoRows {
		return domain.AccountBank, nil
	}
	if err != nil {
		return "", fmt.Errorf("bank account lookup: %w", err)
	}
	return account, nil
}
