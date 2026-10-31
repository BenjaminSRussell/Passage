package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
)

// workspacePath is the read-only summary behind the inventory management workspace tiles.
const workspacePath = "/v1/inventory/workspace/summary"

func (a *API) registerWorkspace(r chi.Router) {
	r.With(authz.RequireDuty(dutyRead)).Get(workspacePath, a.workspaceSummary)
}

// transferStatuses lists every transfer order status so the summary always carries a count for each.
var transferStatuses = []string{"CREATED", "SHIPPED", "PARTIALLY_RECEIVED", "RECEIVED", "CANCELLED"}

type workspaceResponse struct {
	Journals struct {
		Draft            int `json:"draft"`
		AwaitingApproval int `json:"awaitingApproval"`
	} `json:"journals"`
	NegativeOnHandRows        int            `json:"negativeOnHandRows"`
	OpenCountSheets           int            `json:"openCountSheets"`
	CountSheetsCounted        int            `json:"countSheetsCounted"`
	ReconciliationDifferences int            `json:"reconciliationDifferences"`
	LastReconciliationAt      *time.Time     `json:"lastReconciliationAt"`
	ActiveReservations        int            `json:"activeReservations"`
	TransferOrdersByStatus    map[string]int `json:"transferOrdersByStatus"`
}

func (a *API) workspaceSummary(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	fields := map[string]string{}
	entity := optionalUUID(r.URL.Query(), "legalEntityId", fields)
	if len(fields) > 0 {
		fieldProblem(w, r, fields)
		return
	}
	s, err := a.store.WorkspaceSummary(r.Context(), entity)
	if err != nil {
		a.failed(w, r, "workspace summary", err)
		return
	}
	out := workspaceResponse{NegativeOnHandRows: s.NegativeOnHandRows, OpenCountSheets: s.OpenCountSheets, CountSheetsCounted: s.CountSheetsCounted,
		ReconciliationDifferences: s.ReconciliationDifferences, LastReconciliationAt: s.LastReconciliationAt, ActiveReservations: s.ActiveReservations, TransferOrdersByStatus: map[string]int{}}
	out.Journals.Draft, out.Journals.AwaitingApproval = s.JournalsDraft, s.JournalsAwaitingApproval
	for _, st := range transferStatuses {
		out.TransferOrdersByStatus[st] = s.TransferOrdersByStatus[st]
	}
	httpx.JSON(w, http.StatusOK, out)
}
