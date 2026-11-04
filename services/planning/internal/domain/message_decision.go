package domain

// Message statuses (FR-22.7): a message is open until a planner applies or dismisses it.
const (
	MessageOpen      = "open"
	MessageApplied   = "applied"
	MessageDismissed = "dismissed"
)

// MessageActionable reports whether applying a message changes a source order. Only the
// messages about existing supply carry a change for the order's owner (postpone and advance
// move its date, cancel removes it, decrease and increase change its quantity); delay and
// shortage tell the planner about demand, and red-zone, sales-on-top, no-vendor and lot-split ask for a
// planning decision, so they can only be dismissed.
func MessageActionable(action string) bool {
	switch action {
	case ActionPostpone, ActionAdvance, ActionCancel, ActionDecrease, ActionIncrease:
		return true
	}
	return false
}
