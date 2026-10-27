// Package store persists cost objects, cost elements, cost entries and
// allocation runs. Every command runs in one tenant transaction with its
// outbox rows and its Idempotency-Key record.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/costacct/internal/domain"
	ccevents "github.com/BenjaminSRussell/passage/services/costacct/internal/events"
)

// Problem codes (listed in the service README).
const (
	CodeObjectNotFound        = "CST-0001"
	CodePeriodAllocated       = "CST-0002"
	CodeDuplicateCode         = "CST-0003"
	CodeNoPrimaryElement      = "CST-0004"
	CodeNotConverged          = "CST-0005"
	CodeConservation          = "CST-0006"
	CodeSecondaryNotFound     = "CST-0007"
	CodeInvalidAllocation     = "CST-0008"
	CodeAccountRangeOverlap   = "CST-0009"
	CodeIdempotencyReuse      = "idempotency_key_reuse"
	CodeIdempotencyInProgress = "idempotency_in_progress"
	uniqueViolation           = "23505"
	idempotencyPrimaryKey     = "idempotency_keys_pkey"
	oneCompletedRun           = "allocation_run_one_completed"
	eventSource               = "costacct"
	listLimit                 = 500
)

// CostObject is a cost center, internal order, product or project reference.
type CostObject struct {
	ID            string `json:"id"`
	LegalEntityID string `json:"legalEntityId"`
	Kind          string `json:"kind"`
	Code          string `json:"code"`
	Name          string `json:"name"`
}

// CostElement is a primary (main account range) or secondary (allocation) element.
type CostElement struct {
	ID              string `json:"id"`
	LegalEntityID   string `json:"legalEntityId"`
	Kind            string `json:"kind"`
	Code            string `json:"code"`
	Name            string `json:"name"`
	MainAccountFrom string `json:"mainAccountFrom,omitempty"`
	MainAccountTo   string `json:"mainAccountTo,omitempty"`
	// DefaultBehavior is the class used when no behavior rule applies (FR-38.16).
	DefaultBehavior string `json:"defaultBehavior,omitempty"`
}

// Warning is a non-fatal finding reported with a run (CO-0613, CO-0615).
type Warning struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// MirrorLine is one journal line mirrored into the cost ledger (FR-38.6).
type MirrorLine struct {
	JournalLineID string          `json:"journalLineId"`
	MainAccount   string          `json:"mainAccount"`
	CostObjectID  string          `json:"costObjectId"`
	Period        string          `json:"period"`
	Currency      string          `json:"currency"`
	Amount        decimal.Decimal `json:"amount"`
}

// MirrorRequest carries journal lines for one legal entity.
type MirrorRequest struct {
	LegalEntityID string
	Lines         []MirrorLine
}

// RunRequest is one reciprocal allocation step for a period (FR-38.7).
// The cost base of each center is the sum of its journal-origin entries.
type RunRequest struct {
	LegalEntityID      string
	Period             string
	Currency           string
	SecondaryElementID string
	CostObjectIDs      []string
	Basis              [][]decimal.Decimal
}

// RunResponse reports the completed run and its cents-exact result. A rerun
// names the run it reversed (BR-38.7).
type RunResponse struct {
	RunID         string        `json:"runId"`
	Period        string        `json:"period"`
	ReversedRunID string        `json:"reversedRunId,omitempty"`
	Result        domain.Result `json:"result"`
	Warnings      []Warning     `json:"warnings"`
}

// Entry is one cost entry as listed for a cost object.
type Entry struct {
	ID        string          `json:"id"`
	ElementID string          `json:"costElementId"`
	SourceID  string          `json:"sourceId"`
	RunID     string          `json:"allocationRunId,omitempty"`
	TopDownID string          `json:"topDownRunId,omitempty"`
	SettleID  string          `json:"settlementId,omitempty"`
	TplRunID  string          `json:"templateRunId,omitempty"`
	Period    string          `json:"period"`
	Currency  string          `json:"currency"`
	Amount    decimal.Decimal `json:"amount"`
	Behavior  string          `json:"behavior"`
	Origin    string          `json:"origin"`
}

// Postgres is the cost accounting store.
type Postgres struct {
	pool *pgxpool.Pool
}

// New returns a store over pool.
func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

// Ping checks the database for readiness.
func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

type command func(ctx context.Context, tx pgx.Tx) (int, any, error)

// run executes fn once per Idempotency-Key; a replay returns the stored body.
func (s *Postgres) run(ctx context.Context, key, hash string, fn command) (int, []byte, error) {
	p, _ := tenant.FromContext(ctx)
	var status int
	var raw []byte
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var storedHash string
		var storedBody []byte
		err := tx.QueryRow(ctx, `select request_hash, response_status, response_body from idempotency_keys where tenant_id = $1 and key = $2`,
			p.TenantID, key).Scan(&storedHash, &status, &storedBody)
		if err == nil {
			if storedHash != hash {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
			}
			raw = storedBody
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		st, body, err := fn(ctx, tx)
		if err != nil {
			return err
		}
		if raw, err = json.Marshal(body); err != nil {
			return err
		}
		status = st
		_, err = tx.Exec(ctx, `insert into idempotency_keys (tenant_id, key, request_hash, response_status, response_body) values ($1, $2, $3, $4, $5)`,
			p.TenantID, key, hash, st, raw)
		return err
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		switch pgErr.ConstraintName {
		case idempotencyPrimaryKey:
			return 0, nil, httpx.NewProblem(http.StatusConflict, CodeIdempotencyInProgress, "a request with this Idempotency-Key is still in progress")
		case oneCompletedRun:
			return 0, nil, problem(http.StatusConflict, CodePeriodAllocated, "this period already has a completed allocation run")
		case oneTopDownRun:
			return 0, nil, problem(http.StatusConflict, CodePeriodAllocated, "this source and element already have a completed top-down run for the period")
		case oneTemplateRun:
			return 0, nil, problem(http.StatusConflict, CodePeriodAllocated, "this template already has a completed run for the period")
		case oneBehaviorRule:
			return 0, nil, problem(http.StatusConflict, CodeBehaviorRuleExists, "a behavior rule already exists for this element, object kind and date")
		default:
			// Two concurrent creates of the same code: the loser sees the same answer as a sequential duplicate.
			return 0, nil, problem(http.StatusConflict, CodeDuplicateCode, "a cost object or element with this code already exists")
		}
	}
	if err != nil {
		return 0, nil, err
	}
	return status, raw, nil
}

func enqueue(ctx context.Context, tx pgx.Tx, aggregateType, aggregateID, eventType, tenantID string, data any) error {
	evt, err := events.New(eventType, eventSource, aggregateID, tenantID, data)
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, aggregateType, aggregateID, evt)
}

func problem(status int, code, detail string) error {
	return httpx.NewProblem(status, code, detail)
}

// CreateCostObject stores a cost object for a legal entity.
func (s *Postgres) CreateCostObject(ctx context.Context, key, hash string, in CostObject) (int, []byte, error) {
	p, _ := tenant.FromContext(ctx)
	return s.run(ctx, key, hash, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		var exists bool
		if err := tx.QueryRow(ctx, `select exists(select 1 from cost_object where tenant_id = $1 and legal_entity_id = $2 and code = $3)`,
			p.TenantID, in.LegalEntityID, in.Code).Scan(&exists); err != nil {
			return 0, nil, err
		}
		if exists {
			return 0, nil, problem(http.StatusConflict, CodeDuplicateCode, "cost object code already exists")
		}
		in.ID = ids.New()
		if _, err := tx.Exec(ctx, `insert into cost_object (tenant_id, id, legal_entity_id, kind, code, name) values ($1, $2, $3, $4, $5, $6)`,
			p.TenantID, in.ID, in.LegalEntityID, in.Kind, in.Code, in.Name); err != nil {
			return 0, nil, err
		}
		return http.StatusCreated, in, nil
	})
}

// CreateCostElement stores a primary or secondary cost element. Primary
// account ranges may not overlap an existing primary element.
func (s *Postgres) CreateCostElement(ctx context.Context, key, hash string, in CostElement) (int, []byte, error) {
	p, _ := tenant.FromContext(ctx)
	return s.run(ctx, key, hash, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		var exists bool
		if err := tx.QueryRow(ctx, `select exists(select 1 from cost_element where tenant_id = $1 and legal_entity_id = $2 and code = $3)`,
			p.TenantID, in.LegalEntityID, in.Code).Scan(&exists); err != nil {
			return 0, nil, err
		}
		if exists {
			return 0, nil, problem(http.StatusConflict, CodeDuplicateCode, "cost element code already exists")
		}
		if in.Kind == domain.ElementPrimary {
			var overlaps bool
			if err := tx.QueryRow(ctx, `select exists(select 1 from cost_element where tenant_id = $1 and legal_entity_id = $2 and kind = 'Primary'
				and main_account_from <= $3 and main_account_to >= $4)`, p.TenantID, in.LegalEntityID, in.MainAccountTo, in.MainAccountFrom).Scan(&overlaps); err != nil {
				return 0, nil, err
			}
			if overlaps {
				return 0, nil, problem(http.StatusConflict, CodeAccountRangeOverlap, "main account range overlaps an existing primary element")
			}
		}
		in.ID = ids.New()
		if _, err := tx.Exec(ctx, `insert into cost_element (tenant_id, id, legal_entity_id, kind, code, name, main_account_from, main_account_to, default_behavior)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, p.TenantID, in.ID, in.LegalEntityID, in.Kind, in.Code, in.Name,
			nullIfEmpty(in.MainAccountFrom), nullIfEmpty(in.MainAccountTo), nullIfEmpty(in.DefaultBehavior)); err != nil {
			return 0, nil, err
		}
		return http.StatusCreated, in, nil
	})
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// MirrorJournalLines writes one primary cost entry per journal line (FR-38.6),
// classified Fixed, Variable or Unclassified by the behavior rules and element
// defaults in force for its period (FR-38.16). Lines already mirrored are
// skipped, so replays are safe.
func (s *Postgres) MirrorJournalLines(ctx context.Context, key, hash string, in MirrorRequest) (int, []byte, error) {
	p, _ := tenant.FromContext(ctx)
	return s.run(ctx, key, hash, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		ranges, err := primaryRanges(ctx, tx, p.TenantID, in.LegalEntityID)
		if err != nil {
			return 0, nil, err
		}
		objects := make([]string, 0, len(in.Lines))
		seen := map[string]bool{}
		for _, l := range in.Lines {
			if !seen[l.CostObjectID] {
				seen[l.CostObjectID] = true
				objects = append(objects, l.CostObjectID)
			}
		}
		if err := requireObjects(ctx, tx, p.TenantID, in.LegalEntityID, objects); err != nil {
			return 0, nil, err
		}
		classify, err := behaviorClassifier(ctx, tx, p.TenantID, in.LegalEntityID, objects)
		if err != nil {
			return 0, nil, err
		}
		resp := struct {
			Mirrored int `json:"mirrored"`
			Skipped  int `json:"skipped"`
		}{}
		for _, l := range in.Lines {
			elementID, err := domain.MapPrimary(ranges, l.MainAccount)
			if err != nil {
				return 0, nil, problem(http.StatusUnprocessableEntity, CodeNoPrimaryElement,
					fmt.Sprintf("no primary cost element maps main account %s", l.MainAccount))
			}
			var entryID string
			err = tx.QueryRow(ctx, `insert into cost_entry (tenant_id, id, legal_entity_id, cost_object_id, cost_element_id, period_key, currency, amount, behavior, origin, source_id)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'Journal', $10)
				on conflict (tenant_id, source_id) where origin = 'Journal' do nothing returning id`,
				p.TenantID, ids.New(), in.LegalEntityID, l.CostObjectID, elementID, l.Period, l.Currency, l.Amount.StringFixed(4),
				classify(elementID, l.CostObjectID, l.Period), l.JournalLineID).Scan(&entryID)
			if errors.Is(err, pgx.ErrNoRows) {
				resp.Skipped++
				continue
			}
			if err != nil {
				return 0, nil, err
			}
			resp.Mirrored++
			if err := enqueue(ctx, tx, "cost_entry", entryID, ccevents.CostEntryCreated, p.TenantID, ccevents.CostEntryCreatedData{
				EntryID: entryID, CostObjectID: l.CostObjectID, ElementID: elementID, Period: l.Period, Amount: l.Amount, Origin: "Journal",
			}); err != nil {
				return 0, nil, err
			}
		}
		return http.StatusOK, resp, nil
	})
}

func primaryRanges(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID string) ([]domain.AccountRange, error) {
	rows, err := tx.Query(ctx, `select id::text, main_account_from, main_account_to from cost_element
		where tenant_id = $1 and legal_entity_id = $2 and kind = 'Primary' order by main_account_from`, tenantID, legalEntityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.AccountRange
	for rows.Next() {
		var r domain.AccountRange
		if err := rows.Scan(&r.ElementID, &r.From, &r.To); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RunAllocation runs one reciprocal step for a period, stores the run and
// posts Allocation entries (FR-38.7). When the period already has a completed
// run, that run is reversed in the same transaction and the new one replaces
// it (BR-38.7). The step rolls back as a whole on error.
func (s *Postgres) RunAllocation(ctx context.Context, key, hash string, in RunRequest) (int, []byte, error) {
	p, _ := tenant.FromContext(ctx)
	return s.run(ctx, key, hash, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		// Lock the period's completed run so two reruns serialize; the loser
		// finds it Reversed, inserts a second Completed run and gets CST-0002.
		var priorID string
		err := tx.QueryRow(ctx, `select id::text from allocation_run where tenant_id = $1 and legal_entity_id = $2
			and period_key = $3 and status = 'Completed' for update`, p.TenantID, in.LegalEntityID, in.Period).Scan(&priorID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return 0, nil, err
		}
		var kind string
		err = tx.QueryRow(ctx, `select kind from cost_element where tenant_id = $1 and legal_entity_id = $2 and id = $3`,
			p.TenantID, in.LegalEntityID, in.SecondaryElementID).Scan(&kind)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && kind != domain.ElementSecondary) {
			return 0, nil, problem(http.StatusUnprocessableEntity, CodeSecondaryNotFound, "secondary cost element not found")
		}
		if err != nil {
			return 0, nil, err
		}
		if err := requireObjects(ctx, tx, p.TenantID, in.LegalEntityID, in.CostObjectIDs); err != nil {
			return 0, nil, err
		}
		// The cost base is journal-origin cost only, so the prior run's
		// postings never feed the rerun.
		costs, err := journalCosts(ctx, tx, p.TenantID, in.LegalEntityID, in.Period, in.Currency, in.CostObjectIDs)
		if err != nil {
			return 0, nil, err
		}
		res, err := domain.Allocate(domain.Cycle{Centers: in.CostObjectIDs, Costs: costs, Basis: in.Basis}, domain.DefaultSolveOptions())
		if err != nil {
			return 0, nil, allocationProblem(err)
		}
		if res.Unallocated == nil {
			res.Unallocated = []string{}
		}
		if res.Flows == nil {
			res.Flows = []domain.Flow{}
		}
		runID := ids.New()
		if priorID != "" {
			if err := reverseRun(ctx, tx, p.TenantID, in.LegalEntityID, priorID, runID); err != nil {
				return 0, nil, err
			}
		}
		objects, _ := json.Marshal(in.CostObjectIDs)
		flows, _ := json.Marshal(res.Flows)
		postings, _ := json.Marshal(res.Postings)
		unalloc, _ := json.Marshal(res.Unallocated)
		if _, err := tx.Exec(ctx, `insert into allocation_run (tenant_id, id, legal_entity_id, period_key, currency, secondary_element_id, method,
			iterations, residual, converged, swept, total_in, total_out, status, cost_object_ids, flows, postings, unallocated, reverses_run_id)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, 'Completed', $14, $15, $16, $17, $18)`,
			p.TenantID, runID, in.LegalEntityID, in.Period, in.Currency, in.SecondaryElementID, res.Method, res.Iterations,
			res.Residual.StringFixed(12), res.Converged, res.Swept.StringFixed(4), res.TotalIn.StringFixed(4), res.TotalOut.StringFixed(4),
			objects, flows, postings, unalloc, nullIfEmpty(priorID)); err != nil {
			return 0, nil, err
		}
		for _, post := range res.Postings {
			if post.Amount.IsZero() {
				continue
			}
			if err := postAllocationEntry(ctx, tx, p.TenantID, in.LegalEntityID, runID, post.CenterID, in.SecondaryElementID, in.Period, in.Currency, post.Amount); err != nil {
				return 0, nil, err
			}
		}
		if err := enqueue(ctx, tx, "allocation_run", runID, ccevents.AllocationCompleted, p.TenantID, ccevents.AllocationCompletedData{
			RunID: runID, LegalEntityID: in.LegalEntityID, Period: in.Period, Method: res.Method, Iterations: res.Iterations, Converged: res.Converged,
			TotalIn: res.TotalIn, TotalOut: res.TotalOut, Residual: res.Residual, Swept: res.Swept, ReversedRunID: priorID,
		}); err != nil {
			return 0, nil, err
		}
		return http.StatusCreated, RunResponse{RunID: runID, Period: in.Period, ReversedRunID: priorID, Result: res,
			Warnings: runWarnings(res.Unallocated, false, in.Period)}, nil
	})
}

// postAllocationEntry writes one Allocation entry of a run and its event.
func postAllocationEntry(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID, runID, objectID, elementID, period, currency string, amount decimal.Decimal) error {
	entryID := ids.New()
	if _, err := tx.Exec(ctx, `insert into cost_entry (tenant_id, id, legal_entity_id, cost_object_id, cost_element_id, period_key, currency, amount,
		origin, source_id, allocation_run_id) values ($1, $2, $3, $4, $5, $6, $7, $8, 'Allocation', $9, $9)`,
		tenantID, entryID, legalEntityID, objectID, elementID, period, currency, amount.StringFixed(4), runID); err != nil {
		return err
	}
	return enqueue(ctx, tx, "cost_entry", entryID, ccevents.CostEntryCreated, tenantID, ccevents.CostEntryCreatedData{
		EntryID: entryID, CostObjectID: objectID, ElementID: elementID, Period: period, Amount: amount, Origin: "Allocation",
	})
}

// reverseRun posts the negation of every Allocation entry of runID (BR-38.7)
// and marks it Reversed by byRunID. The negations carry runID, so the reversed
// run nets to zero per cost object and the new run's entries stand alone.
func reverseRun(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID, runID, byRunID string) error {
	rows, err := tx.Query(ctx, `select cost_object_id::text, cost_element_id::text, period_key, currency, sum(amount)::text
		from cost_entry where tenant_id = $1 and allocation_run_id = $2 and origin = 'Allocation'
		group by cost_object_id, cost_element_id, period_key, currency order by cost_object_id`, tenantID, runID)
	if err != nil {
		return err
	}
	type line struct {
		object, element, period, currency string
		amount                            decimal.Decimal
	}
	var lines []line
	for rows.Next() {
		var l line
		var amount string
		if err := rows.Scan(&l.object, &l.element, &l.period, &l.currency, &amount); err != nil {
			rows.Close()
			return err
		}
		l.amount = parseDec(amount)
		lines = append(lines, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, l := range lines {
		if l.amount.IsZero() {
			continue
		}
		if err := postAllocationEntry(ctx, tx, tenantID, legalEntityID, runID, l.object, l.element, l.period, l.currency, l.amount.Neg()); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `update allocation_run set status = 'Reversed', reversed_by_run_id = $3 where tenant_id = $1 and id = $2`, tenantID, runID, byRunID)
	return err
}

// runWarnings lists CO-0613 for each sender with zero basis (BR-38.8) and
// CO-0615 when journal cost arrived after the run (BR-38.7).
func runWarnings(unallocated []string, late bool, period string) []Warning {
	out := []Warning{}
	for _, id := range unallocated {
		out = append(out, Warning{Code: "CO-0613", Detail: fmt.Sprintf("sender %s has zero basis; its cost was not allocated", id)})
	}
	if late {
		out = append(out, Warning{Code: "CO-0615", Detail: fmt.Sprintf("journal postings after the last allocation run for %s; rerun the allocation", period)})
	}
	return out
}

func allocationProblem(err error) error {
	switch {
	case errors.Is(err, domain.ErrSingular):
		return problem(http.StatusUnprocessableEntity, CodeNotConverged, "the cycle is closed: some sender reaches no final receiver, so its cost cannot leave the step")
	case errors.Is(err, domain.ErrConservation):
		return problem(http.StatusInternalServerError, CodeConservation, "allocation totals differ; the run was rolled back")
	default:
		return problem(http.StatusUnprocessableEntity, CodeInvalidAllocation, err.Error())
	}
}

func requireObjects(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID string, objectIDs []string) error {
	var found int
	if err := tx.QueryRow(ctx, `select count(distinct id) from cost_object where tenant_id = $1 and legal_entity_id = $2 and id = any($3::uuid[])`,
		tenantID, legalEntityID, objectIDs).Scan(&found); err != nil {
		return err
	}
	if found != len(objectIDs) {
		return problem(http.StatusNotFound, CodeObjectNotFound, "one or more cost objects were not found")
	}
	return nil
}

// journalCosts sums journal-origin entries per cost object in index order of objectIDs.
func journalCosts(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID, period, currency string, objectIDs []string) ([]decimal.Decimal, error) {
	rows, err := tx.Query(ctx, `select cost_object_id::text, sum(amount)::text from cost_entry
		where tenant_id = $1 and legal_entity_id = $2 and period_key = $3 and currency = $4 and origin = 'Journal' and cost_object_id = any($5::uuid[])
		group by cost_object_id`, tenantID, legalEntityID, period, currency, objectIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sums := map[string]decimal.Decimal{}
	for rows.Next() {
		var id, sum string
		if err := rows.Scan(&id, &sum); err != nil {
			return nil, err
		}
		sums[id] = parseDec(sum)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]decimal.Decimal, len(objectIDs))
	for i, id := range objectIDs {
		out[i] = sums[id]
	}
	return out, nil
}

// EntriesFor lists cost entries of one object for one period (cost object line items).
func (s *Postgres) EntriesFor(ctx context.Context, costObjectID, period string) ([]Entry, error) {
	p, _ := tenant.FromContext(ctx)
	out := []Entry{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `select exists(select 1 from cost_object where tenant_id = $1 and id = $2)`, p.TenantID, costObjectID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return problem(http.StatusNotFound, CodeObjectNotFound, "cost object not found")
		}
		rows, err := tx.Query(ctx, `select id::text, cost_element_id::text, source_id::text, coalesce(allocation_run_id::text, ''), coalesce(top_down_run_id::text, ''),
				coalesce(settlement_id::text, ''), coalesce(template_run_id::text, ''), period_key, currency, amount::text, behavior, origin
			from cost_entry where tenant_id = $1 and cost_object_id = $2 and period_key = $3 order by created_at, id`, p.TenantID, costObjectID, period)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e Entry
			var amount string
			if err := rows.Scan(&e.ID, &e.ElementID, &e.SourceID, &e.RunID, &e.TopDownID, &e.SettleID, &e.TplRunID, &e.Period, &e.Currency, &amount, &e.Behavior, &e.Origin); err != nil {
				return err
			}
			e.Amount = parseDec(amount)
			out = append(out, e)
		}
		return rows.Err()
	})
	return out, err
}

func parseDec(s string) decimal.Decimal {
	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Zero
	}
	return d
}
