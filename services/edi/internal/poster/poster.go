// Package poster turns a mapped inbound 850 or ORDERS into a sales order: the
// route POST /messages/{id}:post posts one on demand, and RunOnce posts the
// mapped orders of partners that auto-post, under the service principal
// edi-sales.
package poster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/services/edi/internal/delivery"
	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	"github.com/BenjaminSRussell/passage/services/edi/internal/sources"
	"github.com/BenjaminSRussell/passage/services/edi/internal/store"
)

const (
	ServicePrincipal = "edi-sales"

	DefaultBatch = 20
	DefaultLease = 5 * time.Minute
)

// Intake places an order with the sales service (POST /v1/sales/orders under
// the service-only duty sales.channel.intake).
type Intake interface {
	PlaceOrder(ctx context.Context, legalEntityID, key string, order domain.SalesOrderRequest) (domain.SalesOrderRef, error)
}

var (
	ErrNoTradeProfile  = errors.New("partner has no trade profile")
	ErrNoSalesDefaults = errors.New("partner has no sales defaults")
	ErrNotConfigured   = errors.New("sales intake is not configured")
)

// NotPostableError says why a message is not an order that can be posted.
type NotPostableError struct{ Detail string }

func (e *NotPostableError) Error() string { return e.Detail }

// ParkedError is a message parked (FAILED with its park code) instead of posted.
type ParkedError struct{ domain.Park }

type Result struct {
	MessageID    string `json:"messageId"`
	Status       string `json:"status"`
	SalesOrderID string `json:"salesOrderId"`
}

type Poster struct {
	Store  *store.Store
	Intake Intake
	// PlaceAs gives the context the sales call is signed under, for the legal
	// entity of the order; nil signs under the principal already in the context.
	PlaceAs func(ctx context.Context, legalEntityID string) (context.Context, error)
	Batch   int
	Lease   time.Duration
	Now     func() time.Time
}

// AsServicePrincipal signs the sales call as the service principal edi-sales
// (only sales.channel.intake) in the legal entity of the order.
func AsServicePrincipal(ctx context.Context, legalEntityID string) (context.Context, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return nil, errors.New("sales intake: no tenant in context")
	}
	sp, err := tenant.ServicePrincipal(ServicePrincipal, p.TenantID, legalEntityID)
	if err != nil {
		return nil, err
	}
	return tenant.With(ctx, sp), nil
}

func (p *Poster) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// Post turns the inbound 850 (or ORDERS) into a sales order under the service
// grant edi -> sales for sales.channel.intake, in the legal entity of the
// partner's trade profile. The sales request is built from the stored canonical
// order, the partner's item cross-references, unit map and defaults, and pinned
// to the message so a retry sends the same body; the Idempotency-Key is the
// message id and the externalRef the partner's PO number, so posting twice, or
// the same PO in a second interchange, yields the one sales order. An order that
// cannot be mapped or that sales refuses is parked and a *ParkedError returned;
// any other error is transient or a missing setup and the message stays as it was.
func (p *Poster) Post(ctx context.Context, id string) (Result, error) {
	if p.Intake == nil {
		return Result{}, ErrNotConfigured
	}
	st, err := p.Store.MessageForPost(ctx, id)
	if err != nil {
		return Result{}, err
	}
	if st.Direction != "IN" || (st.TransactionSet != "850" && st.TransactionSet != "ORDERS") {
		return Result{}, &NotPostableError{"only an inbound 850 or ORDERS is posted to sales"}
	}
	if st.Status == "POSTED" {
		return Result{MessageID: id, Status: st.Status, SalesOrderID: st.SalesOrderID}, nil
	}
	if !st.Postable() {
		return Result{}, &NotPostableError{"message is " + st.Status + "; only a mapped or parked order is posted (reprocess a failed mapping first)"}
	}
	var order domain.Order
	if err := json.Unmarshal(st.Canonical, &order); err != nil {
		return Result{}, err
	}
	profile, err := p.Store.GetTradeProfile(ctx, st.PartnerID)
	if errors.Is(err, store.ErrNotFound) {
		return Result{}, ErrNoTradeProfile
	}
	if err != nil {
		return Result{}, err
	}
	defaults, err := p.Store.GetSalesDefaults(ctx, st.PartnerID)
	if errors.Is(err, store.ErrNotFound) {
		return Result{}, ErrNoSalesDefaults
	}
	if err != nil {
		return Result{}, err
	}
	req, pinned := domain.SalesOrderRequest{}, len(st.SalesRequest) > 0
	if pinned {
		if err := json.Unmarshal(st.SalesRequest, &req); err != nil {
			return Result{}, err
		}
	} else {
		refs := make([]domain.ItemRef, 0, len(order.Lines))
		for _, l := range order.Lines {
			if l.ItemRef != nil {
				refs = append(refs, *l.ItemRef)
			}
		}
		xrefs, err := p.Store.LookupXrefs(ctx, st.PartnerID, refs)
		if err != nil {
			return Result{}, err
		}
		built, park := domain.BuildSalesOrder(st.PartnerID, order, profile, defaults, xrefs)
		if park != nil {
			return Result{}, p.park(ctx, id, *park)
		}
		if req, err = p.Store.PinSalesRequest(ctx, id, built); err != nil {
			return Result{}, err
		}
	}
	placeCtx := ctx
	if p.PlaceAs != nil {
		if placeCtx, err = p.PlaceAs(ctx, profile.LegalEntityID); err != nil {
			return Result{}, fmt.Errorf("%w: %v", sources.ErrForbidden, err)
		}
	}
	ref, err := p.Intake.PlaceOrder(placeCtx, profile.LegalEntityID, id, req)
	var refused *sources.RefusedError
	if errors.As(err, &refused) {
		return Result{}, p.park(ctx, id, domain.Park{Code: domain.CodeSalesRefused, Detail: "sales " + refused.Code + ": " + refused.Detail})
	}
	if err != nil {
		return Result{}, err
	}
	if err := p.Store.MarkPosted(ctx, id, ref.ID); err != nil {
		return Result{}, fmt.Errorf("record the posted order %s: %w", ref.ID, err)
	}
	return Result{MessageID: id, Status: "POSTED", SalesOrderID: ref.ID}, nil
}

func (p *Poster) park(ctx context.Context, id string, park domain.Park) error {
	if err := p.Store.ParkMessage(ctx, id, park.Code, park.Detail); err != nil {
		return err
	}
	slog.WarnContext(ctx, "edi order parked", "messageId", id, "parkCode", park.Code, "detail", park.Detail)
	return &ParkedError{park}
}

// RunOnce posts the mapped inbound orders of partners that auto-post, tenant by
// tenant, each under the service principal edi-sales, and returns how many it
// tried. An order that sales cannot take now (an outage, a missing trade profile
// or grant) stays MAPPED and is tried again after delivery.Backoff of its
// attempts; a parked one waits for the cause to be fixed and a manual :post.
func (p *Poster) RunOnce(ctx context.Context) (int, error) {
	tenants, err := p.Store.PostDueTenants(ctx)
	if err != nil {
		return 0, err
	}
	batch, lease := p.Batch, p.Lease
	if batch <= 0 {
		batch = DefaultBatch
	}
	if lease <= 0 {
		lease = DefaultLease
	}
	n := 0
	var failed []error
	for _, tid := range tenants {
		tctx := store.SystemContext(ctx, tid)
		claimed, err := p.Store.ClaimPosts(tctx, batch, lease)
		if err != nil {
			failed = append(failed, fmt.Errorf("tenant %s: %w", tid, err))
			continue
		}
		for _, c := range claimed {
			n++
			_, err := p.Post(tctx, c.ID)
			var parked *ParkedError
			var notPostable *NotPostableError
			switch {
			case err == nil, errors.As(err, &parked), errors.As(err, &notPostable), errors.Is(err, store.ErrNotFound):
				slog.InfoContext(ctx, "edi auto-post", "tenant", tid, "messageId", c.ID, "attempt", c.Attempts, "err", err)
			default:
				retryAt := p.now().Add(delivery.Backoff(c.Attempts))
				slog.WarnContext(ctx, "edi auto-post failed, will retry", "tenant", tid, "messageId", c.ID, "attempt", c.Attempts, "retryAt", retryAt, "err", err)
				if derr := p.Store.DeferPost(tctx, c.ID, retryAt); derr != nil {
					failed = append(failed, fmt.Errorf("tenant %s message %s: %w", tid, c.ID, derr))
				}
			}
		}
	}
	return n, errors.Join(failed...)
}
