package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/costacct/internal/domain"
)

// SettlementRequest settles Amount of an internal order's collected cost for
// Period to a receiver cost object (FR-38.9).
type SettlementRequest struct {
	OrderID            string
	Period             string
	Currency           string
	ReceiverID         string
	SecondaryElementID string
	Amount             decimal.Decimal
}

// Settlement is one stored settlement of an internal order.
type Settlement struct {
	ID                 string          `json:"id"`
	LegalEntityID      string          `json:"legalEntityId"`
	OrderID            string          `json:"orderId"`
	Period             string          `json:"period"`
	Currency           string          `json:"currency"`
	ReceiverID         string          `json:"receiverCostObjectId"`
	SecondaryElementID string          `json:"secondaryElementId"`
	Amount             decimal.Decimal `json:"amount"`
	CollectedBefore    decimal.Decimal `json:"collectedBefore"`
	Remaining          decimal.Decimal `json:"remaining"`
	CreatedAt          string          `json:"createdAt"`
}

// SettleInternalOrder moves Amount from the internal order to the receiver in
// one transaction (FR-38.9). The order is credited and the receiver debited on
// the settlement element, so the settlement nets to zero. Collected cost is the
// net of the order's entries for the period and currency, so earlier
// settlements already reduce it; the order row is locked while it is read, and
// a settlement above it is refused with CO-0618 (BR-38.9).
func (s *Postgres) SettleInternalOrder(ctx context.Context, key, hash string, in SettlementRequest) (int, []byte, error) {
	p, _ := tenant.FromContext(ctx)
	return s.run(ctx, key, hash, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		var legalEntityID, kind string
		err := tx.QueryRow(ctx, `select legal_entity_id::text, kind from cost_object where tenant_id = $1 and id = $2 for update`,
			p.TenantID, in.OrderID).Scan(&legalEntityID, &kind)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil, problem(http.StatusNotFound, CodeObjectNotFound, "internal order not found")
		}
		if err != nil {
			return 0, nil, err
		}
		if kind != domain.KindInternalOrder {
			return 0, nil, problem(http.StatusUnprocessableEntity, CodeNotInternalOrder, "only an internal order can be settled; this cost object is a "+kind)
		}
		if err := requireObjects(ctx, tx, p.TenantID, legalEntityID, []string{in.ReceiverID}); err != nil {
			return 0, nil, err
		}
		var elementKind string
		err = tx.QueryRow(ctx, `select kind from cost_element where tenant_id = $1 and legal_entity_id = $2 and id = $3`,
			p.TenantID, legalEntityID, in.SecondaryElementID).Scan(&elementKind)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && elementKind != domain.ElementSecondary) {
			return 0, nil, problem(http.StatusUnprocessableEntity, CodeSecondaryNotFound, "secondary cost element not found")
		}
		if err != nil {
			return 0, nil, err
		}
		collected, err := bookedOn(ctx, tx, p.TenantID, in.OrderID, "", in.Period, in.Currency)
		if err != nil {
			return 0, nil, err
		}
		if err := domain.CheckSettlement(in.Amount, collected); err != nil {
			if errors.Is(err, domain.ErrOverSettlement) {
				return 0, nil, problem(http.StatusUnprocessableEntity, CodeOverSettlement,
					fmt.Sprintf("Settlement %s exceeds collected cost %s", in.Amount.StringFixed(2), collected.StringFixed(2)))
			}
			return 0, nil, problem(http.StatusUnprocessableEntity, CodeInvalidAllocation, err.Error())
		}
		created := time.Now().UTC()
		st := Settlement{ID: ids.New(), LegalEntityID: legalEntityID, OrderID: in.OrderID, Period: in.Period, Currency: in.Currency,
			ReceiverID: in.ReceiverID, SecondaryElementID: in.SecondaryElementID, Amount: in.Amount, CollectedBefore: collected,
			Remaining: collected.Sub(in.Amount), CreatedAt: stamp(created)}
		if _, err := tx.Exec(ctx, `insert into internal_order_settlement (tenant_id, id, legal_entity_id, period_key, currency, order_id, receiver_id,
			secondary_element_id, amount, collected_before, created_at) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
			p.TenantID, st.ID, legalEntityID, in.Period, in.Currency, in.OrderID, in.ReceiverID, in.SecondaryElementID,
			in.Amount.StringFixed(4), collected.StringFixed(4), created); err != nil {
			return 0, nil, err
		}
		if err := postEntry(ctx, tx, p.TenantID, legalEntityID, in.OrderID, in.SecondaryElementID, in.Period, in.Currency,
			in.Amount.Neg(), st.ID, "settlement_id"); err != nil {
			return 0, nil, err
		}
		if err := postEntry(ctx, tx, p.TenantID, legalEntityID, in.ReceiverID, in.SecondaryElementID, in.Period, in.Currency,
			in.Amount, st.ID, "settlement_id"); err != nil {
			return 0, nil, err
		}
		return http.StatusCreated, st, nil
	})
}

// ListSettlements lists an internal order's settlements, newest first; period narrows it when set.
func (s *Postgres) ListSettlements(ctx context.Context, orderID, period string) ([]Settlement, error) {
	out := []Settlement{}
	err := s.read(ctx, func(tx pgx.Tx, tenantID string) error {
		rows, err := tx.Query(ctx, `select id::text, legal_entity_id::text, order_id::text, period_key, currency, receiver_id::text,
			secondary_element_id::text, amount::text, collected_before::text, created_at from internal_order_settlement
			where tenant_id = $1 and order_id = $2 and ($3 = '' or period_key = $3) order by created_at desc, id limit $4`,
			tenantID, orderID, period, listLimit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var st Settlement
			var amount, before string
			var created time.Time
			if err := rows.Scan(&st.ID, &st.LegalEntityID, &st.OrderID, &st.Period, &st.Currency, &st.ReceiverID, &st.SecondaryElementID,
				&amount, &before, &created); err != nil {
				return err
			}
			st.Amount, st.CollectedBefore, st.CreatedAt = parseDec(amount), parseDec(before), stamp(created)
			st.Remaining = st.CollectedBefore.Sub(st.Amount)
			out = append(out, st)
		}
		return rows.Err()
	})
	return out, err
}
