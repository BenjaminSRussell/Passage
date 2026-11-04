package api_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

func catchWeightRegistration(cwQty string) string {
	return `{"origin":{"sourceType":"PURCH_LINE","sourceId":"` + ids.New() + `"},"legalEntityId":"` + ids.New() + `","productId":"` + ids.New() + `",
		"dimensions":{"site":"S1","warehouse":"W1"},"qty":"102","enteredUnit":"KG","currency":"USD","costPrice":"2","toStatus":"ORDERED"` + cwQty + `}`
}

func TestApplyRegistration_CatchWeightCaseCount(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	rec := do(t, h, http.MethodPost, applyURL, catchWeightRegistration(`,"cwQty":10`), []string{applyDuty}, key("cw-ok"))
	if rec.Code != http.StatusCreated || fs.createIn.CWQty != 10 {
		t.Fatalf("10 cases: %d %s cwQty=%d", rec.Code, rec.Body, fs.createIn.CWQty)
	}
	for _, bad := range []string{`,"cwQty":0`, `,"cwQty":-3`, fmt.Sprintf(`,"cwQty":%d`, 3_000_000_000), `,"cwQty":1000001`} {
		rec := do(t, h, http.MethodPost, applyURL, catchWeightRegistration(bad), []string{applyDuty}, key("cw-bad"))
		if rec.Code != http.StatusBadRequest || decodeProblem(t, rec).Errors["cwQty"] == "" {
			t.Errorf("%s: %d %s, want 400 naming cwQty", bad, rec.Code, rec.Body)
		}
	}
}
