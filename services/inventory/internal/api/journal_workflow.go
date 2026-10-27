package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
)

// registerJournalWorkflow mounts the dry run and the approval steps of the
// journal state machine (BR-16.11). Submitting, reopening a rejected journal
// and cancelling belong to InventoryJournalMaintain (inventory.journal.write);
// approving and rejecting are the approval task of InventoryJournalPost
// (inventory.journal.post, PRD 3.16.17).
func (a *API) registerJournalWorkflow(r chi.Router) {
	r.With(authz.RequireDuty(dutyJournalWrite)).Post(journalsPath+"/{id}:validate", journalRoute(a.validateJournal))
	r.With(authz.RequireDuty(dutyJournalWrite)).Post(journalsPath+"/{id}:submit", journalRoute(a.moveJournal(domain.JournalSubmitted)))
	r.With(authz.RequireDuty(dutyJournalPost)).Post(journalsPath+"/{id}:approve", journalRoute(a.moveJournal(domain.JournalApproved)))
	r.With(authz.RequireDuty(dutyJournalPost)).Post(journalsPath+"/{id}:reject", journalRoute(a.moveJournal(domain.JournalRejected)))
	r.With(authz.RequireDuty(dutyJournalWrite)).Post(journalsPath+"/{id}:reopen", journalRoute(a.moveJournal(domain.JournalDraft)))
	r.With(authz.RequireDuty(dutyJournalWrite)).Post(journalsPath+"/{id}:cancel", journalRoute(a.moveJournal(domain.JournalCancelled)))
}

// validateJournal answers the dry run (FR-16.6 :validate): 200 with
// valid=true when the journal would post now, otherwise the problem the post
// would return. Nothing is written.
func (a *API) validateJournal(w http.ResponseWriter, r *http.Request, id string) {
	j, err := a.store.ValidateJournal(r.Context(), id, a.post)
	if err != nil {
		a.failed(w, r, "validate journal", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"valid": true, "journalId": j.ID, "status": string(j.Status), "version": j.Version,
		"lineCount": len(j.Lines)})
}

// moveJournal returns the handler of one workflow step. If-Match carries the
// journal version.
func (a *API) moveJournal(to domain.JournalStatus) func(http.ResponseWriter, *http.Request, string) {
	return func(w http.ResponseWriter, r *http.Request, id string) {
		version, err := ifMatchVersion(r)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		j, err := a.store.TransitionJournal(r.Context(), id, version, to)
		if err != nil {
			a.failed(w, r, "move journal to "+string(to), err)
			return
		}
		writeJournal(w, http.StatusOK, j)
	}
}

// mayKeyJournal checks the extra duty of a journal type: an OWNERSHIP_CHANGE
// journal is ConsignmentMaintain's (inventory.consignment.write, PRD 3.16.17).
func (a *API) mayKeyJournal(w http.ResponseWriter, r *http.Request, t domain.JournalType) bool {
	if t != domain.JournalOwnershipChange {
		return true
	}
	if p, _ := tenant.FromContext(r.Context()); p.HasDuty(dutyConsignmentWrite) {
		return true
	}
	httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "missing duty "+dutyConsignmentWrite))
	return false
}
