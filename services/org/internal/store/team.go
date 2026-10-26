package store

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	CodeTeamTypeCodeDuplicate = "ORG-TEAM-TYPE-CODE-DUPLICATE"
	CodeTeamTypeNotFound      = "ORG-TEAM-TYPE-NOT-FOUND"
	teamTypeColumns           = "id, code, name, member_kinds, created_at, updated_at"
)

type TeamType struct {
	ID          string    `json:"id"`
	Code        string    `json:"code"`
	Name        string    `json:"name"`
	MemberKinds []string  `json:"memberKinds"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type TeamTypeFilter struct{ MemberKind string }

type TeamTypePage struct {
	Items    []TeamType
	NextCode string `json:"-"`
}

func scanTeamType(row pgx.Row, extra ...any) (TeamType, error) {
	var t TeamType
	err := row.Scan(append([]any{&t.ID, &t.Code, &t.Name, &t.MemberKinds, &t.CreatedAt, &t.UpdatedAt}, extra...)...)
	return t, err
}

func (s *Postgres) CreateTeamType(ctx context.Context, in TeamType, idempotencyKey string) (TeamType, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	var t TeamType
	var inserted bool
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		if key != nil {
			replay, err := scanTeamType(tx.QueryRow(ctx, `select `+teamTypeColumns+` from team_type where tenant_id = $1 and idempotency_key = $2`, p.TenantID, *key))
			if err == nil {
				t = replay
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		t, err = scanTeamType(tx.QueryRow(ctx, `insert into team_type (tenant_id, id, code, name, member_kinds, idempotency_key)
			values ($1, $2, $3, $4, $5, $6)
			on conflict (tenant_id, idempotency_key) do update set updated_at = team_type.updated_at
			returning `+teamTypeColumns+`, (xmax = 0)`, p.TenantID, ids.New(), in.Code, in.Name, in.MemberKinds, key), &inserted)
		return err
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return TeamType{}, false, httpx.NewProblem(http.StatusConflict, CodeTeamTypeCodeDuplicate, "a team type with this code already exists")
	}
	if err == nil && key != nil && (t.Code != in.Code || t.Name != in.Name || !slices.Equal(t.MemberKinds, in.MemberKinds)) {
		return TeamType{}, false, idempotencyReuse()
	}
	return t, inserted, err
}

func (s *Postgres) GetTeamType(ctx context.Context, id string) (TeamType, error) {
	p, _ := tenant.FromContext(ctx)
	var t TeamType
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		t, err = scanTeamType(tx.QueryRow(ctx, `select `+teamTypeColumns+` from team_type where tenant_id = $1 and id = $2`, p.TenantID, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return TeamType{}, httpx.NewProblem(http.StatusNotFound, CodeTeamTypeNotFound, "team type not found")
	}
	return t, err
}

func (s *Postgres) ListTeamTypes(ctx context.Context, f TeamTypeFilter, afterCode string, limit int) (TeamTypePage, error) {
	p, _ := tenant.FromContext(ctx)
	page := TeamTypePage{Items: []TeamType{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+teamTypeColumns+` from team_type where tenant_id = $1 and code > $2
			and ($3::text = '' or $3 = any(member_kinds)) order by code limit $4`, p.TenantID, afterCode, f.MemberKind, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			t, err := scanTeamType(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, t)
		}
		return rows.Err()
	})
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextCode = page.Items[limit-1].Code
	}
	return page, err
}
