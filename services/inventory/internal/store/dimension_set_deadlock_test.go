package store_test

import (
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
)

// Regression (found by the Wave 16 AC2 fixture): dimension sets are shared by
// every product of a tenant and inserted on first use. Adding lines and posting
// inserted them in line / row-key order, so two journals of different products
// that introduce the same new dimension sets in a different order each held one
// uncommitted insert and waited on the other's: Postgres aborted one with
// 40P01 (deadlock detected), a 500 to the caller. Both now insert the dimension
// sets of the whole request first, in hash order, before any other lock.
func TestConcurrentJournalsWithSharedNewDimensionSetsDoNotDeadlock(t *testing.T) {
	_, app, _ := setup(t)
	hot := wideStore(t, app, 24)
	ctx := entityCtx(1, ids.New())
	const rounds, writers, sets = 8, 16, 24
	for round := 0; round < rounds; round++ {
		shared := make([]domain.Dimensions, sets)
		for i := range shared {
			shared[i] = dims("location", fmt.Sprintf("R%d-L%02d", round, i))
		}
		var wg sync.WaitGroup
		errs := make([]error, writers)
		for w := 0; w < writers; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				rng := rand.New(rand.NewPCG(uint64(round), uint64(w)))
				product := ids.New() // a product of its own: no on-hand row is shared
				var lines []domain.JournalLine
				for _, i := range rng.Perm(sets) {
					lines = append(lines, line(product, "1", "1", shared[i]))
				}
				j, err := newJournal(ctx, hot, domain.JournalOpeningBalance, lines...)
				if err == nil {
					_, err = hot.PostJournal(ctx, j.ID, j.Version, postParams())
				}
				errs[w] = err
			}(w)
		}
		wg.Wait()
		for w, err := range errs {
			if err != nil {
				t.Fatalf("round %d writer %d: %v", round, w, err)
			}
		}
	}
}
