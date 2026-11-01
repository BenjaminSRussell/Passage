package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	fulevents "github.com/BenjaminSRussell/passage/services/fulfillment/internal/events"
)

// LegalEntitySettingStore is the posting settings per legal entity (section
// 5.28.13): cogs_at_shipment and asn_enabled.
type LegalEntitySettingStore interface {
	ListLegalEntitySettings(ctx context.Context) ([]LegalEntitySetting, error)
	GetLegalEntitySetting(ctx context.Context, legalEntityID string) (LegalEntitySetting, error)
	SaveLegalEntitySetting(ctx context.Context, legalEntityID string, version int, in LegalEntitySettingInput, idem Idem) (LegalEntitySetting, error)
}

// LegalEntitySetting is the posting defaults of one legal entity. A legal
// entity without a saved setting reads both false at version 0.
type LegalEntitySetting struct {
	LegalEntityID string `json:"legalEntityId"`
	// CogsAtShipment books a packing slip as Dr 5000 / Cr 1400 instead of
	// Dr 1405 / Cr 1400 when the post request does not say.
	CogsAtShipment bool `json:"cogsAtShipment"`
	// AsnEnabled publishes the advance ship notice with a post that does not say.
	AsnEnabled bool       `json:"asnEnabled"`
	Version    int        `json:"version"`
	UpdatedAt  *time.Time `json:"updatedAt"`
}

// LegalEntitySettingInput is the value saved for a legal entity.
type LegalEntitySettingInput struct {
	CogsAtShipment bool
	AsnEnabled     bool
	// CanChangeCogs says the caller holds the finance setup duty that changing
	// cogs_at_shipment needs (an accounting policy, not a warehouse default).
	CanChangeCogs bool
}

// DutyCogsPolicy is the ledger setup duty that changing cogs_at_shipment needs
// on top of the route duty.
const DutyCogsPolicy = "ledger.setup.write"

// ListLegalEntitySettings lists every legal entity the caller sees shipments
// or a saved setting for, with its settings (both false when none is saved).
func (s *Postgres) ListLegalEntitySettings(ctx context.Context) ([]LegalEntitySetting, error) {
	out := []LegalEntitySetting{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select l.legal_entity_id, coalesce(ls.cogs_at_shipment, false), coalesce(ls.asn_enabled, false), coalesce(ls.version, 0), ls.updated_at
			from (select legal_entity_id from shipment union select legal_entity_id from legal_entity_setting) l
			left join legal_entity_setting ls on ls.legal_entity_id = l.legal_entity_id
			order by l.legal_entity_id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var l LegalEntitySetting
			if err := rows.Scan(&l.LegalEntityID, &l.CogsAtShipment, &l.AsnEnabled, &l.Version, &l.UpdatedAt); err != nil {
				return err
			}
			out = append(out, l)
		}
		return rows.Err()
	})
	return out, err
}

// loadLegalEntitySetting reads the saved setting of a legal entity, or the
// default (version 0) when none is saved or visible.
func loadLegalEntitySetting(ctx context.Context, tx pgx.Tx, legalEntityID string, lock bool) (LegalEntitySetting, error) {
	l := LegalEntitySetting{LegalEntityID: legalEntityID}
	q := `select cogs_at_shipment, asn_enabled, version, updated_at from legal_entity_setting where legal_entity_id = $1`
	if lock {
		q += ` for update`
	}
	err := tx.QueryRow(ctx, q, legalEntityID).Scan(&l.CogsAtShipment, &l.AsnEnabled, &l.Version, &l.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return l, nil
	}
	return l, err
}

// GetLegalEntitySetting reads the posting settings of one legal entity.
func (s *Postgres) GetLegalEntitySetting(ctx context.Context, legalEntityID string) (LegalEntitySetting, error) {
	var l LegalEntitySetting
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		l, err = loadLegalEntitySetting(ctx, tx, legalEntityID, false)
		return err
	})
	return l, err
}

// SaveLegalEntitySetting sets the posting settings of a legal entity. version
// is the one the caller read (0 when none is saved); a stale version is
// refused, as is a legal entity outside the caller's scope (FUL-0058) and a
// change of cogs_at_shipment by a caller without DutyCogsPolicy (403).
// legalentitysetting.changed is written in the same transaction.
func (s *Postgres) SaveLegalEntitySetting(ctx context.Context, legalEntityID string, version int, in LegalEntitySettingInput, idem Idem) (LegalEntitySetting, error) {
	p, _ := tenant.FromContext(ctx)
	var out LegalEntitySetting
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		res, err := runIdempotent(ctx, tx, p.TenantID, idem, func() (LegalEntitySetting, error) {
			// Serialise the first save of a legal entity, which has no row to lock yet.
			if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1 || '/legal-entity-setting/' || $2, 0))`, p.TenantID, legalEntityID); err != nil {
				return LegalEntitySetting{}, err
			}
			cur, err := loadLegalEntitySetting(ctx, tx, legalEntityID, true)
			if err != nil {
				return cur, err
			}
			if cur.Version != version {
				return cur, httpx.NewProblem(http.StatusPreconditionFailed, CodeVersion, "legal entity setting changed since it was read; reload and retry")
			}
			if in.CogsAtShipment != cur.CogsAtShipment && !in.CanChangeCogs {
				return cur, httpx.NewProblem(http.StatusForbidden, CodeDutyMissing, "changing cogs_at_shipment needs the "+DutyCogsPolicy+" duty")
			}
			next := LegalEntitySetting{LegalEntityID: legalEntityID, CogsAtShipment: in.CogsAtShipment, AsnEnabled: in.AsnEnabled}
			err = tx.QueryRow(ctx, `insert into legal_entity_setting (tenant_id, legal_entity_id, cogs_at_shipment, asn_enabled, version) values ($1, $2, $3, $4, 1)
				on conflict (tenant_id, legal_entity_id) do update set cogs_at_shipment = excluded.cogs_at_shipment, asn_enabled = excluded.asn_enabled,
					version = legal_entity_setting.version + 1, updated_at = now()
				returning version, updated_at`, p.TenantID, legalEntityID, in.CogsAtShipment, in.AsnEnabled).Scan(&next.Version, &next.UpdatedAt)
			// A legal entity outside the caller's scope trips the row-level security policy.
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "42501" {
				return cur, httpx.NewProblem(http.StatusUnprocessableEntity, CodeLegalEntityScope, "the legal entity is outside your legal-entity scope")
			}
			if err != nil {
				return cur, err
			}
			return next, enqueueFor(ctx, tx, "legal_entity_setting", legalEntityID, fulevents.LegalEntitySettingChanged, p.TenantID, fulevents.LegalEntitySettingChangedData{
				LegalEntityID: legalEntityID, CogsAtShipment: next.CogsAtShipment, AsnEnabled: next.AsnEnabled,
				PreviousCogsAtShipment: cur.CogsAtShipment, PreviousAsnEnabled: cur.AsnEnabled, Version: next.Version})
		})
		out = res
		return err
	})
	return out, err
}
