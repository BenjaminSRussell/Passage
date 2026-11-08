package events

import "github.com/shopspring/decimal"

// RequisitionReleaseCompleted is written when a requisition release run (FR-17.3, PRD 3.17.16) has ordered what it could
// of the approved requisition lines that had a vendor. The subject is the run id. A run that found nothing to release
// writes no event. A buyer notification keys on SkippedCount > 0: those lines are still in the consolidation queue.
const RequisitionReleaseCompleted = "dev.passage.procurement.requisitionrelease.completed.v1"

// ReleasedOrder is a draft purchase order a run created.
type ReleasedOrder struct {
	PurchaseOrderID string          `json:"purchase_order_id"`
	Number          string          `json:"number"`
	VendorID        string          `json:"vendor_id"`
	Currency        string          `json:"currency"`
	Total           decimal.Decimal `json:"total"`
	LineCount       int             `json:"line_count"`
}

// ReleaseSkippedLine is a requisition line a run left in the queue, with the problem code that stopped it.
type ReleaseSkippedLine struct {
	RequisitionLineID string `json:"requisition_line_id"`
	Code              string `json:"code"`
}

// RequisitionReleaseData is the data member of requisitionrelease.completed.v1.
type RequisitionReleaseData struct {
	RunID           string               `json:"run_id"`
	LegalEntityID   string               `json:"legal_entity_id"`
	Status          string               `json:"status"`
	WindowDays      int                  `json:"window_days"`
	LinesConsidered int                  `json:"lines_considered"`
	LinesReleased   int                  `json:"lines_released"`
	SkippedCount    int                  `json:"skipped_count"`
	PurchaseOrders  []ReleasedOrder      `json:"purchase_orders"`
	Skipped         []ReleaseSkippedLine `json:"skipped"`
	RequestedBy     string               `json:"requested_by,omitempty"`
}
