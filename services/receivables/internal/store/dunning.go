package store

import (
	"context"
	"encoding/json"
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
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
	rcvevents "github.com/BenjaminSRussell/passage/services/receivables/internal/events"
)

// DunningRun is one dunning proposal and its letters (FR-30.7, §5.30.4).
type DunningRun struct {
	ID            string             `json:"id"`
	LegalEntityID string             `json:"legalEntityId"`
	RunDate       string             `json:"runDate"`
	GraceDays     int                `json:"graceDays"`
	Status        string             `json:"status"`
	Exclusions    []domain.Exclusion `json:"exclusions"`
	Letters       []DunningLetter    `json:"letters"`
	IssuedAt      *time.Time         `json:"issuedAt,omitempty"`
	Version       int                `json:"version"`
}

// DunningLetter is one customer's letter in one currency.
type DunningLetter struct {
	ID          string          `json:"id"`
	CustomerID  string          `json:"customerId"`
	Currency    string          `json:"currency"`
	Level       int             `json:"level"`
	PriorLevel  int             `json:"priorLevel"`
	Amount      decimal.Decimal `json:"amount"`
	FeeAmount   decimal.Decimal `json:"feeAmount"`
	Status      string          `json:"status"`
	DocumentRef *string         `json:"documentRef,omitempty"`
	Items       []LetterItem    `json:"items"`
}

// DunningRunSummary is one row of the run list: the run and its letter totals.
type DunningRunSummary struct {
	ID            string     `json:"id"`
	LegalEntityID string     `json:"legalEntityId"`
	RunDate       string     `json:"runDate"`
	GraceDays     int        `json:"graceDays"`
	Status        string     `json:"status"`
	LetterCount   int        `json:"letterCount"`
	IssuedAt      *time.Time `json:"issuedAt,omitempty"`
	Version       int        `json:"version"`
}

// LetterItem is one open item listed on a letter.
type LetterItem struct {
	OpenItemID     string          `json:"openItemId"`
	DocumentNumber string          `json:"documentNumber"`
	DueDate        string          `json:"dueDate"`
	OpenAmount     decimal.Decimal `json:"openAmount"`
	DaysOverdue    int             `json:"daysOverdue"`
	FromLevel      int             `json:"fromLevel"`
	ToLevel        int             `json:"toLevel"`
}

const dunnableColumns = `oi.id, oi.customer_id, oi.source_type, oi.document_number, oi.currency, oi.due_date::text,
	oi.open_amount::text, oi.dunning_level, oi.dunning_block, oi.dispute_id is not null, p.promise_date::text, oi.last_dunned_at::text,
	(select max(n.calculation_date) from interest_note n where n.tenant_id = oi.tenant_id and n.open_item_id = oi.id)::text`

const dunnableFrom = ` from open_item oi left join promise_to_pay p on p.tenant_id = oi.tenant_id and p.id = oi.promise_id`

const runColumns = `id, legal_entity_id, run_date::text, grace_days, status, exclusions, issued_at, version`

func scanDunnable(row rowScanner) (domain.DunnableItem, error) {
	var it domain.DunnableItem
	var due, open string
	var promise, last, interested *string
	if err := row.Scan(&it.ID, &it.CustomerID, &it.SourceType, &it.DocumentNumber, &it.Currency, &due, &open,
		&it.DunningLevel, &it.DunningBlock, &it.HasDispute, &promise, &last, &interested); err != nil {
		return domain.DunnableItem{}, err
	}
	var err error
	if it.OpenAmount, err = decimal.NewFromString(open); err != nil {
		return domain.DunnableItem{}, err
	}
	it.DueDate = parseDay(due)
	if promise != nil {
		d := parseDay(*promise)
		it.PromiseDate = &d
	}
	if last != nil {
		d := parseDay(*last)
		it.LastDunnedAt = &d
	}
	if interested != nil {
		d := parseDay(*interested)
		it.InterestedTo = &d
	}
	return it, nil
}

// loadDunnable reads every open item of a legal entity for dunning and interest.
func loadDunnable(ctx context.Context, tx pgx.Tx, legalEntityID string) ([]domain.DunnableItem, error) {
	rows, err := tx.Query(ctx, `select `+dunnableColumns+dunnableFrom+`
		where oi.legal_entity_id = $1 and oi.status in `+openStatuses+` and oi.amount > 0 order by oi.due_date, oi.id`, legalEntityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.DunnableItem
	for rows.Next() {
		it, err := scanDunnable(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// loadDunnableLocked reads named items under NOWAIT row locks (BR-30.7) for issue.
func loadDunnableLocked(ctx context.Context, tx pgx.Tx, itemIDs []string) (map[string]domain.DunnableItem, error) {
	rows, err := tx.Query(ctx, `select `+dunnableColumns+dunnableFrom+`
		where oi.id = any($1::uuid[]) order by oi.id for update of oi nowait`, itemIDs)
	if err != nil {
		return nil, lockProblem(err)
	}
	defer rows.Close()
	out := map[string]domain.DunnableItem{}
	for rows.Next() {
		it, err := scanDunnable(rows)
		if err != nil {
			return nil, lockProblem(err)
		}
		out[it.ID] = it
	}
	if err := rows.Err(); err != nil {
		return nil, lockProblem(err)
	}
	return out, nil
}

func lockProblem(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == lockNotAvailable {
		return domainProblem(domain.CodeItemLocked, "an open item on a letter is locked by another user")
	}
	return err
}

func loadRun(ctx context.Context, tx pgx.Tx, where string, args ...any) (DunningRun, error) {
	var r DunningRun
	var exclusions []byte
	err := tx.QueryRow(ctx, `select `+runColumns+` from dunning_run where `+where, args...).Scan(&r.ID, &r.LegalEntityID,
		&r.RunDate, &r.GraceDays, &r.Status, &exclusions, &r.IssuedAt, &r.Version)
	if err != nil {
		return DunningRun{}, err
	}
	if err := json.Unmarshal(exclusions, &r.Exclusions); err != nil {
		return DunningRun{}, err
	}
	if r.Exclusions == nil {
		r.Exclusions = []domain.Exclusion{}
	}
	r.Letters, err = loadLetters(ctx, tx, r.ID)
	return r, err
}

func loadLetters(ctx context.Context, tx pgx.Tx, runID string) ([]DunningLetter, error) {
	rows, err := tx.Query(ctx, `select id, customer_id, currency, level, prior_level, amount::text, fee_amount::text, status,
		document_ref, items from dunning_letter where run_id = $1 order by currency, customer_id, id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DunningLetter{}
	for rows.Next() {
		var l DunningLetter
		var amount, fee string
		var items []byte
		if err := rows.Scan(&l.ID, &l.CustomerID, &l.Currency, &l.Level, &l.PriorLevel, &amount, &fee, &l.Status,
			&l.DocumentRef, &items); err != nil {
			return nil, err
		}
		if l.Amount, err = decimal.NewFromString(amount); err != nil {
			return nil, err
		}
		if l.FeeAmount, err = decimal.NewFromString(fee); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(items, &l.Items); err != nil {
			return nil, err
		}
		if l.Items == nil {
			l.Items = []LetterItem{}
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func letterItems(lines []domain.LetterLine) []LetterItem {
	out := make([]LetterItem, 0, len(lines))
	for _, ln := range lines {
		out = append(out, LetterItem{OpenItemID: ln.OpenItemID, DocumentNumber: ln.DocumentNumber,
			DueDate: ln.DueDate.Format(dateLayout), OpenAmount: ln.OpenAmount, DaysOverdue: ln.DaysOverdue,
			FromLevel: ln.FromLevel, ToLevel: ln.ToLevel})
	}
	return out
}

// CreateDunningRun proposes letters from the legal entity's open items as of the
// run date and stores the run as DRAFT. Nothing is raised or posted until issue.
// A repeated key returns the stored run when the request matches.
func (s *Postgres) CreateDunningRun(ctx context.Context, in DunningRun, key string) (DunningRun, error) {
	p, _ := tenant.FromContext(ctx)
	var out DunningRun
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		replay, err := loadRun(ctx, tx, `idempotency_key = $1`, key)
		if err == nil {
			if replay.LegalEntityID != in.LegalEntityID || replay.RunDate != in.RunDate || replay.GraceDays != in.GraceDays {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
			}
			out = replay
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		items, err := loadDunnable(ctx, tx, in.LegalEntityID)
		if err != nil {
			return err
		}
		plan := domain.PlanDunning(parseDay(in.RunDate), in.GraceDays, domain.DefaultIntervalDays, items)
		if plan.Exclusions == nil {
			plan.Exclusions = []domain.Exclusion{}
		}
		exclusions, err := json.Marshal(plan.Exclusions)
		if err != nil {
			return err
		}
		runID := ids.New()
		if _, err := tx.Exec(ctx, `insert into dunning_run (tenant_id, id, legal_entity_id, run_date, grace_days, status, exclusions, idempotency_key)
			values ($1, $2, $3, $4, $5, 'DRAFT', $6, $7)`, p.TenantID, runID, in.LegalEntityID, in.RunDate, in.GraceDays, exclusions, key); err != nil {
			return err
		}
		for _, l := range plan.Letters {
			raw, err := json.Marshal(letterItems(l.Lines))
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `insert into dunning_letter (tenant_id, id, run_id, legal_entity_id, customer_id, currency, level,
				prior_level, amount, fee_amount, items, status) values ($1, $2, $3, $4, $5, $6, $7, $8, $9::numeric, $10::numeric, $11, 'DRAFT')`,
				p.TenantID, ids.New(), runID, in.LegalEntityID, l.CustomerID, l.Currency, l.Level, l.PriorLevel,
				l.Amount.String(), l.Fee.String(), raw); err != nil {
				return err
			}
		}
		out, err = loadRun(ctx, tx, `id = $1`, runID)
		return err
	})
	return out, mapWriteErr(err)
}

// GetDunningRun reads one run with its letters.
func (s *Postgres) GetDunningRun(ctx context.Context, id string) (DunningRun, error) {
	var out DunningRun
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = loadRun(ctx, tx, `id = $1`, id)
		return notFound(err, CodeRunNotFound, "dunning run not found")
	})
	return out, err
}

// TransitionDunningRun moves a run DRAFT → REVIEWED, DRAFT|REVIEWED → CANCELLED, or
// REVIEWED → ISSUED. Issue re-checks every letter under lock, raises item levels,
// posts level 3 and 4 fees as FEE items and enqueues dunning.issued.v1 (FR-30.7).
func (s *Postgres) TransitionDunningRun(ctx context.Context, id string, version int, to string) (DunningRun, error) {
	p, _ := tenant.FromContext(ctx)
	var out DunningRun
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		run, err := loadRun(ctx, tx, `id = $1 for update`, id)
		if err != nil {
			return notFound(err, CodeRunNotFound, "dunning run not found")
		}
		if run.Version != version {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "dunning run changed since it was read")
		}
		if !domain.CanMoveRun(run.Status, to) {
			if to == "ISSUED" {
				return httpx.NewProblem(http.StatusConflict, domain.CodeRunNotReviewed, "dunning run must be REVIEWED before issue")
			}
			return domainProblem(domain.CodeRunState, "dunning run in status "+run.Status+" cannot move to "+to)
		}
		if to == "ISSUED" {
			if err := issueRun(ctx, tx, p.TenantID, run); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `update dunning_run set status = $3, version = version + 1,
			issued_at = case when $3 = 'ISSUED' then now() else issued_at end where tenant_id = $1 and id = $2`,
			p.TenantID, id, to); err != nil {
			return err
		}
		out, err = loadRun(ctx, tx, `id = $1`, id)
		return err
	})
	return out, mapWriteErr(err)
}

// issueRun re-evaluates each DRAFT letter at the run date and raises the levels of
// the items that remain issuable. Letters left below the minimum are SKIPPED.
func issueRun(ctx context.Context, tx pgx.Tx, tenantID string, run DunningRun) error {
	asOf := parseDay(run.RunDate)
	for _, l := range run.Letters {
		if l.Status != "DRAFT" {
			continue
		}
		planned := domain.PlannedLetter{CustomerID: l.CustomerID, Currency: l.Currency, PriorLevel: l.PriorLevel}
		itemIDs := make([]string, 0, len(l.Items))
		for _, it := range l.Items {
			itemIDs = append(itemIDs, it.OpenItemID)
			planned.Lines = append(planned.Lines, domain.LetterLine{OpenItemID: it.OpenItemID, DocumentNumber: it.DocumentNumber,
				DueDate: parseDay(it.DueDate), OpenAmount: it.OpenAmount, DaysOverdue: it.DaysOverdue, FromLevel: it.FromLevel, ToLevel: it.ToLevel})
		}
		current, err := loadDunnableLocked(ctx, tx, itemIDs)
		if err != nil {
			return err
		}
		revised, ok := domain.ReviseLetter(asOf, planned, current)
		if !ok {
			if _, err := tx.Exec(ctx, `update dunning_letter set status = 'SKIPPED' where tenant_id = $1 and id = $2`, tenantID, l.ID); err != nil {
				return err
			}
			continue
		}
		eventItems := make([]rcvevents.LetterItemData, 0, len(revised.Lines))
		for _, ln := range revised.Lines {
			eventItems = append(eventItems, rcvevents.LetterItemData{OpenItemID: ln.OpenItemID, OpenAmount: ln.OpenAmount, DaysOverdue: ln.DaysOverdue})
			if _, err := tx.Exec(ctx, `update open_item set dunning_level = $3, last_dunned_at = $4::date, version = version + 1,
				updated_at = now() where tenant_id = $1 and id = $2`, tenantID, ln.OpenItemID, ln.ToLevel, asOf.Format(dateLayout)); err != nil {
				return err
			}
		}
		docRef := "DL-" + l.ID
		if revised.Fee.IsPositive() {
			if _, err := tx.Exec(ctx, `insert into open_item (tenant_id, id, legal_entity_id, customer_id, source_type, document_number,
				document_date, due_date, currency, amount, open_amount, status)
				values ($1, $2, $3, $4, 'FEE', $5, $6::date, $6::date, $7, $8::numeric, $8::numeric, 'OPEN')`,
				tenantID, ids.New(), run.LegalEntityID, l.CustomerID, "FEE-"+l.ID, asOf.Format(dateLayout), l.Currency,
				revised.Fee.String()); err != nil {
				return err
			}
		}
		raw, err := json.Marshal(letterItems(revised.Lines))
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update dunning_letter set status = 'ISSUED', level = $3, amount = $4::numeric,
			fee_amount = $5::numeric, items = $6, document_ref = $7, issued_at = now() where tenant_id = $1 and id = $2`,
			tenantID, l.ID, revised.Level, revised.Amount.String(), revised.Fee.String(), raw, docRef); err != nil {
			return err
		}
		if err := enqueue(ctx, tx, "dunning_letter", l.ID, rcvevents.DunningLetterIssued, tenantID, rcvevents.DunningLetterIssuedData{
			RunID: run.ID, LetterID: l.ID, LegalEntityID: run.LegalEntityID, CustomerID: l.CustomerID, Currency: l.Currency,
			Level: revised.Level, Items: eventItems, Fee: revised.Fee, DocumentRef: docRef,
		}); err != nil {
			return err
		}
	}
	return nil
}

// ListDunningRuns reads runs, latest run date first, with the count of letters
// that are (or will be) sent: DRAFT and ISSUED, not SKIPPED.
func (s *Postgres) ListDunningRuns(ctx context.Context, status string, limit int) ([]DunningRunSummary, error) {
	out := []DunningRunSummary{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select r.id, r.legal_entity_id, r.run_date::text, r.grace_days, r.status,
			(select count(*) from dunning_letter l where l.tenant_id = r.tenant_id and l.run_id = r.id and l.status <> 'SKIPPED'),
			r.issued_at, r.version
			from dunning_run r where ($1::text = '' or r.status = $1::text)
			order by r.run_date desc, r.created_at desc, r.id limit $2`, status, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r DunningRunSummary
			if err := rows.Scan(&r.ID, &r.LegalEntityID, &r.RunDate, &r.GraceDays, &r.Status, &r.LetterCount, &r.IssuedAt, &r.Version); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}
