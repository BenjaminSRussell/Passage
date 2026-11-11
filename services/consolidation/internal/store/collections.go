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
	// MappingVersion is the version of the chart mapping the lines were mapped with (0 for a collection made before mappings
	// were versioned).
	MappingVersion int
}

// CollectionDetail is a collection with every collected line and the rates it translated at.
type CollectionDetail struct {
	Collection
	Lines []domain.CollectLine
	Rates map[string]domain.CollectRate
}

func errCollectionNotFound(id string) error {
	return &domain.Error{Code: domain.CodeCollectionGone, Detail: "no collection with id " + id}
}

// CreateCollection stores collected lines as the next version of the group's
// collection for the period. The lines and header are immutable. With an
// Idempotency-Key a retry returns the stored collection.
func (s *Postgres) CreateCollection(ctx context.Context, groupID, period string, mappingVersion int, lines []domain.CollectLine, inputs any, key string) (Collection, bool, error) {
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
			if err := lockCollectedUnits(ctx, tx, tenantID, groupID, lines); err != nil {
				return Collection{}, err
			}
			c := Collection{ID: ids.New(), GroupID: groupID, Period: period, Currency: currency, CreatedBy: by, MappingVersion: mappingVersion}
			if err := tx.QueryRow(ctx, `select coalesce(max(version), 0) + 1 from consolidation_collection where tenant_id = $1 and group_id = $2::uuid and period = $3`,
				tenantID, groupID, period).Scan(&c.Version); err != nil {
				return Collection{}, err
			}
			if err := tx.QueryRow(ctx, `insert into consolidation_collection (tenant_id, id, group_id, period, version, currency, inputs, created_by, mapping_version)
				values ($1, $2, $3::uuid, $4, $5, $6, $7, $8, nullif($9, 0)) returning created_at`, tenantID, c.ID, groupID, period, c.Version, currency, raw, by, mappingVersion).Scan(&c.CreatedAt); err != nil {
				return Collection{}, err
			}
			var batch pgx.Batch
			for i, l := range lines {
				var acct, typ any
				if l.Mapped() {
					acct, typ = l.ConsolidationAccount, l.Type
				}
				batch.Queue(`insert into consolidation_collection_line (tenant_id, collection_id, seq, unit_id, main_account, consolidation_account, account_type, unit_currency, unit_amount, rate, group_amount, role)
					values ($1, $2, $3, $4::uuid, $5, $6, $7, $8, $9::numeric, $10::numeric, $11::numeric, nullif($12, ''))`,
					tenantID, c.ID, i, l.UnitID, l.MainAccount, acct, typ, l.UnitCurrency, l.UnitAmount.String(), l.Rate.String(), l.GroupAmount.String(), l.Role)
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
		return loadCollection(ctx, tx, pr.TenantID, `id = $2::uuid`, id, &d)
	})
	if err != nil {
		return CollectionDetail{}, wrap("get consolidation collection", err)
	}
	return d, nil
}

// LatestCollection returns the newest version of the group's collection for a period; ok is false when there is none.
func (s *Postgres) LatestCollection(ctx context.Context, groupID, period string) (CollectionDetail, bool, error) {
	pr, err := principal(ctx)
	if err != nil {
		return CollectionDetail{}, false, err
	}
	var d CollectionDetail
	found := true
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var id string
		err := tx.QueryRow(ctx, `select id::text from consolidation_collection where tenant_id = $1 and group_id = $2::uuid and period = $3 order by version desc limit 1`,
			pr.TenantID, groupID, period).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			found = false
			return nil
		}
		if err != nil {
			return err
		}
		return loadCollection(ctx, tx, pr.TenantID, `id = $2::uuid`, id, &d)
	})
	if err != nil {
		return CollectionDetail{}, false, wrap("get latest consolidation collection", err)
	}
	return d, found, nil
}

func loadCollection(ctx context.Context, tx pgx.Tx, tenantID, where, id string, d *CollectionDetail) error {
	var inputs []byte
	err := tx.QueryRow(ctx, `select id::text, group_id::text, period, version, currency, created_by, created_at, coalesce(mapping_version, 0), inputs from consolidation_collection
		where tenant_id = $1 and `+where, tenantID, id).Scan(&d.ID, &d.GroupID, &d.Period, &d.Version, &d.Currency, &d.CreatedBy, &d.CreatedAt, &d.MappingVersion, &inputs)
	if errors.Is(err, pgx.ErrNoRows) {
		return errCollectionNotFound(id)
	}
	if err != nil {
		return err
	}
	var in struct {
		Rates map[string]domain.CollectRate `json:"rates"`
	}
	if err := json.Unmarshal(inputs, &in); err != nil {
		return fmt.Errorf("store: collection inputs: %w", err)
	}
	d.Rates = in.Rates
	rows, err := tx.Query(ctx, `select unit_id::text, main_account, coalesce(consolidation_account, ''), coalesce(account_type, ''), coalesce(role, ''), unit_currency,
			unit_amount::text, rate::text, group_amount::text
		from consolidation_collection_line where tenant_id = $1 and collection_id = $2::uuid order by seq`, tenantID, d.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var l domain.CollectLine
		var amount, rate, group string
		if err := rows.Scan(&l.UnitID, &l.MainAccount, &l.ConsolidationAccount, &l.Type, &l.Role, &l.UnitCurrency, &amount, &rate, &group); err != nil {
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
}

// lockCollectedUnits holds the units the lines were collected from (FOR SHARE) until the collection commits, and refuses lines
// of a unit that left the group or changed currency since it was read: the lines are read from the ledgers before this
// transaction, so without it a unit removed meanwhile (RemoveUnit checks for collection lines under its row lock) would leave
// stored lines of a unit the group no longer has, and a currency change would leave lines in a currency the unit no longer is.
func lockCollectedUnits(ctx context.Context, tx pgx.Tx, tenantID, groupID string, lines []domain.CollectLine) error {
	want := map[string]string{}
	for _, l := range lines {
		want[l.UnitID] = l.UnitCurrency
	}
	if len(want) == 0 {
		return nil
	}
	unitIDs := make([]string, 0, len(want))
	for id := range want {
		unitIDs = append(unitIDs, id)
	}
	rows, err := tx.Query(ctx, `select id::text, currency from consolidation_unit where tenant_id = $1 and group_id = $2::uuid and id = any($3::uuid[])
		order by id for share`, tenantID, groupID, unitIDs)
	if err != nil {
		return err
	}
	have := map[string]string{}
	for rows.Next() {
		var id, currency string
		if err := rows.Scan(&id, &currency); err != nil {
			rows.Close()
			return err
		}
		have[id] = currency
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range unitIDs {
		currency, ok := have[id]
		switch {
		case !ok:
			return &domain.Error{Code: domain.CodeCollectionError, Detail: "unit " + id + " left the group while it was collected; collect again"}
		case currency != want[id]:
			return &domain.Error{Code: domain.CodeCollectionError, Detail: "unit " + id + " changed its currency to " + currency + " while it was collected; collect again"}
		}
	}
	return nil
}
