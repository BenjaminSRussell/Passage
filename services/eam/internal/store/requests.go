package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/eam/internal/domain"
	eamevents "github.com/BenjaminSRussell/passage/services/eam/internal/events"
)

// Request is a maintenance request (FR-44.6): from users, mobile or a condition rule.
type Request struct {
	ID              string    `json:"id"`
	AssetID         string    `json:"assetId"`
	Source          string    `json:"source"`
	Severity        int       `json:"severity"`
	Description     string    `json:"description"`
	ReadingValue    *float64  `json:"readingValue,omitempty"`
	Status          string    `json:"status"`
	WorkOrderID     *string   `json:"workOrderId,omitempty"`
	DedupeKey       *string   `json:"dedupeKey,omitempty"`
	ConditionRuleID *string   `json:"conditionRuleId,omitempty"`
	TriageNote      *string   `json:"triageNote,omitempty"`
	CreatedAt       time.Time `json:"createdAt"`
}

const requestColumns = `id::text, asset_id::text, source, severity, description, reading_value::float8, status,
  work_order_id::text, dedupe_key, condition_rule_id::text, triage_note, created_at`

func scanRequest(row pgx.Row) (Request, error) {
	var r Request
	err := row.Scan(&r.ID, &r.AssetID, &r.Source, &r.Severity, &r.Description, &r.ReadingValue, &r.Status,
		&r.WorkOrderID, &r.DedupeKey, &r.ConditionRuleID, &r.TriageNote, &r.CreatedAt)
	return r, err
}

func getRequest(ctx context.Context, tx pgx.Tx, t, id string, lock bool) (Request, error) {
	q := `select ` + requestColumns + ` from maintenance_request where tenant_id = $1 and id = $2`
	if lock {
		q += ` for update`
	}
	r, err := scanRequest(tx.QueryRow(ctx, q, t, id))
	return r, notFound(err, "maintenance request")
}

// insertRequest stores a NEW request and emits request.created.
func insertRequest(ctx context.Context, tx pgx.Tx, t string, in Request, key string) (Request, error) {
	id := ids.New()
	r, err := scanRequest(tx.QueryRow(ctx, `insert into maintenance_request (tenant_id, id, asset_id, source, severity,
	    description, reading_value, dedupe_key, condition_rule_id, idempotency_key)
	  values ($1, $2, $3, $4, $5, $6, $7::numeric, $8, $9, $10) returning `+requestColumns,
		t, id, in.AssetID, in.Source, in.Severity, in.Description, numPtr(in.ReadingValue), in.DedupeKey,
		in.ConditionRuleID, optStr(key)))
	if err != nil {
		return r, err
	}
	err = enqueue(ctx, tx, t, "request", r.ID, eamevents.RequestCreated, eamevents.RequestCreatedData{
		RequestID: r.ID, AssetID: r.AssetID, Source: r.Source, Severity: r.Severity,
		DedupeKey: deref(r.DedupeKey, "")})
	return r, err
}

// CreateRequest records a user or mobile request against an existing asset.
func (s *Postgres) CreateRequest(ctx context.Context, in Request, key string) (Request, error) {
	var out Request
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		if id, err := existingID(ctx, tx, "maintenance_request", t, key); err != nil || id != "" {
			if err != nil {
				return err
			}
			var gerr error
			out, gerr = getRequest(ctx, tx, t, id, false)
			return gerr
		}
		if _, err := getAsset(ctx, tx, t, in.AssetID, false); err != nil {
			return err
		}
		var err error
		out, err = insertRequest(ctx, tx, t, in, key)
		return err
	})
	return out, err
}

// ConvertRequest turns a request into a work order in one action (8.44.2):
// severity 1 becomes an EMERGENCY order at priority 1, otherwise CORRECTIVE at
// the criticality default (BR-44.15). A repeated key returns the same order.
func (s *Postgres) ConvertRequest(ctx context.Context, id, currency, key string) (WorkOrder, error) {
	var out WorkOrder
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		if woID, err := existingID(ctx, tx, "work_order", t, key); err != nil || woID != "" {
			if err != nil {
				return err
			}
			var gerr error
			out, gerr = getWorkOrder(ctx, tx, t, woID, false)
			return gerr
		}
		req, err := getRequest(ctx, tx, t, id, true)
		if err != nil {
			return err
		}
		if req.Status != "NEW" && req.Status != "TRIAGED" {
			return problem(422, CodeRequestState, "request is "+req.Status+"; only NEW or TRIAGED requests convert")
		}
		asset, err := getAsset(ctx, tx, t, req.AssetID, false)
		if err != nil {
			return err
		}
		if !domain.ActiveState(asset.LifecycleState) {
			return problem(422, CodeAssetState, "asset is "+asset.LifecycleState+"; work orders need ACTIVE")
		}
		wo := WorkOrder{AssetID: req.AssetID, Type: "CORRECTIVE", Status: "CREATED", Currency: currency,
			Priority: domain.DefaultPriority(asset.Criticality, true), RequestID: &req.ID}
		if req.Severity == 1 {
			wo.Type, wo.Priority = "EMERGENCY", 1
		}
		woID, err := insertWorkOrder(ctx, tx, t, wo, key, s.now())
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update maintenance_request set status = 'CONVERTED', work_order_id = $3,
		    updated_at = now() where tenant_id = $1 and id = $2`, t, req.ID, woID); err != nil {
			return err
		}
		if err := enqueue(ctx, tx, t, "workorder", woID, eamevents.WorkOrderCreated, eamevents.WorkOrderCreatedData{
			WorkOrderID: woID, AssetID: req.AssetID, Type: wo.Type}); err != nil {
			return err
		}
		out, err = getWorkOrder(ctx, tx, t, woID, false)
		return err
	})
	return out, err
}

// ListRequests returns a page of maintenance requests, newest first,
// optionally for one status or asset (8.44.14 Request list).
func (s *Postgres) ListRequests(ctx context.Context, opt ListOptions) (Page[Request], error) {
	var out Page[Request]
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		rows, err := tx.Query(ctx, `select `+requestColumns+` from maintenance_request
		  where tenant_id = $1 and ($2::text is null or status = $2) and ($3::uuid is null or asset_id = $3)
		    and ($4::uuid is null or id < $4)
		  order by id desc limit $5`, t, optStr(opt.Status), optStr(opt.AssetID), optStr(opt.Cursor), opt.Limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		var items []Request
		for rows.Next() {
			r, err := scanRequest(rows)
			if err != nil {
				return err
			}
			items = append(items, r)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		out = pageOf(items, opt.Limit, func(r Request) string { return r.ID })
		return nil
	})
	return out, err
}
