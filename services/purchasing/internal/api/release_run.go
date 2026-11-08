package api

import (
	"errors"
	"net/http"
	"sort"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// Release run limits: a run reads at most maxLines open lines that have a vendor (200 unless the caller asks for more,
// never above the 500 a consolidation takes), oldest first, so the demand that has waited longest is ordered first and
// a larger backlog is released by running again. Lines with no vendor are listed up to the same number but never count
// against it: they cannot be ordered, and would otherwise fill the run and starve the lines that can.
const (
	defaultReleaseLines = 200
	releasePageSize     = 200
)

// releaseBody is the body of POST /requisition-releases (PRD 3.17.16 "Release approved requisitions"): the legal entity
// to release for, the consolidation window (days, default 7) and the most lines to read (default 200, at most 500).
type releaseBody struct {
	LegalEntityID string `json:"legalEntityId"`
	WindowDays    *int   `json:"windowDays,omitempty"`
	MaxLines      *int   `json:"maxLines,omitempty"`
}

// registerReleaseRoutes mounts the release run: starting one needs the duty of consolidating by hand, reading the
// record the duty of inquiring.
func (a *API) registerReleaseRoutes(r chi.Router) {
	r.With(authz.RequireDuty(dutyPurchaseOrderMaintain)).Post("/requisition-releases", a.releaseRequisitions)
	inquire := r.With(authz.RequireDuty(dutyPurchaseOrderInquire))
	inquire.Get("/requisition-releases", a.listReleaseRuns)
	inquire.Get("/requisition-releases/{id}", a.getReleaseRun)
}

// releaseRequisitions consolidates every OPEN line of an APPROVED requisition of one legal entity that has a vendor into
// draft POs, one vendor at a time, and records the run. A line with no vendor, and every line of a vendor whose
// POs cannot be built or written (HARD category access, an agreement over its limit, a line a concurrent write
// changed), stays in the consolidation queue and is listed as skipped with the reason; the other vendors are still released.
func (a *API) releaseRequisitions(w http.ResponseWriter, r *http.Request) {
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
	var body releaseBody
	if err := httpx.Decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	if err := uuidField("legalEntityId", body.LegalEntityID, true); err != nil {
		fail(w, r, err)
		return
	}
	if err := inScope(p, body.LegalEntityID); err != nil {
		fail(w, r, err)
		return
	}
	window, maxLines := domain.DefaultConsolidationWindowDays, defaultReleaseLines
	if body.WindowDays != nil {
		window = *body.WindowDays
	}
	if window < domain.MinConsolidationWindowDays || window > domain.MaxConsolidationWindowDays {
		fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeConsolidationWindow,
			"consolidation window must be "+strconv.Itoa(domain.MinConsolidationWindowDays)+"–"+strconv.Itoa(domain.MaxConsolidationWindowDays)+" days, not "+strconv.Itoa(window)))
		return
	}
	if body.MaxLines != nil {
		maxLines = *body.MaxLines
	}
	if maxLines < 1 || maxLines > maxRequisitionLines {
		fail(w, r, invalid("maxLines", "must be 1–"+strconv.Itoa(maxRequisitionLines)))
		return
	}

	candidates, err := a.releaseCandidates(r, store.SourcingFilter{LegalEntityID: body.LegalEntityID, Sourcing: store.SourcingHasVendor}, maxLines)
	if err != nil {
		fail(w, r, err)
		return
	}
	vendorless, err := a.releaseCandidates(r, store.SourcingFilter{LegalEntityID: body.LegalEntityID, Sourcing: store.SourcingNeedsSourcing}, maxLines)
	if err != nil {
		fail(w, r, err)
		return
	}
	in := store.ReleaseInput{LegalEntityID: body.LegalEntityID, WindowDays: window, MaxLines: maxLines,
		Considered: len(candidates) + len(vendorless), RequestedBy: p.UserID}
	for _, l := range vendorless {
		in.Skipped = append(in.Skipped, store.ReleaseSkip{RequisitionLineID: l.ID, RequisitionNumber: l.RequisitionNumber, LineNo: l.LineNo,
			Code: codeNeedsSourcing, Reason: "the line has no vendor; choose one or source it with an RFQ"})
	}
	byVendor := map[string][]store.SourcingLine{}
	for _, l := range candidates {
		byVendor[l.PreferredVendorID] = append(byVendor[l.PreferredVendorID], l)
	}
	vendors := make([]string, 0, len(byVendor))
	for v := range byVendor {
		vendors = append(vendors, v)
	}
	sort.Strings(vendors)
	for _, v := range vendors {
		lines := byVendor[v]
		cb := consolidateBody{WindowDays: &window}
		for _, l := range lines {
			cb.Lines = append(cb.Lines, consolidateLineBody{RequisitionLineID: l.ID})
		}
		orders, _, warnings, err := a.buildConsolidation(r.Context(), p, cb)
		var prob *httpx.Problem
		if errors.As(err, &prob) {
			for _, l := range lines {
				in.Skipped = append(in.Skipped, store.ReleaseSkip{RequisitionLineID: l.ID, RequisitionNumber: l.RequisitionNumber, LineNo: l.LineNo,
					VendorID: v, Code: prob.Code, Reason: prob.Detail})
			}
			continue
		}
		if err != nil {
			fail(w, r, err)
			return
		}
		in.Groups = append(in.Groups, store.ReleaseGroup{VendorID: v, Lines: lines, Orders: orders, Warnings: warnings})
	}

	run, err := a.store.ReleaseRequisitions(r.Context(), in, store.Idem{Key: key, Hash: hashBody("requisition-releases.create", body)})
	if err != nil {
		fail(w, r, err)
		return
	}
	a.releaseRuns.Add(1)
	a.ordersCreated.Add(int64(len(run.PurchaseOrders)))
	httpx.JSON(w, http.StatusCreated, run)
}

// releaseCandidates reads the consolidation queue page by page, oldest first, up to maxLines.
func (a *API) releaseCandidates(r *http.Request, f store.SourcingFilter, maxLines int) ([]store.SourcingLine, error) {
	f.OldestFirst = true
	var out []store.SourcingLine
	after := ""
	for len(out) < maxLines {
		page, err := a.store.ListOpenRequisitionLines(r.Context(), f, after, releasePageSize)
		if err != nil {
			return nil, err
		}
		out = append(out, page.Items...)
		if page.Next == "" {
			break
		}
		after = page.Next
	}
	if len(out) > maxLines {
		out = out[:maxLines]
	}
	return out, nil
}

// listReleaseRuns handles GET /requisition-releases, newest first.
func (a *API) listReleaseRuns(w http.ResponseWriter, r *http.Request) {
	limit, after, err := listParams(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	q := r.URL.Query()
	f := store.ReleaseRunFilter{LegalEntityID: q.Get("legalEntityId"), Status: q.Get("status")}
	if err := uuidField("legalEntityId", f.LegalEntityID, false); err != nil {
		fail(w, r, err)
		return
	}
	switch f.Status {
	case "", store.ReleaseEmpty, store.ReleaseCompleted, store.ReleasePartial, store.ReleaseFailed:
	default:
		fail(w, r, invalid("status", "must be EMPTY, COMPLETED, PARTIAL or FAILED"))
		return
	}
	page, err := a.store.ListReleaseRuns(r.Context(), f, after, limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	writePage(w, page)
}

func (a *API) getReleaseRun(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.GetReleaseRun(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}
