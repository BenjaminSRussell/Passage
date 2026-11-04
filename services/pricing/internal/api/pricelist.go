package api

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

const (
	priceListPath    = "/v1/pricing/price-list"
	netPriceListPath = priceListPath + "/net-prices"
)

// MaxNetPriceRows and MaxNetPriceBreaks cap one net price response: at most 500 rows and 2,000
// breaks in all, each break a one-line resolve. Rows past either cap are not resolved, so a request
// holds one core for about four maximal (PRICING_MAX_LINES) resolves, not the 10,000 one-line
// resolves 500 rows of 20 breaks would be (domain BenchmarkNetPriceList_FullPage), and a report
// cannot crowd out the FR-26.14 resolve latency.
const (
	MaxNetPriceRows   = 500
	MaxNetPriceBreaks = 2000
)

// MaxPriceListItems caps one price list response. A legal entity's all-customer records cover every
// product it sells, so the list is cut after this many entries (in its product order) and marked
// truncated; narrow it with type to see the rest.
const MaxPriceListItems = 1000

// priceListItem is an ACTIVE condition record that applies to the customer, with its scope.
type priceListItem struct {
	recordView
	Scope string `json:"scope"`
}

// priceList is the price list by customer inquiry (PRD 5.26.14): every ACTIVE condition record that
// applies to a customer on a date, most specific agreement first. Resolve stays the authority on which
// condition wins a given line.
func (a *API) priceList(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	if !ids.Valid(q.Get("legalEntityId")) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if q.Get("customerId") == "" {
		fields["customerId"] = "is required"
	}
	if !currencyCode.MatchString(q.Get("currency")) {
		fields["currency"] = "must be three upper-case letters"
	}
	day, err := time.Parse(dateLayout, q.Get("asOf"))
	if err != nil {
		fields["asOf"] = "must be a date in YYYY-MM-DD form"
	}
	types := map[domain.ConditionType]bool{}
	if raw := q.Get("type"); raw != "" {
		for _, t := range strings.Split(raw, ",") {
			if !domain.ValidType(domain.ConditionType(t)) {
				fields["type"] = "must list PRICE, LINE_DISC, MULTILINE_DISC, TOTAL_DISC or SURCHARGE, separated by commas"
				break
			}
			types[domain.ConditionType(t)] = true
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	group := q.Get("customerGroupId")
	in, err := a.resolveInput(r.Context(), store.ActiveQuery{LegalEntityID: q.Get("legalEntityId"), Currency: q.Get("currency"), CustomerGroupID: group, AsOf: day})
	if err != nil {
		slog.Error("load active records failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	records := in.Records
	if in.Set != nil {
		records = in.Set.Records()
	}
	list := domain.PriceListFor(records, q.Get("customerId"), group, types)
	truncated := len(list) > MaxPriceListItems
	if truncated {
		list = list[:MaxPriceListItems]
	}
	items := make([]priceListItem, len(list))
	for i, e := range list {
		rec := e.Record
		items[i] = priceListItem{Scope: e.Scope, recordView: viewOf(store.Entry{LegalEntityID: q.Get("legalEntityId"), Record: rec})}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "truncated": truncated})
}

// netPriceList is the price list by customer with the net price at each quantity break (PRD 5.26.14):
// the product rows of the customer's applicable PRICE records, each resolved as a one-line sale at its
// breaks under the procedure the sales channel is assigned (or the one named), so the figures are what
// a sales line of that quantity gets. Order-level conditions are not included.
func (a *API) netPriceList(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	if !ids.Valid(q.Get("legalEntityId")) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if q.Get("customerId") == "" {
		fields["customerId"] = "is required"
	}
	if !currencyCode.MatchString(q.Get("currency")) {
		fields["currency"] = "must be three upper-case letters"
	}
	day, err := time.Parse(dateLayout, q.Get("asOf"))
	if err != nil {
		fields["asOf"] = "must be a date in YYYY-MM-DD form"
	}
	if msg := domain.ValidateSalesChannel(q.Get("salesChannel")); msg != "" {
		fields["salesChannel"] = msg
	}
	if name := q.Get("procedure"); name != "" {
		if msg := domain.ValidateProcedureName(name); msg != "" {
			fields["procedure"] = msg
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	group := q.Get("customerGroupId")
	in, err := a.resolveInput(r.Context(), store.ActiveQuery{LegalEntityID: q.Get("legalEntityId"), Currency: q.Get("currency"), CustomerGroupID: group, AsOf: day})
	if err != nil {
		slog.Error("load active records failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	proc, err := a.store.DetermineProcedure(r.Context(), q.Get("legalEntityId"), q.Get("salesChannel"), q.Get("procedure"))
	if err != nil {
		slog.Error("determine procedure failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	records := in.Records
	if in.Set != nil {
		records = in.Set.Records()
	}
	// The resolve input keeps the cache's shared RecordSet (or the loaded records), as /resolve does.
	in.Header = domain.Header{CustomerID: q.Get("customerId"), CustomerGroupID: group, Currency: q.Get("currency"), PricingDate: day}
	in.Procedure = proc
	params, err := a.entityParameters(r, q.Get("legalEntityId"))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in.TotalDiscountBasis = params.TotalDiscountBasis
	filter := domain.NetPriceFilter{ProductID: q.Get("productId"), ProductGroupID: q.Get("productGroupId")}
	rows, truncated := domain.NetPriceList(in, domain.PriceListFor(records, q.Get("customerId"), group, nil), filter, domain.NetPriceLimits{Rows: MaxNetPriceRows, Breaks: MaxNetPriceBreaks})
	a.resolves.Add(1)
	for _, row := range rows {
		a.linesPriced.Add(int64(len(row.Breaks)))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"rows": rows, "truncated": truncated})
}
