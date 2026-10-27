package api

import (
	"context"
	"net/http"
	"strconv"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// codeNeedsSourcing rejects a consolidation line with no vendor: neither the
// buyer nor the requisition named one, so it belongs in the "Needs sourcing"
// queue (FR-17.3) until a vendor is chosen or an RFQ awards one.
const codeNeedsSourcing = "PRC-CONS-002"

// consolidateBody is the request of POST /requisitions:consolidate (FR-17.3).
// Each line names an approved requisition line; vendorId overrides the line's
// preferred vendor (buyer override) and agreementLineId releases the PO line
// against a purchase agreement. WindowDays defaults to 7 (1–60).
type consolidateBody struct {
	WindowDays *int                  `json:"windowDays,omitempty"`
	Lines      []consolidateLineBody `json:"lines"`
}

type consolidateLineBody struct {
	RequisitionLineID string `json:"requisitionLineId"`
	VendorID          string `json:"vendorId,omitempty"`
	AgreementLineID   string `json:"agreementLineId,omitempty"`
}

// consolidateResponse lists the POs created or, on replay, already linked.
type consolidateResponse struct {
	PurchaseOrders []store.PurchaseOrder `json:"purchaseOrders"`
}

func (a *API) consolidateRequisitions(w http.ResponseWriter, r *http.Request) {
	p, err := principal(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body consolidateBody
	if err := httpx.Decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	orders, lineIDs, err := a.buildConsolidation(r.Context(), p, body)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.ConsolidateRequisitionLines(r.Context(), orders, lineIDs,
		store.Idem{Key: key, Hash: hashBody("requisitions.consolidate", body)})
	if err != nil {
		fail(w, r, err)
		return
	}
	a.ordersCreated.Add(int64(len(out)))
	if out == nil {
		out = []store.PurchaseOrder{}
	}
	httpx.JSON(w, http.StatusCreated, consolidateResponse{PurchaseOrders: out})
}

// buildConsolidation validates the request, loads the requisition lines,
// groups them with domain.Consolidate and builds one priced PO per group. Each
// PO line keeps its requisition_line_id (FR-17.3) and is priced like a direct
// PO line, falling back to the requisition estimate when no agreement or trade
// agreement prices it. It returns the POs and the requisition line ids.
func (a *API) buildConsolidation(ctx context.Context, p tenant.Principal, body consolidateBody) ([]store.PurchaseOrder, []string, error) {
	if len(body.Lines) == 0 {
		return nil, nil, invalid("lines", "needs at least one line")
	}
	if len(body.Lines) > maxRequisitionLines {
		return nil, nil, httpx.NewProblem(http.StatusUnprocessableEntity, codeTooManyLines,
			"more than "+strconv.Itoa(maxRequisitionLines)+" lines in one consolidation")
	}
	window := domain.DefaultConsolidationWindowDays
	if body.WindowDays != nil {
		window = *body.WindowDays
	}
	byLine := make(map[string]consolidateLineBody, len(body.Lines))
	lineIDs := make([]string, 0, len(body.Lines))
	for i, l := range body.Lines {
		field := func(n string) string { return "lines[" + strconv.Itoa(i) + "]." + n }
		for _, f := range []struct {
			name, value string
			required    bool
		}{{"requisitionLineId", l.RequisitionLineID, true}, {"vendorId", l.VendorID, false}, {"agreementLineId", l.AgreementLineID, false}} {
			if err := uuidField(field(f.name), f.value, f.required); err != nil {
				return nil, nil, err
			}
		}
		if _, dup := byLine[l.RequisitionLineID]; dup {
			return nil, nil, invalid(field("requisitionLineId"), "appears more than once")
		}
		byLine[l.RequisitionLineID] = l
		lineIDs = append(lineIDs, l.RequisitionLineID)
	}

	sources, err := a.store.RequisitionLinesByID(ctx, lineIDs)
	if err != nil {
		return nil, nil, err
	}
	byID := make(map[string]store.SourcingLine, len(sources))
	for _, s := range sources {
		byID[s.ID] = s
	}
	cl := make([]domain.ConsolidationLine, 0, len(lineIDs))
	for i, id := range lineIDs {
		s, ok := byID[id]
		if !ok {
			return nil, nil, httpx.NewProblem(http.StatusNotFound, store.CodeRequisitionLineNotFound, "requisition line "+id+" not found")
		}
		if err := inScope(p, s.LegalEntityID); err != nil {
			return nil, nil, err
		}
		vendor := byLine[id].VendorID
		if vendor == "" {
			vendor = s.PreferredVendorID
		}
		if vendor == "" {
			prob := httpx.NewProblem(http.StatusUnprocessableEntity, codeNeedsSourcing,
				"requisition "+s.RequisitionNumber+" line "+strconv.Itoa(s.LineNo)+" has no vendor; choose one or source it with an RFQ")
			prob.Errors = map[string]string{"lines[" + strconv.Itoa(i) + "].vendorId": "is required: the line has no preferred vendor"}
			return nil, nil, prob
		}
		cl = append(cl, domain.ConsolidationLine{RequisitionLineID: id, LegalEntityID: s.LegalEntityID, VendorID: vendor,
			SiteID: s.SiteID, Currency: s.Currency, RequestedDate: s.RequestedDate})
	}
	groups, err := domain.Consolidate(cl, window)
	if err != nil {
		return nil, nil, err
	}

	orders := make([]store.PurchaseOrder, 0, len(groups))
	for _, g := range groups {
		ob := orderBody{LegalEntityID: g.LegalEntityID, VendorID: g.VendorID, SiteID: g.SiteID, Currency: g.Currency}
		for _, l := range g.Lines {
			s := byID[l.RequisitionLineID]
			estimate := s.EstimatedUnitPrice
			ob.Lines = append(ob.Lines, orderLineBody{
				ProductID: s.ProductID, Description: s.Description, Qty: s.Qty.String(), Unit: s.Unit,
				DeliveryDate: s.RequestedDate, RequisitionLineID: s.ID, AgreementLineID: byLine[s.ID].AgreementLineID,
				estimate: &estimate,
			})
		}
		po, err := a.buildOrder(ctx, ob)
		if err != nil {
			return nil, nil, err
		}
		orders = append(orders, po)
	}
	return orders, lineIDs, nil
}

func (a *API) listOpenRequisitionLines(w http.ResponseWriter, r *http.Request) {
	limit, after, err := listParams(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	q := r.URL.Query()
	f := store.SourcingFilter{Sourcing: q.Get("sourcing"), VendorID: q.Get("vendorId")}
	if f.Sourcing != "" && f.Sourcing != store.SourcingNeedsSourcing && f.Sourcing != store.SourcingHasVendor {
		fail(w, r, invalid("sourcing", "must be NEEDS_SOURCING or HAS_VENDOR"))
		return
	}
	if err := uuidField("vendorId", f.VendorID, false); err != nil {
		fail(w, r, err)
		return
	}
	page, err := a.store.ListOpenRequisitionLines(r.Context(), f, after, limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	writePage(w, page)
}
