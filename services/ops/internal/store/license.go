package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/services/ops/internal/domain"
)

// LicenseRecord is an installed licence.
type LicenseRecord struct {
	License     domain.License `json:"license"`
	Version     int64          `json:"version"`
	InstalledBy string         `json:"installedBy"`
	InstalledAt time.Time      `json:"installedAt"`
}

// CodeLicenseNone: no licence is installed for the tenant.
const CodeLicenseNone = "LIC-1807"

func licenseProblem(err error) error { return Problem(err) }

// InstallLicense stores a verified licence for its tenant. ifMatch is the version the caller saw (0 when none is installed), so
// two operators installing at once cannot overwrite each other unknowingly (LIC-1806). A licence that expires earlier than the
// installed one is allowed (a correction); the signature, not the date, is the authority.
func (s *Postgres) InstallLicense(ctx context.Context, l domain.License, payload, signature []byte, user string, ifMatch int64) (LicenseRecord, error) {
	var rec LicenseRecord
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var cur int64
		err := tx.QueryRow(ctx, `select version from ops_tenant_license where licensee_id = $1::uuid for update`, l.TenantID).Scan(&cur)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if cur != ifMatch {
			return &domain.Error{Code: domain.CodeLicenseStale, Detail: "the installed licence changed; read it again and retry with its version"}
		}
		_, err = tx.Exec(ctx, `insert into ops_tenant_license (licensee_id, issued_at, expires_at, full_users, operational_users, selfservice_users, grace_days, version, payload, signature, installed_by)
			values ($1::uuid, $2::date, $3::date, $4, $5, $6, $7, 1, $8, $9, $10)
			on conflict (licensee_id) do update set issued_at = excluded.issued_at, expires_at = excluded.expires_at, full_users = excluded.full_users,
			  operational_users = excluded.operational_users, selfservice_users = excluded.selfservice_users, grace_days = excluded.grace_days,
			  version = ops_tenant_license.version + 1, payload = excluded.payload, signature = excluded.signature,
			  installed_by = excluded.installed_by, installed_at = now()`,
			l.TenantID, l.IssuedAt, l.ExpiresAt, l.FullUsers, l.OperationalUsers, l.SelfServiceUsers, l.Grace(), payload, signature, user)
		if err != nil {
			return err
		}
		rec, err = readLicense(ctx, tx, l.TenantID)
		return err
	})
	return rec, licenseProblem(err)
}

type licenseQuery interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func readLicense(ctx context.Context, q licenseQuery, tenantID string) (LicenseRecord, error) {
	var r LicenseRecord
	var grace int
	err := q.QueryRow(ctx, `select licensee_id::text, to_char(issued_at, 'YYYY-MM-DD'), to_char(expires_at, 'YYYY-MM-DD'), full_users, operational_users,
		selfservice_users, grace_days, version, installed_by, installed_at from ops_tenant_license where licensee_id = $1::uuid`, tenantID).Scan(
		&r.License.TenantID, &r.License.IssuedAt, &r.License.ExpiresAt, &r.License.FullUsers, &r.License.OperationalUsers, &r.License.SelfServiceUsers,
		&grace, &r.Version, &r.InstalledBy, &r.InstalledAt)
	r.License.GraceDays = &grace
	return r, err
}

// License reads the installed licence of a tenant.
func (s *Postgres) License(ctx context.Context, tenantID string) (LicenseRecord, error) {
	r, err := readLicense(ctx, s.pool, tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, Problem(&domain.Error{Code: CodeLicenseNone, Detail: "no licence is installed for this tenant"})
	}
	return r, err
}

// Licenses lists every installed licence (for the days-to-expiry metric).
func (s *Postgres) Licenses(ctx context.Context) ([]LicenseRecord, error) {
	rows, err := s.pool.Query(ctx, `select licensee_id::text, to_char(issued_at, 'YYYY-MM-DD'), to_char(expires_at, 'YYYY-MM-DD'), full_users, operational_users,
		selfservice_users, grace_days, version, installed_by, installed_at from ops_tenant_license order by licensee_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LicenseRecord
	for rows.Next() {
		var r LicenseRecord
		var grace int
		if err := rows.Scan(&r.License.TenantID, &r.License.IssuedAt, &r.License.ExpiresAt, &r.License.FullUsers, &r.License.OperationalUsers, &r.License.SelfServiceUsers,
			&grace, &r.Version, &r.InstalledBy, &r.InstalledAt); err != nil {
			return nil, err
		}
		r.License.GraceDays = &grace
		out = append(out, r)
	}
	return out, rows.Err()
}
