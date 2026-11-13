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
	rows, err := tx.Query(ctx, `select id::text, legal_entity_id::text, ledger_id, currency, fiscal_year_start_month from consolidation_unit
		where tenant_id = $1 and group_id = $2::uuid order by created_at, id`, tenantID, id)
	if err != nil {
		return d, err
	}
	defer rows.Close()
	index := map[string]int{}
	for rows.Next() {
		u := domain.Unit{GroupID: id}
		if err := rows.Scan(&u.ID, &u.LegalEntityID, &u.LedgerID, &u.Currency, &u.FiscalYearStartMonth); err != nil {
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
	if u.FiscalYearStartMonth == 0 {
		u.FiscalYearStartMonth = 1
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := loadGroupHeader(ctx, tx, pr.TenantID, groupID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `insert into consolidation_unit (tenant_id, id, group_id, legal_entity_id, ledger_id, currency, fiscal_year_start_month) values ($1, $2, $3::uuid, $4::uuid, $5, $6, $7)`,
			pr.TenantID, u.ID, groupID, u.LegalEntityID, u.LedgerID, u.Currency, u.FiscalYearStartMonth)
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

// MappingVersion is one stored version of a group's chart mapping.
type MappingVersion struct {
	Version   int
	CreatedBy string
	CreatedAt time.Time
	Rows      int
}

// SetMapping stores the group's chart mapping as its next version (never replacing an older one, so a collection keeps the
// mapping it was made with) and returns the version number.
func (s *Postgres) SetMapping(ctx context.Context, groupID string, rows []domain.AccountMapping) (int, error) {
	if err := domain.ValidateMapping(rows); err != nil {
		return 0, err
	}
	tenantID, by, err := creator(ctx)
	if err != nil {
		return 0, err
	}
	var version int
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := loadGroupHeader(ctx, tx, tenantID, groupID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, tenantID+"|mapping|"+groupID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `select coalesce(max(version), 0) + 1 from consolidation_mapping_version where tenant_id = $1 and group_id = $2::uuid`, tenantID, groupID).Scan(&version); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into consolidation_mapping_version (tenant_id, group_id, version, created_by) values ($1, $2::uuid, $3, $4)`, tenantID, groupID, version, by); err != nil {
			return err
		}
		var batch pgx.Batch
		for _, r := range rows {
			batch.Queue(`insert into consolidation_account_map (tenant_id, group_id, version, main_account, consolidation_account, account_type, role) values ($1, $2::uuid, $3, $4, $5, $6, nullif($7, ''))`,
				tenantID, groupID, version, r.MainAccount, r.ConsolidationAccount, r.Type, r.Role)
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
		return 0, wrap("set consolidation mapping", err)
	}
	return version, nil
}

// GetMapping returns one version of the group's chart mapping ordered by main account; version 0 is the latest. The version
// returned comes with it (0 when the group has no mapping yet).
func (s *Postgres) GetMapping(ctx context.Context, groupID string, version int) ([]domain.AccountMapping, int, error) {
	pr, err := principal(ctx)
	if err != nil {
		return nil, 0, err
	}
	out := []domain.AccountMapping{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := loadGroupHeader(ctx, tx, pr.TenantID, groupID); err != nil {
			return err
		}
		if version == 0 {
			if err := tx.QueryRow(ctx, `select coalesce(max(version), 0) from consolidation_mapping_version where tenant_id = $1 and group_id = $2::uuid`, pr.TenantID, groupID).Scan(&version); err != nil {
				return err
			}
		}
		rows, err := tx.Query(ctx, `select main_account, consolidation_account, account_type, coalesce(role, '') from consolidation_account_map
			where tenant_id = $1 and group_id = $2::uuid and version = $3 order by main_account`, pr.TenantID, groupID, version)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m domain.AccountMapping
			if err := rows.Scan(&m.MainAccount, &m.ConsolidationAccount, &m.Type, &m.Role); err != nil {
				return err
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, 0, wrap("get consolidation mapping", err)
	}
	return out, version, nil
}

// ListMappingVersions lists the versions of the group's mapping, newest first.
func (s *Postgres) ListMappingVersions(ctx context.Context, groupID string) ([]MappingVersion, error) {
	pr, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	out := []MappingVersion{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := loadGroupHeader(ctx, tx, pr.TenantID, groupID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select v.version, v.created_by, v.created_at, (select count(*) from consolidation_account_map m
				where m.tenant_id = v.tenant_id and m.group_id = v.group_id and m.version = v.version)
			from consolidation_mapping_version v where v.tenant_id = $1 and v.group_id = $2::uuid order by v.version desc`, pr.TenantID, groupID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v MappingVersion
			if err := rows.Scan(&v.Version, &v.CreatedBy, &v.CreatedAt, &v.Rows); err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, wrap("list consolidation mapping versions", err)
	}
	return out, nil
}

// UnitChange is what an edit of a unit may change; a nil field is left as it is.
type UnitChange struct {
	LedgerID             *string
	Currency             *string
	FiscalYearStartMonth *int
}

// UpdateUnit changes the ledger a unit is collected from, its currency or its fiscal year. A unit that collections were made
// from keeps its currency (CNS-0931): its stored balances are in it.
func (s *Postgres) UpdateUnit(ctx context.Context, groupID, unitID string, c UnitChange) (domain.Unit, error) {
	pr, err := principal(ctx)
	if err != nil {
		return domain.Unit{}, err
	}
	var out domain.Unit
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		out = domain.Unit{ID: unitID, GroupID: groupID}
		err := tx.QueryRow(ctx, `select legal_entity_id::text, ledger_id, currency, fiscal_year_start_month from consolidation_unit
			where tenant_id = $1 and group_id = $2::uuid and id = $3::uuid for update`, pr.TenantID, groupID, unitID).Scan(&out.LegalEntityID, &out.LedgerID, &out.Currency, &out.FiscalYearStartMonth)
		if errors.Is(err, pgx.ErrNoRows) {
			return &domain.Error{Code: domain.CodeGroupNotFound, Detail: "no unit " + unitID + " in group " + groupID}
		}
		if err != nil {
			return err
		}
		if c.LedgerID != nil {
			out.LedgerID = *c.LedgerID
		}
		if c.FiscalYearStartMonth != nil {
			out.FiscalYearStartMonth = *c.FiscalYearStartMonth
		}
		if c.Currency != nil && *c.Currency != out.Currency {
			var used bool
			if err := tx.QueryRow(ctx, `select exists(select 1 from consolidation_collection_line where tenant_id = $1 and unit_id = $2::uuid)`, pr.TenantID, unitID).Scan(&used); err != nil {
				return err
			}
			if used {
				return &domain.Error{Code: domain.CodeUnitInUse, Detail: "collections were made from this unit in " + out.Currency + "; its currency can no longer change"}
			}
			out.Currency = *c.Currency
		}
		if err := out.Validate(); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `update consolidation_unit set ledger_id = $4, currency = $5, fiscal_year_start_month = $6 where tenant_id = $1 and group_id = $2::uuid and id = $3::uuid`,
			pr.TenantID, groupID, unitID, out.LedgerID, out.Currency, out.FiscalYearStartMonth)
		return err
	})
	if err != nil {
		return domain.Unit{}, wrap("update consolidation unit", err)
	}
	return out, nil
}

// RemoveUnit removes a unit from a group with its ownership history and terms. A unit that collections were made from, or that
// holds a share of another unit, stays (CNS-0931): the stored figures and the structure refer to it.
func (s *Postgres) RemoveUnit(ctx context.Context, groupID, unitID string) error {
	pr, err := principal(ctx)
	if err != nil {
		return err
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var locked string
		err := tx.QueryRow(ctx, `select id::text from consolidation_unit where tenant_id = $1 and group_id = $2::uuid and id = $3::uuid for update`, pr.TenantID, groupID, unitID).Scan(&locked)
		if errors.Is(err, pgx.ErrNoRows) {
			return &domain.Error{Code: domain.CodeGroupNotFound, Detail: "no unit " + unitID + " in group " + groupID}
		}
		if err != nil {
			return err
		}
		var collected, holds bool
		if err := tx.QueryRow(ctx, `select exists(select 1 from consolidation_collection_line where tenant_id = $1 and unit_id = $2::uuid),
			exists(select 1 from consolidation_ownership where tenant_id = $1 and holder_unit_id = $2::uuid)`, pr.TenantID, unitID).Scan(&collected, &holds); err != nil {
			return err
		}
		switch {
		case collected:
			return &domain.Error{Code: domain.CodeUnitInUse, Detail: "collections were made from this unit, so it stays in the group; end its ownership instead"}
		case holds:
			return &domain.Error{Code: domain.CodeUnitInUse, Detail: "this unit holds a share of another unit; change that ownership first"}
		}
		for _, q := range []string{
			`delete from consolidation_ownership where tenant_id = $1 and unit_id = $2::uuid`,
			`delete from consolidation_unit_terms where tenant_id = $1 and unit_id = $2::uuid`,
			`delete from consolidation_unit where tenant_id = $1 and id = $2::uuid`,
		} {
			if _, err := tx.Exec(ctx, q, pr.TenantID, unitID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return wrap("remove consolidation unit", err)
	}
	return nil
}

// UnitTermsRecord is a unit's stored acquisition terms.
type UnitTermsRecord struct {
	UnitID string
	domain.UnitTerms
	UpdatedBy string
	UpdatedAt time.Time
}

// SetUnitTerms stores (replaces) the acquisition terms of a unit.
func (s *Postgres) SetUnitTerms(ctx context.Context, groupID, unitID string, t domain.UnitTerms) (UnitTermsRecord, error) {
	tenantID, by, err := creator(ctx)
	if err != nil {
		return UnitTermsRecord{}, err
	}
	rec := UnitTermsRecord{UnitID: unitID, UnitTerms: t, UpdatedBy: by}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var one int
		if err := tx.QueryRow(ctx, `select 1 from consolidation_unit where tenant_id = $1 and group_id = $2::uuid and id = $3::uuid`, tenantID, groupID, unitID).Scan(&one); errors.Is(err, pgx.ErrNoRows) {
			return &domain.Error{Code: domain.CodeGroupNotFound, Detail: "no unit " + unitID + " in group " + groupID}
		} else if err != nil {
			return err
		}
		method := t.Method
		if method == "" {
			method = domain.MethodFull
		}
		return tx.QueryRow(ctx, `insert into consolidation_unit_terms (tenant_id, unit_id, investment_cost, share_capital, pre_acquisition_re, historical_rate, seller_is_subsidiary, ic_net_investment, fx_gain_loss_account, updated_by, method,
				ic_receivable_account, parent_payable_account)
			values ($1, $2::uuid, $3::numeric, $4::numeric, $5::numeric, $6::numeric, $7, $8, nullif($9, ''), $10, $11, nullif($12, ''), nullif($13, ''))
			on conflict (tenant_id, unit_id) do update set investment_cost = excluded.investment_cost, share_capital = excluded.share_capital, pre_acquisition_re = excluded.pre_acquisition_re,
				historical_rate = excluded.historical_rate, seller_is_subsidiary = excluded.seller_is_subsidiary, ic_net_investment = excluded.ic_net_investment,
				fx_gain_loss_account = excluded.fx_gain_loss_account, method = excluded.method, ic_receivable_account = excluded.ic_receivable_account,
				parent_payable_account = excluded.parent_payable_account, updated_by = excluded.updated_by, updated_at = now() returning updated_at`,
			tenantID, unitID, t.InvestmentCost.String(), t.ShareCapital.String(), t.PreAcquisitionRE.String(), t.HistoricalRate.String(), t.SellerIsSubsidiary, t.ICNetInvestment, t.FXGainLossAccount, by, method,
			t.ICReceivableAccount, t.ParentPayableAccount).Scan(&rec.UpdatedAt)
	})
	if err != nil {
		return UnitTermsRecord{}, wrap("set consolidation unit terms", err)
	}
	return rec, nil
}

// GetUnitTerms returns the stored terms of every unit of a group, by unit id.
func (s *Postgres) GetUnitTerms(ctx context.Context, groupID string) (map[string]UnitTermsRecord, error) {
	pr, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]UnitTermsRecord{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := loadGroupHeader(ctx, tx, pr.TenantID, groupID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select t.unit_id::text, t.investment_cost::text, t.share_capital::text, t.pre_acquisition_re::text, t.historical_rate::text,
				t.seller_is_subsidiary, t.ic_net_investment, coalesce(t.fx_gain_loss_account, ''), t.updated_by, t.updated_at, t.method,
				coalesce(t.ic_receivable_account, ''), coalesce(t.parent_payable_account, '')
			from consolidation_unit_terms t join consolidation_unit u on u.tenant_id = t.tenant_id and u.id = t.unit_id
			where t.tenant_id = $1 and u.group_id = $2::uuid`, pr.TenantID, groupID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r UnitTermsRecord
			var inv, capital, re, rate string
			if err := rows.Scan(&r.UnitID, &inv, &capital, &re, &rate, &r.SellerIsSubsidiary, &r.ICNetInvestment, &r.FXGainLossAccount, &r.UpdatedBy, &r.UpdatedAt, &r.Method,
				&r.ICReceivableAccount, &r.ParentPayableAccount); err != nil {
				return err
			}
			for _, f := range []struct {
				dst *decimal.Decimal
				raw string
			}{{&r.InvestmentCost, inv}, {&r.ShareCapital, capital}, {&r.PreAcquisitionRE, re}, {&r.HistoricalRate, rate}} {
				if *f.dst, err = decimal.NewFromString(f.raw); err != nil {
					return fmt.Errorf("store: terms amount %q: %w", f.raw, err)
				}
			}
			out[r.UnitID] = r
		}
		return rows.Err()
	})
	if err != nil {
		return nil, wrap("get consolidation unit terms", err)
	}
	return out, nil
}
