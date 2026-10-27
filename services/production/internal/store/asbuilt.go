package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	prodevents "github.com/BenjaminSRussell/passage/services/production/internal/events"
)

// CodeAsBuiltNotFound is returned for an unknown as-built record.
const CodeAsBuiltNotFound = "PRODUCTION_AS_BUILT_NOT_FOUND"

// maxAsBuiltRows caps one genealogy query; the tree of one serial is small.
const maxAsBuiltRows = 500

// serialIndex is the unique index that keeps a component serial on one finished serial (BR-21.14).
const serialIndex = "production_as_built_component_serial"

// AsBuiltRecord is one stored as-built record (PRD 4.21.4 AsBuiltRecord): a
// component serial or batch consumed into a finished serial or batch.
// JournalID and RAFJournalID stay null until tracking dimensions (wave 16)
// capture serials on picking and RAF lines. A detached record no longer counts.
type AsBuiltRecord struct {
	ID                     string          `json:"id"`
	OrderID                string          `json:"orderId"`
	OrderNumber            string          `json:"orderNumber"`
	FinishedItemID         string          `json:"finishedItemId"`
	FinishedSerialOrBatch  string          `json:"finishedSerialOrBatch"`
	ComponentItemID        string          `json:"componentItemId"`
	Tracking               domain.Tracking `json:"tracking"`
	ComponentSerialOrBatch string          `json:"componentSerialOrBatch"`
	Qty                    decimal.Decimal `json:"qty"`
	JournalID              *string         `json:"journalId,omitempty"`
	RAFJournalID           *string         `json:"rafJournalId,omitempty"`
	AssignedBy             string          `json:"assignedBy"`
	AssignedAt             time.Time       `json:"assignedAt"`
	DetachedBy             *string         `json:"detachedBy,omitempty"`
	DetachedAt             *time.Time      `json:"detachedAt,omitempty"`
}

// NewAsBuilt is an assignment to record on an order. RequestHash identifies
// the request for Idempotency-Key replays.
type NewAsBuilt struct {
	OrderID        string
	AsBuilt        domain.AsBuilt
	Actor          string
	IdempotencyKey string
	RequestHash    string
}

// AsBuiltFilter selects attached records; every non-empty field narrows the
// result. A finished serial gives its as-built; a component serial its where-used.
type AsBuiltFilter struct {
	OrderID                string
	FinishedSerialOrBatch  string
	ComponentSerialOrBatch string
	ComponentItemID        string
}

const asBuiltColumns = `a.id::text, a.order_id::text, o.order_number, a.finished_item_id::text, a.finished_serial_or_batch,
	a.component_item_id::text, a.tracking, a.component_serial_or_batch, a.qty::text, a.journal_id::text, a.raf_journal_id::text,
	a.assigned_by, a.assigned_at, a.detached_by, a.detached_at`

const asBuiltFrom = ` from production_as_built a
	join production_order o on o.tenant_id = a.tenant_id and o.id = a.order_id`

func scanAsBuilt(row pgx.Row) (AsBuiltRecord, error) {
	var r AsBuiltRecord
	var tracking, qty string
	err := row.Scan(&r.ID, &r.OrderID, &r.OrderNumber, &r.FinishedItemID, &r.FinishedSerialOrBatch, &r.ComponentItemID, &tracking,
		&r.ComponentSerialOrBatch, &qty, &r.JournalID, &r.RAFJournalID, &r.AssignedBy, &r.AssignedAt, &r.DetachedBy, &r.DetachedAt)
	r.Tracking, r.Qty = domain.Tracking(tracking), dec(qty)
	return r, err
}

func loadAsBuilt(ctx context.Context, q querier, tenantID, id string) (AsBuiltRecord, error) {
	r, err := scanAsBuilt(q.QueryRow(ctx, `select `+asBuiltColumns+asBuiltFrom+` where a.tenant_id = $1 and a.id = $2`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return r, httpx.NewProblem(http.StatusNotFound, CodeAsBuiltNotFound, "as-built record not found")
	}
	return r, err
}

func enqueueAsBuilt(ctx context.Context, tx pgx.Tx, tenantID, eventType, actor string, r AsBuiltRecord) error {
	data := prodevents.AsBuiltData{RecordID: r.ID, OrderID: r.OrderID, FinishedItemID: r.FinishedItemID,
		FinishedSerialOrBatch: r.FinishedSerialOrBatch, ComponentItemID: r.ComponentItemID, Tracking: string(r.Tracking),
		ComponentSerialOrBatch: r.ComponentSerialOrBatch, Qty: r.Qty.String(), AssignedBy: r.AssignedBy,
		AssignedAt: r.AssignedAt.UTC().Format(time.RFC3339Nano)}
	if r.DetachedAt != nil {
		at := r.DetachedAt.UTC().Format(time.RFC3339Nano)
		data.DetachedAt = &at
	}
	return enqueue(ctx, tx, r.OrderID, eventType, tenantID, actor, data)
}

// AssignAsBuilt records that a component serial or batch went into a finished
// serial or batch of an order (FR-21.19). Under the order row lock it applies
// the rules of domain.CheckAsBuilt and refuses a component serial already on
// an attached record anywhere in the tenant (BR-21.14, SERIAL_DUPLICATE). A
// replay with the same Idempotency-Key and request returns the stored record
// with created=false; the same key with another request is refused.
func (s *Postgres) AssignAsBuilt(ctx context.Context, in NewAsBuilt) (AsBuiltRecord, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out AsBuiltRecord
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var replayID, replayHash string
		err := tx.QueryRow(ctx, `select id::text, request_hash from production_as_built where tenant_id = $1 and idempotency_key = $2`,
			p.TenantID, in.IdempotencyKey).Scan(&replayID, &replayHash)
		if err == nil {
			if replayHash != in.RequestHash {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request")
			}
			out, err = loadAsBuilt(ctx, tx, p.TenantID, replayID)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		h, err := lockOrder(ctx, tx, p.TenantID, in.OrderID, 0)
		if err != nil {
			return err
		}
		a := in.AsBuilt
		facts := domain.AsBuiltFacts{Status: h.status, OrderQty: h.quantity}
		if facts.Lines, err = loadLines(ctx, tx, p.TenantID, h.id); err != nil {
			return err
		}
		var assigned string
		if err := tx.QueryRow(ctx, `select coalesce(sum(qty) filter (where component_item_id = $3), 0)::text,
				count(distinct finished_serial_or_batch), coalesce(bool_or(finished_serial_or_batch = $4), false)
			from production_as_built where tenant_id = $1 and order_id = $2 and detached_at is null`,
			p.TenantID, h.id, a.ComponentItemID, a.FinishedSerial).Scan(&assigned, &facts.FinishedCount, &facts.FinishedKnown); err != nil {
			return err
		}
		facts.ComponentAssigned = dec(assigned)
		if err := domain.CheckAsBuilt(a, facts); err != nil {
			return domainProblem(err)
		}
		if a.Tracking == domain.TrackSerial {
			var taken bool
			if err := tx.QueryRow(ctx, `select exists(select 1 from production_as_built where tenant_id = $1 and component_item_id = $2
					and component_serial_or_batch = $3 and tracking = 'serial' and detached_at is null)`,
				p.TenantID, a.ComponentItemID, a.ComponentSerial).Scan(&taken); err != nil {
				return err
			}
			if taken {
				return serialDuplicate(a.ComponentSerial)
			}
		}
		id := ids.New()
		if _, err := tx.Exec(ctx, `insert into production_as_built (tenant_id, id, legal_entity_id, order_id, finished_item_id,
				finished_serial_or_batch, component_item_id, tracking, component_serial_or_batch, qty, idempotency_key, request_hash, assigned_by)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
			p.TenantID, id, h.legalEntityID, h.id, h.productID, a.FinishedSerial, a.ComponentItemID, string(a.Tracking), a.ComponentSerial,
			a.Qty.String(), in.IdempotencyKey, in.RequestHash, in.Actor); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == serialIndex {
				// Another order took the serial between the check and the insert.
				return serialDuplicate(a.ComponentSerial)
			}
			return err
		}
		if out, err = loadAsBuilt(ctx, tx, p.TenantID, id); err != nil {
			return err
		}
		created = true
		return enqueueAsBuilt(ctx, tx, p.TenantID, prodevents.AsBuiltAssigned, in.Actor, out)
	})
	return out, created, err
}

func serialDuplicate(serial string) error {
	return httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeSerialDuplicate,
		fmt.Sprintf("component serial %s is already assigned to a finished serial", serial))
}

// ListAsBuilt returns the attached records matching f, oldest first, at most 500.
func (s *Postgres) ListAsBuilt(ctx context.Context, f AsBuiltFilter) ([]AsBuiltRecord, error) {
	p, _ := tenant.FromContext(ctx)
	out := []AsBuiltRecord{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+asBuiltColumns+asBuiltFrom+`
			where a.tenant_id = $1 and a.detached_at is null
			  and ($2 = '' or a.order_id = nullif($2, '')::uuid)
			  and ($3 = '' or a.finished_serial_or_batch = $3)
			  and ($4 = '' or a.component_serial_or_batch = $4)
			  and ($5 = '' or a.component_item_id = nullif($5, '')::uuid)
			order by a.assigned_at, a.id
			limit $6`, p.TenantID, f.OrderID, f.FinishedSerialOrBatch, f.ComponentSerialOrBatch, f.ComponentItemID, maxAsBuiltRows)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanAsBuilt(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// DetachAsBuilt detaches a record to correct a wrong assignment while its
// order is still Started or ReportedAsFinished. The record stays for the
// audit trail. Detaching a detached record returns it unchanged.
func (s *Postgres) DetachAsBuilt(ctx context.Context, id, actor string) (AsBuiltRecord, error) {
	p, _ := tenant.FromContext(ctx)
	var out AsBuiltRecord
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		r, err := loadAsBuilt(ctx, tx, p.TenantID, id)
		if err != nil {
			return err
		}
		h, err := lockOrder(ctx, tx, p.TenantID, r.OrderID, 0)
		if err != nil {
			return err
		}
		if r, err = loadAsBuilt(ctx, tx, p.TenantID, id); err != nil {
			return err
		}
		if r.DetachedAt != nil {
			out = r
			return nil
		}
		if h.status != domain.Started && h.status != domain.ReportedAsFinished {
			return domainProblem(&domain.Error{Code: domain.CodeAsBuiltState,
				Detail: fmt.Sprintf("as-built records can be detached while the order is Started or ReportedAsFinished, not %s", h.status)})
		}
		if _, err := tx.Exec(ctx, `update production_as_built set detached_by = $3, detached_at = now() where tenant_id = $1 and id = $2`,
			p.TenantID, id, actor); err != nil {
			return err
		}
		if out, err = loadAsBuilt(ctx, tx, p.TenantID, id); err != nil {
			return err
		}
		return enqueueAsBuilt(ctx, tx, p.TenantID, prodevents.AsBuiltDetached, actor, out)
	})
	return out, err
}

// detachOrderAsBuilt detaches every attached record of an order, inside the
// caller's transaction, and enqueues asbuilt.detached for each (BR-21.14: a
// reset that reverses the reports as finished takes the finished serials back).
// Until RAF lines carry serials (wave 16) the records cannot be matched to one
// RAF journal, and every RAF journal is reversed together, so all detach.
func detachOrderAsBuilt(ctx context.Context, tx pgx.Tx, tenantID, orderID, actor string) error {
	rows, err := tx.Query(ctx, `update production_as_built set detached_by = $3, detached_at = now()
		where tenant_id = $1 and order_id = $2 and detached_at is null returning id::text`, tenantID, orderID, actor)
	if err != nil {
		return err
	}
	var detached []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		detached = append(detached, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range detached {
		r, err := loadAsBuilt(ctx, tx, tenantID, id)
		if err != nil {
			return err
		}
		if err := enqueueAsBuilt(ctx, tx, tenantID, prodevents.AsBuiltDetached, actor, r); err != nil {
			return err
		}
	}
	return nil
}
