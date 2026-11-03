package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
	ledgerevents "github.com/BenjaminSRussell/passage/services/ledger/internal/events"
)

// Recurring journal statuses and error codes (FR-11.10).
const (
	RecurringActive = "active"
	RecurringPaused = "paused"

	CodeRecurringNotFound     = "LEDGER_RECURRING_NOT_FOUND"
	CodeRecurringPaused       = "LEDGER_RECURRING_PAUSED"
	CodeRecurringDateInvalid  = "LEDGER_RECURRING_DATE_INVALID"
	CodeRecurringBaseRequired = "LEDGER_RECURRING_BASE_REQUIRED"
	CodeRecurringLedgerFixed  = "LEDGER_RECURRING_LEDGER_FIXED"
	CodeRecurringIDConflict   = "LEDGER_RECURRING_ID_CONFLICT"
	recurringAggregate        = "recurring_journal"
)

// RecurringLine is a template line: a fixed amount or a percentage of the base amount.
type RecurringLine struct {
	LineNumber  int               `json:"lineNumber"`
	Account     string            `json:"account"`
	Dimensions  map[string]string `json:"dimensions,omitempty"`
	DebitCredit string            `json:"debitCredit"`
	AmountKind  string            `json:"amountKind"`
	Amount      string            `json:"amount"`
	Currency    string            `json:"currency"`
	Text        string            `json:"text,omitempty"`
	// Quantity and Unit are a fixed statistical quantity (BR-11.2) copied to every generated line.
	Quantity string `json:"quantity,omitempty"`
	Unit     string `json:"unit,omitempty"`
}

// RecurringJournal is a journal template with its schedule. Lines are set on
// single reads only. DayOfMonth 0 means the last day of the month.
type RecurringJournal struct {
	ID            string          `json:"id"`
	LedgerID      string          `json:"ledgerId"`
	LegalEntityID string          `json:"legalEntityId"`
	JournalName   string          `json:"journalName"`
	Name          string          `json:"name"`
	Description   string          `json:"description"`
	Frequency     string          `json:"frequency"`
	DayOfMonth    int             `json:"dayOfMonth"`
	StartDate     string          `json:"startDate"`
	EndDate       *string         `json:"endDate,omitempty"`
	BaseAmount    *string         `json:"baseAmount,omitempty"`
	Status        string          `json:"status"`
	NextRunDate   *string         `json:"nextRunDate,omitempty"`
	LastRunDate   *string         `json:"lastRunDate,omitempty"`
	Version       int             `json:"version"`
	LineCount     int             `json:"lineCount"`
	CreatedBy     string          `json:"createdBy"`
	CreatedAt     time.Time       `json:"createdAt"`
	UpdatedAt     time.Time       `json:"updatedAt"`
	Lines         []RecurringLine `json:"lines,omitempty"`
}

// RecurringInput creates or replaces a recurring journal.
type RecurringInput struct {
	LedgerID    string
	JournalName string
	Name        string
	Description string
	Frequency   string
	DayOfMonth  int
	StartDate   time.Time
	EndDate     *time.Time
	BaseAmount  *string
	Lines       []RecurringLine
}

// GenerateRecurringInput generates a draft batch for a scheduled date, which
// is required; a nil BaseAmount uses the template's own.
type GenerateRecurringInput struct {
	ID            string
	ScheduledDate *time.Time
	BaseAmount    *string
}

// RecurringRun is one generation: the draft batch made for a scheduled date.
type RecurringRun struct {
	ID            string    `json:"id"`
	RecurringID   string    `json:"recurringId"`
	ScheduledDate string    `json:"scheduledDate"`
	BatchID       string    `json:"batchId"`
	GeneratedBy   string    `json:"generatedBy"`
	GeneratedAt   time.Time `json:"generatedAt"`
}

const recurringSelect = `select r.id::text, r.ledger_id::text, r.legal_entity_id::text, r.journal_name, r.name, r.description,
		r.frequency, r.day_of_month, r.start_date::text, r.end_date::text, r.base_amount::text, r.status,
		r.next_run_date::text, r.last_run_date::text, r.version,
		(select count(*)::int from recurring_journal_line x where x.tenant_id = r.tenant_id and x.recurring_id = r.id),
		r.created_by, r.created_at, r.updated_at
	from recurring_journal r`

func scanRecurring(row pgx.Row) (RecurringJournal, error) {
	var r RecurringJournal
	err := row.Scan(&r.ID, &r.LedgerID, &r.LegalEntityID, &r.JournalName, &r.Name, &r.Description, &r.Frequency, &r.DayOfMonth,
		&r.StartDate, &r.EndDate, &r.BaseAmount, &r.Status, &r.NextRunDate, &r.LastRunDate, &r.Version, &r.LineCount,
		&r.CreatedBy, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

func recurringNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeRecurringNotFound, "recurring journal not found")
}

func getRecurringTx(ctx context.Context, tx pgx.Tx, tenantID, id string, lock bool) (RecurringJournal, error) {
	q := recurringSelect + ` where r.tenant_id = $1 and r.id = $2`
	if lock {
		q += ` for update of r`
	}
	r, err := scanRecurring(tx.QueryRow(ctx, q, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return r, recurringNotFound()
	}
	if err != nil {
		return r, err
	}
	rows, err := tx.Query(ctx, `select line_number, account, dimensions, debit_credit::text, amount_kind, amount::text,
			currency::text, coalesce(line_text, ''), coalesce(quantity::text, ''), coalesce(unit, '')
		from recurring_journal_line where tenant_id = $1 and recurring_id = $2 order by line_number`, tenantID, id)
	if err != nil {
		return r, err
	}
	defer rows.Close()
	for rows.Next() {
		var l RecurringLine
		if err := rows.Scan(&l.LineNumber, &l.Account, &l.Dimensions, &l.DebitCredit, &l.AmountKind, &l.Amount, &l.Currency, &l.Text, &l.Quantity, &l.Unit); err != nil {
			return r, err
		}
		if len(l.Dimensions) == 0 {
			l.Dimensions = nil
		}
		if l.AmountKind == domain.AmountFixed {
			l.Amount = fmtMoney(l.Amount, l.Currency)
		} else if d, err := decimal.NewFromString(l.Amount); err == nil {
			l.Amount = d.String()
		}
		r.Lines = append(r.Lines, l)
	}
	return r, rows.Err()
}

func checkRecurringVersion(r RecurringJournal, want int) error {
	if want > 0 && want != r.Version {
		return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "recurring journal changed; reload and retry")
	}
	return nil
}

// ListRecurring returns the recurring journals of the caller's scope, ordered
// by next run date then name; status filters when set.
func (s *Postgres) ListRecurring(ctx context.Context, status string) ([]RecurringJournal, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return nil, err
	}
	out := []RecurringJournal{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, recurringSelect+` where r.tenant_id = $1 and ($2 = '' or r.status = $2)
			order by r.next_run_date nulls last, r.name, r.id`, p.TenantID, status)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanRecurring(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// GetRecurring returns one recurring journal with its lines.
func (s *Postgres) GetRecurring(ctx context.Context, id string) (RecurringJournal, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return RecurringJournal{}, err
	}
	var out RecurringJournal
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		out, err = getRecurringTx(ctx, tx, p.TenantID, id, false)
		return err
	})
	return out, err
}

func dateText(t time.Time) string { return t.Format(time.DateOnly) }

// nextRunFor is the next scheduled date of a template: the first date of its
// schedule after its last run, or its first run when it never ran; nil once
// past the end date.
func nextRunFor(freq string, dom int, start time.Time, end, last *time.Time) *string {
	next := domain.FirstRun(freq, dom, start)
	if last != nil {
		next = domain.RunAfter(freq, dom, start, *last)
	}
	return withinEnd(next, end)
}

// withinEnd is d as date text, or nil when d is after end.
func withinEnd(d time.Time, end *time.Time) *string {
	if end != nil && d.After(*end) {
		return nil
	}
	t := dateText(d)
	return &t
}

func parseDatePtr(v *string) *time.Time {
	if v == nil {
		return nil
	}
	t, err := time.Parse(time.DateOnly, *v)
	if err != nil {
		return nil
	}
	return &t
}

// PutRecurring creates (created = true) or replaces a recurring journal. The
// ledger of a template cannot change. A replace checks version when > 0. When
// the schedule (frequency, day, start) is unchanged the next run date is kept,
// so runs skipped by a resume stay skipped; a changed schedule, or one that had
// finished, recomputes it from the last run. Either way the end date applies.
func (s *Postgres) PutRecurring(ctx context.Context, id string, version int, in RecurringInput) (RecurringJournal, bool, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return RecurringJournal{}, false, err
	}
	var out RecurringJournal
	var created bool
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		ledger, err := loadLedger(ctx, tx, p.TenantID, in.LedgerID, false)
		if err != nil {
			return err
		}
		name, err := usableJournalName(ctx, tx, p.TenantID, in.JournalName)
		if err != nil {
			return err
		}
		if err := checkLinesLimit(name, len(in.Lines)); err != nil {
			return err
		}
		cur, err := getRecurringTx(ctx, tx, p.TenantID, id, true)
		var pe *httpx.Problem
		switch {
		case err == nil:
			if cur.LedgerID != ledger.ID {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeRecurringLedgerFixed, "the ledger of a recurring journal cannot change")
			}
			if err := checkRecurringVersion(cur, version); err != nil {
				return err
			}
		case errors.As(err, &pe) && pe.Code == CodeRecurringNotFound:
			created = true
		default:
			return err
		}
		var end *string
		if in.EndDate != nil {
			e := dateText(*in.EndDate)
			end = &e
		}
		var next *string
		if created {
			next = nextRunFor(in.Frequency, in.DayOfMonth, in.StartDate, in.EndDate, nil)
		} else if cur.NextRunDate != nil && cur.Frequency == in.Frequency && cur.DayOfMonth == in.DayOfMonth &&
			cur.StartDate == dateText(in.StartDate) {
			next = withinEnd(*parseDatePtr(cur.NextRunDate), in.EndDate)
		} else {
			next = nextRunFor(in.Frequency, in.DayOfMonth, in.StartDate, in.EndDate, parseDatePtr(cur.LastRunDate))
		}
		if created {
			_, err = tx.Exec(ctx, `insert into recurring_journal (tenant_id, id, ledger_id, legal_entity_id, journal_name, name,
					description, frequency, day_of_month, start_date, end_date, base_amount, status, next_run_date, created_by)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::text::date, $11::text::date, $12::numeric, $13, $14::text::date, $15)`,
				p.TenantID, id, ledger.ID, ledger.LegalEntityID, name.Code, in.Name, in.Description, in.Frequency, in.DayOfMonth,
				dateText(in.StartDate), end, in.BaseAmount, RecurringActive, next, p.UserID)
		} else {
			if _, err = tx.Exec(ctx, `delete from recurring_journal_line where tenant_id = $1 and recurring_id = $2`, p.TenantID, id); err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `update recurring_journal set journal_name = $3, name = $4, description = $5, frequency = $6,
					day_of_month = $7, start_date = $8::text::date, end_date = $9::text::date, base_amount = $10::numeric,
					next_run_date = $11::text::date, version = version + 1, updated_at = now()
				where tenant_id = $1 and id = $2`,
				p.TenantID, id, name.Code, in.Name, in.Description, in.Frequency, in.DayOfMonth, dateText(in.StartDate), end,
				in.BaseAmount, next)
		}
		if err != nil {
			return err
		}
		batch := &pgx.Batch{}
		for i, l := range in.Lines {
			dims := l.Dimensions
			if dims == nil {
				dims = map[string]string{}
			}
			batch.Queue(`insert into recurring_journal_line (tenant_id, recurring_id, line_number, legal_entity_id, account, dimensions,
					debit_credit, amount_kind, amount, currency, line_text, quantity, unit)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9::numeric, $10, nullif($11, ''), nullif($12, '')::numeric, nullif($13, ''))`,
				p.TenantID, id, i+1, ledger.LegalEntityID, l.Account, dims, l.DebitCredit, l.AmountKind, l.Amount, l.Currency, l.Text,
				l.Quantity, l.Unit)
		}
		if err := execBatch(ctx, tx, batch); err != nil {
			return err
		}
		out, err = getRecurringTx(ctx, tx, p.TenantID, id, false)
		return err
	})
	if isUnique(err) {
		// A concurrent create of the same id, or an id held by a template outside the caller's legal entities.
		return RecurringJournal{}, false, httpx.NewProblem(http.StatusConflict, CodeRecurringIDConflict,
			"this id is already used by another recurring journal; reload, or create with a new id")
	}
	if err != nil {
		return RecurringJournal{}, false, err
	}
	return out, created, nil
}

// SetRecurringStatus pauses or resumes a recurring journal. Resuming skips the
// runs missed while paused: a next run date before today moves to the first
// scheduled date on or after today. A missed date can still be generated by
// naming it.
func (s *Postgres) SetRecurringStatus(ctx context.Context, id string, version int, status string, today time.Time) (RecurringJournal, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return RecurringJournal{}, err
	}
	var out RecurringJournal
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := getRecurringTx(ctx, tx, p.TenantID, id, true)
		if err != nil {
			return err
		}
		if err := checkRecurringVersion(cur, version); err != nil {
			return err
		}
		next := cur.NextRunDate
		if status == RecurringActive && cur.Status != RecurringActive {
			if next != nil && *next < dateText(today) {
				start := *parseDatePtr(&cur.StartDate)
				next = withinEnd(domain.RunOnOrAfter(cur.Frequency, cur.DayOfMonth, start, today), parseDatePtr(cur.EndDate))
			}
		}
		if _, err := tx.Exec(ctx, `update recurring_journal set status = $3, next_run_date = $4::text::date, version = version + 1,
				updated_at = now() where tenant_id = $1 and id = $2`, p.TenantID, id, status, next); err != nil {
			return err
		}
		out, err = getRecurringTx(ctx, tx, p.TenantID, id, false)
		return err
	})
	return out, err
}

func scanRun(row pgx.Row) (RecurringRun, error) {
	var r RecurringRun
	err := row.Scan(&r.ID, &r.RecurringID, &r.ScheduledDate, &r.BatchID, &r.GeneratedBy, &r.GeneratedAt)
	return r, err
}

const runSelect = `select id::text, recurring_id::text, scheduled_date::text, batch_id::text, generated_by, generated_at
	from recurring_journal_run`

// ListRecurringRuns returns the generation history of a recurring journal,
// newest scheduled date first.
func (s *Postgres) ListRecurringRuns(ctx context.Context, id string) ([]RecurringRun, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return nil, err
	}
	out := []RecurringRun{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := getRecurringTx(ctx, tx, p.TenantID, id, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, runSelect+` where tenant_id = $1 and recurring_id = $2 order by scheduled_date desc`, p.TenantID, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanRun(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

func recurringInvalidDate(msg string) error {
	return httpx.NewProblem(http.StatusUnprocessableEntity, CodeRecurringDateInvalid, msg)
}

// GenerateRecurring creates the draft batch for a scheduled date and records
// the run, in one transaction. The date must be one of the schedule's dates,
// so each period has at most one run. A date that already has a run returns
// that run (created = false), so retries and a scheduler that fires twice are
// safe. The batch posting date is the scheduled date and the lines are the
// template's, with percentages applied to the base amount. A run at or after
// the next run date advances the schedule to the date after it.
func (s *Postgres) GenerateRecurring(ctx context.Context, in GenerateRecurringInput) (RecurringRun, bool, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return RecurringRun{}, false, err
	}
	var out RecurringRun
	var created bool
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := getRecurringTx(ctx, tx, p.TenantID, in.ID, true)
		if err != nil {
			return err
		}
		if in.ScheduledDate == nil {
			return recurringInvalidDate("scheduledDate is required")
		}
		// A replay is answered before the date rules, so a run made under an
		// earlier version of the schedule still replays.
		if existing, err := scanRun(tx.QueryRow(ctx, runSelect+` where tenant_id = $1 and recurring_id = $2 and scheduled_date = $3::text::date`,
			p.TenantID, cur.ID, dateText(*in.ScheduledDate))); err == nil {
			out = existing
			return nil
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		date, err := runDate(cur, *in.ScheduledDate)
		if err != nil {
			return err
		}
		if cur.Status != RecurringActive {
			return httpx.NewProblem(http.StatusConflict, CodeRecurringPaused, "the recurring journal is paused")
		}
		batchIn, err := recurringBatch(cur, date, in.BaseAmount)
		if err != nil {
			return err
		}
		batch, err := createBatchTx(ctx, tx, p, batchIn)
		if err != nil {
			return err
		}
		out = RecurringRun{ID: ids.New(), RecurringID: cur.ID, ScheduledDate: dateText(date), BatchID: batch.ID, GeneratedBy: p.UserID}
		if err := tx.QueryRow(ctx, `insert into recurring_journal_run (tenant_id, id, recurring_id, legal_entity_id, scheduled_date, batch_id,
				generated_by) values ($1, $2, $3, $4, $5::text::date, $6, $7) returning generated_at`,
			p.TenantID, out.ID, cur.ID, cur.LegalEntityID, out.ScheduledDate, batch.ID, p.UserID).Scan(&out.GeneratedAt); err != nil {
			return err
		}
		created = true
		if err := advanceRecurring(ctx, tx, p, cur, date); err != nil {
			return err
		}
		return enqueueFor(ctx, tx, recurringAggregate, cur.ID, ledgerevents.RecurringGenerated, p.TenantID, ledgerevents.RecurringGeneratedData{
			RecurringID: cur.ID, BatchID: batch.ID, LedgerID: cur.LedgerID, JournalName: cur.JournalName,
			ScheduledDate: out.ScheduledDate, Actor: p.UserID})
	})
	return out, created, err
}

// runDate checks the scheduled date of a generation: inside the start and end
// dates and one of the schedule's dates.
func runDate(cur RecurringJournal, date time.Time) (time.Time, error) {
	if dateText(date) < cur.StartDate {
		return date, recurringInvalidDate("scheduledDate is before the start date")
	}
	if cur.EndDate != nil && dateText(date) > *cur.EndDate {
		return date, recurringInvalidDate("scheduledDate is after the end date")
	}
	start := *parseDatePtr(&cur.StartDate)
	if !domain.OnSchedule(cur.Frequency, cur.DayOfMonth, start, date) {
		return date, recurringInvalidDate("scheduledDate is not a date of the schedule; the next one on or after it is " +
			dateText(domain.RunOnOrAfter(cur.Frequency, cur.DayOfMonth, start, date)))
	}
	return date, nil
}

// recurringBatch builds the draft batch of a run.
func recurringBatch(cur RecurringJournal, date time.Time, baseOverride *string) (BatchInput, error) {
	baseText := cur.BaseAmount
	if baseOverride != nil {
		baseText = baseOverride
	}
	var base *decimal.Decimal
	if baseText != nil {
		b, err := decimal.NewFromString(*baseText)
		if err != nil || b.IsNegative() {
			return BatchInput{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeRecurringBaseRequired, "baseAmount must be a non-negative decimal string")
		}
		base = &b
	}
	tpl := make([]domain.RecurringLine, len(cur.Lines))
	for i, l := range cur.Lines {
		tpl[i] = domain.RecurringLine{Account: l.Account, Dimensions: l.Dimensions, DebitCredit: l.DebitCredit, Kind: l.AmountKind,
			Amount: l.Amount, Currency: l.Currency, Text: l.Text, Quantity: l.Quantity, Unit: l.Unit}
	}
	resolved, err := domain.ResolveAmounts(tpl, base)
	if errors.Is(err, domain.ErrNoBase) {
		return BatchInput{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeRecurringBaseRequired,
			"the template has percentage lines; set a base amount on it or pass baseAmount")
	}
	if err != nil {
		return BatchInput{}, err
	}
	lines := make([]BatchLine, len(resolved))
	for i, r := range resolved {
		lines[i] = BatchLine{Account: r.Account, Dimensions: r.Dimensions, DebitCredit: r.DebitCredit, Amount: r.Resolved,
			Currency: r.Currency, Text: r.Text, Quantity: r.Quantity, Unit: r.Unit}
	}
	return BatchInput{LedgerID: cur.LedgerID, JournalName: cur.JournalName, Description: cur.Description, PostingDate: date, Lines: lines}, nil
}

// advanceRecurring records a run on the template: the last run date moves
// forward and, when the run reached the next run date, so does the schedule.
func advanceRecurring(ctx context.Context, tx pgx.Tx, p tenant.Principal, cur RecurringJournal, date time.Time) error {
	last := date
	if cur.LastRunDate != nil && *cur.LastRunDate > dateText(date) {
		last = *parseDatePtr(cur.LastRunDate)
	}
	next := cur.NextRunDate
	if next != nil && dateText(date) >= *next {
		start := *parseDatePtr(&cur.StartDate)
		next = withinEnd(domain.RunAfter(cur.Frequency, cur.DayOfMonth, start, date), parseDatePtr(cur.EndDate))
	}
	_, err := tx.Exec(ctx, `update recurring_journal set last_run_date = $3::text::date, next_run_date = $4::text::date,
			version = version + 1, updated_at = now() where tenant_id = $1 and id = $2`,
		p.TenantID, cur.ID, dateText(last), next)
	return err
}
