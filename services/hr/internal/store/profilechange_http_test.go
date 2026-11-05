package store_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/hr/internal/store"
)

const dutyWorkerMaintain = "hr.worker.maintain"

func (o *org) change(t *testing.T, user, body string) store.ProfileChange {
	t.Helper()
	rec := o.as(user, http.MethodPost, "/v1/hr/me/profile-changes", body, dutyEssBase)
	if rec.Code != http.StatusCreated {
		t.Fatalf("request change %s: status %d body %s", body, rec.Code, rec.Body.String())
	}
	return decodeBody[store.ProfileChange](t, rec)
}

func (o *org) sealedOnChange(t *testing.T, id string) bool {
	t.Helper()
	var sealed bool
	if err := dbx.WithTenantTx(o.ctx, o.st.Pool(), func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `select value_sealed is not null from hr_profile_change where id = $1`, id).Scan(&sealed)
	}); err != nil {
		t.Fatal(err)
	}
	return sealed
}

func TestEmployeeChangesOwnProfileWithApprovalForSensitiveFields(t *testing.T) {
	o := newOrg(t)
	rep := o.users["rep"]

	name := o.change(t, rep, `{"kind":"PREFERRED_NAME","preferredName":"  Rae "}`)
	if name.Status != "APPLIED" || name.WorkerID != o.workers["rep"].ID || name.Summary != "Rae" {
		t.Fatalf("preferred name change = %+v", name)
	}
	if w, _ := o.st.GetWorker(o.ctx, o.workers["rep"].ID); w.PreferredName == nil || *w.PreferredName != "Rae" {
		t.Fatalf("preferred name not applied: %v", w.PreferredName)
	}

	addr := o.change(t, rep, `{"kind":"HOME_ADDRESS","address":{"line1":"1 Main St","city":"Springfield","postalCode":"62701","country":"us"}}`)
	bank := o.change(t, rep, `{"kind":"BANK_ACCOUNT","bankAccount":"DE89 3704 0044 0532 0130 00"}`)
	if addr.Status != "PENDING" || addr.Summary != "Springfield, US" || bank.Status != "PENDING" || bank.Summary != "****3000" {
		t.Fatalf("sensitive changes = %+v / %+v, want pending with masked summaries", addr, bank)
	}
	if strings.Contains(o.as(rep, http.MethodGet, "/v1/hr/me/profile-changes", "", dutyEssBase).Body.String(), "Main St") {
		t.Fatal("the change list leaks the full address")
	}
	if rec := o.as(rep, http.MethodPost, "/v1/hr/me/profile-changes", `{"kind":"BANK_ACCOUNT","bankAccount":"GB29NWBK60161331926819"}`, dutyEssBase); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), store.CodeProfileChangePending) {
		t.Fatalf("second pending bank change: status %d body %s", rec.Code, rec.Body.String())
	}
	if !o.sealedOnChange(t, addr.ID) {
		t.Fatal("a pending address change must hold its sealed value")
	}

	// The manager's queue holds the direct report's address change only, never the bank change.
	mgr := o.users["mgr"]
	queue := decodeBody[listOf[store.ProfileChange]](t, o.as(mgr, http.MethodGet, "/v1/hr/me/team/profile-changes?status=PENDING", "", dutyMssApprove))
	if len(queue.Items) != 1 || queue.Items[0].ID != addr.ID {
		t.Fatalf("manager queue = %+v, want only the address change", queue.Items)
	}
	if rec := o.as(mgr, http.MethodPost, "/v1/hr/me/team/profile-changes/"+bank.ID+"/decide", `{"decision":"APPROVE"}`, dutyMssApprove); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "HCM-ESS-005") {
		t.Fatalf("manager approving a bank change: status %d body %s", rec.Code, rec.Body.String())
	}
	for _, user := range []string{o.users["peer"], o.users["ceo"]} {
		rec := o.as(user, http.MethodPost, "/v1/hr/me/team/profile-changes/"+addr.ID+"/decide", `{"decision":"APPROVE"}`, dutyMssApprove)
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), store.CodeProfileChangeNotFound) {
			t.Errorf("non-manager deciding: status %d body %s, want 404", rec.Code, rec.Body.String())
		}
	}
	if q := decodeBody[listOf[store.ProfileChange]](t, o.as(o.users["peer"], http.MethodGet, "/v1/hr/me/team/profile-changes", "", dutyMssApprove)); len(q.Items) != 0 {
		t.Fatalf("a peer's queue shows %d changes, want 0", len(q.Items))
	}

	approved := o.as(mgr, http.MethodPost, "/v1/hr/me/team/profile-changes/"+addr.ID+"/decide", `{"decision":"APPROVE","note":"ok"}`, dutyMssApprove)
	if got := decodeBody[store.ProfileChange](t, approved); approved.Code != http.StatusOK || got.Status != "APPROVED" || got.DecidedBy == nil || *got.DecidedBy != mgr {
		t.Fatalf("approve: status %d body %s", approved.Code, approved.Body.String())
	}
	if o.sealedOnChange(t, addr.ID) {
		t.Fatal("the sealed copy must be dropped once decided")
	}
	if again := o.as(mgr, http.MethodPost, "/v1/hr/me/team/profile-changes/"+addr.ID+"/decide", `{"decision":"APPROVE"}`, dutyMssApprove); again.Code != http.StatusOK {
		t.Fatalf("repeat approve: status %d", again.Code)
	}
	if flip := o.as(mgr, http.MethodPost, "/v1/hr/me/team/profile-changes/"+addr.ID+"/decide", `{"decision":"REJECT"}`, dutyMssApprove); flip.Code != http.StatusConflict {
		t.Fatalf("reject after approve: status %d, want 409", flip.Code)
	}

	pii, err := o.st.ReadPII(o.ctx, o.workers["rep"].ID, false, "", "")
	if err != nil || pii.HomeAddress == nil || *pii.HomeAddress != "Springfield, US" || pii.BankAccount != nil {
		t.Fatalf("masked pii after address approval = %+v (err %v)", pii, err)
	}
	revealed, err := o.st.ReadPII(o.ctx, o.workers["rep"].ID, true, "testing the reveal", "")
	if err != nil || revealed.HomeAddress == nil || *revealed.HomeAddress != "1 Main St, Springfield, 62701, US" {
		t.Fatalf("revealed pii = %+v (err %v)", revealed, err)
	}

	// HR decides the bank change; nobody decides their own.
	if rec := o.as(rep, http.MethodPost, "/v1/hr/profile-changes/"+bank.ID+"/decide", `{"decision":"APPROVE"}`, dutyWorkerMaintain); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "HCM-ESS-006") {
		t.Fatalf("self-approval: status %d body %s", rec.Code, rec.Body.String())
	}
	if rec := o.as(o.users["peer"], http.MethodPost, "/v1/hr/profile-changes/"+bank.ID+"/decide", `{"decision":"APPROVE"}`, dutyWorkerMaintain); rec.Code != http.StatusOK {
		t.Fatalf("HR approval: status %d body %s", rec.Code, rec.Body.String())
	}
	if pii, _ := o.st.ReadPII(o.ctx, o.workers["rep"].ID, false, "", ""); pii.BankAccount == nil || *pii.BankAccount != "****3000" {
		t.Fatalf("bank after approval = %v", pii.BankAccount)
	}
	var audits int
	if err := dbx.WithTenantTx(o.ctx, o.st.Pool(), func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `select count(*) from audit_event where action = 'p3_change' and entity_id = $1 and reason like 'self-service profile change %'`, o.workers["rep"].ID).Scan(&audits)
	}); err != nil || audits != 2 {
		t.Fatalf("p3_change audit rows = %d (err %v), want 2", audits, err)
	}
	all := decodeBody[listOf[store.ProfileChange]](t, o.as(o.users["peer"], http.MethodGet, "/v1/hr/profile-changes?workerId="+o.workers["rep"].ID, "", dutyWorkerMaintain))
	if len(all.Items) != 3 || all.Items[0].ID != bank.ID {
		t.Fatalf("HR list = %d items, want 3 newest first", len(all.Items))
	}
}

func TestEmployeeWithdrawsOnlyTheirOwnPendingChange(t *testing.T) {
	o := newOrg(t)
	bank := o.change(t, o.users["rep"], `{"kind":"BANK_ACCOUNT","bankAccount":"12345678"}`)
	if rec := o.as(o.users["peer"], http.MethodPost, "/v1/hr/me/profile-changes/"+bank.ID+"/cancel", "", dutyEssBase); rec.Code != http.StatusNotFound {
		t.Fatalf("another worker withdrawing: status %d, want 404", rec.Code)
	}
	for i := 0; i < 2; i++ {
		rec := o.as(o.users["rep"], http.MethodPost, "/v1/hr/me/profile-changes/"+bank.ID+"/cancel", "", dutyEssBase)
		if got := decodeBody[store.ProfileChange](t, rec); rec.Code != http.StatusOK || got.Status != "CANCELED" {
			t.Fatalf("withdraw #%d: status %d body %s", i+1, rec.Code, rec.Body.String())
		}
	}
	if o.sealedOnChange(t, bank.ID) {
		t.Fatal("a withdrawn change must drop its sealed value")
	}
	if rec := o.as(o.users["peer"], http.MethodPost, "/v1/hr/profile-changes/"+bank.ID+"/decide", `{"decision":"APPROVE"}`, dutyWorkerMaintain); rec.Code != http.StatusConflict {
		t.Fatalf("approving a withdrawn change: status %d, want 409", rec.Code)
	}
	// A new pending change of the same kind is allowed once the old one is withdrawn.
	o.change(t, o.users["rep"], `{"kind":"BANK_ACCOUNT","bankAccount":"87654321"}`)
	if rec := o.as(ids.New(), http.MethodPost, "/v1/hr/me/profile-changes", `{"kind":"PREFERRED_NAME","preferredName":"x"}`, dutyEssBase); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), store.CodeNoLinkedWorker) {
		t.Fatalf("unlinked sign-in: status %d body %s", rec.Code, rec.Body.String())
	}
}
