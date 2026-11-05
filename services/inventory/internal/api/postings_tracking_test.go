package api

import (
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
)

// A tracked delivery line is issued entry by entry (#480): a plain line's
// inventory quantity is shared in proportion with the remainder on the last
// entry; a catch-weight line takes the line's weight on one entry and each
// entry's weight on several. Entries that do not add up park the line.
func TestDeliveryEntries(t *testing.T) {
	dims := domain.Dimensions{domain.DimSite: postSite}
	plain := deliveryEntry{dims: dims, qty: d("36"), entered: d("3"), src: postLine}
	got, err := deliveryEntries(plain, false, []trackingEntry{{batch: "B1", quantity: "2"}, {batch: "B2", quantity: "1"}})
	if err != nil || len(got) != 2 || !got[0].qty.Equal(d("24")) || !got[1].qty.Equal(d("12")) || got[0].dims[domain.DimBatch] != "B1" ||
		got[0].src == got[1].src || got[0].src == postLine || plain.dims[domain.DimBatch] != "" {
		t.Fatalf("plain split %+v %v", got, err)
	}
	thirds := deliveryEntry{dims: dims, qty: d("1"), entered: d("3"), src: postLine}
	if got, err := deliveryEntries(thirds, false, []trackingEntry{{batch: "B1", quantity: "1"}, {batch: "B2", quantity: "1"}, {batch: "B3", quantity: "1"}}); err != nil ||
		!got[0].qty.Add(got[1].qty).Add(got[2].qty).Equal(d("1")) || !got[2].qty.Equal(d("0.333334")) {
		t.Fatalf("thirds %+v %v", got, err)
	}
	cw := deliveryEntry{dims: dims, qty: d("51"), entered: d("5"), cases: 5, src: postLine}
	if got, err := deliveryEntries(cw, true, []trackingEntry{{batch: "B1", quantity: "5"}}); err != nil || !got[0].qty.Equal(d("51")) || got[0].cases != 5 {
		t.Fatalf("one catch-weight entry %+v %v", got, err)
	}
	if got, err := deliveryEntries(cw, true, []trackingEntry{{batch: "B1", quantity: "2", weight: "20.5"}, {batch: "B2", quantity: "3", weight: "30.5"}}); err != nil ||
		!got[1].qty.Equal(d("30.5")) || got[1].cases != 3 {
		t.Fatalf("weighed catch-weight entries %+v %v", got, err)
	}
	for name, tc := range map[string]struct {
		line  deliveryEntry
		cw    bool
		track []trackingEntry
	}{
		"no batch or serial":     {plain, false, []trackingEntry{{quantity: "3"}}},
		"does not add up":        {plain, false, []trackingEntry{{batch: "B1", quantity: "2"}}},
		"serial not one unit":    {plain, false, []trackingEntry{{serial: "S1", quantity: "1"}, {batch: "B", quantity: "2"}}},
		"serial twice":           {deliveryEntry{dims: dims, qty: d("2"), entered: d("2"), src: postLine}, false, []trackingEntry{{serial: "S", quantity: "1"}, {batch: "X", serial: "S", quantity: "1"}}},
		"cases not whole":        {cw, true, []trackingEntry{{batch: "B1", quantity: "2.5", weight: "25"}, {batch: "B2", quantity: "2.5", weight: "26"}}},
		"unweighed split":        {cw, true, []trackingEntry{{batch: "B1", quantity: "2"}, {batch: "B2", quantity: "3"}}},
		"weights not the line's": {cw, true, []trackingEntry{{batch: "B1", quantity: "2", weight: "20"}, {batch: "B2", quantity: "3", weight: "30"}}},
	} {
		if _, err := deliveryEntries(tc.line, tc.cw, tc.track); !eventing.IsPermanent(err) || !strings.Contains(err.Error(), CodePostingTracking) {
			t.Errorf("%s: %v, want permanent %s", name, err, CodePostingTracking)
		}
	}
}
