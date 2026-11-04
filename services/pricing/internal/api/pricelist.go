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

const priceListPath = "/v1/pricing/price-list"

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
