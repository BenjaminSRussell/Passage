package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
)

const captureTable = "receiving_invoice_capture"

func sampleCapture(id, legalEntityID, digest string) store.Capture {
	return store.Capture{
		ID: id, LegalEntityID: legalEntityID, Source: domain.CaptureSourceOCR, Status: domain.CaptureNeedsReview, DocumentSHA256: digest,
		Reasons: []string{domain.ReviewLowConfidence}, CreatedBy: "alice",
		Invoice: domain.ExtractedInvoice{
			VendorTaxID: "DE1", InvoiceNumber: "INV-1", InvoiceDate: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC), Currency: "EUR",
			Total: decimal.RequireFromString("25.50"), Confidence: decimal.RequireFromString("0.61"),
			Lines: []domain.CaptureLine{{Description: "Bolt", Unit: "EA", Qty: decimal.NewFromInt(10), UnitPrice: decimal.RequireFromString("2.55"), Amount: decimal.RequireFromString("25.50")}},
		},
	}
}

func TestCapture_RoundTripsDeduplicatesAndIsIsolated(t *testing.T) {
	db := seedInvoice(t)
	st := store.New(db.pool)
	ctx := tenant.With(context.Background(), db.one)
	first, created, err := st.CreateCapture(ctx, sampleCapture(payeeOne, db.one.LegalEntityID, "abc"))
	if err != nil || !created {
		t.Fatalf("create: %v created=%v", err, created)
	}
	if first.Status != domain.CaptureNeedsReview || len(first.Reasons) != 1 || first.Invoice.Total.String() != "25.5" || len(first.Invoice.Lines) != 1 || first.Invoice.Lines[0].Description != "Bolt" || first.CreatedAt.IsZero() {
		t.Fatalf("stored %+v", first)
	}
	again, created, err := st.CreateCapture(ctx, sampleCapture(payeeOther, db.one.LegalEntityID, "abc"))
	if err != nil || created || again.ID != payeeOne {
		t.Fatalf("duplicate: %v created=%v id=%s", err, created, again.ID)
	}
	if got := db.rows(db.one, captureTable); got != 1 {
		t.Fatalf("%d rows", got)
	}
	read, err := st.GetCapture(ctx, payeeOne)
	if err != nil || read.Invoice.InvoiceNumber != "INV-1" {
		t.Fatalf("read %+v %v", read, err)
	}
	if _, err := st.GetCapture(tenant.With(context.Background(), principal(2)), payeeOne); !errors.Is(err, store.ErrCaptureNotFound) {
		t.Fatalf("another tenant read the capture: %v", err)
	}
	otherEntity := tenant.Principal{TenantID: db.one.TenantID, LegalEntityID: testkit.TenantID(8)}
	if _, err := st.GetCapture(tenant.With(context.Background(), otherEntity), payeeOne); !errors.Is(err, store.ErrCaptureNotFound) {
		t.Fatalf("another legal entity read the capture: %v", err)
	}
	for name, sql := range map[string]string{"an update": `update ` + captureTable + ` set status = 'CAPTURED'`, "a delete": `delete from ` + captureTable} {
		if err := db.run(db.one, sql); err == nil {
			t.Errorf("passage_app must not run %s", name)
		}
	}
}
