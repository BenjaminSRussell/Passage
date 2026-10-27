package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

// proposalNow is the clock the proposal inquiry defaults its window to; tests pin it.
var proposalNow = time.Now

// campaignProposals proposes campaigns from scheduled orders that share a
// campaign key within a window (FR-23.14). It is a read: nothing is stored,
// and the planner creates a campaign from a proposal with POST /campaigns.
func (a *API) campaignProposals(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	from := proposalNow().UTC().Truncate(time.Minute)
	if raw := q.Get("from"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			fields["from"] = "must be an RFC 3339 time"
		}
		from = t.UTC()
	}
	window := domain.DefaultProposalWindowDays
	if raw := q.Get("windowDays"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > domain.MaxProposalWindowDays {
			fields["windowDays"] = fmt.Sprintf("must be 1 to %d", domain.MaxProposalWindowDays)
		}
		window = n
	}
	resourceID := strings.ToLower(q.Get("resourceId"))
	if resourceID != "" && !ids.Valid(resourceID) {
		fields["resourceId"] = "must be a UUID"
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	items, err := a.store.ProposeCampaigns(r.Context(), store.ProposalFilter{From: from, WindowDays: window, ResourceID: resourceID})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"from": from, "to": from.AddDate(0, 0, window), "windowDays": window, "items": items})
}
