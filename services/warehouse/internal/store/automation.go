package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/warehouse/internal/domain"
)

// RFID reads (FR-32.9) and warehouse automation (FR-32.12) in the store.

// RFIDRead is one tag read posted by a reader or a portal.
type RFIDRead struct {
	ReaderID     string
	Antenna      int
	LocationCode string
	EPC          string // hexadecimal, 24 characters
	RSSI         *int
	ReadAt       time.Time
	// PostedBy is the authenticated caller that posted the read; only a rule naming it acts on the read.
	PostedBy string
}

// RFIDRejected is a read that was not stored, with its position in the batch.
type RFIDRejected struct {
	Index  int    `json:"index"`
	Code   string `json:"code"`
	Reason string `json:"reason"`
}

// RFIDIngest is the outcome of a batch: what was stored, what repeated a read already held (a portal reads a tag many
// times as it passes), and what could not be read.
type RFIDIngest struct {
	Accepted   int            `json:"accepted"`
	Duplicates int            `json:"duplicates"`
	Rejected   []RFIDRejected `json:"rejected"`
	Reads      []RFIDStored   `json:"reads"`
}

// RFIDStored is an accepted read with what the tag identifies; a tag that is a known pallet names its receipt.
type RFIDStored struct {
	Index     int        `json:"index"`
	ID        string     `json:"id"`
	EPC       domain.EPC `json:"epc"`
	ReceiptID string     `json:"receiptId,omitempty"`
	LineNo    int        `json:"lineNo,omitempty"`
	// Actions is what the reader's rules did with the read (receive the pallet, confirm its put-away).
	Actions []RFIDAction `json:"actions,omitempty"`
}

// DedupeWindow is how close two reads of one tag at one reader must be to count as one.
const DedupeWindow = 5 * time.Second

// IngestRFID decodes and stores a batch of reads. A read with an unreadable EPC is rejected and the rest stored. A
// read of the same tag at the same reader within DedupeWindow of one held (earlier in the batch, in the store, or in a
// batch from the same reader committed meanwhile) is a duplicate. A tag that is an SSCC the warehouse labelled names its receipt and pallet.
//
// A stored read then runs the rules of its reader (rfid_rule): receive the ASN pallet a dock portal read, or confirm
// the put-away of the pallet read at its destination. A repeat read runs nothing.
func (s *Store) IngestRFID(ctx context.Context, reads []RFIDRead) (RFIDIngest, error) {
	out := RFIDIngest{Rejected: []RFIDRejected{}, Reads: []RFIDStored{}}
	var claims undoLog
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		out, claims = RFIDIngest{Rejected: []RFIDRejected{}, Reads: []RFIDStored{}}, undoLog{}
		rules, err := rulesOf(ctx, tx, "")
		if err != nil {
			return err
		}
		// A duplicate is a read within a sliding window of another, which no unique key can hold, so the check and the
		// insert run under a lock per reader: two batches from one reader (a portal resending) cannot both store a read.
		// The readers are locked in order, so two batches naming the same readers cannot deadlock.
		readers := make([]string, 0, len(reads))
		for _, rd := range reads {
			readers = append(readers, rd.ReaderID)
		}
		slices.Sort(readers)
		for _, r := range slices.Compact(readers) {
			if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('warehouse.rfid_read:' || $1::text || ':' || $2::text, 0))`, tenantID(ctx), r); err != nil {
				return err
			}
		}
		type seenKey struct{ reader, epc string }
		seen := map[seenKey][]time.Time{}
		for i, rd := range reads {
			epc, err := domain.DecodeEPC(rd.EPC)
			if err != nil {
				var de *domain.Error
				if errors.As(err, &de) {
					out.Rejected = append(out.Rejected, RFIDRejected{Index: i, Code: de.Code, Reason: de.Detail})
					continue
				}
				return err
			}
			hex := strings.ToUpper(strings.TrimSpace(rd.EPC))
			dup := false
			for _, at := range seen[seenKey{rd.ReaderID, hex}] {
				if absDuration(at.Sub(rd.ReadAt)) <= DedupeWindow {
					dup = true
				}
			}
			if !dup {
				if err := tx.QueryRow(ctx, `select exists (select 1 from rfid_read where reader_id = $1 and epc_hex = $2 and read_at between $3 and $4)`,
					rd.ReaderID, hex, rd.ReadAt.Add(-DedupeWindow), rd.ReadAt.Add(DedupeWindow)).Scan(&dup); err != nil {
					return err
				}
			}
			if dup {
				out.Duplicates++
				continue
			}
			seen[seenKey{rd.ReaderID, hex}] = append(seen[seenKey{rd.ReaderID, hex}], rd.ReadAt)
			id := ids.New()
			if _, err := tx.Exec(ctx, `insert into rfid_read (tenant_id, id, reader_id, antenna, location_code, epc_hex, scheme, gs1_key, serial, rssi, read_at, posted_by)
				values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, tenantID(ctx), id, rd.ReaderID, rd.Antenna, rd.LocationCode, hex, epc.Scheme, epc.Key, epc.Serial, rd.RSSI, rd.ReadAt, rd.PostedBy); err != nil {
				return err
			}
			stored := RFIDStored{Index: i, ID: id, EPC: epc}
			if epc.Scheme == "SSCC-96" {
				err := tx.QueryRow(ctx, `select receipt_id::text, line_no from receipt_line where sscc = $1`, epc.Key).Scan(&stored.ReceiptID, &stored.LineNo)
				if err != nil && !errors.Is(err, pgx.ErrNoRows) {
					return err
				}
			}
			if len(rules) > 0 {
				acts, err := s.actOnRead(ctx, tx, rules, id, rd, epc, time.Now().UTC(), &claims)
				if err != nil {
					return err
				}
				stored.Actions = acts
			}
			out.Reads = append(out.Reads, stored)
			out.Accepted++
		}
		return nil
	})
	if err != nil {
		claims.run()
	}
	return out, err
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// RFIDReadRow is a stored read.
type RFIDReadRow struct {
	ID           string    `json:"id"`
	ReaderID     string    `json:"readerId"`
	Antenna      int       `json:"antenna"`
	LocationCode string    `json:"locationCode,omitempty"`
	EPC          string    `json:"epc"`
	Scheme       string    `json:"scheme"`
	Key          string    `json:"key"`
	Serial       string    `json:"serial,omitempty"`
	RSSI         *int      `json:"rssi,omitempty"`
	ReadAt       time.Time `json:"readAt"`
	// Action, ActionOutcome, ActionDetail and ActionRef are what a rule did with the read (rfid_rule); empty when none ran.
	Action        string `json:"action,omitempty"`
	ActionOutcome string `json:"actionOutcome,omitempty"`
	ActionDetail  string `json:"actionDetail,omitempty"`
	ActionRef     string `json:"actionRef,omitempty"`
}

// RFIDFilter narrows the reads listed; Key is the GS1 key (an SSCC, say), newest first.
type RFIDFilter struct {
	Key      string
	ReaderID string
	From, To time.Time
	Limit    int
}

// ListRFIDReads returns stored reads, newest first.
func (s *Store) ListRFIDReads(ctx context.Context, f RFIDFilter) ([]RFIDReadRow, error) {
	out := []RFIDReadRow{}
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id::text, reader_id, antenna, location_code, epc_hex, scheme, gs1_key, serial, rssi, read_at, action, action_outcome, action_detail, action_ref from rfid_read
			where ($1 = '' or gs1_key = $1) and ($2 = '' or reader_id = $2) and ($3::timestamptz is null or read_at >= $3) and ($4::timestamptz is null or read_at < $4)
			order by read_at desc, id limit $5`, f.Key, f.ReaderID, nullTime(f.From), nullTime(f.To), f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r RFIDReadRow
			if err := rows.Scan(&r.ID, &r.ReaderID, &r.Antenna, &r.LocationCode, &r.EPC, &r.Scheme, &r.Key, &r.Serial, &r.RSSI, &r.ReadAt, &r.Action, &r.ActionOutcome, &r.ActionDetail, &r.ActionRef); err != nil {
				return err
			}
			r.ReadAt = r.ReadAt.UTC()
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// Automation order statuses.
const (
	OrderPending   = "PENDING"
	OrderSent      = "SENT"
	OrderRefused   = "REFUSED"
	OrderArrived   = "ARRIVED"
	OrderDiverted  = "DIVERTED"
	OrderFault     = "FAULT"
	OrderCancelled = "CANCELLED"
)

// Codes of automation orders.
const (
	CodeAutoOrderNotFound = "WMS-AUTO-ORDER-NOT-FOUND" // 404
	CodeAutoOrderExists   = "WMS-AUTO-ORDER-EXISTS"    // 409: the line has an order already
	CodeAutoOrderState    = "WMS-AUTO-ORDER-STATE"     // 409: the order is not in a state the report needs
	CodeAutoLocation      = "WMS-AUTO-LOCATION"        // 422: a location the telegram cannot name
	CodeAutoLoadUnit      = "WMS-AUTO-LOAD-UNIT"       // 422: the line names no pallet SSCC
)

// AutomationOrder is one move the warehouse asks a PLC to make.
type AutomationOrder struct {
	ID           string     `json:"id"`
	WorkID       string     `json:"workId"`
	LineNo       int        `json:"lineNo"`
	Station      string     `json:"station"`
	LoadUnit     string     `json:"loadUnit"`
	FromLocation string     `json:"fromLocation"`
	ToLocation   string     `json:"toLocation"`
	Priority     int        `json:"priority"`
	Status       string     `json:"status"`
	Reason       string     `json:"reason,omitempty"`
	ConfirmError string     `json:"confirmError,omitempty"`
	CreatedBy    string     `json:"createdBy"`
	CreatedAt    time.Time  `json:"createdAt"`
	SentAt       *time.Time `json:"sentAt,omitempty"`
	DoneAt       *time.Time `json:"doneAt,omitempty"`
}

const orderCols = `id::text, work_id::text, line_no, station, load_unit, from_location, to_location, priority, status, reason, confirm_error, created_by, created_at, sent_at, done_at`

func scanOrder(row pgx.Row) (AutomationOrder, error) {
	var o AutomationOrder
	err := row.Scan(&o.ID, &o.WorkID, &o.LineNo, &o.Station, &o.LoadUnit, &o.FromLocation, &o.ToLocation, &o.Priority, &o.Status, &o.Reason, &o.ConfirmError, &o.CreatedBy, &o.CreatedAt, &o.SentAt, &o.DoneAt)
	return o, err
}

// OrderInput creates an order from a transport work line. LoadUnit may be left out: the line's sscc attribute (the
// pallet the receipt labelled) is used.
type OrderInput struct {
	WorkID   string
	LineNo   int
	Station  string
	LoadUnit string
	// FromLocation and ToLocation name the telegram's two locations when the line has only one: a put-away line has
	// the shelf it goes to and no source (the PLC's infeed point is the source), a pick line the reverse (the outfeed).
	FromLocation string
	ToLocation   string
	User         string
	Now          time.Time
}

// TelegramPriority maps a work priority (1 highest to 99) onto a telegram's 1 to 9.
func TelegramPriority(workPriority int) int {
	p := 1 + (workPriority-1)/11
	return min(max(p, 1), 9)
}

// CreateAutomationOrder makes the move of one line of a work an order for a PLC station: the load unit's SSCC and the
// location names the telegram carries (12 characters at most). The work is assigned to the station's automation
// identity, so the PLC's arrival confirms the line as that identity and stock moves then. Only a work that is OPEN, or
// already assigned to that station, can be given to a PLC.
func (s *Store) CreateAutomationOrder(ctx context.Context, in OrderInput) (AutomationOrder, error) {
	var out AutomationOrder
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		w, err := readWork(ctx, tx, in.WorkID, true)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		line, err := w.Line(in.LineNo)
		if err != nil {
			return err
		}
		if line.Status != domain.LineOpen {
			return &domain.Error{Code: domain.CodeIllegalTransition, Detail: fmt.Sprintf("line %d is %s", in.LineNo, line.Status)}
		}
		unit := strings.TrimSpace(in.LoadUnit)
		if unit == "" {
			unit = line.Attributes["sscc"]
		}
		if !domain.ValidSSCC(unit) {
			return &domain.Error{Code: CodeAutoLoadUnit, Detail: "the line names no pallet SSCC: give loadUnit, or receive the pallet first so its label carries one"}
		}
		var from, to string
		for _, ref := range []struct {
			id    string
			given string
			dst   *string
			what  string
		}{{line.FromLocationID, strings.TrimSpace(in.FromLocation), &from, "from"}, {line.ToLocationID, strings.TrimSpace(in.ToLocation), &to, "to"}} {
			switch {
			case ref.given != "":
				*ref.dst = ref.given
			case ref.id != "":
				if err := tx.QueryRow(ctx, `select name from location where id = $1`, ref.id).Scan(ref.dst); err != nil {
					return err
				}
			default:
				return &domain.Error{Code: CodeAutoLocation, Detail: fmt.Sprintf("the line has no %s location: give %sLocation, the PLC's infeed or outfeed point", ref.what, ref.what)}
			}
			if len(*ref.dst) > 12 {
				return &domain.Error{Code: CodeAutoLocation, Detail: fmt.Sprintf("the location %q is longer than the 12 characters a telegram carries", *ref.dst)}
			}
		}
		if from == to {
			return &domain.Error{Code: CodeAutoLocation, Detail: "a move needs two different locations"}
		}
		identity := AutomationUser(in.Station)
		switch {
		case w.Status == domain.WorkOpen:
			if err := w.Assign(identity, in.Now); err != nil {
				return err
			}
			if err := updateWork(ctx, tx, &w); err != nil {
				return err
			}
		case (w.Status == domain.WorkAssigned || w.Status == domain.WorkInProgress) && w.AssignedUser == identity:
		default:
			return &domain.Error{Code: domain.CodeIllegalTransition, Detail: fmt.Sprintf("a %s work assigned to %q cannot be given to PLC station %s", w.Status, w.AssignedUser, in.Station)}
		}
		out, err = scanOrder(tx.QueryRow(ctx, `insert into automation_order (tenant_id, id, work_id, line_no, station, load_unit, from_location, to_location, priority, status, created_by)
			values ($1,$2,$3,$4,$5,$6,$7,$8,$9,'PENDING',$10) returning `+orderCols,
			tenantID(ctx), ids.New(), in.WorkID, in.LineNo, in.Station, unit, from, to, TelegramPriority(w.Priority), in.User))
		if err != nil {
			if mapErr(err) == ErrConflict {
				return &domain.Error{Code: CodeAutoOrderExists, Detail: fmt.Sprintf("line %d of work %s has an automation order already", in.LineNo, in.WorkID)}
			}
			return err
		}
		return nil
	})
	return out, err
}

// AutomationUser is the identity a PLC station's work runs as.
func AutomationUser(station string) string { return "automation:" + station }

// OrderFilter narrows the orders listed.
type OrderFilter struct {
	Station string
	Status  string
	Limit   int
}

// ListAutomationOrders returns orders oldest first (the PLC works them in that order).
func (s *Store) ListAutomationOrders(ctx context.Context, f OrderFilter) ([]AutomationOrder, error) {
	out := []AutomationOrder{}
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+orderCols+` from automation_order where ($1 = '' or station = $1) and ($2 = '' or status = $2)
			order by created_at, id limit $3`, f.Station, f.Status, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			o, err := scanOrder(rows)
			if err != nil {
				return err
			}
			out = append(out, o)
		}
		return rows.Err()
	})
	return out, err
}

// ReportAutomationOrder records what the connector did with a PENDING order: sent as a MOVE telegram, or refused (with
// the PLC's reason). Reporting the status it already has changes nothing.
func (s *Store) ReportAutomationOrder(ctx context.Context, id, status, reason string, now time.Time) (AutomationOrder, error) {
	var out AutomationOrder
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		cur, err := scanOrder(tx.QueryRow(ctx, `select `+orderCols+` from automation_order where id = $1 for update`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return &domain.Error{Code: CodeAutoOrderNotFound, Detail: "automation order not found"}
		}
		if err != nil {
			return err
		}
		if cur.Status == status {
			out = cur
			return nil
		}
		if cur.Status != OrderPending {
			return &domain.Error{Code: CodeAutoOrderState, Detail: fmt.Sprintf("the order is %s; only a PENDING order is reported sent or refused", cur.Status)}
		}
		out, err = scanOrder(tx.QueryRow(ctx, `update automation_order set status = $2, reason = $3, sent_at = case when $2 = 'SENT' then $4 else sent_at end,
			done_at = case when $2 = 'REFUSED' then $4 else done_at end
			where id = $1 returning `+orderCols, id, status, reason, now))
		if err != nil || status != OrderRefused {
			return err
		}
		// The PLC will not make the move: the work goes back to people, as after a fault.
		return releaseFromStation(ctx, tx, cur.Station, cur.WorkID, cur.LineNo, "refused by the PLC: "+orDefault(reason, "no reason given"))
	})
	return out, err
}

// AutomationEvent is what a PLC reported (the payload of dev.passage.warehouse.automation.event.v1).
type AutomationEvent struct {
	Kind string
	// Session names the connector's link session to the PLC. The PLC numbers its telegrams per session (from 1 again
	// after the connector dials it again), so a sequence number alone does not name an event.
	Session  string
	Seq      int
	LoadUnit string
	Location string
	Lane     string
	Code     string
	Text     string
	At       time.Time
}

// AutomationIngest is the outcome of a batch of PLC events.
type AutomationIngest struct {
	Accepted   int `json:"accepted"`
	Duplicates int `json:"duplicates"`
	// Confirmed is how many transport work lines an arrival confirmed.
	Confirmed int `json:"confirmed"`
	// Unmatched is how many events named a load unit with no order sent to that station.
	Unmatched int `json:"unmatched"`
}

// EventDedupeWindow is how long a (station, session, seq) is remembered. A telegram the PLC repeats (the connector's
// answer was lost) comes back within seconds; the same number in the same session after the window is the sequence
// wrapping after 99999, a new event.
const EventDedupeWindow = 10 * time.Minute

// IngestAutomationEvents stores the events of a station once each: a repeat is the same session and sequence within
// EventDedupeWindow. An arrival of a load unit with an order SENT to the station marks the order ARRIVED and confirms
// its work line as the station's identity in the same transaction, so the event, the status and the stock move commit
// together or not at all; a divert marks it DIVERTED, a fault FAULT with the PLC's code. A confirmation the warehouse
// refuses (the location is full, the stock is not there) leaves the order ARRIVED with the reason in confirm_error,
// for a person. Any other failure fails the whole batch: nothing is stored, the connector answers the PLC busy and
// the PLC sends the telegram again.
func (s *Store) IngestAutomationEvents(ctx context.Context, station string, events []AutomationEvent, now time.Time) (AutomationIngest, error) {
	var out AutomationIngest
	var claims undoLog
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		out, claims = AutomationIngest{}, undoLog{}
		// One batch per station at a time, so a repeat posted while the first is still running is seen as one.
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('warehouse.automation_event:' || $1::text || ':' || $2::text, 0))`,
			tenantID(ctx), station); err != nil {
			return err
		}
		for _, e := range events {
			var dup bool
			if err := tx.QueryRow(ctx, `select exists (select 1 from automation_event where station = $1 and session = $2 and seq = $3 and received_at > $4)`,
				station, e.Session, e.Seq, now.Add(-EventDedupeWindow)).Scan(&dup); err != nil {
				return err
			}
			if dup {
				out.Duplicates++
				continue
			}
			out.Accepted++
			var orderID, workID string
			var lineNo int
			if e.LoadUnit != "" {
				err := tx.QueryRow(ctx, `select id::text, work_id::text, line_no from automation_order where station = $1 and load_unit = $2 and status = 'SENT'
					order by sent_at desc limit 1 for update`, station, e.LoadUnit).Scan(&orderID, &workID, &lineNo)
				switch {
				case errors.Is(err, pgx.ErrNoRows):
					out.Unmatched++
				case err != nil:
					return err
				}
			}
			var order any
			if orderID != "" {
				order = orderID
			}
			if _, err := tx.Exec(ctx, `insert into automation_event (tenant_id, id, station, session, seq, kind, load_unit, location, lane, code, text, at, received_at, order_id)
				values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`, tenantID(ctx), ids.New(), station, e.Session, e.Seq, e.Kind, e.LoadUnit, e.Location, e.Lane, e.Code, e.Text, e.At, now, order); err != nil {
				return err
			}
			if orderID == "" {
				continue
			}
			newStatus := map[string]string{"arrived": OrderArrived, "diverted": OrderDiverted, "fault": OrderFault}[e.Kind]
			reason := ""
			if e.Kind == "fault" {
				reason = strings.TrimSpace(e.Code + " " + e.Text)
			}
			if _, err := tx.Exec(ctx, `update automation_order set status = $2, reason = $3, done_at = $4 where id = $1`, orderID, newStatus, reason, e.At); err != nil {
				return err
			}
			switch e.Kind {
			case "fault":
				if err := releaseFromStation(ctx, tx, station, workID, lineNo, "PLC fault: "+orDefault(reason, "no code")); err != nil {
					return err
				}
			case "diverted":
				note := "diverted by the PLC"
				if e.Lane != "" {
					note += " to lane " + e.Lane
				}
				if e.Location != "" {
					note += " at " + e.Location
				}
				if e.Text != "" {
					note += ": " + e.Text
				}
				if err := releaseFromStation(ctx, tx, station, workID, lineNo, note); err != nil {
					return err
				}
			}
			if e.Kind == "arrived" {
				confirmed, err := s.confirmArrival(ctx, tx, station, orderID, workID, lineNo, now, &claims)
				if err != nil {
					return err
				}
				if confirmed {
					out.Confirmed++
				}
			}
		}
		return nil
	})
	if err != nil {
		claims.run()
		return AutomationIngest{}, err
	}
	return out, nil
}

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

// releaseFromStation gives the work of an order that will not complete (a fault, a divert, a cancelled order) back to
// people: the line notes why, and a work still ASSIGNED to the station with no other live order there returns to OPEN,
// so the next person who asks for work can move the pallet by hand. A work the station already confirmed a line of is
// IN_PROGRESS and stays with the station; its line can be given to the PLC again with a new order.
func releaseFromStation(ctx context.Context, tx pgx.Tx, station, workID string, lineNo int, note string) error {
	w, err := readWork(ctx, tx, workID, true)
	if err != nil {
		return err
	}
	if err := w.NoteLine(lineNo, note); err != nil {
		return err
	}
	var live bool
	if err := tx.QueryRow(ctx, `select exists (select 1 from automation_order where work_id = $1 and station = $2 and status in ('PENDING', 'SENT'))`,
		workID, station).Scan(&live); err != nil {
		return err
	}
	if !live && w.Status == domain.WorkAssigned && w.AssignedUser == AutomationUser(station) {
		if err := w.Release(AutomationUser(station)); err != nil {
			return err
		}
	}
	return updateWork(ctx, tx, &w)
}

// CancelInput cancels an automation order. PLCCleared says a person has made sure the PLC no longer holds the move:
// an order already SENT is in the PLC's hands, and cancelling it here alone would let the PLC move a pallet the
// warehouse no longer expects.
type CancelInput struct {
	ID         string
	Reason     string
	User       string
	PLCCleared bool
	Now        time.Time
}

// CancelAutomationOrder cancels an order that has not arrived. A REFUSED, FAULT or DIVERTED order is cancelled at once;
// a PENDING or SENT one only with PLCCleared, since the connector may have sent the MOVE before reporting it. Its work goes back to people as after a fault. Cancelling a cancelled order
// changes nothing; an ARRIVED order is done and cannot be cancelled.
func (s *Store) CancelAutomationOrder(ctx context.Context, in CancelInput) (AutomationOrder, error) {
	var out AutomationOrder
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		cur, err := scanOrder(tx.QueryRow(ctx, `select `+orderCols+` from automation_order where id = $1 for update`, in.ID))
		if errors.Is(err, pgx.ErrNoRows) {
			return &domain.Error{Code: CodeAutoOrderNotFound, Detail: "automation order not found"}
		}
		if err != nil {
			return err
		}
		switch cur.Status {
		case OrderCancelled:
			out = cur
			return nil
		case OrderArrived:
			return &domain.Error{Code: CodeAutoOrderState, Detail: "the order has arrived; its move is done"}
		case OrderSent, OrderPending:
			// A PENDING order may already be in the PLC's hands: the connector pulls it, sends the MOVE and only then
			// reports it sent. Either way only a person who cleared the move on the PLC may cancel it.
			if !in.PLCCleared {
				return &domain.Error{Code: CodeAutoOrderState, Detail: "the order may already be with the PLC (the connector sends the MOVE before it reports it): clear the move on the PLC first, then cancel it with plcCleared"}
			}
		}
		out, err = scanOrder(tx.QueryRow(ctx, `update automation_order set status = 'CANCELLED', reason = $2, cancelled_by = $3, done_at = $4
			where id = $1 returning `+orderCols, in.ID, in.Reason, in.User, in.Now))
		if err != nil {
			return err
		}
		return releaseFromStation(ctx, tx, cur.Station, cur.WorkID, cur.LineNo, "automation order cancelled: "+in.Reason)
	})
	return out, err
}

// confirmArrival confirms the work line of an order that just turned ARRIVED, inside the ingest's transaction and
// under a savepoint. The confirmation is keyed by the order: the SENT to ARRIVED change, made under the order's row
// lock, already happens once per order. A refusal rolls back to the savepoint and records the reason on the order;
// it returns false and no error. Any other failure is returned and fails the batch.
func (s *Store) confirmArrival(ctx context.Context, tx pgx.Tx, station, orderID, workID string, lineNo int, now time.Time, claims *undoLog) (bool, error) {
	var qty string
	if err := tx.QueryRow(ctx, `select qty::text from work_line where work_id = $1 and line_no = $2`, workID, lineNo).Scan(&qty); err != nil {
		return false, err
	}
	q, err := dec(qty)
	if err != nil {
		return false, err
	}
	sp, err := tx.Begin(ctx) // a savepoint
	if err != nil {
		return false, err
	}
	var mine undoLog
	_, _, cerr := s.confirmLineTx(ctx, sp, ArrivalIdem(orderID), ConfirmInput{WorkID: workID, LineNo: lineNo, Qty: q, User: AutomationUser(station), Now: now}, &mine)
	if cerr == nil {
		if err := sp.Commit(ctx); err != nil {
			mine.run()
			return false, err
		}
		claims.fns = append(claims.fns, mine.fns...)
		return true, nil
	}
	mine.run()
	if err := sp.Rollback(ctx); err != nil {
		return false, err
	}
	if !confirmRefused(cerr) {
		return false, cerr
	}
	msg := cerr.Error()
	if len(msg) > 500 {
		msg = msg[:500]
	}
	_, err = tx.Exec(ctx, `update automation_order set confirm_error = $2 where id = $1`, orderID, msg)
	return false, err
}

// ArrivalIdem is the idempotency key of the confirmation an order's arrival makes.
func ArrivalIdem(orderID string) Idem {
	return Idem{Key: "automation-arrival:" + orderID, Hash: "automation-arrival"}
}

// confirmRefused tells a confirmation the warehouse refused, which a person has to look at, from a failure that may
// pass when the PLC sends the telegram again (the database or inventory being away).
func confirmRefused(err error) bool {
	var de *domain.Error
	var pr *httpx.Problem
	switch {
	case errors.As(err, &de):
		return true
	case errors.As(err, &pr):
		return pr.Status < 500
	}
	return errors.Is(err, ErrNotFound) || errors.Is(err, pgx.ErrNoRows) || errors.Is(err, ErrIdempotencyReuse) || errors.Is(err, ErrVersionConflict)
}
