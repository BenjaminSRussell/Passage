// Package demoplant seeds a small demo plant into the production store (W21 AC4, issue #478): one legal entity with an
// All x All posting profile and five production orders spread over the lifecycle, from estimated to ended with variances,
// so the order list, the cost reads, WIP and the GL posting run have something to show. The amounts come from the same
// BOM and routing shapes the store tests prove (BR-21.7), so a demo reconciles to known numbers: WIP 1951.00 for each
// partially reported order, 1890.00 quantity and 61.00 price variance on the ended one.
//
// Seed is replay-safe: every write carries an idempotency key derived from Config.Prefix, an order that already exists
// is left as it is, and an existing posting profile is kept. A run interrupted part way leaves the orders it reached;
// seed again under another prefix for a fresh set.
package demoplant

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	"github.com/BenjaminSRussell/passage/services/production/internal/store"
)

// DefaultPrefix keys the default demo set.
const DefaultPrefix = "demo-plant"

const actor = "demo-plant-seed"

// Profile is the posting profile the demo legal entity gets: the accounts the ledger chart must hold to book it.
var Profile = domain.Profile{ItemGroup: domain.AllKey, SiteID: domain.AllKey, WIP: "1450", Inventory: "1400", FinishedGoods: "1410",
	LaborAbsorption: "5510", OverheadAbsorption: "5520", VarLotSize: "5610", VarQuantity: "5620", VarPrice: "5630", VarSubstitution: "5640"}

// Accounts are the distinct accounts of Profile.
func Accounts() []string {
	p := Profile
	return []string{p.WIP, p.Inventory, p.FinishedGoods, p.LaborAbsorption, p.OverheadAbsorption, p.VarLotSize, p.VarQuantity, p.VarPrice, p.VarSubstitution}
}

// Config names where the plant goes. The context's principal must be a user of LegalEntityID.
type Config struct {
	LegalEntityID string
	Currency      string
	Prefix        string
	// PostingDate dates the journals; empty lets the store date them today.
	PostingDate string
	// Ledger and LedgerID, when both set, book the seeded WIP in the general ledger and reconcile it.
	Ledger   ledgerclient.Ledger
	LedgerID string
}

// Order is one seeded order.
type Order struct {
	Name     string        `json:"name"`
	ID       string        `json:"id"`
	Number   string        `json:"orderNumber"`
	Status   domain.Status `json:"status"`
	Existing bool          `json:"existing"`
}

// Result is what a seed run left.
type Result struct {
	LegalEntityID  string                   `json:"legalEntityId"`
	ProfileCreated bool                     `json:"profileCreated"`
	Orders         []Order                  `json:"orders"`
	GL             *store.GLPostResult      `json:"gl,omitempty"`
	Reconciliation *store.WIPReconciliation `json:"reconciliation,omitempty"`
}

// stage is how far an order is taken.
type stage int

const (
	estimated stage = iota
	released
	inProgress
	partiallyReported
	ended
)

type plan struct {
	name  string
	stage stage
}

// plans is the demo set: name and how far each order goes.
var plans = []plan{
	{"bracket-estimated", estimated},
	{"bracket-released", released},
	{"housing-in-progress", inProgress},
	{"housing-partial-raf", partiallyReported},
	{"gearbox-ended", ended},
}

// StableID derives a UUID-shaped id from parts, so a rerun names the same items, sites and resources.
func StableID(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	b := h.Sum(nil)[:16]
	b[6] = (b[6] & 0x0f) | 0x80 // version 8 (custom)
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// newOrder is the BR-21.7 shape: 100 units, 3 units of raw material per unit at 10.00 (2 direct, 1 under a phantom),
// one operation of 0.5 h per unit at 30.00 labor and 10.00 overhead.
func newOrder(cfg Config, name string) (store.NewOrder, error) {
	id := func(kind string) string { return StableID(cfg.Prefix, cfg.LegalEntityID, name, kind) }
	rm := StableID(cfg.Prefix, cfg.LegalEntityID, "raw-material")
	bom := []domain.BOMComponent{
		{LineNo: "10", ItemID: rm, QtyPer: d("2"), UnitCost: d("10")},
		{LineNo: "20", Phantom: true, QtyPer: d("1"), Children: []domain.BOMComponent{{ItemID: rm, QtyPer: d("1"), UnitCost: d("10")}}},
	}
	lines, err := domain.Explode(bom, d("100"))
	if err != nil {
		return store.NewOrder{}, err
	}
	ops, err := domain.PlanOperations([]domain.Operation{{OperNumber: 10, ResourceID: StableID(cfg.Prefix, cfg.LegalEntityID, "work-center-10"),
		RunHPer: d("0.5"), LaborRate: d("30"), OverheadRate: d("10")}}, d("100"))
	if err != nil {
		return store.NewOrder{}, err
	}
	key := cfg.Prefix + ":" + name
	return store.NewOrder{LegalEntityID: cfg.LegalEntityID, ProductID: id("product"), SiteID: StableID(cfg.Prefix, cfg.LegalEntityID, "site"),
		WarehouseID: StableID(cfg.Prefix, cfg.LegalEntityID, "warehouse"), Currency: cfg.Currency, Quantity: d("100"), OriginType: "Manual",
		Lines: lines, Operations: ops, EstimateOnCreate: true, TraceID: key, IdempotencyKey: key, RequestHash: key}, nil
}

type driver struct {
	ctx   context.Context
	s     *store.Postgres
	cfg   Config
	name  string
	order store.Order
}

func (r *driver) key(step string) string { return r.cfg.Prefix + ":" + r.name + ":" + step }

func (r *driver) transition(cmd domain.Command, mut func(*store.Transition)) error {
	t := store.Transition{OrderID: r.order.ID, IfMatch: r.order.Version, Command: cmd, Actor: actor, TraceID: r.key(string(cmd)),
		IdempotencyKey: r.key(string(cmd)), RequestHash: string(cmd)}
	if mut != nil {
		mut(&t)
	}
	o, _, err := r.s.Transition(r.ctx, t)
	if err != nil {
		return fmt.Errorf("%s %s: %w", r.name, cmd, err)
	}
	r.order = o
	return nil
}

func (r *driver) journal(step string, in store.NewJournal) error {
	in.OrderID, in.Actor, in.PostingDate = r.order.ID, actor, r.cfg.PostingDate
	in.IdempotencyKey, in.RequestHash, in.Proposal = r.key(step), r.key(step), domain.ProposeRemaining
	j, _, err := r.s.CreateJournal(r.ctx, in)
	if err != nil {
		return fmt.Errorf("%s %s journal: %w", r.name, step, err)
	}
	if _, _, err := r.s.PostJournal(r.ctx, store.PostRequest{JournalID: j.ID, OverConsumptionPct: decimal.NewFromInt(10), AutoStart: true,
		Actor: actor, TraceID: r.key(step + ":post"), IdempotencyKey: r.key(step + ":post")}); err != nil {
		return fmt.Errorf("%s %s post: %w", r.name, step, err)
	}
	o, err := r.s.GetOrder(r.ctx, r.order.ID)
	if err != nil {
		return err
	}
	r.order = o
	return nil
}

func (r *driver) run(to stage) error {
	if to >= released {
		for _, cmd := range []domain.Command{domain.CmdSchedule, domain.CmdRelease} {
			if err := r.transition(cmd, nil); err != nil {
				return err
			}
		}
	}
	if to < inProgress {
		return nil
	}
	if err := r.transition(domain.CmdStart, nil); err != nil {
		return err
	}
	price := d("10.20")
	if err := r.journal("picking-list", store.NewJournal{Type: domain.JournalPickingList, Issues: []store.IssueInput{
		{LineNo: "10", Qty: d("203"), UnitCost: &price}, {LineNo: "20.1", Qty: d("102"), UnitCost: &price}}}); err != nil {
		return err
	}
	if err := r.journal("route-card", store.NewJournal{Type: domain.JournalRouteCard, Hours: []store.HoursInput{{OperNumber: 10, Hours: d("46"), QtyGood: d("60")}}}); err != nil {
		return err
	}
	if to < partiallyReported {
		return nil
	}
	if err := r.transition(domain.CmdReportFinished, func(t *store.Transition) { t.Good = d("60") }); err != nil {
		return err
	}
	if to < ended {
		return nil
	}
	return r.transition(domain.CmdEnd, nil)
}

// Seed writes the demo plant and, with a ledger configured, books and reconciles its WIP.
func Seed(ctx context.Context, s *store.Postgres, cfg Config) (Result, error) {
	if !ids.Valid(cfg.LegalEntityID) {
		return Result{}, errors.New("demo plant needs a legal entity UUID")
	}
	if cfg.Currency == "" {
		cfg.Currency = "USD"
	}
	if cfg.Prefix == "" {
		cfg.Prefix = DefaultPrefix
	}
	res := Result{LegalEntityID: cfg.LegalEntityID, Orders: []Order{}}
	_, created, err := s.SavePostingProfile(ctx, store.ProfileSave{LegalEntityID: cfg.LegalEntityID, Profile: Profile, Actor: actor})
	var p *httpx.Problem
	switch {
	case errors.As(err, &p) && p.Status == http.StatusPreconditionRequired:
		// The legal entity already has an All x All profile; keep it.
	case err != nil:
		return res, fmt.Errorf("posting profile: %w", err)
	}
	res.ProfileCreated = created

	for _, pl := range plans {
		in, err := newOrder(cfg, pl.name)
		if err != nil {
			return res, err
		}
		o, createdOrder, err := s.CreateOrder(ctx, in)
		if err != nil {
			return res, fmt.Errorf("%s create: %w", pl.name, err)
		}
		r := &driver{ctx: ctx, s: s, cfg: cfg, name: pl.name, order: o}
		if createdOrder {
			if err := r.run(pl.stage); err != nil {
				return res, err
			}
		} else if r.order, err = s.GetOrder(ctx, o.ID); err != nil {
			return res, err
		}
		res.Orders = append(res.Orders, Order{Name: pl.name, ID: r.order.ID, Number: r.order.OrderNumber, Status: r.order.Status, Existing: !createdOrder})
	}

	if cfg.Ledger == nil || cfg.LedgerID == "" {
		return res, nil
	}
	posted, err := s.PostToGL(ctx, cfg.Ledger, store.GLPostRequest{LegalEntityID: cfg.LegalEntityID, LedgerID: cfg.LedgerID})
	if err != nil {
		return res, fmt.Errorf("book WIP in the general ledger: %w", err)
	}
	res.GL = &posted
	rec, err := s.ReconcileWIP(ctx, cfg.Ledger, store.GLReconcileRequest{LegalEntityID: cfg.LegalEntityID, LedgerID: cfg.LedgerID, Currency: cfg.Currency})
	if err != nil {
		return res, fmt.Errorf("reconcile WIP: %w", err)
	}
	res.Reconciliation = &rec
	return res, nil
}
