package store_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
	"github.com/BenjaminSRussell/passage/services/party/internal/store"
)

const (
	requester = "user-requester"
	approver  = "user-approver"
)

func asUser(tenantNumber int, legalEntityID, user string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(tenantNumber), LegalEntityID: legalEntityID, UserID: user, Duties: testkit.ScopeDuties(legalEntityID)})
}

func customerWithLimit(t *testing.T, app *pgxpool.Pool, limit string) string {
	t.Helper()
	ctx := principalContext(1, "")
	partyID, err := insertParty(ctx, app, testkit.TenantID(1), "P-1")
	if err != nil {
		t.Fatal(err)
	}
	customerID := ids.New()
	err = dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
		profileID, termID := ids.New(), ids.New()
		if _, err := tx.Exec(ctx, `insert into posting_profile (tenant_id, id, legal_entity_id, code, name) values ($1, $2, $3, 'PP', 'Default')`, testkit.TenantID(1), profileID, legalEntityA); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into payment_term (tenant_id, id, code, name, net_days) values ($1, $2, 'T', 'Net 30', 30)`, testkit.TenantID(1), termID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `insert into customer_role (tenant_id, id, party_id, legal_entity_id, account_number, customer_group_id, payment_term_id, posting_profile_id, currency_code, credit_limit)
			values ($1, $2, $3, $4, 'CUST-1', $5, $6, $7, 'USD', $8::text::numeric)`, testkit.TenantID(1), customerID, partyID, legalEntityA, ids.New(), termID, profileID, limit)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return customerID
}

func storedLimit(t *testing.T, app *pgxpool.Pool, customerID string) string {
	t.Helper()
	ctx := principalContext(1, "")
	var limit string
	err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select credit_limit::text from customer_role where id = $1`, customerID).Scan(&limit)
	})
	if err != nil {
		t.Fatal(err)
	}
	return limit
}

func outboxTypes(t *testing.T, app *pgxpool.Pool) string {
	t.Helper()
	var types []string
	for _, row := range outboxRows(t, principalContext(1, ""), app) {
		types = append(types, row.typ)
	}
	return strings.Join(types, ",")
}

func problemCode(t *testing.T, err error) string {
	t.Helper()
	var p *httpx.Problem
	if !errors.As(err, &p) {
		t.Fatalf("want a problem, got %v", err)
	}
	return p.Code
}

func TestCreditLimit_IncreaseStaysPendingUntilApproved(t *testing.T) {
	app := migratedApp(t)
	s := store.New(app)
	customerID := customerWithLimit(t, app, "50000.00")

	res, err := s.RequestCreditLimit(asUser(1, "", requester), customerID, "120000.00")
	if err != nil {
		t.Fatal(err)
	}
	if res.Pending == nil || res.Pending.Status != domain.ChangePending || *res.CreditLimit != "50000.00" {
		t.Fatalf("got %+v", res)
	}
	if got := storedLimit(t, app, customerID); got != "50000.00" {
		t.Fatalf("limit changed before approval: %s", got)
	}
	if got := outboxTypes(t, app); got != "dev.passage.party.change.requested.v1" {
		t.Fatalf("outbox after request = %s", got)
	}

	if _, err := s.DecidePendingChange(asUser(1, "", requester), res.Pending.ID, true, ""); problemCode(t, err) != store.CodeSelfApproval {
		t.Fatalf("self approval: %v", err)
	}
	if got := storedLimit(t, app, customerID); got != "50000.00" {
		t.Fatalf("limit changed by self approval: %s", got)
	}

	decided, err := s.DecidePendingChange(asUser(1, "", approver), res.Pending.ID, true, "")
	if err != nil || decided.Status != domain.ChangeApproved || *decided.DecidedBy != approver {
		t.Fatalf("got %+v, %v", decided, err)
	}
	if got := storedLimit(t, app, customerID); got != "120000.00" {
		t.Fatalf("limit after approval = %s", got)
	}
	if got := outboxTypes(t, app); got != "dev.passage.party.change.requested.v1,dev.passage.party.change.approved.v1" {
		t.Fatalf("outbox after approval = %s", got)
	}
	if _, err := s.DecidePendingChange(asUser(1, "", approver), res.Pending.ID, false, ""); problemCode(t, err) != store.CodeChangeDecided {
		t.Fatalf("second decision: %v", err)
	}
}

func TestCreditLimit_RejectDiscardsChange(t *testing.T) {
	app := migratedApp(t)
	s := store.New(app)
	customerID := customerWithLimit(t, app, "50000.00")
	res, err := s.RequestCreditLimit(asUser(1, "", requester), customerID, "120000")
	if err != nil {
		t.Fatal(err)
	}
	decided, err := s.DecidePendingChange(asUser(1, "", approver), res.Pending.ID, false, "too risky")
	if err != nil || decided.Status != domain.ChangeRejected || decided.Reason == nil || *decided.Reason != "too risky" {
		t.Fatalf("got %+v, %v", decided, err)
	}
	if got := storedLimit(t, app, customerID); got != "50000.00" {
		t.Fatalf("limit after reject = %s", got)
	}
	if got := outboxTypes(t, app); got != "dev.passage.party.change.requested.v1,dev.passage.party.change.rejected.v1" {
		t.Fatalf("outbox = %s", got)
	}
	if _, err := s.RequestCreditLimit(asUser(1, "", requester), customerID, "130000"); err != nil {
		t.Fatalf("a new request after rejection: %v", err)
	}
}

func TestCreditLimit_DecreaseAppliesImmediately(t *testing.T) {
	app := migratedApp(t)
	s := store.New(app)
	customerID := customerWithLimit(t, app, "50000.00")
	res, err := s.RequestCreditLimit(asUser(1, "", requester), customerID, "10000.50")
	if err != nil || res.Pending != nil || *res.CreditLimit != "10000.50" {
		t.Fatalf("got %+v, %v", res, err)
	}
	if got := storedLimit(t, app, customerID); got != "10000.50" {
		t.Fatalf("limit = %s", got)
	}
	if got := outboxTypes(t, app); got != "dev.passage.party.customer.updated.v1" {
		t.Fatalf("outbox = %s", got)
	}
	if _, err := s.RequestCreditLimit(asUser(1, "", requester), customerID, "10000.50"); err != nil || outboxTypes(t, app) != "dev.passage.party.customer.updated.v1" {
		t.Fatalf("an unchanged limit must be a no-op: %v", err)
	}
}

func TestCreditLimit_OnePendingIncreasePerCustomer(t *testing.T) {
	app := migratedApp(t)
	s := store.New(app)
	customerID := customerWithLimit(t, app, "50000.00")
	res, err := s.RequestCreditLimit(asUser(1, "", requester), customerID, "60000")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.RequestCreditLimit(asUser(1, "", requester), customerID, "70000")
	if problemCode(t, err) != store.CodeChangePending {
		t.Fatalf("got %v", err)
	}
	var problem *httpx.Problem
	if !errors.As(err, &problem) || problem.Errors["pendingChangeId"] != res.Pending.ID {
		t.Fatalf("pending change id was not returned: %#v", problem)
	}
	if got := count(principalContext(1, ""), t, app, "pending_change"); got != 1 {
		t.Fatalf("pending rows = %d", got)
	}
}

func TestCreditLimit_RejectsBadAmountsAndUnknownIDs(t *testing.T) {
	app := migratedApp(t)
	s := store.New(app)
	customerID := customerWithLimit(t, app, "50000.00")
	for _, bad := range []string{"", "-5", "abc", "1.234", "1e6"} {
		_, err := s.RequestCreditLimit(asUser(1, "", requester), customerID, bad)
		var p *httpx.Problem
		if !errors.As(err, &p) || p.Status != http.StatusUnprocessableEntity || p.Code != store.CodeCreditInvalid {
			t.Fatalf("%q: got %v", bad, err)
		}
	}
	if _, err := s.RequestCreditLimit(asUser(1, "", requester), ids.New(), "1"); problemCode(t, err) != store.CodeCustomerNotFound {
		t.Fatalf("unknown customer: %v", err)
	}
	if _, err := s.DecidePendingChange(asUser(1, "", approver), "not-a-uuid", true, ""); problemCode(t, err) != store.CodeChangeNotFound {
		t.Fatalf("malformed change id: %v", err)
	}
}

func TestCreditLimit_IsolatedByTenantAndLegalEntity(t *testing.T) {
	app := migratedApp(t)
	s := store.New(app)
	customerID := customerWithLimit(t, app, "50000.00")
	if _, err := s.RequestCreditLimit(asUser(2, "", requester), customerID, "60000"); problemCode(t, err) != store.CodeCustomerNotFound {
		t.Fatalf("other tenant: %v", err)
	}
	if _, err := s.RequestCreditLimit(asUser(1, legalEntityB, requester), customerID, "60000"); problemCode(t, err) != store.CodeCustomerNotFound {
		t.Fatalf("other legal entity: %v", err)
	}
	res, err := s.RequestCreditLimit(asUser(1, legalEntityA, requester), customerID, "60000")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DecidePendingChange(asUser(2, "", approver), res.Pending.ID, true, ""); problemCode(t, err) != store.CodeChangeNotFound {
		t.Fatalf("other tenant deciding: %v", err)
	}
	if _, err := s.DecidePendingChange(asUser(1, legalEntityB, approver), res.Pending.ID, true, ""); problemCode(t, err) != store.CodeChangeNotFound {
		t.Fatalf("other legal entity deciding: %v", err)
	}
	if got := storedLimit(t, app, customerID); got != "50000.00" {
		t.Fatalf("limit = %s", got)
	}
}

func TestCreditLimit_BlankUserIsRejected(t *testing.T) {
	app := migratedApp(t)
	s := store.New(app)
	customerID := customerWithLimit(t, app, "50000.00")
	for _, user := range []string{"", "  "} {
		if _, err := s.RequestCreditLimit(asUser(1, "", user), customerID, "60000"); problemCode(t, err) != "AUTH-0001" {
			t.Fatalf("blank user request %q: %v", user, err)
		}
	}
	res, err := s.RequestCreditLimit(asUser(1, "", requester), customerID, "60000")
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{"", "  "} {
		if _, err := s.DecidePendingChange(asUser(1, "", user), res.Pending.ID, true, ""); problemCode(t, err) != "AUTH-0001" {
			t.Fatalf("blank user decision %q: %v", user, err)
		}
	}
	if got := storedLimit(t, app, customerID); got != "50000.00" {
		t.Fatalf("limit = %s", got)
	}
}

func TestCreditLimit_DatabaseRefusesBlankOrSelfDecider(t *testing.T) {
	app := migratedApp(t)
	customerID := customerWithLimit(t, app, "50000.00")
	ctx := principalContext(1, "")
	insert := func(requestedBy string) error {
		return dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `insert into pending_change (tenant_id, id, legal_entity_id, customer_id, field, requested_value, requested_by)
				values ($1, $2, $3, $4, 'credit_limit', 1, $5)`, testkit.TenantID(1), ids.New(), legalEntityA, customerID, requestedBy)
			return err
		})
	}
	if err := insert(""); sqlState(err) != "23514" {
		t.Fatalf("blank requested_by: %v", err)
	}
	decide := func(decider string) error {
		return dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `insert into pending_change (tenant_id, id, legal_entity_id, customer_id, field, requested_value, requested_by, status, decided_by, decided_at)
				values ($1, $2, $3, $4, 'credit_limit', 1, 'alice', 'approved', $5, now())`, testkit.TenantID(1), ids.New(), legalEntityA, customerID, decider)
			return err
		})
	}
	for _, decider := range []string{"", "alice"} {
		if err := decide(decider); sqlState(err) != "23514" {
			t.Fatalf("decider %q: %v", decider, err)
		}
	}
	err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into pending_change (tenant_id, id, legal_entity_id, customer_id, field, requested_value, requested_by, status, decided_at)
			values ($1, $2, $3, $4, 'credit_limit', 1, 'alice', 'approved', now())`, testkit.TenantID(1), ids.New(), legalEntityA, customerID)
		return err
	})
	if sqlState(err) != "23514" {
		t.Fatalf("decided row without decided_by: %v", err)
	}
	if err := decide("bob"); err != nil {
		t.Fatalf("distinct decider: %v", err)
	}
}

func TestCreditLimit_ApprovalGoesStaleWhenLimitMovesAfterRequest(t *testing.T) {
	app := migratedApp(t)
	s := store.New(app)
	customerID := customerWithLimit(t, app, "50000.00")
	res, err := s.RequestCreditLimit(asUser(1, "", requester), customerID, "120000")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RequestCreditLimit(asUser(1, "", requester), customerID, "10000"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DecidePendingChange(asUser(1, "", approver), res.Pending.ID, true, ""); problemCode(t, err) != store.CodeChangeStale {
		t.Fatalf("stale approve: %v", err)
	}
	if got := storedLimit(t, app, customerID); got != "10000.00" {
		t.Fatalf("limit jumped to %s", got)
	}
	rejected, err := s.DecidePendingChange(asUser(1, "", approver), res.Pending.ID, false, "")
	if err != nil || rejected.Status != domain.ChangeRejected {
		t.Fatalf("reject after stale: %+v, %v", rejected, err)
	}
}

func TestCreditLimit_DecisionReasonLimitCountsCharacters(t *testing.T) {
	app := migratedApp(t)
	s := store.New(app)
	customerID := customerWithLimit(t, app, "50000.00")
	res, err := s.RequestCreditLimit(asUser(1, "", requester), customerID, "60000")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DecidePendingChange(asUser(1, "", approver), res.Pending.ID, false, strings.Repeat("é", 501)); problemCode(t, err) != store.CodeReasonTooLong {
		t.Fatalf("501 Unicode characters: %v", err)
	}
	decided, err := s.DecidePendingChange(asUser(1, "", approver), res.Pending.ID, false, strings.Repeat("é", 500))
	if err != nil || decided.Reason == nil || *decided.Reason != strings.Repeat("é", 500) {
		t.Fatalf("500 Unicode characters: %+v, %v", decided, err)
	}
}

func TestCreditLimit_ListPendingChangesPagesNewestFirstAndIsScoped(t *testing.T) {
	app := migratedApp(t)
	s := store.New(app)
	customerID := customerWithLimit(t, app, "50000.00")
	res, err := s.RequestCreditLimit(asUser(1, legalEntityA, requester), customerID, "60000")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.ListPendingChanges(asUser(1, legalEntityA, approver), 10, time.Time{}, "")
	if err != nil || len(got) != 1 || got[0].ID != res.Pending.ID {
		t.Fatalf("list = %+v, %v", got, err)
	}
	if past, err := s.ListPendingChanges(asUser(1, legalEntityA, approver), 10, got[0].RequestedAt, got[0].ID); err != nil || len(past) != 0 {
		t.Fatalf("after the only change = %+v, %v", past, err)
	}
	if other, err := s.ListPendingChanges(asUser(1, legalEntityB, approver), 10, time.Time{}, ""); err != nil || len(other) != 0 {
		t.Fatalf("other legal entity = %+v, %v", other, err)
	}
	if other, err := s.ListPendingChanges(asUser(2, "", approver), 10, time.Time{}, ""); err != nil || len(other) != 0 {
		t.Fatalf("other tenant = %+v, %v", other, err)
	}
	if _, err := s.DecidePendingChange(asUser(1, legalEntityA, approver), res.Pending.ID, false, ""); err != nil {
		t.Fatal(err)
	}
	if decided, err := s.ListPendingChanges(asUser(1, legalEntityA, approver), 10, time.Time{}, ""); err != nil || len(decided) != 0 {
		t.Fatalf("a decided change is still listed: %+v, %v", decided, err)
	}
}

func TestCreditLimit_GetPendingChangeIsScoped(t *testing.T) {
	app := migratedApp(t)
	s := store.New(app)
	customerID := customerWithLimit(t, app, "50000.00")
	res, err := s.RequestCreditLimit(asUser(1, legalEntityA, requester), customerID, "60000")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetPendingChange(asUser(1, legalEntityA, approver), res.Pending.ID)
	if err != nil || got.ID != res.Pending.ID {
		t.Fatalf("get pending change = %+v, %v", got, err)
	}
	if _, err := s.GetPendingChange(asUser(1, legalEntityB, approver), res.Pending.ID); problemCode(t, err) != store.CodeChangeNotFound {
		t.Fatalf("other legal entity: %v", err)
	}
	if _, err := s.GetPendingChange(asUser(2, "", approver), res.Pending.ID); problemCode(t, err) != store.CodeChangeNotFound {
		t.Fatalf("other tenant: %v", err)
	}
}

func TestCreditLimit_SameDeciderReplayIsIdempotent(t *testing.T) {
	app := migratedApp(t)
	s := store.New(app)
	customerID := customerWithLimit(t, app, "50000.00")
	res, err := s.RequestCreditLimit(asUser(1, "", requester), customerID, "120000")
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.DecidePendingChange(asUser(1, "", approver), res.Pending.ID, true, "ok")
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.DecidePendingChange(asUser(1, "", approver), res.Pending.ID, true, "")
	if err != nil || again.ID != first.ID || again.Status != domain.ChangeApproved || !again.Replayed || first.Replayed {
		t.Fatalf("replay: %+v, %v", again, err)
	}
	if again.Reason == nil || *again.Reason != "ok" {
		t.Fatalf("replay did not preserve the original reason: %+v", again)
	}
	if got := outboxTypes(t, app); got != "dev.passage.party.change.requested.v1,dev.passage.party.change.approved.v1" {
		t.Fatalf("replay emitted an event: %s", got)
	}
	if _, err := s.DecidePendingChange(asUser(1, "", "someone-else"), res.Pending.ID, true, ""); problemCode(t, err) != store.CodeChangeDecided {
		t.Fatalf("other decider: %v", err)
	}
	if _, err := s.DecidePendingChange(asUser(1, "", approver), res.Pending.ID, false, ""); problemCode(t, err) != store.CodeChangeDecided {
		t.Fatalf("other decision: %v", err)
	}
}
