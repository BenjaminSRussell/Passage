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
const dutyPurchaseOrderConfirm = "PurchaseOrderConfirm"

// poActionDuty is the duty a PO custom method needs (PRD 3.17.17): approving,
// rejecting and confirming are PurchaseOrderConfirm; submit and cancel are
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
	next, err := a.buildOrder(r.Context(), body)
	if err != nil {
		fail(w, r, err)
		return
	}
	numberLines(cur, body.Lines, next.Lines)
	out, err := a.store.EditPurchaseOrder(r.Context(), id, want, next)
	if err != nil {
		fail(w, r, err)
		return
	}
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

// numberLines gives each built line the number its body line names, and new
// lines the numbers after the highest on the PO (cancelled lines included).
func numberLines(cur store.PurchaseOrder, body []orderLineBody, lines []store.PurchaseOrderLine) {
	top := 0
	for _, l := range cur.Lines {
		top = max(top, l.LineNo)
	}
	for i := range lines {
		if body[i].LineNo > 0 {
			lines[i].LineNo = body[i].LineNo
			continue
		}
		top++
		lines[i].LineNo = top
	}
}

// orderAction handles POST /purchase-orders/{id}:submit|approve|reject|confirm|cancel.
// Each needs its duty, If-Match and an Idempotency-Key.
func (a *API) orderAction(w http.ResponseWriter, r *http.Request) {
	p, err := principal(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	id, action, found := strings.Cut(chi.URLParam(r, "id"), ":")
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
