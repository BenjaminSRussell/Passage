package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	planevents "github.com/BenjaminSRussell/passage/services/planning/internal/events"
)

const (
	CodeMessageNotFound      = "PLAN-MESSAGE-NOT-FOUND"
	CodeMessageDecided       = "PLAN-MESSAGE-DECIDED"
	CodeMessageStale         = "PLAN-MESSAGE-STALE"
	CodeMessageNotActionable = "PLAN-MESSAGE-NOT-ACTIONABLE"
)

// MessageDecision is a planner's decision on one action message.
type MessageDecision struct {
	// Apply applies the message (the owning context changes the source order); false dismisses it.
	Apply bool
	// Note is the planner's reason, at most 500 characters.
	Note string
	// IdempotencyKey makes a retry of the same decision return the stored message.
	IdempotencyKey string
}

// DecideMessage applies or dismisses an open message of a plan's current version under
// If-Match (FR-22.7), and writes message.applied.v1 or message.dismissed.v1 in the same
// transaction. The bool is true when the request replays a decision already made under the
// same Idempotency-Key. 404 PLAN-MESSAGE-NOT-FOUND, 409 PLAN-MESSAGE-DECIDED when it was
// decided otherwise, 409 PLAN-MESSAGE-STALE when its version is no longer current, 412 for a
// stale If-Match and 422 PLAN-MESSAGE-NOT-ACTIONABLE when an information message is applied.
func (s *Postgres) DecideMessage(ctx context.Context, id string, expectedVersion int, d MessageDecision) (Message, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out Message
	replayed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var (
			m                 Message
			planID, planCode  string
			versionID, siteID string
			versionStatus     string
			entity            string
			key               *string
			date              time.Time
		)
		// Lock the message's version before the message, in the order a run takes them (version, then
		// messages), so a run that replaces the version waits for this decision and carries it, or this
		// decision waits for the run and finds the version no longer current.
		var locked string
		err := tx.QueryRow(ctx, `select v.status from action_message m join plan_version v on v.tenant_id = m.tenant_id and v.id = m.plan_version_id
			where m.tenant_id = $1 and m.id = $2 for share of v`, p.TenantID, id).Scan(&locked)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeMessageNotFound, "message not found")
		}
		if err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `select m.action, m.item_id::text, m.warehouse_id::text, m.supply_ref, m.demand_ref, m.suggested_date,
				m.delay_days, m.qty::text, m.status, m.version, m.decision_key, p.id::text, p.code, v.id::text, v.site_id::text, v.status,
				coalesce(v.legal_entity_id::text, '')
			from action_message m join plan_version v on v.tenant_id = m.tenant_id and v.id = m.plan_version_id
			join plan p on p.tenant_id = v.tenant_id and p.id = v.plan_id
			where m.tenant_id = $1 and m.id = $2 for update of m`, p.TenantID, id).
			Scan(&m.Action, &m.ItemID, &m.WarehouseID, &m.SupplyRef, &m.DemandRef, &date, &m.DelayDays, &m.Qty, &m.Status, &m.Version,
				&key, &planID, &planCode, &versionID, &siteID, &versionStatus, &entity)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeMessageNotFound, "message not found")
		}
		if err != nil {
			return err
		}
		m.ID = id
		m.SuggestedDate = date.Format(dateLayout)
		target := domain.MessageDismissed
		if d.Apply {
			target = domain.MessageApplied
		}
		if m.Status != domain.MessageOpen {
			if d.IdempotencyKey != "" && key != nil && *key == d.IdempotencyKey && m.Status == target {
				replayed = true
				var e error
				out, e = readMessage(ctx, tx, p.TenantID, id)
				return e
			}
			return httpx.NewProblem(http.StatusConflict, CodeMessageDecided, "the message was already "+m.Status)
		}
		if m.Version != expectedVersion {
			return versionConflict("message", m.Version)
		}
		if versionStatus != "current" {
			return httpx.NewProblem(http.StatusConflict, CodeMessageStale, "the message belongs to a plan version that is no longer current")
		}
		if d.Apply && !domain.MessageActionable(m.Action) {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeMessageNotActionable,
				"a "+m.Action+" message changes no source order; dismiss it instead")
		}
		if _, err := tx.Exec(ctx, `update action_message set status = $3, version = version + 1, decided_by = $4, decided_at = now(),
			decision_note = $5, decision_key = nullif($6, '') where tenant_id = $1 and id = $2`,
			p.TenantID, id, target, p.UserID, d.Note, d.IdempotencyKey); err != nil {
			return err
		}
		typ := planevents.MessageDismissed
		if d.Apply {
			typ = planevents.MessageApplied
		}
		if err := enqueue(ctx, tx, typ, planID, p.TenantID, id, planevents.MessageDecidedData{
			MessageID: id, PlanID: planID, PlanCode: planCode, VersionID: versionID, SiteID: siteID, LegalEntityID: optional(entity),
			Action: m.Action, ItemID: m.ItemID, WarehouseID: m.WarehouseID, SupplyRef: m.SupplyRef, DemandRef: m.DemandRef,
			SuggestedDate: m.SuggestedDate, DelayDays: m.DelayDays, Qty: m.Qty, Version: m.Version + 1, DecidedBy: p.UserID, Note: d.Note,
		}); err != nil {
			return err
		}
		out, err = readMessage(ctx, tx, p.TenantID, id)
		return err
	})
	return out, replayed, err
}

// readMessage reads one message with its catalog text.
func readMessage(ctx context.Context, tx pgx.Tx, tenantID, id string) (Message, error) {
	rows, err := tx.Query(ctx, messageSelect+` where m.tenant_id = $1 and m.id = $2`, tenantID, id)
	if err != nil {
		return Message{}, err
	}
	defer rows.Close()
	msgs, err := scanMessages(rows)
	if err != nil {
		return Message{}, err
	}
	if len(msgs) != 1 {
		return Message{}, httpx.NewProblem(http.StatusNotFound, CodeMessageNotFound, "message not found")
	}
	return msgs[0], nil
}

// carryDecisions copies the decisions of the replaced version's messages onto the identical messages
// of the new version (FR-22.7): same item, scope, action, supply, demand, suggested date, quantity and
// delay. A message the run raises again unchanged keeps its applied or dismissed status, so a dismissed
// message does not come back as open and an applied one is not applied twice while its owner has not
// changed the order yet; a message whose date or quantity changed is a new one and stays open.
// carried_from names the message the planner decided. It returns the ids of the new messages that
// carry a decision.
func carryDecisions(ctx context.Context, tx pgx.Tx, tenantID, previousID, versionID string) (map[string]bool, error) {
	carried := map[string]bool{}
	if previousID == "" {
		return carried, nil
	}
	rows, err := tx.Query(ctx, `update action_message n set status = o.status, decided_by = o.decided_by, decided_at = o.decided_at,
			decision_note = o.decision_note, carried_from = coalesce(o.carried_from, o.id)
		from action_message o
		where n.tenant_id = $1 and n.plan_version_id = $3 and o.tenant_id = $1 and o.plan_version_id = $2 and o.status <> 'open'
			and o.item_id = n.item_id and o.warehouse_id is not distinct from n.warehouse_id and o.action = n.action
			and o.supply_ref = n.supply_ref and o.demand_ref = n.demand_ref and o.suggested_date = n.suggested_date
			and o.qty = n.qty and o.delay_days = n.delay_days
		returning n.id::text`, tenantID, previousID, versionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		carried[id] = true
	}
	return carried, rows.Err()
}
