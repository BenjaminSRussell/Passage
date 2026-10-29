package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	ActionPromote  = "promote"
	ActionRollback = "rollback"
	eventSource    = "platform-health"
	fkViolation    = "23503"
	releaseColumns = "id, service, environment, action, image_digest, previous_digest, git_sha, signed, recorded_by_tenant::text, recorded_by_user, coalesce(reason, ''), created_at"
)

var (
	ErrUnknownEnvironment   = errors.New("unknown environment")
	ErrIdempotencyConflict  = errors.New("idempotency key reused with a different release")
	ErrRollbackTargetUnseen = errors.New("rollback target was never promoted to this service and environment")
)

type Release struct {
	ID             string    `json:"id"`
	Service        string    `json:"service"`
	Environment    string    `json:"environment"`
	Action         string    `json:"action"`
	ImageDigest    string    `json:"imageDigest"`
	PreviousDigest string    `json:"previousDigest"`
	GitSHA         string    `json:"gitSha"`
	Signed         bool      `json:"signed"`
	Actor          string    `json:"actor"`
	Reason         string    `json:"reason,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`

	RecordedByTenant string `json:"-"`
}

func (r Release) samePayload(o Release) bool {
	return r.Service == o.Service && r.Environment == o.Environment && r.Action == o.Action &&
		r.ImageDigest == o.ImageDigest && r.PreviousDigest == o.PreviousDigest && r.GitSHA == o.GitSHA &&
		r.Reason == o.Reason
}

type releaseEventData struct {
	ReleaseID      string `json:"release_id"`
	Service        string `json:"service"`
	Environment    string `json:"environment"`
	ImageDigest    string `json:"image_digest"`
	PreviousDigest string `json:"previous_digest"`
	GitSHA         string `json:"git_sha"`
	Actor          string `json:"actor"`
	Reason         string `json:"reason,omitempty"`
}

func scanRelease(row pgx.Row, r *Release) error {
	return row.Scan(&r.ID, &r.Service, &r.Environment, &r.Action, &r.ImageDigest, &r.PreviousDigest, &r.GitSHA, &r.Signed, &r.RecordedByTenant, &r.Actor, &r.Reason, &r.CreatedAt)
}

func platformScope(ctx context.Context) context.Context {
	p, _ := tenant.FromContext(ctx)
	p.TenantID = ids.Nil
	p.LegalEntityID = ""
	return tenant.With(ctx, p)
}

func (s *Postgres) RecordRelease(ctx context.Context, in Release, idempotencyKey string) (Release, error) {
	caller, _ := tenant.FromContext(ctx)
	actor := "anonymous"
	if caller.UserID != "" {
		actor = caller.UserID
	}
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	typ := events.Type("platform", "release", "promoted", 1)
	if in.Action == ActionRollback {
		typ = events.Type("platform", "release", "rolledback", 1)
	}
	var r Release
	err := dbx.WithTenantTx(platformScope(ctx), s.pool, func(tx pgx.Tx) error {
		if in.Action == ActionRollback {
			var promoted bool
			if err := tx.QueryRow(ctx, `select exists (select 1 from platform_release where service = $1 and environment = $2 and action = 'promote' and image_digest = $3)`,
				in.Service, in.Environment, in.ImageDigest).Scan(&promoted); err != nil {
				return err
			}
			if !promoted {
				return ErrRollbackTargetUnseen
			}
		}
		var reason *string
		if in.Reason != "" {
			reason = &in.Reason
		}
		err := scanRelease(tx.QueryRow(ctx, `insert into platform_release (tenant_id, id, service, environment, action, image_digest, previous_digest, git_sha, signed, recorded_by_tenant, recorded_by_user, reason, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
			on conflict (recorded_by_tenant, idempotency_key) do nothing
			returning `+releaseColumns,
			ids.Nil, ids.New(), in.Service, in.Environment, in.Action, in.ImageDigest, in.PreviousDigest, in.GitSHA, in.Signed, caller.TenantID, actor, reason, key), &r)
		if errors.Is(err, pgx.ErrNoRows) {
			if err := scanRelease(tx.QueryRow(ctx, `select `+releaseColumns+` from platform_release where recorded_by_tenant = $1 and idempotency_key = $2`, caller.TenantID, key), &r); err != nil {
				return err
			}
			if !r.samePayload(in) {
				return ErrIdempotencyConflict
			}
			return nil
		}
		if err != nil {
			return err
		}
		evt, err := events.New(typ, eventSource, r.ID, ids.Nil, releaseEventData{
			ReleaseID: r.ID, Service: r.Service, Environment: r.Environment, ImageDigest: r.ImageDigest,
			PreviousDigest: r.PreviousDigest, GitSHA: r.GitSHA, Actor: r.Actor, Reason: r.Reason,
		})
		if err != nil {
			return err
		}
		evt.Actor = r.Actor
		return outbox.Enqueue(ctx, tx, "release", r.ID, evt)
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == fkViolation {
		return Release{}, ErrUnknownEnvironment
	}
	return r, err
}

func (s *Postgres) ListReleases(ctx context.Context, service, environment string) ([]Release, error) {
	releases := []Release{}
	err := dbx.WithTenantTx(platformScope(ctx), s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+releaseColumns+` from platform_release
			where ($1 = '' or service = $1) and ($2 = '' or environment = $2)
			order by created_at desc, id desc limit $3`, service, environment, listLimit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r Release
			if err := scanRelease(rows, &r); err != nil {
				return err
			}
			releases = append(releases, r)
		}
		return rows.Err()
	})
	return releases, err
}
