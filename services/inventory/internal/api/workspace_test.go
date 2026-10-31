package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

var lastRecon = time.Date(2026, 10, 27, 2, 0, 0, 0, time.UTC)

func (f *fakeStore) WorkspaceSummary(_ context.Context, legalEntityID string) (store.WorkspaceSummary, error) {
	f.workspaceEntity = legalEntityID
	if f.err != nil {
		return store.WorkspaceSummary{}, f.err
	}
	return store.WorkspaceSummary{JournalsDraft: 4, JournalsAwaitingApproval: 2, NegativeOnHandRows: 1, OpenCountSheets: 3, CountSheetsCounted: 1,
		ReconciliationDifferences: 1, LastReconciliationAt: &lastRecon, ActiveReservations: 7, TransferOrdersByStatus: map[string]int{"SHIPPED": 5}}, nil
}

func TestWorkspaceSummaryCarriesEveryTileAndEveryTransferStatus(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	entity := ids.New()
	rec := do(t, h, http.MethodGet, "/v1/inventory/workspace/summary?legalEntityId="+entity, "", []string{"inventory.read"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Journals                  struct{ Draft, AwaitingApproval int }
		NegativeOnHandRows        int
		OpenCountSheets           int
		CountSheetsCounted        int
		ReconciliationDifferences int
		LastReconciliationAt      *time.Time
		ActiveReservations        int
		TransferOrdersByStatus    map[string]int
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Journals.Draft != 4 || out.Journals.AwaitingApproval != 2 || out.NegativeOnHandRows != 1 || out.OpenCountSheets != 3 ||
		out.CountSheetsCounted != 1 || out.ReconciliationDifferences != 1 || out.ActiveReservations != 7 {
		t.Fatalf("tiles %+v", out)
	}
	if out.LastReconciliationAt == nil || !out.LastReconciliationAt.Equal(lastRecon) {
		t.Fatalf("last reconciliation %v, want %v", out.LastReconciliationAt, lastRecon)
	}
	// Statuses without orders are present with 0, so the page never has to guess.
	if len(out.TransferOrdersByStatus) != 5 || out.TransferOrdersByStatus["SHIPPED"] != 5 || out.TransferOrdersByStatus["CREATED"] != 0 {
		t.Fatalf("transfer statuses %+v", out.TransferOrdersByStatus)
	}
	if _, ok := out.TransferOrdersByStatus["CANCELLED"]; !ok {
		t.Fatalf("CANCELLED missing from %+v", out.TransferOrdersByStatus)
	}
	if fs.workspaceEntity != entity {
		t.Fatalf("legal entity filter %q", fs.workspaceEntity)
	}
}

func TestWorkspaceSummaryRejectsBadEntityAndNeedsTheReadDuty(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	rec := do(t, h, http.MethodGet, "/v1/inventory/workspace/summary?legalEntityId=nope", "", []string{"inventory.read"}, nil)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "legalEntityId") {
		t.Fatalf("bad entity: %d %s", rec.Code, rec.Body.String())
	}
	if fs.workspaceEntity != "" {
		t.Fatalf("store called for a rejected request with %q", fs.workspaceEntity)
	}
	if rec := do(t, h, http.MethodGet, "/v1/inventory/workspace/summary", "", []string{"inventory.transfer.write"}, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("no read duty: %d", rec.Code)
	}
}

func TestWorkspaceSummaryStoreFailureIsAProblemNotPartialCounts(t *testing.T) {
	h := newServer(t, &fakeStore{err: errors.New("connection reset")})
	rec := do(t, h, http.MethodGet, "/v1/inventory/workspace/summary", "", []string{"inventory.read"}, nil)
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "negativeOnHandRows") {
		t.Fatalf("store failure: %d %s", rec.Code, rec.Body.String())
	}
}
