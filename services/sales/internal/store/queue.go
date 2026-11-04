package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
)

// HoldQueueItem is one ACTIVE hold in the release queue (PRD §5.27.6 credit
// release queue, §5.27.14): the order, its amount when held, the rule that
// failed, and the customer's credit limit so exposure can be read against it.
type HoldQueueItem struct {
	HoldID        string          `json:"holdId"`
	LegalEntityID string          `json:"legalEntityId"`
	OrderID       string          `json:"orderId"`
	OrderNumber   string          `json:"orderNumber"`
	OrderVersion  int             `json:"orderVersion"`
	CustomerID    string          `json:"customerId"`
	HoldType      string          `json:"holdType"`
	ReasonCode    string          `json:"reasonCode"`
	Amount        decimal.Decimal `json:"amount"`
	Currency      string          `json:"currency"`
	AppliedBy     string          `json:"appliedBy"`
	AppliedAt     time.Time       `json:"appliedAt"`
	// FailedRule is the first failing rule of the order's last credit check
	// (CREDIT holds): for CR-1 its value is exposure plus the order and its
	// threshold the limit with tolerance.
	FailedRule *domain.RuleResult `json:"failedRule,omitempty"`
	// SLADueAt is AppliedAt plus the legal entity's release SLA in business
	// days (FR-27.8); the queue marks a hold past it.
	SLADueAt time.Time `json:"slaDueAt"`
	// CreditLimit and LimitCurrency come from the customer's credit profile
	// when one exists.
	CreditLimit   *decimal.Decimal `json:"creditLimit,omitempty"`
	LimitCurrency *string          `json:"limitCurrency,omitempty"`
	// OrderStatus tells the queue which decisions apply: only a CREDIT hold
	// of a DRAFT or OPEN order can be rejected (slice 26).
	OrderStatus string `json:"orderStatus"`
}

// HoldQueueFilter narrows the queue. Empty fields match everything; an empty
// LegalEntityID means every legal entity in the caller's scope.
type HoldQueueFilter struct {
	LegalEntityID string
	HoldType      string
	Limit         int
}

// ListHolds returns ACTIVE holds, oldest first, so the queue is worked in
// age order against the release SLA (FR-27.8).
func (s *Postgres) ListHolds(ctx context.Context, f HoldQueueFilter) ([]HoldQueueItem, error) {
	p, _ := tenant.FromContext(ctx)
	out := []HoldQueueItem{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select h.id::text, h.legal_entity_id::text, h.order_id::text, o.order_number, o.version, o.status, o.customer_id::text,
				h.hold_type, h.reason_code, h.order_amount::text, o.currency::text, h.applied_by, h.applied_at,
				o.credit_check_result::text, c.credit_limit::text, c.currency::text
			from order_hold h
			join sales_order o on o.tenant_id = h.tenant_id and o.id = h.order_id
			left join credit_profile c on c.tenant_id = o.tenant_id and c.legal_entity_id = o.legal_entity_id and c.customer_id = o.customer_id
			where h.tenant_id = $1 and h.status = $2 and ($3 = '' or h.legal_entity_id::text = $3) and ($4 = '' or h.hold_type = $4)
			order by h.applied_at, h.id limit $5`,
			p.TenantID, domain.HoldActive, f.LegalEntityID, f.HoldType, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var it HoldQueueItem
			var amount string
			var check, limit *string
			if err := rows.Scan(&it.HoldID, &it.LegalEntityID, &it.OrderID, &it.OrderNumber, &it.OrderVersion, &it.OrderStatus, &it.CustomerID, &it.HoldType,
				&it.ReasonCode, &amount, &it.Currency, &it.AppliedBy, &it.AppliedAt, &check, &limit, &it.LimitCurrency); err != nil {
				return err
			}
			it.Amount = decimal.RequireFromString(amount)
			if limit != nil {
				l := decimal.RequireFromString(*limit)
				it.CreditLimit = &l
			}
			if it.HoldType == domain.HoldCredit && check != nil {
				it.FailedRule, err = failedRule(*check, it.ReasonCode)
				if err != nil {
					return err
				}
			}
			out = append(out, it)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()
		params := newParameterCache(tx, p.TenantID)
		for i := range out {
			le, err := params.get(ctx, out[i].LegalEntityID)
			if err != nil {
				return err
			}
			out[i].SLADueAt = domain.AddBusinessDays(out[i].AppliedAt, le.CreditHoldSLABusinessDays)
		}
		return nil
	})
	return out, err
}

// failedRule picks the rule a CREDIT hold names (its reason code) from the
// stored evaluation, or the first failing rule when the code is not a rule id.
func failedRule(stored, reasonCode string) (*domain.RuleResult, error) {
	var ev domain.CreditEvaluation
	if err := json.Unmarshal([]byte(stored), &ev); err != nil {
		return nil, err
	}
	var first *domain.RuleResult
	for i := range ev.Rules {
		r := ev.Rules[i]
		if r.Result != domain.RuleFail {
			continue
		}
		if r.ID == reasonCode {
			return &r, nil
		}
		if first == nil {
			first = &r
		}
	}
	return first, nil
}
