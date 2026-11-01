package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/migration/internal/store"
)

// pageLimit reads limit (default defaultPage) and reports a bad value as a 400.
func pageLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	limit, ok := parseLimit(w, r)
	if ok && limit == 0 {
		limit = defaultPage
	}
	return limit, ok
}

func encodePageCursor(c any) string {
	raw, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(raw)
}

// decodePageCursor rejects unknown fields, so a cursor issued by another list (whose fields differ) is not
// mistaken for this one. A cursor carries only the keyset position: filters come from the query and the tenant
// from the principal, so an edited cursor can move the page start but never widen what the caller can see.
func decodePageCursor(s string, into any) bool {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(into) == nil && !dec.More()
}

// cursorProblem is the 400 for a cursor this endpoint did not issue.
func cursorProblem() error {
	return validation(map[string]string{"cursor": "is not a cursor returned by this endpoint"})
}

func runCursor(s string) (*store.RunCursor, bool) {
	var c store.RunCursor
	if !decodePageCursor(s, &c) || !ids.Valid(c.ID) || c.CreatedAt.IsZero() {
		return nil, false
	}
	return &c, true
}

func specCursor(s string) (*store.SpecCursor, bool) {
	var c store.SpecCursor
	if !decodePageCursor(s, &c) || c.Entity == "" || c.Version < 1 {
		return nil, false
	}
	return &c, true
}

func templateCursor(s string) (*store.TemplateCursor, bool) {
	var c store.TemplateCursor
	if !decodePageCursor(s, &c) || c.Entity == "" || c.Name == "" || c.Version < 1 {
		return nil, false
	}
	return &c, true
}

// ledgerCursor is the last source key of the previous page, wrapped so it is opaque to callers.
func ledgerCursor(s string) (string, bool) {
	var c struct {
		K string `json:"k"`
	}
	if !decodePageCursor(s, &c) || c.K == "" {
		return "", false
	}
	return c.K, true
}

// trimPage cuts a limit+1 fetch down to limit and reports whether another page follows.
func trimPage[T any](rows []T, limit int) ([]T, bool) {
	if len(rows) > limit {
		return rows[:limit], true
	}
	return rows, false
}
