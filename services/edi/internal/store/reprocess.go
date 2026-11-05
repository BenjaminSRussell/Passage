package store

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"

	ediev "github.com/BenjaminSRussell/passage/services/edi/internal/events"
)

// ErrNotInbound means the interchange was sent by us; only inbound
// interchanges are reprocessed from their raw payload (an outbound one is
// resent, which needs the transports).
var ErrNotInbound = errors.New("edi: interchange is not inbound")

// ErrUnreadable is what a ReprocessPlan wraps when the raw payload recorded at
// receipt no longer parses (the parser became stricter since).
var ErrUnreadable = errors.New("the stored interchange no longer parses")

// ReprocessPlan maps the failed messages of an interchange again from its raw
// payload. It returns one outcome per message it was given, in the same order,
// with the same ID; Status and Error are the new result and Canonical the new
// document. It must not have side effects: a dry run discards its result.
type ReprocessPlan func(raw string, failed []Message) ([]Message, error)

// ReprocessedMessage is the outcome for one message that was FAILED before.
type ReprocessedMessage struct {
	MessageID      string `json:"messageId"`
	TransactionSet string `json:"transactionSet"`
	STControl      string `json:"stControl"`
	PreviousError  string `json:"previousError,omitempty"`
	Status         string `json:"status"`
	Error          string `json:"error,omitempty"`
	// Changed is false when the new outcome equals the stored one (still
	// FAILED with the same error): nothing is written for it.
	Changed bool `json:"changed"`
}

// ReprocessResult is the result of one reprocess (or its dry run). A replay
// of the same Idempotency-Key returns the stored result.
type ReprocessResult struct {
	InterchangeID string               `json:"interchangeId"`
	DryRun        bool                 `json:"dryRun"`
	Changed       int                  `json:"changed"`
	Messages      []ReprocessedMessage `json:"messages"`
}

// ReprocessInterchange re-runs mapping for the FAILED messages of one inbound
// interchange from its stored raw payload (FR-35.10, runbook "reprocess failed
// interchanges after a map fix"). Received 997s are left alone: their outcome
// is the match against an outbound interchange, not a mapping. The 997 we sent
// for the interchange and its ack_status are never changed.
//
// Interchange and messages are locked for the duration, so two reprocesses
// of the same interchange serialize. A message that now maps becomes MAPPED
// and gets a fresh message.received (status MAPPED) so posting consumers pick
// it up; one that still fails with another error is updated and gets a
// message.failed. A dry run computes the same result and writes nothing; it
// takes no Idempotency-Key. ErrNotFound and ErrNotInbound roll back the key.
func (s *Store) ReprocessInterchange(ctx context.Context, key, requestHash, id string, dryRun bool, plan ReprocessPlan) (ReprocessResult, bool, error) {
	var out ReprocessResult
	replayed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tid, err := tenantID(ctx)
		if err != nil {
			return err
		}
		if !dryRun {
			if replayed, err = claimKey(ctx, tx, tid, key, requestHash, &out); err != nil || replayed {
				return err
			}
		}
		if out, err = reprocessOne(ctx, tx, tid, id, dryRun, plan); err != nil {
			return err
		}
		return storeIfKept(ctx, tx, dryRun, key, out)
	})
	return out, replayed, err
}

// MaxBulkReprocess caps one bulk reprocess (PRD §6.35.6 interaction rules).
const MaxBulkReprocess = 200

// Outcomes of one interchange inside a bulk reprocess.
const (
	BulkReprocessed = "REPROCESSED"
	BulkNotFound    = "NOT_FOUND"
	BulkNotInbound  = "NOT_INBOUND"
	BulkUnreadable  = "UNREADABLE"
)

// BulkItem is the outcome for one requested interchange. Messages lists the
// messages that were FAILED before, as in a single reprocess; it is empty for
// every outcome other than REPROCESSED.
type BulkItem struct {
	InterchangeID string               `json:"interchangeId"`
	Outcome       string               `json:"outcome"`
	Error         string               `json:"error,omitempty"`
	Changed       int                  `json:"changed"`
	Messages      []ReprocessedMessage `json:"messages"`
}

// BulkReprocessResult is the result of a bulk reprocess (or its dry run), with
// one item per requested id in request order. Changed sums the items.
type BulkReprocessResult struct {
	DryRun    bool       `json:"dryRun"`
	Requested int        `json:"requested"`
	Changed   int        `json:"changed"`
	Items     []BulkItem `json:"items"`
}

// ReprocessInterchanges reprocesses up to MaxBulkReprocess interchanges in one
// transaction, under one Idempotency-Key (FR-35.10, "bulk reprocess (≤ 200)
// with a dry run"). Each interchange follows the rules of ReprocessInterchange.
// An interchange that is unknown, outbound or whose stored payload no longer
// parses does not stop the others: its item carries the outcome and nothing is
// written for it. Interchanges are locked in id order so that two overlapping
// bulk runs serialize instead of deadlocking. ids must be distinct.
func (s *Store) ReprocessInterchanges(ctx context.Context, key, requestHash string, list []string, dryRun bool, plan ReprocessPlan) (BulkReprocessResult, bool, error) {
	var out BulkReprocessResult
	replayed := false
	if len(list) == 0 || len(list) > MaxBulkReprocess {
		return out, false, fmt.Errorf("edi: bulk reprocess takes 1 to %d interchanges, got %d", MaxBulkReprocess, len(list))
	}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tid, err := tenantID(ctx)
		if err != nil {
			return err
		}
		if !dryRun {
			if replayed, err = claimKey(ctx, tx, tid, key, requestHash, &out); err != nil || replayed {
				return err
			}
		}
		order := make([]int, len(list))
		for i := range order {
			order[i] = i
		}
		sort.Slice(order, func(a, b int) bool { return list[order[a]] < list[order[b]] })
		out = BulkReprocessResult{DryRun: dryRun, Requested: len(list), Items: make([]BulkItem, len(list))}
		for _, i := range order {
			item := BulkItem{InterchangeID: list[i], Outcome: BulkReprocessed, Messages: []ReprocessedMessage{}}
			res, err := reprocessOne(ctx, tx, tid, list[i], dryRun, plan)
			switch {
			case errors.Is(err, ErrNotFound):
				item.Outcome, item.Error = BulkNotFound, "interchange not found"
			case errors.Is(err, ErrNotInbound):
				item.Outcome, item.Error = BulkNotInbound, "only inbound interchanges are reprocessed; an outbound document is resent"
			case errors.Is(err, ErrUnreadable):
				item.Outcome, item.Error = BulkUnreadable, err.Error()
			case err != nil:
				return err
			default:
				item.Changed, item.Messages = res.Changed, res.Messages
			}
			out.Changed += item.Changed
			out.Items[i] = item
		}
		return storeIfKept(ctx, tx, dryRun, key, out)
	})
	return out, replayed, err
}

// reprocessOne reprocesses one interchange inside tx. It returns ErrNotFound,
// ErrNotInbound or the plan's error before writing anything.
func reprocessOne(ctx context.Context, tx pgx.Tx, tid, id string, dryRun bool, plan ReprocessPlan) (ReprocessResult, error) {
	var partnerID, direction, standard, control, raw string
	err := tx.QueryRow(ctx, `select partner_id::text, direction, standard, control_number, raw_payload
		from interchange where id = $1 for update`, id).Scan(&partnerID, &direction, &standard, &control, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return ReprocessResult{}, ErrNotFound
	}
	if err != nil {
		return ReprocessResult{}, err
	}
	if direction != "IN" {
		return ReprocessResult{}, ErrNotInbound
	}
	rows, err := tx.Query(ctx, `select id::text, transaction_set, st_control, status, coalesce(error, '')
		from message where interchange_id = $1 and status = 'FAILED' and transaction_set not in ('997', 'CONTRL')
		order by created_at, st_control, id for update`, id)
	if err != nil {
		return ReprocessResult{}, err
	}
	failed, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Message, error) {
		var m Message
		return m, r.Scan(&m.ID, &m.TransactionSet, &m.STControl, &m.Status, &m.Error)
	})
	if err != nil {
		return ReprocessResult{}, err
	}
	out := ReprocessResult{InterchangeID: id, DryRun: dryRun, Messages: []ReprocessedMessage{}}
	if len(failed) == 0 {
		return out, nil
	}
	next, err := plan(raw, failed)
	if err != nil {
		return ReprocessResult{}, err
	}
	if len(next) != len(failed) {
		return ReprocessResult{}, errors.New("edi: reprocess plan returned a different number of messages")
	}
	for i, old := range failed {
		m := next[i]
		if m.ID != old.ID {
			return ReprocessResult{}, errors.New("edi: reprocess plan reordered the messages")
		}
		changed := m.Status != old.Status || m.Error != old.Error
		out.Messages = append(out.Messages, ReprocessedMessage{MessageID: m.ID, TransactionSet: old.TransactionSet,
			STControl: old.STControl, PreviousError: old.Error, Status: m.Status, Error: m.Error, Changed: changed})
		if !changed {
			continue
		}
		out.Changed++
		if dryRun {
			continue
		}
		if _, err := tx.Exec(ctx, `update message set status = $2, canonical = $3, error = $4 where id = $1`,
			m.ID, m.Status, nullBytes(m.Canonical), nullString(m.Error)); err != nil {
			return ReprocessResult{}, err
		}
		if m.Status == "FAILED" {
			if err := enqueueFailed(ctx, tx, tid, ediev.MessageFailedData{
				MessageID: m.ID, InterchangeID: id, PartnerID: partnerID, TransactionSet: old.TransactionSet,
				ControlNumber: control, STControl: old.STControl, Error: m.Error, Reprocessed: true,
			}); err != nil {
				return ReprocessResult{}, err
			}
			continue
		}
		if err := enqueueReceived(ctx, tx, tid, ediev.MessageReceivedData{
			MessageID: m.ID, InterchangeID: id, PartnerID: partnerID, Standard: standard,
			TransactionSet: old.TransactionSet, ControlNumber: control, STControl: old.STControl, Status: m.Status,
		}); err != nil {
			return ReprocessResult{}, err
		}
	}
	return out, nil
}

// storeIfKept records the response under the key unless this is a dry run.
func storeIfKept(ctx context.Context, tx pgx.Tx, dryRun bool, key string, out any) error {
	if dryRun {
		return nil
	}
	return storeResponse(ctx, tx, key, out)
}
