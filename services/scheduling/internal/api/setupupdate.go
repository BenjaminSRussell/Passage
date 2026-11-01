package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

type blockPatchRequest struct {
	Priority  *int                `json:"priority"`
	Active    *bool               `json:"active"`
	Condition *[]domain.Predicate `json:"condition"`
}

func (a *API) getCapacityBlock(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := strings.ToLower(chi.URLParam(r, "id"))
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeBlockNotFound, "capacity block not found"))
		return
	}
	b, err := a.store.GetCapacityBlock(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(b.Version))
	httpx.JSON(w, http.StatusOK, b)
}

// updateCapacityBlock changes a block's priority, condition or active flag
// (FR-23.17); deactivating a block is how a planner retires it. The answer
// lists the booked jobs the resource's blocks now keep out.
func (a *API) updateCapacityBlock(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := strings.ToLower(chi.URLParam(r, "id"))
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeBlockNotFound, "capacity block not found"))
		return
	}
	var req blockPatchRequest
	body, ok := decodeBody(w, r, &req)
	if !ok {
		return
	}
	idem, ok := requireIdem(w, r, body)
	if !ok {
		return
	}
	version, ok := ifMatchOf(w, r, "capacity block")
	if !ok {
		return
	}
	idem.Fingerprint = fingerprint(id+"|"+strconv.Itoa(version), body)
	fields := map[string]string{}
	if req.Priority == nil && req.Active == nil && req.Condition == nil {
		fields["body"] = "send at least one of priority, active or condition"
	}
	if req.Priority != nil && (*req.Priority < 0 || *req.Priority > 1000) {
		fields["priority"] = "must be 0 to 1000"
	}
	if req.Condition != nil {
		if len(*req.Condition) > maxPredicates {
			fields["condition"] = fmt.Sprintf("must hold at most %d predicates", maxPredicates)
		}
		for _, p := range *req.Condition {
			if err := p.Validate(); err != nil {
				fields["condition"] = err.Error()
			} else if len(p.Attribute) > maxAttributeLen || len(p.Value) > maxAttributeLen {
				fields["condition"] = fmt.Sprintf("attributes and values are at most %d characters", maxAttributeLen)
			}
		}
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	b, replayed, err := a.store.UpdateCapacityBlock(r.Context(), id, version, store.BlockPatch{Priority: req.Priority, Active: req.Active, Condition: req.Condition}, time.Now().UTC(), idem)
	if err != nil {
		slog.Error("update capacity block failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	w.Header().Set("ETag", etag(b.Version))
	httpx.JSON(w, http.StatusOK, b)
}

type networkPatchRequest struct {
	Name     *string `json:"name"`
	ActiveTo *string `json:"activeTo"`
}

// updateNetwork renames a resource network or ends it on a date (FR-23.13).
func (a *API) updateNetwork(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := strings.ToLower(chi.URLParam(r, "id"))
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeNetworkNotFound, "resource network not found"))
		return
	}
	var req networkPatchRequest
	body, ok := decodeBody(w, r, &req)
	if !ok {
		return
	}
	idem, ok := requireIdem(w, r, body)
	if !ok {
		return
	}
	version, ok := ifMatchOf(w, r, "resource network")
	if !ok {
		return
	}
	idem.Fingerprint = fingerprint(id+"|"+strconv.Itoa(version), body)
	fields := map[string]string{}
	patch := store.NetworkPatch{}
	if req.Name == nil && req.ActiveTo == nil && !sendsNull(body, "activeTo") {
		fields["body"] = "send at least one of name or activeTo"
	}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if n := utf8.RuneCountInString(name); n < 1 || n > 120 {
			fields["name"] = "must be 1 to 120 characters"
		}
		patch.Name = &name
	}
	if req.ActiveTo == nil && sendsNull(body, "activeTo") {
		fields["activeTo"] = "an end date can be moved but not cleared"
	}
	if req.ActiveTo != nil {
		if !validDate(*req.ActiveTo) {
			fields["activeTo"] = "must be a date (YYYY-MM-DD)"
		}
		patch.ActiveTo = req.ActiveTo
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	n, replayed, err := a.store.UpdateNetwork(r.Context(), id, version, patch, idem)
	if err != nil {
		slog.Error("update network failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	w.Header().Set("ETag", etag(n.Version))
	httpx.JSON(w, http.StatusOK, n)
}

// sendsNull reports whether the JSON object body sets field to null, which a
// pointer field cannot tell apart from leaving it out.
func sendsNull(body []byte, field string) bool {
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return false
	}
	v, ok := m[field]
	return ok && bytes.Equal(bytes.TrimSpace(v), []byte("null"))
}
