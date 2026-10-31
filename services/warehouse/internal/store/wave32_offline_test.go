package store

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/warehouse/internal/domain"
)

// Wave 32 proof, server half of AC-32.3 (FR-32.5, BR-32.3): a device that was
// offline replays 200 queued ConfirmWorkLine transactions with
// Idempotency-Key = device_id:seq, in seq order, over a link that drops
// requests before they are sent and drops acknowledgements after the server
// applied them (the duplicate case). Every confirmation must be applied exactly
// once and in order, and replaying the whole batch again must change nothing.
// The client half (the queue never sends past a failure and never resends an
// applied transaction) is offlineQueue.test.ts in apps/web.
//
// Line i (1..200) puts i units of SKU-(i mod 4). Expected stock, derived by hand
// from the arithmetic series, independent of the engine:
//
//	SKU-1: 1+5+...+197 = 50 terms, mean 99  = 4950
//	SKU-2: 2+6+...+198 = 50 terms, mean 100 = 5000
//	SKU-3: 3+7+...+199 = 50 terms, mean 101 = 5050
//	SKU-0: 4+8+...+200 = 50 terms, mean 102 = 5100   (total 20100 = 200*201/2)
func TestWave32_OfflineReplay200ExactlyOnceInOrder(t *testing.T) {
	st, ctx, wh := setup(t)
	mustLocations(t, st, ctx, wh, loc("A-01", "A", 1, domain.Capacity{AllowMixedItems: true}))
	mustDirective(t, st, ctx, directive(wh, "PURCHASE_PUTAWAY", domain.DirectionPut, domain.StrategyConsolidate, "A"))

	const n = 200
	lines := make([]domain.NewLine, n)
	for i := range n {
		lines[i] = domain.NewLine{ItemID: fmt.Sprintf("SKU-%d", (i+1)%4), Direction: domain.DirectionPut, Qty: decimal.NewFromInt(int64(i + 1))}
	}
	w := plan(t, st, ctx, wh, "PURCHASE_PUTAWAY", lines...)
	const user = "worker-7"
	if got := assign(t, st, ctx, wh, user, time.Now()); got == nil || got.ID != w.ID || got.Version != 2 {
		t.Fatalf("assign = %+v", got)
	}

	idem := func(seq int) Idem {
		return Idem{Key: fmt.Sprintf("dev-7:%d", seq), Hash: fmt.Sprintf("POST /work/%s/lines/%d:confirm worker-7 qty=%d", w.ID, seq, seq)}
	}
	send := func(seq int) (domain.Work, bool, error) {
		return st.ConfirmLine(ctx, idem(seq), ConfirmInput{WorkID: w.ID, LineNo: seq, Qty: decimal.NewFromInt(int64(seq)), User: user, Now: time.Now()})
	}

	// Reconnect: replay in seq order. A request lost before sending is retried with
	// the same seq; a lost acknowledgement makes the device resend a transaction the
	// server already applied.
	rng := rand.New(rand.NewPCG(32, 3))
	var dropped, lostAcks int
	for seq := 1; seq <= n; {
		r := rng.Float64()
		if r < 0.15 {
			dropped++
			continue
		}
		out, replayed, err := send(seq)
		if err != nil || replayed {
			t.Fatalf("seq %d first delivery: replayed=%v err=%v", seq, replayed, err)
		}
		// Applied in order: the work version moves by exactly one per confirmation.
		if out.Version != 2+seq || out.Lines[seq-1].Status != domain.LineCompleted {
			t.Fatalf("seq %d: version %d line %s, want version %d COMPLETED", seq, out.Version, out.Lines[seq-1].Status, 2+seq)
		}
		if seq < n && out.Lines[seq].Status != domain.LineOpen {
			t.Fatalf("seq %d: line %d is %s before its confirmation arrived", seq, seq+1, out.Lines[seq].Status)
		}
		if r < 0.30 {
			lostAcks++
			again, replayed, err := send(seq)
			if err != nil || !replayed || again.Version != out.Version {
				t.Fatalf("seq %d resend after a lost ack: replayed=%v version=%d err=%v", seq, replayed, again.Version, err)
			}
		}
		seq++
	}
	if dropped == 0 || lostAcks == 0 {
		t.Fatalf("the link never failed (dropped=%d lost acks=%d); the test proves nothing", dropped, lostAcks)
	}
	t.Logf("200 confirmations replayed: %d requests dropped before send, %d acknowledgements lost after apply", dropped, lostAcks)

	want := map[string]string{"SKU-1": "4950", "SKU-2": "5000", "SKU-3": "5050", "SKU-0": "5100"}
	check := func(stage string) {
		t.Helper()
		got, err := st.GetWork(ctx, w.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != domain.WorkCompleted || got.Version != 2+n {
			t.Fatalf("%s: work %s version %d, want COMPLETED version %d", stage, got.Status, got.Version, 2+n)
		}
		for item, qty := range want {
			if q := stockAt(t, st, ctx, wh, "A-01", item); !q.Equal(dq(qty)) {
				t.Fatalf("%s: A-01 holds %s %s, want %s", stage, q, item, qty)
			}
		}
		// Each line carries the key of the one transaction that applied it.
		rows, err := adminOf(st).Query(context.Background(), `select line_no, confirm_key from work_line where work_id = $1 order by line_no`, w.ID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		count := 0
		for rows.Next() {
			var no int
			var k string
			if err := rows.Scan(&no, &k); err != nil {
				t.Fatal(err)
			}
			count++
			if k != fmt.Sprintf("dev-7:%d", no) {
				t.Fatalf("%s: line %d confirmed by %q", stage, no, k)
			}
		}
		if count != n {
			t.Fatalf("%s: %d lines, want %d", stage, count, n)
		}
		if c := outboxCount(t, adminOf(st), "dev.passage.wms.work.completed.v1"); c != 1 {
			t.Fatalf("%s: %d work.completed events, want 1", stage, c)
		}
	}
	check("after replay")

	// Replaying the same batch again (a device restored from an older sealed queue)
	// changes nothing: every key answers its first result.
	for seq := 1; seq <= n; seq++ {
		out, replayed, err := send(seq)
		if err != nil || !replayed || out.Version != 2+seq {
			t.Fatalf("second replay seq %d: replayed=%v version=%d err=%v", seq, replayed, out.Version, err)
		}
	}
	check("after a second replay of all 200")

	// A key reused for a different confirmation is refused and changes nothing.
	if _, _, err := st.ConfirmLine(ctx, Idem{Key: "dev-7:5", Hash: "a different request"}, ConfirmInput{WorkID: w.ID, LineNo: 6, Qty: dq("6"), User: user, Now: time.Now()}); !errors.Is(err, ErrIdempotencyReuse) {
		t.Fatalf("reused key: err = %v, want ErrIdempotencyReuse", err)
	}
	check("after a reused key")
}
