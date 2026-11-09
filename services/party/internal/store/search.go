package store

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

type SearchKind string

const (
	SearchParties   SearchKind = "party"
	SearchVendors   SearchKind = "vendor"
	SearchCustomers SearchKind = "customer"
	MaxSearchPage              = 50
)

type SearchHit struct {
	ID            string `json:"id"`
	PartyID       string `json:"partyId"`
	PartyNumber   string `json:"partyNumber"`
	Name          string `json:"name"`
	AccountNumber string `json:"accountNumber,omitempty"`
	Blocked       string `json:"blocked,omitempty"`
}

type SearchKey struct{ Name, ID string }

type SearchPage struct {
	Items []SearchHit
	Next  *SearchKey
}

type SearchStore interface {
	Search(ctx context.Context, kind SearchKind, query string, after SearchKey, limit int) (SearchPage, error)
}

func searchSQL(kind SearchKind) string {
	head, extraMatch, idColumn := `select p.id::text, p.id::text, p.party_number, p.name, '', '' from party p where p.tenant_id = $1`, "", "p.id"
	switch kind {
	case SearchVendors, SearchCustomers:
		table := string(kind) + "_role"
		head = `select r.id::text, p.id::text, p.party_number, p.name, r.account_number, r.blocked
			from ` + table + ` r join party p on p.tenant_id = r.tenant_id and p.id = r.party_id where r.tenant_id = $1`
		extraMatch, idColumn = " or r.account_number ilike $2", "r.id"
	}
	return head + ` and p.status <> 'merged' and ($2::text = '' or p.name ilike $2 or p.party_number ilike $2` + extraMatch + `)
		and (p.name, ` + idColumn + `) > ($3, $4::uuid) order by p.name, ` + idColumn + ` limit $5`
}

func searchPattern(q string) string {
	q = strings.TrimSpace(q)
	if q == "" {
		return ""
	}
	return "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(q) + "%"
}

func (s *Postgres) Search(ctx context.Context, kind SearchKind, query string, after SearchKey, limit int) (SearchPage, error) {
	caller, _ := tenant.FromContext(ctx)
	if after.ID == "" {
		after.ID = "00000000-0000-0000-0000-000000000000"
	}
	if limit < 1 || limit > MaxSearchPage {
		limit = MaxSearchPage
	}
	page := SearchPage{Items: []SearchHit{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, searchSQL(kind), caller.TenantID, searchPattern(query), after.Name, after.ID, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var hit SearchHit
			if err := rows.Scan(&hit.ID, &hit.PartyID, &hit.PartyNumber, &hit.Name, &hit.AccountNumber, &hit.Blocked); err != nil {
				return err
			}
			page.Items = append(page.Items, hit)
		}
		return rows.Err()
	})
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		last := page.Items[limit-1]
		page.Next = &SearchKey{Name: last.Name, ID: last.ID}
	}
	return page, err
}
