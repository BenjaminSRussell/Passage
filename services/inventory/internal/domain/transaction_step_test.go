package domain_test

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"

	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
)

func TestValidateStepTable(t *testing.T) {
	const (
		step   = domain.CommandStep
		unp    = domain.CommandUnpick
		unreg  = domain.CommandUnregister
		cancel = domain.CommandCancelReservation
	)
	cases := []struct {
		name     string
		cmd      domain.StepCommand
		from, to domain.Status
		want     error
	}{
		{"forward skip allowed", step, domain.StatusOnOrder, domain.StatusDeducted, nil},
		{"forward receipt to financial", step, domain.StatusReceived, domain.StatusPurchased, nil},
		{"same status is not a step", step, domain.StatusPicked, domain.StatusPicked, domain.ErrBackwardStep},
		{"backward under STEP refused", step, domain.StatusPicked, domain.StatusReservedPhysical, domain.ErrBackwardStep},
		{"direction change refused", step, domain.StatusDeducted, domain.StatusArrived, domain.ErrBackwardStep},
		{"unpick is named", unp, domain.StatusPicked, domain.StatusReservedPhysical, nil},
		{"unpick other move refused", unp, domain.StatusDeducted, domain.StatusPicked, domain.ErrNotNamedMove},
		{"unregister is named", unreg, domain.StatusRegistered, domain.StatusArrived, nil},
		{"unregister wrong pair refused", unreg, domain.StatusArrived, domain.StatusRegistered, domain.ErrNotNamedMove},
		{"unregister cannot skip back to ordered", unreg, domain.StatusRegistered, domain.StatusOrdered, domain.ErrNotNamedMove},
		{"cancel physical reservation", cancel, domain.StatusReservedPhysical, domain.StatusOnOrder, nil},
		{"cancel ordered reservation", cancel, domain.StatusReservedOrdered, domain.StatusOnOrder, nil},
		{"cancel reservation of picked stock refused", cancel, domain.StatusPicked, domain.StatusOnOrder, domain.ErrNotNamedMove},
		{"create is not a move", domain.CommandCreate, domain.StatusOrdered, domain.StatusArrived, domain.ErrUnknownCommand},
		{"unknown command", domain.StepCommand("CANCEL"), domain.StatusPicked, domain.StatusDeducted, domain.ErrUnknownCommand},
		{"unknown status", step, domain.Status("LOST"), domain.StatusDeducted, domain.ErrUnknownStatus},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := domain.ValidateStep(c.cmd, c.from, c.to)
			if c.want == nil {
				if err != nil {
					t.Fatalf("want nil, got %v", err)
				}
				return
			}
			if !errors.Is(err, c.want) {
				t.Fatalf("want %v, got %v", c.want, err)
			}
		})
	}
}

func TestNamedMoveAppliesOnlyToItsSources(t *testing.T) {
	if to, ok := domain.NamedMove(domain.CommandUnregister, domain.StatusRegistered); !ok || to != domain.StatusArrived {
		t.Fatalf("unregister from REGISTERED: %s %v", to, ok)
	}
	if _, ok := domain.NamedMove(domain.CommandUnregister, domain.StatusReceived); ok {
		t.Fatal("unregister must not apply to a RECEIVED receipt")
	}
	if _, ok := domain.NamedMove(domain.CommandStep, domain.StatusPicked); ok {
		t.Fatal("STEP is not a named move")
	}
}

func TestValidateCreate(t *testing.T) {
	pos, neg := decimal.NewFromInt(5), decimal.NewFromInt(-5)
	cases := []struct {
		name   string
		qty    decimal.Decimal
		status domain.Status
		want   error
	}{
		{"ordered receipt", pos, domain.StatusOrdered, nil},
		{"registered receipt", pos, domain.StatusRegistered, nil},
		{"on-order issue", neg, domain.StatusOnOrder, nil},
		{"picked issue", neg, domain.StatusPicked, nil},
		{"receipt at an issue status", pos, domain.StatusOnOrder, domain.ErrBackwardStep},
		{"issue at a receipt status", neg, domain.StatusArrived, domain.ErrBackwardStep},
		{"financial receipt refused", pos, domain.StatusPurchased, domain.ErrBackwardStep},
		{"financial issue refused", neg, domain.StatusSold, domain.ErrBackwardStep},
		{"unknown status", pos, domain.Status("LOST"), domain.ErrUnknownStatus},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := domain.ValidateCreate(c.qty, c.status)
			if c.want == nil && err != nil {
				t.Fatalf("want nil, got %v", err)
			}
			if c.want != nil && !errors.Is(err, c.want) {
				t.Fatalf("want %v, got %v", c.want, err)
			}
		})
	}
	if err := domain.ValidateCreate(decimal.Zero, domain.StatusOrdered); err == nil {
		t.Fatal("zero quantity must be refused")
	}
}

func TestBucketQtyReadsTheStatusBucket(t *testing.T) {
	o := domain.OnHand{PostedQty: decimal.NewFromInt(7), Registered: decimal.NewFromInt(3), OnOrder: decimal.NewFromInt(2)}
	if got := o.BucketQty(domain.StatusRegistered); !got.Equal(decimal.NewFromInt(3)) {
		t.Fatalf("registered %s", got)
	}
	if got := o.BucketQty(domain.StatusPurchased); !got.Equal(decimal.NewFromInt(7)) {
		t.Fatalf("financial reads posted qty, got %s", got)
	}
	if got := o.BucketQty(domain.StatusOnOrder); !got.Equal(decimal.NewFromInt(2)) {
		t.Fatalf("on order %s", got)
	}
}

// TestStatusMovesAreMonotonicUnlessNamed is the PRD 3.16 test-plan property
// over ApplyTransactionStep (BR-16.1): over any sequence of commands, a
// transaction never moves backward or changes direction except through a named
// command, and each named command performs only its own move. It also replays
// the moves on a projection row and checks the row equals a rebuild from the
// transaction's final status (FR-16.3).
func TestStatusMovesAreMonotonicUnlessNamed(t *testing.T) {
	all := domain.AllStatuses()
	cmds := append([]domain.StepCommand{domain.CommandStep}, domain.NamedCommands()...)
	rapid.Check(t, func(rt *rapid.T) {
		cur := rapid.SampledFrom(all).Draw(rt, "start")
		dir, _, _ := domain.Lookup(cur)
		qty := decimal.NewFromInt(int64(rapid.IntRange(1, 1000).Draw(rt, "qty")))
		if dir == domain.IssueDirection {
			qty = qty.Neg()
		}
		var live domain.OnHand
		if err := live.Post(cur, qty, decimal.Zero, 1); err != nil {
			rt.Fatal(err)
		}
		steps := rapid.IntRange(1, 60).Draw(rt, "steps")
		for i := 0; i < steps; i++ {
			cmd := rapid.SampledFrom(cmds).Draw(rt, "cmd")
			to := rapid.SampledFrom(all).Draw(rt, "to")
			if domain.ValidateStep(cmd, cur, to) != nil {
				continue
			}
			curDir, curRank, _ := domain.Lookup(cur)
			toDir, toRank, _ := domain.Lookup(to)
			if curDir != toDir {
				rt.Fatalf("direction changed %s -> %s under %s", cur, to, cmd)
			}
			if toRank < curRank {
				if cmd == domain.CommandStep {
					rt.Fatalf("backward move %s -> %s under STEP", cur, to)
				}
				if want, ok := domain.NamedMove(cmd, cur); !ok || want != to {
					rt.Fatalf("named command %s moved %s -> %s", cmd, cur, to)
				}
			}
			if toRank == curRank {
				rt.Fatalf("no-op move %s accepted under %s", cur, cmd)
			}
			if err := live.Post(cur, qty, decimal.Zero, -1); err != nil {
				rt.Fatal(err)
			}
			if err := live.Post(to, qty, decimal.Zero, 1); err != nil {
				rt.Fatal(err)
			}
			cur = to
		}
		rebuilt, err := domain.Rebuild([]domain.Contribution{{Status: cur, Qty: qty}})
		if err != nil {
			rt.Fatal(err)
		}
		if diff := domain.Diff(live, rebuilt); len(diff) != 0 {
			rt.Fatalf("projection differs from a rebuild at %s: %v", cur, diff)
		}
	})
}
