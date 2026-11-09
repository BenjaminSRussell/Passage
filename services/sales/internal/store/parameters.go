package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
)

// SalesParameters are the stored parameters of one legal entity. Version is 0
// for a legal entity that never saved them (the defaults apply), and then
// UpdatedBy and UpdatedAt are absent.
type SalesParameters struct {
	LegalEntityID string `json:"legalEntityId"`
	domain.Parameters
	Version   int        `json:"version"`
	UpdatedBy string     `json:"updatedBy,omitempty"`
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
}

// loadParameters returns the parameters of a legal entity, or the defaults.
// Row-level security hides a legal entity outside the caller's scope, which
// then reads as the defaults; the API refuses such a legal entity first.
func loadParameters(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID string) (SalesParameters, error) {
	out := SalesParameters{LegalEntityID: legalEntityID, Parameters: domain.DefaultParameters()}
	var updatedAt time.Time
	var oneTimeLimit *string
	err := tx.QueryRow(ctx, `select quotation_expiry_days, quotation_probability_default, credit_release_validity_days,
			credit_hold_sla_business_days, default_order_type, one_time_order_limit::text, one_time_order_limit_currency, credit_check_points, intake_dedup_window_days, delivery_date_control_default, sales_lead_time_days, partial_delivery_default, version, updated_by, updated_at
		from sales_parameter where tenant_id = $1 and legal_entity_id = $2`, tenantID, legalEntityID).
		Scan(&out.QuotationExpiryDays, &out.QuotationProbabilityDefault, &out.CreditReleaseValidityDays,
			&out.CreditHoldSLABusinessDays, &out.DefaultOrderType, &oneTimeLimit, &out.OneTimeOrderLimitCurrency, &out.CreditCheckPoints, &out.IntakeDedupWindowDays, &out.DeliveryDateControlDefault, &out.SalesLeadTimeDays, &out.PartialDeliveryDefault, &out.Version, &out.UpdatedBy, &updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if oneTimeLimit != nil {
		limit, err := decimal.NewFromString(*oneTimeLimit)
		if err != nil {
			return out, err
		}
		out.OneTimeOrderLimit = &limit
	}
	out.UpdatedAt = &updatedAt
	return out, nil
}

// parameterCache loads each legal entity's parameters once per transaction
// (the hold queue reads them for every row).
type parameterCache struct {
	tx       pgx.Tx
	tenantID string
	byLE     map[string]domain.Parameters
}

func newParameterCache(tx pgx.Tx, tenantID string) *parameterCache {
	return &parameterCache{tx: tx, tenantID: tenantID, byLE: map[string]domain.Parameters{}}
}

func (c *parameterCache) get(ctx context.Context, legalEntityID string) (domain.Parameters, error) {
	if p, ok := c.byLE[legalEntityID]; ok {
		return p, nil
	}
	p, err := loadParameters(ctx, c.tx, c.tenantID, legalEntityID)
	if err != nil {
		return domain.Parameters{}, err
	}
	c.byLE[legalEntityID] = p.Parameters
	return p.Parameters, nil
}

// GetParameters returns the parameters of a legal entity or the defaults.
func (s *Postgres) GetParameters(ctx context.Context, legalEntityID string) (SalesParameters, error) {
	p, _ := tenant.FromContext(ctx)
	var out SalesParameters
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = loadParameters(ctx, tx, p.TenantID, legalEntityID)
		return err
	})
	return out, err
}

// PutParameters creates the parameters when version is 0 and otherwise
// replaces them when version matches the stored one; any other version is a
// version conflict. An empty PartialDeliveryDefault keeps the stored policy
// (ALLOWED on create), as PUT /change-policy keeps what it does not name.
// parameters.changed is written in the same transaction.
func (s *Postgres) PutParameters(ctx context.Context, in SalesParameters, version int, actor string) (SalesParameters, error) {
	p, _ := tenant.FromContext(ctx)
	var out SalesParameters
	if len(in.CreditCheckPoints) == 0 {
		in.CreditCheckPoints = domain.DefaultParameters().CreditCheckPoints
	}
	if in.IntakeDedupWindowDays == 0 {
		in.IntakeDedupWindowDays = domain.DefaultIntakeDedupWindowDays
	}
	if in.DeliveryDateControlDefault == "" {
		in.DeliveryDateControlDefault = domain.DeliveryControlNone
	}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var stored int
		var err error
		var limit *string
		if in.OneTimeOrderLimit != nil {
			l := in.OneTimeOrderLimit.StringFixed(2)
			limit = &l
		}
		if version == 0 {
			err = tx.QueryRow(ctx, `insert into sales_parameter (tenant_id, legal_entity_id, quotation_expiry_days, quotation_probability_default,
					credit_release_validity_days, credit_hold_sla_business_days, default_order_type, one_time_order_limit, one_time_order_limit_currency, credit_check_points, intake_dedup_window_days, delivery_date_control_default, sales_lead_time_days, partial_delivery_default, updated_by)
				values ($1, $2, $3, $4, $5, $6, $7, $8::numeric, $9, $10, $11, $12, $13, coalesce(nullif($14, ''), 'ALLOWED'), $15) on conflict do nothing returning version`,
				p.TenantID, in.LegalEntityID, in.QuotationExpiryDays, in.QuotationProbabilityDefault, in.CreditReleaseValidityDays,
				in.CreditHoldSLABusinessDays, in.DefaultOrderType, limit, in.OneTimeOrderLimitCurrency, in.CreditCheckPoints, in.IntakeDedupWindowDays, in.DeliveryDateControlDefault, in.SalesLeadTimeDays, in.PartialDeliveryDefault, actor).Scan(&stored)
		} else {
			err = tx.QueryRow(ctx, `update sales_parameter set quotation_expiry_days = $3, quotation_probability_default = $4,
					credit_release_validity_days = $5, credit_hold_sla_business_days = $6, default_order_type = $7,
					one_time_order_limit = $8::numeric, one_time_order_limit_currency = $9, credit_check_points = $10, intake_dedup_window_days = $11, delivery_date_control_default = $12, sales_lead_time_days = $13, partial_delivery_default = coalesce(nullif($14, ''), partial_delivery_default), updated_by = $15,
					version = version + 1, updated_at = now()
				where tenant_id = $1 and legal_entity_id = $2 and version = $16 returning version`,
				p.TenantID, in.LegalEntityID, in.QuotationExpiryDays, in.QuotationProbabilityDefault, in.CreditReleaseValidityDays,
				in.CreditHoldSLABusinessDays, in.DefaultOrderType, limit, in.OneTimeOrderLimitCurrency, in.CreditCheckPoints, in.IntakeDedupWindowDays, in.DeliveryDateControlDefault, in.SalesLeadTimeDays, in.PartialDeliveryDefault, actor, version).Scan(&stored)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return versionConflict()
		}
		if err != nil {
			return err
		}
		out, err = loadParameters(ctx, tx, p.TenantID, in.LegalEntityID)
		if err != nil {
			return err
		}
		return enqueue(ctx, tx, "parameters", in.LegalEntityID, salesevents.ParametersChanged, p.TenantID, salesevents.ParametersChangedData{
			LegalEntityID: in.LegalEntityID, QuotationExpiryDays: out.QuotationExpiryDays, QuotationProbabilityDefault: out.QuotationProbabilityDefault,
			CreditReleaseValidityDays: out.CreditReleaseValidityDays, CreditHoldSLABusinessDays: out.CreditHoldSLABusinessDays,
			DefaultOrderType: out.DefaultOrderType, OneTimeOrderLimit: limit, OneTimeOrderLimitCurrency: out.OneTimeOrderLimitCurrency,
			CreditCheckPoints: out.CreditCheckPoints, IntakeDedupWindowDays: out.IntakeDedupWindowDays,
			DeliveryDateControlDefault: out.DeliveryDateControlDefault, SalesLeadTimeDays: out.SalesLeadTimeDays,
			PartialDeliveryDefault: out.PartialDeliveryDefault, Actor: actor, Version: out.Version})
	})
	return out, err
}
