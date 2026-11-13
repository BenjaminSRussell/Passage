package store_test

import (
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// #35 (35-3): a punchout line keeps its catalog source, supplier part and approval flag, and the requisition remembers it held a
// flagged line: an edit that drops the line leaves the requisition needing approval.
func TestRequisitionLine_KeepsItsPunchoutFlags(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(31)
	in := requisition(t, ids.New(), "10", "20")
	in.Lines[0].CatalogSource, in.Lines[0].SupplierPartID, in.Lines[0].NeedsApproval = store.CatalogSourcePunchout, "SKU-77", true
	r, err := s.CreateRequisition(ctx, in, store.Idem{})
	if err != nil || r.Lines[0].CatalogSource != "PUNCHOUT" || r.Lines[0].SupplierPartID != "SKU-77" || !r.Lines[0].NeedsApproval ||
		r.Lines[1].CatalogSource != "" || r.Lines[1].NeedsApproval {
		t.Fatalf("created = %+v, %v", r.Lines, err)
	}
	if !r.NeedsApproval {
		t.Fatal("a requisition created with a flagged line needs approval")
	}
	edit := store.RequisitionEdit{Currency: "USD", Lines: []store.RequisitionLine{lineOf(t, 1, "7")}, Total: dec("14"), ActorID: r.RequesterID}
	got, err := s.UpdateRequisition(ctx, r.ID, r.Version, edit, store.Idem{})
	if err != nil || !got.NeedsApproval || got.Lines[0].NeedsApproval {
		t.Fatalf("after dropping the flagged line = %+v, %v", got, err)
	}
	reloaded, err := s.GetRequisition(ctx, r.ID)
	if err != nil || !reloaded.NeedsApproval {
		t.Fatalf("reloaded = %v, %v", reloaded.NeedsApproval, err)
	}

	// An edit that flags a line marks a requisition that was not flagged.
	plain, err := s.CreateRequisition(ctx, requisition(t, ids.New(), "10", "20"), store.Idem{})
	if err != nil || plain.NeedsApproval {
		t.Fatalf("plain = %v, %v", plain.NeedsApproval, err)
	}
	flaggedLine := lineOf(t, 1, "7")
	flaggedLine.CatalogSource, flaggedLine.SupplierPartID, flaggedLine.NeedsApproval = store.CatalogSourcePunchout, "SKU-78", true
	got, err = s.UpdateRequisition(ctx, plain.ID, plain.Version, store.RequisitionEdit{Currency: "USD", Lines: []store.RequisitionLine{flaggedLine},
		Total: dec("14"), ActorID: plain.RequesterID}, store.Idem{})
	if err != nil || !got.NeedsApproval {
		t.Fatalf("flagged by edit = %v, %v", got.NeedsApproval, err)
	}
}
