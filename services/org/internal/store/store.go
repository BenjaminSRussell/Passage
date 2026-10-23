package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	MaxPageSize          = 100
	CodeLegalEntityCode  = "ORG-LE-CODE-DUPLICATE"
	CodeIdempotencyReuse = "idempotency_key_reuse"
	uniqueViolation      = "23505"
)

type LegalEntity struct {
	ID        string    `json:"id"`
	Code      string    `json:"code"`
	Name      string    `json:"name"`
	Country   string    `json:"country"`
	Currency  string    `json:"currency"`
	Status    string    `json:"status"`
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"createdAt"`
}

type LegalEntityPage struct {
	Items    []LegalEntity `json:"items"`
	NextCode string        `json:"-"`
}

type Store interface {
	CreateLegalEntity(ctx context.Context, in LegalEntity, idempotencyKey string) (LegalEntity, error)
	ListLegalEntities(ctx context.Context, afterCode string, limit int) (LegalEntityPage, error)
	Ping(ctx context.Context) error
}

type Postgres struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

const legalEntityColumns = "id, code, name, country, currency, status, version, created_at"

func scanLegalEntity(row pgx.Row) (LegalEntity, error) {
	var e LegalEntity
	err := row.Scan(&e.ID, &e.Code, &e.Name, &e.Country, &e.Currency, &e.Status, &e.Version, &e.CreatedAt)
	return e, err
}

func (s *Postgres) CreateLegalEntity(ctx context.Context, in LegalEntity, idempotencyKey string) (LegalEntity, error) {
	p, _ := tenant.FromContext(ctx)
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	var e LegalEntity
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		e, err = scanLegalEntity(tx.QueryRow(ctx, `insert into legal_entity (tenant_id, id, code, name, country, currency, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7)
			on conflict (tenant_id, idempotency_key) do update set version = legal_entity.version
			returning `+legalEntityColumns, p.TenantID, ids.New(), in.Code, in.Name, in.Country, in.Currency, key))
		return err
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return LegalEntity{}, httpx.NewProblem(http.StatusConflict, CodeLegalEntityCode, "a legal entity with this code already exists")
	}
	if err == nil && key != nil && (e.Code != in.Code || e.Name != in.Name || e.Country != in.Country || e.Currency != in.Currency) {
		return LegalEntity{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
	}
	return e, err
}

func (s *Postgres) ListLegalEntities(ctx context.Context, afterCode string, limit int) (LegalEntityPage, error) {
	p, _ := tenant.FromContext(ctx)
	page := LegalEntityPage{Items: []LegalEntity{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+legalEntityColumns+` from legal_entity where tenant_id = $1 and code > $2 order by code limit $3`, p.TenantID, afterCode, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			e, err := scanLegalEntity(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, e)
		}
		return rows.Err()
	})
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextCode = page.Items[limit-1].Code
	}
	return page, err
}
