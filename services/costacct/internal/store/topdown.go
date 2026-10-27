package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/costacct/internal/domain"
	ccevents "github.com/BenjaminSRussell/passage/services/costacct/internal/events"
)

const (
	CodeTopDownRunNotFound = "CST-0011"
	CodeExceedsBooked      = "CST-0012"
	CodeOverSettlement     = "CO-0618"
	CodeNotInternalOrder   = "CST-0013"
	oneTopDownRun          = "top_down_run_one_completed"
)

// TopDownSegment is one detailed receiver with its reference basis.
type TopDownSegment struct {
	CostObjectID string          `json:"costObjectId"`
	Basis        decimal.Decimal `json:"basis"`
}

// TopDownRequest distributes Amount booked on SourceCostObjectID and
// CostElementID in Period to Segments (FR-38.19).
type TopDownRequest struct {
	LegalEntityID      string
	Period             string
	ReferencePeriod    string
	Currency           string
	SourceCostObjectID string
	CostElementID      string
	Amount             decimal.Decimal
	Segments           []TopDownSegment
}

// TopDownRun is a stored top-down distribution with its cents-exact shares.
type TopDownRun struct {
	ID                 string          `json:"id"`
	LegalEntityID      string          `json:"legalEntityId"`
	Period             string          `json:"period"`
	ReferencePeriod    string          `json:"referencePeriod,omitempty"`
	Currency           string          `json:"currency"`
	SourceCostObjectID string          `json:"sourceCostObjectId"`
	CostElementID      string          `json:"costElementId"`
	Amount             decimal.Decimal `json:"amount"`
	Shares             []domain.Share  `json:"shares"`
	Excluded           []string        `json:"excluded"`
	Status             string          `json:"status"`
	CreatedAt          string          `json:"createdAt"`
}

// bookedOn sums every entry of one object, element, period and currency.
func bookedOn(ctx context.Context, tx pgx.Tx, tenantID, objectID, elementID, period, currency string) (decimal.Decimal, error) {
	var s string
	err := tx.QueryRow(ctx, `select coalesce(sum(amount), 0)::text from cost_entry where tenant_id = $1 and cost_object_id = $2
		and ($3 = '' or cost_element_id::text = $3) and period_key = $4 and currency = $5`, tenantID, objectID, elementID, period, currency).Scan(&s)
	return parseDec(s), err
}

// lockObject locks one cost object row of a legal entity and returns its kind.
// Commands that read an object's balance and then post against it hold this
// lock, so two of them cannot both spend the same balance.
func lockObject(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID, id string) (string, error) {
	var kind string
	err := tx.QueryRow(ctx, `select kind from cost_object where tenant_id = $1 and ($2 = '' or legal_entity_id::text = $2) and id = $3 for update`,
		tenantID, legalEntityID, id).Scan(&kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", problem(http.StatusNotFound, CodeObjectNotFound, "cost object not found")
	}
	return kind, err
}

// postEntry writes one Allocation-origin entry linked to its run or settlement
// and enqueues costentry.created in the same transaction.
func postEntry(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID, objectID, elementID, period, currency string, amount decimal.Decimal,
	sourceID, linkColumn string) error {
	entryID := ids.New()
	if _, err := tx.Exec(ctx, `insert into cost_entry (tenant_id, id, legal_entity_id, cost_object_id, cost_element_id, period_key, currency,
		amount, origin, source_id, `+linkColumn+`) values ($1, $2, $3, $4, $5, $6, $7, $8, 'Allocation', $9, $9)`,
		tenantID, entryID, legalEntityID, objectID, elementID, period, currency, amount.StringFixed(4), sourceID); err != nil {
		return err
	}
	return enqueue(ctx, tx, "cost_entry", entryID, ccevents.CostEntryCreated, tenantID, ccevents.CostEntryCreatedData{
		EntryID: entryID, CostObjectID: objectID, ElementID: elementID, Period: period, Amount: amount, Origin: "Allocation",
	})
}

// RunTopDown distributes an aggregated amount to detailed segments in one
// transaction (FR-38.19, BR-38.13). The source is credited and each segment is
// debited on the same element, so the run nets to zero and Σ primary costs per
// element (the GL reconciliation, AC-38.6) is unchanged. The amount may not
// exceed what is booked on the source for the element and period.
func (s *Postgres) RunTopDown(ctx context.Context, key, hash string, in TopDownRequest) (int, []byte, error) {
	p, _ := tenant.FromContext(ctx)
	return s.run(ctx, key, hash, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		if _, err := lockObject(ctx, tx, p.TenantID, in.LegalEntityID, in.SourceCostObjectID); err != nil {
			return 0, nil, err
		}
		var done bool
		if err := tx.QueryRow(ctx, `select exists(select 1 from top_down_run where tenant_id = $1 and legal_entity_id = $2 and period_key = $3
			and source_cost_object_id = $4 and cost_element_id = $5 and status = 'Completed')`,
			p.TenantID, in.LegalEntityID, in.Period, in.SourceCostObjectID, in.CostElementID).Scan(&done); err != nil {
			return 0, nil, err
		}
		if done {
			return 0, nil, problem(http.StatusConflict, CodePeriodAllocated, "this source and element already have a completed top-down run for the period")
		}
		var exists bool
		if err := tx.QueryRow(ctx, `select exists(select 1 from cost_element where tenant_id = $1 and legal_entity_id = $2 and id = $3)`,
			p.TenantID, in.LegalEntityID, in.CostElementID).Scan(&exists); err != nil {
			return 0, nil, err
		}
		if !exists {
			return 0, nil, problem(http.StatusNotFound, CodeSecondaryNotFound, "cost element not found")
		}
		segIDs := make([]string, len(in.Segments))
		segs := make([]domain.Segment, len(in.Segments))
		for i, sg := range in.Segments {
			segIDs[i] = sg.CostObjectID
			segs[i] = domain.Segment{ID: sg.CostObjectID, Basis: sg.Basis}
		}
		if err := requireObjects(ctx, tx, p.TenantID, in.LegalEntityID, segIDs); err != nil {
			return 0, nil, err
		}
		booked, err := bookedOn(ctx, tx, p.TenantID, in.SourceCostObjectID, in.CostElementID, in.Period, in.Currency)
		if err != nil {
			return 0, nil, err
		}
		if in.Amount.GreaterThan(booked) {
			return 0, nil, problem(http.StatusUnprocessableEntity, CodeExceedsBooked,
				fmt.Sprintf("amount %s exceeds the %s booked on the source for the element and period", in.Amount.StringFixed(2), booked.StringFixed(2)))
		}
		res, err := domain.TopDown(in.Amount, segs, money.MinorUnits(in.Currency))
		if errors.Is(err, domain.ErrConservation) {
			return 0, nil, problem(http.StatusInternalServerError, CodeConservation, "top-down totals differ; the run was rolled back")
		}
		if err != nil {
			return 0, nil, problem(http.StatusUnprocessableEntity, CodeInvalidAllocation, err.Error())
		}
		shares, _ := json.Marshal(res.Shares)
		excluded, _ := json.Marshal(res.Excluded)
		run := TopDownRun{ID: ids.New(), LegalEntityID: in.LegalEntityID, Period: in.Period, ReferencePeriod: in.ReferencePeriod, Currency: in.Currency,
			SourceCostObjectID: in.SourceCostObjectID, CostElementID: in.CostElementID, Amount: in.Amount, Shares: res.Shares, Excluded: res.Excluded,
			Status: "Completed"}
		created := time.Now().UTC()
		run.CreatedAt = stamp(created)
		if _, err := tx.Exec(ctx, `insert into top_down_run (tenant_id, id, legal_entity_id, period_key, reference_period, currency, source_cost_object_id,
			cost_element_id, amount, shares, excluded, status, created_at) values ($1, $2, $3, $4, nullif($5, ''), $6, $7, $8, $9, $10, $11, 'Completed', $12)`,
			p.TenantID, run.ID, in.LegalEntityID, in.Period, in.ReferencePeriod, in.Currency, in.SourceCostObjectID, in.CostElementID,
			in.Amount.StringFixed(4), shares, excluded, created); err != nil {
			return 0, nil, err
		}
		if err := postEntry(ctx, tx, p.TenantID, in.LegalEntityID, in.SourceCostObjectID, in.CostElementID, in.Period, in.Currency,
			in.Amount.Neg(), run.ID, "top_down_run_id"); err != nil {
			return 0, nil, err
		}
		for _, sh := range res.Shares {
			if sh.Amount.IsZero() {
				continue
			}
			if err := postEntry(ctx, tx, p.TenantID, in.LegalEntityID, sh.SegmentID, in.CostElementID, in.Period, in.Currency,
				sh.Amount, run.ID, "top_down_run_id"); err != nil {
				return 0, nil, err
			}
		}
		if err := enqueue(ctx, tx, "top_down_run", run.ID, ccevents.AllocationCompleted, p.TenantID, ccevents.AllocationCompletedData{
			RunID: run.ID, LegalEntityID: in.LegalEntityID, Period: in.Period, Method: "TopDown", Converged: true,
			TotalIn: in.Amount, TotalOut: res.Total, Residual: decimal.Zero, Swept: decimal.Zero,
		}); err != nil {
			return 0, nil, err
		}
		return http.StatusCreated, run, nil
	})
}

const topDownColumns = `id::text, legal_entity_id::text, period_key, coalesce(reference_period, ''), currency, source_cost_object_id::text,
	cost_element_id::text, amount::text, shares, excluded, status, created_at`

func scanTopDown(row pgx.Row) (TopDownRun, error) {
	var r TopDownRun
	var amount string
	var shares, excluded []byte
	var created time.Time
	if err := row.Scan(&r.ID, &r.LegalEntityID, &r.Period, &r.ReferencePeriod, &r.Currency, &r.SourceCostObjectID, &r.CostElementID,
		&amount, &shares, &excluded, &r.Status, &created); err != nil {
		return r, err
	}
	r.Amount, r.CreatedAt = parseDec(amount), stamp(created)
	if err := json.Unmarshal(shares, &r.Shares); err != nil {
		return r, err
	}
	return r, json.Unmarshal(excluded, &r.Excluded)
}

// ListTopDownRuns lists top-down runs, newest period first; legalEntityID narrows it when set.
func (s *Postgres) ListTopDownRuns(ctx context.Context, legalEntityID string) ([]TopDownRun, error) {
	out := []TopDownRun{}
	err := s.read(ctx, func(tx pgx.Tx, tenantID string) error {
		rows, err := tx.Query(ctx, `select `+topDownColumns+` from top_down_run
			where tenant_id = $1 and ($2 = '' or legal_entity_id::text = $2) order by period_key desc, created_at desc limit $3`,
			tenantID, legalEntityID, listLimit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanTopDown(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// GetTopDownRun returns one top-down run, or CST-0011.
func (s *Postgres) GetTopDownRun(ctx context.Context, id string) (TopDownRun, error) {
	var r TopDownRun
	err := s.read(ctx, func(tx pgx.Tx, tenantID string) error {
		var err error
		r, err = scanTopDown(tx.QueryRow(ctx, `select `+topDownColumns+` from top_down_run where tenant_id = $1 and id = $2`, tenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return problem(http.StatusNotFound, CodeTopDownRunNotFound, "top-down run not found")
		}
		return err
	})
	return r, err
}
