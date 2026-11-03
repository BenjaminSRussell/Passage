package consumer_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/reimbursement"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/expense/internal/consumer"
	"github.com/BenjaminSRussell/passage/services/expense/internal/domain"
	"github.com/BenjaminSRussell/passage/services/expense/internal/store"
	"github.com/BenjaminSRussell/passage/services/expense/migrations"
)

func settledEvent(t *testing.T, tenantID string, d reimbursement.OpenItemSettled) events.Event {
	t.Helper()
	evt, err := events.New(reimbursement.OpenItemSettledType, "payables", d.OpenItemID, tenantID, d)
	if err != nil {
		t.Fatal(err)
	}
	return evt
}

func TestHandleOpenItemSettled_ReimbursesTheApprovedReportOnceAndIgnoresTheRest(t *testing.T) {
	pool := testkit.DB(t)
	le, day := ids.New(), time.Date(2025, 3, 4, 0, 0, 0, 0, time.UTC)
	as := func(user string) context.Context {
		return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(31), LegalEntityID: le, UserID: user})
	}
	if err := dbx.Migrate(as("sam"), pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(pool)
	rep, _, err := s.CreateReport(as("sam"), "c", "hc", store.NewReport{LegalEntityID: le, WorkerID: ids.New(), Purpose: "Client visit", Currency: "USD"})
	if err != nil {
		t.Fatal(err)
	}
	hotel := domain.Line{ID: ids.New(), Category: "TRAVEL", TxnDate: day, Amount: decimal.NewFromInt(80), Merchant: "Hotel", HasReceipt: true}
	if _, _, err = s.AddLine(as("sam"), rep.ID, "l", "hl", hotel, day); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.SubmitReport(as("sam"), rep.ID, "s", "hs", 0, day); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.DecideReport(as("dana"), rep.ID, true, day, store.Command{Key: "d", Hash: "hd"}); err != nil {
		t.Fatal(err)
	}

	settled := reimbursement.OpenItemSettled{OpenItemID: ids.New(), LegalEntityID: le, VendorID: rep.WorkerID, SourceInvoiceID: rep.ID,
		Currency: "USD", OpenAmount: "0", Status: reimbursement.StatusSettled, SettledAmount: "80", SettlementID: "run-1"}
	partial := settled
	partial.Status, partial.OpenAmount, partial.SettledAmount = "PARTIALLY_SETTLED", "30", "50"
	if err := consumer.HandleOpenItemSettled(context.Background(), s, settledEvent(t, testkit.TenantID(31), partial)); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetReport(as("sam"), rep.ID); got.Status != "APPROVED" {
		t.Fatalf("after a partial settlement status = %s; want APPROVED", got.Status)
	}
	for range 2 {
		if err := consumer.HandleOpenItemSettled(context.Background(), s, settledEvent(t, testkit.TenantID(31), settled)); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := s.GetReport(as("sam"), rep.ID)
	if got.Status != "PAID" || got.APOpenItemID != settled.OpenItemID || got.ReimbursedAt == nil {
		t.Fatalf("report = %+v; want PAID against open item %s", got, settled.OpenItemID)
	}

	bad := settled
	bad.VendorID = "not-a-uuid"
	wrongType, _ := events.New(events.Type("payables", "openitem", "created", 1), "payables", rep.ID, testkit.TenantID(31), settled)
	for name, evt := range map[string]events.Event{"bad id": settledEvent(t, testkit.TenantID(31), bad), "wrong type": wrongType} {
		if err := consumer.HandleOpenItemSettled(context.Background(), s, evt); !errors.Is(err, consumer.ErrInvalidEvent) {
			t.Errorf("%s: err = %v; want ErrInvalidEvent", name, err)
		}
	}
}
