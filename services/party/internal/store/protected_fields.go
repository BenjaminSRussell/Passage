package store

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
	partyevents "github.com/BenjaminSRussell/passage/services/party/internal/events"
)

func scopedLegalEntity(caller tenant.Principal, legalEntityID string) (string, error) {
	id, err := canonicalID(legalEntityID)
	if err != nil {
		return "", httpx.NewProblem(http.StatusUnprocessableEntity, CodeFieldsInvalid, "legal entity id is not a UUID")
	}
	if caller.LegalEntityID != "" && !strings.EqualFold(caller.LegalEntityID, id) {
		return "", httpx.NewProblem(http.StatusForbidden, CodeOutOfScope, "the caller is not scoped to this legal entity")
	}
	return id, nil
}

func (s *Postgres) ProtectedFields(ctx context.Context, legalEntityID string) ([]string, error) {
	caller, _ := tenant.FromContext(ctx)
	id, err := scopedLegalEntity(caller, legalEntityID)
	if err != nil {
		return nil, err
	}
	fields := domain.DefaultProtectedFields()
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `select fields from protected_field_config where tenant_id = $1 and legal_entity_id = $2`, caller.TenantID, id).Scan(&fields)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	})
	return fields, err
}

func (s *Postgres) SetProtectedFields(ctx context.Context, legalEntityID string, fields []string) ([]string, error) {
	caller, _ := tenant.FromContext(ctx)
	if err := requireUser(caller); err != nil {
		return nil, err
	}
	id, err := scopedLegalEntity(caller, legalEntityID)
	if err != nil {
		return nil, err
	}
	fields = append([]string{}, fields...)
	slices.Sort(fields)
	fields = slices.Compact(fields)
	for _, f := range fields {
		if !slices.Contains(domain.ProtectableFields, f) {
			p := httpx.NewProblem(http.StatusUnprocessableEntity, CodeFieldsInvalid, "protected fields failed validation")
			p.Errors = map[string]string{"fields": "unknown field " + f}
			return nil, p
		}
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		before := domain.DefaultProtectedFields()
		err := tx.QueryRow(ctx, `select fields from protected_field_config where tenant_id = $1 and legal_entity_id = $2 for update`, caller.TenantID, id).Scan(&before)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var updatedAt time.Time
		if err := tx.QueryRow(ctx, `insert into protected_field_config (tenant_id, legal_entity_id, fields, updated_by) values ($1, $2, $3, $4)
			on conflict (tenant_id, legal_entity_id) do update set fields = excluded.fields, updated_by = excluded.updated_by, updated_at = now()
			returning fields, updated_at`, caller.TenantID, id, fields, caller.UserID).Scan(&fields, &updatedAt); err != nil {
			return err
		}
		evt, err := events.New(partyevents.ProtectedFieldsUpdated, domain.EventSource, id, caller.TenantID, partyevents.ProtectedFieldsUpdatedData{
			LegalEntityID: id, Before: before, After: fields, ActorUserID: caller.UserID, UpdatedAt: updatedAt,
		})
		if err != nil {
			return err
		}
		return outbox.Enqueue(ctx, tx, domain.AggregateLegalEntity, id, evt)
	})
	return fields, err
}
