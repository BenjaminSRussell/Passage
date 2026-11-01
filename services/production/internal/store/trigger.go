package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	prodevents "github.com/BenjaminSRussell/passage/services/production/internal/events"
)

// Stable error codes of trigger point setup (FR-21.17).
const (
	CodeTriggerPointNotFound    = "PRODUCTION_TRIGGER_POINT_NOT_FOUND"
	CodeTriggerPointExists      = "PRODUCTION_TRIGGER_POINT_EXISTS"
	CodeTriggerPointFired       = "PRODUCTION_TRIGGER_POINT_FIRED"
	CodeTriggerOperationMissing = "PRODUCTION_TRIGGER_OPERATION_NOT_FOUND"
	CodeTriggerOrderClosed      = "PRODUCTION_TRIGGER_ORDER_CLOSED"
)

// Firing statuses.
const (
	FiringFired  = "fired"
	FiringFailed = "failed"
)

// NewTriggerPoint defines a trigger point on operation OperNumber of an order. The API
// has checked it with domain.CheckTriggerPoint.
type NewTriggerPoint struct {
	OrderID        string
	OperNumber     int
	Sequence       int
	Condition      string
	Function       domain.TriggerFunction
	Operations     []domain.TriggerOperation
	Repeatable     bool
	Actor          string
	IdempotencyKey string
	RequestHash    string
}

// TriggerPoint is a stored trigger point with its firings, oldest first.
type TriggerPoint struct {
	domain.TriggerPoint
	LastFiredAt *time.Time      `json:"lastFiredAt,omitempty"`
	CreatedBy   string          `json:"createdBy"`
	CreatedAt   time.Time       `json:"createdAt"`
	Firings     []TriggerFiring `json:"firings"`
}

// TriggerFiring is one due trigger point on a route-card post (BR-21.13): fired, with
// what its function did, or failed with the reason (PROD-0019).
type TriggerFiring struct {
	ID                 string          `json:"id"`
	TriggerPointID     string          `json:"triggerPointId"`
	JournalID          string          `json:"journalId"`
	OperNumber         int             `json:"operNumber"`
	Function           string          `json:"function"`
	Status             string          `json:"status"`
	Reason             *string         `json:"reason,omitempty"`
	Feedback           domain.Feedback `json:"feedback"`
	InsertedOperations []int           `json:"insertedOperations"`
	ReworkOrderID      *string         `json:"reworkOrderId,omitempty"`
	Actor              string          `json:"actor"`
	FiredAt            time.Time       `json:"firedAt"`
}

// CreateTriggerPoint stores a trigger point on an open order's operation. The order row
// is locked first, so a definition never races a route-card post. A replay with the same
// Idempotency-Key and body returns the stored trigger point and created=false.
func (s *Postgres) CreateTriggerPoint(ctx context.Context, in NewTriggerPoint) (TriggerPoint, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out TriggerPoint
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var replayID, replayHash string
		err := tx.QueryRow(ctx, `select id::text, request_hash from production_trigger_point where tenant_id = $1 and idempotency_key = $2`,
			p.TenantID, in.IdempotencyKey).Scan(&replayID, &replayHash)
		if err == nil {
			if replayHash != in.RequestHash {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
			}
			out, err = loadTriggerPoint(ctx, tx, p.TenantID, replayID)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		h, err := lockOrder(ctx, tx, p.TenantID, in.OrderID, 0)
		if err != nil {
			return err
		}
		if h.status == domain.Ended || h.status == domain.Cancelled {
			return httpx.NewProblem(http.StatusConflict, CodeTriggerOrderClosed, fmt.Sprintf("a %s order takes no more feedback, so no trigger points", h.status))
		}
		var found bool
		if err := tx.QueryRow(ctx, `select exists(select 1 from production_operation where tenant_id = $1 and order_id = $2 and oper_number = $3)`,
			p.TenantID, h.id, in.OperNumber).Scan(&found); err != nil {
			return err
		}
		if !found {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeTriggerOperationMissing, fmt.Sprintf("operation %d is not on the order's route", in.OperNumber))
		}
		if err := tx.QueryRow(ctx, `select exists(select 1 from production_trigger_point where tenant_id = $1 and order_id = $2 and oper_number = $3 and sequence = $4)`,
			p.TenantID, h.id, in.OperNumber, in.Sequence).Scan(&found); err != nil {
			return err
		}
		if found {
			return httpx.NewProblem(http.StatusConflict, CodeTriggerPointExists, fmt.Sprintf("operation %d already has a trigger point with sequence %d", in.OperNumber, in.Sequence))
		}
		ops := in.Operations
		if ops == nil {
			ops = []domain.TriggerOperation{}
		}
		raw, err := json.Marshal(ops)
		if err != nil {
			return err
		}
		id := ids.New()
		if _, err := tx.Exec(ctx, `insert into production_trigger_point (tenant_id, id, legal_entity_id, order_id, oper_number, sequence, condition,
				function, operations, repeatable, created_by, idempotency_key, request_hash)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10, $11, $12, $13)`,
			p.TenantID, id, h.legalEntityID, h.id, in.OperNumber, in.Sequence, in.Condition, string(in.Function), string(raw),
			in.Repeatable, in.Actor, in.IdempotencyKey, in.RequestHash); err != nil {
			return err
		}
		created = true
		out, err = loadTriggerPoint(ctx, tx, p.TenantID, id)
		return err
	})
	return out, created, err
}

// ListTriggerPoints returns an order's trigger points by operation and sequence, each
// with its firings.
func (s *Postgres) ListTriggerPoints(ctx context.Context, orderID string) ([]TriggerPoint, error) {
	p, _ := tenant.FromContext(ctx)
	var out []TriggerPoint
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `select exists(select 1 from production_order where tenant_id = $1 and id = $2)`, p.TenantID, orderID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return notFound()
		}
		var err error
		out, err = loadTriggerPoints(ctx, tx, p.TenantID, `t.order_id = $2`, orderID)
		return err
	})
	return out, err
}

// DeleteTriggerPoint removes a trigger point that has never fired; one that fired stays
// as the record of what it did.
func (s *Postgres) DeleteTriggerPoint(ctx context.Context, id string) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var orderID string
		err := tx.QueryRow(ctx, `select order_id::text from production_trigger_point where tenant_id = $1 and id = $2`, p.TenantID, id).Scan(&orderID)
		if errors.Is(err, pgx.ErrNoRows) {
			return triggerPointNotFound()
		}
		if err != nil {
			return err
		}
		// The order lock serializes the delete with route-card posts that could fire it.
		if _, err := lockOrder(ctx, tx, p.TenantID, orderID, 0); err != nil {
			return err
		}
		var fired int
		err = tx.QueryRow(ctx, `select fired_count from production_trigger_point where tenant_id = $1 and id = $2`, p.TenantID, id).Scan(&fired)
		if errors.Is(err, pgx.ErrNoRows) {
			return triggerPointNotFound()
		}
		if err != nil {
			return err
		}
		if fired > 0 {
			return httpx.NewProblem(http.StatusConflict, CodeTriggerPointFired, "the trigger point has fired and is kept as the record of what it did")
		}
		_, err = tx.Exec(ctx, `delete from production_trigger_point where tenant_id = $1 and id = $2`, p.TenantID, id)
		return err
	})
}

func triggerPointNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeTriggerPointNotFound, "trigger point not found")
}

func loadTriggerPoint(ctx context.Context, q querier, tenantID, id string) (TriggerPoint, error) {
	tps, err := loadTriggerPoints(ctx, q, tenantID, `t.id = $2`, id)
	if err != nil {
		return TriggerPoint{}, err
	}
	if len(tps) == 0 {
		return TriggerPoint{}, triggerPointNotFound()
	}
	return tps[0], nil
}

// loadTriggerPoints reads the trigger points matching where (over alias t, with $2 as
// its argument) and attaches their firings.
func loadTriggerPoints(ctx context.Context, q querier, tenantID, where, arg string) ([]TriggerPoint, error) {
	rows, err := q.Query(ctx, `select t.id::text, t.order_id::text, t.oper_number, t.sequence, t.condition, t.function, t.operations::text,
			t.repeatable, t.fired_count, t.last_fired_at, t.created_by, t.created_at
		from production_trigger_point t where t.tenant_id = $1 and `+where+` order by t.oper_number, t.sequence`, tenantID, arg)
	if err != nil {
		return nil, err
	}
	out := []TriggerPoint{}
	at := map[string]int{}
	for rows.Next() {
		var tp TriggerPoint
		var fn, raw string
		if err := rows.Scan(&tp.ID, &tp.OrderID, &tp.OperNumber, &tp.Sequence, &tp.Condition, &fn, &raw, &tp.Repeatable,
			&tp.FiredCount, &tp.LastFiredAt, &tp.CreatedBy, &tp.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		tp.Function = domain.TriggerFunction(fn)
		if err := json.Unmarshal([]byte(raw), &tp.Operations); err != nil {
			rows.Close()
			return nil, err
		}
		tp.Firings = []TriggerFiring{}
		at[tp.ID] = len(out)
		out = append(out, tp)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}
	firings, err := loadFirings(ctx, q, tenantID, `f.trigger_point_id in (select t.id from production_trigger_point t where t.tenant_id = $1 and `+where+`)`, arg)
	if err != nil {
		return nil, err
	}
	for _, f := range firings {
		if i, ok := at[f.TriggerPointID]; ok {
			out[i].Firings = append(out[i].Firings, f)
		}
	}
	return out, nil
}

// loadFirings reads the firings matching where (over alias f, $2 its argument), oldest first.
func loadFirings(ctx context.Context, q querier, tenantID, where, arg string) ([]TriggerFiring, error) {
	rows, err := q.Query(ctx, `select f.id::text, f.trigger_point_id::text, f.journal_id::text, f.oper_number, f.function, f.status, f.reason,
			f.hours::text, f.qty_good::text, f.qty_error::text, f.inserted_operations, f.rework_order_id::text, f.actor, f.fired_at
		from production_trigger_firing f where f.tenant_id = $1 and `+where+` order by f.fired_at, f.id`, tenantID, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TriggerFiring{}
	for rows.Next() {
		var f TriggerFiring
		var hours, good, bad string
		var inserted []int32
		if err := rows.Scan(&f.ID, &f.TriggerPointID, &f.JournalID, &f.OperNumber, &f.Function, &f.Status, &f.Reason,
			&hours, &good, &bad, &inserted, &f.ReworkOrderID, &f.Actor, &f.FiredAt); err != nil {
			return nil, err
		}
		f.Feedback = domain.Feedback{Hours: dec(hours), QtyGood: dec(good), QtyError: dec(bad)}
		f.InsertedOperations = make([]int, len(inserted))
		for i, n := range inserted {
			f.InsertedOperations[i] = int(n)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// fireTriggers evaluates the order's trigger points on a posted route card or job card (FR-21.17):
// each hours line is the feedback of its operation, and the operation's due trigger
// points act in ascending sequence (BR-21.13). Each function runs in a savepoint: when it
// fails on a business rule its changes are rolled back, the firing is recorded as failed
// (PROD-0019) and the post goes on. Operations a trigger inserts are not on the journal,
// so nothing is evaluated on them in the same post. The firings are added to j.
func fireTriggers(ctx context.Context, tx pgx.Tx, tenantID string, h orderHead, j *Journal, actor string) error {
	if (j.Type != domain.JournalRouteCard && j.Type != domain.JournalJobCard) || j.ReversalOf != nil {
		return nil
	}
	tps, err := loadTriggerPoints(ctx, tx, tenantID, `t.order_id = $2`, h.id)
	if err != nil || len(tps) == 0 {
		return err
	}
	prm, err := orderParameters(ctx, tx, tenantID, h, domain.Parameters{})
	if err != nil || !prm.TriggerPointsEnabled {
		return err
	}
	defs := make([]domain.TriggerPoint, len(tps))
	for i, tp := range tps {
		defs[i] = tp.TriggerPoint
	}
	for _, l := range j.Lines {
		if l.Kind != KindHours || l.OperNumber == nil {
			continue
		}
		fb := domain.Feedback{Hours: l.Qty, QtyGood: l.QtyGood, QtyError: l.QtyError}
		due, err := domain.DueTriggers(defs, *l.OperNumber, fb)
		if err != nil {
			return err
		}
		for _, tp := range due {
			f, err := fireOne(ctx, tx, tenantID, h, j.ID, tp, fb, actor)
			if err != nil {
				return err
			}
			if f.Status == FiringFired {
				for i := range defs {
					if defs[i].ID == tp.ID {
						defs[i].FiredCount++
					}
				}
			}
			j.TriggerFirings = append(j.TriggerFirings, f)
		}
	}
	return nil
}

// fireOne performs one due trigger point in a savepoint, records the firing and
// enqueues triggerpoint.fired.
func fireOne(ctx context.Context, tx pgx.Tx, tenantID string, h orderHead, journalID string, tp domain.TriggerPoint, fb domain.Feedback, actor string) (TriggerFiring, error) {
	f := TriggerFiring{ID: ids.New(), TriggerPointID: tp.ID, JournalID: journalID, OperNumber: tp.OperNumber, Function: string(tp.Function),
		Status: FiringFired, Feedback: fb, InsertedOperations: []int{}, Actor: actor}
	sp, err := tx.Begin(ctx)
	if err != nil {
		return f, err
	}
	ferr := perform(ctx, sp, tenantID, h, journalID, tp, fb, actor, &f)
	if ferr != nil {
		_ = sp.Rollback(ctx)
		reason, ok := ruleFailure(ferr)
		if !ok {
			return f, ferr
		}
		f.Status, f.Reason, f.InsertedOperations, f.ReworkOrderID = FiringFailed, &reason, []int{}, nil
	} else if err := sp.Commit(ctx); err != nil {
		return f, err
	}
	if err := tx.QueryRow(ctx, `insert into production_trigger_firing (tenant_id, id, legal_entity_id, order_id, trigger_point_id, journal_id,
			oper_number, function, status, reason, hours, qty_good, qty_error, inserted_operations, rework_order_id, actor)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16) returning fired_at`,
		tenantID, f.ID, h.legalEntityID, h.id, tp.ID, journalID, tp.OperNumber, f.Function, f.Status, f.Reason,
		fb.Hours.String(), fb.QtyGood.String(), fb.QtyError.String(), f.InsertedOperations, f.ReworkOrderID, actor).Scan(&f.FiredAt); err != nil {
		return f, err
	}
	if f.Status == FiringFired {
		if _, err := tx.Exec(ctx, `update production_trigger_point set fired_count = fired_count + 1, last_fired_at = $3 where tenant_id = $1 and id = $2`,
			tenantID, tp.ID, f.FiredAt); err != nil {
			return f, err
		}
	}
	return f, enqueue(ctx, tx, h.id, prodevents.TriggerPointFired, tenantID, actor, prodevents.TriggerPointFiredData{
		FiringID: f.ID, TriggerPointID: tp.ID, OrderID: h.id, JournalID: journalID, OperNumber: tp.OperNumber, Sequence: tp.Sequence,
		Condition: tp.Condition, Function: f.Function, Status: f.Status, Reason: f.Reason,
		Feedback:           prodevents.TriggerFeedback{Hours: fb.Hours.String(), QtyGood: fb.QtyGood.String(), QtyError: fb.QtyError.String()},
		InsertedOperations: f.InsertedOperations, ReworkOrderID: f.ReworkOrderID, Actor: actor,
	})
}

// perform runs the trigger's function inside the savepoint sp and fills in what it did.
func perform(ctx context.Context, sp pgx.Tx, tenantID string, h orderHead, journalID string, tp domain.TriggerPoint, fb domain.Feedback, actor string, f *TriggerFiring) error {
	switch tp.Function {
	case domain.FnInsertOperations:
		ops, err := loadOperations(ctx, sp, tenantID, h.id)
		if err != nil {
			return err
		}
		inserted, err := domain.InsertAfter(ops, tp.OperNumber, tp.Operations, fb)
		if err != nil {
			return err
		}
		if err := insertOperations(ctx, sp, tenantID, h.id, inserted); err != nil {
			return err
		}
		// AC-21.13: the inserted operations are scheduled forward with the rest of a dated route.
		if err := redateOrder(ctx, sp, tenantID, h.id, actor); err != nil {
			return err
		}
		for _, op := range inserted {
			f.InsertedOperations = append(f.InsertedOperations, op.OperNumber)
		}
		return nil
	case domain.FnCreateReworkOrder:
		route, err := domain.ReworkRoute(tp.Operations, fb)
		if err != nil {
			return err
		}
		var warehouse string
		if err := sp.QueryRow(ctx, `select warehouse_id::text from production_order where tenant_id = $1 and id = $2`, tenantID, h.id).Scan(&warehouse); err != nil {
			return err
		}
		origin := h.id
		key := "sys:trigger:" + tp.ID + ":" + journalID
		id, err := insertOrder(ctx, sp, tenantID, actor, NewOrder{LegalEntityID: h.legalEntityID, ProductID: h.productID, SiteID: h.siteID,
			WarehouseID: warehouse, ItemGroup: h.itemGroup, Currency: h.currency, Quantity: fb.QtyError, OriginType: domain.OriginRework,
			OriginID: &origin, ReworkReference: &domain.ReworkRef{Type: domain.ReworkRefOrder, ID: h.id}, Lines: []domain.OrderLine{},
			Operations: route, IdempotencyKey: key, RequestHash: key})
		if err != nil {
			return err
		}
		f.ReworkOrderID = &id
		return nil
	case domain.FnNotify:
		// The triggerpoint.fired event is the notification.
		return nil
	}
	return &domain.Error{Code: domain.CodeTriggerFunctionFailed, Detail: fmt.Sprintf("trigger function %s is not performed by this service", tp.Function)}
}

// ruleFailure reports whether err is a business-rule failure of a trigger function
// (recorded, the post goes on) rather than an infrastructure error (the post fails).
func ruleFailure(err error) (string, bool) {
	var de *domain.Error
	if errors.As(err, &de) {
		return de.Detail, true
	}
	var prob *httpx.Problem
	if errors.As(err, &prob) && prob.Status < http.StatusInternalServerError {
		return prob.Detail, true
	}
	return "", false
}
