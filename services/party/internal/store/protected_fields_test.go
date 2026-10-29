package store_test

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
	"github.com/BenjaminSRussell/passage/services/party/internal/store"
)

func TestProtectedFields_DefaultsToCreditLimit(t *testing.T) {
	s := store.New(migratedApp(t))
	got, err := s.ProtectedFields(asUser(1, "", approver), legalEntityA)
	if err != nil || !slices.Equal(got, []string{domain.FieldCreditLimit}) {
		t.Fatalf("default = %v, %v", got, err)
	}
}

func TestProtectedFields_UnprotectedIncreaseAppliesImmediately(t *testing.T) {
	app := migratedApp(t)
	s := store.New(app)
	customerID := customerWithLimit(t, app, "50000.00")
	if _, err := s.SetProtectedFields(asUser(1, "", approver), legalEntityA, []string{}); err != nil {
		t.Fatal(err)
	}
	res, err := s.RequestCreditLimit(asUser(1, "", requester), customerID, "120000.00")
	if err != nil || res.Pending != nil || *res.CreditLimit != "120000.00" {
		t.Fatalf("got %+v, %v", res, err)
	}
	if got := storedLimit(t, app, customerID); got != "120000.00" {
		t.Fatalf("limit = %s", got)
	}
	if got := outboxTypes(t, app); got != "dev.passage.party.protectedfields.updated.v1,dev.passage.party.customer.updated.v1" {
		t.Fatalf("outbox = %s", got)
	}
	if _, err := s.SetProtectedFields(asUser(1, "", approver), legalEntityA, []string{domain.FieldCreditLimit}); err != nil {
		t.Fatal(err)
	}
	res, err = s.RequestCreditLimit(asUser(1, "", requester), customerID, "200000.00")
	if err != nil || res.Pending == nil || storedLimit(t, app, customerID) != "120000.00" {
		t.Fatalf("re-protected increase: %+v, %v", res, err)
	}
}

func TestProtectedFields_ConfigIsPerLegalEntity(t *testing.T) {
	app := migratedApp(t)
	s := store.New(app)
	customerID := customerWithLimit(t, app, "50000.00")
	if _, err := s.SetProtectedFields(asUser(1, "", approver), legalEntityB, []string{}); err != nil {
		t.Fatal(err)
	}
	res, err := s.RequestCreditLimit(asUser(1, "", requester), customerID, "60000")
	if err != nil || res.Pending == nil {
		t.Fatalf("customer in legal entity A must stay protected: %+v, %v", res, err)
	}
	got, err := s.ProtectedFields(asUser(1, "", approver), legalEntityB)
	if err != nil || len(got) != 0 {
		t.Fatalf("legal entity B = %v, %v", got, err)
	}
}

func TestProtectedFields_SetValidatesAndNormalizes(t *testing.T) {
	s := store.New(migratedApp(t))
	ctx := asUser(1, "", approver)
	if _, err := s.SetProtectedFields(ctx, legalEntityA, []string{"bank_account"}); problemCode(t, err) != store.CodeFieldsInvalid {
		t.Fatalf("unknown field: %v", err)
	}
	if _, err := s.SetProtectedFields(ctx, "not-a-uuid", nil); problemCode(t, err) != store.CodeFieldsInvalid {
		t.Fatalf("malformed legal entity: %v", err)
	}
	if _, err := s.SetProtectedFields(asUser(1, "", ""), legalEntityA, nil); problemCode(t, err) != "AUTH-0001" {
		t.Fatalf("blank user: %v", err)
	}
	got, err := s.SetProtectedFields(ctx, legalEntityA, []string{domain.FieldCreditLimit, domain.FieldCreditLimit})
	if err != nil || !slices.Equal(got, []string{domain.FieldCreditLimit}) {
		t.Fatalf("duplicates not collapsed: %v, %v", got, err)
	}
}

func TestProtectedFields_IsolatedByTenantAndScope(t *testing.T) {
	app := migratedApp(t)
	s := store.New(app)
	if _, err := s.SetProtectedFields(asUser(1, "", approver), legalEntityA, []string{}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ProtectedFields(asUser(2, "", approver), legalEntityA)
	if err != nil || !slices.Equal(got, []string{domain.FieldCreditLimit}) {
		t.Fatalf("other tenant sees %v, %v", got, err)
	}
	if _, err := s.ProtectedFields(asUser(1, legalEntityB, approver), legalEntityA); problemCode(t, err) != store.CodeOutOfScope {
		t.Fatalf("read outside scope: %v", err)
	}
	if _, err := s.SetProtectedFields(asUser(1, legalEntityB, approver), legalEntityA, []string{}); problemCode(t, err) != store.CodeOutOfScope {
		t.Fatalf("write outside scope: %v", err)
	}
	ctx := principalContext(1, legalEntityB)
	err = dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into protected_field_config (tenant_id, legal_entity_id, fields, updated_by) values ($1, $2, '{}', 'x')`, testkit.TenantID(1), legalEntityA)
		return err
	})
	if sqlState(err) != rlsViolation {
		t.Fatalf("RLS must refuse a row outside the legal entity scope: %v", err)
	}
}

func TestPendingChange_StatusMustMatchDecidedBy(t *testing.T) {
	app := migratedApp(t)
	customerID := customerWithLimit(t, app, "50000.00")
	ctx := principalContext(1, "")
	insert := func(status, decidedBy, decidedAt string) error {
		return dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `insert into pending_change (tenant_id, id, legal_entity_id, customer_id, field, requested_value, requested_by, status, decided_by, decided_at)
				values ($1, $2, $3, $4, 'credit_limit', 1, 'alice', $5, nullif($6, ''), case when $7 = '' then null else now() end)`,
				testkit.TenantID(1), ids.New(), legalEntityA, customerID, status, decidedBy, decidedAt)
			return err
		})
	}
	if err := insert("pending", "bob", ""); sqlState(err) != "23514" {
		t.Fatalf("pending row with decided_by: %v", err)
	}
	if err := insert("approved", "", "now"); sqlState(err) != "23514" {
		t.Fatalf("approved row without decided_by: %v", err)
	}
	if err := insert("approved", "bob", "now"); err != nil {
		t.Fatalf("approved row with decided_by: %v", err)
	}
}

func TestProtectedFields_UpdateWritesAuditEvent(t *testing.T) {
	app := migratedApp(t)
	s := store.New(app)
	if _, err := s.SetProtectedFields(asUser(1, "", approver), legalEntityA, []string{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetProtectedFields(asUser(1, "", "user-other"), legalEntityA, []string{domain.FieldCreditLimit}); err != nil {
		t.Fatal(err)
	}
	rows := outboxRows(t, principalContext(1, ""), app)
	if len(rows) != 2 {
		t.Fatalf("outbox rows = %d", len(rows))
	}
	for i, want := range []struct{ actor, before, after string }{
		{approver, `["credit_limit"]`, `[]`},
		{"user-other", `[]`, `["credit_limit"]`},
	} {
		var evt struct {
			Data struct {
				LegalEntityID string          `json:"legal_entity_id"`
				Before        json.RawMessage `json:"before"`
				After         json.RawMessage `json:"after"`
				ActorUserID   string          `json:"actor_user_id"`
				UpdatedAt     time.Time       `json:"updated_at"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(rows[i].payload), &evt); err != nil {
			t.Fatal(err)
		}
		d := evt.Data
		if rows[i].typ != "dev.passage.party.protectedfields.updated.v1" || d.LegalEntityID != legalEntityA || d.ActorUserID != want.actor ||
			string(d.Before) != want.before || string(d.After) != want.after || d.UpdatedAt.IsZero() {
			t.Fatalf("event %d = %s", i, rows[i].payload)
		}
	}
}

func TestProtectedFields_SelfUnprotectDoesNotBypassOwnApproval(t *testing.T) {
	app := migratedApp(t)
	s := store.New(app)
	customerID := customerWithLimit(t, app, "50000.00")
	if _, err := s.SetProtectedFields(asUser(1, "", approver), legalEntityA, []string{}); err != nil {
		t.Fatal(err)
	}
	res, err := s.RequestCreditLimit(asUser(1, "", approver), customerID, "90000.00")
	if err != nil || res.Pending == nil || storedLimit(t, app, customerID) != "50000.00" {
		t.Fatalf("own increase after unprotecting must be held: %+v, %v", res, err)
	}
	ctx := principalContext(1, "")
	err = dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update protected_field_config set updated_at = now() - interval '25 hours'`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DecidePendingChange(asUser(1, "", "user-other"), res.Pending.ID, false, ""); err != nil {
		t.Fatal(err)
	}
	res, err = s.RequestCreditLimit(asUser(1, "", approver), customerID, "90000.00")
	if err != nil || res.Pending != nil || storedLimit(t, app, customerID) != "90000.00" {
		t.Fatalf("after 24h the exemption lapses: %+v, %v", res, err)
	}
}
