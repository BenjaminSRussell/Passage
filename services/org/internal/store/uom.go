package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/org/internal/domain"
)

const (
	CodeUnitNotFound      = "ORG-UOM-UNIT-NOT-FOUND"
	CodeUnitCodeDuplicate = "ORG-UOM-CODE-DUPLICATE"
	KindUnit              = "uom"

	CodeUnitInactive        = "ORG-UOM-UNIT-INACTIVE"
	CodeClassMismatch       = "ORG-UOM-CLASS-MISMATCH"
	CodeConversionDuplicate = "ORG-UOM-CONVERSION-DUPLICATE"

	visibleUnits = `select u.code, u.name, u.class, u.decimal_precision, u.system_flag, coalesce(s.status, u.status) as status
		from unit_of_measure u
		left join reference_status s on s.tenant_id = $1 and s.kind = 'uom' and s.code = u.code
		where u.tenant_id in ($1, $2)`
)

type Unit struct {
	Code             string `json:"code"`
	Name             string `json:"name"`
	Class            string `json:"class"`
	DecimalPrecision int    `json:"decimalPrecision"`
	System           bool   `json:"system"`
	Status           string `json:"status"`
}

type UnitFilter struct{ Class, Status string }

func UnitClasses() []string { return slices.Clone(unitClasses) }

func ValidUnitCode(code string) bool { return unitCode.MatchString(code) }

func unitNotFound(code string) error {
	return httpx.NewProblem(http.StatusNotFound, CodeUnitNotFound, "no unit with code "+code)
}

func queryUnits(ctx context.Context, q querier, tenantID, code string, f UnitFilter) ([]Unit, error) {
	rows, err := q.Query(ctx, `select v.code, v.name, v.class, v.decimal_precision, v.system_flag, v.status from (`+visibleUnits+`) v
		where ($3::text = '' or v.code = $3) and ($4::text = '' or v.class = $4) and ($5::text = '' or v.status = $5) order by v.code`,
		tenantID, SysTenantID, code, f.Class, f.Status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	units := []Unit{}
	for rows.Next() {
		var u Unit
		if err := rows.Scan(&u.Code, &u.Name, &u.Class, &u.DecimalPrecision, &u.System, &u.Status); err != nil {
			return nil, err
		}
		units = append(units, u)
	}
	return units, rows.Err()
}

func oneUnit(ctx context.Context, q querier, tenantID, code string) (Unit, error) {
	units, err := queryUnits(ctx, q, tenantID, code, UnitFilter{})
	if err != nil {
		return Unit{}, err
	}
	if len(units) == 0 {
		return Unit{}, unitNotFound(code)
	}
	return units[0], nil
}

func (s *Postgres) ListUnits(ctx context.Context, f UnitFilter) ([]Unit, error) {
	p, _ := tenant.FromContext(ctx)
	var units []Unit
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		units, err = queryUnits(ctx, tx, p.TenantID, "", f)
		return err
	})
	return units, err
}

func (s *Postgres) GetUnit(ctx context.Context, code string) (Unit, error) {
	p, _ := tenant.FromContext(ctx)
	var unit Unit
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		unit, err = oneUnit(ctx, tx, p.TenantID, code)
		return err
	})
	return unit, err
}

func (s *Postgres) CreateUnit(ctx context.Context, in Unit, idempotencyKey string) (Unit, error) {
	p, _ := tenant.FromContext(ctx)
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	duplicate := httpx.NewProblem(http.StatusConflict, CodeUnitCodeDuplicate, "a unit with code "+in.Code+" already exists")
	var unit Unit
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var taken bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from unit_of_measure where tenant_id = $1 and code = $2)`, SysTenantID, in.Code).Scan(&taken); err != nil {
			return err
		}
		if taken {
			return duplicate
		}
		var code string
		if err := tx.QueryRow(ctx, `insert into unit_of_measure (tenant_id, code, name, class, decimal_precision, idempotency_key)
			values ($1, $2, $3, $4, $5, $6)
			on conflict (tenant_id, idempotency_key) where idempotency_key is not null do update set name = unit_of_measure.name
			returning code`, p.TenantID, in.Code, in.Name, in.Class, in.DecimalPrecision, key).Scan(&code); err != nil {
			return err
		}
		var err error
		unit, err = oneUnit(ctx, tx, p.TenantID, code)
		return err
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return Unit{}, duplicate
	}
	if err == nil && key != nil && (unit.Code != in.Code || unit.Name != in.Name || unit.Class != in.Class || unit.DecimalPrecision != in.DecimalPrecision) {
		return Unit{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
	}
	return unit, err
}

func (s *Postgres) SetUnitStatus(ctx context.Context, code, status string) (Unit, error) {
	p, _ := tenant.FromContext(ctx)
	var unit Unit
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := oneUnit(ctx, tx, p.TenantID, code); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into reference_status (tenant_id, kind, code, status) values ($1, $2, $3, $4)
			on conflict (tenant_id, kind, code) do update set status = excluded.status, updated_at = now()`, p.TenantID, KindUnit, code, status); err != nil {
			return err
		}
		var err error
		unit, err = oneUnit(ctx, tx, p.TenantID, code)
		return err
	})
	return unit, err
}

func (s *Postgres) ConversionGraph(ctx context.Context) ([]domain.Conversion, map[string]int32, error) {
	p, _ := tenant.FromContext(ctx)
	var convs []domain.Conversion
	precision := map[string]int32{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `with vu as (`+visibleUnits+`)
			select c.from_code, c.to_code, f.class, c.factor, c.offset_value from uom_conversion c
			join vu f on f.code = c.from_code and f.status = 'active'
			join vu t on t.code = c.to_code and t.status = 'active'
			where c.tenant_id in ($1, $2) and c.product_id is null order by c.from_code, c.to_code`, p.TenantID, SysTenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c domain.Conversion
			if err := rows.Scan(&c.From, &c.To, &c.Class, &c.Factor, &c.Offset); err != nil {
				return err
			}
			convs = append(convs, c)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		units, err := queryUnits(ctx, tx, p.TenantID, "", UnitFilter{Status: "active"})
		if err != nil {
			return err
		}
		for _, u := range units {
			precision[u.Code] = int32(u.DecimalPrecision)
		}
		return nil
	})
	return convs, precision, err
}

type Conversion struct {
	ID       string
	FromCode string
	ToCode   string
	Class    string
	Factor   decimal.Decimal
	Offset   decimal.Decimal
	System   bool
	Status   string
}

type ConversionFilter struct{ Class, Unit, Status, ID string }

func queryConversions(ctx context.Context, q querier, tenantID string, f ConversionFilter) ([]Conversion, error) {
	rows, err := q.Query(ctx, `with vu as (`+visibleUnits+`)
		select x.* from (
			select c.id::text, c.from_code, c.to_code, f.class, c.factor, c.offset_value, c.tenant_id = $2 as system,
				case when f.status = 'active' and t.status = 'active' then 'active' else 'inactive' end as status
			from uom_conversion c
			join vu f on f.code = c.from_code
			join vu t on t.code = c.to_code
			where c.tenant_id in ($1, $2) and c.product_id is null
				and ($3::text = '' or f.class = $3) and ($4::text = '' or c.from_code = $4 or c.to_code = $4) and ($6::text = '' or c.id::text = $6)
		) x where $5::text = '' or x.status = $5 order by x.from_code, x.to_code`,
		tenantID, SysTenantID, f.Class, f.Unit, f.Status, f.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	convs := []Conversion{}
	for rows.Next() {
		var c Conversion
		if err := rows.Scan(&c.ID, &c.FromCode, &c.ToCode, &c.Class, &c.Factor, &c.Offset, &c.System, &c.Status); err != nil {
			return nil, err
		}
		convs = append(convs, c)
	}
	return convs, rows.Err()
}

func (s *Postgres) ListConversions(ctx context.Context, f ConversionFilter) ([]Conversion, error) {
	p, _ := tenant.FromContext(ctx)
	var convs []Conversion
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		convs, err = queryConversions(ctx, tx, p.TenantID, f)
		return err
	})
	return convs, err
}

func inactiveUnit(u Unit, field string) error {
	problem := httpx.NewProblem(http.StatusUnprocessableEntity, CodeUnitInactive, fmt.Sprintf("unit %s is inactive", u.Code))
	problem.Errors = map[string]string{field: "must be an active unit"}
	return problem
}

func sameConversion(a, b Conversion) bool {
	return a.FromCode == b.FromCode && a.ToCode == b.ToCode && a.Factor.Equal(b.Factor) && a.Offset.Equal(b.Offset)
}

func (s *Postgres) CreateConversion(ctx context.Context, in Conversion, idempotencyKey string) (Conversion, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	duplicate := httpx.NewProblem(http.StatusConflict, CodeConversionDuplicate, "a conversion between "+in.FromCode+" and "+in.ToCode+" already exists")
	reuse := httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
	var out Conversion
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		replay := func(id string) error {
			convs, err := queryConversions(ctx, tx, p.TenantID, ConversionFilter{ID: id})
			if err != nil {
				return err
			}
			if len(convs) != 1 || !sameConversion(convs[0], in) {
				return reuse
			}
			out = convs[0]
			return nil
		}
		if key != nil {
			var id string
			err := tx.QueryRow(ctx, `select id::text from uom_conversion where tenant_id = $1 and idempotency_key = $2`, p.TenantID, *key).Scan(&id)
			if err == nil {
				return replay(id)
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		from, err := oneUnit(ctx, tx, p.TenantID, in.FromCode)
		if err != nil {
			return err
		}
		to, err := oneUnit(ctx, tx, p.TenantID, in.ToCode)
		if err != nil {
			return err
		}
		switch {
		case from.Status != "active":
			return inactiveUnit(from, "fromCode")
		case to.Status != "active":
			return inactiveUnit(to, "toCode")
		case from.Class != to.Class:
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeClassMismatch, fmt.Sprintf("%s is %s and %s is %s", from.Code, from.Class, to.Code, to.Class))
		}
		var taken bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from uom_conversion where tenant_id in ($1, $2) and product_id is null
			and least(from_code, to_code) = least($3::text, $4::text) and greatest(from_code, to_code) = greatest($3::text, $4::text))`,
			p.TenantID, SysTenantID, in.FromCode, in.ToCode).Scan(&taken); err != nil {
			return err
		}
		if taken {
			return duplicate
		}
		var id string
		var inserted bool
		if err := tx.QueryRow(ctx, `insert into uom_conversion (tenant_id, from_code, to_code, factor, offset_value, idempotency_key)
			values ($1, $2, $3, $4, $5, $6)
			on conflict (tenant_id, idempotency_key) where idempotency_key is not null do update set id = uom_conversion.id
			returning id::text, xmax = 0`, p.TenantID, in.FromCode, in.ToCode, in.Factor.String(), in.Offset.String(), key).Scan(&id, &inserted); err != nil {
			return err
		}
		created = inserted
		return replay(id)
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return Conversion{}, false, duplicate
	}
	return out, created, err
}
