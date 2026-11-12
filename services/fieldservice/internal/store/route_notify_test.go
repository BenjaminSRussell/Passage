package store

import (
	"context"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/domain"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/routefeed"
)

// A change of a technician's route (assignment, publication, checklist, manual status move) raises a NOTIFY in its own
// transaction that wakes the technician's open stream through a listening feed, and nobody else's (#45).
func TestStore_RouteChangesWakeTheTechniciansStream(t *testing.T) {
	d := newDispatch(t, 35)
	wo, tech := d.dispatchedWith(t, "SSE", nil, []domain.ChecklistItem{{ID: ids.New(), Text: "Lock out", Required: true}})
	bystander := d.technician(t, "BYS")

	feed := routefeed.New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go feed.Run(ctx, d.s.pool)
	mine, stopMine := feed.Subscribe(tenantOf(d.ctx), tech.ID)
	defer stopMine()
	other, stopOther := feed.Subscribe(tenantOf(d.ctx), bystander.ID)
	defer stopOther()

	// The listener connects asynchronously: probe until it hears a notification before the real changes start.
	probe := func() {
		deadline := time.After(10 * time.Second)
		for {
			if err := d.s.notifyForTest(d.ctx, tech.ID); err != nil {
				t.Fatal(err)
			}
			select {
			case <-mine:
				return
			case <-time.After(100 * time.Millisecond):
			case <-deadline:
				t.Fatal("the feed never heard a notification")
			}
		}
	}
	probe()
	drain := func(ch <-chan struct{}) {
		for {
			select {
			case <-ch:
			case <-time.After(150 * time.Millisecond):
				return
			}
		}
	}
	drain(mine)
	expectSignal := func(what string) {
		t.Helper()
		select {
		case <-mine:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s did not wake the technician's stream", what)
		}
		drain(mine)
	}

	cur, _ := d.s.GetWorkOrder(d.ctx, wo.ID)
	if _, err := d.s.SetChecklist(d.ctx, wo.ID, cur.Version, []domain.ChecklistItem{{ID: ids.New(), Text: "Lock out and tag", Required: true}}); err != nil {
		t.Fatal(err)
	}
	expectSignal("a checklist change")

	// A new order scheduled for the technician and then given to someone else: the first assignment and the unassignment wake
	// the technician, the second assignment wakes only the one who gets it.
	second := d.order(t, 60, nil, nil)
	second = d.assign(t, second, tech, time.Date(2026, 10, 27, 15, 0, 0, 0, time.UTC))
	expectSignal("an assignment")
	cur, _ = d.s.GetWorkOrder(d.ctx, second.ID)
	if _, err := d.s.UnassignWorkOrder(d.ctx, cur.ID, cur.Version, time.Now()); err != nil {
		t.Fatal(err)
	}
	expectSignal("an unassignment")
	cur, _ = d.s.GetWorkOrder(d.ctx, second.ID)
	d.assign(t, cur, bystander, time.Date(2026, 10, 27, 15, 0, 0, 0, time.UTC))
	select {
	case <-other:
	case <-time.After(5 * time.Second):
		t.Fatal("an assignment did not wake the technician who got the work order")
	}
	select {
	case <-mine:
		t.Fatal("the technician's stream was woken by a change that is not on their route")
	case <-time.After(300 * time.Millisecond):
	}
}
