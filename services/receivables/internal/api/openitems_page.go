package api

import (
	"encoding/hex"
	"net/http"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/store"
)

// openItemWindow reads due_from, due_to and cursor into the filter: the due date window the cash forecast asks for, and the
// place to continue a listing. It writes the 400 and returns false when one is malformed.
func openItemWindow(w http.ResponseWriter, r *http.Request, f *store.OpenItemFilter) bool {
	q := r.URL.Query()
	for name, into := range map[string]*string{"due_from": &f.DueFrom, "due_to": &f.DueTo} {
		if raw := q.Get(name); raw != "" {
			if _, err := time.Parse(time.DateOnly, raw); err != nil {
				badQuery(w, r, name, "must be a YYYY-MM-DD date")
				return false
			}
			*into = raw
		}
	}
	if f.DueFrom != "" && f.DueTo != "" && f.DueFrom > f.DueTo {
		badQuery(w, r, "due_from", "must not be after due_to")
		return false
	}
	if raw := q.Get("cursor"); raw != "" {
		due, id, ok := decodeOpenItemCursor(raw)
		if !ok {
			badQuery(w, r, "cursor", "must be the nextCursor of an earlier page")
			return false
		}
		f.AfterDue, f.AfterID = due, id
	}
	return true
}

func encodeOpenItemCursor(due, id string) string { return hex.EncodeToString([]byte(due + "|" + id)) }

func decodeOpenItemCursor(raw string) (due, id string, ok bool) {
	b, err := hex.DecodeString(raw)
	if err != nil || len(b) != len(time.DateOnly)+1+36 || b[len(time.DateOnly)] != '|' {
		return "", "", false
	}
	due, id = string(b[:len(time.DateOnly)]), string(b[len(time.DateOnly)+1:])
	_, err = time.Parse(time.DateOnly, due)
	return due, id, err == nil && ids.Valid(id)
}
