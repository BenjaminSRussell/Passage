package store

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"

	ediev "github.com/BenjaminSRussell/passage/services/edi/internal/events"
)

func TestReprocessInterchanges(t *testing.T) {
	pool := migrated(t)
	ctx := testkit.Ctx(testkit.TenantID(14), "edi.operate")
	st := New(pool, 30)
	p, _, err := st.CreatePartner(ctx, "k", "h", profile("BULK"))
	if err != nil {
		t.Fatal(err)
	}
	a := receive(t, st, ctx, p.ID, "000000021", msg("850", "0001", "FAILED", "EDI-0105: no item cross-reference"))
	b := receive(t, st, ctx, p.ID, "000000022", msg("850", "0001", "FAILED", "EDI-0105: no item cross-reference"))
	unreadable := receive(t, st, ctx, p.ID, "000000023", msg("850", "0001", "FAILED", "EDI-0160: BEG missing"))
	missing := ids.New()

	// The plan maps every message again, except that the third payload no
	// longer parses.
	plan := func(raw string, failed []Message) ([]Message, error) {
		if raw == "raw 000000023" {
			return nil, fmt.Errorf("%w: ISA is 80 characters", ErrUnreadable)
		}
		out := make([]Message, 0, len(failed))
		for _, m := range failed {
			out = append(out, Message{ID: m.ID, Status: "MAPPED", Canonical: []byte(`{"poNumber":"PO-OK"}`)})
		}
		return out, nil
	}
	list := []string{b.InterchangeID, missing, unreadable.InterchangeID, a.InterchangeID}
	received := outboxCount(t, pool, ediev.MessageReceived)

	res, replayed, err := st.ReprocessInterchanges(ctx, "bulk", "h-bulk", list, false, plan)
	if err != nil || replayed {
		t.Fatalf("bulk: replayed=%v err=%v", replayed, err)
	}
	if res.Requested != 4 || res.Changed != 2 || len(res.Items) != 4 {
		t.Fatalf("result = %+v", res)
	}
	wantOutcome := []string{BulkReprocessed, BulkNotFound, BulkUnreadable, BulkReprocessed}
	for i, it := range res.Items {
		if it.InterchangeID != list[i] || it.Outcome != wantOutcome[i] {
			t.Fatalf("item %d = %+v, want %s for %s (request order)", i, it, wantOutcome[i], list[i])
		}
	}
	if it := res.Items[2]; it.Changed != 0 || len(it.Messages) != 0 || !strings.Contains(it.Error, "80 characters") {
		t.Fatalf("unreadable item = %+v", it)
	}
	if m, _ := st.GetMessage(ctx, unreadable.Messages[0].ID); m.Status != "FAILED" {
		t.Fatalf("an unreadable interchange must be left alone: %+v", m)
	}
	for _, r := range []Received{a, b} {
		if m, _ := st.GetMessage(ctx, r.Messages[0].ID); m.Status != "MAPPED" || !strings.Contains(string(m.Canonical), "PO-OK") {
			t.Fatalf("message after bulk = %+v", m)
		}
	}
	if got := outboxCount(t, pool, ediev.MessageReceived); got != received+2 {
		t.Fatalf("message.received events = %d, want %d", got, received+2)
	}

	// A replay under the key returns the stored result without running the plan.
	again, replayed, err := st.ReprocessInterchanges(ctx, "bulk", "h-bulk", list, false, func(string, []Message) ([]Message, error) {
		t.Fatal("a replay must not plan again")
		return nil, nil
	})
	if err != nil || !replayed || again.Changed != 2 || len(again.Items) != 4 {
		t.Fatalf("replay = %+v replayed=%v err=%v", again, replayed, err)
	}
	if _, _, err := st.ReprocessInterchanges(ctx, "bulk", "other", list[:1], false, plan); !errors.Is(err, ErrIdempotencyReuse) {
		t.Fatalf("reuse err = %v", err)
	}

	// A plan failure that is not ErrUnreadable aborts the whole run.
	c := receive(t, st, ctx, p.ID, "000000024", msg("850", "0001", "FAILED", "EDI-0160: BEG missing"))
	boom := errors.New("boom")
	if _, _, err := st.ReprocessInterchanges(ctx, "bulk-2", "h2", []string{c.InterchangeID}, false, func(string, []Message) ([]Message, error) {
		return nil, boom
	}); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if _, _, err := st.ReprocessInterchanges(ctx, "", "h3", nil, true, plan); err == nil {
		t.Fatal("an empty list must be refused")
	}

}
