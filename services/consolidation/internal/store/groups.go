package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/consolidation/internal/domain"
)

const uniqueViolation = "23505"

// GroupSummary is a group with how many units it has.
type GroupSummary struct {
	domain.Group
	UnitCount int
}

// GroupDetail is a group with its units and their ownership history.
type GroupDetail struct {
	domain.Group
	Units []domain.Unit
}

func errGroupNotFound(id string) error {
	return &domain.Error{Code: domain.CodeGroupNotFound, Detail: "no consolidation group or unit with id " + id}
}

func creator(ctx context.Context) (tenantID, by string, err error) {
	pr, err := principal(ctx)
	if err != nil {
		return "", "", err
	}
	by = pr.UserID
	if by == "" {
		by = pr.Service
	}
	if by == "" {
		return "", "", errors.New("store: principal has neither user nor service")
	}
	return pr.TenantID, by, nil
}

func isUnique(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == uniqueViolation
}

func requestHash(parts ...any) (string, error) {
	raw, err := json.Marshal(parts)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// CreateGroup stores a group. A retry with the same Idempotency-Key returns the
// stored group and replayed=true; a second group with the same code fails with CNS-0925.
func (s *Postgres) CreateGroup(ctx context.Context, g domain.Group, key string) (domain.Group, bool, error) {
	if err := g.Validate(); err != nil {
		return domain.Group{}, false, err
	}
	tenantID, by, err := creator(ctx)
	if err != nil {
		return domain.Group{}, false, err
	}
	hash, err := requestHash("group", g.Code, g.Name, g.Currency)
	if err != nil {
		return domain.Group{}, false, err
	}
	var (
		out      domain.Group
		replayed bool
	)
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		create := func() (domain.Group, error) {
			g.ID, g.CreatedBy = ids.New(), by
			err := tx.QueryRow(ctx, `insert into consolidation_group (tenant_id, id, code, name, currency, created_by) values ($1, $2, $3, $4, $5, $6)
				returning created_at`, tenantID, g.ID, g.Code, g.Name, g.Currency, by).Scan(&g.CreatedAt)
			if isUnique(err) {
				return domain.Group{}, &domain.Error{Code: domain.CodeGroupExists, Detail: "a consolidation group with code " + g.Code + " already exists"}
			}
			return g, err
		}
		if key == "" {
			out, err = create()
			return err
		}
		out, replayed, err = idempotent(ctx, tx, tenantID, key, hash, create)
		return err
	})
	if err != nil {
		return domain.Group{}, false, wrap("create consolidation group", err)
	}
	return out, replayed, nil
}

// ListGroups returns the tenant's groups ordered by code.
func (s *Postgres) ListGroups(ctx context.Context) ([]GroupSummary, error) {
	pr, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	out := []GroupSummary{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select g.id::text, g.code, g.name, g.currency, g.created_by, g.created_at,
				(select count(*) from consolidation_unit u where u.tenant_id = g.tenant_id and u.group_id = g.id)
			from consolidation_group g where g.tenant_id = $1 order by g.code`, pr.TenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var g GroupSummary
			if err := rows.Scan(&g.ID, &g.Code, &g.Name, &g.Currency, &g.CreatedBy, &g.CreatedAt, &g.UnitCount); err != nil {
				return err
			}
			out = append(out, g)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, wrap("list consolidation groups", err)
	}
	return out, nil
}

// GetGroup returns a group with its units and ownership history, or CNS-0924.
func (s *Postgres) GetGroup(ctx context.Context, id string) (GroupDetail, error) {
	pr, err := principal(ctx)
	if err != nil {
		return GroupDetail{}, err
	}
	var d GroupDetail
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		d, err = loadGroup(ctx, tx, pr.TenantID, id)
		return err
	})
	if err != nil {
		return GroupDetail{}, wrap("get consolidation group", err)
	}
	return d, nil
}

func loadGroup(ctx context.Context, tx pgx.Tx, tenantID, id string) (GroupDetail, error) {
	var d GroupDetail
	err := tx.QueryRow(ctx, `select id::text, code, name, currency, created_by, created_at from consolidation_group where tenant_id = $1 and id = $2::uuid`,
		tenantID, id).Scan(&d.ID, &d.Code, &d.Name, &d.Currency, &d.CreatedBy, &d.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, errGroupNotFound(id)
	}
	if err != nil {
		return d, err
	}
	rows, err := tx.Query(ctx, `select id::text, legal_entity_id::text, ledger_id, currency from consolidation_unit
		where tenant_id = $1 and group_id = $2::uuid order by created_at, id`, tenantID, id)
	if err != nil {
		return d, err
	}
	defer rows.Close()
	index := map[string]int{}
	for rows.Next() {
		u := domain.Unit{GroupID: id}
		if err := rows.Scan(&u.ID, &u.LegalEntityID, &u.LedgerID, &u.Currency); err != nil {
			return d, err
		}
		index[u.ID] = len(d.Units)
		d.Units = append(d.Units, u)
	}
	if err := rows.Err(); err != nil {
		return d, err
	}
	rows.Close()
	hrows, err := tx.Query(ctx, `select o.unit_id::text, o.id::text, coalesce(o.holder_unit_id::text, ''), o.effective_from, o.effective_to, o.percent::text
		from consolidation_ownership o join consolidation_unit u on u.tenant_id = o.tenant_id and u.id = o.unit_id
		where o.tenant_id = $1 and u.group_id = $2::uuid order by o.effective_from, o.id`, tenantID, id)
	if err != nil {
		return d, err
	}
	defer hrows.Close()
	for hrows.Next() {
		var unitID, percent string
		var h domain.Holding
		var to *time.Time
		if err := hrows.Scan(&unitID, &h.ID, &h.HolderUnitID, &h.From, &to, &percent); err != nil {
			return d, err
		}
		if to != nil {
			h.To = *to
		}
		if h.Percent, err = decimal.NewFromString(percent); err != nil {
			return d, fmt.Errorf("store: percent %q: %w", percent, err)
		}
		i := index[unitID]
		d.Units[i].Holdings = append(d.Units[i].Holdings, h)
	}
	return d, hrows.Err()
}

// AddUnit adds a legal entity to a group. The same entity twice fails with CNS-0926.
func (s *Postgres) AddUnit(ctx context.Context, groupID string, u domain.Unit) (domain.Unit, error) {
	if err := u.Validate(); err != nil {
		return domain.Unit{}, err
	}
	pr, err := principal(ctx)
	if err != nil {
		return domain.Unit{}, err
	}
	u.ID, u.GroupID = ids.New(), groupID
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := loadGroupHeader(ctx, tx, pr.TenantID, groupID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `insert into consolidation_unit (tenant_id, id, group_id, legal_entity_id, ledger_id, currency) values ($1, $2, $3::uuid, $4::uuid, $5, $6)`,
			pr.TenantID, u.ID, groupID, u.LegalEntityID, u.LedgerID, u.Currency)
		if isUnique(err) {
			return &domain.Error{Code: domain.CodeUnitExists, Detail: "that legal entity is already a unit of the group"}
		}
		return err
	})
	if err != nil {
		return domain.Unit{}, wrap("add consolidation unit", err)
	}
	return u, nil
}

func loadGroupHeader(ctx context.Context, tx pgx.Tx, tenantID, id string) (string, error) {
	var currency string
	err := tx.QueryRow(ctx, `select currency from consolidation_group where tenant_id = $1 and id = $2::uuid`, tenantID, id).Scan(&currency)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errGroupNotFound(id)
	}
	return currency, err
}

// AddOwnership records a holding of a unit from a date, closing the holder's
// open interval on that date, and returns the unit with its history. The
// holders together never hold more than 100.00 % (CNS-0904).
func (s *Postgres) AddOwnership(ctx context.Context, groupID, unitID string, h domain.Holding) (domain.Unit, error) {
	pr, err := principal(ctx)
	if err != nil {
		return domain.Unit{}, err
	}
	var out domain.Unit
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		// The unit row serializes concurrent changes to one unit's history.
		var locked string
		err := tx.QueryRow(ctx, `select id::text from consolidation_unit where tenant_id = $1 and group_id = $2::uuid and id = $3::uuid for update`,
			pr.TenantID, groupID, unitID).Scan(&locked)
		if errors.Is(err, pgx.ErrNoRows) {
			return errGroupNotFound(unitID)
		}
		if err != nil {
			return err
		}
		if h.HolderUnitID == unitID {
			return &domain.Error{Code: domain.CodeInvalid, Detail: "a unit cannot hold itself"}
		}
		if h.HolderUnitID != "" {
			var holder string
			if err := tx.QueryRow(ctx, `select id::text from consolidation_unit where tenant_id = $1 and group_id = $2::uuid and id = $3::uuid`,
				pr.TenantID, groupID, h.HolderUnitID).Scan(&holder); errors.Is(err, pgx.ErrNoRows) {
				return &domain.Error{Code: domain.CodeInvalid, Detail: "the holder is not a unit of this group"}
			} else if err != nil {
				return err
			}
		}
		d, err := loadGroup(ctx, tx, pr.TenantID, groupID)
		if err != nil {
			return err
		}
		var unit domain.Unit
		for _, u := range d.Units {
			if u.ID == unitID {
				unit = u
			}
		}
		next, err := domain.AddHolding(unit.Holdings, h)
		if err != nil {
			return err
		}
		for _, held := range next {
			if held.ID == "" {
				continue
			}
			// An existing holding that the new one closed.
			for _, old := range unit.Holdings {
				if old.ID == held.ID && !old.To.Equal(held.To) {
					if _, err := tx.Exec(ctx, `update consolidation_ownership set effective_to = $3 where tenant_id = $1 and id = $2::uuid`, pr.TenantID, held.ID, held.To); err != nil {
						return err
					}
				}
			}
		}
		id := ids.New()
		var to any
		if !h.To.IsZero() {
			to = h.To
		}
		var holder any
		if h.HolderUnitID != "" {
			holder = h.HolderUnitID
		}
		if _, err := tx.Exec(ctx, `insert into consolidation_ownership (tenant_id, id, unit_id, holder_unit_id, effective_from, effective_to, percent)
			values ($1, $2, $3::uuid, $4::uuid, $5, $6, $7::numeric)`, pr.TenantID, id, unitID, holder, h.From, to, h.Percent.String()); err != nil {
			return err
		}
		d, err = loadGroup(ctx, tx, pr.TenantID, groupID)
		if err != nil {
			return err
		}
		for _, u := range d.Units {
			if u.ID == unitID {
				out = u
			}
		}
		return nil
	})
	if err != nil {
		return domain.Unit{}, wrap("add consolidation ownership", err)
	}
	return out, nil
}

// SetMapping replaces the group's chart mapping.
func (s *Postgres) SetMapping(ctx context.Context, groupID string, rows []domain.AccountMapping) error {
	if err := domain.ValidateMapping(rows); err != nil {
		return err
	}
	pr, err := principal(ctx)
	if err != nil {
		return err
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := loadGroupHeader(ctx, tx, pr.TenantID, groupID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `delete from consolidation_account_map where tenant_id = $1 and group_id = $2::uuid`, pr.TenantID, groupID); err != nil {
			return err
		}
		var batch pgx.Batch
		for _, r := range rows {
			batch.Queue(`insert into consolidation_account_map (tenant_id, group_id, main_account, consolidation_account, account_type) values ($1, $2::uuid, $3, $4, $5)`,
				pr.TenantID, groupID, r.MainAccount, r.ConsolidationAccount, r.Type)
		}
		res := tx.SendBatch(ctx, &batch)
		for range batch.Len() {
			if _, err := res.Exec(); err != nil {
				_ = res.Close()
				return err
			}
		}
		return res.Close()
	})
	if err != nil {
		return wrap("set consolidation mapping", err)
	}
	return nil
}

// GetMapping returns the group's chart mapping ordered by main account.
func (s *Postgres) GetMapping(ctx context.Context, groupID string) ([]domain.AccountMapping, error) {
	pr, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	out := []domain.AccountMapping{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := loadGroupHeader(ctx, tx, pr.TenantID, groupID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select main_account, consolidation_account, account_type from consolidation_account_map
			where tenant_id = $1 and group_id = $2::uuid order by main_account`, pr.TenantID, groupID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m domain.AccountMapping
			if err := rows.Scan(&m.MainAccount, &m.ConsolidationAccount, &m.Type); err != nil {
				return err
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, wrap("get consolidation mapping", err)
	}
	return out, nil
}
