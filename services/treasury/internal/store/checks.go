package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/domain"
	tevents "github.com/BenjaminSRussell/passage/services/treasury/internal/events"
)

const checkColumns = `id, legal_entity_id, bank_account_id, check_number, payee, amount::text, currency, status,
	void_reason, replaces_id, replaced_by_id, version, created_at, updated_at`

// Check is an issued check drawn on a bank account (FR-36.6). Numbers are gap-free per account.
type Check struct {
	ID            string    `json:"id"`
	LegalEntityID string    `json:"legalEntityId"`
	BankAccountID string    `json:"bankAccountId"`
	CheckNumber   int64     `json:"checkNumber"`
	Payee         string    `json:"payee"`
	Amount        string    `json:"amount"`
	Currency      string    `json:"currency"`
	Status        string    `json:"status"`
	VoidReason    *string   `json:"voidReason,omitempty"`
	ReplacesID    *string   `json:"replacesId,omitempty"`
	ReplacedByID  *string   `json:"replacedById,omitempty"`
	Version       int       `json:"version"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// NewCheck is a validated issue request. The legal entity comes from the bank account.
type NewCheck struct {
	BankAccountID string
	Payee         string
	Amount        string
	Currency      string
}

// checkDest lists the scan targets for checkColumns, in order.
func checkDest(c *Check) []any {
	return []any{&c.ID, &c.LegalEntityID, &c.BankAccountID, &c.CheckNumber, &c.Payee, &c.Amount, &c.Currency, &c.Status,
		&c.VoidReason, &c.ReplacesID, &c.ReplacedByID, &c.Version, &c.CreatedAt, &c.UpdatedAt}
}

func scanCheck(row pgx.Row) (Check, error) {
	var c Check
	err := row.Scan(checkDest(&c)...)
	return c, err
}

// IssueCheck allocates the next number of the account and records the check
// as Created. A repeated Idempotency-Key replays the stored check; reusing it
// with another body is refused.
func (s *Postgres) IssueCheck(ctx context.Context, in NewCheck, key string) (Check, error) {
	p, _ := tenant.FromContext(ctx)
	reqHash, err := requestHash("issue-check", in)
	if err != nil {
		return Check{}, err
	}
	var out Check
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = idempotent(ctx, tx, key, reqHash, func() (Check, error) {
			number, legalEntityID, err := allocateCheckNumber(ctx, tx, p.TenantID, in.BankAccountID, in.Currency)
			if err != nil {
				return Check{}, err
			}
			return scanCheck(tx.QueryRow(ctx, `insert into bank_check (tenant_id, id, legal_entity_id, bank_account_id, check_number, payee, amount, currency)
				values ($1, $2, $3, $4, $5, $6, $7, $8) returning `+checkColumns,
				p.TenantID, ids.New(), legalEntityID, in.BankAccountID, number, in.Payee, in.Amount, in.Currency))
		})
		return err
	})
	return out, err
}

// ListChecks returns the most recent checks of one bank account (the check register), newest number first.
func (s *Postgres) ListChecks(ctx context.Context, bankAccountID string) ([]Check, error) {
	p, _ := tenant.FromContext(ctx)
	items := []Check{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+checkColumns+` from bank_check where tenant_id = $1 and bank_account_id = $2
			order by check_number desc limit $3`, p.TenantID, bankAccountID, MaxChecksListed)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanCheck(rows)
			if err != nil {
				return err
			}
			items = append(items, c)
		}
		return rows.Err()
	})
	return items, err
}

// GetCheck returns one check.
func (s *Postgres) GetCheck(ctx context.Context, id string) (Check, error) {
	p, _ := tenant.FromContext(ctx)
	var out Check
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanCheck(tx.QueryRow(ctx, `select `+checkColumns+` from bank_check where tenant_id = $1 and id = $2`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound(CodeCheckNotFound, "check not found")
		}
		return err
	})
	return out, err
}

// lockCheck loads a check for update and enforces the If-Match version.
func lockCheck(ctx context.Context, tx pgx.Tx, tenantID, id string, version int) (Check, error) {
	c, err := scanCheck(tx.QueryRow(ctx, `select `+checkColumns+` from bank_check where tenant_id = $1 and id = $2 for update`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Check{}, notFound(CodeCheckNotFound, "check not found")
	}
	if err != nil {
		return Check{}, err
	}
	if c.Version != version {
		return Check{}, versionMismatch()
	}
	return c, nil
}

// PrintCheck moves a created check to Printed.
func (s *Postgres) PrintCheck(ctx context.Context, id string, version int) (Check, error) {
	return s.moveCheck(ctx, id, version, domain.CheckPrinted, nil)
}

// SendCheck records that a printed check left the company (mailed or handed over).
func (s *Postgres) SendCheck(ctx context.Context, id string, version int) (Check, error) {
	return s.moveCheck(ctx, id, version, domain.CheckSent, nil)
}

// ClearCheck records that the bank paid a sent check. Reconciliation (FR-36.3)
// will call this when a statement line matches the check; until then a user records it.
func (s *Postgres) ClearCheck(ctx context.Context, id string, version int) (Check, error) {
	return s.moveCheck(ctx, id, version, domain.CheckCleared, nil)
}

// StopCheck records a stop-payment order on a sent check.
func (s *Postgres) StopCheck(ctx context.Context, id string, version int) (Check, error) {
	return s.moveCheck(ctx, id, version, domain.CheckStopped, nil)
}

// VoidCheck voids a check and publishes check.voided for positive pay (FR-36.6, BR-36.5).
func (s *Postgres) VoidCheck(ctx context.Context, id string, version int, reason string) (Check, error) {
	return s.moveCheck(ctx, id, version, domain.CheckVoided, &reason)
}

// moveCheck applies one lifecycle transition under the optimistic version check.
func (s *Postgres) moveCheck(ctx context.Context, id string, version int, to domain.CheckStatus, reason *string) (Check, error) {
	p, _ := tenant.FromContext(ctx)
	var out Check
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := lockCheck(ctx, tx, p.TenantID, id, version)
		if err != nil {
			return err
		}
		if err := domain.TransitionCheck(domain.CheckStatus(cur.Status), to); err != nil {
			return err
		}
		out, err = scanCheck(tx.QueryRow(ctx, `update bank_check set status = $3, void_reason = coalesce($4, void_reason),
			version = version + 1, updated_at = now() where tenant_id = $1 and id = $2 returning `+checkColumns,
			p.TenantID, id, string(to), reason))
		if err != nil {
			return err
		}
		if to == domain.CheckVoided {
			return enqueueCheckVoided(ctx, tx, p.TenantID, cur, reasonText(reason))
		}
		return nil
	})
	return out, err
}

// ReprintCheck voids a check and issues a new one with the same payee and
// amount on the next number of the same account, in one transaction (AC-36.5).
// The old number stays consumed; the new check is Printed and points back to it.
func (s *Postgres) ReprintCheck(ctx context.Context, id string, version int, reason string) (Check, error) {
	p, _ := tenant.FromContext(ctx)
	var fresh Check
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		old, err := lockCheck(ctx, tx, p.TenantID, id, version)
		if err != nil {
			return err
		}
		if err := domain.TransitionCheck(domain.CheckStatus(old.Status), domain.CheckVoided); err != nil {
			return err
		}
		number, legalEntityID, err := allocateCheckNumber(ctx, tx, p.TenantID, old.BankAccountID, old.Currency)
		if err != nil {
			return err
		}
		fresh, err = scanCheck(tx.QueryRow(ctx, `insert into bank_check (tenant_id, id, legal_entity_id, bank_account_id, check_number, payee, amount, currency,
			status, replaces_id)
			values ($1, $2, $3, $4, $5, $6, $7, $8, 'printed', $9) returning `+checkColumns,
			p.TenantID, ids.New(), legalEntityID, old.BankAccountID, number, old.Payee, old.Amount, old.Currency, old.ID))
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update bank_check set status = 'voided', void_reason = $3, replaced_by_id = $4,
			version = version + 1, updated_at = now() where tenant_id = $1 and id = $2`,
			p.TenantID, old.ID, reason, fresh.ID); err != nil {
			return err
		}
		return enqueueCheckVoided(ctx, tx, p.TenantID, old, reason)
	})
	return fresh, err
}

func reasonText(reason *string) string {
	if reason == nil {
		return ""
	}
	return *reason
}

// enqueueCheckVoided writes check.voided to the outbox in the voiding transaction.
func enqueueCheckVoided(ctx context.Context, tx pgx.Tx, tenantID string, c Check, reason string) error {
	evt, err := events.New(tevents.CheckVoided, eventSource, c.ID, tenantID, tevents.CheckVoidedData{
		CheckID: c.ID, BankAccountID: c.BankAccountID, Number: c.CheckNumber, Reason: reason,
		PositivePayVoid: domain.NeedsPositivePayVoid(domain.CheckStatus(c.Status)),
	})
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, "check", c.ID, evt)
}
