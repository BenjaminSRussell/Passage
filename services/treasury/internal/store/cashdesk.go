package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/domain"
)

// CashDesk is a physical cash desk of a legal entity in one currency (FR-36.16).
type CashDesk struct {
	ID            string    `json:"id"`
	LegalEntityID string    `json:"legalEntityId"`
	Code          string    `json:"code"`
	Name          string    `json:"name"`
	Currency      string    `json:"currency"`
	MainAccountID string    `json:"mainAccountId"`
	MaxBalance    string    `json:"maxBalance"`
	Status        string    `json:"status"`
	Version       int       `json:"version"`
	CreatedAt     time.Time `json:"createdAt"`
	// Balance and OpenDate describe the open day, when there is one.
	OpenDate *string `json:"openDate,omitempty"`
	Balance  *string `json:"balance,omitempty"`
}

// NewCashDesk is a validated desk create request.
type NewCashDesk struct {
	LegalEntityID string
	Code          string
	Name          string
	Currency      string
	MainAccountID string
	MaxBalance    string
}

// CashDeskDay is one business day of a desk.
type CashDeskDay struct {
	DeskID           string     `json:"deskId"`
	BusinessDate     string     `json:"businessDate"`
	Status           string     `json:"status"`
	OpeningBalance   string     `json:"openingBalance"`
	BookBalance      string     `json:"bookBalance"`
	CountedBalance   *string    `json:"countedBalance,omitempty"`
	Difference       *string    `json:"difference,omitempty"`
	DifferenceReason string     `json:"differenceReason,omitempty"`
	OpenedBy         string     `json:"openedBy"`
	ClosedBy         string     `json:"closedBy,omitempty"`
	OpenedAt         time.Time  `json:"openedAt"`
	ClosedAt         *time.Time `json:"closedAt,omitempty"`
}

// CashDeskEntry is one line of the cash journal.
type CashDeskEntry struct {
	ID           string    `json:"id"`
	DeskID       string    `json:"deskId"`
	BusinessDate string    `json:"businessDate"`
	LineNo       int       `json:"lineNo"`
	Kind         string    `json:"kind"`
	Amount       string    `json:"amount"`
	Description  string    `json:"description"`
	Reference    string    `json:"reference,omitempty"`
	CreatedBy    string    `json:"createdBy"`
	CreatedAt    time.Time `json:"createdAt"`
	// BalanceAfter is the desk's book balance after this line.
	BalanceAfter string `json:"balanceAfter"`
}

// NewCashDeskEntry is a validated journal entry.
type NewCashDeskEntry struct {
	DeskID      string
	Kind        string
	Amount      string
	Description string
	Reference   string
}

const deskColumns = `id, legal_entity_id, desk_code, name, currency, main_account_id, max_balance::text, status, version, created_at`

func scanDesk(row pgx.Row) (CashDesk, error) {
	var d CashDesk
	err := row.Scan(&d.ID, &d.LegalEntityID, &d.Code, &d.Name, &d.Currency, &d.MainAccountID, &d.MaxBalance, &d.Status, &d.Version, &d.CreatedAt)
	return d, err
}

const dayColumns = `desk_id, business_date::text, status, opening_balance::text, counted_balance::text, difference::text, coalesce(difference_reason, ''),
	opened_by, coalesce(closed_by, ''), opened_at, closed_at`

func scanDay(row pgx.Row) (CashDeskDay, error) {
	var d CashDeskDay
	err := row.Scan(&d.DeskID, &d.BusinessDate, &d.Status, &d.OpeningBalance, &d.CountedBalance, &d.Difference, &d.DifferenceReason, &d.OpenedBy, &d.ClosedBy, &d.OpenedAt, &d.ClosedAt)
	return d, err
}

func deskNotFound() error { return notFound(domain.CodeDeskNotFound, "cash desk not found") }

func lockDesk(ctx context.Context, tx pgx.Tx, tenantID, id string) (CashDesk, error) {
	d, err := scanDesk(tx.QueryRow(ctx, `select `+deskColumns+` from cash_desk where tenant_id = $1 and id = $2 for update`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return d, deskNotFound()
	}
	return d, err
}

func entriesOf(ctx context.Context, tx pgx.Tx, tenantID, deskID, date string) ([]CashDeskEntry, error) {
	rows, err := tx.Query(ctx, `select id, desk_id, business_date::text, line_no, kind, amount::text, description, reference, created_by, created_at
		from cash_desk_entry where tenant_id = $1 and desk_id = $2 and business_date = $3::date order by line_no`, tenantID, deskID, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CashDeskEntry{}
	for rows.Next() {
		var e CashDeskEntry
		if err := rows.Scan(&e.ID, &e.DeskID, &e.BusinessDate, &e.LineNo, &e.Kind, &e.Amount, &e.Description, &e.Reference, &e.CreatedBy, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func dec(s string) decimal.Decimal {
	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Zero
	}
	return d
}

// withBalances fills BalanceAfter on the entries and returns the day's book balance.
func withBalances(opening decimal.Decimal, entries []CashDeskEntry) decimal.Decimal {
	b := opening
	for i := range entries {
		b = domain.DeskBalance(b, []domain.DeskEntry{{Kind: entries[i].Kind, Amount: dec(entries[i].Amount)}})
		entries[i].BalanceAfter = b.String()
	}
	return b
}

// CreateCashDesk stores an active desk; a repeated Idempotency-Key replays it.
func (s *Postgres) CreateCashDesk(ctx context.Context, in NewCashDesk, key string) (CashDesk, error) {
	p, _ := tenant.FromContext(ctx)
	reqHash, err := requestHash("create-cash-desk", in)
	if err != nil {
		return CashDesk{}, err
	}
	var out CashDesk
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = idempotent(ctx, tx, key, reqHash, func() (CashDesk, error) {
			id := ids.New()
			if _, err := tx.Exec(ctx, `insert into cash_desk (tenant_id, id, legal_entity_id, desk_code, name, currency, main_account_id, max_balance)
				values ($1, $2, $3, $4, $5, $6, $7, $8)`, p.TenantID, id, in.LegalEntityID, in.Code, in.Name, in.Currency, in.MainAccountID, in.MaxBalance); err != nil {
				return CashDesk{}, err
			}
			return scanDesk(tx.QueryRow(ctx, `select `+deskColumns+` from cash_desk where tenant_id = $1 and id = $2`, p.TenantID, id))
		})
		return err
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return CashDesk{}, httpx.NewProblem(http.StatusConflict, "TRS-0550", "a cash desk with this code exists in the legal entity")
	}
	return out, err
}

// ListCashDesks lists the desks of a legal entity with the balance of the open day.
func (s *Postgres) ListCashDesks(ctx context.Context, legalEntityID string) ([]CashDesk, error) {
	p, _ := tenant.FromContext(ctx)
	out := []CashDesk{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+deskColumns+` from cash_desk where tenant_id = $1 and legal_entity_id = $2 order by desk_code`, p.TenantID, legalEntityID)
		if err != nil {
			return err
		}
		for rows.Next() {
			d, err := scanDesk(rows)
			if err != nil {
				rows.Close()
				return err
			}
			out = append(out, d)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for i := range out {
			if err := fillOpenDay(ctx, tx, p.TenantID, &out[i]); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

func fillOpenDay(ctx context.Context, tx pgx.Tx, tenantID string, d *CashDesk) error {
	day, err := scanDay(tx.QueryRow(ctx, `select `+dayColumns+` from cash_desk_day where tenant_id = $1 and desk_id = $2 and status = 'open'`, tenantID, d.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	entries, err := entriesOf(ctx, tx, tenantID, d.ID, day.BusinessDate)
	if err != nil {
		return err
	}
	bal := withBalances(dec(day.OpeningBalance), entries).String()
	d.OpenDate, d.Balance = &day.BusinessDate, &bal
	return nil
}

// GetCashDesk reads one desk with its open day's balance.
func (s *Postgres) GetCashDesk(ctx context.Context, id string) (CashDesk, error) {
	p, _ := tenant.FromContext(ctx)
	var d CashDesk
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		d, err = scanDesk(tx.QueryRow(ctx, `select `+deskColumns+` from cash_desk where tenant_id = $1 and id = $2`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return deskNotFound()
		}
		if err != nil {
			return err
		}
		return fillOpenDay(ctx, tx, p.TenantID, &d)
	})
	return d, err
}

// OpenCashDeskDay opens a business day. The opening balance is the counted cash of the last closed day (zero for a
// new desk). Refused while another day is open (TRS-0543) and on or before the last closed day (TRS-0545).
func (s *Postgres) OpenCashDeskDay(ctx context.Context, deskID, date string) (CashDeskDay, error) {
	p, _ := tenant.FromContext(ctx)
	var out CashDeskDay
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		desk, err := lockDesk(ctx, tx, p.TenantID, deskID)
		if err != nil {
			return err
		}
		if desk.Status != "active" {
			return httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeDeskInactive, "the cash desk is inactive")
		}
		var openDate *string
		if err := tx.QueryRow(ctx, `select (select business_date::text from cash_desk_day where tenant_id = $1 and desk_id = $2 and status = 'open')`, p.TenantID, deskID).Scan(&openDate); err != nil {
			return err
		}
		if openDate != nil {
			return httpx.NewProblem(http.StatusConflict, domain.CodeDeskDayOpen, "the desk already has an open day ("+*openDate+"); close it first")
		}
		opening := "0"
		var lastDate, counted *string
		if err := tx.QueryRow(ctx, `select business_date::text, counted_balance::text from cash_desk_day where tenant_id = $1 and desk_id = $2 and status = 'closed'
			order by business_date desc limit 1`, p.TenantID, deskID).Scan(&lastDate, &counted); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if lastDate != nil {
			if date <= *lastDate {
				return httpx.NewProblem(http.StatusConflict, domain.CodeDeskDateOrder, "the last closed day is "+*lastDate+"; open a later day")
			}
			opening = *counted
		}
		if _, err := tx.Exec(ctx, `insert into cash_desk_day (tenant_id, desk_id, business_date, legal_entity_id, opening_balance, opened_by) values ($1, $2, $3::date, $4, $5, $6)`,
			p.TenantID, deskID, date, desk.LegalEntityID, opening, p.UserID); err != nil {
			return err
		}
		out, err = scanDay(tx.QueryRow(ctx, `select `+dayColumns+` from cash_desk_day where tenant_id = $1 and desk_id = $2 and business_date = $3::date`, p.TenantID, deskID, date))
		out.BookBalance = out.OpeningBalance
		return err
	})
	return out, err
}

// AddCashDeskEntry books a receipt or payment on the desk's open day. The balance rules run under the desk row lock,
// so two payments cannot both pass on the same cash.
func (s *Postgres) AddCashDeskEntry(ctx context.Context, in NewCashDeskEntry, key string) (CashDeskEntry, error) {
	p, _ := tenant.FromContext(ctx)
	reqHash, err := requestHash("cash-desk-entry", in)
	if err != nil {
		return CashDeskEntry{}, err
	}
	var out CashDeskEntry
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = idempotent(ctx, tx, key, reqHash, func() (CashDeskEntry, error) {
			desk, err := lockDesk(ctx, tx, p.TenantID, in.DeskID)
			if err != nil {
				return CashDeskEntry{}, err
			}
			day, err := scanDay(tx.QueryRow(ctx, `select `+dayColumns+` from cash_desk_day where tenant_id = $1 and desk_id = $2 and status = 'open'`, p.TenantID, in.DeskID))
			if errors.Is(err, pgx.ErrNoRows) {
				return CashDeskEntry{}, httpx.NewProblem(http.StatusConflict, domain.CodeDeskDayClosed, "the desk has no open day; open one first")
			}
			if err != nil {
				return CashDeskEntry{}, err
			}
			entries, err := entriesOf(ctx, tx, p.TenantID, in.DeskID, day.BusinessDate)
			if err != nil {
				return CashDeskEntry{}, err
			}
			balance := withBalances(dec(day.OpeningBalance), entries)
			if err := domain.CheckDeskEntry(balance, domain.DeskEntry{Kind: in.Kind, Amount: dec(in.Amount)}, dec(desk.MaxBalance)); err != nil {
				return CashDeskEntry{}, err
			}
			id := ids.New()
			lineNo := len(entries) + 1
			if _, err := tx.Exec(ctx, `insert into cash_desk_entry (tenant_id, id, desk_id, business_date, legal_entity_id, line_no, kind, amount, description, reference, created_by)
				values ($1, $2, $3, $4::date, $5, $6, $7, $8, $9, $10, $11)`,
				p.TenantID, id, in.DeskID, day.BusinessDate, desk.LegalEntityID, lineNo, in.Kind, in.Amount, in.Description, in.Reference, p.UserID); err != nil {
				return CashDeskEntry{}, err
			}
			entries, err = entriesOf(ctx, tx, p.TenantID, in.DeskID, day.BusinessDate)
			if err != nil {
				return CashDeskEntry{}, err
			}
			withBalances(dec(day.OpeningBalance), entries)
			return entries[len(entries)-1], nil
		})
		return err
	})
	return out, err
}

// ListCashDeskDays lists a desk's days, newest first.
func (s *Postgres) ListCashDeskDays(ctx context.Context, deskID string) ([]CashDeskDay, error) {
	p, _ := tenant.FromContext(ctx)
	out := []CashDeskDay{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := scanDesk(tx.QueryRow(ctx, `select `+deskColumns+` from cash_desk where tenant_id = $1 and id = $2`, p.TenantID, deskID)); errors.Is(err, pgx.ErrNoRows) {
			return deskNotFound()
		} else if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select `+dayColumns+` from cash_desk_day where tenant_id = $1 and desk_id = $2 order by business_date desc limit 100`, p.TenantID, deskID)
		if err != nil {
			return err
		}
		var days []CashDeskDay
		for rows.Next() {
			d, err := scanDay(rows)
			if err != nil {
				rows.Close()
				return err
			}
			days = append(days, d)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, d := range days {
			entries, err := entriesOf(ctx, tx, p.TenantID, deskID, d.BusinessDate)
			if err != nil {
				return err
			}
			d.BookBalance = withBalances(dec(d.OpeningBalance), entries).String()
			out = append(out, d)
		}
		return nil
	})
	return out, err
}

// ListCashDeskEntries is the journal of one day, in line order, each line with the balance after it.
func (s *Postgres) ListCashDeskEntries(ctx context.Context, deskID, date string) ([]CashDeskEntry, error) {
	p, _ := tenant.FromContext(ctx)
	var out []CashDeskEntry
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		day, err := scanDay(tx.QueryRow(ctx, `select `+dayColumns+` from cash_desk_day where tenant_id = $1 and desk_id = $2 and business_date = $3::date`, p.TenantID, deskID, date))
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound(domain.CodeDeskDayNotFound, "the desk has no such business day")
		}
		if err != nil {
			return err
		}
		if out, err = entriesOf(ctx, tx, p.TenantID, deskID, date); err != nil {
			return err
		}
		withBalances(dec(day.OpeningBalance), out)
		return nil
	})
	return out, err
}

// CloseCashDeskDay closes the open day against the counted cash. A difference needs a reason (TRS-0542) and is kept;
// the counted cash becomes the next day's opening balance.
func (s *Postgres) CloseCashDeskDay(ctx context.Context, deskID, date string, counted decimal.Decimal, reason string) (CashDeskDay, error) {
	p, _ := tenant.FromContext(ctx)
	var out CashDeskDay
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := lockDesk(ctx, tx, p.TenantID, deskID); err != nil {
			return err
		}
		day, err := scanDay(tx.QueryRow(ctx, `select `+dayColumns+` from cash_desk_day where tenant_id = $1 and desk_id = $2 and business_date = $3::date for update`, p.TenantID, deskID, date))
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound(domain.CodeDeskDayNotFound, "the desk has no such business day")
		}
		if err != nil {
			return err
		}
		if day.Status != "open" {
			return httpx.NewProblem(http.StatusConflict, domain.CodeDeskDayClosed, "the day is already closed")
		}
		entries, err := entriesOf(ctx, tx, p.TenantID, deskID, date)
		if err != nil {
			return err
		}
		book := withBalances(dec(day.OpeningBalance), entries)
		diff, err := domain.CloseDifference(book, counted, reason)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update cash_desk_day set status = 'closed', counted_balance = $4, book_balance = $5, difference = $6, difference_reason = nullif($7, ''),
			closed_by = $8, closed_at = now() where tenant_id = $1 and desk_id = $2 and business_date = $3::date`,
			p.TenantID, deskID, date, counted, book, diff, reason, p.UserID); err != nil {
			return err
		}
		out, err = scanDay(tx.QueryRow(ctx, `select `+dayColumns+` from cash_desk_day where tenant_id = $1 and desk_id = $2 and business_date = $3::date`, p.TenantID, deskID, date))
		out.BookBalance = book.String()
		return err
	})
	return out, err
}
