package api

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/dq"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/store"
)

// openOrderLineDTO is one legacy open order line at the cutoff.
type openOrderLineDTO struct {
	LegacyID     string `json:"legacyId"`
	OrderType    string `json:"orderType"`
	OrderNumber  string `json:"orderNumber"`
	LineNumber   string `json:"lineNumber"`
	Item         string `json:"item"`
	Party        string `json:"party"`
	Unit         string `json:"unit"`
	Currency     string `json:"currency"`
	OrderedQty   string `json:"orderedQty"`
	DeliveredQty string `json:"deliveredQty"`
	UnitPrice    string `json:"unitPrice"`
	DeliveryDate string `json:"deliveryDate,omitempty"`
}

// openOrdersRunRequest starts an open_orders run. Items, Customers and Vendors are the masters the
// referential rule checks against; Customers is needed when a line is a sales order line, Vendors
// when it is a purchase order line.
type openOrdersRunRequest struct {
	SourceSystem     string             `json:"sourceSystem"`
	SourceCompany    string             `json:"sourceCompany"`
	CutoffTS         time.Time          `json:"cutoffTs"`
	FreezeTS         time.Time          `json:"freezeTs"`
	QuarantineMaxPct string             `json:"quarantineMaxPct,omitempty"`
	Items            []string           `json:"items"`
	Customers        []string           `json:"customers,omitempty"`
	Vendors          []string           `json:"vendors,omitempty"`
	Records          []openOrderLineDTO `json:"records"`
}

func idSet(in []string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, id := range splitIDs(in) {
		out[id] = struct{}{}
	}
	return out
}

// startOpenOrdersRun validates an open_orders extract, quarantines failing lines, loads the rest
// idempotently by legacy_xref as opening order lines and reconciles it (FR-49.1, FR-49.2,
// FR-49.10, FR-49.11). Nothing posts to sales or purchasing (H-S11-1).
func (s *Server) startOpenOrdersRun(w http.ResponseWriter, r *http.Request) {
	var req openOrdersRunRequest
	idem, err := readCommand(r, &req, packRunBodyLimit)
	if err != nil {
		fail(w, r, err)
		return
	}
	p, ok := principal(w, r)
	if !ok {
		return
	}
	sys, err := domain.ParseSourceSystem(req.SourceSystem)
	if err != nil {
		fail(w, r, err)
		return
	}
	company := strings.TrimSpace(req.SourceCompany)
	if company == "" {
		fail(w, r, invalid("sourceCompany is required (DataAreaId or WERKS company code)"))
		return
	}
	if req.CutoffTS.IsZero() || req.FreezeTS.IsZero() {
		fail(w, r, invalid("cutoffTs and freezeTs are required"))
		return
	}
	if err := domain.CheckCutoff(req.CutoffTS, req.FreezeTS); err != nil {
		fail(w, r, err)
		return
	}
	maxPct, err := domain.ParseQuarantineMaxPct(req.QuarantineMaxPct)
	if err != nil {
		fail(w, r, err)
		return
	}
	masters := domain.OrderMasters{Items: idSet(req.Items), Customers: idSet(req.Customers), Vendors: idSet(req.Vendors)}
	if len(masters.Items) == 0 {
		fail(w, r, invalid("items must list the item master the lines are checked against"))
		return
	}
	if len(req.Records) == 0 {
		fail(w, r, invalid("records must hold at least one line"))
		return
	}
	lines := make([]domain.OpenOrderLine, 0, len(req.Records))
	var sales, purchase bool
	t := strings.TrimSpace
	for _, rec := range req.Records {
		o := domain.OpenOrderLine{LegacyID: t(rec.LegacyID), OrderType: strings.ToUpper(t(rec.OrderType)), OrderNumber: t(rec.OrderNumber),
			LineNumber: t(rec.LineNumber), Item: t(rec.Item), Party: t(rec.Party), Unit: t(rec.Unit), Currency: t(rec.Currency), DeliveryDate: t(rec.DeliveryDate)}
		var perr error
		parse := func(dst *decimal.Decimal, name, raw string) {
			if perr != nil {
				return
			}
			if *dst, perr = parseMoney(raw); perr != nil {
				perr = fmt.Errorf("%w: %s %q of %q %s", domain.ErrInvalid, name, raw, rec.LegacyID, perr)
			}
		}
		parse(&o.OrderedQty, "orderedQty", rec.OrderedQty)
		parse(&o.DeliveredQty, "deliveredQty", rec.DeliveredQty)
		parse(&o.UnitPrice, "unitPrice", rec.UnitPrice)
		if perr != nil {
			fail(w, r, perr)
			return
		}
		sales, purchase = sales || o.OrderType == domain.OrderSales, purchase || o.OrderType == domain.OrderPurchase
		lines = append(lines, o)
	}
	if sales && len(masters.Customers) == 0 {
		fail(w, r, invalid("customers must list the customer master the sales order lines are checked against"))
		return
	}
	if purchase && len(masters.Vendors) == 0 {
		fail(w, r, invalid("vendors must list the vendor master the purchase order lines are checked against"))
		return
	}
	accepted, quarantined := domain.ValidateOpenOrders(lines, masters)
	accepted, quarantined, err = gateSuite(domain.PackOpenOrders, lines, dq.OpenOrderRows(lines),
		map[string]map[string]bool{"items": dq.SetOf(masters.Items), "customers": dq.SetOf(masters.Customers), "vendors": dq.SetOf(masters.Vendors), "iso4217": dq.ISO4217()},
		domain.OpenOrderLine.HeldKey, domain.OpenOrderLine.HeldKey, accepted, quarantined,
		func(o domain.OpenOrderLine, why string) domain.Quarantined { return quarantine(o.Record(), o, why) })
	if err != nil {
		fail(w, r, err)
		return
	}
	if err := domain.QuarantineShareExceeded(len(quarantined), len(lines), maxPct); err != nil {
		fail(w, r, err)
		return
	}
	run := domain.PackRun{
		RunID: ids.New(), TenantID: p.TenantID, Pack: domain.PackOpenOrders, SourceSystem: sys, SourceCompany: company,
		CutoffTS: req.CutoffTS.UTC(), InputHash: domain.HashOpenOrders(lines), Status: domain.StatusExtracted,
		RecordsIn: len(lines), RecordsQuarantined: len(quarantined), Quarantined: quarantined, Parties: orderMasterIDs(masters),
	}
	if err := run.Advance(domain.StatusValidated); err != nil {
		fail(w, r, err)
		return
	}
	out, replayed, err := s.store.RunOpenOrders(r.Context(), idem, store.OpenOrdersRun{Run: run, Lines: lines, Accepted: accepted, StartedBy: p.UserID})
	if err != nil {
		fail(w, r, err)
		return
	}
	respond(w, http.StatusCreated, replayed, toRunDTO(out))
}

// orderMasterIDs keeps the masters a run validated against as prefixed ids in the run's master
// snapshot, so a later release checks the corrected line against them.
func orderMasterIDs(m domain.OrderMasters) []string {
	var out []string
	for prefix, set := range map[string]map[string]struct{}{"item:": m.Items, "customer:": m.Customers, "vendor:": m.Vendors} {
		for id := range set {
			out = append(out, prefix+id)
		}
	}
	sort.Strings(out)
	return out
}
