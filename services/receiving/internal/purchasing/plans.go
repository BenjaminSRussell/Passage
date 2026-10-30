package purchasing

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
)

const planDateLayout = "2006-01-02"

type planRowBody struct {
	Seq     int             `json:"seq"`
	DueDate string          `json:"dueDate"`
	Amount  decimal.Decimal `json:"amount"`
	Status  string          `json:"status"`
}

type planBody struct {
	PurchaseOrderID      string        `json:"purchaseOrderId"`
	LineNo               int           `json:"lineNo"`
	SettleWithoutReceipt bool          `json:"settleWithoutReceipt"`
	Currency             string        `json:"currency"`
	Rows                 []planRowBody `json:"rows"`
}

func (c *Client) List(ctx context.Context, orderID string) ([]domain.InvoicingPlan, error) {
	const what = "invoicing plans get"
	status, raw, err := c.fetch(ctx, what, orderID, fmt.Sprintf(domain.InvoicingPlansPathTmpl, url.PathEscape(orderID)))
	if err != nil {
		return nil, err
	}
	switch status {
	case http.StatusOK:
		var page struct {
			Items []planBody `json:"items"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			slog.ErrorContext(ctx, "invoicing plans response unusable", "err", err, "purchaseOrderId", orderID, "correlationId", middleware.GetReqID(ctx))
			return nil, fmt.Errorf("invoicing plans get %s: response is not a plan list", orderID)
		}
		plans, err := toDomainPlans(page.Items)
		if err != nil {
			slog.ErrorContext(ctx, "invoicing plans response unusable", "err", err, "purchaseOrderId", orderID, "correlationId", middleware.GetReqID(ctx))
			return nil, fmt.Errorf("invoicing plans get %s: %w", orderID, err)
		}
		return plans, nil
	case http.StatusNotFound:
		return nil, fmt.Errorf("invoicing plans get %s: %w", orderID, domain.ErrPurchaseOrderNotFound)
	case http.StatusForbidden:
		return nil, fmt.Errorf("invoicing plans get %s: %w", orderID, domain.ErrPurchaseOrderForbidden)
	}
	err = fmt.Errorf("invoicing plans get %s: unexpected status %d", orderID, status)
	slog.ErrorContext(ctx, "invoicing plans get rejected", "err", err, "status", status, "purchaseOrderId", orderID, "correlationId", middleware.GetReqID(ctx))
	return nil, err
}

func toDomainPlans(items []planBody) ([]domain.InvoicingPlan, error) {
	plans := make([]domain.InvoicingPlan, 0, len(items))
	for _, p := range items {
		plan := domain.InvoicingPlan{PurchaseOrderID: p.PurchaseOrderID, LineNo: p.LineNo, SettleWithoutReceipt: p.SettleWithoutReceipt, Currency: p.Currency}
		for _, r := range p.Rows {
			due, err := time.Parse(planDateLayout, r.DueDate)
			if err != nil || r.Seq < 1 {
				return nil, fmt.Errorf("line %d row %d has no usable due date", p.LineNo, r.Seq)
			}
			plan.Rows = append(plan.Rows, domain.PlanRow{Seq: r.Seq, DueDate: due, Amount: r.Amount, Status: r.Status})
		}
		plans = append(plans, plan)
	}
	return plans, nil
}
