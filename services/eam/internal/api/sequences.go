package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/eam/internal/domain"
	"github.com/BenjaminSRussell/passage/services/eam/internal/store"
)

// sequenceUnits are the time units of a sequence's base interval, as on plan lines.
var sequenceUnits = map[string]bool{"DAYS": true, "WEEKS": true, "MONTHS": true, "YEARS": true}

type sequenceIn struct {
	Code              string                `json:"code"`
	Name              string                `json:"name"`
	AssetType         *string               `json:"assetType,omitempty"`
	AssetID           *string               `json:"assetId,omitempty"`
	BaseIntervalValue int                   `json:"baseIntervalValue"`
	BaseIntervalUnit  *string               `json:"baseIntervalUnit,omitempty"`
	CounterID         *string               `json:"counterId,omitempty"`
	Steps             []domain.SequenceStep `json:"steps"`
}

func (h *Handler) createSequence(w http.ResponseWriter, r *http.Request) {
	key, err := idempotencyKey(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body sequenceIn
	if err := decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	switch {
	case !planCode.MatchString(body.Code):
		fail(w, r, invalid("code must match ^[A-Z0-9][A-Z0-9_-]{1,29}$"))
		return
	case body.Name == "" || len(body.Name) > 120:
		fail(w, r, invalid("name must be 1-120 characters"))
		return
	case body.AssetType != nil && !assetType.MatchString(*body.AssetType):
		fail(w, r, invalid("assetType must match ^[A-Z0-9_]{2,20}$"))
		return
	case body.AssetID != nil && !ids.Valid(*body.AssetID):
		fail(w, r, invalid("assetId must be a UUID"))
		return
	case body.CounterID != nil && !ids.Valid(*body.CounterID):
		fail(w, r, invalid("counterId must be a UUID"))
		return
	case body.BaseIntervalUnit != nil && !sequenceUnits[*body.BaseIntervalUnit]:
		fail(w, r, invalid("baseIntervalUnit must be DAYS, WEEKS, MONTHS or YEARS"))
		return
	case body.BaseIntervalValue < 1 || body.BaseIntervalValue > 100000:
		fail(w, r, invalid("baseIntervalValue must be 1-100000"))
		return
	case len(body.Steps) == 0 || len(body.Steps) > 50:
		fail(w, r, invalid("steps must hold 1-50 entries"))
		return
	}
	// Step structure (codes, multiples, hierarchy, includes) is a business rule:
	// the store answers it with EAM-SEQ-001.
	out, err := h.store.CreateSequence(r.Context(), store.MaintenanceSequence{Code: body.Code, Name: body.Name,
		AssetType: body.AssetType, AssetID: body.AssetID, BaseIntervalValue: body.BaseIntervalValue,
		BaseIntervalUnit: body.BaseIntervalUnit, CounterID: body.CounterID, Steps: body.Steps}, key)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func (h *Handler) listSequences(w http.ResponseWriter, r *http.Request) {
	opt, err := listOptions(r, nil)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := h.store.ListSequences(r.Context(), opt)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) getSequence(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := h.store.GetSequence(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// simulateSequence returns the next occurrences (default 12, at most 120)
// without generating them.
func (h *Handler) simulateSequence(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	n := 12
	if raw := r.URL.Query().Get("occurrences"); raw != "" {
		n, err = strconv.Atoi(raw)
		if err != nil || n < 1 || n > 120 {
			fail(w, r, invalid("occurrences must be 1-120"))
			return
		}
	}
	out, err := h.store.SimulateSequence(r.Context(), id, n)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}

type occurrenceIn struct {
	Currency string     `json:"currency"`
	DueAt    *time.Time `json:"dueAt,omitempty"`
}

func (h *Handler) generateOccurrence(w http.ResponseWriter, r *http.Request) {
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
	var body occurrenceIn
	if r.ContentLength != 0 {
		if err := decode(r, &body); err != nil {
			fail(w, r, err)
			return
		}
	}
	if body.Currency == "" {
		body.Currency = "USD"
	}
	if !currencyCode.MatchString(body.Currency) {
		fail(w, r, invalid("currency must be an ISO 4217 code"))
		return
	}
	out, err := h.store.GenerateOccurrence(r.Context(), id, store.OccurrenceInput{Currency: body.Currency, DueAt: body.DueAt}, key)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func (h *Handler) listOccurrences(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := h.store.ListOccurrences(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}
