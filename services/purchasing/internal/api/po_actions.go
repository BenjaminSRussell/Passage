package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// dutyPurchaseOrderConfirm covers :confirm and the PO approval tasks (PRD 3.17.17).
const dutyPurchaseOrderConfirm = "procurement.purchase-order.confirm"

// poActionDuty is the duty a PO custom method needs (PRD 3.17.17): approving,
// rejecting and confirming are procurement.purchase-order.confirm; submit and cancel are
// maintenance.
func poActionDuty(action string) string {
	switch action {
	case domain.POActionApprove, domain.POActionReject, domain.POActionConfirm:
		return dutyPurchaseOrderConfirm
	}
	return dutyPurchaseOrderMaintain
}

func isPOAction(action string) bool {
	for _, a := range domain.POActions {
		if a == action {
			return true
		}
	}
	return false
}

// editOrder handles PATCH /purchase-orders/{id} (FR-17.8, BR-17.8). The body is
// the whole order as on create; each line names the existing line it changes
// with lineNo, a line without lineNo is new, and a live line left out is
// cancelled. An existing line that omits unitPrice keeps its price and price
// source while its product and unit stay the same. Requisition and agreement
// links of existing lines are kept; new lines cannot have them.
func (a *API) editOrder(w http.ResponseWriter, r *http.Request) {
	p, err := principal(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	want, err := ifMatch(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body orderBody
	if err := httpx.Decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	if err := inScope(p, body.LegalEntityID); err != nil {
		fail(w, r, err)
		return
	}
	cur, err := a.store.GetPurchaseOrder(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	if err := matchExistingLines(cur, body.Lines); err != nil {
		fail(w, r, err)
		return
	}
	if err := a.orderCategories(r.Context(), &body); err != nil {
		fail(w, r, err)
		return
	}
	body.curSiteID, body.curWarehouseID = cur.SiteID, cur.WarehouseID
	body.keepCharges = map[string]store.PurchaseOrderCharge{}
	for _, c := range cur.Charges {
		if k := chargeKey(c.ChargeCodeID, c.LineNo); c.ChargeCodeID != "" {
			if _, ok := body.keepCharges[k]; !ok {
				body.keepCharges[k] = c
			}
		}
	}
	body.lineNos = lineNumbers(cur, body.Lines)
	body.autoApplied = map[string]bool{}
	for _, id := range cur.AutoChargeCodes {
		body.autoApplied[id] = true
	}
	next, err := a.buildOrder(r.Context(), body)
	if err != nil {
		fail(w, r, err)
		return
	}
	for i, n := range body.lineNos {
		next.Lines[i].LineNo = n
	}
	// FR-17.20: the vendor of a PO never changes and an existing line keeps its category,
	// so only the lines this edit adds are checked.
	warnings, err := a.checkOrderCategoryAccess(r.Context(), next, func(i int) bool { return body.Lines[i].keep == nil })
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.EditPurchaseOrder(r.Context(), id, want, next)
	if err != nil {
		fail(w, r, err)
		return
	}
	out.Warnings = append(out.Warnings, warnings...)
	a.orderEdits.Add(1)
	if out.ApprovalStatus == domain.POStatusConfirmed && out.VersionNo > cur.VersionNo {
		a.ordersConfirmed.Add(1)
	}
	w.Header().Set("ETag", etag(out.RowVersion))
	httpx.JSON(w, http.StatusOK, out)
}

// matchExistingLines checks each body line's lineNo against the PO and, for an
// existing line, carries over its links and the price to keep.
func matchExistingLines(cur store.PurchaseOrder, lines []orderLineBody) error {
	byNo := make(map[int]store.PurchaseOrderLine, len(cur.Lines))
	for _, l := range cur.Lines {
		byNo[l.LineNo] = l
	}
	seen := map[int]bool{}
	for i := range lines {
		l := &lines[i]
		field := "lines[" + strconv.Itoa(i) + "]."
		if l.LineNo == 0 {
			continue
		}
		c, ok := byNo[l.LineNo]
		switch {
		case l.LineNo < 0 || !ok:
			return invalid(field+"lineNo", "is not a line of this purchase order")
		case seen[l.LineNo]:
			return invalid(field+"lineNo", "appears more than once")
		case c.Status == domain.POLineCancelled:
			return invalid(field+"lineNo", "is cancelled and cannot be changed")
		}
		seen[l.LineNo] = true
		if l.RequisitionLineID == "" {
			l.RequisitionLineID = c.RequisitionLineID
		}
		if l.AgreementLineID == "" {
			l.AgreementLineID = c.AgreementLineID
		}
		keep := c
		l.keep = &keep
	}
	return nil
}

// lineNumbers gives each body line the number it names, and new lines the numbers after the
// highest on the PO (cancelled lines included).
func lineNumbers(cur store.PurchaseOrder, body []orderLineBody) []int {
	top := 0
	for _, l := range cur.Lines {
		top = max(top, l.LineNo)
	}
	out := make([]int, len(body))
	for i := range body {
		if body[i].LineNo > 0 {
			out[i] = body[i].LineNo
			continue
		}
		top++
		out[i] = top
	}
	return out
}

// orderAction handles POST /purchase-orders/{id}:submit|approve|reject|confirm|cancel.
// Each needs its duty, If-Match and an Idempotency-Key. :reprice is handed to
// repriceOrder, which checks its own duty.
func (a *API) orderAction(w http.ResponseWriter, r *http.Request) {
	p, err := principal(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	id, action, found := strings.Cut(chi.URLParam(r, "id"), ":")
	if found && action == repriceAction && ids.Valid(id) {
		a.repriceOrder(w, r, p, id)
		return
	}
	if !found || !isPOAction(action) || !ids.Valid(id) {
		fail(w, r, httpx.NewProblem(http.StatusNotFound, codeUnknownAction, "unknown purchase order action"))
		return
	}
	if duty := poActionDuty(action); !p.HasDuty(duty) {
		fail(w, r, httpx.NewProblem(http.StatusForbidden, codeScope, "missing duty "+duty))
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	want, err := ifMatch(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	// FR-17.17 / BR-17.15: an approver whose approval limit is below the PO total, or whose
	// limit is past its recertification date, leaves the approval to the next approver. The
	// total checked is the one at the If-Match version: every edit or reprice moves the row
	// version, and the store refuses any other version under its row lock, so the PO cannot
	// be approved at a total that was not checked. A PO already past that version is a
	// replay of an approval (answered from the key) or a 412, so the check is left to it.
	if action == domain.POActionApprove {
		po, err := a.store.GetPurchaseOrder(r.Context(), id)
		if err != nil {
			fail(w, r, err)
			return
		}
		if po.RowVersion == want {
			if err := a.approverCheck(r, po.LegalEntityID, p.UserID, domain.DocPurchaseOrder, po.Total, po.Currency); err != nil {
				fail(w, r, err)
				return
			}
		}
	}
	idem := store.Idem{Key: key, Hash: hashBody("purchase-order."+action, map[string]any{"id": id, "ifMatch": want})}
	out, err := a.store.ActOnPurchaseOrder(r.Context(), id, want, action, idem)
	if err != nil {
		fail(w, r, err)
		return
	}
	switch action {
	case domain.POActionConfirm:
		a.ordersConfirmed.Add(1)
	case domain.POActionCancel:
		a.ordersCancelled.Add(1)
	}
	w.Header().Set("ETag", etag(out.RowVersion))
	httpx.JSON(w, http.StatusOK, out)
}

// orderVersions handles GET /purchase-orders/{id}/versions: every stored
// version oldest first, a pending change included.
func (a *API) orderVersions(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	versions, err := a.store.ListPurchaseOrderVersions(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": versions})
}

// versionDiff is the body of GET /purchase-orders/{id}/versions/{n}/diff. Patch
// turns version n-1 into version n (RFC 6902); version 1 has an empty patch.
type versionDiff struct {
	PurchaseOrderID    string           `json:"purchaseOrderId"`
	FromVersion        int              `json:"fromVersion"`
	ToVersion          int              `json:"toVersion"`
	Confirmed          bool             `json:"confirmed"`
	ConfirmedAt        *time.Time       `json:"confirmedAt,omitempty"`
	ReapprovalRequired bool             `json:"reapprovalRequired"`
	Patch              []domain.PatchOp `json:"patch"`
}

// orderVersionDiff handles GET /purchase-orders/{id}/versions/{n}/diff.
func (a *API) orderVersionDiff(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	n, err := strconv.Atoi(chi.URLParam(r, "n"))
	if err != nil || n < 1 {
		fail(w, r, invalid("n", "must be a version number of at least 1"))
		return
	}
	v, err := a.store.GetPurchaseOrderVersion(r.Context(), id, n)
	if err != nil {
		fail(w, r, err)
		return
	}
	patch := v.Patch
	if patch == nil {
		patch = []domain.PatchOp{}
	}
	httpx.JSON(w, http.StatusOK, versionDiff{
		PurchaseOrderID:    id,
		FromVersion:        n - 1,
		ToVersion:          n,
		Confirmed:          v.ConfirmedAt != nil,
		ConfirmedAt:        v.ConfirmedAt,
		ReapprovalRequired: v.ReapprovalRequired,
		Patch:              patch,
	})
}
