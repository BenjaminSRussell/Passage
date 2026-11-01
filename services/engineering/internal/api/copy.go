package api

import (
	"fmt"
	"net/http"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/store"
)

// bomCopyRequest carries the new version's effectivity and, optionally, another product or site and
// the source lines to keep (FR-9.12). An omitted productId or siteId keeps the source's; siteId ""
// makes the copy valid for every site. An omitted lineNos copies every line.
type bomCopyRequest struct {
	ProductID     string  `json:"productId,omitempty"`
	SiteID        *string `json:"siteId,omitempty"`
	EffectiveFrom string  `json:"effectiveFrom"`
	EffectiveTo   string  `json:"effectiveTo,omitempty"`
	LineNos       []int   `json:"lineNos,omitempty"`
}

// routeCopyRequest carries the new route version's effectivity and, optionally, another product or
// site; an omitted productId or siteId keeps the source's.
type routeCopyRequest struct {
	ProductID     string `json:"productId,omitempty"`
	SiteID        string `json:"siteId,omitempty"`
	EffectiveFrom string `json:"effectiveFrom"`
	EffectiveTo   string `json:"effectiveTo,omitempty"`
}

// copyDates validates the requested effectivity before the source is loaded, so a bad request is a
// 400 whether or not the source exists.
func copyDates(from, to string) map[string]string {
	fields := map[string]string{}
	start, ok := parseDate(from)
	if !ok {
		fields["effectiveFrom"] = "is required as YYYY-MM-DD"
	}
	end := optionalDate("effectiveTo", to, fields)
	if ok && !end.IsZero() && end.Before(start) {
		fields["effectiveTo"] = "must not be before effectiveFrom"
	}
	return fields
}

// copyIdem keys a copy on the source as well as the body, so one key cannot copy two sources.
func copyIdem(r *http.Request, sourceID string, body any) store.Idem {
	return store.Idem{Key: r.Header.Get(idempotencyKey), Hash: hashOf(struct {
		Source string
		Body   any
	}{sourceID, body})}
}

// keepLines returns the source lines named in lineNos, in source order; nil keeps them all.
func keepLines(lines []bomLineBody, lineNos []int, fields map[string]string) []bomLineBody {
	if lineNos == nil {
		return lines
	}
	if len(lineNos) == 0 {
		fields["lineNos"] = "must name at least one line, or be omitted to copy every line"
		return nil
	}
	present := make(map[int]bool, len(lines))
	for _, l := range lines {
		present[l.LineNo] = true
	}
	wanted := make(map[int]bool, len(lineNos))
	for _, n := range lineNos {
		if !present[n] {
			fields["lineNos"] = fmt.Sprintf("line %d is not on the source version", n)
			return nil
		}
		wanted[n] = true
	}
	var kept []bomLineBody
	for _, l := range lines {
		if wanted[l.LineNo] {
			kept = append(kept, l)
		}
	}
	return kept
}

// copyBomVersion creates a new draft BOM version from any existing version (FR-9.12): the header
// quantity range and the chosen lines are copied, the product and site are the source's unless the
// request names others, and the effectivity is the request's. The source may be in any status
// (a cancelled or expired version is a valid template) and is not changed. The draft records the
// source as copiedFromVersionId, goes through the same cycle check as a create, emits no event (a
// created draft emits none) and is activated or released through an ECO like any other draft.
func (a *API) copyBomVersion(w http.ResponseWriter, r *http.Request) {
	var body bomCopyRequest
	if !readBody(w, r, &body) {
		return
	}
	sourceID, ok := pathID(w, r)
	if !ok {
		return
	}
	if fields := copyDates(body.EffectiveFrom, body.EffectiveTo); len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	source, err := a.store.GetBomVersion(r.Context(), sourceID)
	if err != nil {
		a.fail(w, r, "copy bom version", err, &a.bomFailed)
		return
	}
	request := bodyFromVersion(source)
	request.ID, request.Status, request.EffectiveFrom, request.EffectiveTo = "", domain.StatusDraft, body.EffectiveFrom, body.EffectiveTo
	if body.ProductID != "" {
		request.ProductID = body.ProductID
	}
	fields := map[string]string{}
	if body.SiteID != nil {
		if len(*body.SiteID) > maxIDLength {
			fields["siteId"] = fmt.Sprintf("must be at most %d characters", maxIDLength)
		}
		request.SiteID = *body.SiteID
	}
	request.Lines = keepLines(request.Lines, body.LineNos, fields)
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	draft, problems := request.toDomain()
	if len(problems) > 0 {
		invalid(w, r, problems)
		return
	}
	draft.CopiedFrom = source.ID
	created, err := a.store.CreateBomVersion(r.Context(), draft, copyIdem(r, sourceID, body), checkVersion(draft))
	if err != nil {
		a.fail(w, r, "copy bom version", err, &a.bomFailed)
		return
	}
	a.bomCopied.Add(1)
	httpx.JSON(w, http.StatusCreated, bodyFromVersion(created))
}

// copyRouteVersion creates a new draft route version from any existing one (FR-9.12): the route
// code, operations and requirements are copied, the product and site are the source's unless the
// request names others, the version number is the next one for that product, site and route code,
// and the effectivity is the request's. The draft records the source as copiedFromVersionId and
// emits no event; requirements are checked against the target site's resources at activation.
func (a *API) copyRouteVersion(w http.ResponseWriter, r *http.Request) {
	var body routeCopyRequest
	if !readBody(w, r, &body) {
		return
	}
	sourceID, ok := pathID(w, r)
	if !ok {
		return
	}
	if fields := copyDates(body.EffectiveFrom, body.EffectiveTo); len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	source, err := a.store.GetRouteVersion(r.Context(), sourceID)
	if err != nil {
		a.fail(w, r, "copy route version", err, &a.routeFailed)
		return
	}
	request := routeVersionRequest{
		ProductID: source.ProductID, SiteID: source.SiteID, RouteCode: source.RouteCode, EffectiveFrom: body.EffectiveFrom, EffectiveTo: body.EffectiveTo,
		Operations: bodyFromRouteVersion(source).Operations,
	}
	if body.ProductID != "" {
		request.ProductID = body.ProductID
	}
	if body.SiteID != "" {
		request.SiteID = body.SiteID
	}
	draft, problems := request.toDomain()
	if len(problems) > 0 {
		invalid(w, r, problems)
		return
	}
	draft.CopiedFrom = source.ID
	created, err := a.store.CreateRouteVersion(r.Context(), draft, copyIdem(r, sourceID, body))
	if err != nil {
		a.fail(w, r, "copy route version", err, &a.routeFailed)
		return
	}
	a.routeCopied.Add(1)
	httpx.JSON(w, http.StatusCreated, bodyFromRouteVersion(created))
}

// cancelRouteVersion moves a draft route version to cancelled (BR-9.1). Unlike a BOM draft there is
// no open-ECO guard: an ECO only replaces BOM versions (eco_replacement references bom_version), so
// no ECO release can be waiting on a route draft.
func (a *API) cancelRouteVersion(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	v, err := a.store.CancelRouteVersion(r.Context(), id)
	if err != nil {
		a.fail(w, r, "cancel route version", err, &a.routeFailed)
		return
	}
	a.routeCancelled.Add(1)
	httpx.JSON(w, http.StatusOK, bodyFromRouteVersion(v))
}
