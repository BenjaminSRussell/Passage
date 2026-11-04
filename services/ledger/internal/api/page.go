package api

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/store"
)

// Keyset paging of the journal batch, recurring journal and clearing document lists. The response is
// {"items": [...], "nextCursor": "..."}; nextCursor is present only when another page exists and is
// passed back as ?cursor= with the same filters. limit defaults to defaultListPage, at most maxListPage.
const (
	defaultListPage = 100
	maxListPage     = 200
)

// Normalizers of one cursor key value: each returns the value in the form the SQL cast accepts, or false,
// so an altered cursor is a 400 and never reaches SQL as a bad cast (Go parses years Postgres refuses, such
// as 0000, and offsets beyond its range, so both are checked here).
func cursorTimestamp(s string) (string, bool) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return "", false
	}
	t = t.UTC()
	if t.Year() < 1 || t.Year() > 9999 {
		return "", false
	}
	return t.Format(time.RFC3339Nano), true
}

func cursorDateOrInfinity(s string) (string, bool) {
	if s == "infinity" {
		return s, true
	}
	d, ok := parseDate(s)
	if !ok || d.Year() < 1 {
		return "", false
	}
	return s, true
}

func cursorText(s string) (string, bool) { return s, s != "" && len(s) <= 400 }

func cursorUUID(s string) (string, bool) { return ids.Canonical(s) }

// pageParams reads limit and cursor. check holds one normalizer per key value of the list's cursor. It
// writes the 422 and returns false when either is malformed.
func pageParams(w http.ResponseWriter, r *http.Request, check ...func(string) (string, bool)) (store.Page, bool) {
	q := r.URL.Query()
	fields := map[string]string{}
	page := store.Page{Limit: defaultListPage}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxListPage {
			fields["limit"] = fmt.Sprintf("must be 1 to %d", maxListPage)
		} else {
			page.Limit = n
		}
	}
	if raw := q.Get("cursor"); raw != "" {
		parts, ok := store.DecodeCursor(raw, len(check))
		for i := 0; ok && i < len(parts); i++ {
			parts[i], ok = check[i](parts[i])
		}
		if !ok {
			fields["cursor"] = "is not a cursor returned by this endpoint"
		} else {
			page.After = parts
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return store.Page{}, false
	}
	return page, true
}

// trimPage drops the look-ahead row of a list read with Limit+1 and returns the cursor of the last kept
// row, empty when this is the last page.
func trimPage[T any](items []T, limit int, key func(T) []string) ([]T, string) {
	if len(items) <= limit {
		return items, ""
	}
	items = items[:limit]
	return items, store.EncodeCursor(key(items[limit-1])...)
}

func pageBody[T any](items []T, next string) map[string]any {
	body := map[string]any{"items": items}
	if next != "" {
		body["nextCursor"] = next
	}
	return body
}
