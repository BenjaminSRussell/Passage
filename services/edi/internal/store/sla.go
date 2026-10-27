package store

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
)

// PartnerSLA is one partner's traffic over a window (partner SLA dashboard,
// PRD §6.35.6 and §6.35.14): message volume and failures in both directions,
// and for our outbound interchanges how fast the partner's 997 came back
// against the profile's ack_sla_minutes.
type PartnerSLA struct {
	PartnerID     string `json:"partnerId"`
	PartnerName   string `json:"partnerName"`
	Environment   string `json:"environment"`
	AckSLAMinutes int    `json:"ackSlaMinutes"`
	Messages      int64  `json:"messages"`
	Failed        int64  `json:"failed"`
	// Outbound is the number of outbound interchanges recorded in the window,
	// Acked how many of them have the partner's 997.
	Outbound int64 `json:"outbound"`
	Acked    int64 `json:"acked"`
	// P95AckSeconds is the 95th percentile of acked_at - received_at over
	// the acked outbound interchanges; nil when none was acked.
	P95AckSeconds *float64 `json:"-"`
	// LateAcks were acked after ack_sla_minutes; OverdueAcks are still
	// waiting past it (both count against the SLA, BR-35.5).
	LateAcks    int64 `json:"lateAcks"`
	OverdueAcks int64 `json:"overdueAcks"`
}

// PartnerSLAs aggregates the last days days per partner, ordered by partner
// name. Partners with no traffic in the window are omitted.
func (s *Store) PartnerSLAs(ctx context.Context, days int) ([]PartnerSLA, error) {
	out := []PartnerSLA{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			with msg as (
				select i.partner_id, count(*) as messages, count(*) filter (where m.status = 'FAILED') as failed
				from message m join interchange i on i.tenant_id = m.tenant_id and i.id = m.interchange_id
				where m.created_at > now() - make_interval(days => $1)
				group by i.partner_id
			), outb as (
				select i.partner_id,
					count(*) as outbound,
					count(*) filter (where i.acked_at is not null) as acked,
					percentile_cont(0.95) within group (order by extract(epoch from i.acked_at - i.received_at))
						filter (where i.acked_at is not null) as p95_seconds,
					count(*) filter (where i.acked_at - i.received_at > make_interval(mins => p.ack_sla_minutes)) as late,
					count(*) filter (where i.acked_at is null and i.ack_status in ('PENDING', 'TIMEOUT')
						and i.received_at < now() - make_interval(mins => p.ack_sla_minutes)) as overdue
				from interchange i join trading_partner p on p.tenant_id = i.tenant_id and p.id = i.partner_id
				where i.direction = 'OUT' and i.received_at > now() - make_interval(days => $1)
				group by i.partner_id
			)
			select p.id::text, p.name, p.environment, p.ack_sla_minutes,
				coalesce(msg.messages, 0), coalesce(msg.failed, 0),
				coalesce(outb.outbound, 0), coalesce(outb.acked, 0), outb.p95_seconds,
				coalesce(outb.late, 0), coalesce(outb.overdue, 0)
			from trading_partner p
			left join msg on msg.partner_id = p.id
			left join outb on outb.partner_id = p.id
			where msg.partner_id is not null or outb.partner_id is not null
			order by p.name, p.environment, p.id`, days)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r PartnerSLA
			if err := rows.Scan(&r.PartnerID, &r.PartnerName, &r.Environment, &r.AckSLAMinutes, &r.Messages, &r.Failed,
				&r.Outbound, &r.Acked, &r.P95AckSeconds, &r.LateAcks, &r.OverdueAcks); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}
