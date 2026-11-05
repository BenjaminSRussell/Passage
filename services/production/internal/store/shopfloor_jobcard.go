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
	shopfloorJobCardRevKey  = SystemKeyPrefix + "shopfloor-jobcard-reverse:"
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
// is a no-op. Clock punches, open stops and non-UUID order refs are ignored; a
// registration id that is not a UUID, and an operation the order has no process
// job for, are permanent refusals.
func (s *Postgres) PostShopfloorJobCard(ctx context.Context, in ShopfloorJobCard) (Journal, bool, error) {
	hours, ok := shopfloorHours(in)
	if !ok || in.Kind != "job-stop" || !ids.Valid(in.OrderRef) {
		return Journal{}, false, nil
	}
	regID, ok := ids.Canonical(in.RegistrationID)
	if !ok {
		return Journal{}, false, fmt.Errorf("%w: registration id %q is not a UUID", ErrJobCardRefused, in.RegistrationID)
	}
	in.RegistrationID = regID
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
		Actor: actor, IdempotencyKey: key, RequestHash: key, AllowFinished: true,
	})
	if err != nil {
		return Journal{}, false, refusal(err)
	}
	if j.Posted {
		return j, false, nil
	}
	posted, replayed, err := s.PostJournal(ctx, PostRequest{
		JournalID: j.ID, AutoStart: true, Actor: actor,
		TraceID: in.TraceID, IdempotencyKey: shopfloorJobCardPostKey + in.RegistrationID, AllowFinished: true,
	})
	var problem *httpx.Problem
	if errors.As(err, &problem) && problem.Code == CodeJournalPosted {
		done, gerr := s.GetJournal(ctx, j.ID)
		return done, false, gerr
	}
	if err != nil {
		return Journal{}, false, refusal(err)
	}
	return posted, !replayed, nil
}

func (s *Postgres) processJobID(ctx context.Context, orderID string, oper int) (string, error) {
	p, _ := tenant.FromContext(ctx)
	var id string
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		id, err = processJobIDIn(ctx, tx, p.TenantID, orderID, oper)
		return err
	})
	return id, err
}

func processJobIDIn(ctx context.Context, q querier, tenantID, orderID string, oper int) (string, error) {
	var id string
	err := q.QueryRow(ctx, `select id::text from production_job where tenant_id = $1 and order_id = $2 and oper_number = $3 and job_type = $4 order by (status = 'finished'), seq limit 1`,
		tenantID, orderID, oper, string(domain.JobProcess)).Scan(&id)
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

// AdjustShopfloorJobCard sets the hours a registration's job card books, for a
// registration.corrected event (a server stop superseded by the kiosk's own stop
// is corrected to 0 h; a supervisor correction changes the hours). In one
// transaction it reverses the live card and, for positive hours, posts a new one
// at the same rate. A registration with no card is a no-op, as is one whose live
// card already has newHours. An order that no longer takes job cards (Ended,
// settled) refuses with ErrJobCardRefused and nothing is reversed. A card that
// something other than an adjust reversed (an order reset, FR-21.9) is left
// reversed: the reset took the labor off the order and a correction does not
// bring it back. Reports whether anything changed.
func (s *Postgres) AdjustShopfloorJobCard(ctx context.Context, registrationID string, newHours decimal.Decimal, traceID string) (bool, error) {
	newHours = newHours.Round(domain.HoursScale)
	canonical, ok := ids.Canonical(registrationID)
	if !ok || newHours.IsNegative() {
		return false, fmt.Errorf("%w: registration id %q, hours %s", ErrJobCardRefused, registrationID, newHours)
	}
	registrationID = canonical
	p, _ := tenant.FromContext(ctx)
	outcome := "no-op"
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		outcome = "no-op"
		card, err := latestShopfloorCard(ctx, tx, p.TenantID, registrationID)
		if err != nil || card.journalID == "" {
			return err
		}
		h, err := lockOrder(ctx, tx, p.TenantID, card.orderID, 0)
		if err != nil {
			return err
		}
		if card, err = latestShopfloorCard(ctx, tx, p.TenantID, registrationID); err != nil {
			return err
		}
		if card.reversed && newHours.IsZero() || !card.reversed && card.hours.Equal(newHours) {
			return nil
		}
		if card.reversed && !card.reversedByAdjust {
			outcome = "no-op: card reversed by the order"
			return nil
		}
		if !domain.JournalAllowed(domain.JournalJobCard, h.status, false) {
			return journalNotAllowed(domain.JournalJobCard, h.status)
		}
		if !card.reversed {
			if err := reverseJournal(ctx, tx, p.TenantID, &h, card.journalID, shopfloorActor, shopfloorJobCardRevKey+registrationID, true); err != nil {
				return err
			}
			outcome = "reversed"
		}
		if newHours.IsPositive() {
			jobID, err := processJobIDIn(ctx, tx, p.TenantID, card.orderID, card.oper)
			if err != nil {
				return err
			}
			if jobID == "" {
				return fmt.Errorf("%w: order %s has no process job for operation %d", ErrJobCardRefused, card.orderID, card.oper)
			}
			key := fmt.Sprintf("%s%s:adj:%d:%s", shopfloorJobCardKey, registrationID, card.revisions, newHours)
			rate := card.rate
			in := NewJournal{
				Type: domain.JournalJobCard, Proposal: domain.ProposeNone, AutoStart: true,
				Hours: []HoursInput{{JobID: jobID, Hours: newHours, LaborRate: &rate}}, AllowFinished: true,
			}
			if err := autoPost(ctx, tx, p.TenantID, &h, in, shopfloorActor, key, decimal.Zero, nil); err != nil {
				return err
			}
			outcome = "reversed and rebooked"
			if card.reversed {
				outcome = "rebooked"
			}
		}
		_, err = tx.Exec(ctx, `update production_order set version = version + 1, updated_at = now() where tenant_id = $1 and id = $2`, p.TenantID, h.id)
		return err
	})
	if err != nil {
		slog.ErrorContext(ctx, "shop-floor job card adjust failed", "registrationId", registrationID, "hours", newHours.String(), "err", err, "correlationId", traceID)
		return false, refusal(err)
	}
	slog.InfoContext(ctx, "shop-floor job card adjusted", "registrationId", registrationID, "hours", newHours.String(), "outcome", outcome, "correlationId", traceID)
	return !strings.HasPrefix(outcome, "no-op"), nil
}

type postedJobCard struct {
	journalID, orderID string
	oper, revisions    int
	hours, rate        decimal.Decimal
	reversed           bool
	// reversedByAdjust: the reversal is an adjust's own, not an order reset's.
	reversedByAdjust bool
}

func latestShopfloorCard(ctx context.Context, tx pgx.Tx, tenantID, registrationID string) (postedJobCard, error) {
	var c postedJobCard
	var hours, rate string
	var revKey *string
	key := shopfloorJobCardKey + registrationID
	err := tx.QueryRow(ctx, `select count(*) from production_journal where tenant_id = $1 and posted and reversal_of is null
		and (idempotency_key = $2 or idempotency_key like $3)`, tenantID, key, key+":adj:%").Scan(&c.revisions)
	if err != nil || c.revisions == 0 {
		return c, err
	}
	err = tx.QueryRow(ctx, `select j.id::text, j.order_id::text, l.oper_number, l.qty::text, l.unit_cost::text,
			(select r.idempotency_key from production_journal r where r.tenant_id = j.tenant_id and r.reversal_of = j.id limit 1)
		from production_journal j join production_journal_line l on l.tenant_id = j.tenant_id and l.journal_id = j.id and l.kind = 'hours'
		where j.tenant_id = $1 and j.posted and j.reversal_of is null and (j.idempotency_key = $2 or j.idempotency_key like $3)
		order by j.posted_at desc, j.id desc limit 1`, tenantID, key, key+":adj:%").
		Scan(&c.journalID, &c.orderID, &c.oper, &hours, &rate, &revKey)
	c.hours, c.rate = dec(hours), dec(rate)
	c.reversed = revKey != nil
	c.reversedByAdjust = c.reversed && strings.HasPrefix(*revKey, shopfloorJobCardRevKey)
	return c, err
}

// DutyShopfloorJobCard is the duty of the service principal that posts shop-floor job
// cards (the registration consumer, service:production-shopfloor). Only it may look up
// an order's legal entity across legal entities.
const DutyShopfloorJobCard = "production.jobcard.shopfloor"

// ShopfloorOrderLegalEntity returns the legal entity of the production order a shop-floor
// registration names. The registration consumer runs as a tenant-level principal (the
// shop floor has no legal entity), so it reads only this one column across legal entities
// and then posts the job card as a principal pinned to that legal entity. ok is false when
// the tenant has no such order or orderRef is not a UUID. Any other caller is refused.
func (s *Postgres) ShopfloorOrderLegalEntity(ctx context.Context, orderRef string) (string, bool, error) {
	orderID, ok := ids.Canonical(orderRef)
	if !ok {
		return "", false, nil
	}
	return s.shopfloorLegalEntity(ctx, `select legal_entity_id::text from production_order where tenant_id = $1 and id = $2`, orderID)
}

// ShopfloorCardLegalEntity returns the legal entity of the job card a shop-floor
// registration posted, for a registration.corrected event (which names no order). ok is
// false when the registration posted no card. The caller rule is ShopfloorOrderLegalEntity's.
func (s *Postgres) ShopfloorCardLegalEntity(ctx context.Context, registrationID string) (string, bool, error) {
	regID, ok := ids.Canonical(registrationID)
	if !ok {
		return "", false, nil
	}
	return s.shopfloorLegalEntity(ctx, `select legal_entity_id::text from production_journal where tenant_id = $1 and idempotency_key = $2 limit 1`, shopfloorJobCardKey+regID)
}

func (s *Postgres) shopfloorLegalEntity(ctx context.Context, query, arg string) (string, bool, error) {
	p, _ := tenant.FromContext(ctx)
	if p.LegalEntityID != "" || !p.HasDuty(DutyShopfloorJobCard) {
		return "", false, fmt.Errorf("%w: the legal entity lookup needs the tenant-level %s principal", ErrJobCardRefused, DutyShopfloorJobCard)
	}
	var le string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select set_config('passage.tenant_id', $1, true), set_config('passage.legal_entity_scopes', '*', true)`, p.TenantID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, query, p.TenantID, arg).Scan(&le)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return le, err == nil, err
}
