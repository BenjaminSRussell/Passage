package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	"github.com/BenjaminSRussell/passage/services/edi/internal/store"
)

func patchPartnerReq(t *testing.T, h http.Handler, id, body, ifMatch, duties string, headers map[string]string) (store.Partner, int, string, string) {
	t.Helper()
	hs := map[string]string{}
	for k, v := range headers {
		hs[k] = v
	}
	if ifMatch != "" {
		hs["If-Match"] = ifMatch
	}
	rec := do(t, h, http.MethodPatch, "/edi/v1/partners/"+id, body, duties, hs)
	var p store.Partner
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		return p, rec.Code, "", rec.Header().Get("ETag")
	}
	return p, rec.Code, problemCode(t, rec), rec.Header().Get("ETag")
}

func TestPatchPartner_IfMatchMergePatchScopeAndDuty(t *testing.T) {
	h := dbHandler(t)
	p := createPartner(t, h, "p", distributor)

	if _, code, pc, _ := patchPartnerReq(t, h, p.ID, `{"name":"New"}`, "", "edi.admin", nil); code != http.StatusPreconditionRequired || pc != CodeIfMatchRequired {
		t.Fatalf("no If-Match: %d %s", code, pc)
	}
	if _, code, pc, _ := patchPartnerReq(t, h, p.ID, `{"name":"New"}`, `"abc"`, "edi.admin", nil); code != http.StatusBadRequest || pc != CodeValidation {
		t.Fatalf("bad If-Match: %d %s", code, pc)
	}
	if _, code, _, _ := patchPartnerReq(t, h, p.ID, `{"name":"New"}`, `"1"`, "edi.operate", nil); code != http.StatusForbidden {
		t.Fatalf("edi.operate: %d", code)
	}
	if _, code, _, _ := patchPartnerReq(t, h, p.ID, `{"environment":"PROD"}`, `"1"`, "edi.admin", nil); code != http.StatusBadRequest {
		t.Fatalf("environment cannot change: %d", code)
	}
	if _, code, pc, _ := patchPartnerReq(t, h, p.ID, `{"isaId":"has space","status":"gone","ackSlaMinutes":0}`, `"1"`, "edi.admin", nil); code != http.StatusBadRequest || pc != CodeValidation {
		t.Fatalf("validation: %d %s", code, pc)
	}
	got, code, pc, etag := patchPartnerReq(t, h, p.ID, `{"name":"Distributor (EU)","ackSlaMinutes":15,"edifactId":"4012345000009","edifactQualifier":"14"}`, `"1"`, "edi.admin", nil)
	if code != http.StatusOK || etag != `"2"` || got.Name != "Distributor (EU)" || got.AckSLAMinutes != 15 || got.ISAID != "DISTRIBUTOR" ||
		got.EdifactID != "4012345000009" || got.EdifactContrl != domain.ContrlRequested || got.Version != 2 {
		t.Fatalf("patch: %d %s %+v", code, pc, got)
	}
	// The EDIFACT identity now resolves inbound EDIFACT.
	if _, code, pc := postEdifact(t, h, ordersEdifact(t)); code != http.StatusCreated {
		t.Fatalf("EDIFACT after the patch: %d %s", code, pc)
	}
	// A stale ETag is 412 with the current one; W/ and bare forms are read.
	if _, code, pc, etag := patchPartnerReq(t, h, p.ID, `{"name":"Again"}`, `"1"`, "edi.admin", nil); code != http.StatusPreconditionFailed || pc != CodeVersionConflict || etag != `"2"` {
		t.Fatalf("stale: %d %s %s", code, pc, etag)
	}
	if got, code, _, _ := patchPartnerReq(t, h, p.ID, `{"edifactContrl":"NEVER"}`, `W/"2"`, "edi.admin", nil); code != http.StatusOK || got.EdifactContrl != "NEVER" || got.EdifactQualifier != "14" {
		t.Fatalf("weak ETag: %d %+v", code, got)
	}
	// null removes the EDIFACT identity; a qualifier alone is refused.
	if _, code, pc, _ := patchPartnerReq(t, h, p.ID, `{"edifactId":null,"edifactQualifier":"14"}`, `3`, "edi.admin", nil); code != http.StatusBadRequest || pc != CodeValidation {
		t.Fatalf("qualifier without id: %d %s", code, pc)
	}
	if got, code, _, _ := patchPartnerReq(t, h, p.ID, `{"edifactId":null}`, `3`, "edi.admin", nil); code != http.StatusOK || got.EdifactID != "" || got.EdifactQualifier != "" || got.EdifactContrl != "" {
		t.Fatalf("remove EDIFACT: %d %+v", code, got)
	}
	// Another partner's ISA identity is 409; an unknown partner 404.
	createPartner(t, h, "o", `{"name":"Other","isaQualifier":"ZZ","isaId":"OTHER","environment":"TEST"}`)
	if _, code, pc, _ := patchPartnerReq(t, h, p.ID, `{"isaId":"OTHER"}`, `"4"`, "edi.admin", nil); code != http.StatusConflict || pc != CodePartnerExists {
		t.Fatalf("duplicate identity: %d %s", code, pc)
	}
	if _, code, _, _ := patchPartnerReq(t, h, "0195f0a0-0000-7000-8000-0000000000aa", `{"name":"x"}`, `"1"`, "edi.admin", nil); code != http.StatusNotFound {
		t.Fatalf("unknown: %d", code)
	}

	// Inactive: inbound from the partner is refused (its sender no longer resolves).
	if got, code, _, _ := patchPartnerReq(t, h, p.ID, `{"status":"inactive"}`, `"4"`, "edi.admin", nil); code != http.StatusOK || got.Status != "inactive" {
		t.Fatalf("deactivate: %d %+v", code, got)
	}
	if rec := do(t, h, http.MethodPost, "/edi/v1/inbound/x12?environment=TEST", sample850(t, "1"), "edi.operate", nil); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("inbound from an inactive partner: %d", rec.Code)
	}

	// Bound to srcLE by its trade profile: a caller of another entity is 403.
	putProfile(t, h, p.ID, profileBody)
	otherLE := map[string]string{tenant.HeaderLegalEntity: "0195f0a0-0000-7000-8000-0000000000e9"}
	if _, code, pc, _ := patchPartnerReq(t, h, p.ID, `{"status":"active"}`, `"5"`, "edi.admin", otherLE); code != http.StatusForbidden || pc != CodePartnerScope {
		t.Fatalf("other entity: %d %s", code, pc)
	}
	if got, code, _, etag := patchPartnerReq(t, h, p.ID, `{"status":"active"}`, `"5"`, "edi.admin", srcScope); code != http.StatusOK || got.Status != "active" || etag != `"6"` {
		t.Fatalf("own entity: %d %+v", code, got)
	}
	if rec := do(t, h, http.MethodGet, "/edi/v1/partners/"+p.ID, "", "edi.admin", nil); rec.Header().Get("ETag") != `"6"` || !strings.Contains(rec.Body.String(), `"status":"active"`) {
		t.Fatalf("read back: %s %s", rec.Header().Get("ETag"), rec.Body.String())
	}
}
