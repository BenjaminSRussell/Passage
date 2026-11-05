package domain

// Load statuses (§6.34.4). A rule-built load starts PLANNED. Releasing it for
// tendering, or starting a tender on it, makes it TENDERED; the tender's
// outcome makes it ACCEPTED or, when every carrier declined or timed out,
// PLANNED again so it can be tendered anew. RATED, DISPATCHED, IN_TRANSIT,
// DELIVERED, CLOSED and CANCELLED are set by steps not built yet.
const (
	LoadPlanned    = "PLANNED"
	LoadRated      = "RATED"
	LoadTendered   = "TENDERED"
	LoadAccepted   = "ACCEPTED"
	LoadDispatched = "DISPATCHED"
	LoadInTransit  = "IN_TRANSIT"
	LoadDelivered  = "DELIVERED"
	LoadClosed     = "CLOSED"
	LoadCancelled  = "CANCELLED"
)

// LoadStatuses lists the load statuses in life-cycle order.
var LoadStatuses = []string{LoadPlanned, LoadRated, LoadTendered, LoadAccepted, LoadDispatched, LoadInTransit, LoadDelivered, LoadClosed, LoadCancelled}

// CodeLoadState is slice-local (the catalog ends at TMS-0135): the load's
// status does not allow the command.
const CodeLoadState = "TMS-0136"

// Load commands and tender outcomes that move a load.
const (
	// MoveRelease is the planner's release for tendering (FR-34.5, #426).
	MoveRelease = "release"
	// MoveTender is a tender start, by a planner or by the workflow after a release.
	MoveTender = "tender"
	// MoveTenderAccepted is a carrier accepting the load's tender.
	MoveTenderAccepted = "tender-accepted"
	// MoveTenderFailed is the tender failing after the last carrier (TMS-0106).
	MoveTenderFailed = "tender-failed"
)

// loadMoves maps each command or outcome to the statuses it applies to and the
// status it leads to. A release needs a load that is not tendered yet: a second
// release would start a second cascade. A tender start also takes a TENDERED
// load, because the workflow starts the tender after the release moved the load.
var loadMoves = map[string]struct {
	from []string
	to   string
}{
	MoveRelease:        {from: []string{LoadPlanned, LoadRated}, to: LoadTendered},
	MoveTender:         {from: []string{LoadPlanned, LoadRated, LoadTendered}, to: LoadTendered},
	MoveTenderAccepted: {from: []string{LoadTendered}, to: LoadAccepted},
	MoveTenderFailed:   {from: []string{LoadTendered}, to: LoadPlanned},
}

// NextLoadStatus returns the status a load in status from moves to on move, or
// TMS-0136 when the status does not allow it.
func NextLoadStatus(from, move string) (string, error) {
	m, ok := loadMoves[move]
	if !ok {
		return "", &Error{Code: CodeLoadState, Detail: "unknown load move " + move}
	}
	if !contains(m.from, from) {
		return "", &Error{Code: CodeLoadState, Detail: "a " + from + " load cannot take the " + move + " step"}
	}
	return m.to, nil
}

// LoadAfterTender returns the status a load in status loadStatus takes when its
// tender reaches tenderStatus, and false when the load stays as it is: a
// PENDING tender, or a load that is not TENDERED (moved on by another step, or
// tendered before loads kept their status). A tender's outcome is the carrier's
// answer and is never refused for the load's sake.
func LoadAfterTender(loadStatus, tenderStatus string) (string, bool) {
	var move string
	switch tenderStatus {
	case TenderAccepted:
		move = MoveTenderAccepted
	case TenderFailed:
		move = MoveTenderFailed
	default:
		return loadStatus, false
	}
	next, err := NextLoadStatus(loadStatus, move)
	if err != nil {
		return loadStatus, false
	}
	return next, true
}
