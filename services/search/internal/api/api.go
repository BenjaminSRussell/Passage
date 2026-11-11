// Package api serves GET /v1/search: one ranked list of records the caller may
// open. A record type is searched only when the caller holds the read duty of
// its source service; tenant and legal-entity scope come from row-level
// security.
package api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/search/internal/store"
)

const (
	MinQueryLength = 2
	MaxQueryLength = 100
	DefaultLimit   = 20
	MaxLimit       = 50

	codeValidation = "COM-0001"
	codeTenant     = "AUTH-0001"
)

// source is one searchable record type: the duties that let a caller search it
// (any one of them) and the web route of its record.
type source struct {
	recordType string
	duties     []string
	link       string
	byNumber   bool
}

func (s source) allows(p tenant.Principal) bool { return slices.ContainsFunc(s.duties, p.HasDuty) }

var sources = []source{
	{store.TypeParty, []string{"party.read"}, "/party/%s", false},
	{store.TypeProduct, []string{"product.read"}, "/product/%s", true},
	{store.TypePurchaseOrder, []string{"procurement.purchase-order.inquire"}, "/purchasing/purchase-orders/%s", false},
	{store.TypeReceipt, []string{"receiving.receipt.maintain"}, "/receiving/receipts/%s", false},
	{store.TypeVendorInvoice, []string{"receiving.ap.inquire"}, "/ap-invoices/%s", false},
	{store.TypeSalesOrder, []string{"sales.order.read"}, "/sales/orders/%s", false},
	{store.TypePackingSlip, []string{"fulfillment.warehouse.view"}, "/fulfillment/packing-slips/%s", false},
	{store.TypeCustomerInvoice, []string{"billing.invoice.view"}, "/billing/invoices/%s", false},
	{store.TypeProductionOrder, []string{"production.order.read"}, "/production/%s", false},
	{store.TypeWorker, []string{"hr.worker.inquire"}, "/hr/workers/%s", false},
}

type Searcher interface {
	Search(ctx context.Context, q store.Query) ([]store.Hit, error)
}

type API struct {
	st                Searcher
	searches, emptied atomic.Int64
}

func New(st Searcher) *API { return &API{st: st} }

func (a *API) Register(r chi.Router) {
	var duties []string
	for _, s := range sources {
		duties = append(duties, s.duties...)
	}
	r.With(requireTenant, authz.RequireAnyDuty(duties...)).Get("/v1/search", a.search)
}

func (a *API) Metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = fmt.Fprintf(w, "# TYPE search_requests_total counter\nsearch_requests_total %d\n# TYPE search_requests_empty_total counter\nsearch_requests_empty_total %d\n",
			a.searches.Load(), a.emptied.Load())
	})
}

func requireTenant(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p, ok := tenant.FromContext(r.Context()); !ok || !ids.Valid(p.TenantID) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, codeTenant, "missing or malformed tenant"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

type result struct {
	Type          string `json:"type"`
	ID            string `json:"id"`
	LegalEntityID string `json:"legalEntityId,omitempty"`
	Number        string `json:"number"`
	Title         string `json:"title"`
	Subtitle      string `json:"subtitle,omitempty"`
	Status        string `json:"status,omitempty"`
	Link          string `json:"link"`
}

func (a *API) search(w http.ResponseWriter, r *http.Request) {
	p, _ := tenant.FromContext(r.Context())
	q := r.URL.Query()
	text := strings.TrimSpace(q.Get("q"))
	limit, errs := parseLimit(q.Get("limit"))
	types, terr := parseTypes(q.Get("types"), p)
	if n := utf8.RuneCountInString(text); n < MinQueryLength || n > MaxQueryLength {
		errs["q"] = fmt.Sprintf("must be %d to %d characters", MinQueryLength, MaxQueryLength)
	}
	if terr != "" {
		errs["types"] = terr
	}
	if len(errs) > 0 {
		pr := httpx.NewProblem(http.StatusBadRequest, codeValidation, "request failed validation")
		pr.Errors = errs
		httpx.Fail(w, r, pr)
		return
	}
	a.searches.Add(1)
	out := []result{}
	if len(types) > 0 {
		hits, err := a.st.Search(r.Context(), store.Query{Text: text, Types: types, Limit: limit})
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		for _, h := range hits {
			out = append(out, result{Type: h.Type, ID: h.ID, LegalEntityID: h.LegalEntityID, Number: h.Number, Title: h.Title,
				Subtitle: h.Subtitle, Status: h.Status, Link: linkFor(h)})
		}
	}
	if len(out) == 0 {
		a.emptied.Add(1)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"results": out})
}

func linkFor(h store.Hit) string {
	for _, s := range sources {
		if s.recordType == h.Type {
			key := h.ID
			if s.byNumber {
				key = url.PathEscape(h.Number)
			}
			return fmt.Sprintf(s.link, key)
		}
	}
	return "/"
}

func parseLimit(raw string) (int, map[string]string) {
	errs := map[string]string{}
	if raw == "" {
		return DefaultLimit, errs
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > MaxLimit {
		errs["limit"] = fmt.Sprintf("must be a number from 1 to %d", MaxLimit)
		return DefaultLimit, errs
	}
	return n, errs
}

// parseTypes narrows the requested types (all when none are named) to those
// the caller holds the read duty for. A named type the caller may not read is
// dropped silently, so the answer does not reveal what exists.
func parseTypes(raw string, p tenant.Principal) ([]string, string) {
	requested := map[string]bool{}
	for _, t := range strings.Split(raw, ",") {
		if t = strings.TrimSpace(t); t == "" {
			continue
		}
		if !slices.ContainsFunc(sources, func(s source) bool { return s.recordType == t }) {
			return nil, "unknown record type " + strconv.Quote(t)
		}
		requested[t] = true
	}
	var allowed []string
	for _, s := range sources {
		if (len(requested) == 0 || requested[s.recordType]) && s.allows(p) {
			allowed = append(allowed, s.recordType)
		}
	}
	return allowed, ""
}
