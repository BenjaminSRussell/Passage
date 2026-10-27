package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	"github.com/BenjaminSRussell/passage/services/edi/internal/store"
)

// Message monitor, reprocess and partner SLA dashboard (FR-35.10, PRD
// §6.35.6 and §6.35.14).

// messageStatuses are the stored message statuses (PRD §6.35.4).
var messageStatuses = map[string]bool{
	"RECEIVED": true, "MAPPED": true, "POSTED": true, "FAILED": true, "SENT": true, "ACKED": true,
}

type messagePage struct {
	Items      []store.MessageRecord `json:"items"`
	NextCursor string                `json:"nextCursor,omitempty"`
}

// listMessages serves the message monitor: every stored message, newest first,
// filtered by partner, direction, status, transaction set and ISA13, paged
// with an opaque keyset cursor. Rows carry no canonical document.
func (a *API) listMessages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.MessageFilter{PartnerID: q.Get("partnerId"), Direction: q.Get("direction"), Status: q.Get("status"),
		TransactionSet: q.Get("transactionSet"), Limit: defaultListLimit}
	fields := map[string]string{}
	if f.PartnerID != "" && !validID(f.PartnerID) {
		fields["partnerId"] = "must be a UUID"
	}
	if f.Direction != "" && f.Direction != "IN" && f.Direction != "OUT" {
		fields["direction"] = "must be IN or OUT"
	}
	if f.Status != "" && !messageStatuses[f.Status] {
		fields["status"] = "must be RECEIVED, MAPPED, POSTED, FAILED, SENT or ACKED"
	}
	if f.TransactionSet != "" && !digits(f.TransactionSet, 3, 3) {
		fields["transactionSet"] = "must be a three-digit transaction set id"
	}
	if v := q.Get("controlNumber"); v != "" {
		if !digits(v, 1, 9) {
			fields["controlNumber"] = "must be an ISA13 of 1 to 9 digits"
		} else {
			f.ControlNumber = strings.Repeat("0", 9-len(v)) + v
		}
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxListLimit {
			fields["limit"] = fmt.Sprintf("must be 1 to %d", maxListLimit)
		}
		f.Limit = n
	}
	if v := q.Get("cursor"); v != "" {
		c, err := decodeCursor(v)
		if err != nil {
			fields["cursor"] = "is not a cursor returned by this list"
		}
		f.After = &c
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	items, err := a.store.ListMessages(r.Context(), f)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	page := messagePage{Items: items}
	if len(items) == f.Limit {
		last := items[len(items)-1]
		page.NextCursor = encodeCursor(store.Cursor{At: last.CreatedAt, ID: last.ID})
	}
	httpx.JSON(w, http.StatusOK, page)
}

// getMessage returns one message with its envelope and canonical document.
// The raw payload is not returned (BR-35.12 masking viewer is not built).
func (a *API) getMessage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !validID(id) {
		httpx.Fail(w, r, notFound("message"))
		return
	}
	m, err := a.store.GetMessage(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(w, r, notFound("message"))
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, m)
}

// digits reports whether s is between min and max ASCII digits long.
func digits(s string, min, max int) bool {
	if len(s) < min || len(s) > max {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// encodeCursor makes an opaque token from a keyset position.
func encodeCursor(c store.Cursor) string {
	return base64.RawURLEncoding.EncodeToString([]byte(c.At.UTC().Format(time.RFC3339Nano) + "|" + c.ID))
}

// decodeCursor reverses encodeCursor and rejects anything that is not a
// timestamp and a canonical UUID.
func decodeCursor(s string) (store.Cursor, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return store.Cursor{}, err
	}
	at, id, ok := strings.Cut(string(b), "|")
	if !ok || !validID(id) {
		return store.Cursor{}, errors.New("cursor shape")
	}
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return store.Cursor{}, err
	}
	return store.Cursor{At: t, ID: id}, nil
}

// errStoredPayload means the raw payload recorded at receipt no longer parses
// (the parser became stricter since).
var errStoredPayload = errors.New("the stored interchange no longer parses")

// reprocessPlan maps each failed message again from the raw interchange. A
// message is found by transaction set and ST02 among the non-997 groups. A
// transaction that is missing, or whose set and ST02 occur more than once (ST02
// is only unique inside a group), stays FAILED with the reason.
func reprocessPlan(raw string, failed []store.Message) ([]store.Message, error) {
	ic, err := domain.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errStoredPayload, err)
	}
	byKey := map[string][]domain.Transaction{}
	for _, g := range ic.Groups {
		if g.FunctionalID == "FA" {
			continue
		}
		for _, t := range g.Transactions {
			k := t.SetID + "/" + t.Control
			byKey[k] = append(byKey[k], t)
		}
	}
	out := make([]store.Message, 0, len(failed))
	for _, m := range failed {
		next := store.Message{ID: m.ID, TransactionSet: m.TransactionSet, STControl: m.STControl, Status: "FAILED"}
		switch ts := byKey[m.TransactionSet+"/"+m.STControl]; len(ts) {
		case 0:
			next.Error = fmt.Sprintf("transaction set %s ST02 %s is not in the stored interchange", m.TransactionSet, m.STControl)
		case 1:
			res, err := mapTransaction(ts[0])
			if err != nil {
				return nil, err
			}
			next.Status, next.Canonical, next.Error = res.status, res.canonical, res.errText
		default:
			next.Error = fmt.Sprintf("transaction set %s ST02 %s occurs in more than one group; reprocess cannot tell them apart", m.TransactionSet, m.STControl)
		}
		out = append(out, next)
	}
	return out, nil
}

// reprocessInterchange re-runs mapping for the failed messages of one inbound
// interchange (POST /interchanges/{id}:reprocess). With dryRun=true it returns
// the outcome without writing anything and needs no Idempotency-Key.
func (a *API) reprocessInterchange(w http.ResponseWriter, r *http.Request) {
	dryRun := false
	switch r.URL.Query().Get("dryRun") {
	case "", "false":
	case "true":
		dryRun = true
	default:
		httpx.Fail(w, r, validation(map[string]string{"dryRun": "must be true or false"}))
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if !dryRun && (key == "" || len(key) > 255) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, CodeIdempotencyKey, "Idempotency-Key header is required (max 255 characters)"))
		return
	}
	id := chi.URLParam(r, "id")
	if !validID(id) {
		httpx.Fail(w, r, notFound("interchange"))
		return
	}
	sum := sha256.Sum256([]byte("reprocess\x00" + id))
	res, _, err := a.store.ReprocessInterchange(r.Context(), key, hex.EncodeToString(sum[:]), id, dryRun, reprocessPlan)
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.Fail(w, r, notFound("interchange"))
	case errors.Is(err, store.ErrNotInbound):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeNotInbound, "only inbound interchanges are reprocessed; an outbound document is resent"))
	case errors.Is(err, store.ErrIdempotencyReuse):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request"))
	case errors.Is(err, errStoredPayload):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, CodeSyntax, err.Error()))
	case err != nil:
		httpx.Fail(w, r, err)
	default:
		httpx.JSON(w, http.StatusOK, res)
	}
}

// SLA dashboard window in days: default and maximum.
const (
	defaultSLADays = 30
	maxSLADays     = 365
)

type slaRow struct {
	store.PartnerSLA
	// FailureRatePct is Failed / Messages × 100 rounded to two decimals.
	FailureRatePct float64 `json:"failureRatePct"`
	// P95AckMinutes is rounded to one decimal; absent when nothing was acked.
	P95AckMinutes *float64 `json:"p95AckMinutes,omitempty"`
}

// partnerSLA serves the partner SLA dashboard over the last `days` days.
func (a *API) partnerSLA(w http.ResponseWriter, r *http.Request) {
	days := defaultSLADays
	if v := r.URL.Query().Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxSLADays {
			httpx.Fail(w, r, validation(map[string]string{"days": fmt.Sprintf("must be 1 to %d", maxSLADays)}))
			return
		}
		days = n
	}
	rows, err := a.store.PartnerSLAs(r.Context(), days)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out := make([]slaRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, toSLARow(row))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"days": days, "items": out})
}

func toSLARow(row store.PartnerSLA) slaRow {
	v := slaRow{PartnerSLA: row}
	if row.Messages > 0 {
		v.FailureRatePct = round(float64(row.Failed)*100/float64(row.Messages), 2)
	}
	if row.P95AckSeconds != nil {
		m := round(*row.P95AckSeconds/60, 1)
		v.P95AckMinutes = &m
	}
	return v
}

// round rounds half away from zero to the given number of decimals.
func round(v float64, decimals int) float64 {
	p := math.Pow(10, float64(decimals))
	return math.Round(v*p) / p
}
