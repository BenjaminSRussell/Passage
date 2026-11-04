package store

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
	partyevents "github.com/BenjaminSRussell/passage/services/party/internal/events"
)

const (
	CodeVendorGroupNotFound = "PTY-VENDOR-GROUP-NOT-FOUND"
	CodeVendorGroupInvalid  = "PTY-VENDOR-GROUP-INVALID"
	CodeVendorGroupExists   = "PTY-VENDOR-GROUP-EXISTS"
	vendorGroupColumns      = `id::text, code, name, version`
	vendorGroupKeyPrefix    = "vendor-group:"
)

// NewVendorGroupRequest is a vendor group to add; Key and Hash carry the
// optional Idempotency-Key and the hash of the request body.
type NewVendorGroupRequest struct {
	In   domain.NewVendorGroup
	Key  string
	Hash string
}

func scanVendorGroup(row pgx.Row) (domain.VendorGroup, error) {
	var g domain.VendorGroup
	err := row.Scan(&g.ID, &g.Code, &g.Name, &g.Version)
	return g, err
}

func vendorGroupNotFound() error {
	return notFound(CodeVendorGroupNotFound, "vendor group not found")
}

// CreateVendorGroup registers a vendor group and emits vendorgroup.created in
// the same transaction. The bool is false for an idempotent replay.
func (s *Postgres) CreateVendorGroup(ctx context.Context, in NewVendorGroupRequest) (domain.VendorGroup, bool, error) {
	g, created, err := s.createVendorGroupOnce(ctx, in)
	if err != nil && in.Key != "" && isUniqueViolation(err) {
		g, created, err = s.createVendorGroupOnce(ctx, in)
	}
	if isUniqueViolation(err) {
		return domain.VendorGroup{}, false, httpx.NewProblem(http.StatusConflict, CodeVendorGroupExists, "the vendor group code or id is already taken")
	}
	return g, created, err
}

func (s *Postgres) createVendorGroupOnce(ctx context.Context, in NewVendorGroupRequest) (domain.VendorGroup, bool, error) {
	caller, _ := tenant.FromContext(ctx)
	var g domain.VendorGroup
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var key *string
		if in.Key != "" {
			k := vendorGroupKeyPrefix + in.Key
			key = &k
			var storedHash string
			err := tx.QueryRow(ctx, `select request_hash from idempotency_keys where tenant_id = $1 and key = $2`, caller.TenantID, k).Scan(&storedHash)
			if err == nil {
				if storedHash != in.Hash {
					return httpx.NewProblem(http.StatusConflict, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
				}
				g, err = scanVendorGroup(tx.QueryRow(ctx, `select `+vendorGroupColumns+` from vendor_group where tenant_id = $1 and idempotency_key = $2`, caller.TenantID, k))
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
		g, err = scanVendorGroup(tx.QueryRow(ctx, `insert into vendor_group (tenant_id, id, code, name, idempotency_key) values ($1, $2, $3, $4, $5) returning `+vendorGroupColumns,
			caller.TenantID, id, in.In.Code, in.In.Name, key))
		if err != nil {
			return err
		}
		if key != nil {
			if _, err := tx.Exec(ctx, `insert into idempotency_keys (tenant_id, key, request_hash, response_status) values ($1, $2, $3, 201)`, caller.TenantID, *key, in.Hash); err != nil {
				return err
			}
		}
		evt, err := events.New(partyevents.VendorGroupCreated, domain.EventSource, g.ID, caller.TenantID, partyevents.VendorGroupCreatedData{
			VendorGroupID: g.ID, Code: g.Code, Name: g.Name,
		})
		if err != nil {
			return err
		}
		if err := outbox.Enqueue(ctx, tx, domain.AggregateVendorGroup, g.ID, evt); err != nil {
			return err
		}
		created = true
		return nil
	})
	return g, created, err
}

func (s *Postgres) GetVendorGroup(ctx context.Context, groupID string) (domain.VendorGroup, error) {
	caller, _ := tenant.FromContext(ctx)
	id, err := canonicalID(groupID)
	if err != nil {
		return domain.VendorGroup{}, vendorGroupNotFound()
	}
	var g domain.VendorGroup
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		g, err = scanVendorGroup(tx.QueryRow(ctx, `select `+vendorGroupColumns+` from vendor_group where tenant_id = $1 and id = $2`, caller.TenantID, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.VendorGroup{}, vendorGroupNotFound()
	}
	return g, err
}

func (s *Postgres) ListVendorGroups(ctx context.Context) ([]domain.VendorGroup, error) {
	caller, _ := tenant.FromContext(ctx)
	groups := []domain.VendorGroup{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+vendorGroupColumns+` from vendor_group where tenant_id = $1 order by code`, caller.TenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			g, err := scanVendorGroup(rows)
			if err != nil {
				return err
			}
			groups = append(groups, g)
		}
		return rows.Err()
	})
	return groups, err
}
