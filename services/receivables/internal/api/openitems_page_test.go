package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/store"
)

// pagedStore answers ListOpenItems from a fixed list the way the database does: due date window, keyset after the cursor,
// ordered by due date then id, at most limit rows.
type pagedStore struct {
	fakeStore
	items []store.OpenItem
	seen  []store.OpenItemFilter
	asked []int
}

func (p *pagedStore) ListOpenItems(_ context.Context, f store.OpenItemFilter, limit int) ([]store.OpenItem, error) {
	p.seen, p.asked = append(p.seen, f), append(p.asked, limit)
	out := []store.OpenItem{}
	for _, it := range p.items {
		switch {
		case f.DueFrom != "" && it.DueDate < f.DueFrom, f.DueTo != "" && it.DueDate > f.DueTo:
		case f.AfterDue != "" && (it.DueDate < f.AfterDue || it.DueDate == f.AfterDue && it.ID <= f.AfterID):
		default:
			out = append(out, it)
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func TestListOpenItems_PagesByDueDateWithACursorAndAWindow(t *testing.T) {
	st := &pagedStore{}
	for i := 0; i < 5; i++ {
		st.items = append(st.items, store.OpenItem{ID: ids.New(), DueDate: "2027-03-0" + string(rune('1'+i)), Currency: "USD", OpenAmount: decimal.NewFromInt(int64(i + 1))})
	}
	h := router(st)
	get := func(path string) (items []store.OpenItem, next string) {
		t.Helper()
		rec := do(h, http.MethodGet, path, "", "receivables.forecast.read", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d %s", path, rec.Code, rec.Body)
		}
		var body struct {
			Items      []store.OpenItem `json:"items"`
			NextCursor string           `json:"nextCursor"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.Items, body.NextCursor
	}
	first, next := get("/v1/receivables/open-items?limit=2")
	if len(first) != 2 || next == "" || st.asked[0] != 3 {
		t.Fatalf("first page = %d items, cursor %q, asked for %d; want 2 items, a cursor and one extra row asked", len(first), next, st.asked[0])
	}
	second, next2 := get("/v1/receivables/open-items?limit=2&cursor=" + next)
	if len(second) != 2 || second[0].ID == first[0].ID || second[0].DueDate <= first[1].DueDate || next2 == "" {
		t.Fatalf("second page = %+v (cursor %q); want the next two items after %s", second, next2, first[1].DueDate)
	}
	last, none := get("/v1/receivables/open-items?limit=2&cursor=" + next2)
	if len(last) != 1 || none != "" {
		t.Fatalf("last page = %d items, cursor %q; want one item and no cursor", len(last), none)
	}
	windowed, _ := get("/v1/receivables/open-items?due_from=2027-03-02&due_to=2027-03-03")
	if len(windowed) != 2 || st.seen[len(st.seen)-1].DueFrom != "2027-03-02" || st.seen[len(st.seen)-1].DueTo != "2027-03-03" {
		t.Fatalf("window = %+v with filter %+v; want the two items due 2 and 3 March", windowed, st.seen[len(st.seen)-1])
	}
	if strings.Contains(next, "|") {
		t.Fatalf("cursor %q leaks its parts", next)
	}
}

func TestOpenItemCursor_RoundTripsAndRefusesGarbage(t *testing.T) {
	id := ids.New()
	due, got, ok := decodeOpenItemCursor(encodeOpenItemCursor("2027-03-01", id))
	if !ok || due != "2027-03-01" || got != id {
		t.Fatalf("round trip = %s %s %v", due, got, ok)
	}
	for _, bad := range []string{"", "zz", encodeOpenItemCursor("2027-13-01", id), encodeOpenItemCursor("2027-03-01", "not-a-uuid-not-a-uuid-not-a-uuid-xxxx")} {
		if _, _, ok := decodeOpenItemCursor(bad); ok {
			t.Errorf("cursor %q accepted", bad)
		}
	}
}
