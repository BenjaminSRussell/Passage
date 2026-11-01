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
	Seen(ctx context.Context, consumer, eventID string) (bool, error)
	Record(ctx context.Context, consumer, eventID string) error
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

func inboxKey(ctx context.Context, consumer, eventID string) (tenant.Principal, error) {
	p, ok := tenant.FromContext(ctx)
	if consumer == "" || eventID == "" || !ok || !ids.Valid(p.TenantID) {
		return p, fmt.Errorf("tenant, consumer and event id are required: %w", ErrInvalidArgument)
	}
	return p, nil
}

func (i *PgInbox) Seen(ctx context.Context, consumer, eventID string) (bool, error) {
	p, err := inboxKey(ctx, consumer, eventID)
	if err != nil {
		return false, err
	}
	seen := false
	err = dbx.WithTenantTx(ctx, i.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select exists(select 1 from consumer_inbox where tenant_id = $1 and consumer = $2 and event_id = $3)`, p.TenantID, consumer, eventID).Scan(&seen)
	})
	return seen, err
}

func (i *PgInbox) Record(ctx context.Context, consumer, eventID string) error {
	p, err := inboxKey(ctx, consumer, eventID)
	if err != nil {
		return err
	}
	return dbx.WithTenantTx(ctx, i.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into consumer_inbox(tenant_id, consumer, event_id) values($1,$2,$3) on conflict do nothing`, p.TenantID, consumer, eventID)
		return err
	})
}
