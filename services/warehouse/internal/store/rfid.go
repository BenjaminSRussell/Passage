package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/warehouse/internal/domain"
)

// RFID rules (#32, FR-32.9): what a read at a reader does. A dock-door portal that reads a pallet's SSCC receives the
// ASN pallet; a reader at a location confirms the put-away of the pallet that arrived there.

// Actions of an RFID rule.
const (
	RFIDReceivePallet  = "RECEIVE_PALLET"
	RFIDConfirmPutaway = "CONFIRM_PUTAWAY"
)

// Outcomes of a rule on a read.
const (
	RFIDDone    = "DONE"    // the pallet was received or the put-away confirmed
	RFIDSkipped = "SKIPPED" // nothing to do: not a pallet tag, no expected pallet, no open put-away to that location
	RFIDRefused = "REFUSED" // the warehouse refused the receipt or confirmation (capacity, state); a person looks at it
)

// Codes of RFID rules.
const (
	CodeRFIDRuleExists   = "WMS-RFID-RULE-EXISTS"    // 409: the reader (antenna) has a rule of that action already
	CodeRFIDRuleNotFound = "WMS-RFID-RULE-NOT-FOUND" // 404
)

// RFIDRule is one rule of a reader. Antenna nil matches every antenna of the reader. WarehouseID limits a receive to
// the receipts of that warehouse; LocationCode is the location a put-away must be bound for (the read's own location
// code when empty).
type RFIDRule struct {
	ID           string    `json:"id"`
	ReaderID     string    `json:"readerId"`
	Antenna      *int      `json:"antenna,omitempty"`
	Action       string    `json:"action"`
	WarehouseID  string    `json:"warehouseId,omitempty"`
	LocationCode string    `json:"locationCode,omitempty"`
	CreatedBy    string    `json:"createdBy"`
	CreatedAt    time.Time `json:"createdAt"`
}

const ruleCols = `id::text, reader_id, antenna, action, warehouse_id, location_code, created_by, created_at`

func scanRule(row pgx.Row) (RFIDRule, error) {
	var r RFIDRule
	err := row.Scan(&r.ID, &r.ReaderID, &r.Antenna, &r.Action, &r.WarehouseID, &r.LocationCode, &r.CreatedBy, &r.CreatedAt)
	r.CreatedAt = r.CreatedAt.UTC()
	return r, err
}

// CreateRFIDRule stores a rule; a reader (antenna) has at most one rule per action.
func (s *Store) CreateRFIDRule(ctx context.Context, r RFIDRule) (RFIDRule, error) {
	var out RFIDRule
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = scanRule(tx.QueryRow(ctx, `insert into rfid_rule (tenant_id, id, reader_id, antenna, action, warehouse_id, location_code, created_by)
			values ($1,$2,$3,$4,$5,$6,$7,$8) returning `+ruleCols, tenantID(ctx), ids.New(), r.ReaderID, r.Antenna, r.Action, r.WarehouseID, r.LocationCode, r.CreatedBy))
		if err != nil && mapErr(err) == ErrConflict {
			return &domain.Error{Code: CodeRFIDRuleExists, Detail: fmt.Sprintf("reader %s has a %s rule for that antenna already", r.ReaderID, r.Action)}
		}
		return err
	})
	return out, err
}

// ListRFIDRules returns the rules, of one reader when readerID is given.
func (s *Store) ListRFIDRules(ctx context.Context, readerID string) ([]RFIDRule, error) {
	out := []RFIDRule{}
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = rulesOf(ctx, tx, readerID)
		return err
	})
	return out, err
}

func rulesOf(ctx context.Context, tx pgx.Tx, readerID string) ([]RFIDRule, error) {
	out := []RFIDRule{}
	rows, err := tx.Query(ctx, `select `+ruleCols+` from rfid_rule where ($1 = '' or reader_id = $1) order by reader_id, antenna nulls first, action`, readerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteRFIDRule removes a rule; reads already acted on keep what was done.
func (s *Store) DeleteRFIDRule(ctx context.Context, id string) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `delete from rfid_rule where id = $1`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return &domain.Error{Code: CodeRFIDRuleNotFound, Detail: "RFID rule not found"}
		}
		return nil
	})
}

// RFIDAction is what one rule did with a read.
type RFIDAction struct {
	RuleID    string `json:"ruleId"`
	Action    string `json:"action"`
	Outcome   string `json:"outcome"`
	Detail    string `json:"detail,omitempty"`
	ReceiptID string `json:"receiptId,omitempty"`
	LineNo    int    `json:"lineNo,omitempty"`
	WorkID    string `json:"workId,omitempty"`
}

// RFIDUser is the identity a reader's actions run as.
func RFIDUser(reader string) string { return "rfid:" + reader }

func (r RFIDRule) matches(rd RFIDRead) bool {
	return r.ReaderID == rd.ReaderID && (r.Antenna == nil || *r.Antenna == rd.Antenna)
}

// actOnRead runs the reader's rules on a read that was just stored, each under a savepoint: a refusal rolls back only
// that rule's work and is recorded on the read; any other failure fails the batch.
func (s *Store) actOnRead(ctx context.Context, tx pgx.Tx, rules []RFIDRule, readID string, rd RFIDRead, epc domain.EPC, now time.Time, claims *undoLog) ([]RFIDAction, error) {
	var out []RFIDAction
	for _, rule := range rules {
		if !rule.matches(rd) {
			continue
		}
		act := RFIDAction{RuleID: rule.ID, Action: rule.Action}
		if epc.Scheme != "SSCC-96" {
			act.Outcome, act.Detail = RFIDSkipped, "the tag is a "+epc.Scheme+", not a pallet's SSCC"
			out = append(out, act)
			continue
		}
		sp, err := tx.Begin(ctx)
		if err != nil {
			return nil, err
		}
		var mine undoLog
		switch rule.Action {
		case RFIDReceivePallet:
			err = receiveByRead(ctx, sp, rule, rd, epc.Key, now, &act)
		case RFIDConfirmPutaway:
			err = s.confirmPutawayByRead(ctx, sp, rule, readID, rd, epc.Key, now, &act, &mine)
		default:
			err = fmt.Errorf("store: unknown RFID action %q", rule.Action)
		}
		if err == nil {
			if err := sp.Commit(ctx); err != nil {
				mine.run()
				return nil, err
			}
			claims.fns = append(claims.fns, mine.fns...)
			out = append(out, act)
			continue
		}
		mine.run()
		if rerr := sp.Rollback(ctx); rerr != nil {
			return nil, rerr
		}
		if !confirmRefused(err) {
			return nil, err
		}
		act.Outcome, act.Detail = RFIDRefused, truncate(err.Error(), 500)
		out = append(out, act)
	}
	if len(out) > 0 {
		// The read keeps the first action that did something, else the first one tried.
		keep := out[0]
		for _, a := range out {
			if a.Outcome == RFIDDone {
				keep = a
				break
			}
		}
		ref := keep.ReceiptID
		if keep.WorkID != "" {
			ref = keep.WorkID
		}
		if _, err := tx.Exec(ctx, `update rfid_read set action = $2, action_outcome = $3, action_detail = $4, action_ref = $5 where id = $1`,
			readID, keep.Action, keep.Outcome, keep.Detail, ref); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// receiveByRead receives the expected ASN pallet whose SSCC (or license plate) the dock portal read, at the quantity
// the ASN announced: the portal saw the whole pallet, and a count that differs is a person's correction afterwards.
func receiveByRead(ctx context.Context, tx pgx.Tx, rule RFIDRule, rd RFIDRead, sscc string, now time.Time, act *RFIDAction) error {
	var receiptID string
	var lineNo int
	var expected string
	err := tx.QueryRow(ctx, `select l.receipt_id::text, l.line_no, l.expected_qty::text from receipt_line l join receipt r on r.id = l.receipt_id
		where (l.sscc = $1 or l.lp_id = $1) and l.status = 'EXPECTED' and ($2 = '' or r.warehouse_id::text = $2)
		order by r.created_at limit 1`, sscc, rule.WarehouseID).Scan(&receiptID, &lineNo, &expected)
	if errors.Is(err, pgx.ErrNoRows) {
		act.Outcome, act.Detail = RFIDSkipped, "no expected ASN pallet has this SSCC"
		return nil
	}
	if err != nil {
		return err
	}
	qty, err := dec(expected)
	if err != nil {
		return err
	}
	res, err := receiveLineTx(ctx, tx, ReceiveInput{ReceiptID: receiptID, LineNo: lineNo, Qty: qty, User: RFIDUser(rd.ReaderID), Now: now})
	if err != nil {
		return err
	}
	act.Outcome, act.ReceiptID, act.LineNo, act.WorkID = RFIDDone, receiptID, lineNo, res.Work.ID
	act.Detail = fmt.Sprintf("received pallet %d of the receipt at the announced %s", lineNo, qty)
	return nil
}

// confirmPutawayByRead confirms the open put-away line of the pallet that was read at the rule's location (or the
// read's location): the reader is the proof that the pallet arrived. An OPEN work is taken by the reader's identity;
// a work a person holds is confirmed for that person, who brought the pallet.
func (s *Store) confirmPutawayByRead(ctx context.Context, tx pgx.Tx, rule RFIDRule, readID string, rd RFIDRead, sscc string, now time.Time, act *RFIDAction, claims *undoLog) error {
	location := rule.LocationCode
	if location == "" {
		location = rd.LocationCode
	}
	if location == "" {
		act.Outcome, act.Detail = RFIDSkipped, "the rule and the read name no location"
		return nil
	}
	var workID string
	var lineNo int
	err := tx.QueryRow(ctx, `select l.work_id::text, l.line_no from work_line l join work w on w.id = l.work_id join location loc on loc.id = l.to_location_id
		where l.attributes->>'sscc' = $1 and l.status = 'OPEN' and l.direction = 'PUT' and loc.name = $2
		  and w.status in ('OPEN', 'ASSIGNED', 'IN_PROGRESS') and ($3 = '' or w.warehouse_id::text = $3)
		order by w.created_at limit 1`, sscc, location, rule.WarehouseID).Scan(&workID, &lineNo)
	if errors.Is(err, pgx.ErrNoRows) {
		act.Outcome, act.Detail = RFIDSkipped, "no open put-away of this pallet is bound for "+location
		return nil
	}
	if err != nil {
		return err
	}
	w, err := readWork(ctx, tx, workID, true)
	if err != nil {
		return err
	}
	user := w.AssignedUser
	if w.Status == domain.WorkOpen {
		user = RFIDUser(rd.ReaderID)
		if err := w.Assign(user, now); err != nil {
			return err
		}
		if err := updateWork(ctx, tx, &w); err != nil {
			return err
		}
	}
	line, err := w.Line(lineNo)
	if err != nil {
		return err
	}
	if _, _, err := s.confirmLineTx(ctx, tx, Idem{Key: "rfid-putaway:" + readID, Hash: "rfid-putaway"}, ConfirmInput{WorkID: workID, LineNo: lineNo, Qty: line.Qty, User: user, Now: now}, claims); err != nil {
		return err
	}
	act.Outcome, act.WorkID, act.LineNo = RFIDDone, workID, lineNo
	act.Detail = fmt.Sprintf("put-away line %d confirmed at %s for %s", lineNo, location, strings.TrimPrefix(user, "rfid:"))
	return nil
}
