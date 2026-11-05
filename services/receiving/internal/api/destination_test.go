package api_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
)

// A receipt line without a purchase order line names its own delivery site
// and warehouse (#480); receipt.posted carries them so inventory can book it.
func TestDirectReceipt_LineWithoutPOLineCarriesItsDestination(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	site, warehouse := ids.New(), ids.New()
	body := directBody("PS-DEST", 10, 4, `,"siteId":"`+site+`","warehouseId":"`+warehouse+`"`)
	r := decode[struct {
		ID      string `json:"id"`
		Version int    `json:"version"`
		Lines   []struct {
			SiteID      string `json:"siteId"`
			WarehouseID string `json:"warehouseId"`
		} `json:"lines"`
	}](t, rg.call(http.MethodPost, receiptsPath, body, rg.maintain()), http.StatusCreated)
	if len(r.Lines) != 1 || r.Lines[0].SiteID != site || r.Lines[0].WarehouseID != warehouse {
		t.Fatalf("receipt lines %+v, want site %s warehouse %s", r.Lines, site, warehouse)
	}
	decode[receiptJSON](t, rg.call(http.MethodPost, receiptsPath+"/"+r.ID+":post", "", rg.poster(r.Version)), http.StatusOK)
	var data struct {
		Lines []map[string]any `json:"lines"`
	}
	if err := json.Unmarshal(rg.receiptPostedEvent(t).Data, &data); err != nil || len(data.Lines) != 1 {
		t.Fatalf("receipt.posted: %v", err)
	}
	if data.Lines[0]["site_id"] != site || data.Lines[0]["warehouse_id"] != warehouse {
		t.Fatalf("receipt.posted line %v, want site %s warehouse %s", data.Lines[0], site, warehouse)
	}
}

func TestDirectReceipt_DestinationRules(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	for name, tc := range map[string]struct{ extra, field string }{
		"site not a uuid":        {`,"siteId":"S1"`, "lines[0].siteId"},
		"warehouse without site": {`,"warehouseId":"` + ids.New() + `"`, "lines[0].warehouseId"},
	} {
		t.Run(name, func(t *testing.T) {
			rec := rg.call(http.MethodPost, receiptsPath, directBody("PS-"+name, 10, 4, tc.extra), rg.maintain())
			var p struct {
				Errors map[string]string `json:"errors"`
			}
			if rec.Code != http.StatusBadRequest || json.Unmarshal(rec.Body.Bytes(), &p) != nil || p.Errors[tc.field] == "" {
				t.Fatalf("got %d %s, want 400 on %s", rec.Code, rec.Body.String(), tc.field)
			}
		})
	}
}
