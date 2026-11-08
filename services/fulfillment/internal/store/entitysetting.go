package store

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
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
	AsnEnabled bool `json:"asnEnabled"`
	// GS1CompanyPrefix and SSCCExtensionDigit are the SSCC parameters of the
	// entity's containers (BR-28.5); nil takes the process default.
	GS1CompanyPrefix   *string `json:"gs1CompanyPrefix"`
	SSCCExtensionDigit *int    `json:"ssccExtensionDigit"`
	// PackagingFeeBillable marks the packing slips posted from now on as
	// carrying packaging fees billing is to charge (BR-28.12).
	PackagingFeeBillable bool `json:"packagingFeeBillable"`
	// RTPTrackingEnabled (rtp_tracking_enabled): the entity's packing slips
	// raise the customers' returnable balances (BR-28.13); false for none.
	// RTPChargeDays (rtp_charge_days, 30 to 365, 90 when none is saved) is the
	// age after which an outstanding balance of a slip of the entity is
	// chargeable (FR-28.16).
	RTPTrackingEnabled bool       `json:"rtpTrackingEnabled"`
	RTPChargeDays      int        `json:"rtpChargeDays"`
	Version            int        `json:"version"`
	UpdatedAt          *time.Time `json:"updatedAt"`
}

// LegalEntitySettingInput is the value saved for a legal entity.
type LegalEntitySettingInput struct {
	CogsAtShipment bool
	AsnEnabled     bool
	// GS1CompanyPrefix (6 to 12 digits) and SSCCExtensionDigit (0 to 9) are
	// validated by the caller. A field whose Set is false keeps the saved
	// value, so a client that predates a field never clears it; Set with a nil
	// Value returns the entity to the process default.
	GS1CompanyPrefix   Change[*string]
	SSCCExtensionDigit Change[*int]
	// PackagingFeeBillable keeps the saved value unless Set.
	PackagingFeeBillable Change[bool]
	// RTPTrackingEnabled and RTPChargeDays (30 to 365, validated by the
	// caller) keep the saved value unless Set.
	RTPTrackingEnabled Change[bool]
	RTPChargeDays      Change[int]
	// CanChangeCogs says the caller holds the finance setup duty that changing
	// cogs_at_shipment needs (an accounting policy, not a warehouse default).
	CanChangeCogs bool
}

// Change is an optional field of a save: Set false keeps the saved value.
type Change[T any] struct {
	Set   bool
	Value T
}

func (c Change[T]) or(saved T) T {
	if c.Set {
		return c.Value
	}
	return saved
}

// DutyCogsPolicy is the ledger setup duty that changing cogs_at_shipment needs
// on top of the route duty.
const DutyCogsPolicy = "ledger.setup.write"

// ListLegalEntitySettings lists every legal entity the caller sees shipments
// or a saved setting for, with its settings (both false when none is saved).
func (s *Postgres) ListLegalEntitySettings(ctx context.Context) ([]LegalEntitySetting, error) {
	out := []LegalEntitySetting{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select l.legal_entity_id, coalesce(ls.cogs_at_shipment, false), coalesce(ls.asn_enabled, false),
			ls.gs1_company_prefix, ls.sscc_extension_digit::int, coalesce(ls.packaging_fee_billable, false),
			coalesce(ls.rtp_tracking_enabled, true), coalesce(ls.rtp_charge_days, 90), coalesce(ls.version, 0), ls.updated_at
			from (select legal_entity_id from shipment union select legal_entity_id from legal_entity_setting) l
			left join legal_entity_setting ls on ls.legal_entity_id = l.legal_entity_id
			order by l.legal_entity_id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var l LegalEntitySetting
			if err := rows.Scan(&l.LegalEntityID, &l.CogsAtShipment, &l.AsnEnabled, &l.GS1CompanyPrefix, &l.SSCCExtensionDigit, &l.PackagingFeeBillable, &l.RTPTrackingEnabled, &l.RTPChargeDays, &l.Version, &l.UpdatedAt); err != nil {
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
	l := LegalEntitySetting{LegalEntityID: legalEntityID, RTPTrackingEnabled: true, RTPChargeDays: domain.DefaultRTPChargeDays}
	q := `select cogs_at_shipment, asn_enabled, gs1_company_prefix, sscc_extension_digit::int, packaging_fee_billable, rtp_tracking_enabled, rtp_charge_days, version, updated_at from legal_entity_setting where legal_entity_id = $1`
	if lock {
		q += ` for update`
	}
	err := tx.QueryRow(ctx, q, legalEntityID).Scan(&l.CogsAtShipment, &l.AsnEnabled, &l.GS1CompanyPrefix, &l.SSCCExtensionDigit, &l.PackagingFeeBillable, &l.RTPTrackingEnabled, &l.RTPChargeDays, &l.Version, &l.UpdatedAt)
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
			next := LegalEntitySetting{LegalEntityID: legalEntityID, CogsAtShipment: in.CogsAtShipment, AsnEnabled: in.AsnEnabled,
				GS1CompanyPrefix: in.GS1CompanyPrefix.or(cur.GS1CompanyPrefix), SSCCExtensionDigit: in.SSCCExtensionDigit.or(cur.SSCCExtensionDigit),
				PackagingFeeBillable: in.PackagingFeeBillable.or(cur.PackagingFeeBillable),
				RTPTrackingEnabled:   in.RTPTrackingEnabled.or(cur.RTPTrackingEnabled), RTPChargeDays: in.RTPChargeDays.or(cur.RTPChargeDays)}
			if next.GS1CompanyPrefix != nil && deref(next.GS1CompanyPrefix) != deref(cur.GS1CompanyPrefix) {
				if err := s.checkPrefixNesting(ctx, tx, legalEntityID, *next.GS1CompanyPrefix); err != nil {
					return cur, err
				}
			}
			err = tx.QueryRow(ctx, `insert into legal_entity_setting (tenant_id, legal_entity_id, cogs_at_shipment, asn_enabled, gs1_company_prefix, sscc_extension_digit, packaging_fee_billable, rtp_tracking_enabled, rtp_charge_days, version)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, 1)
				on conflict (tenant_id, legal_entity_id) do update set cogs_at_shipment = excluded.cogs_at_shipment, asn_enabled = excluded.asn_enabled,
					gs1_company_prefix = excluded.gs1_company_prefix, sscc_extension_digit = excluded.sscc_extension_digit,
					packaging_fee_billable = excluded.packaging_fee_billable, rtp_tracking_enabled = excluded.rtp_tracking_enabled,
					rtp_charge_days = excluded.rtp_charge_days, version = legal_entity_setting.version + 1, updated_at = now()
				returning version, updated_at`, p.TenantID, legalEntityID, next.CogsAtShipment, next.AsnEnabled, next.GS1CompanyPrefix, next.SSCCExtensionDigit, next.PackagingFeeBillable, next.RTPTrackingEnabled, next.RTPChargeDays).Scan(&next.Version, &next.UpdatedAt)
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
				PreviousCogsAtShipment: cur.CogsAtShipment, PreviousAsnEnabled: cur.AsnEnabled,
				GS1CompanyPrefix: deref(next.GS1CompanyPrefix), PreviousGS1CompanyPrefix: deref(cur.GS1CompanyPrefix),
				SSCCExtensionDigit: next.SSCCExtensionDigit, PreviousSSCCExtensionDigit: cur.SSCCExtensionDigit,
				PackagingFeeBillable: next.PackagingFeeBillable, PreviousPackagingFeeBillable: cur.PackagingFeeBillable,
				RTPTrackingEnabled: next.RTPTrackingEnabled, PreviousRTPTrackingEnabled: cur.RTPTrackingEnabled,
				RTPChargeDays: next.RTPChargeDays, PreviousRTPChargeDays: cur.RTPChargeDays, Version: next.Version})
		})
		out = res
		return err
	})
	return out, err
}

// checkPrefixNesting refuses a GS1 company prefix that is a proper prefix of,
// or extends, the process prefix or another legal entity's saved prefix.
// GS1 never allocates nested company prefixes, so such a pair is a typo, and
// the two would issue the same SSCC from their separate serial counters (an
// 8-digit prefix and its 7-digit head share every SSCC body whose serial
// starts with the eighth digit). An equal prefix is allowed: it shares the
// serial counter. Another entity's prefix outside the caller's legal-entity
// scope is not visible here; the container's unique SSCC still refuses a
// duplicate at pack time (FUL-0007).
func (s *Postgres) checkPrefixNesting(ctx context.Context, tx pgx.Tx, legalEntityID, prefix string) error {
	refuse := func(other string) error {
		p := httpx.NewProblem(http.StatusUnprocessableEntity, CodeValidation, "request failed validation")
		p.Errors = map[string]string{"gs1CompanyPrefix": "nests with the GS1 company prefix " + other + " already in use; the two would issue the same SSCCs"}
		return p
	}
	if prefix != s.gs1Prefix && (strings.HasPrefix(prefix, s.gs1Prefix) || strings.HasPrefix(s.gs1Prefix, prefix)) {
		return refuse(s.gs1Prefix)
	}
	var other string
	err := tx.QueryRow(ctx, `select gs1_company_prefix from legal_entity_setting
		where legal_entity_id <> $1 and gs1_company_prefix <> $2 and (starts_with($2, gs1_company_prefix) or starts_with(gs1_company_prefix, $2))
		order by gs1_company_prefix limit 1`, legalEntityID, prefix).Scan(&other)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return refuse(other)
}
