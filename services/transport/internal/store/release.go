package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/transport/internal/domain"
	tmsevents "github.com/BenjaminSRussell/passage/services/transport/internal/events"
)

const releaseColumns = `id, load_id, legal_entity_id::text, carriers, coalesce(currency, ''), timeout_min, released_by,
	legal_entity_scope::text[], released_at, request_hash`

// ReleaseLoad releases a load for tendering (FR-34.5) and writes the
// dev.passage.tms.load.released.v1 event in the same transaction; workflow-ts
// starts the tender cascade from it. ReleasedBy and LegalEntityScope are taken
// from the caller in ctx and in's are ignored: the workflow mints its scoped
// tender token from them, so they must be the verified principal. A load
// outside the caller's legal-entity scope is not found (row-level security)
// or refused with AUTH-0003, and nothing is published. The release takes the
// tender start's per-load lock and gates: a FAILed DG check (TMS-0130), an OPEN
// screening hold (TMS-0133) or an open tender (TMS-0114) refuse it, so the
// workflow is not started for a cascade transport would refuse. A replay with
// the same Idempotency-Key returns the first release and publishes nothing.
func (s *Postgres) ReleaseLoad(ctx context.Context, in domain.LoadRelease, idem Idem, now time.Time) (domain.LoadRelease, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.LoadRelease
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		replay := func() (bool, error) {
			if idem.Key == "" {
				return false, nil
			}
			r, hash, err := scanRelease(tx.QueryRow(ctx, `select `+releaseColumns+` from load_release where tenant_id = $1 and idempotency_key = $2`, p.TenantID, idem.Key))
			if errors.Is(err, pgx.ErrNoRows) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			if hash != idem.Fingerprint {
				return false, reuse()
			}
			out = r
			return true, nil
		}
		if done, err := replay(); done || err != nil {
			return err
		}
		entity, err := parentEntityTx(ctx, tx, parentLoad, p.TenantID, in.LoadID, "")
		if err != nil {
			return err
		}
		scope := tenant.LegalEntityScopeList(p)
		if p.UserID == "" || len(scope) == 0 || !tenant.InLegalEntityScope(p, entity) {
			return httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "a load is released by a user whose legal-entity scope lists the load's legal entity")
		}
		if err := lockLoadTender(ctx, tx, p.TenantID, in.LoadID); err != nil {
			return err
		}
		if done, err := replay(); done || err != nil {
			return err
		}
		if err := dgGate(ctx, tx, p.TenantID, in.LoadID); err != nil {
			return err
		}
		if err := screeningGate(ctx, tx, p.TenantID, in.LoadID); err != nil {
			return err
		}
		if err := openTenderGate(ctx, tx, p.TenantID, in.LoadID, now); err != nil {
			return err
		}
		carriers, err := json.Marshal(in.Carriers)
		if err != nil {
			return err
		}
		out, _, err = scanRelease(tx.QueryRow(ctx, `insert into load_release (tenant_id, id, load_id, legal_entity_id, carriers, currency, timeout_min,
			released_by, legal_entity_scope, released_at, idempotency_key, request_hash)
			values ($1, $2, $3, $4::uuid, $5::jsonb, $6, $7, $8, $9::uuid[], $10, $11, $12)
			returning `+releaseColumns,
			p.TenantID, ids.New(), in.LoadID, entity, string(carriers), nullable(in.Currency), in.TimeoutMin,
			p.UserID, scope, now, nullable(idem.Key), nullable(idem.Fingerprint)))
		if isUnique(err) {
			// The per-load lock serialises a repeat of this request, so the key
			// was taken by a concurrent release of another load.
			return reuse()
		}
		if err != nil {
			return err
		}
		return enqueueReleased(ctx, tx, p.TenantID, out)
	})
	return out, err
}

func enqueueReleased(ctx context.Context, tx pgx.Tx, tenantID string, r domain.LoadRelease) error {
	data := tmsevents.LoadReleasedPayload{
		ReleaseID: r.ID, LoadID: r.LoadID, LegalEntityID: r.LegalEntityID, ReleasedBy: r.ReleasedBy,
		LegalEntityScope: r.LegalEntityScope, Carriers: make([]tmsevents.ReleasedCarrier, 0, len(r.Carriers)),
		Currency: r.Currency, TimeoutMin: r.TimeoutMin,
	}
	for _, c := range r.Carriers {
		data.Carriers = append(data.Carriers, tmsevents.ReleasedCarrier{CarrierID: c.CarrierID, RateQuoted: c.RateQuoted})
	}
	evt, err := events.New(tmsevents.LoadReleased, eventSource, r.LoadID, tenantID, data)
	if err != nil {
		return err
	}
	evt.Actor = r.ReleasedBy
	return outbox.Enqueue(ctx, tx, "load", r.LoadID, evt)
}

func scanRelease(row pgx.Row) (domain.LoadRelease, string, error) {
	var r domain.LoadRelease
	var carriers []byte
	var hash *string
	if err := row.Scan(&r.ID, &r.LoadID, &r.LegalEntityID, &carriers, &r.Currency, &r.TimeoutMin, &r.ReleasedBy,
		&r.LegalEntityScope, &r.ReleasedAt, &hash); err != nil {
		return domain.LoadRelease{}, "", err
	}
	if err := json.Unmarshal(carriers, &r.Carriers); err != nil {
		return domain.LoadRelease{}, "", err
	}
	return r, deref(hash), nil
}
