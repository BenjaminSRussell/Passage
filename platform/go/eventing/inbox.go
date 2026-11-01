package eventing

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

var ErrInvalidArgument = errors.New("invalid argument")

type Inbox interface {
	Once(ctx context.Context, consumer, eventID string, effect func(pgx.Tx) error) (bool, error)
}

type PgInbox struct{ pool *pgxpool.Pool }

func NewPgInbox(pool *pgxpool.Pool) *PgInbox { return &PgInbox{pool: pool} }

func (i *PgInbox) Once(ctx context.Context, consumer, eventID string, effect func(pgx.Tx) error) (bool, error) {
	p, ok := tenant.FromContext(ctx)
	if consumer == "" || eventID == "" || effect == nil || !ok || !ids.Valid(p.TenantID) {
		return false, fmt.Errorf("tenant, consumer, event id and effect are required: %w", ErrInvalidArgument)
	}
	applied := false
	err := dbx.WithTenantTx(ctx, i.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `insert into consumer_inbox(tenant_id, consumer, event_id) values($1,$2,$3) on conflict do nothing`, p.TenantID, consumer, eventID)
		if err != nil {
			return fmt.Errorf("insert inbox: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		if err := effect(tx); err != nil {
			return fmt.Errorf("apply event %s: %w", eventID, err)
		}
		applied = true
		return nil
	})
	return applied, err
}
