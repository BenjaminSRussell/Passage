// Package replay applies a device's queued offline transactions to the
// warehouse in seq order, exactly once (BR-32.3, BR-32.4). A batch is recorded
// first, then the device's pending transactions are applied one by one; the
// first conflict stops the run and leaves the rest PENDING. Each
// transaction reaches the warehouse with its own key as Idempotency-Key, so a
// crash or a repeated replay never repeats an effect there.
package replay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/mobile/internal/session"
	"github.com/BenjaminSRussell/passage/services/mobile/internal/store"
	"github.com/BenjaminSRussell/passage/services/mobile/internal/warehouse"
)

const (
	CodeGap        = "MOB-0108"
	CodeSkew       = "MOB-0114"
	CodeUnmapped   = "MOB-0118"
	CodeNotOwner   = "MOB-NOT-OWNER"
	MaxBatch       = 500
	StatusGap      = "GAP"
	StatusReused   = "KEY_REUSED"
	StatusNotOwner = "NOT_OWNER"
	defaultReason  = "the target changed since the device captured it"
)

var ErrBatchTooLarge = errors.New("replay: batch is too large")

// WarehouseError is a failure to reach the warehouse; what it left unapplied
// stays PENDING for the next replay.
type WarehouseError struct{ error }

func (e WarehouseError) Unwrap() error { return e.error }

// SkewError blocks replay when the device clock is too far off (MOB-0114).
type SkewError struct{ Skew time.Duration }

func (e *SkewError) Error() string {
	return fmt.Sprintf("device clock skew %d s exceeds %d s; replay blocked", int(e.Skew.Seconds()), int(session.MaxSkew.Seconds()))
}

type Ledger interface {
	Record(ctx context.Context, in store.OfflineIn) (store.OfflineTx, error)
	NextPending(ctx context.Context, deviceID string) (*store.OfflineTx, error)
	Settle(ctx context.Context, key, status, reason string) (store.OfflineTx, error)
	GetOffline(ctx context.Context, key string) (store.OfflineTx, error)
	DeviceSkew(ctx context.Context, deviceID string) (time.Duration, error)
}

type Runner struct {
	Ledger    Ledger
	Warehouse warehouse.Caller
}

type Item struct {
	UserID     string
	Seq        int64
	Command    string
	Payload    json.RawMessage
	CapturedAt time.Time
}

type Result struct {
	IdempotencyKey string `json:"idempotencyKey"`
	Seq            int64  `json:"seq"`
	Status         string `json:"status"`
	Code           string `json:"code,omitempty"`
	Reason         string `json:"reason,omitempty"`
}

// Run records items and applies the device's pending transactions. The
// results follow items in seq order. A transport failure to the warehouse is
// returned as err together with the results so far; what was not applied stays
// PENDING for the next replay.
func (r Runner) Run(ctx context.Context, deviceID, sessionID string, items []Item) ([]Result, error) {
	if len(items) > MaxBatch {
		return nil, ErrBatchTooLarge
	}
	skew, err := r.Ledger.DeviceSkew(ctx, deviceID)
	if err != nil {
		return nil, err
	}
	if session.SkewBlocked(skew) {
		return nil, &SkewError{Skew: skew}
	}
	caller := callerID(ctx)
	items = slices.Clone(items)
	slices.SortFunc(items, func(a, b Item) int {
		switch {
		case a.Seq < b.Seq:
			return -1
		case a.Seq > b.Seq:
			return 1
		}
		return 0
	})
	results := make([]Result, 0, len(items))
	for _, it := range items {
		key := store.OfflineKey(deviceID, it.Seq)
		if it.UserID != "" && it.UserID != caller {
			results = append(results, Result{IdempotencyKey: key, Seq: it.Seq, Status: StatusNotOwner, Code: CodeNotOwner, Reason: "the transaction was queued by another user; keep the queue and sign in as that user"})
			return r.finish(ctx, deviceID, items, results)
		}
		in := store.OfflineIn{DeviceID: deviceID, SessionID: sessionID, Seq: it.Seq, Command: it.Command, Payload: it.Payload, CapturedAt: it.CapturedAt.Add(skew), Hash: hash(it)}
		_, err := r.Ledger.Record(ctx, in)
		if errors.Is(err, store.ErrConflict) {
			_, err = r.Ledger.Record(ctx, in)
		}
		var gap *store.GapError
		switch {
		case errors.As(err, &gap):
			results = append(results, Result{IdempotencyKey: key, Seq: it.Seq, Status: StatusGap, Code: CodeGap, Reason: gap.Error()})
			return r.finish(ctx, deviceID, items, results)
		case errors.Is(err, store.ErrNotOwner):
			results = append(results, Result{IdempotencyKey: key, Seq: it.Seq, Status: StatusNotOwner, Code: CodeNotOwner, Reason: "the key was recorded for another user"})
			return r.finish(ctx, deviceID, items, results)
		case errors.Is(err, store.ErrIdempotencyReuse):
			results = append(results, Result{IdempotencyKey: key, Seq: it.Seq, Status: StatusReused, Reason: "the key was recorded with another command or payload"})
			return r.finish(ctx, deviceID, items, results)
		case err != nil:
			return results, err
		}
	}
	return r.finish(ctx, deviceID, items, results)
}

func (r Runner) finish(ctx context.Context, deviceID string, items []Item, rejected []Result) ([]Result, error) {
	drainErr := r.Drain(ctx, deviceID)
	out := make([]Result, 0, len(items))
	bad := map[int64]Result{}
	for _, x := range rejected {
		bad[x.Seq] = x
	}
	for _, it := range items {
		if x, ok := bad[it.Seq]; ok {
			out = append(out, x)
			continue
		}
		t, err := r.Ledger.GetOffline(ctx, store.OfflineKey(deviceID, it.Seq))
		if err != nil {
			continue
		}
		out = append(out, Result{IdempotencyKey: t.IdempotencyKey, Seq: t.Seq, Status: t.Status, Reason: t.ConflictReason})
	}
	return out, drainErr
}

// Drain applies the device's pending transactions in seq order until none is
// ready, one conflicts, the next was queued by another user, or the warehouse
// cannot be reached. A rejected transaction is closed and does not stop it.
func (r Runner) Drain(ctx context.Context, deviceID string) error {
	caller := callerID(ctx)
	for range MaxBatch * 4 {
		next, err := r.Ledger.NextPending(ctx, deviceID)
		if err != nil || next == nil || next.UserID != caller {
			return err
		}
		status, reason, err := r.apply(ctx, *next)
		if err != nil {
			return err
		}
		if _, err := r.Ledger.Settle(ctx, next.IdempotencyKey, status, reason); err != nil {
			return err
		}
		if status == store.TxConflict {
			return nil
		}
	}
	return nil
}

func (r Runner) apply(ctx context.Context, t store.OfflineTx) (status, reason string, err error) {
	op, params, mapErr := Map(t.Command, t.Payload)
	if mapErr != nil {
		return store.TxRejected, CodeUnmapped + ": " + mapErr.Error(), nil
	}
	out, err := r.Warehouse.Do(ctx, op, t.IdempotencyKey, params)
	if err != nil {
		return "", "", WarehouseError{err}
	}
	switch {
	case out.OK():
		return store.TxApplied, "", nil
	case out.Conflict():
		reason = out.Detail
		if reason == "" {
			reason = defaultReason
		}
		return store.TxConflict, reason, nil
	}
	return store.TxRejected, fmt.Sprintf("%s: %s", out.Code, out.Detail), nil
}

func callerID(ctx context.Context) string {
	p, _ := tenant.FromContext(ctx)
	return p.UserID
}

func hash(it Item) string {
	h := sha256.New()
	h.Write([]byte(it.Command))
	h.Write([]byte{0})
	h.Write(it.Payload)
	return hex.EncodeToString(h.Sum(nil))
}

var commands = map[string]struct {
	op     string
	params map[string][]string
}{
	"ConfirmWorkLine": {"confirm", map[string][]string{"workId": {"workId", "work_id"}, "lineNo": {"lineNo", "line_no"}, "qty": {"qty", "enter_qty", "quantity"}}},
	"ReceiveLine":     {"receive", map[string][]string{"receiptId": {"receiptId", "receipt_id", "scan_receipt"}, "lineNo": {"lineNo", "line_no", "enter_line"}, "qty": {"qty", "enter_qty", "quantity"}}},
	"ReportException": {"exception", map[string][]string{"workId": {"workId", "work_id"}, "action": {"action"}, "lineNo": {"lineNo", "line_no"}, "qty": {"qty", "enter_qty", "confirmedQty"}, "reason": {"reason", "short_reason"}}},
}

// Map turns a queued command and its payload into a warehouse operation. A
// payload carries the parameters at its top level or, as the device's flow
// runner sends it, in its values or fields maps keyed by step id.
func Map(command string, payload json.RawMessage) (string, map[string]string, error) {
	spec, ok := commands[command]
	if !ok {
		return "", nil, fmt.Errorf("command %q is not replayable", command)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(payload, &doc); err != nil {
		return "", nil, errors.New("payload is not a JSON object")
	}
	scopes := []map[string]json.RawMessage{doc}
	for _, nested := range []string{"values", "fields"} {
		var m map[string]json.RawMessage
		if raw, ok := doc[nested]; ok && json.Unmarshal(raw, &m) == nil {
			scopes = append(scopes, m)
		}
	}
	params := map[string]string{}
	for name, aliases := range spec.params {
		for _, scope := range scopes {
			if v := firstString(scope, aliases); v != "" {
				params[name] = v
				break
			}
		}
	}
	for _, required := range requiredFor(spec.op) {
		if params[required] == "" {
			return "", nil, fmt.Errorf("payload has no %s", required)
		}
	}
	return spec.op, params, nil
}

func requiredFor(op string) []string {
	switch op {
	case "confirm":
		return []string{"workId", "lineNo", "qty"}
	case "receive":
		return []string{"receiptId", "lineNo", "qty"}
	}
	return []string{"workId", "action", "reason"}
}

func firstString(m map[string]json.RawMessage, keys []string) string {
	for _, k := range keys {
		raw, ok := m[k]
		if !ok {
			continue
		}
		var s string
		if json.Unmarshal(raw, &s) == nil && s != "" {
			return s
		}
		var n json.Number
		if json.Unmarshal(raw, &n) == nil && n != "" {
			return n.String()
		}
	}
	return ""
}
