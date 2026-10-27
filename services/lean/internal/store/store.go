// Package store persists production flows, kanban rules and kanban cards in
// Postgres. Every query runs inside dbx.WithTenantTx, so row-level security
// applies, and every write commits its outbox events in that same transaction.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

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
	"github.com/BenjaminSRussell/passage/services/lean/internal/domain"
	leanevents "github.com/BenjaminSRussell/passage/services/lean/internal/events"
)

// Stable problem codes raised by the store (README "Error codes").
const (
	CodeFlowNotFound        = "LEAN-0101"
	CodeRuleNotFound        = "LEAN-0102"
	CodeKanbanNotFound      = "LEAN-0103"
	CodeVersionConflict     = "LEAN-0104"
	CodeDuplicate           = "LEAN-0105"
	CodeFlowNotDraft        = "LEAN-0106"
	CodeRuleNotGenerable    = "LEAN-0107"
	CodeIdempotencyReuse    = "idempotency_key_reuse"
	CodeIdempotencyInFlight = "idempotency_key_in_flight"

	uniqueViolation = "23505"
	// MaxList bounds every list query.
	MaxList = 200
	// MaxBoard bounds the cards one board view returns.
	MaxBoard = 500
	// DateLayout is the wire format of calendar dates.
	DateLayout = "2006-01-02"
)

// Idem carries an Idempotency-Key and a fingerprint of the request. A replay
// with a different fingerprint is refused (422).
type Idem struct{ Key, Fingerprint string }

// Activity is a FlowActivity (FR-24.8). Predecessors are the sequence numbers
// of other activities of the same flow.
type Activity struct {
	ID                 string  `json:"id"`
	Sequence           int     `json:"sequence"`
	Name               string  `json:"name"`
	Kind               string  `json:"kind"`
	CycleTimeS         int     `json:"cycleTimeS"`
	WorkCellResourceID *string `json:"workCellResourceId"`
	InputLocationID    *string `json:"inputLocationId"`
	OutputLocationID   *string `json:"outputLocationId"`
	Predecessors       []int   `json:"predecessors"`
	PickingRequired    bool    `json:"pickingRequired"`
}

// Flow is a ProductionFlow (FR-24.8). List results carry ActivityCount only.
type Flow struct {
	ID            string     `json:"id"`
	ValueStreamID string     `json:"valueStreamId"`
	SiteID        string     `json:"siteId"`
	Name          string     `json:"name"`
	TaktTimeS     *int       `json:"taktTimeS"`
	Status        string     `json:"status"`
	ActiveFrom    *string    `json:"activeFrom"`
	ActiveTo      *string    `json:"activeTo"`
	Version       int        `json:"version"`
	ActivityCount int        `json:"activityCount"`
	Activities    []Activity `json:"activities,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}

// Rule is a KanbanRule (FR-24.9). ActiveKanbans counts the cards that are not
// Emptied (BR-24.7).
type Rule struct {
	ID              string          `json:"id"`
	Code            string          `json:"code"`
	ItemID          string          `json:"itemId"`
	SiteID          string          `json:"siteId"`
	FlowID          *string         `json:"flowId"`
	RuleType        string          `json:"ruleType"`
	Replenishment   string          `json:"replenishment"`
	FixedQty        decimal.Decimal `json:"fixedQty"`
	NumberOfKanbans *int            `json:"numberOfKanbans"`
	LeadTimeH       int             `json:"leadTimeH"`
	EventSource     *string         `json:"eventSource"`
	ExactQty        bool            `json:"exactQty"`
	EffectiveFrom   string          `json:"effectiveFrom"`
	EffectiveTo     *string         `json:"effectiveTo"`
	Version         int             `json:"version"`
	ActiveKanbans   int             `json:"activeKanbans"`
	CreatedAt       time.Time       `json:"createdAt"`
}

// Kanban is a kanban card (FR-24.10) with the rule fields a board shows.
type Kanban struct {
	ID            string          `json:"id"`
	RuleID        string          `json:"ruleId"`
	RuleCode      string          `json:"ruleCode"`
	ItemID        string          `json:"itemId"`
	RuleType      string          `json:"ruleType"`
	Replenishment string          `json:"replenishment"`
	CardNumber    string          `json:"cardNumber"`
	Qty           decimal.Decimal `json:"qty"`
	State         domain.State    `json:"state"`
	Due           *time.Time      `json:"due"`
	EventRefType  *string         `json:"eventRefType"`
	EventRefID    *string         `json:"eventRefId"`
	Version       int             `json:"version"`
	CreatedAt     time.Time       `json:"createdAt"`
	UpdatedAt     time.Time       `json:"updatedAt"`
}

// Generate is the kanbans:generate command. A fixed rule creates Count cards
// (default: up to its number of kanbans); an event rule creates the cards for
// DemandQty (BR-24.8), referencing the triggering line.
type Generate struct {
	RuleID       string
	Count        int
	DemandQty    decimal.Decimal
	EventRefType *string
	EventRefID   *string
	Due          *time.Time
}

// BoardFilter narrows the board; empty fields do not filter.
type BoardFilter struct {
	SiteID string
	FlowID string
	RuleID string
}

// Store is what the API layer needs.
type Store interface {
	CreateFlow(ctx context.Context, in Flow, idem Idem) (Flow, bool, error)
	ListFlows(ctx context.Context, status string, limit int) ([]Flow, error)
	GetFlow(ctx context.Context, id string) (Flow, error)
	ActivateFlow(ctx context.Context, id string, version int, from time.Time, to *time.Time, idem Idem) (Flow, bool, error)
	CreateRule(ctx context.Context, in Rule, idem Idem) (Rule, bool, error)
	ListRules(ctx context.Context, flowID string, limit int) ([]Rule, error)
	GetRule(ctx context.Context, id string) (Rule, error)
	GenerateKanbans(ctx context.Context, in Generate, idem Idem) ([]Kanban, bool, error)
	GetKanban(ctx context.Context, id string) (Kanban, error)
	MoveKanban(ctx context.Context, id string, to domain.State, version int, idem Idem) (Kanban, bool, error)
	Board(ctx context.Context, f BoardFilter) ([]Kanban, error)
	Ping(ctx context.Context) error
}

// Postgres implements Store on a pgx pool.
type Postgres struct{ pool *pgxpool.Pool }

// New returns a Postgres store.
func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

// Ping checks database reachability for readiness.
func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func principal(ctx context.Context) tenant.Principal {
	p, _ := tenant.FromContext(ctx)
	return p
}

func reuseProblem() error {
	return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
}

func notFound(code, what, id string) error {
	return httpx.NewProblem(http.StatusNotFound, code, fmt.Sprintf("%s %s not found", what, id))
}

func staleVersion(what string, have, want int) error {
	return httpx.NewProblem(http.StatusConflict, CodeVersionConflict,
		fmt.Sprintf("%s is at version %d, not %d; reload and try again", what, have, want))
}

// mapDuplicate turns a unique violation into a 409. A clash on the
// idempotency key means a concurrent call with the same key committed first;
// the client retries and gets that call's answer.
func mapDuplicate(err error, detail string) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == uniqueViolation {
		if strings.Contains(pg.ConstraintName, "idempotency_key") {
			return httpx.NewProblem(http.StatusConflict, CodeIdempotencyInFlight, "a request with this Idempotency-Key was processed concurrently; retry to get its result")
		}
		return httpx.NewProblem(http.StatusConflict, CodeDuplicate, detail)
	}
	return err
}

func enqueue(ctx context.Context, tx pgx.Tx, aggregateType, aggregateID, eventType string, data any) error {
	evt, err := events.New(eventType, leanevents.Source, aggregateID, principal(ctx).TenantID, data)
	if err != nil {
		return err
	}
	evt.Actor = principal(ctx).UserID
	return outbox.Enqueue(ctx, tx, aggregateType, aggregateID, evt)
}

// replay returns the stored response of a command key, if any.
func replay(ctx context.Context, tx pgx.Tx, idem Idem, out any) (bool, error) {
	if idem.Key == "" {
		return false, nil
	}
	var hash string
	var body []byte
	err := tx.QueryRow(ctx, `select request_hash, response_body from idempotency_keys where key = $1`, idem.Key).Scan(&hash, &body)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if hash != idem.Fingerprint {
		return false, reuseProblem()
	}
	return true, json.Unmarshal(body, out)
}

func remember(ctx context.Context, tx pgx.Tx, idem Idem, status int, body any) error {
	if idem.Key == "" {
		return nil
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `insert into idempotency_keys (tenant_id, key, request_hash, response_status, response_body) values ($1, $2, $3, $4, $5)`,
		principal(ctx).TenantID, idem.Key, idem.Fingerprint, status, raw)
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == uniqueViolation {
		// The key was stored meanwhile by a command on another resource.
		return reuseProblem()
	}
	return err
}

// rowReplay looks up a create command by the idempotency key stored on the
// created row itself. It returns the row id when the key was used before.
func rowReplay(ctx context.Context, tx pgx.Tx, table string, idem Idem) (string, error) {
	if idem.Key == "" {
		return "", nil
	}
	var id, fp string
	err := tx.QueryRow(ctx, `select id, coalesce(idempotency_fingerprint, '') from `+table+` where idempotency_key = $1`, idem.Key).Scan(&id, &fp)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if fp != idem.Fingerprint {
		return "", reuseProblem()
	}
	return id, nil
}

func dateString(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.Format(DateLayout)
	return &s
}

func toDomainActivities(acts []Activity) []domain.Activity {
	out := make([]domain.Activity, len(acts))
	for i, a := range acts {
		preds := make([]string, len(a.Predecessors))
		for j, p := range a.Predecessors {
			preds[j] = fmt.Sprint(p)
		}
		output := ""
		if a.OutputLocationID != nil {
			output = *a.OutputLocationID
		}
		out[i] = domain.Activity{ID: fmt.Sprint(a.Sequence), Name: a.Name, Kind: a.Kind, OutputLocationID: output, PredecessorIDs: preds}
	}
	return out
}

// CreateFlow stores a draft flow with its activities. Activities may still be
// incomplete (a process step without an output location); activation checks
// connectivity (FR-24.8).
func (s *Postgres) CreateFlow(ctx context.Context, in Flow, idem Idem) (Flow, bool, error) {
	var out Flow
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if id, err := rowReplay(ctx, tx, "production_flow", idem); err != nil || id != "" {
			if err == nil {
				out, err = getFlow(ctx, tx, id, "")
			}
			return err
		}
		p := principal(ctx)
		in.ID, in.Status, in.Version, in.ActivityCount = ids.New(), "draft", 1, len(in.Activities)
		in.ActiveFrom, in.ActiveTo = nil, nil
		if err := tx.QueryRow(ctx, `insert into production_flow (tenant_id, id, value_stream_id, site_id, name, takt_time_s, idempotency_key, idempotency_fingerprint)
			values ($1, $2, $3, $4, $5, $6, nullif($7, ''), nullif($8, '')) returning created_at, updated_at`,
			p.TenantID, in.ID, in.ValueStreamID, in.SiteID, in.Name, in.TaktTimeS, idem.Key, idem.Fingerprint).Scan(&in.CreatedAt, &in.UpdatedAt); err != nil {
			return mapDuplicate(err, "the value stream already has a flow named "+in.Name)
		}
		bySeq := make(map[int]string, len(in.Activities))
		for i := range in.Activities {
			in.Activities[i].ID = ids.New()
			bySeq[in.Activities[i].Sequence] = in.Activities[i].ID
		}
		for _, a := range in.Activities {
			preds := make([]string, len(a.Predecessors))
			for j, seq := range a.Predecessors {
				preds[j] = bySeq[seq]
			}
			if _, err := tx.Exec(ctx, `insert into flow_activity (tenant_id, id, flow_id, sequence, name, kind, cycle_time_s, work_cell_resource_id,
					input_location_id, output_location_id, predecessor_ids, picking_required)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
				p.TenantID, a.ID, in.ID, a.Sequence, a.Name, a.Kind, a.CycleTimeS, a.WorkCellResourceID, a.InputLocationID, a.OutputLocationID,
				preds, a.PickingRequired); err != nil {
				return mapDuplicate(err, "activity sequence numbers and names must be unique within a flow")
			}
		}
		out, created = in, true
		return nil
	})
	return out, created, err
}

const flowCols = `id, value_stream_id, site_id, name, takt_time_s, status, active_from, active_to, version, created_at, updated_at,
	(select count(*) from flow_activity a where a.flow_id = production_flow.id)`

func scanFlow(row pgx.Row) (Flow, error) {
	var f Flow
	var from, to *time.Time
	err := row.Scan(&f.ID, &f.ValueStreamID, &f.SiteID, &f.Name, &f.TaktTimeS, &f.Status, &from, &to, &f.Version, &f.CreatedAt, &f.UpdatedAt, &f.ActivityCount)
	f.ActiveFrom, f.ActiveTo = dateString(from), dateString(to)
	return f, err
}

// ListFlows returns flows newest first, without their activities.
func (s *Postgres) ListFlows(ctx context.Context, status string, limit int) ([]Flow, error) {
	out := []Flow{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+flowCols+` from production_flow where ($1 = '' or status = $1)
			order by created_at desc, id desc limit $2`, status, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			f, err := scanFlow(rows)
			if err != nil {
				return err
			}
			out = append(out, f)
		}
		return rows.Err()
	})
	return out, err
}

// GetFlow returns one flow with its activities in sequence order.
func (s *Postgres) GetFlow(ctx context.Context, id string) (Flow, error) {
	var out Flow
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = getFlow(ctx, tx, id, "")
		return err
	})
	return out, err
}

// getFlow reads a flow and its activities; lock is "" or " for update".
func getFlow(ctx context.Context, tx pgx.Tx, id, lock string) (Flow, error) {
	f, err := scanFlow(tx.QueryRow(ctx, `select `+flowCols+` from production_flow where id = $1`+lock, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return f, notFound(CodeFlowNotFound, "production flow", id)
	}
	if err != nil {
		return f, err
	}
	rows, err := tx.Query(ctx, `select id, sequence, name, kind, cycle_time_s, work_cell_resource_id, input_location_id, output_location_id,
			predecessor_ids, picking_required
		from flow_activity where flow_id = $1 order by sequence`, id)
	if err != nil {
		return f, err
	}
	defer rows.Close()
	f.Activities = []Activity{}
	predIDs := [][]string{}
	seqOf := map[string]int{}
	for rows.Next() {
		var a Activity
		var preds []string
		if err := rows.Scan(&a.ID, &a.Sequence, &a.Name, &a.Kind, &a.CycleTimeS, &a.WorkCellResourceID, &a.InputLocationID, &a.OutputLocationID,
			&preds, &a.PickingRequired); err != nil {
			return f, err
		}
		seqOf[a.ID] = a.Sequence
		f.Activities = append(f.Activities, a)
		predIDs = append(predIDs, preds)
	}
	if err := rows.Err(); err != nil {
		return f, err
	}
	for i, preds := range predIDs {
		f.Activities[i].Predecessors = make([]int, len(preds))
		for j, p := range preds {
			f.Activities[i].Predecessors[j] = seqOf[p]
		}
	}
	return f, nil
}

// ActivateFlow validates connectivity (FR-24.8) and activates a draft flow
// from the given date. version is the version the caller read.
func (s *Postgres) ActivateFlow(ctx context.Context, id string, version int, from time.Time, to *time.Time, idem Idem) (Flow, bool, error) {
	var out Flow
	changed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		f, err := getFlow(ctx, tx, id, " for update")
		if err != nil {
			return err
		}
		if found, err := replay(ctx, tx, idem, &out); found || err != nil {
			return err
		}
		if f.Version != version {
			return staleVersion("production flow "+f.Name, f.Version, version)
		}
		if f.Status != "draft" {
			return httpx.NewProblem(http.StatusConflict, CodeFlowNotDraft, fmt.Sprintf("production flow %s is %s; only a draft can be activated", f.Name, f.Status))
		}
		if err := domain.ValidateFlow(toDomainActivities(f.Activities)); err != nil {
			return err
		}
		var activeFrom time.Time
		var activeTo *time.Time
		if err := tx.QueryRow(ctx, `update production_flow set status = 'active', active_from = $2, active_to = $3, version = version + 1, updated_at = now()
			where id = $1 returning active_from, active_to, version, updated_at`, id, from, to).Scan(&activeFrom, &activeTo, &f.Version, &f.UpdatedAt); err != nil {
			return err
		}
		f.Status, f.ActiveFrom, f.ActiveTo = "active", dateString(&activeFrom), dateString(activeTo)
		if err := enqueue(ctx, tx, "flow", f.ID, leanevents.FlowActivated, leanevents.FlowActivatedData{
			FlowID: f.ID, ValueStreamID: f.ValueStreamID, SiteID: f.SiteID, Name: f.Name, ActiveFrom: *f.ActiveFrom, Activities: len(f.Activities)}); err != nil {
			return err
		}
		out, changed = f, true
		return remember(ctx, tx, idem, http.StatusOK, f)
	})
	return out, changed, err
}

// CreateRule validates (FR-24.9) and stores a kanban rule.
func (s *Postgres) CreateRule(ctx context.Context, in Rule, idem Idem) (Rule, bool, error) {
	n := 0
	if in.NumberOfKanbans != nil {
		n = *in.NumberOfKanbans
	}
	source := ""
	if in.EventSource != nil {
		source = *in.EventSource
	}
	if err := domain.ValidateRule(domain.Rule{Type: domain.RuleType(in.RuleType), Replenishment: domain.Replenishment(in.Replenishment),
		FixedQty: in.FixedQty, NumberOfKanbans: n, EventSource: source}); err != nil {
		return Rule{}, false, err
	}
	var out Rule
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if id, err := rowReplay(ctx, tx, "kanban_rule", idem); err != nil || id != "" {
			if err == nil {
				out, err = getRule(ctx, tx, id, "")
			}
			return err
		}
		if in.FlowID != nil {
			var one int
			if err := tx.QueryRow(ctx, `select 1 from production_flow where id = $1`, *in.FlowID).Scan(&one); errors.Is(err, pgx.ErrNoRows) {
				return notFound(CodeFlowNotFound, "production flow", *in.FlowID)
			} else if err != nil {
				return err
			}
		}
		p := principal(ctx)
		in.ID, in.Version, in.ActiveKanbans = ids.New(), 1, 0
		if err := tx.QueryRow(ctx, `insert into kanban_rule (tenant_id, id, code, item_id, site_id, flow_id, rule_type, replenishment, fixed_qty,
				number_of_kanbans, lead_time_h, event_source, exact_qty, effective_from, effective_to, idempotency_key, idempotency_fingerprint)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14::date, $15::date, nullif($16, ''), nullif($17, '')) returning created_at`,
			p.TenantID, in.ID, in.Code, in.ItemID, in.SiteID, in.FlowID, in.RuleType, in.Replenishment, in.FixedQty, in.NumberOfKanbans,
			in.LeadTimeH, in.EventSource, in.ExactQty, in.EffectiveFrom, in.EffectiveTo, idem.Key, idem.Fingerprint).Scan(&in.CreatedAt); err != nil {
			return mapDuplicate(err, "a kanban rule with code "+in.Code+" already exists")
		}
		out, created = in, true
		return nil
	})
	return out, created, err
}

const ruleCols = `r.id, r.code, r.item_id, r.site_id, r.flow_id, r.rule_type, r.replenishment, r.fixed_qty, r.number_of_kanbans, r.lead_time_h,
	r.event_source, r.exact_qty, r.effective_from, r.effective_to, r.version, r.created_at,
	(select count(*) from kanban k where k.rule_id = r.id and k.state <> 'emptied')`

func scanRule(row pgx.Row) (Rule, error) {
	var r Rule
	var from time.Time
	var to *time.Time
	err := row.Scan(&r.ID, &r.Code, &r.ItemID, &r.SiteID, &r.FlowID, &r.RuleType, &r.Replenishment, &r.FixedQty, &r.NumberOfKanbans, &r.LeadTimeH,
		&r.EventSource, &r.ExactQty, &from, &to, &r.Version, &r.CreatedAt, &r.ActiveKanbans)
	r.EffectiveFrom, r.EffectiveTo = from.Format(DateLayout), dateString(to)
	return r, err
}

// ListRules returns rules ordered by code, optionally for one flow.
func (s *Postgres) ListRules(ctx context.Context, flowID string, limit int) ([]Rule, error) {
	out := []Rule{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+ruleCols+` from kanban_rule r where ($1 = '' or r.flow_id::text = $1) order by r.code limit $2`, flowID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanRule(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// GetRule returns one rule with its active card count.
func (s *Postgres) GetRule(ctx context.Context, id string) (Rule, error) {
	var out Rule
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = getRule(ctx, tx, id, "")
		return err
	})
	return out, err
}

// getRule reads a rule; lock is "" or " for update of r".
func getRule(ctx context.Context, tx pgx.Tx, id, lock string) (Rule, error) {
	r, err := scanRule(tx.QueryRow(ctx, `select `+ruleCols+` from kanban_rule r where r.id = $1`+lock, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return r, notFound(CodeRuleNotFound, "kanban rule", id)
	}
	return r, err
}

// GenerateKanbans creates cards for a fixed or event rule (FR-24.9, FR-24.11).
// The rule row is locked, so card numbers are gap-free and two concurrent
// calls cannot together exceed a fixed rule's number of kanbans (BR-24.7).
func (s *Postgres) GenerateKanbans(ctx context.Context, in Generate, idem Idem) ([]Kanban, bool, error) {
	out := []Kanban{}
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Lock the rule first, then look for a replay: a concurrent retry with
		// the same key waits here and then sees the first call's stored answer.
		r, err := getRule(ctx, tx, in.RuleID, " for update of r")
		if err != nil {
			return err
		}
		if found, err := replay(ctx, tx, idem, &out); found || err != nil {
			return err
		}
		var effective bool
		if err := tx.QueryRow(ctx, `select current_date >= $1::date and ($2::date is null or current_date <= $2::date)`,
			r.EffectiveFrom, r.EffectiveTo).Scan(&effective); err != nil {
			return err
		}
		if !effective {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeRuleNotGenerable, fmt.Sprintf("kanban rule %s is not effective today", r.Code))
		}
		var cards []domain.Kanban
		var due *time.Time
		switch domain.Replenishment(r.Replenishment) {
		case domain.ReplenishFixed:
			count := in.Count
			if count == 0 {
				count = *r.NumberOfKanbans - r.ActiveKanbans
			}
			rule := domain.Rule{Type: domain.RuleType(r.RuleType), Replenishment: domain.ReplenishFixed, FixedQty: r.FixedQty, NumberOfKanbans: *r.NumberOfKanbans}
			if count < 1 {
				return domain.CheckCapacity(r.Code, r.ActiveKanbans, 1, *r.NumberOfKanbans)
			}
			cards, err = domain.FixedCards(r.ID, rule, r.ActiveKanbans, count, ids.New)
		case domain.ReplenishEvent:
			cards, err = domain.GenerateEventKanbans(r.ID, in.DemandQty, r.FixedQty, r.ExactQty, ids.New)
			due = in.Due
			if due == nil {
				d := time.Now().UTC().Add(time.Duration(r.LeadTimeH) * time.Hour).Truncate(time.Second)
				due = &d
			}
		default:
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeRuleNotGenerable,
				fmt.Sprintf("kanban rule %s is scheduled: its kanbans come from master planning, not from generate", r.Code))
		}
		if err != nil {
			return err
		}
		var first int
		if err := tx.QueryRow(ctx, `update kanban_rule set next_card = next_card + $2 where id = $1 returning next_card - $2`, r.ID, len(cards)).Scan(&first); err != nil {
			return err
		}
		p := principal(ctx)
		for i, c := range cards {
			k := Kanban{ID: c.ID, RuleID: r.ID, RuleCode: r.Code, ItemID: r.ItemID, RuleType: r.RuleType, Replenishment: r.Replenishment,
				CardNumber: fmt.Sprintf("%s-%04d", r.Code, first+i), Qty: c.Qty, State: c.State, Due: due, Version: 1}
			if domain.Replenishment(r.Replenishment) == domain.ReplenishEvent {
				k.EventRefType, k.EventRefID = in.EventRefType, in.EventRefID
			}
			if err := tx.QueryRow(ctx, `insert into kanban (tenant_id, id, rule_id, card_number, qty, state, due, event_ref_type, event_ref_id)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9) returning created_at, updated_at`,
				p.TenantID, k.ID, k.RuleID, k.CardNumber, k.Qty, string(k.State), k.Due, k.EventRefType, k.EventRefID).Scan(&k.CreatedAt, &k.UpdatedAt); err != nil {
				return err
			}
			var dueText *string
			if k.Due != nil {
				t := k.Due.UTC().Format(time.RFC3339)
				dueText = &t
			}
			if err := enqueue(ctx, tx, "kanban", k.ID, leanevents.KanbanCreated, leanevents.KanbanCreatedData{
				KanbanID: k.ID, RuleID: k.RuleID, CardNumber: k.CardNumber, ItemID: k.ItemID, RuleType: k.RuleType, Qty: k.Qty.String(),
				State: string(k.State), EventRefType: k.EventRefType, EventRefID: k.EventRefID, Due: dueText}); err != nil {
				return err
			}
			out = append(out, k)
		}
		created = true
		return remember(ctx, tx, idem, http.StatusCreated, out)
	})
	return out, created, err
}

const kanbanCols = `k.id, k.rule_id, r.code, r.item_id, r.rule_type, r.replenishment, k.card_number, k.qty, k.state, k.due,
	k.event_ref_type, k.event_ref_id, k.version, k.created_at, k.updated_at`

func scanKanban(row pgx.Row) (Kanban, error) {
	var k Kanban
	err := row.Scan(&k.ID, &k.RuleID, &k.RuleCode, &k.ItemID, &k.RuleType, &k.Replenishment, &k.CardNumber, &k.Qty, &k.State, &k.Due,
		&k.EventRefType, &k.EventRefID, &k.Version, &k.CreatedAt, &k.UpdatedAt)
	return k, err
}

// GetKanban returns one card.
func (s *Postgres) GetKanban(ctx context.Context, id string) (Kanban, error) {
	var out Kanban
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = getKanban(ctx, tx, id, "")
		return err
	})
	return out, err
}

// getKanban reads a card; lock is "" or " for update of k".
func getKanban(ctx context.Context, tx pgx.Tx, id, lock string) (Kanban, error) {
	k, err := scanKanban(tx.QueryRow(ctx, `select `+kanbanCols+` from kanban k join kanban_rule r on r.id = k.rule_id where k.id = $1`+lock, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return k, notFound(CodeKanbanNotFound, "kanban", id)
	}
	return k, err
}

// MoveKanban moves a card one step through the BR-24.6 state machine. Moving
// to Planned sets the due date from the rule's lead time when none is set;
// moving back to Not planned clears it. Reusing an emptied fixed-rule card
// counts against the rule's number of kanbans again (BR-24.7).
func (s *Postgres) MoveKanban(ctx context.Context, id string, to domain.State, version int, idem Idem) (Kanban, bool, error) {
	var out Kanban
	changed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		k, err := getKanban(ctx, tx, id, " for update of k")
		if err != nil {
			return err
		}
		if found, err := replay(ctx, tx, idem, &out); found || err != nil {
			return err
		}
		if k.Version != version {
			return staleVersion("kanban "+k.CardNumber, k.Version, version)
		}
		from := k.State
		if err := domain.CheckRuleMove(domain.Replenishment(k.Replenishment), from, to); err != nil {
			return err
		}
		if from == domain.Emptied && to == domain.NotPlanned {
			r, err := getRule(ctx, tx, k.RuleID, " for update of r")
			if err != nil {
				return err
			}
			if err := domain.CheckCapacity(r.Code, r.ActiveKanbans, 1, *r.NumberOfKanbans); err != nil {
				return err
			}
		}
		if err := tx.QueryRow(ctx, `update kanban k set state = $2,
				due = case when $2 = 'not-planned' then null
				           when $2 = 'planned' then coalesce(k.due, date_trunc('second', now()) + make_interval(hours => r.lead_time_h))
				           else k.due end,
				version = k.version + 1, updated_at = now()
			from kanban_rule r where r.id = k.rule_id and k.id = $1
			returning k.due, k.version, k.updated_at`, id, string(to)).Scan(&k.Due, &k.Version, &k.UpdatedAt); err != nil {
			return err
		}
		k.State = to
		if err := enqueue(ctx, tx, "kanban", k.ID, leanevents.KanbanStateChanged, leanevents.KanbanStateChangedData{
			KanbanID: k.ID, RuleID: k.RuleID, CardNumber: k.CardNumber, ItemID: k.ItemID, RuleType: k.RuleType,
			From: string(from), To: string(to), Qty: k.Qty.String(), Version: k.Version}); err != nil {
			return err
		}
		out, changed = k, true
		return remember(ctx, tx, idem, http.StatusOK, k)
	})
	return out, changed, err
}

// Board returns the cards on the board (FR-24.10), oldest due first: every
// card that is not Emptied, plus emptied fixed-rule cards waiting for reuse.
// Spent event-rule cards are left out.
func (s *Postgres) Board(ctx context.Context, f BoardFilter) ([]Kanban, error) {
	out := []Kanban{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+kanbanCols+` from kanban k join kanban_rule r on r.id = k.rule_id
			where (k.state <> 'emptied' or r.replenishment = 'fixed')
			  and ($1 = '' or r.site_id::text = $1) and ($2 = '' or r.flow_id::text = $2) and ($3 = '' or r.id::text = $3)
			order by k.due nulls last, k.card_number limit $4`, f.SiteID, f.FlowID, f.RuleID, MaxBoard)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			k, err := scanKanban(rows)
			if err != nil {
				return err
			}
			out = append(out, k)
		}
		return rows.Err()
	})
	return out, err
}
