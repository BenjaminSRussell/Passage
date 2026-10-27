package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/costacct/internal/domain"
)

// CodeRunNotFound is returned for an unknown allocation run id.
const CodeRunNotFound = "CST-0010"

// Run is a stored allocation run with its cents-exact result.
type Run struct {
	ID                 string           `json:"id"`
	LegalEntityID      string           `json:"legalEntityId"`
	Period             string           `json:"period"`
	Currency           string           `json:"currency"`
	SecondaryElementID string           `json:"secondaryElementId"`
	Method             string           `json:"method"`
	Iterations         int              `json:"iterations"`
	Residual           decimal.Decimal  `json:"residual"`
	Converged          bool             `json:"converged"`
	Swept              decimal.Decimal  `json:"swept"`
	TotalIn            decimal.Decimal  `json:"totalIn"`
	TotalOut           decimal.Decimal  `json:"totalOut"`
	Status             string           `json:"status"`
	CostObjectIDs      []string         `json:"costObjectIds"`
	Flows              []domain.Flow    `json:"flows"`
	Postings           []domain.Posting `json:"postings"`
	Unallocated        []string         `json:"unallocated"`
	CreatedAt          string           `json:"createdAt"`
	// ReversesRunID is the run this rerun replaced; ReversedByRunID the rerun
	// that replaced this one (BR-38.7).
	ReversesRunID   string `json:"reversesRunId,omitempty"`
	ReversedByRunID string `json:"reversedByRunId,omitempty"`
	// Warnings: CO-0613 per zero-basis sender; CO-0615 when journal cost for
	// the run's objects arrived after a completed run, so it needs a rerun.
	Warnings []Warning `json:"warnings"`
}

// ReconcileRequest carries the journal balances of one legal entity, period and currency.
type ReconcileRequest struct {
	LegalEntityID string
	Period        string
	Currency      string
	Journal       []domain.JournalBalance
}

func (s *Postgres) read(ctx context.Context, fn func(tx pgx.Tx, tenantID string) error) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error { return fn(tx, p.TenantID) })
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

const objectColumns = `id::text, legal_entity_id::text, kind, code, name`

// ListCostObjects lists cost objects by code; legalEntityID and kind narrow it when set.
func (s *Postgres) ListCostObjects(ctx context.Context, legalEntityID, kind string) ([]CostObject, error) {
	out := []CostObject{}
	err := s.read(ctx, func(tx pgx.Tx, tenantID string) error {
		rows, err := tx.Query(ctx, `select `+objectColumns+` from cost_object
			where tenant_id = $1 and ($2 = '' or legal_entity_id::text = $2) and ($3 = '' or kind = $3) order by code limit $4`,
			tenantID, legalEntityID, kind, listLimit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var o CostObject
			if err := rows.Scan(&o.ID, &o.LegalEntityID, &o.Kind, &o.Code, &o.Name); err != nil {
				return err
			}
			out = append(out, o)
		}
		return rows.Err()
	})
	return out, err
}

// GetCostObject returns one cost object, or CST-0001.
func (s *Postgres) GetCostObject(ctx context.Context, id string) (CostObject, error) {
	var o CostObject
	err := s.read(ctx, func(tx pgx.Tx, tenantID string) error {
		err := tx.QueryRow(ctx, `select `+objectColumns+` from cost_object where tenant_id = $1 and id = $2`, tenantID, id).
			Scan(&o.ID, &o.LegalEntityID, &o.Kind, &o.Code, &o.Name)
		if errors.Is(err, pgx.ErrNoRows) {
			return problem(http.StatusNotFound, CodeObjectNotFound, "cost object not found")
		}
		return err
	})
	return o, err
}

// ListCostElements lists cost elements by code; legalEntityID narrows it when set.
func (s *Postgres) ListCostElements(ctx context.Context, legalEntityID string) ([]CostElement, error) {
	out := []CostElement{}
	err := s.read(ctx, func(tx pgx.Tx, tenantID string) error {
		rows, err := tx.Query(ctx, `select id::text, legal_entity_id::text, kind, code, name, coalesce(main_account_from, ''), coalesce(main_account_to, ''),
				coalesce(default_behavior, '')
			from cost_element where tenant_id = $1 and ($2 = '' or legal_entity_id::text = $2) order by code limit $3`, tenantID, legalEntityID, listLimit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e CostElement
			if err := rows.Scan(&e.ID, &e.LegalEntityID, &e.Kind, &e.Code, &e.Name, &e.MainAccountFrom, &e.MainAccountTo, &e.DefaultBehavior); err != nil {
				return err
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	return out, err
}

// runColumns reads a run from allocation_run r. late is true when a journal
// entry for one of the run's objects, its period and currency was written
// after a completed run (CO-0615, BR-38.7).
const runColumns = `r.id::text, r.legal_entity_id::text, r.period_key, r.currency, r.secondary_element_id::text, r.method, r.iterations, r.residual::text,
	r.converged, r.swept::text, r.total_in::text, r.total_out::text, r.status, r.cost_object_ids, r.flows, r.postings, r.unallocated, r.created_at,
	coalesce(r.reverses_run_id::text, ''), coalesce(r.reversed_by_run_id::text, ''),
	r.status = 'Completed' and exists(select 1 from cost_entry e where e.tenant_id = r.tenant_id and e.legal_entity_id = r.legal_entity_id
		and e.period_key = r.period_key and e.currency = r.currency and e.origin = 'Journal' and e.created_at > r.created_at
		and e.cost_object_id in (select jsonb_array_elements_text(r.cost_object_ids)::uuid))`

func scanRun(row pgx.Row) (Run, error) {
	var r Run
	var residual, swept, in, out string
	var objects, flows, postings, unalloc []byte
	var created time.Time
	var late bool
	if err := row.Scan(&r.ID, &r.LegalEntityID, &r.Period, &r.Currency, &r.SecondaryElementID, &r.Method, &r.Iterations, &residual,
		&r.Converged, &swept, &in, &out, &r.Status, &objects, &flows, &postings, &unalloc, &created,
		&r.ReversesRunID, &r.ReversedByRunID, &late); err != nil {
		return r, err
	}
	r.Residual, r.Swept, r.TotalIn, r.TotalOut, r.CreatedAt = parseDec(residual), parseDec(swept), parseDec(in), parseDec(out), stamp(created)
	for _, part := range []struct {
		raw []byte
		v   any
	}{{objects, &r.CostObjectIDs}, {flows, &r.Flows}, {postings, &r.Postings}, {unalloc, &r.Unallocated}} {
		if err := json.Unmarshal(part.raw, part.v); err != nil {
			return r, err
		}
	}
	r.Warnings = runWarnings(r.Unallocated, late, r.Period)
	return r, nil
}

// ListRuns lists allocation runs, newest period first; legalEntityID narrows it when set.
func (s *Postgres) ListRuns(ctx context.Context, legalEntityID string) ([]Run, error) {
	out := []Run{}
	err := s.read(ctx, func(tx pgx.Tx, tenantID string) error {
		rows, err := tx.Query(ctx, `select `+runColumns+` from allocation_run r
			where r.tenant_id = $1 and ($2 = '' or r.legal_entity_id::text = $2) order by r.period_key desc, r.created_at desc limit $3`,
			tenantID, legalEntityID, listLimit)
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

// GetRun returns one allocation run, or CST-0010.
func (s *Postgres) GetRun(ctx context.Context, id string) (Run, error) {
	var r Run
	err := s.read(ctx, func(tx pgx.Tx, tenantID string) error {
		var err error
		r, err = scanRun(tx.QueryRow(ctx, `select `+runColumns+` from allocation_run r where r.tenant_id = $1 and r.id = $2`, tenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return problem(http.StatusNotFound, CodeRunNotFound, "allocation run not found")
		}
		return err
	})
	return r, err
}

// Reconcile compares Σ primary cost entries per element with the journal
// balances the caller supplies for the period (FR-38.11, AC-38.6).
func (s *Postgres) Reconcile(ctx context.Context, in ReconcileRequest) (domain.Reconciliation, error) {
	var out domain.Reconciliation
	err := s.read(ctx, func(tx pgx.Tx, tenantID string) error {
		ranges, err := primaryRanges(ctx, tx, tenantID, in.LegalEntityID)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select cost_element_id::text, sum(amount)::text from cost_entry
			where tenant_id = $1 and legal_entity_id = $2 and period_key = $3 and currency = $4 and origin = 'Journal'
			group by cost_element_id`, tenantID, in.LegalEntityID, in.Period, in.Currency)
		if err != nil {
			return err
		}
		defer rows.Close()
		ledger := map[string]decimal.Decimal{}
		for rows.Next() {
			var id, sum string
			if err := rows.Scan(&id, &sum); err != nil {
				return err
			}
			ledger[id] = parseDec(sum)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		out = domain.Reconcile(ranges, in.Journal, ledger)
		return nil
	})
	return out, err
}
