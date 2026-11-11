package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/domain"
	tevents "github.com/BenjaminSRussell/passage/services/treasury/internal/events"
)

// CashDesk is a physical cash desk of a legal entity in one currency (FR-36.16).
type CashDesk struct {
	ID            string `json:"id"`
	LegalEntityID string `json:"legalEntityId"`
	Code          string `json:"code"`
	Name          string `json:"name"`
	Currency      string `json:"currency"`
	MainAccountID string `json:"mainAccountId"`
	MaxBalance    string `json:"maxBalance"`
	Status        string `json:"status"`
	// DualControl: a day is closed by one person's count and a second person's signature.
	DualControl bool      `json:"dualControl"`
	Version     int       `json:"version"`
	CreatedAt   time.Time `json:"createdAt"`
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
	DualControl   bool
}

// CashDeskDay is one business day of a desk.
type CashDeskDay struct {
	DeskID           string  `json:"deskId"`
	BusinessDate     string  `json:"businessDate"`
	Status           string  `json:"status"`
	OpeningBalance   string  `json:"openingBalance"`
	BookBalance      string  `json:"bookBalance"`
	CountedBalance   *string `json:"countedBalance,omitempty"`
	Difference       *string `json:"difference,omitempty"`
	DifferenceReason string  `json:"differenceReason,omitempty"`
	// Denominations is the cash count by note and coin value, when the count was entered that way.
	Denominations []domain.Denomination `json:"denominations,omitempty"`
	OpenedBy      string                `json:"openedBy"`
	ClosedBy      string                `json:"closedBy,omitempty"`
	// ApprovedBy is the second signature of a dual-control desk.
	ApprovedBy string     `json:"approvedBy,omitempty"`
	OpenedAt   time.Time  `json:"openedAt"`
	ClosedAt   *time.Time `json:"closedAt,omitempty"`
	// DifferencePosting is the ledger state of the cash over or short entry: not_posted, pending or posted.
	DifferencePosting       string `json:"differencePosting"`
	DifferenceLedgerEntryID string `json:"differenceLedgerEntryId,omitempty"`
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
	// CounterAccount is the ledger account on the other side of the cash account; BankAccountID is set instead when the
	// line moves cash to or from a bank account.
	CounterAccount string  `json:"counterAccount,omitempty"`
	BankAccountID  *string `json:"bankAccountId,omitempty"`
	// PostingStatus: not_posted (no ledger was configured), pending (booked, not yet in the ledger) or posted.
	PostingStatus string     `json:"postingStatus"`
	LedgerEntryID string     `json:"ledgerEntryId,omitempty"`
	PostedAt      *time.Time `json:"postedAt,omitempty"`
	// BalanceAfter is the desk's book balance after this line.
	BalanceAfter string `json:"balanceAfter"`
}

// NewCashDeskEntry is a validated journal entry.
type NewCashDeskEntry struct {
	DeskID         string
	Kind           string
	Amount         string
	Description    string
	Reference      string
	CounterAccount string
	BankAccountID  string
	// PostPending books the line as waiting for the ledger (the service has a ledger and a posting profile).
	PostPending bool
}

// CloseCashDeskDay is the count that closes a day.
type CloseDay struct {
	Counted       decimal.Decimal
	Reason        string
	Denominations []domain.Denomination
	// PostDifference books a cash over or short entry in the ledger when the count differs.
	PostDifference bool
}

// UpdateCashDesk changes the fields that are set.
type UpdateCashDesk struct {
	Name          *string
	MaxBalance    *string
	MainAccountID *string
	DualControl   *bool
}

const deskColumns = `id, legal_entity_id, desk_code, name, currency, main_account_id, max_balance::text, status, dual_control, version, created_at`

func scanDesk(row pgx.Row) (CashDesk, error) {
	var d CashDesk
	err := row.Scan(&d.ID, &d.LegalEntityID, &d.Code, &d.Name, &d.Currency, &d.MainAccountID, &d.MaxBalance, &d.Status, &d.DualControl, &d.Version, &d.CreatedAt)
	return d, err
}

const dayColumns = `desk_id, business_date::text, status, opening_balance::text, counted_balance::text, difference::text, coalesce(difference_reason, ''),
	opened_by, coalesce(closed_by, ''), opened_at, closed_at, denominations, coalesce(approved_by, ''), difference_posting_status,
	coalesce(difference_ledger_entry_id, '')`

func scanDay(row pgx.Row) (CashDeskDay, error) {
	var d CashDeskDay
	var den []byte
	err := row.Scan(&d.DeskID, &d.BusinessDate, &d.Status, &d.OpeningBalance, &d.CountedBalance, &d.Difference, &d.DifferenceReason, &d.OpenedBy, &d.ClosedBy,
		&d.OpenedAt, &d.ClosedAt, &den, &d.ApprovedBy, &d.DifferencePosting, &d.DifferenceLedgerEntryID)
	if err == nil && len(den) > 0 {
		err = json.Unmarshal(den, &d.Denominations)
	}
	return d, err
}

const entryColumns = `id, desk_id, business_date::text, line_no, kind, amount::text, description, reference, created_by, created_at,
	coalesce(counter_account, ''), bank_account_id::text, posting_status, coalesce(ledger_entry_id, ''), posted_at`

func scanEntry(row pgx.Row) (CashDeskEntry, error) {
	var e CashDeskEntry
	err := row.Scan(&e.ID, &e.DeskID, &e.BusinessDate, &e.LineNo, &e.Kind, &e.Amount, &e.Description, &e.Reference, &e.CreatedBy, &e.CreatedAt,
		&e.CounterAccount, &e.BankAccountID, &e.PostingStatus, &e.LedgerEntryID, &e.PostedAt)
	return e, err
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
	rows, err := tx.Query(ctx, `select `+entryColumns+`
		from cash_desk_entry where tenant_id = $1 and desk_id = $2 and business_date = $3::date order by line_no`, tenantID, deskID, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CashDeskEntry{}
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
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
			if _, err := tx.Exec(ctx, `insert into cash_desk (tenant_id, id, legal_entity_id, desk_code, name, currency, main_account_id, max_balance, dual_control)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, p.TenantID, id, in.LegalEntityID, in.Code, in.Name, in.Currency, in.MainAccountID, in.MaxBalance, in.DualControl); err != nil {
				return CashDesk{}, err
			}
			d, err := scanDesk(tx.QueryRow(ctx, `select `+deskColumns+` from cash_desk where tenant_id = $1 and id = $2`, p.TenantID, id))
			if err != nil {
				return d, err
			}
			return d, enqueueDesk(ctx, tx, p, tevents.CashDeskCreated, d)
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
	day, err := scanDay(tx.QueryRow(ctx, `select `+dayColumns+` from cash_desk_day where tenant_id = $1 and desk_id = $2 and status in ('open', 'closing')`, tenantID, d.ID))
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
		if err := tx.QueryRow(ctx, `select (select business_date::text from cash_desk_day where tenant_id = $1 and desk_id = $2 and status in ('open', 'closing'))`, p.TenantID, deskID).Scan(&openDate); err != nil {
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
			if desk.Status != "active" {
				return CashDeskEntry{}, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeDeskInactive, "the cash desk is inactive")
			}
			day, err := scanDay(tx.QueryRow(ctx, `select `+dayColumns+` from cash_desk_day where tenant_id = $1 and desk_id = $2 and status in ('open', 'closing')`, p.TenantID, in.DeskID))
			if errors.Is(err, pgx.ErrNoRows) {
				return CashDeskEntry{}, httpx.NewProblem(http.StatusConflict, domain.CodeDeskDayClosed, "the desk has no open day; open one first")
			}
			if err != nil {
				return CashDeskEntry{}, err
			}
			if day.Status != "open" {
				return CashDeskEntry{}, httpx.NewProblem(http.StatusConflict, domain.CodeDeskDayClosed, "the day is counted and waits for the second signature; nothing more can be booked")
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
			posting := "not_posted"
			if in.PostPending {
				posting = "pending"
			}
			if _, err := tx.Exec(ctx, `insert into cash_desk_entry (tenant_id, id, desk_id, business_date, legal_entity_id, line_no, kind, amount, description, reference, created_by,
				counter_account, bank_account_id, posting_status)
				values ($1, $2, $3, $4::date, $5, $6, $7, $8, $9, $10, $11, nullif($12, ''), nullif($13, '')::uuid, $14)`,
				p.TenantID, id, in.DeskID, day.BusinessDate, desk.LegalEntityID, lineNo, in.Kind, in.Amount, in.Description, in.Reference, p.UserID,
				in.CounterAccount, in.BankAccountID, posting); err != nil {
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

// GetCashDeskDay reads one business day of a desk with its book balance.
func (s *Postgres) GetCashDeskDay(ctx context.Context, deskID, date string) (CashDeskDay, error) {
	p, _ := tenant.FromContext(ctx)
	var out CashDeskDay
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanDay(tx.QueryRow(ctx, `select `+dayColumns+` from cash_desk_day where tenant_id = $1 and desk_id = $2 and business_date = $3::date`, p.TenantID, deskID, date))
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound(domain.CodeDeskDayNotFound, "the desk has no such business day")
		}
		if err != nil {
			return err
		}
		entries, err := entriesOf(ctx, tx, p.TenantID, deskID, date)
		if err != nil {
			return err
		}
		out.BookBalance = withBalances(dec(out.OpeningBalance), entries).String()
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

// CloseCashDeskDay counts the open day against the cash. A difference needs a reason (TRS-0542) and is kept; the counted
// cash becomes the next day's opening balance. Entries that never reached the ledger stop the close (TRS-0552). On a
// desk with dual control the day moves to closing and waits for ApproveCashDeskDay; otherwise it is closed here.
func (s *Postgres) CloseCashDeskDay(ctx context.Context, deskID, date string, in CloseDay) (CashDeskDay, error) {
	p, _ := tenant.FromContext(ctx)
	var out CashDeskDay
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		desk, err := lockDesk(ctx, tx, p.TenantID, deskID)
		if err != nil {
			return err
		}
		day, err := scanDay(tx.QueryRow(ctx, `select `+dayColumns+` from cash_desk_day where tenant_id = $1 and desk_id = $2 and business_date = $3::date for update`, p.TenantID, deskID, date))
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound(domain.CodeDeskDayNotFound, "the desk has no such business day")
		}
		if err != nil {
			return err
		}
		if day.Status == "closing" {
			return httpx.NewProblem(http.StatusConflict, domain.CodeDeskDayClosed, "the day is already counted and waits for the second signature")
		}
		if day.Status != "open" {
			return httpx.NewProblem(http.StatusConflict, domain.CodeDeskDayClosed, "the day is already closed")
		}
		var unposted int
		if err := tx.QueryRow(ctx, `select count(*) from cash_desk_entry where tenant_id = $1 and desk_id = $2 and business_date = $3::date and posting_status = 'pending'`,
			p.TenantID, deskID, date).Scan(&unposted); err != nil {
			return err
		}
		if unposted > 0 {
			return httpx.NewProblem(http.StatusConflict, domain.CodeDeskUnposted, fmt.Sprintf("%d journal lines are not in the ledger yet; post them first", unposted))
		}
		entries, err := entriesOf(ctx, tx, p.TenantID, deskID, date)
		if err != nil {
			return err
		}
		book := withBalances(dec(day.OpeningBalance), entries)
		diff, err := domain.CloseDifference(book, in.Counted, in.Reason)
		if err != nil {
			return err
		}
		var den any
		if len(in.Denominations) > 0 {
			raw, err := json.Marshal(in.Denominations)
			if err != nil {
				return err
			}
			den = raw
		}
		status, closedAt := "closed", "now()"
		if desk.DualControl {
			status, closedAt = "closing", "null"
		}
		differencePosting := "not_posted"
		if in.PostDifference && !diff.IsZero() && !desk.DualControl {
			differencePosting = "pending"
		}
		if _, err := tx.Exec(ctx, `update cash_desk_day set status = $9, counted_balance = $4, book_balance = $5, difference = $6, difference_reason = nullif($7, ''),
			closed_by = $8, closed_at = `+closedAt+`, denominations = $10, difference_posting_status = $11 where tenant_id = $1 and desk_id = $2 and business_date = $3::date`,
			p.TenantID, deskID, date, in.Counted, book, diff, in.Reason, p.UserID, status, den, differencePosting); err != nil {
			return err
		}
		out, err = scanDay(tx.QueryRow(ctx, `select `+dayColumns+` from cash_desk_day where tenant_id = $1 and desk_id = $2 and business_date = $3::date`, p.TenantID, deskID, date))
		out.BookBalance = book.String()
		if err == nil && status == "closed" {
			err = enqueueDayClosed(ctx, tx, p, desk, out)
		}
		return err
	})
	return out, err
}

// ApproveCashDeskDay is the second signature: it closes a counted day. The approver must differ from the person who
// counted (TRS-0553).
func (s *Postgres) ApproveCashDeskDay(ctx context.Context, deskID, date string, postDifference bool) (CashDeskDay, error) {
	p, _ := tenant.FromContext(ctx)
	var out CashDeskDay
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		desk, err := lockDesk(ctx, tx, p.TenantID, deskID)
		if err != nil {
			return err
		}
		day, err := scanDay(tx.QueryRow(ctx, `select `+dayColumns+` from cash_desk_day where tenant_id = $1 and desk_id = $2 and business_date = $3::date for update`, p.TenantID, deskID, date))
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound(domain.CodeDeskDayNotFound, "the desk has no such business day")
		}
		if err != nil {
			return err
		}
		if day.Status != "closing" {
			return httpx.NewProblem(http.StatusConflict, domain.CodeDeskNotClosing, "the day is not waiting for a second signature")
		}
		if err := domain.SecondSigner(day.ClosedBy, p.UserID); err != nil {
			return err
		}
		differencePosting := "not_posted"
		if postDifference && day.Difference != nil && !dec(*day.Difference).IsZero() {
			differencePosting = "pending"
		}
		if _, err := tx.Exec(ctx, `update cash_desk_day set status = 'closed', approved_by = $4, approved_at = now(), closed_at = now(), difference_posting_status = $5
			where tenant_id = $1 and desk_id = $2 and business_date = $3::date`, p.TenantID, deskID, date, p.UserID, differencePosting); err != nil {
			return err
		}
		out, err = scanDay(tx.QueryRow(ctx, `select `+dayColumns+` from cash_desk_day where tenant_id = $1 and desk_id = $2 and business_date = $3::date`, p.TenantID, deskID, date))
		if err != nil {
			return err
		}
		entries, err := entriesOf(ctx, tx, p.TenantID, deskID, date)
		if err != nil {
			return err
		}
		out.BookBalance = withBalances(dec(out.OpeningBalance), entries).String()
		return enqueueDayClosed(ctx, tx, p, desk, out)
	})
	return out, err
}

// RejectCashDeskDay sends a counted day back to open, so the cash is recounted. The count is cleared.
func (s *Postgres) RejectCashDeskDay(ctx context.Context, deskID, date string) (CashDeskDay, error) {
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
		if day.Status != "closing" {
			return httpx.NewProblem(http.StatusConflict, domain.CodeDeskNotClosing, "the day is not waiting for a second signature")
		}
		if _, err := tx.Exec(ctx, `update cash_desk_day set status = 'open', counted_balance = null, book_balance = null, difference = null, difference_reason = null,
			closed_by = null, denominations = null where tenant_id = $1 and desk_id = $2 and business_date = $3::date`, p.TenantID, deskID, date); err != nil {
			return err
		}
		out, err = scanDay(tx.QueryRow(ctx, `select `+dayColumns+` from cash_desk_day where tenant_id = $1 and desk_id = $2 and business_date = $3::date`, p.TenantID, deskID, date))
		return err
	})
	return out, err
}

// UpdateCashDesk changes a desk's name, maximum, cash account or dual control. It needs the current version (If-Match). A
// maximum below the cash the open day holds is refused (TRS-0541).
func (s *Postgres) UpdateCashDesk(ctx context.Context, id string, version int, in UpdateCashDesk) (CashDesk, error) {
	p, _ := tenant.FromContext(ctx)
	var out CashDesk
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := lockDesk(ctx, tx, p.TenantID, id)
		if err != nil {
			return err
		}
		if cur.Version != version {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "the cash desk changed; reload and retry")
		}
		if in.MaxBalance != nil {
			if err := fillOpenDay(ctx, tx, p.TenantID, &cur); err != nil {
				return err
			}
			if cur.Balance != nil && dec(*cur.Balance).GreaterThan(dec(*in.MaxBalance)) {
				return httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeDeskOverLimit, "the open day already holds "+*cur.Balance+", more than the new maximum; pay cash to the bank first")
			}
		}
		if _, err := tx.Exec(ctx, `update cash_desk set name = coalesce($4, name), max_balance = coalesce($5::numeric, max_balance), main_account_id = coalesce($6::uuid, main_account_id),
			dual_control = coalesce($7, dual_control), version = version + 1, updated_at = now() where tenant_id = $1 and id = $2 and version = $3`,
			p.TenantID, id, version, in.Name, in.MaxBalance, in.MainAccountID, in.DualControl); err != nil {
			return err
		}
		if out, err = scanDesk(tx.QueryRow(ctx, `select `+deskColumns+` from cash_desk where tenant_id = $1 and id = $2`, p.TenantID, id)); err != nil {
			return err
		}
		return enqueueDesk(ctx, tx, p, tevents.CashDeskUpdated, out)
	})
	return out, err
}

// SetCashDeskStatus activates or deactivates a desk. A desk with an open day cannot be deactivated (TRS-0555).
func (s *Postgres) SetCashDeskStatus(ctx context.Context, id string, version int, status string) (CashDesk, error) {
	p, _ := tenant.FromContext(ctx)
	var out CashDesk
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := lockDesk(ctx, tx, p.TenantID, id)
		if err != nil {
			return err
		}
		if cur.Version != version {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "the cash desk changed; reload and retry")
		}
		if cur.Status == status {
			return httpx.NewProblem(http.StatusConflict, domain.CodeDeskInactive, "the cash desk is already "+status)
		}
		if status == "inactive" {
			if err := fillOpenDay(ctx, tx, p.TenantID, &cur); err != nil {
				return err
			}
			if cur.OpenDate != nil {
				return httpx.NewProblem(http.StatusConflict, domain.CodeDeskHasOpenDay, "the desk has an open day ("+*cur.OpenDate+"); close it first")
			}
		}
		if _, err := tx.Exec(ctx, `update cash_desk set status = $3, version = version + 1, updated_at = now() where tenant_id = $1 and id = $2`, p.TenantID, id, status); err != nil {
			return err
		}
		if out, err = scanDesk(tx.QueryRow(ctx, `select `+deskColumns+` from cash_desk where tenant_id = $1 and id = $2`, p.TenantID, id)); err != nil {
			return err
		}
		typ := tevents.CashDeskUpdated
		if status == "inactive" {
			typ = tevents.CashDeskDeactivated
		}
		return enqueueDesk(ctx, tx, p, typ, out)
	})
	return out, err
}

// DeskPostings is what a day still owes the ledger: its pending journal lines and, when it is closed with a difference
// that was not posted, the cash over or short amount.
type DeskPostings struct {
	Desk    CashDesk
	Day     CashDeskDay
	Entries []CashDeskEntry
	// Difference is counted less book, set while the difference entry is pending.
	Difference *string
}

// PendingDeskPostings lists what is waiting for the ledger on one day.
func (s *Postgres) PendingDeskPostings(ctx context.Context, deskID, date string) (DeskPostings, error) {
	p, _ := tenant.FromContext(ctx)
	var out DeskPostings
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if out.Desk, err = scanDesk(tx.QueryRow(ctx, `select `+deskColumns+` from cash_desk where tenant_id = $1 and id = $2`, p.TenantID, deskID)); errors.Is(err, pgx.ErrNoRows) {
			return deskNotFound()
		} else if err != nil {
			return err
		}
		out.Day, err = scanDay(tx.QueryRow(ctx, `select `+dayColumns+` from cash_desk_day where tenant_id = $1 and desk_id = $2 and business_date = $3::date`, p.TenantID, deskID, date))
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound(domain.CodeDeskDayNotFound, "the desk has no such business day")
		}
		if err != nil {
			return err
		}
		all, err := entriesOf(ctx, tx, p.TenantID, deskID, date)
		if err != nil {
			return err
		}
		for _, e := range all {
			if e.PostingStatus == "pending" {
				out.Entries = append(out.Entries, e)
			}
		}
		if out.Day.DifferencePosting == "pending" {
			out.Difference = out.Day.Difference
		}
		return nil
	})
	return out, err
}

// MarkDeskEntryPosted records the ledger entry of a journal line and publishes cashdeskentry.posted. Marking a line that
// is already posted changes nothing.
func (s *Postgres) MarkDeskEntryPosted(ctx context.Context, deskID, entryID, ledgerEntryID string) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		desk, err := lockDesk(ctx, tx, p.TenantID, deskID)
		if err != nil {
			return err
		}
		e, err := scanEntry(tx.QueryRow(ctx, `update cash_desk_entry set posting_status = 'posted', ledger_entry_id = $4, posted_at = now()
			where tenant_id = $1 and desk_id = $2 and id = $3 and posting_status = 'pending' returning `+entryColumns, p.TenantID, deskID, entryID, ledgerEntryID))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		evt, err := events.New(tevents.CashDeskEntryPosted, eventSource, deskID, p.TenantID, tevents.CashDeskEntryPostedData{DeskID: deskID, EntryID: e.ID,
			LegalEntityID: desk.LegalEntityID, BusinessDate: e.BusinessDate, Kind: e.Kind, Amount: e.Amount, Currency: desk.Currency, LedgerEntryID: ledgerEntryID})
		if err != nil {
			return err
		}
		return outbox.Enqueue(ctx, tx, deskAggType, deskID, evt)
	})
}

// MarkDeskDifferencePosted records the ledger entry of the cash over or short amount of a closed day.
func (s *Postgres) MarkDeskDifferencePosted(ctx context.Context, deskID, date, ledgerEntryID string) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update cash_desk_day set difference_posting_status = 'posted', difference_ledger_entry_id = $4
			where tenant_id = $1 and desk_id = $2 and business_date = $3::date and difference_posting_status = 'pending'`, p.TenantID, deskID, date, ledgerEntryID)
		return err
	})
}

const deskAggType = "cashdesk"

func enqueueDesk(ctx context.Context, tx pgx.Tx, p tenant.Principal, typ string, d CashDesk) error {
	evt, err := events.New(typ, eventSource, d.ID, p.TenantID, tevents.CashDeskData{DeskID: d.ID, LegalEntityID: d.LegalEntityID, Code: d.Code, Currency: d.Currency,
		Status: d.Status, MaxBalance: d.MaxBalance, DualControl: d.DualControl})
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, deskAggType, d.ID, evt)
}

func enqueueDayClosed(ctx context.Context, tx pgx.Tx, p tenant.Principal, d CashDesk, day CashDeskDay) error {
	counted, diff := "", ""
	if day.CountedBalance != nil {
		counted = *day.CountedBalance
	}
	if day.Difference != nil {
		diff = *day.Difference
	}
	evt, err := events.New(tevents.CashDeskDayClosed, eventSource, d.ID, p.TenantID, tevents.CashDeskDayClosedData{DeskID: d.ID, LegalEntityID: d.LegalEntityID,
		BusinessDate: day.BusinessDate, Currency: d.Currency, OpeningBalance: day.OpeningBalance, BookBalance: day.BookBalance, CountedBalance: counted, Difference: diff,
		ClosedBy: day.ClosedBy, ApprovedBy: day.ApprovedBy})
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, deskAggType, d.ID, evt)
}
