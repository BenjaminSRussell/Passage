package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
)

// MaxNetworkFindings bounds the network feasibility inquiry.
const MaxNetworkFindings = 500

// FlagWaitExceeded names the SCHED-0010 finding: the wait behind a frozen
// predecessor is over the network's maximum (BR-23.11).
const FlagWaitExceeded = "WAIT_EXCEEDED"

// NetworkFinding is one run message about a resource network: an operation the
// run left unplaced because its chain is infeasible (SCHED-0009,
// NETWORK_INFEASIBLE) or a placed one whose wait is over the maximum
// (SCHED-0010, WAIT_EXCEEDED). Order, operation number and network come from the
// operation as it is now; they are empty when the operation was deleted since.
type NetworkFinding struct {
	RunID       string    `json:"runId"`
	RunScope    string    `json:"runScope"`
	RunAt       time.Time `json:"runAt"`
	Code        string    `json:"code"`
	Flag        string    `json:"flag"`
	OperationID string    `json:"operationId"`
	OrderID     *string   `json:"orderId"`
	OperNumber  *int      `json:"operNumber"`
	NetworkID   *string   `json:"networkId"`
	NetworkCode *string   `json:"networkCode"`
	Detail      string    `json:"detail"`
}

// NetworkFeasibilityFilter selects findings by network and by the time the run
// was made (From inclusive, To exclusive; nil leaves that side open).
type NetworkFeasibilityFilter struct {
	NetworkID string
	From, To  *time.Time
	Limit     int
}

// NetworkFeasibility is the inquiry result. The counts cover every finding the
// filter matches, whatever the limit.
type NetworkFeasibility struct {
	Items      []NetworkFinding `json:"items"`
	Truncated  bool             `json:"truncated"`
	Infeasible int              `json:"infeasible"`
	// InfeasibleOrders counts the distinct orders flagged NETWORK_INFEASIBLE
	// (PRD 4.23.14 "orders flagged"); a chain that fails lists every one of its
	// operations, so Infeasible can be larger. Deleted operations have no order
	// and are not counted here.
	InfeasibleOrders int `json:"infeasibleOrders"`
	WaitViolations   int `json:"waitViolations"`
}

// NetworkFeasibilityStore is the network feasibility inquiry (PRD 4.23.14).
type NetworkFeasibilityStore interface {
	NetworkFeasibility(ctx context.Context, f NetworkFeasibilityFilter) (NetworkFeasibility, error)
}

// The findings are the unplaced messages of runs ("<operation id>: SCHED-0009
// ...") and the SCHED-0010 warnings. An operation id is 36 characters, so the
// detail of a message starts at character 39.
const networkFindingsCTE = `with raw as (
		select r.tenant_id, r.id as run_id, r.scope, r.created_at, 'SCHED-0009' as code,
			substr(m, 1, 36)::uuid as op_id, substr(m, 39) as detail
		from schedule_run r, jsonb_array_elements_text(r.messages) m
		where m ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}: SCHED-0009'
			and ($1::timestamptz is null or r.created_at >= $1) and ($2::timestamptz is null or r.created_at < $2)
		union all
		select r.tenant_id, r.id, r.scope, r.created_at, 'SCHED-0010',
			(w->>'operationId')::uuid, coalesce(w->>'detail', '')
		from schedule_run r, jsonb_array_elements(r.warnings) w
		where w->>'code' = 'SCHED-0010'
			and w->>'operationId' ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
			and ($1::timestamptz is null or r.created_at >= $1) and ($2::timestamptz is null or r.created_at < $2)
	), found as (
		select raw.*, o.order_id, o.oper_number, o.network_id, n.code as network_code
		from raw
		left join scheduling_operation o on o.tenant_id = raw.tenant_id and o.id = raw.op_id
		left join scheduling_resource_network n on n.tenant_id = o.tenant_id and n.id = o.network_id
		where ($3::uuid is null or o.network_id = $3)
	)`

// NetworkFeasibility lists the network findings of runs in the range, newest
// run first, at most Limit (more sets Truncated), with the totals per kind. A
// network that does not exist is SCHED-0109.
func (s *Postgres) NetworkFeasibility(ctx context.Context, f NetworkFeasibilityFilter) (NetworkFeasibility, error) {
	out := NetworkFeasibility{Items: []NetworkFinding{}}
	limit := f.Limit
	if limit < 1 || limit > MaxNetworkFindings {
		limit = MaxNetworkFindings
	}
	var network *string
	if f.NetworkID != "" {
		network = &f.NetworkID
	}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if network != nil {
			var one int
			if err := tx.QueryRow(ctx, `select 1 from scheduling_resource_network where id = $1`, f.NetworkID).Scan(&one); errors.Is(err, pgx.ErrNoRows) {
				return httpx.NewProblem(http.StatusNotFound, CodeNetworkNotFound, "resource network not found")
			} else if err != nil {
				return err
			}
		}
		if err := tx.QueryRow(ctx, networkFindingsCTE+` select count(*) filter (where code = 'SCHED-0009'),
				count(distinct order_id) filter (where code = 'SCHED-0009'), count(*) filter (where code = 'SCHED-0010') from found`,
			f.From, f.To, network).Scan(&out.Infeasible, &out.InfeasibleOrders, &out.WaitViolations); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, networkFindingsCTE+` select run_id::text, scope, created_at, code, op_id::text, order_id::text, oper_number,
				network_id::text, network_code, detail
			from found order by created_at desc, run_id desc, code, order_id nulls last, oper_number nulls last, op_id limit $4`, f.From, f.To, network, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var x NetworkFinding
			if err := rows.Scan(&x.RunID, &x.RunScope, &x.RunAt, &x.Code, &x.OperationID, &x.OrderID, &x.OperNumber, &x.NetworkID, &x.NetworkCode, &x.Detail); err != nil {
				return err
			}
			x.Flag = domain.FlagNetworkInfeasible
			if x.Code == domain.CodeNetworkWait {
				x.Flag = FlagWaitExceeded
			}
			out.Items = append(out.Items, x)
		}
		return rows.Err()
	})
	if out.Truncated = len(out.Items) > limit; out.Truncated {
		out.Items = out.Items[:limit]
	}
	return out, err
}
