package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const CodeReferenceNotFound = "ORG-REF-NOT-FOUND"

type ReferenceKind string

const (
	KindCountry  ReferenceKind = "country"
	KindCurrency ReferenceKind = "currency"
	KindRegion   ReferenceKind = "region"
	KindLanguage ReferenceKind = "language"
)

type ReferenceItem struct {
	Code           string  `json:"code"`
	Name           string  `json:"name"`
	Status         string  `json:"status"`
	RegionRequired *bool   `json:"regionRequired,omitempty"`
	PostalRegex    *string `json:"postalRegex,omitempty"`
	NumericCode    string  `json:"numericCode,omitempty"`
	MinorUnits     *int    `json:"minorUnits,omitempty"`
	CountryCode    string  `json:"countryCode,omitempty"`
}

type ReferenceFilter struct {
	Country string
	Status  string
}

type kindSpec struct {
	table   string
	columns string
	targets func(*ReferenceItem) []any
}

var referenceKinds = map[ReferenceKind]kindSpec{
	KindCountry:  {"country", ", b.region_required, b.postal_regex", func(i *ReferenceItem) []any { return []any{&i.RegionRequired, &i.PostalRegex} }},
	KindCurrency: {"currency", ", b.numeric_code, b.minor_units", func(i *ReferenceItem) []any { return []any{&i.NumericCode, &i.MinorUnits} }},
	KindRegion:   {"region", ", b.country_code", func(i *ReferenceItem) []any { return []any{&i.CountryCode} }},
	KindLanguage: {"language", "", func(*ReferenceItem) []any { return nil }},
}

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func queryReference(ctx context.Context, q querier, kind ReferenceKind, tenantID, code string, f ReferenceFilter) ([]ReferenceItem, error) {
	spec, ok := referenceKinds[kind]
	if !ok {
		return nil, fmt.Errorf("unknown reference kind %q", kind)
	}
	args := []any{tenantID, string(kind), SysTenantID}
	query := `select b.code, b.name, coalesce(s.status, b.status)` + spec.columns + ` from ` + spec.table + ` b
		left join reference_status s on s.tenant_id = $1 and s.kind = $2 and s.code = b.code
		where b.tenant_id = $3`
	for _, c := range []struct{ column, value string }{{"b.code", code}, {"b.country_code", f.Country}, {"coalesce(s.status, b.status)", f.Status}} {
		if c.value == "" || (c.column == "b.country_code" && kind != KindRegion) {
			continue
		}
		args = append(args, c.value)
		query += fmt.Sprintf(" and %s = $%d", c.column, len(args))
	}
	rows, err := q.Query(ctx, query+" order by b.code", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []ReferenceItem{}
	for rows.Next() {
		var item ReferenceItem
		if err := rows.Scan(append([]any{&item.Code, &item.Name, &item.Status}, spec.targets(&item)...)...); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func referenceNotFound(kind ReferenceKind, code string) error {
	return httpx.NewProblem(http.StatusNotFound, CodeReferenceNotFound, fmt.Sprintf("no %s with code %s", kind, code))
}

func (s *Postgres) ListReference(ctx context.Context, kind ReferenceKind, f ReferenceFilter) ([]ReferenceItem, error) {
	p, _ := tenant.FromContext(ctx)
	var items []ReferenceItem
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		items, err = queryReference(ctx, tx, kind, p.TenantID, "", f)
		return err
	})
	return items, err
}

func (s *Postgres) GetReference(ctx context.Context, kind ReferenceKind, code string) (ReferenceItem, error) {
	p, _ := tenant.FromContext(ctx)
	var items []ReferenceItem
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		items, err = queryReference(ctx, tx, kind, p.TenantID, code, ReferenceFilter{})
		return err
	})
	if err != nil {
		return ReferenceItem{}, err
	}
	if len(items) == 0 {
		return ReferenceItem{}, referenceNotFound(kind, code)
	}
	return items[0], nil
}

func (s *Postgres) SetReferenceStatus(ctx context.Context, kind ReferenceKind, code, status string) (ReferenceItem, error) {
	p, _ := tenant.FromContext(ctx)
	var item ReferenceItem
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		spec, ok := referenceKinds[kind]
		if !ok {
			return fmt.Errorf("unknown reference kind %q", kind)
		}
		var exists bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from `+spec.table+` where tenant_id = $1 and code = $2)`, SysTenantID, code).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return referenceNotFound(kind, code)
		}
		if _, err := tx.Exec(ctx, `insert into reference_status (tenant_id, kind, code, status) values ($1, $2, $3, $4)
			on conflict (tenant_id, kind, code) do update set status = excluded.status, updated_at = now()`, p.TenantID, string(kind), code, status); err != nil {
			return err
		}
		items, err := queryReference(ctx, tx, kind, p.TenantID, code, ReferenceFilter{})
		if err != nil {
			return err
		}
		if len(items) == 0 {
			return errors.New("reference row vanished within transaction")
		}
		item = items[0]
		return nil
	})
	return item, err
}
