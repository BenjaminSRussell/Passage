package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

type intakeStore struct {
	store.Store
	recorded []store.NewIntakeError
	listed   store.IntakeErrorFilter
	resolved struct {
		id, note, actor string
		version         int
	}
	createErr error
	recordErr error

	// replay fakes: rows is what the store holds as replayable, the rest record
	// what the handler did with them.
	rows         []store.IntakeReplay
	skipped      int
	listedReplay store.IntakeErrorFilter
	created      []store.NewOrder
	failed       []string
	resolvedBy   map[string]string
}

func (s *intakeStore) GetIntakeReplay(_ context.Context, id string) (store.IntakeReplay, error) {
	for _, r := range s.rows {
		if r.ID == id {
			return r, nil
		}
	}
	return store.IntakeReplay{}, httpx.NewProblem(http.StatusNotFound, store.CodeIntakeErrorNotFound, "intake error not found")
}

func (s *intakeStore) ListIntakeReplays(_ context.Context, f store.IntakeErrorFilter) ([]store.IntakeReplay, int, error) {
	s.listedReplay = f
	return s.rows, s.skipped, nil
}

func (s *intakeStore) RecordReplayFailure(_ context.Context, id, code, message string, _ time.Time) (store.IntakeError, error) {
	s.failed = append(s.failed, id+" "+code+" "+message)
	return store.IntakeError{ID: id, Status: store.IntakeErrorOpen, Attempts: 2, Version: 3}, nil
}

func (s *intakeStore) ResolveReplayed(_ context.Context, id, orderID, orderNumber, actor string, _ time.Time) (store.IntakeError, error) {
	if s.resolvedBy == nil {
		s.resolvedBy = map[string]string{}
	}
	s.resolvedBy[id] = orderID + " " + orderNumber + " " + actor
	return store.IntakeError{ID: id, Status: store.IntakeErrorResolvedStatus, Version: 4}, nil
}

func (s *intakeStore) CreateOrder(_ context.Context, in store.NewOrder) (store.Order, bool, error) {
	s.created = append(s.created, in)
	return store.Order{ID: "11111111-1111-4111-8111-111111111111", OrderNumber: "SO-1", Lines: []store.Line{}, Holds: []store.Hold{}}, true, s.createErr
}

func (s *intakeStore) RecordIntakeError(_ context.Context, in store.NewIntakeError, _ time.Time) (store.IntakeError, error) {
	s.recorded = append(s.recorded, in)
	return store.IntakeError{}, s.recordErr
}

func (s *intakeStore) ListIntakeErrors(_ context.Context, f store.IntakeErrorFilter) ([]store.IntakeError, error) {
	s.listed = f
	return []store.IntakeError{}, nil
}

func (s *intakeStore) ResolveIntakeError(_ context.Context, id string, version int, note, actor string, _ time.Time) (store.IntakeError, error) {
	s.resolved.id, s.resolved.version, s.resolved.note, s.resolved.actor = id, version, note, actor
	return store.IntakeError{ID: id, Status: store.IntakeErrorResolvedStatus, Version: version + 1}, nil
}

func intakeBody(channel, ref string) string {
	body := `{"legalEntityId":"` + testLE + `","customerId":"` + ids.New() + `","invoiceAccountId":"` + ids.New() + `","currency":"USD",
		"requestedShipDate":"2027-03-04","channel":"` + channel + `","lines":[{"productId":"` + ids.New() + `","quantity":"2","unit":"EA",
		"siteId":"` + ids.New() + `","warehouseId":"` + ids.New() + `","unitPrice":"10","requestedShipDate":"2027-03-04"}]`
	if ref != "" {
		body += `,"externalRef":"` + ref + `"`
	}
	return body + "}"
}

func TestCreateOrder_RefusedChannelOrderIsLogged(t *testing.T) {
	h := map[string]string{idempotencyKey: "k1"}
	s := &intakeStore{createErr: httpx.NewProblem(http.StatusConflict, store.CodeDuplicateIntake, "payload differs")}
	rec := serveBody(t, s, http.MethodPost, orders, intakeBody("EDI", "PO-1"), principal(dutyIntake), h)
	if rec.Code != http.StatusConflict || len(s.recorded) != 1 {
		t.Fatalf("a refused EDI order is logged: %d %+v", rec.Code, s.recorded)
	}
	got := s.recorded[0]
	if got.LegalEntityID != testLE || got.Channel != "EDI" || got.ExternalRef != "PO-1" || got.ErrorCode != store.CodeDuplicateIntake || got.RequestHash == "" || got.Message != "payload differs" {
		t.Fatalf("logged: %+v", got)
	}

	// A validation failure is logged with its field errors, without a reference.
	s = &intakeStore{}
	bad := strings.Replace(intakeBody("PORTAL", ""), `"currency":"USD"`, `"currency":"usd"`, 1)
	if rec := serveBody(t, s, http.MethodPost, orders, bad, principal(dutyIntake), h); rec.Code != http.StatusBadRequest || len(s.recorded) != 1 ||
		s.recorded[0].ErrorCode != "COM-0001" || !strings.Contains(s.recorded[0].Message, "currency") || s.recorded[0].ExternalRef != "" {
		t.Fatalf("validation failure: %d %+v", rec.Code, s.recorded)
	}

	// UI orders, server faults and a legal entity outside the scope are not logged.
	s = &intakeStore{createErr: httpx.NewProblem(http.StatusConflict, store.CodeDuplicateIntake, "x")}
	serveBody(t, s, http.MethodPost, orders, intakeBody("UI", ""), principal(dutyWrite), h)
	serveBody(t, s, http.MethodPost, orders, strings.Replace(intakeBody("EDI", ""), testLE, ids.New(), 1), principal(dutyIntake), h)
	s.createErr = httpx.NewProblem(http.StatusInternalServerError, "COM-0500", "boom")
	serveBody(t, s, http.MethodPost, orders, intakeBody("EDI", ""), principal(dutyIntake), h)
	if len(s.recorded) != 0 {
		t.Fatalf("nothing to log: %+v", s.recorded)
	}
	// The 409 of a concurrent request that stored the same order first is a
	// retry away from the order, not a refusal: answered, not logged.
	s = &intakeStore{createErr: store.ConcurrentIntake{Problem: httpx.NewProblem(http.StatusConflict, store.CodeDuplicateIntake, "a concurrent request stored this order first")}}
	if rec := serveBody(t, s, http.MethodPost, orders, intakeBody("EDI", ""), principal(dutyIntake), h); rec.Code != http.StatusConflict ||
		!strings.Contains(rec.Body.String(), store.CodeDuplicateIntake) || len(s.recorded) != 0 {
		t.Fatalf("concurrent intake: %d %s %+v", rec.Code, rec.Body, s.recorded)
	}

	// A log write that fails leaves the refusal as it was.
	s = &intakeStore{createErr: httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeProductNotListed, "not listed"), recordErr: errors.New("db down")}
	if rec := serveBody(t, s, http.MethodPost, orders, intakeBody("EDI", "PO-3"), principal(dutyIntake), h); rec.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(rec.Body.String(), store.CodeProductNotListed) || len(s.recorded) != 1 {
		t.Fatalf("a failed log write: %d %s", rec.Code, rec.Body)
	}

	// A reference refused for its length is logged cut to the longest accepted.
	s = &intakeStore{}
	long := strings.Repeat("R", 5000)
	if rec := serveBody(t, s, http.MethodPost, orders, intakeBody("EDI", long), principal(dutyIntake), h); rec.Code != http.StatusBadRequest ||
		len(s.recorded) != 1 || s.recorded[0].ExternalRef != long[:maxExternalRef] {
		t.Fatalf("an over-long reference: %d %+v", rec.Code, len(s.recorded))
	}

	// A success is not logged.
	s = &intakeStore{}
	if rec := serveBody(t, s, http.MethodPost, orders, intakeBody("EDI", "PO-2"), principal(dutyIntake), h); rec.Code != http.StatusCreated || len(s.recorded) != 0 {
		t.Fatalf("success: %d %+v", rec.Code, s.recorded)
	}
}

func TestIntakeErrors_ListAndResolve(t *testing.T) {
	s := &intakeStore{}
	rec := serve(t, s, http.MethodGet, intakeErrors+"?channel=EDI&status=OPEN&errorCode=SLS-0007&limit=10", principal(dutyRead), nil)
	if rec.Code != http.StatusOK || s.listed.Channel != "EDI" || s.listed.Status != "OPEN" || s.listed.ErrorCode != "SLS-0007" || s.listed.Limit != 10 || s.listed.LegalEntityID != testLE {
		t.Fatalf("list: %d %+v", rec.Code, s.listed)
	}
	for _, q := range []string{"?status=DONE", "?channel=a%20b", "?limit=0"} {
		if rec := serve(t, s, http.MethodGet, intakeErrors+q, principal(dutyRead), nil); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d", q, rec.Code)
		}
	}
	if rec := serve(t, s, http.MethodGet, intakeErrors, principal(), nil); rec.Code != http.StatusForbidden {
		t.Fatalf("list without the duty: %d", rec.Code)
	}

	id := ids.New()
	path := intakeErrors + "/" + id + ":resolve"
	h := map[string]string{"If-Match": `"3"`}
	if rec := serveBody(t, s, http.MethodPost, path, `{"note":"ok"}`, principal(dutyRead), h); rec.Code != http.StatusForbidden {
		t.Fatalf("resolve without the duty: %d", rec.Code)
	}
	if rec := serveBody(t, s, http.MethodPost, path, `{"note":"ok"}`, principal(dutyWrite), nil); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("resolve without If-Match: %d", rec.Code)
	}
	for _, body := range []string{`{}`, `{"note":"  "}`, `{"note":"` + strings.Repeat("x", 501) + `"}`} {
		if rec := serveBody(t, s, http.MethodPost, path, body, principal(dutyWrite), h); rec.Code != http.StatusBadRequest {
			t.Fatalf("note %.10q: %d", body, rec.Code)
		}
	}
	rec = serveBody(t, s, http.MethodPost, path, `{"note":" mapping fixed "}`, principal(dutyWrite), h)
	if rec.Code != http.StatusOK || s.resolved.id != id || s.resolved.version != 3 || s.resolved.note != "mapping fixed" || s.resolved.actor != "u1" || rec.Header().Get("ETag") != `"4"` {
		t.Fatalf("resolve: %d %+v", rec.Code, s.resolved)
	}
	if rec := serveBody(t, s, http.MethodPost, intakeErrors+"/nope:resolve", `{"note":"x"}`, principal(dutyWrite), h); rec.Code != http.StatusNotFound {
		t.Fatalf("bad id: %d", rec.Code)
	}
}

func replayRow(t *testing.T, body, key string) store.IntakeReplay {
	t.Helper()
	return store.IntakeReplay{IntakeError: store.IntakeError{ID: ids.New(), Status: store.IntakeErrorOpen, Attempts: 1, Version: 2}, Payload: []byte(body), IdempotencyKey: key}
}

func TestCreateOrder_RefusedChannelOrderKeepsItsBodyAndKey(t *testing.T) {
	s := &intakeStore{createErr: httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeProductNotListed, "not listed")}
	serveBody(t, s, http.MethodPost, orders, intakeBody("EDI", "PO-9"), principal(dutyIntake), map[string]string{idempotencyKey: "k-9"})
	if len(s.recorded) != 1 || s.recorded[0].IdempotencyKey != "k-9" || !strings.Contains(string(s.recorded[0].Payload), `"externalRef":"PO-9"`) {
		t.Fatalf("the refused body and key are kept for a replay: %+v", s.recorded)
	}
	if s.recorded[0].PersonalData {
		t.Fatal("an ordinary order carries no one-time buyer")
	}
	// A one-time buyer's body is flagged so the store keeps it only sealed.
	s = &intakeStore{createErr: httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeProductNotListed, "not listed")}
	oneTime := strings.TrimSuffix(intakeBody("EDI", "PO-10"), "}") +
		`,"oneTime":{"name":"Ada Buyer","line1":"1 Main St","city":"Springfield","postalCode":"12345","country":"US"}}`
	serveBody(t, s, http.MethodPost, orders, oneTime, principal(dutyIntake), map[string]string{idempotencyKey: "k-10"})
	if len(s.recorded) != 1 || !s.recorded[0].PersonalData {
		t.Fatalf("a one-time buyer is personal data: %+v", s.recorded)
	}
}

// TestIntakeErrors_ReplayRefusalsThatAreNotTheOrders: a concurrent replay or
// retry storing the same order (409 SLS-0007) and a caller the order is out of
// scope for (403) are answered as errors and are not counted as attempts, so
// they cannot push a row toward the dead letters.
func TestIntakeErrors_ReplayRefusalsThatAreNotTheOrders(t *testing.T) {
	row := replayRow(t, intakeBody("EDI", "PO-11"), "k-11")
	path := intakeErrors + "/" + row.ID + ":replay"
	s := &intakeStore{rows: []store.IntakeReplay{row},
		createErr: store.ConcurrentIntake{Problem: httpx.NewProblem(http.StatusConflict, store.CodeDuplicateIntake, "a concurrent request stored this order first")}}
	if rec := serveBody(t, s, http.MethodPost, path, "", principal(dutyWrite), nil); rec.Code != http.StatusConflict || len(s.failed) != 0 {
		t.Fatalf("concurrent replay: %d %+v", rec.Code, s.failed)
	}
	rec := serveBody(t, s, http.MethodPost, intakeErrors+":replay", "", principal(dutyWrite), nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"attempted":0`) || len(s.failed) != 0 {
		t.Fatalf("concurrent replay in a batch is skipped: %d %s", rec.Code, rec.Body)
	}

	other := tenant.Principal{TenantID: testkit.TenantID(1), UserID: "u2", LegalEntityID: ids.New(), Duties: map[string]struct{}{dutyWrite: {}}}
	s = &intakeStore{rows: []store.IntakeReplay{row}}
	if rec := serveBody(t, s, http.MethodPost, path, "", other, nil); rec.Code != http.StatusForbidden || len(s.failed) != 0 || len(s.created) != 0 {
		t.Fatalf("out of scope: %d %+v %+v", rec.Code, s.failed, s.created)
	}
}

func TestIntakeErrors_ReplayOne(t *testing.T) {
	row := replayRow(t, intakeBody("EDI", "PO-5"), "k-5")
	s := &intakeStore{rows: []store.IntakeReplay{row}}
	path := intakeErrors + "/" + row.ID + ":replay"
	if rec := serveBody(t, s, http.MethodPost, path, "", principal(dutyRead), nil); rec.Code != http.StatusForbidden {
		t.Fatalf("replay without the write duty: %d", rec.Code)
	}
	rec := serveBody(t, s, http.MethodPost, path, "", principal(dutyWrite), nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"replayed":true`) || !strings.Contains(rec.Body.String(), `"orderNumber":"SO-1"`) {
		t.Fatalf("replay: %d %s", rec.Code, rec.Body)
	}
	if len(s.created) != 1 || s.created[0].IdempotencyKey == nil || *s.created[0].IdempotencyKey != "k-5" || s.created[0].Channel != "EDI" {
		t.Fatalf("the order is created under the kept key: %+v", s.created)
	}
	if got := s.resolvedBy[row.ID]; got != "11111111-1111-4111-8111-111111111111 SO-1 u1" {
		t.Fatalf("the intake error is closed with the order: %q", got)
	}

	// A row that never had a key replays under a key derived from its id.
	noKey := replayRow(t, intakeBody("EDI", "PO-6"), "")
	s = &intakeStore{rows: []store.IntakeReplay{noKey}}
	serveBody(t, s, http.MethodPost, intakeErrors+"/"+noKey.ID+":replay", "", principal(dutyWrite), nil)
	if len(s.created) != 1 || *s.created[0].IdempotencyKey != "replay:"+noKey.ID {
		t.Fatalf("derived key: %+v", s.created)
	}

	// Refused again: the row stays open with one more attempt, the answer is 200
	// with the new refusal, and no order is closed over.
	s = &intakeStore{rows: []store.IntakeReplay{row}, createErr: httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeProductNotListed, "not listed")}
	rec = serveBody(t, s, http.MethodPost, path, "", principal(dutyWrite), nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"replayed":false`) || !strings.Contains(rec.Body.String(), store.CodeProductNotListed) ||
		len(s.failed) != 1 || len(s.resolvedBy) != 0 {
		t.Fatalf("refused again: %d %s %+v", rec.Code, rec.Body, s.failed)
	}

	// A fault is an error, not a counted attempt.
	s = &intakeStore{rows: []store.IntakeReplay{row}, createErr: errors.New("db down")}
	if rec := serveBody(t, s, http.MethodPost, path, "", principal(dutyWrite), nil); rec.Code != http.StatusInternalServerError || len(s.failed) != 0 {
		t.Fatalf("fault: %d", rec.Code)
	}

	// A body that no longer validates is refused the way intake refuses it.
	bad := replayRow(t, strings.Replace(intakeBody("EDI", "PO-7"), `"currency":"USD"`, `"currency":"usd"`, 1), "k-7")
	s = &intakeStore{rows: []store.IntakeReplay{bad}}
	rec = serveBody(t, s, http.MethodPost, intakeErrors+"/"+bad.ID+":replay", "", principal(dutyWrite), nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"COM-0001"`) || len(s.created) != 0 || len(s.failed) != 1 {
		t.Fatalf("invalid body: %d %s", rec.Code, rec.Body)
	}

	if rec := serveBody(t, s, http.MethodPost, intakeErrors+"/"+ids.New()+":replay", "", principal(dutyWrite), nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id: %d", rec.Code)
	}
	if rec := serveBody(t, s, http.MethodPost, intakeErrors+"/nope:replay", "", principal(dutyWrite), nil); rec.Code != http.StatusNotFound {
		t.Fatalf("bad id: %d", rec.Code)
	}
}

func TestIntakeErrors_ReplayAll(t *testing.T) {
	good := replayRow(t, intakeBody("EDI", "PO-1"), "k1")
	bad := replayRow(t, strings.Replace(intakeBody("EDI", "PO-2"), `"currency":"USD"`, `"currency":"usd"`, 1), "k2")
	s := &intakeStore{rows: []store.IntakeReplay{good, bad}, skipped: 3}
	rec := serveBody(t, s, http.MethodPost, intakeErrors+":replay?channel=EDI&errorCode=SLS-0011&limit=20", "", principal(dutyWrite), nil)
	if rec.Code != http.StatusOK || s.listedReplay.Channel != "EDI" || s.listedReplay.ErrorCode != "SLS-0011" || s.listedReplay.Limit != 20 || s.listedReplay.LegalEntityID != testLE {
		t.Fatalf("bulk replay: %d %+v", rec.Code, s.listedReplay)
	}
	for _, want := range []string{`"attempted":2`, `"replayed":1`, `"failed":1`, `"skipped":3`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("%s missing from %s", want, rec.Body)
		}
	}
	if len(s.created) != 1 || len(s.failed) != 1 {
		t.Fatalf("one stored, one refused again: %+v %+v", s.created, s.failed)
	}
	serveBody(t, s, http.MethodPost, intakeErrors+":replay", "", principal(dutyWrite), nil)
	if s.listedReplay.Limit != defaultReplayBatch {
		t.Fatalf("default batch: %d", s.listedReplay.Limit)
	}
	for _, q := range []string{"?limit=0", "?limit=101", "?channel=a%20b"} {
		if rec := serveBody(t, s, http.MethodPost, intakeErrors+":replay"+q, "", principal(dutyWrite), nil); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d", q, rec.Code)
		}
	}
	if rec := serveBody(t, s, http.MethodPost, intakeErrors+":replay", "", principal(dutyRead), nil); rec.Code != http.StatusForbidden {
		t.Fatalf("without the write duty: %d", rec.Code)
	}
}
