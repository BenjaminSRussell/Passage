package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"

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
	// An X12 set (three digits), an EDIFACT message type (ORDERS, INVOIC) or a cXML document (InvoiceDetailRequest).
	if f.TransactionSet != "" && !digits(f.TransactionSet, 3, 3) && !letters(f.TransactionSet) {
		fields["transactionSet"] = "must be a three-digit transaction set id, an EDIFACT message type or a cXML document name"
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

type messageDetailView struct {
	store.MessageRecord
	PIIMasked bool `json:"piiMasked"`
}

// getMessage returns one message with its envelope and canonical document.
// Party names in the document are masked without edi.pii.view (BR-35.12); the
// raw payload is served only as the masked segment tree (getPayload).
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
	masked := !piiView(r)
	if masked {
		m.Canonical = domain.MaskCanonical(m.Canonical)
	}
	httpx.JSON(w, http.StatusOK, messageDetailView{MessageRecord: m, PIIMasked: masked})
}

// digits reports whether s is between min and max ASCII digits long.
// letters reports whether s is 3 to 40 ASCII letters.
func letters(s string) bool {
	if len(s) < 3 || len(s) > 40 {
		return false
	}
	for _, c := range s {
		if (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') {
			return false
		}
	}
	return true
}

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
// (the parser became stricter since). Bulk reprocess reports it per item.
var errStoredPayload = store.ErrUnreadable

// reprocessPlan maps each failed message again from the raw interchange. A
// message is found by transaction set and ST02 among the non-997 groups. A
// transaction that is missing, or whose set and ST02 occur more than once (ST02
// is only unique inside a group), stays FAILED with the reason. An EDIFACT
// interchange is reprocessed by edifactReprocessPlan.
func reprocessPlan(raw string, failed []store.Message) ([]store.Message, error) {
	if strings.HasPrefix(strings.TrimSpace(raw), "<") {
		return cxmlReprocessPlan(raw, failed)
	}
	if domain.DetectSyntax([]byte(raw)) == domain.SyntaxEdifact {
		return edifactReprocessPlan(raw, failed)
	}
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

// cxmlReprocessPlan is reprocessPlan for a cXML payload (#35, 35-4): the one document of the payload is mapped again, so a failed
// message whose document now maps (the mapping was fixed, or the document was edited by a support tool) becomes MAPPED and is put
// onto purchasing or receiving by :post. A message whose document type is no longer the payload's stays FAILED with the reason.
func cxmlReprocessPlan(raw string, failed []store.Message) ([]store.Message, error) {
	env, err := domain.ParseCXML([]byte(raw))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errStoredPayload, err)
	}
	out := make([]store.Message, 0, len(failed))
	for _, m := range failed {
		next := store.Message{ID: m.ID, TransactionSet: m.TransactionSet, STControl: m.STControl, Status: "FAILED"}
		if m.TransactionSet != env.DocType {
			next.Error = fmt.Sprintf("document %s is not in the stored payload (it holds a %s)", m.TransactionSet, env.DocType)
			out = append(out, next)
			continue
		}
		canonical, mapErr := domain.MapCXMLDocument([]byte(raw), env)
		if mapErr != nil {
			next.Error = CodeSyntax + ": " + mapErr.Error()
		} else if next.Canonical, err = json.Marshal(canonical); err != nil {
			return nil, err
		} else {
			next.Status = "MAPPED"
		}
		out = append(out, next)
	}
	return out, nil
}

// edifactReprocessPlan is reprocessPlan for EDIFACT: a message is found by
// message type and UNH reference; a message whose UNT was wrong stays FAILED
// (the stored text cannot change), as does a type and reference that occurs
// in more than one group.
func edifactReprocessPlan(raw string, failed []store.Message) ([]store.Message, error) {
	ic, broken, err := domain.ReadEdifact(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errStoredPayload, err)
	}
	bad := map[string]string{}
	for _, e := range broken {
		bad[edifactKey(e.Group, e.Message)] = e.Detail
	}
	byKey := map[string][]domain.EdifactMessage{}
	for _, m := range ic.Messages {
		byKey[m.Type+"/"+m.Ref] = append(byKey[m.Type+"/"+m.Ref], m)
	}
	out := make([]store.Message, 0, len(failed))
	for _, m := range failed {
		next := store.Message{ID: m.ID, TransactionSet: m.TransactionSet, STControl: m.STControl, Status: "FAILED"}
		switch ms := byKey[m.TransactionSet+"/"+m.STControl]; {
		case len(ms) == 0:
			next.Error = fmt.Sprintf("message %s %s is not in the stored interchange", m.TransactionSet, m.STControl)
		case len(ms) > 1:
			next.Error = fmt.Sprintf("message %s %s occurs in more than one group; reprocess cannot tell them apart", m.TransactionSet, m.STControl)
		case bad[edifactKey(ms[0].Group, ms[0].Ref)] != "":
			next.Error = CodeSyntax + ": " + bad[edifactKey(ms[0].Group, ms[0].Ref)]
		default:
			res, _, err := mapEdifactMessage(ms[0])
			if err != nil {
				return nil, err
			}
			next.Status, next.Canonical, next.Error = res.status, res.canonical, res.errText
		}
		out = append(out, next)
	}
	return out, nil
}

// reprocessInterchange re-runs mapping for the failed messages of one inbound
// interchange (POST /interchanges/{id}:reprocess). With dryRun=true it returns
// the outcome without writing anything and needs no Idempotency-Key.
func (a *API) reprocessInterchange(w http.ResponseWriter, r *http.Request) {
	dryRun, key, ok := reprocessParams(w, r)
	if !ok {
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

// reprocessParams reads the dryRun query parameter and, unless it is a dry
// run, the required Idempotency-Key. It writes the problem and returns false
// when either is invalid.
func reprocessParams(w http.ResponseWriter, r *http.Request) (dryRun bool, key string, ok bool) {
	switch r.URL.Query().Get("dryRun") {
	case "", "false":
	case "true":
		dryRun = true
	default:
		httpx.Fail(w, r, validation(map[string]string{"dryRun": "must be true or false"}))
		return false, "", false
	}
	key = strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if !dryRun && (key == "" || len(key) > 255) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, CodeIdempotencyKey, "Idempotency-Key header is required (max 255 characters)"))
		return false, "", false
	}
	return dryRun, key, true
}

type bulkReprocessRequest struct {
	InterchangeIDs []string `json:"interchangeIds"`
}

// validateBulkIDs checks the id list before any work starts: 1 to 200 ids,
// each a UUID, none repeated.
func validateBulkIDs(list []string) map[string]string {
	if len(list) == 0 || len(list) > store.MaxBulkReprocess {
		return map[string]string{"interchangeIds": fmt.Sprintf("must hold 1 to %d interchange ids", store.MaxBulkReprocess)}
	}
	fields := map[string]string{}
	seen := make(map[string]bool, len(list))
	for i, id := range list {
		key := fmt.Sprintf("interchangeIds[%d]", i)
		switch {
		case !validID(id):
			fields[key] = "must be a UUID"
		case seen[id]:
			fields[key] = "is repeated"
		}
		seen[id] = true
	}
	return fields
}

// bulkReprocessHash fingerprints a bulk request: the same ids in the same
// order. The dry-run flag is not part of it because a dry run claims no key.
func bulkReprocessHash(list []string) string {
	sum := sha256.Sum256([]byte("bulk-reprocess\x00" + strings.Join(list, ",")))
	return hex.EncodeToString(sum[:])
}

// bulkReprocess re-runs mapping for the failed messages of up to 200 inbound
// interchanges (POST /interchanges:reprocess, PRD §6.35.6 "bulk reprocess (≤
// 200) with a dry run"). The whole request is one transaction under one
// Idempotency-Key; an unknown, outbound or unreadable interchange is reported
// in its item and does not stop the others.
func (a *API) bulkReprocess(w http.ResponseWriter, r *http.Request) {
	dryRun, key, ok := reprocessParams(w, r)
	if !ok {
		return
	}
	var req bulkReprocessRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if fields := validateBulkIDs(req.InterchangeIDs); len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	res, _, err := a.store.ReprocessInterchanges(r.Context(), key, bulkReprocessHash(req.InterchangeIDs), req.InterchangeIDs, dryRun, reprocessPlan)
	switch {
	case errors.Is(err, store.ErrIdempotencyReuse):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request"))
	case err != nil:
		httpx.Fail(w, r, err)
	default:
		httpx.JSON(w, http.StatusOK, res)
	}
}

// piiView reports whether the caller may see personal data unmasked
// (edi.pii.view, BR-35.12). Without a principal the data stays masked.
func piiView(r *http.Request) bool {
	p, ok := tenant.FromContext(r.Context())
	return ok && p.HasDuty(dutyPIIView)
}

// dutyPIIView unmasks party names, addresses and contacts (PRD §6.35.17).
const dutyPIIView = "edi.pii.view"

type payloadView struct {
	InterchangeID string      `json:"interchangeId"`
	Direction     string      `json:"direction"`
	PIIMasked     bool        `json:"piiMasked"`
	Tree          domain.Node `json:"tree"`
}

// getPayload serves the raw payload of one interchange as the segment tree of
// the monitor's raw viewer, with X12 dictionary names for hover (§6.35.6).
// Party names, addresses and contacts are masked unless the caller holds
// edi.pii.view (BR-35.12). The raw text itself is never returned.
func (a *API) getPayload(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !validID(id) {
		httpx.Fail(w, r, notFound("interchange"))
		return
	}
	p, err := a.store.GetPayload(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(w, r, notFound("interchange"))
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if domain.DetectSyntax([]byte(p.Raw)) == domain.SyntaxEdifact {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, CodeUnsupportedSet, "the segment tree viewer reads X12 only; EDIFACT payloads are not shown yet"))
		return
	}
	ic, err := domain.Parse(p.Raw)
	if err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, CodeSyntax, fmt.Sprintf("%v: %v", errStoredPayload, err)))
		return
	}
	masked := !piiView(r)
	httpx.JSON(w, http.StatusOK, payloadView{InterchangeID: p.InterchangeID, Direction: p.Direction, PIIMasked: masked, Tree: domain.Tree(ic, masked)})
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
