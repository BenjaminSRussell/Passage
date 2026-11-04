package api

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/ledger/internal/store"
)

// The five configuration PUTs read If-Match as the entity tag (lockVersion), hand it to the store (which
// answers 428 and 412) and return the new tag as ETag.
func TestConfigurationPuts_IfMatchAndETag(t *testing.T) {
	structureBody := `{"name":"EXPENSE","version":1,"effectiveFrom":"2026-01-01","segments":["DEPT"],"accountFilter":["6000..6999"],"constraints":[]}`
	puts := []struct {
		name, path, body, duty string
	}{
		{"account", "/v1/ledger/charts/STD/accounts/6010", `{"name":"Freight","accountType":"expense"}`, dutyAccountWrite},
		{"structure", structurePath, structureBody, dutyStructureWrite},
		{"dimension value", "/v1/ledger/ledgers/" + testLedger + "/dimension-values/DEPT/D100", `{"name":"Sales"}`, dutyDimensionWrite},
		{"description template", "/v1/ledger/description-templates/" + testTemplate,
			`{"scope":"tenant","sourceType":"manual_journal","template":"Journal %1","fieldBindings":{"1":"voucher"}}`, dutySetupWrite},
		{"journal name", "/v1/ledger/journal-names/GJ", `{"name":"General journal"}`, dutySetupWrite},
	}
	for _, c := range puts {
		t.Run(c.name, func(t *testing.T) {
			fs := &fakeStore{}
			h := server(fs)

			rec := call(h, http.MethodPut, c.path, c.body, c.duty, map[string]string{"If-Match": `"3"`})
			if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			if rec.Header().Get("ETag") != `"4"` || decode(t, rec)["lockVersion"] != float64(4) {
				t.Fatalf("ETag %q, body %s", rec.Header().Get("ETag"), rec.Body.String())
			}

			// Weak and bare forms are accepted; absent is 0 and the store decides (428 for a replace).
			call(h, http.MethodPut, c.path, c.body, c.duty, map[string]string{"If-Match": `W/"5"`})
			call(h, http.MethodPut, c.path, c.body, c.duty, nil)
			if len(fs.putTags) != 3 || fs.putTags[0] != 3 || fs.putTags[1] != 5 || fs.putTags[2] != 0 {
				t.Fatalf("tags passed to the store: %v", fs.putTags)
			}

			for _, bad := range []string{`"abc"`, `"0"`, `"-2"`} {
				fs.putTags = nil
				rec = call(h, http.MethodPut, c.path, c.body, c.duty, map[string]string{"If-Match": bad})
				errs, _ := decode(t, rec)["errors"].(map[string]any)
				if rec.Code != http.StatusBadRequest || errs["If-Match"] == nil || len(fs.putTags) != 0 {
					t.Fatalf("If-Match %s: status %d body %s tags %v", bad, rec.Code, rec.Body.String(), fs.putTags)
				}
			}
		})
	}
}

func TestConfigurationReads_CarryETag(t *testing.T) {
	fs := &fakeStore{
		journalNames: []store.JournalName{{Code: "GJ", Name: "General", LockVersion: 7}},
		templates:    []store.DescriptionTemplate{{ID: testTemplate, LockVersion: 2}},
	}
	h := server(fs)
	if rec := call(h, http.MethodGet, "/v1/ledger/journal-names/GJ", "", dutyRead, nil); rec.Header().Get("ETag") != `"7"` {
		t.Fatalf("journal name ETag %q", rec.Header().Get("ETag"))
	}
	if rec := call(h, http.MethodGet, "/v1/ledger/description-templates/"+testTemplate, "", dutyRead, nil); rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("template ETag %q", rec.Header().Get("ETag"))
	}
}

func batches(n int) []store.Batch {
	out := make([]store.Batch, n)
	base := time.Date(2026, 3, 1, 10, 0, 0, 123456000, time.UTC)
	for i := range out {
		out[i] = store.Batch{ID: "0190a000-0000-7000-8000-00000000b00" + strconv.Itoa(i), CreatedAt: base.Add(-time.Duration(i) * time.Minute)}
	}
	return out
}

func TestListBatches_KeysetPaging(t *testing.T) {
	// The store returns the look-ahead row; the handler trims it and hands out the cursor of the last kept row.
	fs := &fakeStore{batchPage: batches(3)}
	h := server(fs)
	rec := call(h, http.MethodGet, "/v1/ledger/journal-batches?limit=2", "", journalDuties, nil)
	body := decode(t, rec)
	items, _ := body["items"].([]any)
	next, _ := body["nextCursor"].(string)
	if rec.Code != http.StatusOK || len(items) != 2 || next == "" || fs.batchCalls[0].filter.Page.Limit != 2 {
		t.Fatalf("status %d items %d cursor %q page %+v", rec.Code, len(items), next, fs.batchCalls[0].filter.Page)
	}

	fs.batchPage = batches(2)
	rec = call(h, http.MethodGet, "/v1/ledger/journal-batches?limit=2&cursor="+next, "", journalDuties, nil)
	after := fs.batchCalls[len(fs.batchCalls)-1].filter.Page.After
	if rec.Code != http.StatusOK || len(after) != 2 || after[0] != "2026-03-01T09:59:00.123456Z" || after[1] != "0190a000-0000-7000-8000-00000000b001" {
		t.Fatalf("status %d after %v", rec.Code, after)
	}
	if _, more := decode(t, rec)["nextCursor"]; more {
		t.Fatalf("a short page must not carry a cursor: %s", rec.Body.String())
	}

	for _, q := range []string{"limit=0", "limit=201", "limit=x", "cursor=%25%25", "cursor=" + store.EncodeCursor("not-a-time", "x")} {
		fs.batchCalls = nil
		rec = call(h, http.MethodGet, "/v1/ledger/journal-batches?"+q, "", journalDuties, nil)
		if rec.Code != http.StatusBadRequest || len(fs.batchCalls) != 0 {
			t.Fatalf("%s: status %d, store calls %d", q, rec.Code, len(fs.batchCalls))
		}
	}
}

func TestListRecurring_KeysetPaging(t *testing.T) {
	due := "2026-04-01"
	fs := &fakeStore{}
	fs.rec.list = []store.RecurringJournal{{ID: "0190a000-0000-7000-8000-000000000c01", Name: "Rent", NextRunDate: &due},
		{ID: "0190a000-0000-7000-8000-000000000c02", Name: "Insurance"}}
	h := server(fs)
	rec := call(h, http.MethodGet, "/v1/ledger/recurring-journals?limit=1", "", dutyRead, nil)
	next, _ := decode(t, rec)["nextCursor"].(string)
	if rec.Code != http.StatusOK || next == "" {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if keys, ok := store.DecodeCursor(next, 3); !ok || keys[0] != due || keys[1] != "Rent" {
		t.Fatalf("cursor keys %v", keys)
	}

	// A journal with no next run date sorts last: its key is "infinity".
	fs.rec.list = fs.rec.list[1:]
	fs.rec.list = append(fs.rec.list, store.RecurringJournal{ID: "0190a000-0000-7000-8000-000000000c03", Name: "Zeta"})
	rec = call(h, http.MethodGet, "/v1/ledger/recurring-journals?limit=1", "", dutyRead, nil)
	next, _ = decode(t, rec)["nextCursor"].(string)
	if keys, _ := store.DecodeCursor(next, 3); len(keys) != 3 || keys[0] != "infinity" || keys[1] != "Insurance" {
		t.Fatalf("cursor keys %v", keys)
	}
	rec = call(h, http.MethodGet, "/v1/ledger/recurring-journals?cursor="+next, "", dutyRead, nil)
	if rec.Code != http.StatusOK || len(fs.listPage.After) != 3 {
		t.Fatalf("status %d page %+v", rec.Code, fs.listPage)
	}
	if rec = call(h, http.MethodGet, "/v1/ledger/recurring-journals?cursor="+store.EncodeCursor("2026-13-40", "x", "y"), "", dutyRead, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("forged cursor: %d", rec.Code)
	}
}

func TestListClearings_KeysetPaging(t *testing.T) {
	at := time.Date(2026, 5, 2, 8, 0, 0, 5000, time.UTC)
	fs := &fakeStore{clearingPage: []store.ClearingDocument{{ID: "0190a000-0000-7000-8000-000000000d01", CreatedAt: at},
		{ID: "0190a000-0000-7000-8000-000000000d02", CreatedAt: at.Add(-time.Hour)}}}
	h := server(fs)
	rec := call(h, http.MethodGet, "/v1/ledger/clearings?ledger="+testLedger+"&limit=1", "", dutyRead, nil)
	next, _ := decode(t, rec)["nextCursor"].(string)
	if keys, ok := store.DecodeCursor(next, 2); rec.Code != http.StatusOK || !ok || keys[0] != "2026-05-02T08:00:00.000005Z" {
		t.Fatalf("status %d cursor %q", rec.Code, next)
	}
	rec = call(h, http.MethodGet, "/v1/ledger/clearings?ledger="+testLedger+"&cursor="+next, "", dutyRead, nil)
	if rec.Code != http.StatusOK || len(fs.listPage.After) != 2 || fs.listPage.Limit != defaultListPage {
		t.Fatalf("status %d page %+v", rec.Code, fs.listPage)
	}
	if rec = call(h, http.MethodGet, "/v1/ledger/clearings?ledger="+testLedger+"&limit=500", "", dutyRead, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("limit over the cap: %d", rec.Code)
	}
}

// An altered cursor is a 400 before the store runs, including values Go parses but Postgres refuses (year
// 0000, a UUID in urn form is normalized instead); an accepted cursor reaches the store in the canonical form
// the SQL casts take.
func TestListCursors_AlteredValuesNeverReachSQL(t *testing.T) {
	const id = "0190A000-0000-7000-8000-00000000B001"
	fs := &fakeStore{batchPage: batches(1)}
	h := server(fs)
	for _, cursor := range []string{
		store.EncodeCursor("0000-01-01T00:00:00Z", id),
		store.EncodeCursor("2026-03-01T10:00:00Z", "x"),
		store.EncodeCursor("2026-03-01T10:00:00Z", id, "extra"),
		store.EncodeCursor("2026-03-01T10:00:00Z"),
		"bm90LWpzb24", // base64 of "not-json"
	} {
		fs.batchCalls = nil
		rec := call(h, http.MethodGet, "/v1/ledger/journal-batches?cursor="+cursor, "", journalDuties, nil)
		if rec.Code != http.StatusBadRequest || len(fs.batchCalls) != 0 {
			t.Fatalf("cursor %q: status %d, store calls %d", cursor, rec.Code, len(fs.batchCalls))
		}
	}
	rec := call(h, http.MethodGet, "/v1/ledger/journal-batches?cursor="+store.EncodeCursor("2026-03-01T11:00:00.5+01:00", "urn:uuid:"+id), "", journalDuties, nil)
	after := fs.batchCalls[len(fs.batchCalls)-1].filter.Page.After
	if rec.Code != http.StatusOK || after[0] != "2026-03-01T10:00:00.5Z" || after[1] != "0190a000-0000-7000-8000-00000000b001" {
		t.Fatalf("status %d after %v", rec.Code, after)
	}

	fs.rec.list = nil
	if rec := call(h, http.MethodGet, "/v1/ledger/recurring-journals?cursor="+store.EncodeCursor("0000-01-01", "Rent", id), "", dutyRead, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("recurring cursor with year 0000: %d", rec.Code)
	}
}

// A recurring journal name may hold any character, the separator of a joined cursor included: the cursor
// round-trips it unchanged.
func TestRecurringCursor_RoundTripsAnyName(t *testing.T) {
	name := "Rent\x1fQ1, \"North\" é"
	keys, ok := store.DecodeCursor(store.EncodeCursor("2026-04-01", name, testLedger), 3)
	if !ok || keys[1] != name {
		t.Fatalf("keys %q ok %v", keys, ok)
	}
}
