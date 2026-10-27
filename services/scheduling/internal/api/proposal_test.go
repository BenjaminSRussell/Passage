package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

func (f *fakeStore) ProposeCampaigns(_ context.Context, pf store.ProposalFilter) ([]domain.ProposedCampaign, error) {
	f.lastProposal = pf
	if f.err != nil {
		return nil, f.err
	}
	return []domain.ProposedCampaign{{CampaignKey: "WHITE", ResourceID: resID, PlannedStart: pf.From, RunMinutes: 120,
		Orders: []domain.ProposalCandidate{
			{OrderID: ordB, OperationID: jobID, OperNumber: 10, ResourceID: resID, SetupGroup: "WHITE", RunMinutes: 60, Start: pf.From,
				Attributes: map[string]string{"campaignKey": "WHITE"}},
			{OrderID: ordC, OperationID: jobID, OperNumber: 10, ResourceID: resID, SetupGroup: "WHITE", RunMinutes: 60, Start: pf.From.Add(time.Hour), Firmed: true},
		}}}, nil
}

func TestCampaignProposals_PassesTheWindowAndReturnsProposals(t *testing.T) {
	f := &fakeStore{}
	path := "/v1/scheduling/campaign-proposals?from=2027-03-01T09:00:00%2B01:00&windowDays=30&resourceId=" + strings.ToUpper(resID)
	rec := do(newRouter(f), http.MethodGet, path, "", "scheduling.read", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	from := time.Date(2027, 3, 1, 8, 0, 0, 0, time.UTC)
	if !f.lastProposal.From.Equal(from) || f.lastProposal.From.Location() != time.UTC || f.lastProposal.WindowDays != 30 || f.lastProposal.ResourceID != resID {
		t.Fatalf("store got %+v", f.lastProposal)
	}
	var body struct {
		From       time.Time `json:"from"`
		To         time.Time `json:"to"`
		WindowDays int       `json:"windowDays"`
		Items      []struct {
			CampaignKey string           `json:"campaignKey"`
			Orders      []map[string]any `json:"orders"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.To.Equal(from.AddDate(0, 0, 30)) || body.WindowDays != 30 || len(body.Items) != 1 || len(body.Items[0].Orders) != 2 {
		t.Fatalf("body = %s", rec.Body)
	}
	first := body.Items[0].Orders[0]
	if first["orderId"] != ordB || first["setupGroup"] != "WHITE" || first["attributes"] != nil {
		t.Fatalf("first order = %v (attributes stay internal)", first)
	}
	if body.Items[0].Orders[1]["firmed"] != true {
		t.Fatalf("second order = %v", body.Items[0].Orders[1])
	}
}

func TestCampaignProposals_DefaultsToNowAndFourteenDays(t *testing.T) {
	pinned := time.Date(2027, 3, 1, 10, 17, 42, 0, time.FixedZone("EST", -5*3600))
	proposalNow = func() time.Time { return pinned }
	t.Cleanup(func() { proposalNow = time.Now })
	f := &fakeStore{}
	rec := do(newRouter(f), http.MethodGet, "/v1/scheduling/campaign-proposals", "", "scheduling.read", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	if want := time.Date(2027, 3, 1, 15, 17, 0, 0, time.UTC); !f.lastProposal.From.Equal(want) || f.lastProposal.WindowDays != domain.DefaultProposalWindowDays || f.lastProposal.ResourceID != "" {
		t.Fatalf("store got %+v, want from %v and 14 days", f.lastProposal, want)
	}
}

func TestCampaignProposals_RefusesBadParametersAndMissingDuty(t *testing.T) {
	h := newRouter(&fakeStore{})
	for q, field := range map[string]string{
		"?from=2027-03-01":    `"from"`,
		"?windowDays=0":       `"windowDays"`,
		"?windowDays=91":      `"windowDays"`,
		"?windowDays=two":     `"windowDays"`,
		"?resourceId=nope":    `"resourceId"`,
		"?from=x&windowDays=": `"from"`,
	} {
		rec := do(h, http.MethodGet, "/v1/scheduling/campaign-proposals"+q, "", "scheduling.read", "")
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), field) {
			t.Errorf("%s: status %d body %s", q, rec.Code, rec.Body)
		}
	}
	if rec := do(h, http.MethodGet, "/v1/scheduling/campaign-proposals", "", "scheduling.run", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("without scheduling.read: status %d", rec.Code)
	}
	failing := &fakeStore{err: errors.New("database down")}
	if rec := do(newRouter(failing), http.MethodGet, "/v1/scheduling/campaign-proposals", "", "scheduling.read", ""); rec.Code != http.StatusInternalServerError {
		t.Fatalf("store failure: status %d", rec.Code)
	}
}
