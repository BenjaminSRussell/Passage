package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ledgerclient"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
)

// MaxCompensationsPerRun bounds the journals one compensation run posts; More says whether due
// compensations are left.
const MaxCompensationsPerRun = 200

var (
	// ErrLedgerPinned is returned when a run names another ledger or legal entity for an entity than
	// the one its compensations are pinned to.
	ErrLedgerPinned = errors.New("ledger pinned")
	// ErrLedgerForeign is returned when the ledger service says the legal entity does not own the ledger.
	ErrLedgerForeign = errors.New("ledger not owned by the legal entity")
)

// EntityLedger is the ledger and legal entity a plan entity's compensations are posted in.
type EntityLedger struct {
	Entity        string `json:"entity"`
	LegalEntityID string `json:"legalEntityId"`
	LedgerID      string `json:"ledgerId"`
}

// CompensationRun asks for the compensating journals of a rolled-back plan to be posted. Ledgers
// pins each entity's ledger on first use; RetryParked puts parked compensations back in the queue.
type CompensationRun struct {
	PlanID      string
	Ledgers     []EntityLedger
	RetryParked bool
	By          string
	Now         time.Time
}

// CompensationReport is a plan's compensating journals with their reconciliation against the
// re-keying journals, the pinned ledgers, and whether due compensations are left for another run.
type CompensationReport struct {
	PlanID        string                `json:"planId"`
	Ledgers       []EntityLedger        `json:"ledgers"`
	Compensations []domain.Compensation `json:"-"`
	Totals        []domain.ControlTotal `json:"-"`
	Posted        int                   `json:"posted"`
	Pending       int                   `json:"pending"`
	Parked        int                   `json:"parked"`
	Reconciled    bool                  `json:"reconciled"`
	More          bool                  `json:"more"`
}

// rolledBackPlan returns the plan and its rollback date, or ErrTransition when it is not rolled back.
func rolledBackPlan(ctx context.Context, tx pgx.Tx, planID string) (domain.Plan, time.Time, error) {
	plan, err := getPlan(ctx, tx, planID, false)
	if err != nil {
		return domain.Plan{}, time.Time{}, err
	}
	var at *time.Time
	if err := tx.QueryRow(ctx, `select rolled_back_at from cutover_plan where tenant_id = current_setting('passage.tenant_id', true)::uuid and plan_id = $1`,
		planID).Scan(&at); err != nil {
		return domain.Plan{}, time.Time{}, err
	}
	if !plan.RolledBack || at == nil {
		return domain.Plan{}, time.Time{}, fmt.Errorf("%w: plan is not rolled back, so it has no compensating journals", domain.ErrTransition)
	}
	return plan, at.UTC(), nil
}

func readPinned(ctx context.Context, tx pgx.Tx, planID string) ([]EntityLedger, error) {
	rows, err := tx.Query(ctx, `select entity, legal_entity_id::text, ledger_id from rollback_ledger
		where tenant_id = current_setting('passage.tenant_id', true)::uuid and plan_id = $1 order by entity`, planID)
	if err != nil {
		return nil, fmt.Errorf("read rollback_ledger: %w", err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (EntityLedger, error) {
		var l EntityLedger
		err := r.Scan(&l.Entity, &l.LegalEntityID, &l.LedgerID)
		return l, err
	})
}

func readPinnedMap(ctx context.Context, tx pgx.Tx, planID string) (map[string]EntityLedger, error) {
	list, err := readPinned(ctx, tx, planID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]EntityLedger, len(list))
	for _, l := range list {
		out[l.Entity] = l
	}
	return out, nil
}

func readJournals(ctx context.Context, tx pgx.Tx, planID string) ([]domain.RekeyPosting, error) {
	rows, err := tx.Query(ctx, `select entity, document_ref, line, posted_at, account, dimensions, currency, amount::text, text
		from rekey_line where tenant_id = current_setting('passage.tenant_id', true)::uuid and plan_id = $1 and kind = 'JOURNAL' order by seq`, planID)
	if err != nil {
		return nil, fmt.Errorf("read rekey_line: %w", err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.RekeyPosting, error) {
		l := domain.RekeyPosting{Kind: domain.KindJournal}
		var amount string
		if err := r.Scan(&l.Entity, &l.DocumentRef, &l.Line, &l.PostedAt, &l.Account, &l.Dimensions, &l.Currency, &amount, &l.Text); err != nil {
			return l, err
		}
		var err error
		l.Amount, err = decimal.NewFromString(amount)
		return l, err
	})
}

func readCompensations(ctx context.Context, tx pgx.Tx, planID string) ([]domain.Compensation, error) {
	rows, err := tx.Query(ctx, `select entity, document_ref, currency, idempotency_key, status, attempts, last_code, next_attempt_at, entry_id, posted_at
		from rollback_compensation where tenant_id = current_setting('passage.tenant_id', true)::uuid and plan_id = $1
		order by entity, created_at, document_ref, currency`, planID)
	if err != nil {
		return nil, fmt.Errorf("read rollback_compensation: %w", err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.Compensation, error) {
		var c domain.Compensation
		var status string
		err := r.Scan(&c.Entity, &c.DocumentRef, &c.Currency, &c.Key, &status, &c.Attempts, &c.LastCode, &c.NextAttemptAt, &c.EntryID, &c.PostedAt)
		c.Status = domain.CompensationStatus(status)
		return c, err
	})
}

// pinRollbackLedgers checks each requested ledger with the ledger service (the legal entity must own
// it) and pins it for its entity; an entity already pinned to another ledger or legal entity is
// ErrLedgerPinned. The lookup happens before the transaction that pins, so no row lock waits on it.
func (s *Postgres) pinRollbackLedgers(ctx context.Context, ledger ledgerclient.Ledger, plan domain.Plan, in CompensationRun) error {
	entities := map[string]bool{}
	for _, e := range plan.Entities {
		entities[e] = true
	}
	var pinned map[string]EntityLedger
	if err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		pinned, err = readPinnedMap(ctx, tx, plan.PlanID)
		return err
	}); err != nil {
		return err
	}
	var fresh []EntityLedger
	for _, l := range in.Ledgers {
		if !entities[l.Entity] {
			return fmt.Errorf("%w: entity %q is not an entity of the plan", domain.ErrInvalid, l.Entity)
		}
		if have, ok := pinned[l.Entity]; ok {
			if have != l {
				return fmt.Errorf("%w: entity %s is compensated in ledger %s of legal entity %s", ErrLedgerPinned, l.Entity, have.LedgerID, have.LegalEntityID)
			}
			continue
		}
		owner, err := ledger.LegalEntity(ctx, l.LegalEntityID, l.LedgerID)
		if err != nil {
			if ledgerclient.Rejected(err) {
				return fmt.Errorf("%w: ledger %s is unknown or not readable for legal entity %s (%s)", ErrLedgerForeign, l.LedgerID, l.LegalEntityID, ledgerclient.CodeOf(err))
			}
			return fmt.Errorf("ledger lookup: %w", err)
		}
		if owner != l.LegalEntityID {
			return fmt.Errorf("%w: ledger %s belongs to another legal entity", ErrLedgerForeign, l.LedgerID)
		}
		fresh = append(fresh, l)
	}
	if len(fresh) == 0 {
		return nil
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		for _, l := range fresh {
			if _, err := tx.Exec(ctx, `insert into rollback_ledger (tenant_id, plan_id, entity, legal_entity_id, ledger_id, pinned_by)
				values (current_setting('passage.tenant_id', true)::uuid, $1, $2, $3, $4, $5) on conflict do nothing`,
				plan.PlanID, l.Entity, l.LegalEntityID, l.LedgerID, in.By); err != nil {
				return fmt.Errorf("insert rollback_ledger: %w", err)
			}
		}
		now, err := readPinnedMap(ctx, tx, plan.PlanID)
		if err != nil {
			return err
		}
		for _, l := range fresh {
			if have := now[l.Entity]; have != l {
				return fmt.Errorf("%w: entity %s is compensated in ledger %s of legal entity %s", ErrLedgerPinned, l.Entity, have.LedgerID, have.LegalEntityID)
			}
		}
		return nil
	})
}

// PostCompensations posts the due compensating journals of a rolled-back plan (FR-49.9, D9). It pins
// the requested ledgers, creates one compensation per re-keying journal (once; the key never
// changes), and posts each due compensation of a pinned entity through the ledger with the lines
// derived from the stored rekey_line rows and the rollback date as posting date. A failure backs the
// row off and parks it after a rejection or MaxCompensationAttempts; other rows go on. Posting is
// idempotent: the ledger books a key once, and a POSTED row is never sent again.
func (s *Postgres) PostCompensations(ctx context.Context, ledger ledgerclient.Ledger, in CompensationRun) (CompensationReport, error) {
	var plan domain.Plan
	var rolledBackAt time.Time
	if err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		plan, rolledBackAt, err = rolledBackPlan(ctx, tx, in.PlanID)
		return err
	}); err != nil {
		return CompensationReport{}, err
	}
	if err := s.pinRollbackLedgers(ctx, ledger, plan, in); err != nil {
		return CompensationReport{}, err
	}
	var journals []domain.RekeyPosting
	var pinned map[string]EntityLedger
	var due []domain.Compensation
	more := false
	if err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		if journals, err = readJournals(ctx, tx, plan.PlanID); err != nil {
			return err
		}
		for _, c := range domain.PlanCompensations(plan.PlanID, journals) {
			if _, err := tx.Exec(ctx, `insert into rollback_compensation (tenant_id, plan_id, entity, document_ref, currency, idempotency_key, next_attempt_at)
				values (current_setting('passage.tenant_id', true)::uuid, $1, $2, $3, $4, $5, $6) on conflict do nothing`,
				plan.PlanID, c.Entity, c.DocumentRef, c.Currency, c.Key, in.Now); err != nil {
				return fmt.Errorf("insert rollback_compensation: %w", err)
			}
		}
		if in.RetryParked {
			if _, err := tx.Exec(ctx, `update rollback_compensation set status = 'PENDING', attempts = 0, next_attempt_at = $2
				where tenant_id = current_setting('passage.tenant_id', true)::uuid and plan_id = $1 and status = 'PARKED'`, plan.PlanID, in.Now); err != nil {
				return fmt.Errorf("retry parked compensations: %w", err)
			}
		}
		if pinned, err = readPinnedMap(ctx, tx, plan.PlanID); err != nil {
			return err
		}
		all, err := readCompensations(ctx, tx, plan.PlanID)
		if err != nil {
			return err
		}
		for _, c := range all {
			if c.Status != domain.CompensationPending || c.NextAttemptAt.After(in.Now) {
				continue
			}
			if _, ok := pinned[c.Entity]; !ok {
				continue
			}
			if len(due) == MaxCompensationsPerRun {
				more = true
				break
			}
			due = append(due, c)
		}
		return nil
	}); err != nil {
		return CompensationReport{}, err
	}
	postingDate := rolledBackAt.Format(time.DateOnly)
	for _, c := range due {
		l := pinned[c.Entity]
		entry, err := domain.CompensationEntry(c, journals, l.LegalEntityID, l.LedgerID, postingDate)
		var entryID string
		if err == nil {
			entryID, err = ledger.Post(ctx, entry)
		}
		if err := s.recordCompensation(ctx, plan.PlanID, c, entryID, err, in.Now); err != nil {
			return CompensationReport{}, err
		}
	}
	rep, err := s.CompensationReport(ctx, plan.PlanID)
	rep.More = more
	return rep, err
}

// recordCompensation stores the outcome of one post: POSTED with the ledger's entry id, or the
// failure with its backoff (parked on a rejection or at the attempt limit).
func (s *Postgres) recordCompensation(ctx context.Context, planID string, c domain.Compensation, entryID string, postErr error, now time.Time) error {
	return s.tx(ctx, func(tx pgx.Tx) error {
		if postErr == nil {
			_, err := tx.Exec(ctx, `update rollback_compensation set status = 'POSTED', entry_id = $5, posted_at = $6, last_code = ''
				where tenant_id = current_setting('passage.tenant_id', true)::uuid and plan_id = $1 and entity = $2 and document_ref = $3 and currency = $4
				and status <> 'POSTED'`, planID, c.Entity, c.DocumentRef, c.Currency, entryID, now)
			return err
		}
		code := ledgerclient.CodeOf(postErr)
		rejected := ledgerclient.Rejected(postErr) || errors.Is(postErr, domain.ErrInvalid) || errors.Is(postErr, ledgerclient.ErrEmptyEntry) || errors.Is(postErr, ledgerclient.ErrUnbalanced)
		if code == "" {
			code = "MIG-COMPENSATION-FAILED"
			if rejected {
				code = "MIG-COMPENSATION-INVALID"
			}
		}
		slog.ErrorContext(ctx, "rollback compensation not posted", "plan", planID, "entity", c.Entity, "document", c.DocumentRef, "key", c.Key, "code", code, "err", postErr)
		domain.FailCompensation(&c, code, rejected, now)
		_, err := tx.Exec(ctx, `update rollback_compensation set status = $5, attempts = $6, last_code = $7, next_attempt_at = $8
			where tenant_id = current_setting('passage.tenant_id', true)::uuid and plan_id = $1 and entity = $2 and document_ref = $3 and currency = $4
			and status = 'PENDING'`, planID, c.Entity, c.DocumentRef, c.Currency, string(c.Status), c.Attempts, c.LastCode, c.NextAttemptAt)
		return err
	})
}

// CompensationReport reads a rolled-back plan's compensations, pinned ledgers and reconciliation.
func (s *Postgres) CompensationReport(ctx context.Context, planID string) (CompensationReport, error) {
	rep := CompensationReport{PlanID: planID, Ledgers: []EntityLedger{}, Compensations: []domain.Compensation{}}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if _, _, err := rolledBackPlan(ctx, tx, planID); err != nil {
			return err
		}
		ledgers, err := readPinned(ctx, tx, planID)
		if err != nil {
			return err
		}
		rep.Ledgers = append(rep.Ledgers, ledgers...)
		if rep.Compensations, err = readCompensations(ctx, tx, planID); err != nil {
			return err
		}
		journals, err := readJournals(ctx, tx, planID)
		if err != nil {
			return err
		}
		rep.Totals, err = domain.ReconcileCompensations(journals, rep.Compensations)
		return err
	})
	if err != nil {
		return CompensationReport{}, err
	}
	rep.Reconciled = true
	for _, c := range rep.Compensations {
		switch c.Status {
		case domain.CompensationPosted:
			rep.Posted++
		case domain.CompensationParked:
			rep.Parked++
		default:
			rep.Pending++
		}
	}
	for _, t := range rep.Totals {
		if t.Status == domain.StatusMismatch {
			rep.Reconciled = false
		}
	}
	return rep, nil
}
