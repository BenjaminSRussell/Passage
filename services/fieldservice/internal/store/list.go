package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
)

// MaxProfiles bounds the SLA profile list; a tenant keeps a handful of profiles.
const MaxProfiles = 500

// Page is one page of a list, newest first. NextCursor is the id of the last
// row and is set only when more rows follow.
type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"nextCursor,omitempty"`
}

// ListFilter narrows a list. Empty strings match everything. Before is the
// NextCursor of the previous page.
type ListFilter struct {
	Status    string
	SLAState  string
	Customer  string
	Territory string
	CaseID    string
	Before    string
	Limit     int
}

// ListSLAProfiles returns the tenant's SLA profiles ordered by code.
func (s *Postgres) ListSLAProfiles(ctx context.Context) ([]SLAProfile, error) {
	tid := tenantOf(ctx)
	out := []SLAProfile{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+profileColumns+` from service_sla_profile where tenant_id = $1 order by code limit $2`, tid, MaxProfiles)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			p, err := scanProfile(rows)
			if err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	return out, err
}

// keyset is the "older than the cursor row" condition shared by the lists.
func keyset(alias, table string, arg int) string {
	return fmt.Sprintf(`($%[3]d = '' or (%[1]s.created_at, %[1]s.id) < (select c.created_at, c.id from %[2]s c where c.tenant_id = $1 and c.id::text = $%[3]d))`,
		alias, table, arg)
}

// listPage runs query (whose last placeholder is the limit) and trims the
// look-ahead row into NextCursor.
func listPage[T any](ctx context.Context, s *Postgres, limit int, idOf func(T) string, scan func(pgx.Row) (T, error), query string, args ...any) (Page[T], error) {
	page := Page[T]{Items: []T{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, query, append(args, limit+1)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			item, err := scan(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, item)
		}
		return rows.Err()
	})
	if err != nil {
		return Page[T]{}, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextCursor = idOf(page.Items[limit-1])
	}
	return page, nil
}

// ListAgreements returns agreements newest first, inside the caller's legal-entity scope.
func (s *Postgres) ListAgreements(ctx context.Context, f ListFilter) (Page[Agreement], error) {
	return listPage(ctx, s, f.Limit, func(a Agreement) string { return a.ID }, scanAgreement,
		`select `+agreementColumns+` from service_agreement a
		where a.tenant_id = $1 and ($2 = '' or a.status = $2) and ($3 = '' or a.customer_party_id::text = $3) and `+keyset("a", "service_agreement", 4)+`
		order by a.created_at desc, a.id desc limit $5`,
		tenantOf(ctx), f.Status, f.Customer, f.Before)
}

// ListCases returns cases newest first, inside the caller's legal-entity scope.
func (s *Postgres) ListCases(ctx context.Context, f ListFilter) (Page[Case], error) {
	return listPage(ctx, s, f.Limit, func(c Case) string { return c.ID }, scanCase,
		`select `+caseColumns+` from service_case x
		where x.tenant_id = $1 and ($2 = '' or x.status = $2) and ($3 = '' or x.sla_state = $3) and ($4 = '' or x.customer_party_id::text = $4)
			and `+keyset("x", "service_case", 5)+`
		order by x.created_at desc, x.id desc limit $6`,
		tenantOf(ctx), f.Status, f.SLAState, f.Customer, f.Before)
}

// ListWorkOrders returns work orders newest first, inside the caller's legal-entity scope.
func (s *Postgres) ListWorkOrders(ctx context.Context, f ListFilter) (Page[WorkOrder], error) {
	return listPage(ctx, s, f.Limit, func(w WorkOrder) string { return w.ID }, scanWorkOrder,
		`select `+workOrderColumns+` from service_work_order x
		where x.tenant_id = $1 and ($2 = '' or x.status = $2) and ($3 = '' or x.territory_id::text = $3) and ($4 = '' or x.case_id::text = $4)
			and ($5 = '' or x.customer_party_id::text = $5) and `+keyset("x", "service_work_order", 6)+`
		order by x.created_at desc, x.id desc limit $7`,
		tenantOf(ctx), f.Status, f.Territory, f.CaseID, f.Customer, f.Before)
}
