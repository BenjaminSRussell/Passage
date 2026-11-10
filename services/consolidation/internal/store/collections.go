package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/consolidation/internal/domain"
)

// Collection is the header of one stored collection of unit balances.
type Collection struct {
	ID        string
	GroupID   string
	Period    string
	Version   int
	Currency  string
	CreatedBy string
	CreatedAt time.Time
}

// CollectionDetail is a collection with every collected line.
type CollectionDetail struct {
	Collection
	Lines []domain.CollectLine
}

func errCollectionNotFound(id string) error {
	return &domain.Error{Code: domain.CodeCollectionGone, Detail: "no collection with id " + id}
}

// CreateCollection stores collected lines as the next version of the group's
// collection for the period. The lines and header are immutable. With an
// Idempotency-Key a retry returns the stored collection.
func (s *Postgres) CreateCollection(ctx context.Context, groupID, period string, lines []domain.CollectLine, inputs any, key string) (Collection, bool, error) {
	tenantID, by, err := creator(ctx)
	if err != nil {
		return Collection{}, false, err
	}
	raw, err := json.Marshal(inputs)
	if err != nil {
		return Collection{}, false, err
	}
	hash, err := requestHash("collection", groupID, period, string(raw))
	if err != nil {
		return Collection{}, false, err
	}
	var (
		out      Collection
		replayed bool
	)
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		create := func() (Collection, error) {
			currency, err := loadGroupHeader(ctx, tx, tenantID, groupID)
			if err != nil {
				return Collection{}, err
			}
			if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, tenantID+"|collection|"+groupID+"|"+period); err != nil {
				return Collection{}, err
			}
			c := Collection{ID: ids.New(), GroupID: groupID, Period: period, Currency: currency, CreatedBy: by}
			if err := tx.QueryRow(ctx, `select coalesce(max(version), 0) + 1 from consolidation_collection where tenant_id = $1 and group_id = $2::uuid and period = $3`,
				tenantID, groupID, period).Scan(&c.Version); err != nil {
				return Collection{}, err
			}
			if err := tx.QueryRow(ctx, `insert into consolidation_collection (tenant_id, id, group_id, period, version, currency, inputs, created_by)
				values ($1, $2, $3::uuid, $4, $5, $6, $7, $8) returning created_at`, tenantID, c.ID, groupID, period, c.Version, currency, raw, by).Scan(&c.CreatedAt); err != nil {
				return Collection{}, err
			}
			var batch pgx.Batch
			for i, l := range lines {
				var acct, typ any
				if l.Mapped() {
					acct, typ = l.ConsolidationAccount, l.Type
				}
				batch.Queue(`insert into consolidation_collection_line (tenant_id, collection_id, seq, unit_id, main_account, consolidation_account, account_type, unit_currency, unit_amount, rate, group_amount)
					values ($1, $2, $3, $4::uuid, $5, $6, $7, $8, $9::numeric, $10::numeric, $11::numeric)`,
					tenantID, c.ID, i, l.UnitID, l.MainAccount, acct, typ, l.UnitCurrency, l.UnitAmount.String(), l.Rate.String(), l.GroupAmount.String())
			}
			res := tx.SendBatch(ctx, &batch)
			for range batch.Len() {
				if _, err := res.Exec(); err != nil {
					_ = res.Close()
					return Collection{}, err
				}
			}
			return c, res.Close()
		}
		if key == "" {
			out, err = create()
			return err
		}
		out, replayed, err = idempotent(ctx, tx, tenantID, key, hash, create)
		return err
	})
	if err != nil {
		return Collection{}, false, wrap("create consolidation collection", err)
	}
	return out, replayed, nil
}

// ListCollections returns a group's collections, newest first.
func (s *Postgres) ListCollections(ctx context.Context, groupID string) ([]Collection, error) {
	pr, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	out := []Collection{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := loadGroupHeader(ctx, tx, pr.TenantID, groupID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select id::text, group_id::text, period, version, currency, created_by, created_at from consolidation_collection
			where tenant_id = $1 and group_id = $2::uuid order by created_at desc, id desc limit 200`, pr.TenantID, groupID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c Collection
			if err := rows.Scan(&c.ID, &c.GroupID, &c.Period, &c.Version, &c.Currency, &c.CreatedBy, &c.CreatedAt); err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, wrap("list consolidation collections", err)
	}
	return out, nil
}

// GetCollection returns a collection with its lines, or CNS-0928.
func (s *Postgres) GetCollection(ctx context.Context, id string) (CollectionDetail, error) {
	pr, err := principal(ctx)
	if err != nil {
		return CollectionDetail{}, err
	}
	var d CollectionDetail
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `select id::text, group_id::text, period, version, currency, created_by, created_at from consolidation_collection
			where tenant_id = $1 and id = $2::uuid`, pr.TenantID, id).Scan(&d.ID, &d.GroupID, &d.Period, &d.Version, &d.Currency, &d.CreatedBy, &d.CreatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return errCollectionNotFound(id)
		}
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select unit_id::text, main_account, coalesce(consolidation_account, ''), coalesce(account_type, ''), unit_currency,
				unit_amount::text, rate::text, group_amount::text
			from consolidation_collection_line where tenant_id = $1 and collection_id = $2::uuid order by seq`, pr.TenantID, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var l domain.CollectLine
			var amount, rate, group string
			if err := rows.Scan(&l.UnitID, &l.MainAccount, &l.ConsolidationAccount, &l.Type, &l.UnitCurrency, &amount, &rate, &group); err != nil {
				return err
			}
			for _, f := range []struct {
				dst *decimal.Decimal
				raw string
			}{{&l.UnitAmount, amount}, {&l.Rate, rate}, {&l.GroupAmount, group}} {
				if *f.dst, err = decimal.NewFromString(f.raw); err != nil {
					return fmt.Errorf("store: collection amount %q: %w", f.raw, err)
				}
			}
			d.Lines = append(d.Lines, l)
		}
		return rows.Err()
	})
	if err != nil {
		return CollectionDetail{}, wrap("get consolidation collection", err)
	}
	return d, nil
}
