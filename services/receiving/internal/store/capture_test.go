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

func TestCapture_ListIsNewestFirstFilteredPagedAndIsolated(t *testing.T) {
	db := seedInvoice(t)
	st := store.New(db.pool)
	ctx := tenant.With(context.Background(), db.one)
	older := sampleCapture(payeeOne, db.one.LegalEntityID, "aaa")
	older.Status, older.Reasons = domain.CaptureCaptured, []string{}
	newer := sampleCapture(payeeOther, db.one.LegalEntityID, "bbb")
	for _, c := range []store.Capture{older, newer} {
		if _, _, err := st.CreateCapture(ctx, c); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	all, err := st.ListCaptures(ctx, store.CaptureFilter{})
	if err != nil || len(all) != 2 || all[0].ID != payeeOther || all[1].ID != payeeOne {
		t.Fatalf("all %+v %v, want the newer capture first", all, err)
	}
	review, err := st.ListCaptures(ctx, store.CaptureFilter{Status: domain.CaptureNeedsReview})
	if err != nil || len(review) != 1 || review[0].ID != payeeOther || len(review[0].Reasons) != 1 {
		t.Fatalf("needs review %+v %v", review, err)
	}
	page, err := st.ListCaptures(ctx, store.CaptureFilter{Limit: 1})
	if err != nil || len(page) != 2 {
		t.Fatalf("limit 1 returns the extra row that signals another page: %d %v", len(page), err)
	}
	rest, err := st.ListCaptures(ctx, store.CaptureFilter{Limit: 1, CursorCreatedAt: page[0].CreatedAt, CursorID: page[0].ID})
	if err != nil || len(rest) != 1 || rest[0].ID != payeeOne {
		t.Fatalf("after the cursor %+v %v", rest, err)
	}
	otherEntity := tenant.Principal{TenantID: db.one.TenantID, LegalEntityID: testkit.TenantID(8)}
	for name, c := range map[string]context.Context{"another tenant": tenant.With(context.Background(), principal(2)), "another legal entity": tenant.With(context.Background(), otherEntity)} {
		if got, err := st.ListCaptures(c, store.CaptureFilter{}); err != nil || len(got) != 0 {
			t.Errorf("%s lists %d captures: %v", name, len(got), err)
		}
	}
}
