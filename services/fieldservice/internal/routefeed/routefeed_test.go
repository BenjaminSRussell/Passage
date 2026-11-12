package routefeed

import (
	"testing"
	"time"
)

func TestFeedWakesOnlyTheTechniciansOwnStreams(t *testing.T) {
	f := New()
	mine, stopMine := f.Subscribe("T1", "TECH-A")
	twin, stopTwin := f.Subscribe("t1", "tech-a")
	other, stopOther := f.Subscribe("t1", "tech-b")
	foreign, stopForeign := f.Subscribe("t2", "tech-a")
	defer stopOther()
	defer stopForeign()
	if f.Subscribers() != 4 {
		t.Fatalf("subscribers %d", f.Subscribers())
	}
	f.Publish(Payload("t1", "tech-a"))
	f.Publish(Payload("t1", "tech-a")) // coalesces: the slow reader gets one signal
	for name, ch := range map[string]<-chan struct{}{"mine": mine, "twin": twin} {
		select {
		case <-ch:
		case <-time.After(time.Second):
			t.Fatalf("%s was not woken", name)
		}
		select {
		case <-ch:
			t.Fatalf("%s got a backlog instead of one signal", name)
		default:
		}
	}
	for name, ch := range map[string]<-chan struct{}{"other technician": other, "other tenant": foreign} {
		select {
		case <-ch:
			t.Fatalf("%s was woken by someone else's route", name)
		default:
		}
	}
	stopMine()
	stopTwin()
	if f.Subscribers() != 2 {
		t.Fatalf("subscribers after stop %d", f.Subscribers())
	}
	f.Publish(Payload("t1", "tech-a")) // nobody listening: not a problem
}
