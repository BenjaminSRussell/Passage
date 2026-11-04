package store

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
)

// Every configuration PUT: create without a tag, replace only with the current tag (428 without, 412
// stale), the tag increments on each replace, and of two replaces sent with the same tag one wins.
func TestPostgres_ConfigurationReplaceNeedsCurrentTag(t *testing.T) {
	e, _ := setup(t)
	e = tenantWide(e)
	structID := ids.New()
	templateID := ids.New()
	from := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	type row struct {
		name string
		// put replaces (or creates) the row with the given tag and returns the row's new tag.
		put func(tag int, label string) (int, bool, error)
		get func() int
	}
	rows := []row{
		{"account", func(tag int, label string) (int, bool, error) {
			a, created, err := e.store.PutAccount(e.ctx, "STD", "6700", tag, Account{Name: label, Type: "expense", Postable: true})
			return a.LockVersion, created, err
		}, func() int { return accountTag(e, "STD", "6700") }},
		{"structure", func(tag int, label string) (int, bool, error) {
			s := plStructure(domain.StructureDraft)
			s.Name = strings.ReplaceAll(label, " ", "_")
			out, created, err := e.store.PutStructure(e.ctx, e.ledger.ID, structID, tag, s)
			return out.LockVersion, created, err
		}, func() int { return structTag(e, e.ledger.ID, structID) }},
		{"dimension value", func(tag int, label string) (int, bool, error) {
			v, created, err := e.store.PutDimensionValue(e.ctx, e.ledger.ID, tag, domain.DimensionValue{Attribute: "DEPT", Value: "D900", Name: label, ActiveFrom: &from})
			return v.LockVersion, created, err
		}, func() int { return dimTag(e, e.ledger.ID, "DEPT", "D900") }},
		{"description template", func(tag int, label string) (int, bool, error) {
			d, created, err := e.store.PutDescriptionTemplate(e.ctx, templateID, tag, DescriptionTemplate{Scope: ScopeTenant, SourceType: "manual_journal",
				Language: "en", Template: label + " %1", FieldBindings: map[string]string{"1": "voucher"}})
			return d.LockVersion, created, err
		}, func() int { return templateTag(e, templateID) }},
		{"journal name", func(tag int, label string) (int, bool, error) {
			n, created, err := e.store.PutJournalName(e.ctx, "LCK", tag, JournalName{Name: label, LinesLimit: 10, Active: true})
			return n.LockVersion, created, err
		}, func() int { return nameTag(e, "LCK") }},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			tag, created, err := r.put(0, "First")
			if err != nil || !created || tag != 1 {
				t.Fatalf("create: tag=%d created=%v err=%v", tag, created, err)
			}
			// A replay of the stored content (a retried create, a re-run load) needs no tag and changes nothing.
			if tag, created, err = r.put(0, "First"); err != nil || created || tag != 1 || r.get() != 1 {
				t.Fatalf("replay without If-Match: tag=%d created=%v err=%v", tag, created, err)
			}
			if tag, _, err = r.put(1, "First"); err != nil || tag != 1 {
				t.Fatalf("unchanged replace keeps the tag: tag=%d err=%v", tag, err)
			}
			if _, _, err := r.put(0, "No tag"); !isProblem(err, http.StatusPreconditionRequired, CodePreconditionRequired) {
				t.Fatalf("replace without If-Match: %v", err)
			}
			if _, _, err := r.put(7, "Stale"); !isProblem(err, http.StatusPreconditionFailed, CodeVersionConflict) {
				t.Fatalf("replace with a stale tag: %v", err)
			}
			if got := r.get(); got != 1 {
				t.Fatalf("a refused replace must not change the tag: %d", got)
			}
			tag, created, err = r.put(1, "Second")
			if err != nil || created || tag != 2 || r.get() != 2 {
				t.Fatalf("replace: tag=%d created=%v err=%v", tag, created, err)
			}
			if _, _, err := r.put(1, "Replayed old tag"); !isProblem(err, http.StatusPreconditionFailed, CodeVersionConflict) {
				t.Fatalf("a used tag must be stale: %v", err)
			}

			// Two editors read tag 2: exactly one replace wins, the other gets 412.
			var wg sync.WaitGroup
			errs := make([]error, 2)
			for i := range errs {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, _, errs[i] = r.put(2, "Racer")
				}()
			}
			wg.Wait()
			wins := 0
			for _, err := range errs {
				if err == nil {
					wins++
				} else if !isProblem(err, http.StatusPreconditionFailed, CodeVersionConflict) {
					t.Fatalf("loser: %v", err)
				}
			}
			if wins != 1 || r.get() != 3 {
				t.Fatalf("wins=%d tag=%d", wins, r.get())
			}
		})
	}
}

// Two requests creating the same row at once without a tag: one creates it, the other must not overwrite
// it unseen (the row lock cannot hold a row that does not exist yet), so it gets 428. A tag sent for a row
// that does not exist matches nothing: 412.
func TestPostgres_ConcurrentCreatesDoNotOverwrite(t *testing.T) {
	e, _ := setup(t)
	e = tenantWide(e)
	creates := map[string]func(code, label string, tag int) (bool, error){
		"journal name": func(code, label string, tag int) (bool, error) {
			_, created, err := e.store.PutJournalName(e.ctx, code, tag, JournalName{Name: label, LinesLimit: 10, Active: true})
			return created, err
		},
		"account": func(code, label string, tag int) (bool, error) {
			_, created, err := e.store.PutAccount(e.ctx, "STD", code, tag, Account{Name: label, Type: "expense", Postable: true})
			return created, err
		},
	}
	for name, create := range creates {
		t.Run(name, func(t *testing.T) {
			if _, err := create("9901", "Tagged", 1); !isProblem(err, http.StatusPreconditionFailed, CodeVersionConflict) {
				t.Fatalf("tag on a missing row: %v", err)
			}
			for round := 0; round < 5; round++ {
				code := "98" + strconv.Itoa(10+round)
				var wg sync.WaitGroup
				created := make([]bool, 2)
				errs := make([]error, 2)
				for i := range errs {
					wg.Add(1)
					go func() {
						defer wg.Done()
						created[i], errs[i] = create(code, "Racer "+strconv.Itoa(i), 0)
					}()
				}
				wg.Wait()
				wins := 0
				for i, err := range errs {
					switch {
					case err == nil && created[i]:
						wins++
					case !isProblem(err, http.StatusPreconditionRequired, CodePreconditionRequired):
						t.Fatalf("round %d loser: created=%v err=%v", round, created[i], err)
					}
				}
				if wins != 1 {
					t.Fatalf("round %d: %d creates won", round, wins)
				}
			}
		})
	}
}

// Keyset pages cover the list exactly once, in the unpaged order, whatever the page size.
func TestPostgres_BatchListKeysetPages(t *testing.T) {
	e, _ := setup(t)
	putName(t, e, "GJ", JournalName{Active: true})
	for i := 0; i < 5; i++ {
		if _, err := e.store.CreateBatch(e.ctx, batchOf(e, "GJ", "10.00")); err != nil {
			t.Fatal(err)
		}
	}
	all, err := e.store.ListBatches(e.ctx, BatchFilter{LedgerID: e.ledger.ID})
	if err != nil || len(all) != 5 {
		t.Fatalf("unpaged: %d %v", len(all), err)
	}
	var seen []string
	var after []string
	for pages := 0; pages < 10; pages++ {
		got, err := e.store.ListBatches(e.ctx, BatchFilter{LedgerID: e.ledger.ID, Page: Page{Limit: 2, After: after}})
		if err != nil {
			t.Fatal(err)
		}
		more := len(got) > 2
		if more {
			got = got[:2]
		}
		for _, b := range got {
			seen = append(seen, b.ID)
		}
		if !more {
			break
		}
		last := got[len(got)-1]
		after = []string{last.CreatedAt.UTC().Format(time.RFC3339Nano), last.ID}
	}
	if len(seen) != 5 {
		t.Fatalf("paged %d of 5: %v", len(seen), seen)
	}
	for i, b := range all {
		if seen[i] != b.ID {
			t.Fatalf("order differs at %d: %s vs %s", i, seen[i], b.ID)
		}
	}
}

func TestPostgres_RecurringListKeysetPages(t *testing.T) {
	e, _ := setup(t)
	putName(t, e, "GJ", JournalName{Active: true})
	// All share a next run date, so the order is by name and the keyset runs on the second and third key.
	for _, name := range []string{"Delta", "Alpha", "Charlie", "Bravo"} {
		in := recurringOf(e, "GJ")
		in.Name = name
		if _, _, err := e.store.PutRecurring(e.ctx, ids.New(), 0, in); err != nil {
			t.Fatal(err)
		}
	}
	all, err := e.store.ListRecurring(e.ctx, "", Page{})
	if err != nil || len(all) != 4 {
		t.Fatalf("unpaged: %d %v", len(all), err)
	}
	var seen []string
	var after []string
	for pages := 0; pages < 10; pages++ {
		got, err := e.store.ListRecurring(e.ctx, "", Page{Limit: 3, After: after})
		if err != nil {
			t.Fatal(err)
		}
		more := len(got) > 3
		if more {
			got = got[:3]
		}
		for _, r := range got {
			seen = append(seen, r.ID)
		}
		if !more {
			break
		}
		last := got[len(got)-1]
		due := "infinity"
		if last.NextRunDate != nil {
			due = *last.NextRunDate
		}
		after = []string{due, last.Name, last.ID}
	}
	if len(seen) != len(all) {
		t.Fatalf("paged %d of %d", len(seen), len(all))
	}
	for i, r := range all {
		if seen[i] != r.ID {
			t.Fatalf("order differs at %d", i)
		}
	}
}

func TestPostgres_ClearingListKeysetPages(t *testing.T) {
	e := oiSetup(t)
	for i, amount := range []string{"10.00", "20.00", "30.00"} {
		key := "pg-" + amount
		postOn(t, e, key+"-i", domain.SideDebit, amount, "", testPostingDate)
		postOn(t, e, key+"-g", domain.SideCredit, amount, "", testPostingDate)
		items := openItems(t, e, "open")
		if len(items) != 2 {
			t.Fatalf("round %d: %d open items", i, len(items))
		}
		if _, _, err := e.store.ClearItems(e.ctx, clearingOf(e, key, items, "", false, testToday)); err != nil {
			t.Fatal(err)
		}
	}
	all := listClearings(t, e, "")
	if len(all) != 3 {
		t.Fatalf("unpaged: %d", len(all))
	}
	var seen []string
	var after []string
	for pages := 0; pages < 10; pages++ {
		got, err := e.store.ListClearings(e.ctx, e.ledger.ID, "", Page{Limit: 2, After: after})
		if err != nil {
			t.Fatal(err)
		}
		more := len(got) > 2
		if more {
			got = got[:2]
		}
		for _, d := range got {
			seen = append(seen, d.ID)
		}
		if !more {
			break
		}
		last := got[len(got)-1]
		after = []string{last.CreatedAt.UTC().Format(time.RFC3339Nano), last.ID}
	}
	if len(seen) != 3 || seen[0] != all[0].ID || seen[1] != all[1].ID || seen[2] != all[2].ID {
		t.Fatalf("paged %v, want %v", seen, []string{all[0].ID, all[1].ID, all[2].ID})
	}
}
