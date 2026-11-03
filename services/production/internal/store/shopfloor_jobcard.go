package store

import (
	"context"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
)

const (
	shopfloorJobCardKey     = "shopfloor-jobcard:"
	shopfloorJobCardPostKey = "shopfloor-jobcard-post:"
	shopfloorActor          = "shopfloor"
)

// ShopfloorJobCard is one closed shop-floor registration the w21 job-card
// consumer turns into a posted job card (FR-25.4, BR-25.9).
type ShopfloorJobCard struct {
	RegistrationID string
	Kind           string
	OrderRef       string
	OperationRef   string
	Hours          *decimal.Decimal
	CostRate       *decimal.Decimal
	Actor          string
}

// PostShopfloorJobCard posts a job-card journal for a closed shop-floor job-stop.
// Hours are valued at the worker's costRate when that rate is positive, otherwise
// the operation's resource labor rate. A repeated registration id is a no-op.
// Clock punches, open stops, non-UUID order refs and unknown operations are ignored.
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
	page, err := s.ListJobs(ctx, JobFilter{OrderID: orderID, JobType: domain.JobProcess, Limit: 100})
	if err != nil {
		return Journal{}, false, err
	}
	var jobID string
	for _, j := range page.Items {
		if j.OperNumber == oper {
			jobID = j.ID
			break
		}
	}
	if jobID == "" {
		return Journal{}, false, nil
	}
	actor := in.Actor
	if actor == "" {
		actor = shopfloorActor
	}
	key := shopfloorJobCardKey + in.RegistrationID
	j, _, err := s.CreateJournal(ctx, NewJournal{
		OrderID: orderID, Type: domain.JournalJobCard, Proposal: domain.ProposeNone, AutoStart: true,
		Hours: []HoursInput{{JobID: jobID, Hours: hours}},
		Actor: actor, IdempotencyKey: key, RequestHash: key,
	})
	if err != nil {
		return Journal{}, false, err
	}
	if j.Posted {
		return j, false, nil
	}
	posted, replayed, err := s.PostJournal(ctx, PostRequest{
		JournalID: j.ID, AutoStart: true, Actor: actor,
		TraceID: in.RegistrationID, IdempotencyKey: shopfloorJobCardPostKey + in.RegistrationID,
		LaborRate: positiveRate(in.CostRate),
	})
	if err != nil {
		return Journal{}, false, err
	}
	return posted, !replayed, nil
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
