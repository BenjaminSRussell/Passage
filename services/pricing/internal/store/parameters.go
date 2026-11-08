package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	pevents "github.com/BenjaminSRussell/passage/services/pricing/internal/events"
)

// Parameters are the pricing parameters of one legal entity (PRD 5.26.13). A legal entity
// with no stored row reads as the defaults at Version 0.
type Parameters struct {
	LegalEntityID string
	// JournalFourEyes makes the user who created a price journal unable to post it (PRC-0057).
	JournalFourEyes bool
	// PricingDateBasis names the document date that is the pricing date when the caller sends
	// none (BR-26.2): SHIP_DATE (default), ORDER_DATE or DELIVERY_DATE.
	PricingDateBasis string
	// TotalDiscountBasis is what TOTAL_DISC applies to: AFTER_LINE_DISC (default) or GROSS.
	TotalDiscountBasis string
	// SmartRoundingBands is the effective smart rounding table of price journal adjustments
	// (FR-26.7); the PRD default table when none was stored.
	SmartRoundingBands []domain.BandSpec
	// PriceSearchMode is the default of searchNext on a new condition record: FIRST_MATCH
	// (default) or SEARCH_NEXT (BR-26.1 step 5).
	PriceSearchMode string
	// SmallOrderThresholds is the SURCHARGE threshold per currency (FR-26.6) as decimal
	// strings; a currency it does not name keeps the reference threshold, and an empty table
	// is the reference table.
	SmallOrderThresholds map[string]string
	// FreeGoodsCalcRule is the calculation rule a new free goods rule takes when its create
	// request names none (BR-26.13): WHOLE_UNITS (default), PROPORTIONAL or UNIT_REFERENCE.
	FreeGoodsCalcRule string
	Version           int
	UpdatedBy         string
	UpdatedAt         *time.Time
}

// DefaultParameters are the parameters of a legal entity with nothing stored (version 0).
func DefaultParameters(legalEntityID string) Parameters {
	return Parameters{LegalEntityID: legalEntityID, PricingDateBasis: domain.DateBasisShip,
		TotalDiscountBasis: domain.TotalBasisAfterLineDisc, SmartRoundingBands: domain.DefaultBandSpecs(),
		PriceSearchMode: domain.SearchModeFirstMatch, SmallOrderThresholds: map[string]string{},
		FreeGoodsCalcRule: domain.CalcWholeUnits}
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
	// A value left empty takes its default (the API requires every value).
	d := DefaultParameters(in.LegalEntityID)
	if in.PricingDateBasis == "" {
		in.PricingDateBasis = d.PricingDateBasis
	}
	if in.TotalDiscountBasis == "" {
		in.TotalDiscountBasis = d.TotalDiscountBasis
	}
	if len(in.SmartRoundingBands) == 0 {
		in.SmartRoundingBands = d.SmartRoundingBands
	}
	if in.PriceSearchMode == "" {
		in.PriceSearchMode = d.PriceSearchMode
	}
	if in.FreeGoodsCalcRule == "" {
		in.FreeGoodsCalcRule = d.FreeGoodsCalcRule
	}
	if !domain.ValidCalcRule(in.FreeGoodsCalcRule) {
		return Parameters{}, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeValidation, "free goods calculation rule is not allowed")
	}
	if !domain.ValidDateBasis(in.PricingDateBasis) || !domain.ValidTotalBasis(in.TotalDiscountBasis) || !domain.ValidSearchMode(in.PriceSearchMode) {
		return Parameters{}, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeValidation, "parameter basis is not allowed")
	}
	bands, faults := domain.ParseBands(in.SmartRoundingBands)
	if faults != nil {
		return Parameters{}, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeValidation, "smart rounding bands are invalid")
	}
	in.SmartRoundingBands = domain.BandSpecs(bands)
	thresholds, faults := domain.ParseThresholds(in.SmallOrderThresholds)
	if faults != nil {
		return Parameters{}, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeValidation, "small order thresholds are invalid")
	}
	in.SmallOrderThresholds = domain.CanonicalThresholds(thresholds)
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		bandsJSON, err := json.Marshal(in.SmartRoundingBands)
		if err != nil {
			return err
		}
		// An empty table is stored as NULL (the reference table); encoding/json sorts the keys,
		// so equal tables hash alike.
		var thresholdsStored any
		thresholdsJSON := []byte("{}")
		if len(in.SmallOrderThresholds) > 0 {
			if thresholdsJSON, err = json.Marshal(in.SmallOrderThresholds); err != nil {
				return err
			}
			thresholdsStored = string(thresholdsJSON)
		}
		hash := commandHash("parameters-set", in.LegalEntityID, strconv.FormatBool(in.JournalFourEyes), strconv.Itoa(version),
			in.PricingDateBasis, in.TotalDiscountBasis, string(bandsJSON), in.PriceSearchMode, string(thresholdsJSON), in.FreeGoodsCalcRule)
		if ok, err := replayJSON(ctx, tx, p.TenantID, idempotencyKey, hash, &out); err != nil || ok {
			return err
		}
		// The default table is stored as NULL, so a later change of the default reaches entities
		// that never customised it.
		var stored any
		if !sameBands(in.SmartRoundingBands, domain.DefaultBandSpecs()) {
			stored = string(bandsJSON)
		}
		var tag pgconn.CommandTag
		if version == 0 {
			tag, err = tx.Exec(ctx, `insert into pricing_parameter (tenant_id, legal_entity_id, journal_four_eyes, pricing_date_basis,
					total_discount_basis, smart_rounding_bands, updated_by, price_search_mode, small_order_thresholds, free_goods_calc_rule)
				values ($1, $2, $3, $4, $5, $6::jsonb, $7, $8, $9::jsonb, $10) on conflict (tenant_id, legal_entity_id) do nothing`,
				p.TenantID, in.LegalEntityID, in.JournalFourEyes, in.PricingDateBasis, in.TotalDiscountBasis, stored, p.UserID,
				in.PriceSearchMode, thresholdsStored, in.FreeGoodsCalcRule)
			if err != nil {
				return err
			}
		} else {
			tag, err = tx.Exec(ctx, `update pricing_parameter set journal_four_eyes = $3, pricing_date_basis = $6, total_discount_basis = $7,
					smart_rounding_bands = $8::jsonb, price_search_mode = $9, small_order_thresholds = $10::jsonb, free_goods_calc_rule = $11,
					version = version + 1, updated_by = $4, updated_at = now()
				where tenant_id = $5 and legal_entity_id = $1 and version = $2`,
				in.LegalEntityID, version, in.JournalFourEyes, p.UserID, p.TenantID, in.PricingDateBasis, in.TotalDiscountBasis, stored,
				in.PriceSearchMode, thresholdsStored, in.FreeGoodsCalcRule)
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
			LegalEntityID: out.LegalEntityID, JournalFourEyes: out.JournalFourEyes, PricingDateBasis: out.PricingDateBasis,
			TotalDiscountBasis: out.TotalDiscountBasis, SmartRoundingBands: bandData(out.SmartRoundingBands), PriceSearchMode: out.PriceSearchMode,
			SmallOrderThresholds: out.SmallOrderThresholds, FreeGoodsCalcRule: out.FreeGoodsCalcRule, Version: out.Version, UpdatedBy: out.UpdatedBy}); err != nil {
			return err
		}
		return rememberJSON(ctx, tx, p.TenantID, idempotencyKey, hash, out)
	})
	return out, err
}

func loadParameters(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID string) (Parameters, error) {
	out := DefaultParameters(legalEntityID)
	var bands, thresholds []byte
	err := tx.QueryRow(ctx, `select journal_four_eyes, pricing_date_basis, total_discount_basis, smart_rounding_bands, price_search_mode,
			small_order_thresholds, free_goods_calc_rule, version, updated_by, updated_at
		from pricing_parameter where tenant_id = $1 and legal_entity_id = $2`, tenantID, legalEntityID).
		Scan(&out.JournalFourEyes, &out.PricingDateBasis, &out.TotalDiscountBasis, &bands, &out.PriceSearchMode, &thresholds,
			&out.FreeGoodsCalcRule, &out.Version, &out.UpdatedBy, &out.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return DefaultParameters(legalEntityID), nil
	}
	if err != nil {
		return out, err
	}
	if bands != nil {
		out.SmartRoundingBands = nil
		if err := json.Unmarshal(bands, &out.SmartRoundingBands); err != nil {
			return out, err
		}
	}
	if thresholds != nil {
		if err := json.Unmarshal(thresholds, &out.SmallOrderThresholds); err != nil {
			return out, err
		}
	}
	return out, nil
}

// sameBands reports whether two smart rounding tables are equal.
func sameBands(a, b []domain.BandSpec) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func bandData(specs []domain.BandSpec) []pevents.BandData {
	out := make([]pevents.BandData, len(specs))
	for i, b := range specs {
		out[i] = pevents.BandData{UpTo: b.UpTo, Ending: b.Ending}
	}
	return out
}
