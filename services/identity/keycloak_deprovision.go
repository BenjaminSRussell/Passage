package identity

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	deprovisionDeactivated = "scim_deactivated"
	deprovisionDeleted     = "scim_deleted"

	// DefaultDeprovisionInterval is how often the worker looks for due Keycloak calls.
	DefaultDeprovisionInterval = 30 * time.Second
	// DeprovisionMaxAttempts is the number of failed Keycloak calls after which a row is parked.
	DeprovisionMaxAttempts = 8
	deprovisionBackoffBase = 30 * time.Second
	deprovisionBackoffMax  = time.Hour
	// deprovisionLease is how long a claimed row is hidden from other workers; a worker that dies mid-call
	// leaves the row to be retried after it.
	deprovisionLease  = 2 * time.Minute
	deprovisionBatch  = 50
	deprovisionErrMax = 500
)

// queueKeycloakDeprovision records, inside the SCIM transaction that deprovisions the user, the Keycloak call
// that must disable it, so the status change and the pending call commit together.
func queueKeycloakDeprovision(ctx context.Context, tx pgx.Tx, tenantID, userID, userName, reason, actor string) error {
	_, err := tx.Exec(ctx, `insert into identity_keycloak_deprovisions (tenant_id, deprovision_id, user_id, idp_subject, user_name, reason, requested_by)
select tenant_id, $3, user_id, idp_subject, nullif($4, ''), $5, $6 from identity_users where tenant_id = $1 and user_id = $2`,
		tenantID, userID, ids.New(), userName, reason, actor)
	return err
}

// supersedeKeycloakDeprovision closes the user's Keycloak calls that have not run (pending or parked) when SCIM
// reactivates it. A call that already ran is not undone: re-enabling the Keycloak user is an administrator's step.
func supersedeKeycloakDeprovision(ctx context.Context, tx pgx.Tx, tenantID, userID string) error {
	_, err := tx.Exec(ctx, `update identity_keycloak_deprovisions set completed_at = now(), outcome = 'superseded', parked_at = null
where tenant_id = $1 and user_id = $2 and completed_at is null`, tenantID, userID)
	return err
}

// keycloakUsers is the Keycloak side of the worker (KeycloakAdmin, or a fake in tests).
type keycloakUsers interface {
	DeprovisionUser(ctx context.Context, realm string, target KeycloakTarget) (KeycloakOutcome, error)
}

// KeycloakDeprovisioner carries queued SCIM deprovisionings to Keycloak. Each round lists the tenants under the
// operator tenant (the only principal that reads the whole tenant registry) and then works each tenant's due rows
// under that tenant's own RLS scope. A row is claimed with a lease, the Keycloak call runs outside any
// transaction, and the result is written back: done (disabled or not_found), retried with exponential backoff,
// or parked after DeprovisionMaxAttempts failures or a permanent error. Parked rows wait for an operator.
type KeycloakDeprovisioner struct {
	pool             *pgxpool.Pool
	keycloak         keycloakUsers
	operatorTenantID string
	interval         time.Duration
	now              func() time.Time
}

// DeprovisionResult counts one round's rows.
type DeprovisionResult struct {
	Done, Retried, Parked int
}

func NewKeycloakDeprovisioner(pool *pgxpool.Pool, keycloak *KeycloakAdmin, operatorTenantID string, interval time.Duration) (*KeycloakDeprovisioner, error) {
	if operatorTenantID == "" {
		return nil, errors.New("keycloak deprovisioning needs the operator tenant to list tenants")
	}
	if interval <= 0 {
		interval = DefaultDeprovisionInterval
	}
	return &KeycloakDeprovisioner{pool: pool, keycloak: keycloak, operatorTenantID: operatorTenantID, interval: interval, now: time.Now}, nil
}

// Run works rounds until ctx ends.
func (d *KeycloakDeprovisioner) Run(ctx context.Context) {
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()
	for {
		if res, err := d.RunOnce(ctx); err != nil {
			slog.Error("keycloak deprovisioning round failed", "err", err)
		} else if res != (DeprovisionResult{}) {
			slog.Info("keycloak deprovisioning round", "done", res.Done, "retried", res.Retried, "parked", res.Parked)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

type deprovisionTenant struct{ id, realm string }

// RunOnce works every tenant's due rows once.
func (d *KeycloakDeprovisioner) RunOnce(ctx context.Context) (DeprovisionResult, error) {
	var tenants []deprovisionTenant
	opCtx := tenant.With(ctx, tenant.Principal{TenantID: d.operatorTenantID, UserID: keycloakDeprovisionActor})
	err := dbx.WithTenantTx(opCtx, d.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select tenant_id::text, realm_name from identity_tenants order by tenant_id`)
		if err != nil {
			return err
		}
		tenants, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (deprovisionTenant, error) {
			var t deprovisionTenant
			return t, row.Scan(&t.id, &t.realm)
		})
		return err
	})
	var total DeprovisionResult
	if err != nil {
		return total, err
	}
	var errs []error
	for _, t := range tenants {
		res, err := d.tenantOnce(ctx, t)
		total.Done += res.Done
		total.Retried += res.Retried
		total.Parked += res.Parked
		if err != nil {
			errs = append(errs, err)
		}
	}
	return total, errors.Join(errs...)
}

const keycloakDeprovisionActor = "system:identity-keycloak-deprovision"

type deprovisionRow struct {
	id, userID, subject, userName string
	attempts                      int
}

func (d *KeycloakDeprovisioner) inTenant(ctx context.Context, tenantID string, fn func(tx pgx.Tx) error) error {
	return dbx.WithTenantTx(tenant.With(ctx, tenant.Principal{TenantID: tenantID, UserID: keycloakDeprovisionActor}), d.pool, fn)
}

func (d *KeycloakDeprovisioner) tenantOnce(ctx context.Context, t deprovisionTenant) (DeprovisionResult, error) {
	var res DeprovisionResult
	now := d.now()
	var claimed []deprovisionRow
	err := d.inTenant(ctx, t.id, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `update identity_keycloak_deprovisions k set next_attempt_at = $3
from (select deprovision_id from identity_keycloak_deprovisions
      where tenant_id = $1 and completed_at is null and parked_at is null and next_attempt_at <= $2
      order by next_attempt_at, deprovision_id limit $4 for update skip locked) due
where k.tenant_id = $1 and k.deprovision_id = due.deprovision_id
returning k.deprovision_id::text, k.user_id::text, k.idp_subject, coalesce(k.user_name, ''), k.attempts`, t.id, now, now.Add(deprovisionLease), deprovisionBatch)
		if err != nil {
			return err
		}
		claimed, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (deprovisionRow, error) {
			var r deprovisionRow
			return r, row.Scan(&r.id, &r.userID, &r.subject, &r.userName, &r.attempts)
		})
		return err
	})
	if err != nil {
		return res, err
	}
	for _, row := range claimed {
		outcome, callErr := d.keycloak.DeprovisionUser(ctx, t.realm, KeycloakTarget{TenantID: t.id, Subject: row.subject, UserName: row.userName})
		if ctx.Err() != nil {
			// Shutting down: the lease returns the row to the queue.
			return res, ctx.Err()
		}
		finished := d.now()
		if err := d.inTenant(ctx, t.id, func(tx pgx.Tx) error {
			if callErr == nil {
				_, err := tx.Exec(ctx, `update identity_keycloak_deprovisions set completed_at = $3, outcome = $4, keycloak_user_id = nullif($5, ''), last_error = null
where tenant_id = $1 and deprovision_id = $2 and completed_at is null`, t.id, row.id, finished, outcome.Result, outcome.UserID)
				return err
			}
			attempts := row.attempts + 1
			park := attempts >= DeprovisionMaxAttempts || isPermanent(callErr)
			_, err := tx.Exec(ctx, `update identity_keycloak_deprovisions set attempts = $3, last_error = $4, next_attempt_at = $5,
  parked_at = case when $6 then $7::timestamptz end
where tenant_id = $1 and deprovision_id = $2 and completed_at is null`,
				t.id, row.id, attempts, limitError(callErr), finished.Add(deprovisionBackoff(attempts)), park, finished)
			return err
		}); err != nil {
			return res, err
		}
		switch {
		case callErr == nil:
			res.Done++
		case row.attempts+1 >= DeprovisionMaxAttempts || isPermanent(callErr):
			res.Parked++
			slog.Error("keycloak deprovisioning parked; an operator must fix the cause and retry it", "tenantId", t.id, "deprovisionId", row.id,
				"userId", row.userID, "attempts", row.attempts+1, "err", limitError(callErr))
		default:
			res.Retried++
			slog.Warn("keycloak deprovisioning failed; retrying", "tenantId", t.id, "deprovisionId", row.id, "attempts", row.attempts+1, "err", limitError(callErr))
		}
	}
	return res, nil
}

// deprovisionBackoff is the wait after the n-th failure: 30 s doubling, at most an hour.
func deprovisionBackoff(failures int) time.Duration {
	return min(deprovisionBackoffBase<<min(failures-1, 10), deprovisionBackoffMax)
}

func isPermanent(err error) bool {
	var keycloakErr *KeycloakError
	return errors.As(err, &keycloakErr) && keycloakErr.Permanent
}

func limitError(err error) string {
	msg := err.Error()
	if len(msg) > deprovisionErrMax {
		msg = msg[:deprovisionErrMax]
	}
	return msg
}
