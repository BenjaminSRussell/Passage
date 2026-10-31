package domain

import (
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// LineStatus is the state of one work line.
type LineStatus string

const (
	LineOpen      LineStatus = "OPEN"
	LineCompleted LineStatus = "COMPLETED"
	LineShort     LineStatus = "SHORT"
	LineCancelled LineStatus = "CANCELLED"
)

// CodeLineConfirmed is the second confirmation of a line with a different key
// (BR-31.8, 409 LINE_ALREADY_CONFIRMED). WMS-0102 and WMS-0103 come from Move
// through MoveError.
const CodeLineConfirmed = "WMS-0105"

// CodeOverPick is a confirmed quantity above the open quantity plus the
// over-pick tolerance, which is 0% until per-warehouse settings exist (BR-31.7).
const CodeOverPick = "WMS-0106"

// DefaultPriority is the priority of a work when the request gives none
// (1 is the highest, 99 the lowest; templates set it in a later slice).
const DefaultPriority = 50

// ErrLineNotFound is returned when a work has no line with the requested number.
var ErrLineNotFound = errors.New("work line not found")

// ErrReasonRequired is returned for a short pick or a cancel without a reason code.
var ErrReasonRequired = errors.New("a reason is required")

// ErrNotAssignee is returned when a user other than the assignee moves a line
// of a work, or cancels a work assigned to someone else without supervision.
var ErrNotAssignee = errors.New("work is assigned to another user")

// WorkTypes are the work order types of the PRD data model (§6.31.4).
var WorkTypes = []string{"PURCHASE_PUTAWAY", "SALES_PICK", "TRANSFER_ISSUE", "TRANSFER_RECEIPT", "PRODUCTION_PICK",
	"RAF_PUTAWAY", "REPLENISHMENT", "MOVEMENT", "COUNT", "RETURN"}

// ValidWorkType reports whether t is one of WorkTypes.
func ValidWorkType(t string) bool {
	for _, w := range WorkTypes {
		if w == t {
			return true
		}
	}
	return false
}

// NewLine is a requested movement before any location is chosen.
type NewLine struct {
	ItemID     string
	Direction  Direction
	Qty        decimal.Decimal
	Weight     decimal.Decimal
	Volume     decimal.Decimal
	LPs        int
	Attributes map[string]string
}

// WorkLine is one movement of a work: an item quantity taken from a location
// (PICK) or put into one (PUT). A line with no location is blocked and its
// Reason holds WMS-0101.
type WorkLine struct {
	LineNo         int               `json:"lineNo"`
	ItemID         string            `json:"itemId"`
	Direction      Direction         `json:"direction"`
	Qty            decimal.Decimal   `json:"qty"`
	ConfirmedQty   decimal.Decimal   `json:"confirmedQty"`
	Weight         decimal.Decimal   `json:"weight"`
	Volume         decimal.Decimal   `json:"volume"`
	LPs            int               `json:"lps"`
	FromLocationID string            `json:"fromLocationId,omitempty"`
	ToLocationID   string            `json:"toLocationId,omitempty"`
	Status         LineStatus        `json:"status"`
	Reason         string            `json:"reason,omitempty"`
	Attributes     map[string]string `json:"attributes,omitempty"`
	// ConfirmKey is the Idempotency-Key of the confirmation (BR-31.8).
	ConfirmKey string `json:"-"`
}

// Work is a unit of warehouse execution: a header and its lines (BR-31.3).
// Version counts every stored change and backs If-Match on work commands.
type Work struct {
	ID           string     `json:"id"`
	WarehouseID  string     `json:"warehouseId"`
	WorkType     string     `json:"workType"`
	Status       WorkStatus `json:"status"`
	Priority     int        `json:"priority"`
	ParentID     string     `json:"parentWorkId,omitempty"`
	AssignedUser string     `json:"assignedUser,omitempty"`
	AssignedAt   *time.Time `json:"assignedAt,omitempty"`
	Version      int        `json:"version"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
	Lines        []WorkLine `json:"lines"`
}

// Plan creates a work for reqs. Each line gets the location the directives
// choose (BR-31.1); a line with no location keeps none and the work is
// BLOCKED_NO_LOCATION. Every chosen location is reserved for the following
// lines of the same work (BR-31.2: in-progress PUT lines use capacity, open
// PICK lines hold their stock), so two lines never claim the same room or
// stock. locs is not modified. The per-line results carry the rule trace.
func Plan(id, warehouseID, workType string, priority int, reqs []NewLine, dirs []Directive, locs []Location, comp Compliance) (Work, []Result, error) {
	if len(reqs) == 0 {
		return Work{}, nil, errors.New("a work needs at least one line")
	}
	if priority == 0 {
		priority = DefaultPriority
	}
	pool := CloneLocations(locs)
	w := Work{ID: id, WarehouseID: warehouseID, WorkType: workType, Status: WorkOpen, Priority: priority}
	results := make([]Result, 0, len(reqs))
	blocked := false
	for i, req := range reqs {
		if !req.Qty.IsPositive() {
			return Work{}, nil, fmt.Errorf("line %d: quantity must be positive", i+1)
		}
		line := WorkLine{LineNo: i + 1, ItemID: req.ItemID, Direction: req.Direction, Qty: req.Qty, ConfirmedQty: decimal.Zero,
			Weight: req.Weight, Volume: req.Volume, LPs: req.LPs, Status: LineOpen, Attributes: req.Attributes}
		res := EvaluateWithCompliance(dirs, Line{ItemID: req.ItemID, WorkType: workType, Direction: req.Direction,
			Warehouse: warehouseID, Qty: req.Qty, Weight: req.Weight, Volume: req.Volume, LPs: req.LPs, Attributes: req.Attributes},
			pool, comp)
		results = append(results, res)
		switch {
		case res.Blocked:
			blocked = true
			line.Reason = res.Code
		case req.Direction == DirectionPick:
			line.FromLocationID = res.Location.ID
		default:
			line.ToLocationID = res.Location.ID
		}
		Reserve(pool, []WorkLine{line})
		w.Lines = append(w.Lines, line)
	}
	if blocked {
		w.Status = WorkBlockedNoLocation
	}
	return w, results, nil
}

// CloneLocations copies locations deeply enough to reserve on the copy.
func CloneLocations(locs []Location) []Location {
	out := make([]Location, len(locs))
	for i, l := range locs {
		l.Stock = maps.Clone(l.Stock)
		l.inbound = maps.Clone(l.inbound)
		l.incoming = maps.Clone(l.incoming)
		out[i] = l
	}
	return out
}

// Reserve applies open lines to the locations they target, in place, for
// planning only (BR-31.2): a PUT line adds its weight, volume and LPs to the
// destination's usage and its item to the items it will hold, and a PICK line removes its quantity from the stock
// the source can still promise. Lines that are not OPEN, or have no location
// in locs, are ignored.
func Reserve(locs []Location, lines []WorkLine) {
	index := make(map[string]int, len(locs))
	for i := range locs {
		index[locs[i].ID] = i
	}
	for _, l := range lines {
		if l.Status != LineOpen {
			continue
		}
		i, ok := index[l.LocationOf()]
		if !ok {
			continue
		}
		loc := &locs[i]
		if l.Direction == DirectionPut {
			loc.Used.Weight = round6(loc.Used.Weight.Add(round6(l.Weight)))
			loc.Used.Volume = round6(loc.Used.Volume.Add(round6(l.Volume)))
			loc.Used.LPs += l.LPs
			if loc.inbound == nil {
				loc.inbound = map[string]bool{}
			}
			loc.inbound[l.ItemID] = true
			if loc.incoming == nil {
				loc.incoming = map[string]decimal.Decimal{}
			}
			loc.incoming[l.ItemID] = loc.incoming[l.ItemID].Add(l.Qty)
			continue
		}
		rest := loc.Stock[l.ItemID].Sub(l.Qty)
		if rest.IsPositive() {
			loc.Stock[l.ItemID] = rest
		} else {
			delete(loc.Stock, l.ItemID)
		}
	}
}

// lineIndex finds line n (1-based) or reports ErrLineNotFound.
func (w *Work) lineIndex(n int) (int, error) {
	for i := range w.Lines {
		if w.Lines[i].LineNo == n {
			return i, nil
		}
	}
	return 0, ErrLineNotFound
}

// Line returns line n of the work.
func (w *Work) Line(n int) (WorkLine, error) {
	i, err := w.lineIndex(n)
	if err != nil {
		return WorkLine{}, err
	}
	return w.Lines[i], nil
}

// LocationOf is the location a line moves stock to (PUT) or from (PICK).
func (l WorkLine) LocationOf() string {
	if l.Direction == DirectionPick {
		return l.FromLocationID
	}
	return l.ToLocationID
}

// confirmedCount counts lines that have been confirmed or short picked.
func (w *Work) confirmedCount() int {
	n := 0
	for _, l := range w.Lines {
		if l.Status == LineCompleted || l.Status == LineShort {
			n++
		}
	}
	return n
}

// Assign gives an OPEN work to user (BR-31.3 OPEN -> ASSIGNED).
func (w *Work) Assign(user string, at time.Time) error {
	if err := CheckTransition(w.Status, WorkAssigned, w.confirmedCount()); err != nil {
		return err
	}
	w.Status = WorkAssigned
	w.AssignedUser = user
	w.AssignedAt = &at
	return nil
}

// requireExecutable checks the work can take line movements by user: it is
// ASSIGNED or IN_PROGRESS, and user is its assignee.
func (w *Work) requireExecutable(user string) error {
	if w.Status != WorkAssigned && w.Status != WorkInProgress {
		return errorf(CodeIllegalTransition, "lines of a %s work cannot be confirmed; assign it first", w.Status)
	}
	if w.AssignedUser != user {
		return ErrNotAssignee
	}
	return nil
}

// start moves an ASSIGNED work to IN_PROGRESS on its first line movement.
func (w *Work) start() error {
	if w.Status == WorkInProgress {
		return nil
	}
	if err := CheckTransition(w.Status, WorkInProgress, w.confirmedCount()); err != nil {
		return err
	}
	w.Status = WorkInProgress
	return nil
}

// ConfirmLine confirms line n at qty for user, the assignee. qty must equal
// the open quantity: the over-pick tolerance is 0 (BR-31.7), so more is
// WMS-0106 and less must be reported as a short pick. key is the
// confirmation's Idempotency-Key: the same key on a confirmed line is a replay
// (replayed=true, nothing changes); another key is WMS-0105 (BR-31.8).
func (w *Work) ConfirmLine(n int, qty decimal.Decimal, key, user string) (replayed bool, err error) {
	i, err := w.lineIndex(n)
	if err != nil {
		return false, err
	}
	l := &w.Lines[i]
	if l.Status == LineCompleted {
		if key != "" && l.ConfirmKey == key {
			return true, nil
		}
		return false, errorf(CodeLineConfirmed, "line %d of work %s is already confirmed", n, w.ID)
	}
	if l.Status != LineOpen {
		return false, errorf(CodeIllegalTransition, "line %d is %s", n, l.Status)
	}
	if err := w.requireExecutable(user); err != nil {
		return false, err
	}
	if qty.GreaterThan(l.Qty) {
		return false, errorf(CodeOverPick, "quantity %s exceeds open quantity %s plus over-pick tolerance 0%%", qty, l.Qty)
	}
	if !qty.Equal(l.Qty) {
		return false, errorf(CodeIllegalTransition, "quantity %s is below open quantity %s: report a short pick", qty, l.Qty)
	}
	if err := w.start(); err != nil {
		return false, err
	}
	l.Status = LineCompleted
	l.ConfirmedQty = qty
	l.ConfirmKey = key
	return false, w.settle()
}

// ShortPick confirms PICK line n at qty below its open quantity with a reason,
// for user, the assignee. The line becomes SHORT, and the remainder becomes a
// new OPEN work (remainderID) whose ParentID is w (BR-31.3). Only qty moves
// stock; the caller applies it. A PUT line cannot be short picked.
func (w *Work) ShortPick(n int, qty decimal.Decimal, reason, remainderID, user string) (*Work, error) {
	i, err := w.lineIndex(n)
	if err != nil {
		return nil, err
	}
	l := &w.Lines[i]
	if l.Direction != DirectionPick {
		return nil, errorf(CodeIllegalTransition, "line %d is a %s line; only PICK lines can be short picked", n, l.Direction)
	}
	if l.Status != LineOpen {
		return nil, errorf(CodeIllegalTransition, "line %d is %s", n, l.Status)
	}
	if err := w.requireExecutable(user); err != nil {
		return nil, err
	}
	if strings.TrimSpace(reason) == "" {
		return nil, ErrReasonRequired
	}
	if qty.IsNegative() || !qty.LessThan(l.Qty) {
		return nil, errorf(CodeIllegalTransition, "short pick quantity %s must be at least 0 and below open quantity %s", qty, l.Qty)
	}
	if err := w.start(); err != nil {
		return nil, err
	}
	rem := l.Qty.Sub(qty)
	ratio := rem.Div(l.Qty)
	child := &Work{ID: remainderID, WarehouseID: w.WarehouseID, WorkType: w.WorkType, Status: WorkOpen,
		Priority: w.Priority, ParentID: w.ID}
	child.Lines = []WorkLine{{LineNo: 1, ItemID: l.ItemID, Direction: l.Direction, Qty: rem, ConfirmedQty: decimal.Zero,
		Weight: round6(l.Weight.Mul(ratio)), Volume: round6(l.Volume.Mul(ratio)), LPs: l.LPs - l.confirmedLPs(qty),
		FromLocationID: l.FromLocationID, ToLocationID: l.ToLocationID, Status: LineOpen, Attributes: l.Attributes}}
	l.ConfirmedQty = qty
	l.Status = LineShort
	l.Reason = reason
	if err := w.settle(); err != nil {
		return nil, err
	}
	return child, nil
}

// confirmedLPs is the whole LPs a confirmed quantity accounts for (rounded down).
func (l WorkLine) confirmedLPs(qty decimal.Decimal) int {
	if l.Qty.IsZero() {
		return 0
	}
	return int(decimal.NewFromInt(int64(l.LPs)).Mul(qty.Div(l.Qty)).IntPart())
}

// ConfirmedUsage is the weight, volume and LPs moved by the confirmed quantity
// of a line, pro rata to the line quantity.
func (l WorkLine) ConfirmedUsage() Usage {
	if l.Qty.IsZero() || !l.ConfirmedQty.IsPositive() {
		return Usage{}
	}
	r := l.ConfirmedQty.Div(l.Qty)
	return Usage{Weight: round6(l.Weight.Mul(r)), Volume: round6(l.Volume.Mul(r)), LPs: l.confirmedLPs(l.ConfirmedQty)}
}

// Cancel cancels an OPEN, ASSIGNED or BLOCKED_NO_LOCATION work (BR-31.3) with
// a reason. It is refused once any line is confirmed. An ASSIGNED work can be
// cancelled by its assignee, or by anyone when supervise is true; open lines
// become CANCELLED and carry the reason.
func (w *Work) Cancel(reason, user string, supervise bool) error {
	if strings.TrimSpace(reason) == "" {
		return ErrReasonRequired
	}
	if !supervise && w.AssignedUser != user {
		return ErrNotAssignee
	}
	if err := CheckTransition(w.Status, WorkCancelled, w.confirmedCount()); err != nil {
		return err
	}
	w.Status = WorkCancelled
	w.AssignedUser, w.AssignedAt = "", nil
	for i := range w.Lines {
		if w.Lines[i].Status == LineOpen {
			w.Lines[i].Status = LineCancelled
			w.Lines[i].Reason = reason
		}
	}
	return nil
}

// Terminal reports whether every line is settled (no OPEN line is left).
func (w *Work) Terminal() bool {
	for _, l := range w.Lines {
		if l.Status == LineOpen {
			return false
		}
	}
	return true
}

// settle moves an IN_PROGRESS work to COMPLETED, or to PARTIALLY_COMPLETED when
// a line was short picked, once no line is OPEN.
func (w *Work) settle() error {
	if !w.Terminal() {
		return nil
	}
	next := WorkCompleted
	for _, l := range w.Lines {
		if l.Status == LineShort {
			next = WorkPartiallyComplete
		}
	}
	if err := CheckTransition(w.Status, next, w.confirmedCount()); err != nil {
		return err
	}
	w.Status = next
	return nil
}

// Completed reports whether the work has reached a terminal execution state.
func (w *Work) Completed() bool {
	return w.Status == WorkCompleted || w.Status == WorkPartiallyComplete
}

// Move applies a confirmed movement to the location: a PUT receives and a PICK
// issues. When a rule refuses, the location is unchanged and the reason is
// returned (BR-31.2). The caller turns the reason into WMS-0102/0103/0110 with
// MoveError.
func (l *Location) Move(dir Direction, item string, qty decimal.Decimal, u Usage) (string, bool) {
	if !l.Status.Allows(dir) {
		return fmt.Sprintf("status %s blocks %s", l.Status, dir), false
	}
	if dir == DirectionPut {
		return l.Receive(item, qty, u)
	}
	return l.Issue(item, qty, u)
}

// Issue removes qty of item and its usage from the location. It refuses when
// the location holds less than qty or when the usage would go negative.
func (l *Location) Issue(item string, qty decimal.Decimal, u Usage) (string, bool) {
	have := l.Stock[item]
	if qty.GreaterThan(have) {
		return fmt.Sprintf("stock: holds %s, needs %s", have, qty), false
	}
	weight := round6(l.Used.Weight.Sub(round6(u.Weight)))
	if weight.IsNegative() {
		return "usage: weight would go below zero", false
	}
	volume := round6(l.Used.Volume.Sub(round6(u.Volume)))
	if volume.IsNegative() {
		return "usage: volume would go below zero", false
	}
	lps := l.Used.LPs - u.LPs
	if lps < 0 {
		return "usage: lps would go below zero", false
	}
	l.Used.Weight, l.Used.Volume, l.Used.LPs = weight, volume, lps
	if rest := have.Sub(qty); rest.IsPositive() {
		l.Stock[item] = rest
	} else {
		delete(l.Stock, item)
	}
	return "", true
}

// MoveError maps a refusal from Move to its catalog code: capacity (WMS-0102),
// mixing (WMS-0103); a blocked status, missing stock or negative usage is an
// illegal movement (WMS-0110).
func MoveError(reason string) *Error {
	switch {
	case strings.HasPrefix(reason, "capacity:"):
		return errorf(CodeCapacity, "%s", reason)
	case strings.HasPrefix(reason, "mixing:"):
		return errorf(CodeMixedItems, "%s", reason)
	}
	return errorf(CodeIllegalTransition, "%s", reason)
}
