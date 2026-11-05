package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
)

var (
	// ErrVersionConflict means the If-Match version is not the stored one.
	ErrVersionConflict = errors.New("edi: version conflict")
	// ErrOutOfScope means the partner's trade profile binds it to a legal
	// entity outside the caller's scope.
	ErrOutOfScope = errors.New("edi: partner is bound to a legal entity outside the caller's scope")
)

// UpdatePartner replaces the partner's changeable fields (name, ISA
// identity, ack SLA, EDIFACT identity and CONTRL mode, status; never the
// environment) when version is its stored version, and bumps the version.
// A partner bound by its trade profile to a legal entity outside the
// caller's scope is ErrOutOfScope (edi_partner_in_entity_scope, migration
// 0009): only a caller of that entity may change it, as for the profile and
// the transport; a partner without a profile is changed by any caller of the
// tenant. A stale version is ErrVersionConflict with the stored partner; an
// identity another partner has in the environment is ErrDuplicate.
func (s *Store) UpdatePartner(ctx context.Context, id string, version int, prof domain.PartnerProfile, status string) (Partner, error) {
	prof = prof.WithDefaults()
	var out Partner
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var edifactID, edifactQualifier, edifactContrl string
		if e := prof.EDIFACT; e != nil {
			edifactID, edifactQualifier, edifactContrl = e.ID, e.Qualifier, e.Contrl
		}
		tag, err := tx.Exec(ctx, `update trading_partner set name = $3, isa_qualifier = $4, isa_id = $5, ack_sla_minutes = $6,
			edifact_id = $7, edifact_qualifier = $8, edifact_contrl = $9, status = $10, version = version + 1
			where id = $1 and version = $2 and edi_partner_in_entity_scope(id)`,
			id, version, prof.Name, prof.ISA.Qualifier, prof.ISA.ID, prof.AckSLAMinutes,
			nullString(edifactID), nullString(edifactQualifier), nullString(edifactContrl), status)
		if isUnique(err) {
			return ErrDuplicate
		}
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			var inScope bool
			err := tx.QueryRow(ctx, `select edi_partner_in_entity_scope(id) from trading_partner where id = $1`, id).Scan(&inScope)
			switch {
			case errors.Is(err, pgx.ErrNoRows):
				return ErrNotFound
			case err != nil:
				return err
			case !inScope:
				return ErrOutOfScope
			}
			if err := scanPartner(tx.QueryRow(ctx, partnerColumns+` where id = $1`, id), &out); err != nil {
				return err
			}
			return ErrVersionConflict
		}
		return scanPartner(tx.QueryRow(ctx, partnerColumns+` where id = $1`, id), &out)
	})
	return out, err
}
