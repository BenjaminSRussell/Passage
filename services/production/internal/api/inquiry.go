package api

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	"github.com/BenjaminSRussell/passage/services/production/internal/store"
)

// maxReportPage bounds a page of the WIP balance and variance reports.
const maxReportPage = 500

var varianceTypes = map[domain.VarianceType]bool{"lot-size": true, "production-quantity": true, "production-price": true,
	"production-substitution": true, "rework": true}

// wipRowView is one order of the WIP balance report, amounts to the currency's minor unit.
type wipRowView struct {
	OrderID       string        `json:"orderId"`
	OrderNumber   string        `json:"orderNumber"`
	LegalEntityID string        `json:"legalEntityId"`
	SiteID        string        `json:"siteId"`
	ProductID     string        `json:"productId"`
	Status        domain.Status `json:"status"`
	Currency      string        `json:"currency"`
	Consumption   string        `json:"consumption"`
	RAFValue      string        `json:"rafValue"`
	Settled       string        `json:"settled"`
	WIPBalance    string        `json:"wipBalance"`
}

type wipTotalView struct {
	Currency    string `json:"currency"`
	Orders      int    `json:"orders"`
	Consumption string `json:"consumption"`
	RAFValue    string `json:"rafValue"`
	Settled     string `json:"settled"`
	WIPBalance  string `json:"wipBalance"`
}

type wipReportView struct {
	AsOf       string         `json:"asOf"`
	Items      []wipRowView   `json:"items"`
	Totals     []wipTotalView `json:"totals"`
	NextCursor string         `json:"nextCursor,omitempty"`
}

func fixed(v decimal.Decimal, currency string) string {
	return v.StringFixed(money.MinorUnits(currency))
}

// reportLimit reads ?limit= (1 to maxReportPage, default defaultPageSize).
func reportLimit(raw string, fields map[string]string) int {
	if raw == "" {
		return defaultPageSize
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > maxReportPage {
		fields["limit"] = fmt.Sprintf("must be a whole number from 1 to %d", maxReportPage)
	}
	return n
}

// wipReport serves the WIP balance by order (PRD 4.21.14) as of a date (default today, UTC).
func (a *API) wipReport(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	f := store.WIPFilter{LegalEntityID: q.Get("legalEntityId"), SiteID: q.Get("siteId"), Status: domain.Status(q.Get("status")),
		AsOf: q.Get("asOf"), Cursor: q.Get("cursor")}
	f.Limit = reportLimit(q.Get("limit"), fields)
	if f.AsOf == "" {
		f.AsOf = time.Now().UTC().Format(time.DateOnly)
	} else if !isDate(f.AsOf) {
		fields["asOf"] = "must be a date (YYYY-MM-DD)"
	}
	if f.Status != "" && !f.Status.Valid() {
		fields["status"] = "must be a production order status"
	}
	for name, v := range map[string]string{"legalEntityId": f.LegalEntityID, "siteId": f.SiteID, "cursor": f.Cursor} {
		if v != "" && !ids.Valid(v) {
			fields[name] = "must be a UUID"
		}
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	rep, err := a.store.WIPReport(r.Context(), f)
	if err != nil {
		a.fail(w, r, "read the WIP balance report", err)
		return
	}
	v := wipReportView{AsOf: f.AsOf, Items: make([]wipRowView, 0, len(rep.Items)), Totals: make([]wipTotalView, 0, len(rep.Totals)), NextCursor: rep.NextCursor}
	for _, x := range rep.Items {
		v.Items = append(v.Items, wipRowView{OrderID: x.OrderID, OrderNumber: x.OrderNumber, LegalEntityID: x.LegalEntityID, SiteID: x.SiteID,
			ProductID: x.ProductID, Status: x.Status, Currency: x.Currency, Consumption: fixed(x.Consumption, x.Currency),
			RAFValue: fixed(x.RAFValue, x.Currency), Settled: fixed(x.Settled, x.Currency), WIPBalance: fixed(x.Balance, x.Currency)})
	}
	for _, t := range rep.Totals {
		v.Totals = append(v.Totals, wipTotalView{Currency: t.Currency, Orders: t.Orders, Consumption: fixed(t.Consumption, t.Currency),
			RAFValue: fixed(t.RAFValue, t.Currency), Settled: fixed(t.Settled, t.Currency), WIPBalance: fixed(t.Balance, t.Currency)})
	}
	httpx.JSON(w, http.StatusOK, v)
}

type varianceRowView struct {
	ID           string              `json:"id"`
	OrderID      string              `json:"orderId"`
	OrderNumber  string              `json:"orderNumber"`
	SiteID       string              `json:"siteId"`
	ItemGroup    string              `json:"itemGroup,omitempty"`
	Currency     string              `json:"currency"`
	Type         domain.VarianceType `json:"type"`
	CostGroup    domain.CostGroup    `json:"costGroup"`
	Amount       string              `json:"amount"`
	Planned      string              `json:"planned"`
	PctOfPlanned *string             `json:"pctOfPlanned"`
	PostedOn     string              `json:"postedOn"`
}

type varianceTotalView struct {
	Currency string              `json:"currency"`
	Type     domain.VarianceType `json:"type"`
	Amount   string              `json:"amount"`
}

type varianceNetView struct {
	Currency string `json:"currency"`
	Amount   string `json:"amount"`
}

type varianceReportView struct {
	Items      []varianceRowView   `json:"items"`
	Totals     []varianceTotalView `json:"totals"`
	Net        []varianceNetView   `json:"net"`
	NextCursor string              `json:"nextCursor,omitempty"`
}

// varianceReport serves the posted variances (PRD 4.21.14): positive is
// unfavourable (debit), with totals by type and the net per currency.
func (a *API) varianceReport(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	f := store.VarianceFilter{LegalEntityID: q.Get("legalEntityId"), SiteID: q.Get("siteId"), ItemGroup: q.Get("itemGroup"),
		OrderID: q.Get("orderId"), Type: domain.VarianceType(q.Get("varianceType")), From: q.Get("from"), To: q.Get("to"), Cursor: q.Get("cursor")}
	f.Limit = reportLimit(q.Get("limit"), fields)
	for name, v := range map[string]string{"legalEntityId": f.LegalEntityID, "siteId": f.SiteID, "orderId": f.OrderID, "cursor": f.Cursor} {
		if v != "" && !ids.Valid(v) {
			fields[name] = "must be a UUID"
		}
	}
	if f.Type != "" && !varianceTypes[f.Type] {
		fields["varianceType"] = "must be lot-size, production-quantity, production-price, production-substitution or rework"
	}
	if len(f.ItemGroup) > maxItemGroup {
		fields["itemGroup"] = fmt.Sprintf("at most %d characters", maxItemGroup)
	}
	for name, v := range map[string]string{"from": f.From, "to": f.To} {
		if v != "" && !isDate(v) {
			fields[name] = "must be a date (YYYY-MM-DD)"
		}
	}
	if isDate(f.From) && isDate(f.To) && f.From > f.To {
		fields["to"] = "must not be before from"
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	rep, err := a.store.VarianceReport(r.Context(), f)
	if err != nil {
		a.fail(w, r, "read the variance report", err)
		return
	}
	v := varianceReportView{Items: make([]varianceRowView, 0, len(rep.Items)), Totals: make([]varianceTotalView, 0, len(rep.Totals)),
		Net: []varianceNetView{}, NextCursor: rep.NextCursor}
	for _, x := range rep.Items {
		row := varianceRowView{ID: x.ID, OrderID: x.OrderID, OrderNumber: x.OrderNumber, SiteID: x.SiteID, ItemGroup: x.ItemGroup,
			Currency: x.Currency, Type: x.Type, CostGroup: x.Group, Amount: fixed(x.Amount, x.Currency), Planned: fixed(x.Planned, x.Currency),
			PostedOn: x.PostedOn}
		if x.PctOfPlanned != nil {
			pct := x.PctOfPlanned.StringFixed(domain.PctScale)
			row.PctOfPlanned = &pct
		}
		v.Items = append(v.Items, row)
	}
	net := map[string]decimal.Decimal{}
	var currencies []string
	for _, t := range rep.Totals {
		v.Totals = append(v.Totals, varianceTotalView{Currency: t.Currency, Type: t.Type, Amount: fixed(t.Amount, t.Currency)})
		if _, seen := net[t.Currency]; !seen {
			currencies = append(currencies, t.Currency)
		}
		net[t.Currency] = net[t.Currency].Add(t.Amount)
	}
	for _, c := range currencies {
		v.Net = append(v.Net, varianceNetView{Currency: c, Amount: fixed(net[c], c)})
	}
	httpx.JSON(w, http.StatusOK, v)
}
