package events_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	pevents "github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/events"
)

// TestEventsMatchAsyncAPI checks each published type passes the kernel naming rule and
// that events/asyncapi.yaml names it and every payload field the code writes.
func TestEventsMatchAsyncAPI(t *testing.T) {
	raw, err := os.ReadFile("../../events/asyncapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	spec := string(raw)
	cases := []struct {
		typ     string
		payload any
	}{
		{events.ConditionRecordActivated, events.ConditionRecordData{JournalID: "x", CampaignID: "x"}},
		{events.ConditionRecordWithdrawn, events.ConditionRecordWithdrawnData{}},
		{events.JournalPosted, events.JournalPostedData{ReversedJournalID: "x"}},
		{events.RebateAccrualPosted, events.RebateAccrualPostedData{}},
		{events.CommissionCalculated, events.CommissionCalculatedData{ReversesInvoiceLineID: "x"}},
		{events.FundClosed, events.FundStatusData{}},
		{events.FundReopened, events.FundStatusData{}},
		{events.FundUsageRecorded, events.FundUsageRecordedData{}},
		{events.CampaignKilled, events.CampaignStatusData{}},
		{events.CampaignRestored, events.CampaignStatusData{}},
		{events.ProcedureActivated, events.ProcedureActivatedData{SupersededID: "x"}},
		{events.VendorRebateClaimCreated, events.VendorRebateClaimData{}},
		{events.VendorRebateClaimApproved, events.VendorRebateClaimData{}},
	}
	for _, c := range cases {
		if !pevents.ValidType(c.typ) {
			t.Errorf("%s is not a valid dev.passage.<ctx>.<entity>.<verb>.v1 type", c.typ)
		}
		if !strings.Contains(spec, "name: "+c.typ) {
			t.Errorf("asyncapi.yaml has no message named %s", c.typ)
		}
		body, _ := json.Marshal(c.payload)
		var fields map[string]any
		_ = json.Unmarshal(body, &fields)
		for field := range fields {
			if !strings.Contains(spec, " "+field+": {") {
				t.Errorf("%s payload field %q is missing from asyncapi.yaml", c.typ, field)
			}
		}
	}
}
