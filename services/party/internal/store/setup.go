package store

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
)

// Party setup records a customer role names (#500): payment terms, posting
// profiles and customer groups. Payment terms and posting profiles are read
// here (they are loaded with the master data); customer groups are also
// registered here, like vendor groups.
const (
	CodePaymentTermNotFound    = "PTY-PAYMENT-TERM-NOT-FOUND"
	CodePostingProfileNotFound = "PTY-POSTING-PROFILE-NOT-FOUND"
	CodeCustomerGroupNotFound  = "PTY-CUSTOMER-GROUP-NOT-FOUND"
	CodeCustomerGroupInvalid   = "PTY-CUSTOMER-GROUP-INVALID"
	CodeCustomerGroupExists    = "PTY-CUSTOMER-GROUP-EXISTS"

	paymentTermColumns    = `id::text, code, name, net_days, day_of_month, offset_months, version`
	postingProfileColumns = `id::text, legal_entity_id::text, code, name, version`
	customerGroupColumns  = `id::text, code, name, version`
	customerGroupKey      = "customer-group:"
)

// PaymentTerm is a payment term as the lists show it: net days or a day of the
// month, with the months it is offset by.
type PaymentTerm struct {
	ID           string `json:"id"`
	Code         string `json:"code"`
	Name         string `json:"name"`
	NetDays      *int   `json:"netDays,omitempty"`
	DayOfMonth   *int   `json:"dayOfMonth,omitempty"`
	OffsetMonths int    `json:"offsetMonths"`
	Version      int    `json:"version"`
}

// PostingProfile is a customer posting profile of one legal entity.
type PostingProfile struct {
	ID            string `json:"id"`
	LegalEntityID string `json:"legalEntityId"`
	Code          string `json:"code"`
	Name          string `json:"name"`
	Version       int    `json:"version"`
}

// NewCustomerGroupRequest is a customer group to add with the optional
// Idempotency-Key and the hash of the request body.
type NewCustomerGroupRequest struct {
	In   domain.NewCustomerGroup
	Key  string
	Hash string
}

// SetupStore reads the setup records a customer role names.
type SetupStore interface {
	ListPaymentTerms(ctx context.Context) ([]PaymentTerm, error)
	GetPaymentTerm(ctx context.Context, id string) (PaymentTerm, error)
	ListPostingProfiles(ctx context.Context, legalEntityID string) ([]PostingProfile, error)
	GetPostingProfile(ctx context.Context, id string) (PostingProfile, error)
	CreateCustomerGroup(ctx context.Context, in NewCustomerGroupRequest) (domain.CustomerGroup, bool, error)
	ListCustomerGroups(ctx context.Context) ([]domain.CustomerGroup, error)
	GetCustomerGroup(ctx context.Context, id string) (domain.CustomerGroup, error)
}

func scanPaymentTerm(row pgx.Row) (PaymentTerm, error) {
	var t PaymentTerm
	err := row.Scan(&t.ID, &t.Code, &t.Name, &t.NetDays, &t.DayOfMonth, &t.OffsetMonths, &t.Version)
	return t, err
}

func scanPostingProfile(row pgx.Row) (PostingProfile, error) {
	var p PostingProfile
	err := row.Scan(&p.ID, &p.LegalEntityID, &p.Code, &p.Name, &p.Version)
	return p, err
}

// listRows reads every row of a query with scan, under the caller's tenant and
// legal entity scope.
func listRows[T any](ctx context.Context, s *Postgres, scan func(pgx.Row) (T, error), sql string, args ...any) ([]T, error) {
	out := []T{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, err := scan(rows)
			if err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return out, err
}

// getRow reads one row by id; an unknown or malformed id is code's 404.
func getRow[T any](ctx context.Context, s *Postgres, scan func(pgx.Row) (T, error), code, sql, raw string) (T, error) {
	var v T
	id, err := canonicalID(raw)
	if err != nil {
		return v, notFound(code, "not found")
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		v, err = scan(tx.QueryRow(ctx, sql, tenantOf(ctx), id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return v, notFound(code, "not found")
	}
	return v, err
}

func (s *Postgres) ListPaymentTerms(ctx context.Context) ([]PaymentTerm, error) {
	return listRows(ctx, s, scanPaymentTerm, `select `+paymentTermColumns+` from payment_term where tenant_id = $1 order by code`, tenantOf(ctx))
}

func (s *Postgres) GetPaymentTerm(ctx context.Context, id string) (PaymentTerm, error) {
	return getRow(ctx, s, scanPaymentTerm, CodePaymentTermNotFound, `select `+paymentTermColumns+` from payment_term where tenant_id = $1 and id = $2`, id)
}

// ListPostingProfiles reads the profiles of the caller's legal entities (row
// security), optionally of one of them.
func (s *Postgres) ListPostingProfiles(ctx context.Context, legalEntityID string) ([]PostingProfile, error) {
	return listRows(ctx, s, scanPostingProfile, `select `+postingProfileColumns+` from posting_profile
		where tenant_id = $1 and ($2 = '' or legal_entity_id::text = $2) order by code, legal_entity_id`, tenantOf(ctx), legalEntityID)
}

func (s *Postgres) GetPostingProfile(ctx context.Context, id string) (PostingProfile, error) {
	return getRow(ctx, s, scanPostingProfile, CodePostingProfileNotFound, `select `+postingProfileColumns+` from posting_profile where tenant_id = $1 and id = $2`, id)
}

func (s *Postgres) ListCustomerGroups(ctx context.Context) ([]domain.CustomerGroup, error) {
	return listRows(ctx, s, scanVendorGroup, `select `+customerGroupColumns+` from customer_group where tenant_id = $1 order by code`, tenantOf(ctx))
}

func (s *Postgres) GetCustomerGroup(ctx context.Context, id string) (domain.CustomerGroup, error) {
	return getRow(ctx, s, scanVendorGroup, CodeCustomerGroupNotFound, `select `+customerGroupColumns+` from customer_group where tenant_id = $1 and id = $2`, id)
}

// CreateCustomerGroup registers a customer group. The bool is false for an
// idempotent replay. A taken code or id is 409.
func (s *Postgres) CreateCustomerGroup(ctx context.Context, in NewCustomerGroupRequest) (domain.CustomerGroup, bool, error) {
	g, created, err := s.createCustomerGroupOnce(ctx, in)
	if err != nil && in.Key != "" && isUniqueViolation(err) {
		g, created, err = s.createCustomerGroupOnce(ctx, in)
	}
	if isUniqueViolation(err) {
		return domain.CustomerGroup{}, false, httpx.NewProblem(http.StatusConflict, CodeCustomerGroupExists, "the customer group code or id is already taken")
	}
	return g, created, err
}

func (s *Postgres) createCustomerGroupOnce(ctx context.Context, in NewCustomerGroupRequest) (domain.CustomerGroup, bool, error) {
	caller, _ := tenant.FromContext(ctx)
	var g domain.CustomerGroup
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var key *string
		if in.Key != "" {
			k := customerGroupKey + in.Key
			key = &k
			var storedHash string
			err := tx.QueryRow(ctx, `select request_hash from idempotency_keys where tenant_id = $1 and key = $2`, caller.TenantID, k).Scan(&storedHash)
			if err == nil {
				if storedHash != in.Hash {
					return httpx.NewProblem(http.StatusConflict, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
				}
				g, err = scanVendorGroup(tx.QueryRow(ctx, `select `+customerGroupColumns+` from customer_group where tenant_id = $1 and idempotency_key = $2`, caller.TenantID, k))
				return err
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		id := in.In.ID
		if id == "" {
			id = ids.New()
		}
		var err error
		g, err = scanVendorGroup(tx.QueryRow(ctx, `insert into customer_group (tenant_id, id, code, name, idempotency_key) values ($1, $2, $3, $4, $5) returning `+customerGroupColumns,
			caller.TenantID, id, in.In.Code, in.In.Name, key))
		if err != nil {
			return err
		}
		if key != nil {
			if _, err := tx.Exec(ctx, `insert into idempotency_keys (tenant_id, key, request_hash, response_status) values ($1, $2, $3, 201)`, caller.TenantID, *key, in.Hash); err != nil {
				return err
			}
		}
		created = true
		return nil
	})
	return g, created, err
}
