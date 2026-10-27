package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/eam/internal/domain"
	eamevents "github.com/BenjaminSRussell/passage/services/eam/internal/events"
)

// TriageInput is a planner's triage action on a request (FR-44.6).
type TriageInput struct {
	Action string
	Note   string
	Actor  string
}

// TriageRequest applies TRIAGE, REJECT or DEFER to a request (FR-44.6). Each
// action is kept in an append-only history with its key, so a retried call
// returns the request as it is now. Repeating the action a request already
// reflects changes nothing and emits no event. CONVERTED requests are final
// (EAM-REQ-001).
func (s *Postgres) TriageRequest(ctx context.Context, id string, in TriageInput, key string) (Request, error) {
	var out Request
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		req, err := getRequest(ctx, tx, t, id, true)
		if err != nil {
			return err
		}
		if key != "" {
			var owner string
			err := tx.QueryRow(ctx, `select request_id::text from maintenance_request_triage
			  where tenant_id = $1 and idempotency_key = $2`, t, key).Scan(&owner)
			if err == nil {
				if owner != id {
					return problem(422, CodeKeyReuse, "Idempotency-Key was already used for another request")
				}
				out = req
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		to, err := domain.TriageTarget(req.Status, in.Action)
		if err != nil {
			return problem(422, CodeRequestState, err.Error())
		}
		if to == req.Status && in.Note == "" {
			out = req
			return nil
		}
		now := s.now()
		if _, err := tx.Exec(ctx, `insert into maintenance_request_triage (tenant_id, id, request_id, action, from_status,
		    to_status, note, actor, at, idempotency_key) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			t, ids.New(), id, in.Action, req.Status, to, optStr(in.Note), optStr(in.Actor), now, optStr(key)); err != nil {
			return err
		}
		out, err = scanRequest(tx.QueryRow(ctx, `update maintenance_request set status = $3,
		    triage_note = coalesce($4, triage_note), updated_at = now()
		  where tenant_id = $1 and id = $2 returning `+requestColumns, t, id, to, optStr(in.Note)))
		if err != nil || to == req.Status {
			return err
		}
		return enqueue(ctx, tx, t, "request", id, eamevents.RequestTriaged, eamevents.RequestTriagedData{
			RequestID: id, AssetID: req.AssetID, Action: in.Action, From: req.Status, To: to,
			At: now.Format(time.RFC3339Nano), Actor: in.Actor})
	})
	return out, err
}
