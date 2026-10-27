package events

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestAsyncAPI_ListsEveryEventType keeps events/asyncapi.yaml and the published
// event types in step, and checks the CloudEvents naming rule.
func TestAsyncAPI_ListsEveryEventType(t *testing.T) {
	raw, err := os.ReadFile("../../events/asyncapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var documented []string
	for _, m := range regexp.MustCompile(`(?m)^      name: (\S+)$`).FindAllStringSubmatch(string(raw), -1) {
		documented = append(documented, m[1])
	}
	published := []string{FreightBillMatched, FreightBillApproved, TenderAccepted, TenderDeclined, TenderExpired, TrackingEventRecorded, LandedCostPosted, GateCheckedIn, GoodsInTransitShipped}
	naming := regexp.MustCompile(`^dev\.passage\.tms\.[a-z]+\.[a-z]+\.v1$`)
	for _, typ := range published {
		if !naming.MatchString(typ) {
			t.Errorf("%s does not follow dev.passage.<ctx>.<entity>.<verb>.v1", typ)
		}
	}
	sort.Strings(documented)
	sort.Strings(published)
	if strings.Join(documented, ",") != strings.Join(published, ",") {
		t.Fatalf("asyncapi.yaml documents %v, the service publishes %v", documented, published)
	}
}
