package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// maxLineNo bounds the {lineNo} path parameter.
const maxLineNo = 9999

// invoicingPlanBody is the PUT body (FR-17.21). A PERIODIC plan sets start,
// frequency, periods, amountPerPeriod and optionally billingDay (start's day
// when omitted); a PARTIAL plan sets milestones, each with a date and either
// percent or amount. settleWithoutReceipt defaults to true.
type invoicingPlanBody struct {
	Kind                 string          `json:"kind"`
	Start                string          `json:"start,omitempty"`
	Frequency            string          `json:"frequency,omitempty"`
	Periods              int             `json:"periods,omitempty"`
	BillingDay           int             `json:"billingDay,omitempty"`
	AmountPerPeriod      string          `json:"amountPerPeriod,omitempty"`
	Milestones           []milestoneBody `json:"milestones,omitempty"`
	SettleWithoutReceipt *bool           `json:"settleWithoutReceipt,omitempty"`
}

type milestoneBody struct {
	Date    string  `json:"date"`
	Percent *string `json:"percent,omitempty"`
	Amount  *string `json:"amount,omitempty"`
}

// planLine reads and validates the {id} and {lineNo} path parameters.
func planLine(r *http.Request) (string, int, error) {
	id, err := pathID(r)
	if err != nil {
		return "", 0, err
	}
	n, err := strconv.Atoi(chi.URLParam(r, "lineNo"))
	if err != nil || n < 1 || n > maxLineNo {
		return "", 0, invalid("lineNo", "must be a line number from 1 to "+strconv.Itoa(maxLineNo))
	}
	return id, n, nil
}

// dateField parses a YYYY-MM-DD field.
func dateField(field, raw string) (domain.Day, error) {
	if raw == "" {
		return domain.Day{}, invalid(field, "is required")
	}
	d, err := domain.ParseDay(raw)
	if err != nil {
		return domain.Day{}, invalid(field, "must be a date in YYYY-MM-DD form")
	}
	return d, nil
}

// buildPlan validates the PUT body's shape and converts it to the store
// input; the plan rules (totals, percents, dates) are the domain's.
func buildPlan(body invoicingPlanBody) (store.InvoicingPlanInput, error) {
	in := store.InvoicingPlanInput{Kind: body.Kind, SettleWithoutReceipt: body.SettleWithoutReceipt == nil || *body.SettleWithoutReceipt}
	switch body.Kind {
	case domain.PlanPeriodic:
		start, err := dateField("start", body.Start)
		if err != nil {
			return in, err
		}
		if body.Frequency != domain.FreqMonthly && body.Frequency != domain.FreqQuarterly {
			return in, invalid("frequency", "must be MONTHLY or QUARTERLY")
		}
		if body.Periods < 1 || body.Periods > domain.MaxPlanRows {
			return in, invalid("periods", "must be from 1 to "+strconv.Itoa(domain.MaxPlanRows))
		}
		if body.BillingDay < 0 || body.BillingDay > 31 {
			return in, invalid("billingDay", "must be from 1 to 31")
		}
		amount, err := decimalField("amountPerPeriod", body.AmountPerPeriod, "")
		if err != nil {
			return in, err
		}
		in.Periodic = domain.PeriodicSpec{Start: start, Frequency: body.Frequency, Periods: body.Periods,
			BillingDay: body.BillingDay, AmountPerPeriod: amount}
	case domain.PlanPartial:
		if len(body.Milestones) == 0 || len(body.Milestones) > domain.MaxPlanRows {
			return in, invalid("milestones", "must hold from 1 to "+strconv.Itoa(domain.MaxPlanRows)+" milestones")
		}
		for i, m := range body.Milestones {
			field := func(n string) string { return "milestones[" + strconv.Itoa(i) + "]." + n }
			date, err := dateField(field("date"), m.Date)
			if err != nil {
				return in, err
			}
			if (m.Percent == nil) == (m.Amount == nil) {
				return in, invalid(field("percent"), "give either percent or amount")
			}
			row := domain.Milestone{Date: date}
			if m.Percent != nil {
				v, err := decimalField(field("percent"), *m.Percent, "")
				if err != nil {
					return in, err
				}
				row.Percent = &v
			} else {
				v, err := decimalField(field("amount"), *m.Amount, "")
				if err != nil {
					return in, err
				}
				row.Amount = &v
			}
			in.Milestones = append(in.Milestones, row)
		}
	default:
		return in, invalid("kind", "must be PERIODIC or PARTIAL")
	}
	return in, nil
}

// listInvoicingPlans handles GET /purchase-orders/{id}/invoicing-plans: every
// plan of the PO in line order.
func (a *API) listInvoicingPlans(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	plans, err := a.store.ListInvoicingPlans(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, pageBody[domain.InvoicingPlan]{Items: plans})
}

// getInvoicingPlan handles GET /purchase-orders/{id}/lines/{lineNo}/invoicing-plan.
// The ETag is the PO row version, which PUT and DELETE take as If-Match.
func (a *API) getInvoicingPlan(w http.ResponseWriter, r *http.Request) {
	id, lineNo, err := planLine(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.GetInvoicingPlan(r.Context(), id, lineNo)
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(out.PORowVersion))
	httpx.JSON(w, http.StatusOK, out)
}

// putInvoicingPlan handles PUT /purchase-orders/{id}/lines/{lineNo}/invoicing-plan:
// replaces the line's plan (FR-17.21, BR-17.17). If-Match is the PO row
// version (BR-17.8); the answer carries the new one. PUT is idempotent, so no
// Idempotency-Key is taken.
func (a *API) putInvoicingPlan(w http.ResponseWriter, r *http.Request) {
	id, lineNo, err := planLine(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	want, err := ifMatch(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body invoicingPlanBody
	if err := httpx.Decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	in, err := buildPlan(body)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.PutInvoicingPlan(r.Context(), id, lineNo, want, in)
	if err != nil {
		fail(w, r, err)
		return
	}
	a.invoicingPlansSaved.Add(1)
	w.Header().Set("ETag", etag(out.PORowVersion))
	httpx.JSON(w, http.StatusOK, out)
}

type planRowSettlementBody struct {
	SettlementRef string `json:"settlementRef"`
	Amount        string `json:"amount"`
}

const (
	maxSettlementRef = 255
	settlingService  = "receiving"
)

// settlePlanRow handles PUT .../invoicing-plan/rows/{seq}/settlement: receiving
// reports that it settled the row (#289), which flips it OPEN -> INVOICED. PUT is
// idempotent per row and settlementRef, so a retry after a lost answer is safe.
func (a *API) settlePlanRow(w http.ResponseWriter, r *http.Request) {
	id, lineNo, err := planLine(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	seq, err := strconv.Atoi(chi.URLParam(r, "seq"))
	if err != nil || seq < 1 || seq > domain.MaxPlanRows {
		fail(w, r, invalid("seq", "must be a row number from 1 to "+strconv.Itoa(domain.MaxPlanRows)))
		return
	}
	if _, err := caller(r.Context()); err != nil {
		fail(w, r, err)
		return
	}
	if p, _ := tenant.FromContext(r.Context()); p.Service != settlingService {
		fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "plan row settlements are accepted from the receiving service only"))
		return
	}
	var body planRowSettlementBody
	if err := httpx.Decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	if body.SettlementRef == "" || len(body.SettlementRef) > maxSettlementRef {
		fail(w, r, invalid("settlementRef", "is required and at most "+strconv.Itoa(maxSettlementRef)+" characters"))
		return
	}
	amount, err := decimalField("amount", body.Amount, "")
	if err == nil && !amount.IsPositive() {
		err = invalid("amount", "must be positive")
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.MarkPlanRowInvoiced(r.Context(), id, lineNo, seq, body.SettlementRef, amount)
	var problem *httpx.Problem
	if errors.As(err, &problem) && problem.Code == domain.CodePlanRowSettlement {
		a.planRowConflicts.Add(1)
		slog.ErrorContext(r.Context(), "plan row settlement conflicts with the purchase order; a person must resolve it", "err", err,
			"purchaseOrderId", id, "lineNo", lineNo, "seq", seq, "settlementRef", body.SettlementRef, "correlationId", middleware.GetReqID(r.Context()))
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	if !out.Replayed {
		a.planRowsInvoiced.Add(1)
	}
	httpx.JSON(w, http.StatusOK, out)
}

// deleteInvoicingPlan handles DELETE /purchase-orders/{id}/lines/{lineNo}/invoicing-plan.
// If-Match is the PO row version; 204 with the new one as the ETag.
func (a *API) deleteInvoicingPlan(w http.ResponseWriter, r *http.Request) {
	id, lineNo, err := planLine(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	want, err := ifMatch(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	v, err := a.store.DeleteInvoicingPlan(r.Context(), id, lineNo, want)
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(v))
	w.WriteHeader(http.StatusNoContent)
}
