package store

import (
	"encoding/base64"
	"encoding/json"
)

// Page is the keyset paging request of a list: Limit rows after the position After (the key values of
// the last row of the previous page, empty for the first page). A list returns up to Limit+1 rows, so
// the caller can tell whether another page exists and takes the next cursor from the last row it keeps.
type Page struct {
	Limit int
	After []string
}

// pageLimit is the page size to query, one more than asked, with def when none was asked.
func (p Page) pageLimit(def int) int {
	if p.Limit < 1 {
		return def + 1
	}
	return p.Limit + 1
}

// keys is After as a non-nil slice: a NULL array would make the keyset predicate unknown.
func (p Page) keys() []string {
	if p.After == nil {
		return []string{}
	}
	return p.After
}

// EncodeCursor packs the key values of a row into an opaque cursor: a JSON array of strings, base64url
// encoded, so any value (a name with any character) survives the round trip. The cursor carries no
// secret and needs none: it only positions a read the caller may make anyway (RLS and the list's filters
// still apply), and the API checks each value's type before it reaches SQL.
func EncodeCursor(parts ...string) string {
	raw, _ := json.Marshal(parts) // a []string always marshals
	return base64.RawURLEncoding.EncodeToString(raw)
}

// DecodeCursor unpacks a cursor made by EncodeCursor with exactly n key values.
func DecodeCursor(cursor string, n int) ([]string, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return nil, false
	}
	var parts []string
	if err := json.Unmarshal(raw, &parts); err != nil || len(parts) != n {
		return nil, false
	}
	return parts, true
}
