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
}

func (s *intakeStore) CreateOrder(context.Context, store.NewOrder) (store.Order, bool, error) {
	return store.Order{Lines: []store.Line{}, Holds: []store.Hold{}}, true, s.createErr
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
