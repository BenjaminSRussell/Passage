package domain

import "fmt"

// MaxBulkOrders is the largest batch a bulk status update carries (FR-21.12).
const MaxBulkOrders = 500

// CodeBulkPartial is PROD-0022: some orders of a bulk status update failed.
const CodeBulkPartial = "PRODUCTION_BULK_PARTIAL"

// BulkCommand reports whether cmd may be applied in bulk. The commands that
// need per-order input (`:report-finished` quantities, a `:reset-status`
// target) are applied one order at a time.
func BulkCommand(cmd Command) bool {
	switch cmd {
	case CmdEstimate, CmdSchedule, CmdRelease, CmdStart, CmdEnd, CmdCancel:
		return true
	}
	return false
}

// ValidateBulk checks the order ids of a bulk status update: one to
// MaxBulkOrders ids, all distinct. The caller checks that each is a UUID.
func ValidateBulk(orderIDs []string) error {
	if len(orderIDs) == 0 || len(orderIDs) > MaxBulkOrders {
		return &Error{Code: CodeInvalidInput, Detail: fmt.Sprintf("a batch carries 1 to %d orders", MaxBulkOrders)}
	}
	seen := make(map[string]bool, len(orderIDs))
	for _, id := range orderIDs {
		if seen[id] {
			return &Error{Code: CodeInvalidInput, Detail: "order " + id + " is listed twice"}
		}
		seen[id] = true
	}
	return nil
}
