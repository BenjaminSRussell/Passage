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
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/lean/internal/domain"
	leanevents "github.com/BenjaminSRussell/passage/services/lean/internal/events"
)

// Job is a KanbanJob (FR-24.10, PRD 4.24.4): a card's work at one activity of
// its rule's production flow, with the card and activity fields the work-cell
// board shows. ReportedQty, ScrapQty and HandlingUnits are set once the job is
// completed.
type Job struct {
	ID                 string           `json:"id"`
	KanbanID           string           `json:"kanbanId"`
	CardNumber         string           `json:"cardNumber"`
	RuleID             string           `json:"ruleId"`
	RuleCode           string           `json:"ruleCode"`
	ItemID             string           `json:"itemId"`
	RuleType           string           `json:"ruleType"`
	CardState          domain.State     `json:"cardState"`
	CardQty            decimal.Decimal  `json:"cardQty"`
	Due                *time.Time       `json:"due"`
	ActivityID         string           `json:"activityId"`
	ActivityName       string           `json:"activityName"`
	ActivityKind       string           `json:"activityKind"`
	Sequence           int              `json:"sequence"`
	WorkCellResourceID *string          `json:"workCellResourceId"`
	PickingRequired    bool             `json:"pickingRequired"`
	State              domain.State     `json:"state"`
	StartedAt          *time.Time       `json:"startedAt"`
	CompletedAt        *time.Time       `json:"completedAt"`
	ReportedQty        *decimal.Decimal `json:"reportedQty"`
	ScrapQty           *decimal.Decimal `json:"scrapQty"`
	HandlingUnits      *int             `json:"handlingUnits"`
	Version            int              `json:"version"`
	UpdatedAt          time.Time        `json:"updatedAt"`
}

// label names a job in messages: its card number and activity sequence.
func (j Job) label() string { return fmt.Sprintf("kanban job %s/%d", j.CardNumber, j.Sequence) }

const jobFrom = `select j.id, j.kanban_id, k.card_number, k.rule_id, r.code, r.item_id, r.rule_type, k.state, k.qty, k.due,
		j.activity_id, a.name, a.kind, j.sequence, a.work_cell_resource_id, a.picking_required,
		j.state, j.started_at, j.completed_at, j.reported_qty, j.scrap_qty, j.handling_units, j.version, j.updated_at
	from kanban_job j
	join kanban k on k.id = j.kanban_id
	join kanban_rule r on r.id = k.rule_id
	join flow_activity a on a.id = j.activity_id`

func scanJob(row pgx.Row) (Job, error) {
	var j Job
	err := row.Scan(&j.ID, &j.KanbanID, &j.CardNumber, &j.RuleID, &j.RuleCode, &j.ItemID, &j.RuleType, &j.CardState, &j.CardQty, &j.Due,
		&j.ActivityID, &j.ActivityName, &j.ActivityKind, &j.Sequence, &j.WorkCellResourceID, &j.PickingRequired,
		&j.State, &j.StartedAt, &j.CompletedAt, &j.ReportedQty, &j.ScrapQty, &j.HandlingUnits, &j.Version, &j.UpdatedAt)
	return j, err
}

// queryJobs runs jobFrom with a where/order tail; the result is never nil.
func queryJobs(ctx context.Context, tx pgx.Tx, tail string, args ...any) ([]Job, error) {
	rows, err := tx.Query(ctx, jobFrom+tail, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

func getJob(ctx context.Context, tx pgx.Tx, id, lock string) (Job, error) {
	j, err := scanJob(tx.QueryRow(ctx, jobFrom+` where j.id = $1`+lock, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return j, notFound(CodeJobNotFound, "kanban job", id)
	}
	return j, err
}

// flowActivity is what a new card needs from its rule's flow: one job per
// activity, in sequence order.
type flowActivity struct {
	ID       string
	Sequence int
}

// flowActivityIDs returns the activities of a rule's flow; none when the rule
// has no flow (its cards are then moved by hand through every state).
func flowActivityIDs(ctx context.Context, tx pgx.Tx, flowID *string) ([]flowActivity, error) {
	if flowID == nil {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `select id, sequence from flow_activity where flow_id = $1 order by sequence`, *flowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []flowActivity
	for rows.Next() {
		var a flowActivity
		if err := rows.Scan(&a.ID, &a.Sequence); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// insertJobs creates a new card's jobs in the card's state.
func insertJobs(ctx context.Context, tx pgx.Tx, k Kanban, acts []flowActivity) error {
	tenantID := principal(ctx).TenantID
	for _, a := range acts {
		if _, err := tx.Exec(ctx, `insert into kanban_job (tenant_id, id, kanban_id, activity_id, sequence, state) values ($1, $2, $3, $4, $5, $6)`,
			tenantID, ids.New(), k.ID, a.ID, a.Sequence, string(k.State)); err != nil {
			return err
		}
	}
	return nil
}

// followCard puts every job of a card in the card's new state (Not planned,
// Planned or Prepared) and clears what the jobs reported, so a reused fixed
// card starts its jobs afresh. The card's state-changed event reports the move.
func followCard(ctx context.Context, tx pgx.Tx, kanbanID string, to domain.State) error {
	_, err := tx.Exec(ctx, `update kanban_job set state = $2, started_at = null, completed_at = null, reported_qty = null,
			scrap_qty = null, handling_units = null, version = version + 1, updated_at = now()
		where kanban_id = $1`, kanbanID, string(to))
	return err
}

// lockJob locks the job's card and then the job: the same order as MoveKanban
// (card, then its jobs), so a card move and a job command cannot deadlock, and
// two jobs of one card complete one after the other, never both missing the
// card roll-up.
func lockJob(ctx context.Context, tx pgx.Tx, id string) (Job, Kanban, error) {
	var kanbanID string
	if err := tx.QueryRow(ctx, `select kanban_id from kanban_job where id = $1`, id).Scan(&kanbanID); errors.Is(err, pgx.ErrNoRows) {
		return Job{}, Kanban{}, notFound(CodeJobNotFound, "kanban job", id)
	} else if err != nil {
		return Job{}, Kanban{}, err
	}
	k, err := getKanban(ctx, tx, kanbanID, " for update of k")
	if err != nil {
		return Job{}, Kanban{}, err
	}
	j, err := getJob(ctx, tx, id, " for update of j")
	return j, k, err
}

// stepCard moves the card one step for job j and writes its state-changed
// event with the job id; done is the job's report when the step completes the
// card (the card's handling units come from it).
func stepCard(ctx context.Context, tx pgx.Tx, k *Kanban, to domain.State, j Job, done *domain.Completion) error {
	from := k.State
	if err := domain.CheckRuleMove(domain.Replenishment(k.Replenishment), from, to); err != nil {
		return err
	}
	var units *int
	if done != nil {
		units = &done.HandlingUnits
	}
	if err := tx.QueryRow(ctx, `update kanban set state = $2, handling_units = coalesce($3, handling_units), version = version + 1, updated_at = now()
		where id = $1 returning handling_units, version, updated_at`, k.ID, string(to), units).Scan(&k.HandlingUnits, &k.Version, &k.UpdatedAt); err != nil {
		return err
	}
	k.State = to
	data := leanevents.KanbanStateChangedData{KanbanID: k.ID, RuleID: k.RuleID, CardNumber: k.CardNumber, ItemID: k.ItemID, RuleType: k.RuleType,
		From: string(from), To: string(to), Qty: k.Qty.String(), Version: k.Version, JobID: &j.ID}
	if done != nil {
		reported, scrap := done.ReportedQty.String(), done.ScrapQty.String()
		data.ReportedQty, data.ScrapQty, data.HandlingUnits = &reported, &scrap, units
	}
	return enqueue(ctx, tx, "kanban", k.ID, leanevents.KanbanStateChanged, data)
}

func enqueueJob(ctx context.Context, tx pgx.Tx, j Job, from domain.State) error {
	data := leanevents.JobStateChangedData{JobID: j.ID, KanbanID: j.KanbanID, CardNumber: j.CardNumber, ItemID: j.ItemID, ActivityID: j.ActivityID,
		ActivityKind: j.ActivityKind, Sequence: j.Sequence, WorkCellResourceID: j.WorkCellResourceID, From: string(from), To: string(j.State),
		HandlingUnits: j.HandlingUnits, Version: j.Version}
	if j.ReportedQty != nil {
		s := j.ReportedQty.String()
		data.ReportedQty = &s
	}
	if j.ScrapQty != nil {
		s := j.ScrapQty.String()
		data.ScrapQty = &s
	}
	return enqueue(ctx, tx, "kanbanjob", j.ID, leanevents.JobStateChanged, data)
}

// StartJob starts a job on the work-cell board (FR-24.10). The first started
// job moves its card to In progress (BR-24.6), through Prepared when the card
// is still Planned and the activity needs no picking; the card's other jobs
// follow it to Prepared. version is the job version the caller read.
func (s *Postgres) StartJob(ctx context.Context, id string, version int, idem Idem) (Job, bool, error) {
	var out Job
	changed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		j, k, err := lockJob(ctx, tx, id)
		if err != nil {
			return err
		}
		if found, err := replay(ctx, tx, idem, &out); found || err != nil {
			return err
		}
		if j.Version != version {
			return staleVersion(j.label(), j.Version, version)
		}
		steps, err := domain.StartJob(j.State, k.State, j.PickingRequired)
		if err != nil {
			return err
		}
		for _, to := range steps {
			if err := stepCard(ctx, tx, &k, to, j, nil); err != nil {
				return err
			}
			if to == domain.Prepared {
				if _, err := tx.Exec(ctx, `update kanban_job set state = 'prepared', version = version + 1, updated_at = now()
					where kanban_id = $1 and id <> $2 and state in ('not-planned', 'planned')`, k.ID, id); err != nil {
					return err
				}
			}
		}
		from := j.State
		if _, err := tx.Exec(ctx, `update kanban_job set state = 'in-progress', started_at = now(), version = version + 1, updated_at = now() where id = $1`, id); err != nil {
			return err
		}
		if j, err = getJob(ctx, tx, id, ""); err != nil {
			return err
		}
		if err := enqueueJob(ctx, tx, j, from); err != nil {
			return err
		}
		out, changed = j, true
		return remember(ctx, tx, idem, http.StatusOK, j)
	})
	return out, changed, err
}

// CompleteJob completes an in-progress job with what the operator reports
// (FR-24.10). The last job of a card to complete moves the card to Completed
// (BR-24.6) with the job's handling units; that card event is the trigger for
// the report as finished or the transfer. A job that is not In progress is
// refused with LEAN-0001 (AC-24.7).
func (s *Postgres) CompleteJob(ctx context.Context, id string, version int, c domain.Completion, idem Idem) (Job, bool, error) {
	var out Job
	changed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		j, k, err := lockJob(ctx, tx, id)
		if err != nil {
			return err
		}
		if found, err := replay(ctx, tx, idem, &out); found || err != nil {
			return err
		}
		if j.Version != version {
			return staleVersion(j.label(), j.Version, version)
		}
		var others []domain.State
		rows, err := tx.Query(ctx, `select state from kanban_job where kanban_id = $1 and id <> $2`, k.ID, id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var st domain.State
			if err := rows.Scan(&st); err != nil {
				rows.Close()
				return err
			}
			others = append(others, st)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		steps, err := domain.CompleteJob(j.State, k.State, others, c)
		if err != nil {
			return err
		}
		from := j.State
		if _, err := tx.Exec(ctx, `update kanban_job set state = 'completed', completed_at = now(), reported_qty = $2, scrap_qty = $3,
				handling_units = $4, version = version + 1, updated_at = now()
			where id = $1`, id, c.ReportedQty, c.ScrapQty, c.HandlingUnits); err != nil {
			return err
		}
		if j, err = getJob(ctx, tx, id, ""); err != nil {
			return err
		}
		if err := enqueueJob(ctx, tx, j, from); err != nil {
			return err
		}
		for _, to := range steps {
			if err := stepCard(ctx, tx, &k, to, j, &c); err != nil {
				return err
			}
		}
		if len(steps) > 0 {
			j.CardState = k.State
		}
		out, changed = j, true
		return remember(ctx, tx, idem, http.StatusOK, j)
	})
	return out, changed, err
}

// BoardJobs returns the work-cell board (FR-24.10): the jobs at the activities
// of one work cell whose card is not emptied, oldest due card first, at most
// MaxBoard jobs. The site, flow and rule filters narrow it further.
func (s *Postgres) BoardJobs(ctx context.Context, f BoardFilter) ([]Job, error) {
	var out []Job
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = queryJobs(ctx, tx, ` where a.work_cell_resource_id = $1::uuid and k.state <> 'emptied'
			  and ($2 = '' or r.site_id::text = $2) and ($3 = '' or r.flow_id::text = $3) and ($4 = '' or r.id::text = $4)
			order by k.due nulls last, k.card_number, j.sequence limit $5`, f.WorkCellID, f.SiteID, f.FlowID, f.RuleID, MaxBoard)
		return err
	})
	return out, err
}
