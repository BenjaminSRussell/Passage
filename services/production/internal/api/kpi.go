package api

import (
	"net/http"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	"github.com/BenjaminSRussell/passage/services/production/internal/store"
)

type kpiGroupView struct {
	Key          string  `json:"key,omitempty"`
	Ended        int     `json:"ended"`
	Dated        int     `json:"dated"`
	OnTime       int     `json:"onTime"`
	OnTimePct    *string `json:"onTimePct"`
	AvgCycleDays *string `json:"avgCycleDays"`
	QtyFinished  string  `json:"qtyFinished"`
	QtyError     string  `json:"qtyError"`
	ScrapPct     *string `json:"scrapPct"`
	Open         int     `json:"open"`
	Overdue      int     `json:"overdue"`
}

type kpiView struct {
	AsOf        string         `json:"asOf"`
	GroupBy     string         `json:"groupBy,omitempty"`
	Groups      []kpiGroupView `json:"groups"`
	GroupsTotal int            `json:"groupsTotal"`
	Totals      kpiGroupView   `json:"totals"`
}

func pctString(p *decimal.Decimal, places int32) *string {
	if p == nil {
		return nil
	}
	s := p.StringFixed(places)
	return &s
}

func kpiGroupOf(key string, g domain.KPIGroup) kpiGroupView {
	return kpiGroupView{Key: key, Ended: g.Ended, Dated: g.Dated, OnTime: g.OnTime, OnTimePct: pctString(g.OnTimePct(), domain.PctScale),
		AvgCycleDays: pctString(g.AvgCycleDays(), 2), QtyFinished: g.QtyFinished.String(), QtyError: g.QtyError.String(),
		ScrapPct: pctString(g.ScrapPct(), domain.PctScale), Open: g.Open, Overdue: g.Overdue}
}

// orderKPIs serves the production order KPIs (PRD 4.21.14, the service-side
// version of the order_kpi projection): orders ended in a period, on-time %
// against the delivery date, mean cycle days, scrap % and the open and overdue
// orders as of a date, in total and by site or product.
func (a *API) orderKPIs(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	f := store.KPIFilter{LegalEntityID: q.Get("legalEntityId"), SiteID: q.Get("siteId"), ProductID: q.Get("productId"),
		From: q.Get("from"), To: q.Get("to"), AsOf: q.Get("asOf"), GroupBy: q.Get("groupBy")}
	limit := reportLimit(q.Get("limit"), fields)
	for name, v := range map[string]string{"legalEntityId": f.LegalEntityID, "siteId": f.SiteID, "productId": f.ProductID} {
		if v != "" && !ids.Valid(v) {
			fields[name] = "must be a UUID"
		}
	}
	for name, v := range map[string]string{"from": f.From, "to": f.To, "asOf": f.AsOf} {
		if v != "" && !isDate(v) {
			fields[name] = "must be a date (YYYY-MM-DD)"
		}
	}
	if isDate(f.From) && isDate(f.To) && f.From > f.To {
		fields["to"] = "must not be before from"
	}
	if f.GroupBy != store.KPIGroupNone && f.GroupBy != store.KPIGroupSite && f.GroupBy != store.KPIGroupProduct {
		fields["groupBy"] = "must be site or product"
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	if f.AsOf == "" {
		f.AsOf = time.Now().UTC().Format(time.DateOnly)
	}
	rep, err := a.store.KPIs(r.Context(), f)
	if err != nil {
		a.fail(w, r, "read the order KPIs", err)
		return
	}
	v := kpiView{AsOf: f.AsOf, GroupBy: f.GroupBy, Groups: []kpiGroupView{}, GroupsTotal: len(rep.Groups), Totals: kpiGroupOf("", rep.Totals)}
	for i, g := range rep.Groups {
		if i == limit {
			break
		}
		v.Groups = append(v.Groups, kpiGroupOf(g.Key, g.KPIGroup))
	}
	httpx.JSON(w, http.StatusOK, v)
}
