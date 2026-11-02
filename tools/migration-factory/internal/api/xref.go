package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/store"
)

type xrefDTO struct {
	XrefID        string    `json:"xrefId"`
	System        string    `json:"system"`
	Company       string    `json:"company"`
	Table         string    `json:"table"`
	LegacyID      string    `json:"legacyId"`
	PassageEntity string    `json:"passageEntity"`
	PassageID     string    `json:"passageId"`
	RunID         string    `json:"runId"`
	CreatedAt     time.Time `json:"createdAt"`
	// TargetConfirmed is true once passageId is the id the target service assigned (the factory's own before that).
	TargetConfirmed bool `json:"targetConfirmed"`
}

// lookupXref resolves a legacy id to the Passage record it became, or a Passage record to its
// legacy references (FR-49.3, 9.49.5). The tenant comes from the principal, never the query.
// An empty result is a 200 with no items, so a record page can show "no legacy reference".
func (s *Server) lookupXref(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	v := r.URL.Query()
	q := store.XrefQuery{
		System: strings.TrimSpace(v.Get("system")), Company: strings.TrimSpace(v.Get("company")),
		Table: strings.TrimSpace(v.Get("table")), SourceID: strings.TrimSpace(v.Get("id")),
		PassageEntity: strings.TrimSpace(v.Get("passageEntity")), PassageID: strings.TrimSpace(v.Get("passageId")),
	}
	byPassage := q.PassageEntity != "" || q.PassageID != ""
	byLegacy := q.System != "" || q.Table != "" || q.SourceID != "" || q.Company != ""
	switch {
	case byPassage && byLegacy:
		fail(w, r, invalid("look up either by system, table and id, or by passageEntity and passageId"))
		return
	case byPassage:
		if q.PassageEntity == "" || !ids.Valid(q.PassageID) {
			fail(w, r, invalid("passageEntity is required and passageId must be a UUID"))
			return
		}
	default:
		if _, err := domain.ParseSourceSystem(q.System); err != nil {
			fail(w, r, err)
			return
		}
		if q.Table == "" || q.SourceID == "" {
			fail(w, r, invalid("table and id are required with system"))
			return
		}
	}
	rows, err := s.store.LookupXref(r.Context(), q)
	if err != nil {
		fail(w, r, err)
		return
	}
	items := make([]xrefDTO, 0, len(rows))
	for _, x := range rows {
		items = append(items, xrefDTO{XrefID: x.XrefID, System: string(x.SourceSystem), Company: x.SourceCompany, Table: x.SourceTable,
			LegacyID: x.SourceID, PassageEntity: x.PassageEntity, PassageID: x.PassageID, RunID: x.RunID, CreatedAt: x.CreatedAt, TargetConfirmed: x.TargetConfirmed})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}
