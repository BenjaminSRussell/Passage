package api

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
)

// deliveryScheduleBody is the PUT body (FR-17.10): the rows of the line's delivery schedule.
type deliveryScheduleBody struct {
	Rows []scheduleRowBody `json:"rows"`
}

type scheduleRowBody struct {
	Qty          string `json:"qty"`
	DeliveryDate string `json:"deliveryDate"`
}

// confirmDateBody is the PUT body that records the vendor's confirmed date.
type confirmDateBody struct {
	ConfirmedDate string `json:"confirmedDate"`
}

// buildSchedule validates the shape of the PUT body; the schedule rules (sum, dates order) are
// the domain's.
func buildSchedule(body deliveryScheduleBody) ([]domain.ScheduleInput, error) {
	if len(body.Rows) == 0 || len(body.Rows) > domain.MaxScheduleRows {
		return nil, invalid("rows", "must hold from 1 to "+strconv.Itoa(domain.MaxScheduleRows)+" rows")
	}
	out := make([]domain.ScheduleInput, 0, len(body.Rows))
	for i, r := range body.Rows {
		field := func(n string) string { return "rows[" + strconv.Itoa(i) + "]." + n }
		qty, err := decimalField(field("qty"), r.Qty, "")
		if err != nil {
			return nil, err
		}
		if !qty.IsPositive() {
			return nil, invalid(field("qty"), "must be positive")
		}
		date, err := dateField(field("deliveryDate"), r.DeliveryDate)
		if err != nil {
			return nil, err
		}
		out = append(out, domain.ScheduleInput{Qty: qty, DeliveryDate: date})
	}
	return out, nil
}

// listDeliverySchedules handles GET /purchase-orders/{id}/delivery-schedules: every schedule of
// the PO in line order.
func (a *API) listDeliverySchedules(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	items, err := a.store.ListDeliverySchedules(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, pageBody[domain.DeliverySchedule]{Items: items})
}

// getDeliverySchedule handles GET /purchase-orders/{id}/lines/{lineNo}/delivery-schedule. The ETag
// is the PO row version, which PUT and DELETE take as If-Match.
func (a *API) getDeliverySchedule(w http.ResponseWriter, r *http.Request) {
	id, lineNo, err := planLine(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.GetDeliverySchedule(r.Context(), id, lineNo)
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(out.PORowVersion))
	httpx.JSON(w, http.StatusOK, out)
}

// putDeliverySchedule handles PUT /purchase-orders/{id}/lines/{lineNo}/delivery-schedule: replaces
// the line's schedule (FR-17.10). If-Match is the PO row version (BR-17.8). PUT is idempotent, so
// no Idempotency-Key is taken.
func (a *API) putDeliverySchedule(w http.ResponseWriter, r *http.Request) {
	id, lineNo, err := planLine(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	want, err := ifMatch(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body deliveryScheduleBody
	if err := httpx.Decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	in, err := buildSchedule(body)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.PutDeliverySchedule(r.Context(), id, lineNo, want, in)
	if err != nil {
		fail(w, r, err)
		return
	}
	a.deliverySchedulesSaved.Add(1)
	w.Header().Set("ETag", etag(out.PORowVersion))
	httpx.JSON(w, http.StatusOK, out)
}

// deleteDeliverySchedule handles DELETE /purchase-orders/{id}/lines/{lineNo}/delivery-schedule.
func (a *API) deleteDeliverySchedule(w http.ResponseWriter, r *http.Request) {
	id, lineNo, err := planLine(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	want, err := ifMatch(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	v, err := a.store.DeleteDeliverySchedule(r.Context(), id, lineNo, want)
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(v))
	w.WriteHeader(http.StatusNoContent)
}

// confirmDeliveryDate handles PUT .../delivery-schedule/rows/{seq}/confirmation: the vendor's
// confirmed date for one row of a CONFIRMED PO. PUT is idempotent: the same date again changes
// nothing.
func (a *API) confirmDeliveryDate(w http.ResponseWriter, r *http.Request) {
	id, lineNo, err := planLine(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	seq, err := strconv.Atoi(chi.URLParam(r, "seq"))
	if err != nil || seq < 1 || seq > domain.MaxScheduleRows {
		fail(w, r, invalid("seq", "must be a row number from 1 to "+strconv.Itoa(domain.MaxScheduleRows)))
		return
	}
	want, err := ifMatch(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body confirmDateBody
	if err := httpx.Decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	date, err := dateField("confirmedDate", body.ConfirmedDate)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.ConfirmDeliveryDate(r.Context(), id, lineNo, seq, want, date)
	if err != nil {
		fail(w, r, err)
		return
	}
	a.deliveryDatesConfirmed.Add(1)
	w.Header().Set("ETag", etag(out.PORowVersion))
	httpx.JSON(w, http.StatusOK, out)
}
