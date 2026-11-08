package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// dutyRequisitionApprove covers :approve, :reject and the approval inbox (PRD 3.17.17 RequisitionApprove).
const dutyRequisitionApprove = "procurement.requisition.approve"

const (
	// codeNotCategoryManager is the 403 for a CATEGORY_OWNER level decided by someone who manages none of the
	// requisition's categories.
	codeNotCategoryManager = "PRC-REQ-011"
	maxDecisionCommentLen  = 1000
)

type requisitionCommentBody struct {
	Comment string `json:"comment"`
}

// readRequisitionComment reads the optional body of a requisition action; an empty body is no comment.
func readRequisitionComment(r *http.Request) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		return "", err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return "", nil
	}
	var body requisitionCommentBody
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		return "", invalid("body", "must be a JSON object with an optional comment")
	}
	comment := strings.TrimSpace(body.Comment)
	if utf8.RuneCountInString(comment) > maxDecisionCommentLen {
		return "", invalid("comment", "must be at most 1000 characters")
	}
	return comment, nil
}

// decideRequisition handles POST /requisitions/{id}:approve|reject|reopen|cancel (BR-17.1). Approve and reject are
// for approvers inside the legal entity scope; reopen and cancel are for the requester. An approver is held to
// their REQUISITION approval limit (BR-17.15), and the CATEGORY_OWNER level to the managers of the requisition's
// categories (FR-17.20) once any are assigned.
func (a *API) decideRequisition(w http.ResponseWriter, r *http.Request, id, action, userID string) {
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
	version, err := ifMatch(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	comment, err := readRequisitionComment(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	if action == domain.ReqActionReject && comment == "" {
		fail(w, r, invalid("comment", "is required to reject a requisition"))
		return
	}
	cur, err := a.store.GetRequisition(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	if err := inScope(p, cur.LegalEntityID); err != nil {
		fail(w, r, err)
		return
	}
	switch action {
	case domain.ReqActionApprove, domain.ReqActionReject:
		if cur.RequesterID == userID {
			fail(w, r, httpx.NewProblem(http.StatusForbidden, store.CodeRequisitionSelfApproval, "the requester cannot decide on their own requisition"))
			return
		}
		// The level rules (approval limit, category manager) are checked against the level at the If-Match version.
		// The store refuses any other version under its row lock, so the level cannot move between this check and
		// the write. A requisition already past that version is a replay of an earlier decision (answered from the
		// Idempotency-Key with the original outcome, even when the next level has another role) or a 409, so the
		// check is left to the store, as for :approve on a purchase order.
		if action == domain.ReqActionApprove && cur.Status == store.RequisitionInReview && cur.Version == version {
			if err := a.approverCheck(r, cur.LegalEntityID, userID, domain.DocRequisition, cur.Total, cur.Currency); err != nil {
				fail(w, r, err)
				return
			}
			if err := a.checkCategoryOwner(r, cur, userID); err != nil {
				fail(w, r, err)
				return
			}
		}
	default:
		if cur.RequesterID != userID {
			fail(w, r, httpx.NewProblem(http.StatusForbidden, codeNotRequester, "only the requester can "+action+" this requisition"))
			return
		}
	}
	act := store.RequisitionAct{Action: action, ActorID: userID, Comment: comment}
	out, err := a.store.ActOnRequisition(r.Context(), id, version, act,
		store.Idem{Key: key, Hash: hashBody("requisition."+action, map[string]any{"id": id, "version": version, "comment": comment})})
	if err != nil {
		fail(w, r, err)
		return
	}
	switch action {
	case domain.ReqActionApprove, domain.ReqActionReject:
		a.requisitionDecisions.Add(1)
	case domain.ReqActionCancel:
		a.requisitionsCancelled.Add(1)
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

// checkCategoryOwner holds the CATEGORY_OWNER level to the managers of the categories on the requisition's lines.
// A requisition whose categories have no manager assigned is open to any approver, as the level was before
// managers existed.
func (a *API) checkCategoryOwner(r *http.Request, cur store.Requisition, userID string) error {
	if domain.NextApprover(cur.ApproverChain, cur.ApprovedSteps) != domain.RoleCategoryOwner {
		return nil
	}
	seen := map[string]bool{}
	var managed []string
	for _, l := range cur.Lines {
		if l.Category == "" || seen[l.Category] {
			continue
		}
		seen[l.Category] = true
		page, err := a.store.ListCategoryManagers(r.Context(), store.CategoryManagerFilter{LegalEntityID: cur.LegalEntityID, Category: l.Category}, "", store.MaxPageSize)
		if err != nil {
			return err
		}
		for _, m := range page.Items {
			managed = append(managed, m.WorkerID)
			if m.WorkerID == userID {
				return nil
			}
		}
	}
	if len(managed) == 0 {
		return nil
	}
	return httpx.NewProblem(http.StatusForbidden, codeNotCategoryManager,
		"this level needs a manager of one of the requisition's categories; you manage none of them")
}

// listRequisitionApprovals handles GET /requisition-approvals: the IN_REVIEW requisitions with the role that decides
// next. The caller's own requisitions are left out because they cannot decide on them.
func (a *API) listRequisitionApprovals(w http.ResponseWriter, r *http.Request) {
	limit, after, err := listParams(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	p, err := principal(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	q := r.URL.Query()
	f := store.ApprovalQueueFilter{LegalEntityID: q.Get("legalEntityId"), Role: q.Get("role")}
	if f.LegalEntityID != "" {
		if err := uuidField("legalEntityId", f.LegalEntityID, true); err != nil {
			fail(w, r, err)
			return
		}
		if err := inScope(p, f.LegalEntityID); err != nil {
			fail(w, r, err)
			return
		}
	}
	if f.Role != "" && !slices.Contains([]string{domain.RoleManager, domain.RoleCategoryOwner, domain.RoleCFO, domain.RoleNextApprover}, f.Role) {
		fail(w, r, invalid("role", "must be MANAGER, CATEGORY_OWNER, CFO or NEXT_APPROVER"))
		return
	}
	if ids.Valid(p.UserID) {
		f.ExcludeRequesterID = p.UserID
	}
	page, err := a.store.ListRequisitionApprovals(r.Context(), f, after, limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	writePage(w, page)
}

func (a *API) registerRequisitionApprovalRoutes(r chi.Router) {
	r.With(authz.Require(anyDuty{dutyRequisitionApprove, dutyPurchaseOrderInquire},
		dutyRequisitionApprove+" or "+dutyPurchaseOrderInquire, "")).Get("/requisition-approvals", a.listRequisitionApprovals)
}
