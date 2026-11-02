package store

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	pevents "github.com/BenjaminSRussell/passage/services/pricing/internal/events"
)

// Parameters are the pricing parameters of one legal entity (PRD 5.26.13). A legal entity
// with no stored row reads as the defaults at Version 0.
type Parameters struct {
	LegalEntityID string
	// JournalFourEyes makes the user who created a price journal unable to post it (PRC-0057).
	JournalFourEyes bool
	Version         int
	UpdatedBy       string
	UpdatedAt       *time.Time
}

// GetParameters returns the parameters of a legal entity, or the defaults when none are stored.
func (s *Postgres) GetParameters(ctx context.Context, legalEntityID string) (Parameters, error) {
	p, _ := tenant.FromContext(ctx)
	var out Parameters
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = loadParameters(ctx, tx, p.TenantID, legalEntityID)
		return err
	})
	return out, err
}

// SetParameters stores the parameters of a legal entity. version is the stored version the
// caller read (0 when none was stored); a different version is 412 conflict_version. The
// change is announced as parameters.changed through the outbox in the same transaction. A
// repeated Idempotency-Key replays the first response.
func (s *Postgres) SetParameters(ctx context.Context, in Parameters, version int, idempotencyKey string) (Parameters, error) {
	p, _ := tenant.FromContext(ctx)
	var out Parameters
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		hash := commandHash("parameters-set", in.LegalEntityID, strconv.FormatBool(in.JournalFourEyes), strconv.Itoa(version))
		if ok, err := replayJSON(ctx, tx, p.TenantID, idempotencyKey, hash, &out); err != nil || ok {
			return err
		}
		var tag pgconn.CommandTag
		var err error
		if version == 0 {
			tag, err = tx.Exec(ctx, `insert into pricing_parameter (tenant_id, legal_entity_id, journal_four_eyes, updated_by)
				values ($1, $2, $3, $4) on conflict (tenant_id, legal_entity_id) do nothing`, p.TenantID, in.LegalEntityID, in.JournalFourEyes, p.UserID)
			if err != nil {
				return err
			}
		} else {
			tag, err = tx.Exec(ctx, `update pricing_parameter set journal_four_eyes = $3, version = version + 1, updated_by = $4, updated_at = now()
				where tenant_id = $5 and legal_entity_id = $1 and version = $2`, in.LegalEntityID, version, in.JournalFourEyes, p.UserID, p.TenantID)
			if err != nil {
				return err
			}
		}
		// An insert that lost the race, or an update that matched no version, changed no row.
		if tag.RowsAffected() != 1 {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "parameters version does not match")
		}
		out, err = loadParameters(ctx, tx, p.TenantID, in.LegalEntityID)
		if err != nil {
			return err
		}
		if err := enqueueEvent(ctx, tx, "parameters", in.LegalEntityID, pevents.ParametersChanged, pevents.ParametersChangedData{
			LegalEntityID: out.LegalEntityID, JournalFourEyes: out.JournalFourEyes, Version: out.Version, UpdatedBy: out.UpdatedBy}); err != nil {
			return err
		}
		return rememberJSON(ctx, tx, p.TenantID, idempotencyKey, hash, out)
	})
	return out, err
}

func loadParameters(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID string) (Parameters, error) {
	out := Parameters{LegalEntityID: legalEntityID}
	err := tx.QueryRow(ctx, `select journal_four_eyes, version, updated_by, updated_at from pricing_parameter
		where tenant_id = $1 and legal_entity_id = $2`, tenantID, legalEntityID).
		Scan(&out.JournalFourEyes, &out.Version, &out.UpdatedBy, &out.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Parameters{LegalEntityID: legalEntityID}, nil
	}
	return out, err
}
