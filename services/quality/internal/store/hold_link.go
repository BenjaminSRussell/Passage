package store

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/quality/internal/domain"
	"github.com/BenjaminSRussell/passage/services/quality/internal/inventory"
)

const (
	holdSourceOrder = "QUALITY_ORDER"
	holdSourceNCR   = "NONCONFORMANCE"
	holdPlace       = "PLACE"
	holdRelease     = "RELEASE"
	holdPending     = "PENDING"
	holdDone        = "DONE"
	holdFailed      = "FAILED"
	holdBatchSize   = 20
	holdMaxAttempts = 10
	holdCallTimeout = 5 * time.Second
)

var inventoryDimensions = map[string]bool{"site": true, "warehouse": true, "location": true, "batch": true, "serial": true,
	"config": true, "size": true, "color": true, "style": true}

type HoldPort interface {
	Place(ctx context.Context, call inventory.Call) error
	Release(ctx context.Context, call inventory.Call) error
}

func (s *Postgres) WithHolds(h HoldPort) *Postgres {
	linked := *s
	linked.holds = h
	return &linked
}

type holdLink struct {
	id          string
	key         string
	action      string
	sourceType  string
	sourceRef   string
	legalEntity string
	user        string
	item        string
	dims        map[string]string
	qty         decimal.Decimal
	keep        decimal.Decimal
	attempts    int
}

func inventoryDims(sets ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, set := range sets {
		for k, v := range set {
			if inventoryDimensions[k] && v != "" {
				out[k] = v
			}
		}
	}
	return out
}

func orderHoldLink(o domain.Order) holdLink {
	return holdLink{key: "quality-order:" + o.ID + ":place", action: holdPlace, sourceType: holdSourceOrder, sourceRef: o.ID, item: o.ItemID, qty: o.LotQty,
		dims: inventoryDims(o.Dims, map[string]string{"site": o.SiteID, "warehouse": o.WarehouseID})}
}

func ncrHoldLink(n domain.Nonconformance) holdLink {
	return holdLink{key: "nonconformance:" + n.ID + ":place", action: holdPlace, sourceType: holdSourceNCR, sourceRef: n.ID, item: n.ItemID, qty: n.Qty,
		dims: inventoryDims(n.Dims)}
}

func ncrReleaseLink(n domain.Nonconformance, status string) holdLink {
	l := holdLink{key: "nonconformance:" + n.ID + ":" + status + ":release", action: holdRelease, sourceType: holdSourceNCR, sourceRef: n.ID, item: n.ItemID}
	if n.QualityOrderID != "" {
		l.sourceType, l.sourceRef = holdSourceOrder, n.QualityOrderID
	}
	return l
}

func (s *Postgres) holdForNCR(ctx context.Context, tx pgx.Tx, n domain.Nonconformance) error {
	if s.holds == nil {
		return nil
	}
	if n.QualityOrderID == "" {
		return s.queueHold(ctx, tx, ncrHoldLink(n))
	}
	o, err := readOrder(ctx, tx, n.QualityOrderID, false)
	if err != nil {
		return err
	}
	return s.queueHold(ctx, tx, orderHoldLink(o))
}

func (s *Postgres) queueHold(ctx context.Context, tx pgx.Tx, l holdLink) error {
	if s.holds == nil {
		return nil
	}
	p, _ := tenant.FromContext(ctx)
	if p.LegalEntityID == "" {
		slog.WarnContext(ctx, "inventory hold not queued: the caller has no single legal entity", "source", l.sourceType, "ref", l.sourceRef,
			"correlationId", middleware.GetReqID(ctx))
		return nil
	}
	if l.dims == nil {
		l.dims = map[string]string{}
	}
	dims, err := json.Marshal(l.dims)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `insert into quality_hold_link (tenant_id, id, idempotency_key, action, source_type, source_ref, legal_entity_id, acting_user,
		item_id, dims, qty, keep_qty) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::numeric, $12::numeric)
		on conflict (tenant_id, idempotency_key) do nothing`,
		p.TenantID, ids.New(), l.key, l.action, l.sourceType, l.sourceRef, p.LegalEntityID, p.UserID, l.item, string(dims), l.qty.String(), l.keep.String())
	return err
}

func (s *Postgres) holdForDecision(ctx context.Context, tx pgx.Tx, o domain.Order, d domain.Decision) error {
	if s.holds == nil {
		return nil
	}
	if d.Split.Reject.IsPositive() {
		if err := s.queueHold(ctx, tx, orderHoldLink(o)); err != nil {
			return err
		}
	}
	release := holdLink{key: "decision:" + d.ID + ":release", action: holdRelease, sourceType: holdSourceOrder, sourceRef: o.ID, item: o.ItemID, keep: d.Split.Reject}
	return s.queueHold(ctx, tx, release)
}

// FlushHolds sends the queued hold calls to inventory in the order they were
// queued. A call inventory refuses is FAILED and not retried; a transport
// failure or a 5xx stays PENDING and stops the batch, so a release never
// overtakes its placement. Pending links are retried by the next flush.
func (s *Postgres) FlushHolds(ctx context.Context) {
	if s.holds == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), holdCallTimeout*holdBatchSize)
	defer cancel()
	var links []holdLink
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id::text, idempotency_key, action, source_type, source_ref, legal_entity_id::text, acting_user, item_id::text,
			dims, qty::text, keep_qty::text, attempts from quality_hold_link where `+ownTenant+` and state = 'PENDING' order by created_at, id limit $1`, holdBatchSize)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var l holdLink
			var raw []byte
			var qty, keep string
			if err := rows.Scan(&l.id, &l.key, &l.action, &l.sourceType, &l.sourceRef, &l.legalEntity, &l.user, &l.item, &raw, &qty, &keep, &l.attempts); err != nil {
				return err
			}
			if err := json.Unmarshal(raw, &l.dims); err != nil {
				return err
			}
			l.qty, l.keep = decimal.RequireFromString(qty), decimal.RequireFromString(keep)
			links = append(links, l)
		}
		return rows.Err()
	})
	if err != nil {
		slog.ErrorContext(ctx, "reading queued inventory holds failed", "err", err, "correlationId", middleware.GetReqID(ctx))
		return
	}
	p, _ := tenant.FromContext(ctx)
	for _, l := range links {
		callCtx, stop := context.WithTimeout(ctx, holdCallTimeout)
		call := inventory.Call{TenantID: p.TenantID, LegalEntityID: l.legalEntity, UserID: l.user, Key: l.key, SourceType: l.sourceType,
			SourceRef: l.sourceRef, ProductID: l.item, Dims: l.dims, Qty: l.qty, KeepQty: l.keep}
		if l.action == holdPlace {
			err = s.holds.Place(callCtx, call)
		} else {
			err = s.holds.Release(callCtx, call)
		}
		stop()
		state := holdDone
		switch {
		case err == nil:
		case errors.Is(err, inventory.ErrRejected) || l.attempts+1 >= holdMaxAttempts:
			state = holdFailed
		default:
			state = holdPending
		}
		if err != nil {
			slog.ErrorContext(ctx, "inventory hold call failed", "err", err, "action", l.action, "source", l.sourceType, "ref", l.sourceRef,
				"state", state, "correlationId", middleware.GetReqID(ctx))
		}
		if markErr := s.markHold(ctx, l.id, state, err); markErr != nil {
			slog.ErrorContext(ctx, "recording an inventory hold outcome failed", "err", markErr, "link", l.id, "correlationId", middleware.GetReqID(ctx))
			return
		}
		if state == holdPending {
			return
		}
	}
}

func (s *Postgres) markHold(ctx context.Context, id, state string, callErr error) error {
	detail := ""
	if callErr != nil {
		detail = callErr.Error()
		if len(detail) > 500 {
			detail = detail[:500]
		}
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update quality_hold_link set state = $2, attempts = attempts + 1, last_error = $3,
			done_at = case when $2 = 'PENDING' then null else now() end where `+ownTenant+` and id = $1 and state = 'PENDING'`, id, state, detail)
		return err
	})
}
