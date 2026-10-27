package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/lean/internal/domain"
	leanevents "github.com/BenjaminSRussell/passage/services/lean/internal/events"
)

// Problem codes of flow costing (README "Error codes").
const (
	CodePeriodOverlap  = "LEAN-0109"
	CodePeriodNotFound = "LEAN-0110"
	CodePeriodSettled  = "LEAN-0111"
	CodePeriodNotEnded = "LEAN-0112"
)

// Period is a FlowCostingPeriod (PRD 4.24.4, FR-24.12). Dates are inclusive
// calendar days in UTC. The amounts are null while the period is open and are
// decimal strings at the currency's minor unit once it is settled; Kanbans is
// the number of cards with jobs in the period at settlement. JournalEntryID
// stays null until the ledger integration posts the journal.
type Period struct {
	ID             string     `json:"id"`
	FlowID         string     `json:"flowId"`
	PeriodStart    string     `json:"periodStart"`
	PeriodEnd      string     `json:"periodEnd"`
	Currency       string     `json:"currency"`
	Status         string     `json:"status"`
	MaterialCost   *string    `json:"materialCost"`
	ConversionCost *string    `json:"conversionCost"`
	OutputValue    *string    `json:"outputValue"`
	Variance       *string    `json:"variance"`
	Kanbans        *int       `json:"kanbans"`
	JournalEntryID *string    `json:"journalEntryId"`
	SettledAt      *time.Time `json:"settledAt"`
	SettledBy      *string    `json:"settledBy"`
	Version        int        `json:"version"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
}

// OpenPeriod is the command that opens a costing period.
type OpenPeriod struct {
	FlowID      string
	PeriodStart time.Time
	PeriodEnd   time.Time
	Currency    string
}

// SettlePeriod is the command that settles a period with its amounts.
type SettlePeriod struct {
	FlowID         string
	PeriodID       string
	Version        int
	MaterialCost   decimal.Decimal
	ConversionCost decimal.Decimal
	OutputValue    decimal.Decimal
}

const periodCols = `id, flow_id, period_start, period_end, currency, status, material_cost, conversion_cost, output_value, variance,
	settled_kanbans, journal_entry_id, settled_at, settled_by, version, created_at, updated_at`

func scanPeriod(row pgx.Row) (Period, error) {
	var p Period
	var start, end time.Time
	var amounts [4]*decimal.Decimal
	err := row.Scan(&p.ID, &p.FlowID, &start, &end, &p.Currency, &p.Status, &amounts[0], &amounts[1], &amounts[2], &amounts[3],
		&p.Kanbans, &p.JournalEntryID, &p.SettledAt, &p.SettledBy, &p.Version, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return p, err
	}
	p.PeriodStart, p.PeriodEnd = start.Format(DateLayout), end.Format(DateLayout)
	minor := money.MinorUnits(p.Currency)
	fixed := func(d *decimal.Decimal) *string {
		if d == nil {
			return nil
		}
		s := d.StringFixed(minor)
		return &s
	}
	p.MaterialCost, p.ConversionCost, p.OutputValue, p.Variance = fixed(amounts[0]), fixed(amounts[1]), fixed(amounts[2]), fixed(amounts[3])
	return p, nil
}

// getPeriod reads a period of a flow; lock is "" or " for update". A period
// of another flow is not found, so a URL cannot settle it under the wrong flow.
func getPeriod(ctx context.Context, tx pgx.Tx, flowID, id, lock string) (Period, error) {
	p, err := scanPeriod(tx.QueryRow(ctx, `select `+periodCols+` from flow_costing_period where id = $1 and flow_id = $2`+lock, id, flowID))
	if errors.Is(err, pgx.ErrNoRows) {
		return p, notFound(CodePeriodNotFound, "costing period", id)
	}
	return p, err
}

// OpenPeriod opens a costing period for a flow (FR-24.12). The flow row is
// locked, so two concurrent opens cannot both pass the overlap check: periods
// of one flow never share a day (LEAN-0109). A retry with the same key returns
// the period it opened.
func (s *Postgres) OpenPeriod(ctx context.Context, in OpenPeriod, idem Idem) (Period, bool, error) {
	var out Period
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var flowName string
		if err := tx.QueryRow(ctx, `select name from production_flow where id = $1 for update`, in.FlowID).Scan(&flowName); errors.Is(err, pgx.ErrNoRows) {
			return notFound(CodeFlowNotFound, "production flow", in.FlowID)
		} else if err != nil {
			return err
		}
		if id, err := rowReplay(ctx, tx, "flow_costing_period", idem); err != nil || id != "" {
			if err == nil {
				// The fingerprint covers the path, so a replay is always for this flow.
				out, err = getPeriod(ctx, tx, in.FlowID, id, "")
			}
			return err
		}
		var clashStart, clashEnd time.Time
		err := tx.QueryRow(ctx, `select period_start, period_end from flow_costing_period
			where flow_id = $1 and period_start <= $3::date and period_end >= $2::date order by period_start limit 1`,
			in.FlowID, in.PeriodStart, in.PeriodEnd).Scan(&clashStart, &clashEnd)
		if err == nil {
			return httpx.NewProblem(http.StatusConflict, CodePeriodOverlap, fmt.Sprintf("production flow %s already has a costing period from %s to %s",
				flowName, clashStart.Format(DateLayout), clashEnd.Format(DateLayout)))
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		id := ids.New()
		if _, err := tx.Exec(ctx, `insert into flow_costing_period (tenant_id, id, flow_id, period_start, period_end, currency, idempotency_key, idempotency_fingerprint)
			values ($1, $2, $3, $4::date, $5::date, $6, nullif($7, ''), nullif($8, ''))`,
			principal(ctx).TenantID, id, in.FlowID, in.PeriodStart, in.PeriodEnd, in.Currency, idem.Key, idem.Fingerprint); err != nil {
			return mapDuplicate(err, "the flow already has a costing period starting "+in.PeriodStart.Format(DateLayout))
		}
		out, err = getPeriod(ctx, tx, in.FlowID, id, "")
		created = err == nil
		return err
	})
	return out, created, err
}

// ListPeriods returns a flow's costing periods, latest first.
func (s *Postgres) ListPeriods(ctx context.Context, flowID string, limit int) ([]Period, error) {
	out := []Period{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var one int
		if err := tx.QueryRow(ctx, `select 1 from production_flow where id = $1`, flowID).Scan(&one); errors.Is(err, pgx.ErrNoRows) {
			return notFound(CodeFlowNotFound, "production flow", flowID)
		} else if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select `+periodCols+` from flow_costing_period where flow_id = $1 order by period_start desc limit $2`, flowID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			p, err := scanPeriod(rows)
			if err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	return out, err
}

// periodCards returns the cards of a flow with jobs in a period (BR-24.9): a
// job started on or before the period's last day (UTC). A card that started
// before the period and is still open had work in it too, so it is included;
// its state decides whether it blocks. Reusing an emptied card clears its
// jobs' start times, so a card's earlier cycle never blocks a later period.
// ID carries the card number for the LEAN-0003 message.
func periodCards(ctx context.Context, tx pgx.Tx, flowID string, end time.Time) ([]domain.Kanban, error) {
	rows, err := tx.Query(ctx, `select k.card_number, k.state from kanban k
		where exists (select 1 from kanban_job j join flow_activity a on a.id = j.activity_id
			where j.kanban_id = k.id and a.flow_id = $1 and j.started_at is not null
			  and j.started_at < (($2::date + 1)::timestamp at time zone 'UTC'))
		order by k.card_number`, flowID, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Kanban
	for rows.Next() {
		var k domain.Kanban
		if err := rows.Scan(&k.ID, &k.State); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// SettlePeriod settles an open period after its last day (BR-24.9): every card
// with jobs in the period must be Completed or Emptied (LEAN-0003 lists the
// open ones), and the variance is material + conversion − output in the
// period's currency. The period row is locked, so a period settles once; the
// flowperiod.settled event is written in the same transaction. version is the
// period version the caller read.
func (s *Postgres) SettlePeriod(ctx context.Context, in SettlePeriod, idem Idem) (Period, bool, error) {
	var out Period
	changed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		p, err := getPeriod(ctx, tx, in.FlowID, in.PeriodID, " for update")
		if err != nil {
			return err
		}
		if found, err := replay(ctx, tx, idem, &out); found || err != nil {
			return err
		}
		if p.Version != in.Version {
			return staleVersion(fmt.Sprintf("costing period %s to %s", p.PeriodStart, p.PeriodEnd), p.Version, in.Version)
		}
		if p.Status != domain.PeriodOpen {
			return httpx.NewProblem(http.StatusConflict, CodePeriodSettled, fmt.Sprintf("costing period %s to %s is already settled", p.PeriodStart, p.PeriodEnd))
		}
		var ended bool
		if err := tx.QueryRow(ctx, `select (now() at time zone 'UTC')::date > $1::date`, p.PeriodEnd).Scan(&ended); err != nil {
			return err
		}
		if !ended {
			return httpx.NewProblem(http.StatusConflict, CodePeriodNotEnded, fmt.Sprintf("costing period %s to %s settles after its last day", p.PeriodStart, p.PeriodEnd))
		}
		costs, err := domain.SettleCosts(p.Currency, in.MaterialCost, in.ConversionCost, in.OutputValue)
		if err != nil {
			return err
		}
		end, _ := time.Parse(DateLayout, p.PeriodEnd)
		cards, err := periodCards(ctx, tx, in.FlowID, end)
		if err != nil {
			return err
		}
		if err := domain.CheckSettleable(cards); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update flow_costing_period set status = 'settled', material_cost = $2, conversion_cost = $3, output_value = $4,
				variance = $5, settled_kanbans = $6, settled_at = now(), settled_by = nullif($7, ''), version = version + 1, updated_at = now()
			where id = $1`, p.ID, costs.Material.Value, costs.Conversion.Value, costs.Output.Value, costs.Variance.Value, len(cards),
			principal(ctx).UserID); err != nil {
			return err
		}
		if p, err = getPeriod(ctx, tx, in.FlowID, p.ID, ""); err != nil {
			return err
		}
		if err := enqueue(ctx, tx, "flowperiod", p.ID, leanevents.FlowPeriodSettled, leanevents.FlowPeriodSettledData{
			FlowID: p.FlowID, PeriodID: p.ID, PeriodStart: p.PeriodStart, PeriodEnd: p.PeriodEnd, Currency: p.Currency,
			MaterialCost: *p.MaterialCost, ConversionCost: *p.ConversionCost, OutputValue: *p.OutputValue, Variance: *p.Variance,
			Kanbans: len(cards), JournalEntryID: p.JournalEntryID}); err != nil {
			return err
		}
		out, changed = p, true
		return remember(ctx, tx, idem, http.StatusOK, p)
	})
	return out, changed, err
}
