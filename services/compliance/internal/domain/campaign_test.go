package domain

import (
	"errors"
	"testing"
	"time"
)

var campaignStart = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

func TestPlanCampaignWindow(t *testing.T) {
	cases := []struct {
		name   string
		start  time.Time
		days   int
		want   time.Time
		wantOK bool
	}{
		{name: "default 14 days", start: campaignStart, want: campaignStart.AddDate(0, 0, 14), wantOK: true},
		{name: "minimum 3", start: campaignStart, days: 3, want: campaignStart.AddDate(0, 0, 3), wantOK: true},
		{name: "maximum 60", start: campaignStart, days: 60, want: campaignStart.AddDate(0, 0, 60), wantOK: true},
		{name: "below minimum", start: campaignStart, days: 2},
		{name: "above maximum", start: campaignStart, days: 61},
		{name: "missing start", days: 14},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := PlanCampaign(c.start, c.days)
			if !c.wantOK {
				expectCampaignCode(t, err, CodeCampaignWindow)
				return
			}
			if err != nil || !got.Equal(c.want) {
				t.Fatalf("got %v %v, want %v", got, err, c.want)
			}
		})
	}
}

func TestCloseCampaignRules(t *testing.T) {
	closes := campaignStart.AddDate(0, 0, 14)
	after := closes.Add(time.Hour)
	mgr, user := "mgr-1", "user-1"

	t.Run("manager cannot certify own role", func(t *testing.T) {
		_, err := CloseCampaign(CampaignCloseInput{Entries: []CampaignEntry{{ManagerID: mgr, UserID: mgr, RoleName: "AP"}}, ClosesAt: closes, Now: after})
		expectCampaignCode(t, err, CodeCampaignSelf)
	})
	t.Run("duplicate user role", func(t *testing.T) {
		_, err := CloseCampaign(CampaignCloseInput{Entries: []CampaignEntry{
			{ManagerID: mgr, UserID: user, RoleName: "AP", Decision: CertCertify},
			{ManagerID: mgr, UserID: user, RoleName: "AP", Decision: CertRevoke},
		}, ClosesAt: closes, Now: after})
		expectCampaignCode(t, err, CodeCampaignDecision)
	})
	t.Run("unknown decision", func(t *testing.T) {
		_, err := CloseCampaign(CampaignCloseInput{Entries: []CampaignEntry{{ManagerID: mgr, UserID: user, RoleName: "AP", Decision: "MAYBE"}}, ClosesAt: closes, Now: after})
		expectCampaignCode(t, err, CodeCampaignDecision)
	})
	t.Run("uncertified roles are flagged without auto-revoke", func(t *testing.T) {
		res, err := CloseCampaign(CampaignCloseInput{Entries: []CampaignEntry{
			{ManagerID: mgr, UserID: user, RoleName: "AP"},
			{ManagerID: mgr, UserID: user, RoleName: "GL", Decision: CertCertify},
		}, ClosesAt: closes, Now: after})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Revoke) != 0 || len(res.Flagged) != 1 || res.Flagged[0].RoleName != "AP" {
			t.Fatalf("pending role should be flagged: %+v", res)
		}
		if res.Progress.OverdueByManager[mgr] != 1 || res.Progress.PercentCertified != 50 {
			t.Fatalf("progress wrong: %+v", res.Progress)
		}
	})
	t.Run("auto-revoke revokes uncertified roles", func(t *testing.T) {
		res, err := CloseCampaign(CampaignCloseInput{Entries: []CampaignEntry{
			{ManagerID: mgr, UserID: user, RoleName: "AP"},
			{ManagerID: mgr, UserID: user, RoleName: "GL", Decision: CertRevoke},
			{ManagerID: mgr, UserID: user, RoleName: "AR", Decision: CertCertify},
		}, AutoRevoke: true, ClosesAt: closes, Now: after})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Revoke) != 2 || len(res.Flagged) != 0 || res.Progress.Revoked != 2 {
			t.Fatalf("want two revocations: %+v", res)
		}
		if res.Progress.PercentCertified != 33 || res.Progress.Total != 3 || res.Progress.OverdueByManager[mgr] != 1 || !res.Closed {
			t.Fatalf("progress wrong: %+v", res.Progress)
		}
	})
	t.Run("missing close instant", func(t *testing.T) {
		_, err := CloseCampaign(CampaignCloseInput{Entries: []CampaignEntry{{ManagerID: mgr, UserID: user, RoleName: "AP"}}, Now: after})
		expectCampaignCode(t, err, CodeCampaignWindow)
	})
	t.Run("before the window ends nothing pending is revoked or flagged", func(t *testing.T) {
		res, err := CloseCampaign(CampaignCloseInput{Entries: []CampaignEntry{
			{ManagerID: mgr, UserID: user, RoleName: "AP"},
			{ManagerID: mgr, UserID: user, RoleName: "GL", Decision: CertRevoke},
		}, AutoRevoke: true, ClosesAt: closes, Now: closes.Add(-time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
		if res.Closed || len(res.Revoke) != 1 || res.Revoke[0].RoleName != "GL" || len(res.Flagged) != 0 || res.Progress.Pending != 1 {
			t.Fatalf("open campaign must only report progress: %+v", res)
		}
	})
	t.Run("overdue counts need the window to have ended", func(t *testing.T) {
		res, err := CloseCampaign(CampaignCloseInput{Entries: []CampaignEntry{{ManagerID: mgr, UserID: user, RoleName: "AP"}}, ClosesAt: closes, Now: closes.Add(-time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Progress.OverdueByManager) != 0 {
			t.Fatalf("window still open, nothing overdue: %+v", res.Progress)
		}
	})
}

func expectCampaignCode(t *testing.T, err error, code string) {
	t.Helper()
	var de *Error
	if !errors.As(err, &de) || de.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}
