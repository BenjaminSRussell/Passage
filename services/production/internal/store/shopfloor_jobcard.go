package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
)

const (
	SystemKeyPrefix         = "sys:"
	shopfloorJobCardKey     = SystemKeyPrefix + "shopfloor-jobcard:"
	shopfloorJobCardPostKey = SystemKeyPrefix + "shopfloor-jobcard-post:"
	shopfloorActor          = "shopfloor"
)

// ErrJobCardRefused marks a permanent refusal (a 4xx problem or an unknown
// operation): retrying the same registration cannot succeed, so a consumer parks it.
// Any other error from PostShopfloorJobCard is transient.
var ErrJobCardRefused = errors.New("shop-floor job card refused")

// ShopfloorJobCard is one closed shop-floor registration the w21 job-card
// consumer turns into a posted job card (FR-25.4, BR-25.9).
type ShopfloorJobCard struct {
	RegistrationID   string
	Kind             string
	OrderRef         string
	OperationRef     string
	Hours            *decimal.Decimal
	CostRate         *decimal.Decimal
	CostRateCurrency string
	TraceID          string
	Actor            string
}

// PostShopfloorJobCard posts a job-card journal for a closed shop-floor job-stop.
// Hours are valued at the worker's costRate when that rate is positive and in the
// order's currency, otherwise (rate unset or in another currency) at the
// operation's resource labor rate. The rate is stored on the draft line, so a
// later post of the same draft books the same value. A repeated registration id
// is a no-op. Clock punches, open stops and non-UUID order refs are ignored; an
// operation the order has no process job for is a permanent refusal.
func (s *Postgres) PostShopfloorJobCard(ctx context.Context, in ShopfloorJobCard) (Journal, bool, error) {
	hours, ok := shopfloorHours(in)
	if !ok || in.Kind != "job-stop" || !ids.Valid(in.OrderRef) {
		return Journal{}, false, nil
	}
	oper, err := strconv.Atoi(strings.TrimSpace(in.OperationRef))
	if err != nil || oper < 1 {
		return Journal{}, false, nil
	}
	orderID, _ := ids.Canonical(in.OrderRef)
	order, err := s.GetOrder(ctx, orderID)
	if err != nil {
		return Journal{}, false, refusal(err)
	}
	jobID, err := s.processJobID(ctx, orderID, oper)
	if err != nil {
		return Journal{}, false, err
	}
	if jobID == "" {
		slog.WarnContext(ctx, "shop-floor job card dropped: no process job for operation", "orderId", orderID, "operation", oper, "registrationId", in.RegistrationID, "correlationId", in.TraceID)
		return Journal{}, false, fmt.Errorf("%w: order %s has no process job for operation %d", ErrJobCardRefused, orderID, oper)
	}
	rate := positiveRate(in.CostRate)
	if rate != nil && in.CostRateCurrency != order.Currency {
		slog.WarnContext(ctx, "shop-floor job card uses the resource labor rate: worker rate is in another currency", "orderId", orderID, "registrationId", in.RegistrationID, "workerCurrency", in.CostRateCurrency, "orderCurrency", order.Currency, "correlationId", in.TraceID)
		rate = nil
	}
	actor := in.Actor
	if actor == "" {
		actor = shopfloorActor
	}
	key := shopfloorJobCardKey + in.RegistrationID
	j, _, err := s.CreateJournal(ctx, NewJournal{
		OrderID: orderID, Type: domain.JournalJobCard, Proposal: domain.ProposeNone, AutoStart: true,
		Hours: []HoursInput{{JobID: jobID, Hours: hours, LaborRate: rate}},
		Actor: actor, IdempotencyKey: key, RequestHash: key,
	})
	if err != nil {
		return Journal{}, false, refusal(err)
	}
	if j.Posted {
		return j, false, nil
	}
	posted, replayed, err := s.PostJournal(ctx, PostRequest{
		JournalID: j.ID, AutoStart: true, Actor: actor,
		TraceID: in.TraceID, IdempotencyKey: shopfloorJobCardPostKey + in.RegistrationID,
	})
	if err != nil {
		return Journal{}, false, refusal(err)
	}
	return posted, !replayed, nil
}

func (s *Postgres) processJobID(ctx context.Context, orderID string, oper int) (string, error) {
	p, _ := tenant.FromContext(ctx)
	var id string
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select id::text from production_job where tenant_id = $1 and order_id = $2 and oper_number = $3 and job_type = $4 order by seq limit 1`,
			p.TenantID, orderID, oper, string(domain.JobProcess)).Scan(&id)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

func refusal(err error) error {
	var p *httpx.Problem
	if errors.As(err, &p) && p.Status >= 400 && p.Status < 500 {
		return fmt.Errorf("%w: %w", ErrJobCardRefused, err)
	}
	return err
}

func shopfloorHours(in ShopfloorJobCard) (decimal.Decimal, bool) {
	if in.Hours == nil {
		return decimal.Zero, false
	}
	h := in.Hours.Round(domain.HoursScale)
	return h, domain.CheckHours(h) == nil
}

func positiveRate(r *decimal.Decimal) *decimal.Decimal {
	if r == nil || !r.IsPositive() {
		return nil
	}
	return r
}
