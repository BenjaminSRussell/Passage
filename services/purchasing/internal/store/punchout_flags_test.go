package store_test

import (
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// #35 (35-3): a punchout line keeps its catalog source, supplier part and approval flag.
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
}
