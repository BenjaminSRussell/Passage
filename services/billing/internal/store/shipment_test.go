package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/billing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/billing/migrations"
)

// shipmentInvoice is a one-line shipment proposal billing slip line slipLine.
func shipmentInvoice(t *testing.T, le, slipLine string) Invoice {
	t.Helper()
	inv := newProposal(t, le)
	inv.Type = domain.TypeShipment
	inv.Lines[0].Source = domain.Source{PackingSlipID: ids.New(), PackingSlipLineID: slipLine, PackingSlipDate: inv.InvoiceDate.AddDate(0, 0, -1),
		OrderID: ids.New(), OrderLineID: ids.New(), ProductID: ids.New(), PriceExplainRef: "px-1"}
	return inv
}

func batchFor(le string) ProposalBatch {
	return ProposalBatch{ID: ids.New(), LegalEntityID: le, IdempotencyKey: ids.New(), RequestHash: "h"}
}

func TestShipmentProposals_BatchStoredReplayedAndPosted(t *testing.T) {
	pool := testkit.DB(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	tctx := ctxAllEntities(testkit.TenantID(1))
	s := New(pool)
	le := ids.New()
	first, second := shipmentInvoice(t, le, ids.New()), shipmentInvoice(t, le, ids.New())
	second.Type = domain.TypeConsolidated
	b := batchFor(le)
	created, err := s.CreateShipmentProposals(tctx, b, []Invoice{first, second})
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 2 || created[0].ProposalBatchSeq != 1 || created[1].ProposalBatchSeq != 2 || created[0].Version != 1 {
		t.Fatalf("created %+v", created)
	}

	got, invs, found, err := s.FindProposalBatch(tctx, b.IdempotencyKey)
	if err != nil || !found || got.ID != b.ID || got.RequestHash != "h" {
		t.Fatalf("batch %+v found %v err %v", got, found, err)
	}
	if len(invs) != 2 || invs[0].ID != first.ID || invs[1].ID != second.ID || invs[1].Type != domain.TypeConsolidated {
		t.Fatalf("batch invoices out of order or missing: %+v", invs)
	}
	src := invs[0].Lines[0].Source
	want := first.Lines[0].Source
	if src.PackingSlipID != want.PackingSlipID || src.PackingSlipLineID != want.PackingSlipLineID || !src.PackingSlipDate.Equal(want.PackingSlipDate) ||
		src.OrderID != want.OrderID || src.OrderLineID != want.OrderLineID || src.ProductID != want.ProductID || src.PriceExplainRef != "px-1" {
		t.Errorf("source read back %+v, want %+v", src, want)
	}
	if _, _, found, err := s.FindProposalBatch(tctx, "unknown"); err != nil || found {
		t.Errorf("unknown key: found %v err %v", found, err)
	}

	// Posting a shipment invoice publishes the slip line, order line and product.
	if _, err := s.PostProposal(tctx, first.ID, time.Date(2027, 3, 12, 0, 0, 0, 0, time.UTC), 1); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := pool.QueryRow(ctx, `select payload from outbox where aggregate_id = $1`, first.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var evt struct {
		Data struct {
			Type  string `json:"type"`
			Lines []struct {
				OrderLineID       string `json:"order_line_id"`
				Product           string `json:"product"`
				PackingSlipLineID string `json:"packing_slip_line_id"`
			} `json:"lines"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &evt); err != nil {
		t.Fatal(err)
	}
	if evt.Data.Type != domain.TypeShipment || len(evt.Data.Lines) != 1 || evt.Data.Lines[0].PackingSlipLineID != want.PackingSlipLineID ||
		evt.Data.Lines[0].OrderLineID != want.OrderLineID || evt.Data.Lines[0].Product != want.ProductID {
		t.Errorf("invoice.posted %s", raw)
	}
}

func TestShipmentProposals_SlipLineInvoicedOnceAndBatchIsAtomic(t *testing.T) {
	pool := testkit.DB(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	tctx := ctxAllEntities(testkit.TenantID(1))
	s := New(pool)
	le := ids.New()
	taken := ids.New()
	if _, err := s.CreateShipmentProposals(tctx, batchFor(le), []Invoice{shipmentInvoice(t, le, taken)}); err != nil {
		t.Fatal(err)
	}

	// A second batch with one fresh and one taken slip line stores neither invoice.
	fresh := shipmentInvoice(t, le, ids.New())
	again := batchFor(le)
	_, err := s.CreateShipmentProposals(tctx, again, []Invoice{fresh, shipmentInvoice(t, le, taken)})
	var p *httpx.Problem
	if !errors.As(err, &p) || p.Status != http.StatusConflict || p.Code != CodeSlipLineInvoiced {
		t.Fatalf("taken slip line: %v, want 409 %s", err, CodeSlipLineInvoiced)
	}
	if _, _, found, _ := s.FindProposalBatch(tctx, again.IdempotencyKey); found {
		t.Error("the refused batch was stored")
	}
	if _, err := s.GetInvoice(tctx, fresh.ID); err == nil {
		t.Error("the refused batch's other invoice was stored")
	}

	// The same key twice is a concurrent duplicate, not a slip conflict.
	dup := batchFor(le)
	if _, err := s.CreateShipmentProposals(tctx, dup, []Invoice{shipmentInvoice(t, le, ids.New())}); err != nil {
		t.Fatal(err)
	}
	dup2 := batchFor(le)
	dup2.IdempotencyKey = dup.IdempotencyKey
	if _, err := s.CreateShipmentProposals(tctx, dup2, []Invoice{shipmentInvoice(t, le, ids.New())}); !errors.As(err, &p) || p.Code != CodeKeyInFlight {
		t.Fatalf("duplicate key: %v, want %s", err, CodeKeyInFlight)
	}

	// A free-text invoice in a batch is refused before anything is stored.
	if _, err := s.CreateShipmentProposals(tctx, batchFor(le), []Invoice{newProposal(t, le)}); err == nil {
		t.Error("a free-text invoice was accepted into a proposal batch")
	}
}

func TestShipmentProposals_Isolation(t *testing.T) {
	if os.Getenv(testkit.EnvURL) == "" || os.Getenv(testkit.EnvAppURL) == "" {
		t.Skipf("%s and %s are needed for the RLS test", testkit.EnvURL, testkit.EnvAppURL)
	}
	admin, app := testkit.DBWithApp(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := New(app)
	le := ids.New()
	b := batchFor(le)
	inv := shipmentInvoice(t, le, ids.New())
	if _, err := s.CreateShipmentProposals(ctxAllEntities(testkit.TenantID(1)), b, []Invoice{inv}); err != nil {
		t.Fatal(err)
	}
	if _, _, found, err := s.FindProposalBatch(ctxAllEntities(testkit.TenantID(2)), b.IdempotencyKey); err != nil || found {
		t.Fatalf("tenant 2 found tenant 1's batch: %v %v", found, err)
	}
	// Another tenant may bill a slip line id of its own without conflict.
	other := shipmentInvoice(t, le, inv.Lines[0].Source.PackingSlipLineID)
	if _, err := s.CreateShipmentProposals(ctxAllEntities(testkit.TenantID(2)), batchFor(le), []Invoice{other}); err != nil {
		t.Fatalf("tenant 2: %v", err)
	}
}

func TestShipmentTables_ForceRowLevelSecurity(t *testing.T) {
	pool := testkit.DB(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	var enabled, forced bool
	var policies int
	if err := pool.QueryRow(ctx, `select c.relrowsecurity, c.relforcerowsecurity,
		(select count(*) from pg_policy p where p.polrelid = c.oid)
		from pg_class c where c.oid = 'proposal_batch'::regclass`).Scan(&enabled, &forced, &policies); err != nil {
		t.Fatal(err)
	}
	if !enabled || !forced || policies != 2 {
		t.Errorf("proposal_batch: rls %v force %v policies %d, want tenant and legal-entity policies under forced RLS", enabled, forced, policies)
	}
}
