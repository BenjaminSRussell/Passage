package api

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/eam/internal/store"
)

// locationCode is one segment of a functional-location path; "/" joins segments.
var locationCode = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_.-]{0,39}$`)

type locationIn struct {
	Code         string  `json:"code"`
	Name         string  `json:"name"`
	SiteID       string  `json:"siteId"`
	ParentID     *string `json:"parentId,omitempty"`
	LocationType *string `json:"locationType,omitempty"`
}

func (h *Handler) createLocation(w http.ResponseWriter, r *http.Request) {
	key, err := idempotencyKey(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body locationIn
	if err := decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.LocationType != nil && *body.LocationType == "" {
		body.LocationType = nil
	}
	switch {
	case !locationCode.MatchString(body.Code):
		fail(w, r, invalid("code must match ^[A-Z0-9][A-Z0-9_.-]{0,39}$"))
		return
	case body.Name == "" || len(body.Name) > 120:
		fail(w, r, invalid("name must be 1-120 characters"))
		return
	case !ids.Valid(body.SiteID):
		fail(w, r, invalid("siteId must be a UUID"))
		return
	case body.ParentID != nil && !ids.Valid(*body.ParentID):
		fail(w, r, invalid("parentId must be a UUID"))
		return
	case body.LocationType != nil && !assetType.MatchString(*body.LocationType):
		fail(w, r, invalid("locationType must match ^[A-Z0-9_]{2,20}$"))
		return
	}
	out, err := h.store.CreateLocation(r.Context(), store.FunctionalLocation{Code: body.Code, Name: body.Name,
		SiteID: body.SiteID, ParentID: body.ParentID, LocationType: body.LocationType}, key)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

// listLocations pages a site's locations in path order: site and under (a
// location id, its subtree) narrow the list; limit and cursor as on every list.
func (h *Handler) listLocations(w http.ResponseWriter, r *http.Request) {
	opt, err := listOptions(r, nil)
	if err != nil {
		fail(w, r, err)
		return
	}
	q := r.URL.Query()
	in := store.LocationListOptions{SiteID: q.Get("site"), Under: q.Get("under"), Cursor: opt.Cursor, Limit: opt.Limit}
	if in.SiteID != "" && !ids.Valid(in.SiteID) {
		fail(w, r, invalid("site must be a UUID"))
		return
	}
	if in.Under != "" && !ids.Valid(in.Under) {
		fail(w, r, invalid("under must be a functional location id"))
		return
	}
	out, err := h.store.ListLocations(r.Context(), in)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) getLocation(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := h.store.GetLocation(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// placeIn names the target functional location; null removes the asset from its location.
type placeIn struct {
	FunctionalLocationID *string `json:"functionalLocationId"`
}

func (h *Handler) placeAsset(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
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
	var body placeIn
	if err := decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	target := ""
	if body.FunctionalLocationID != nil {
		target = *body.FunctionalLocationID
		if !ids.Valid(target) {
			fail(w, r, invalid("functionalLocationId must be a UUID, or null to remove the asset"))
			return
		}
	}
	out, err := h.store.PlaceAsset(r.Context(), id, target, version, key)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) listLocationHistory(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := h.store.ListLocationHistory(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}
