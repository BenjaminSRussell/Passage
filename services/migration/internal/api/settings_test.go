package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

const settingsPath = "/v1/integration/migration-settings"

type settingsBody struct {
	Min       int    `json:"recurringMinIntervalMin"`
	Version   int    `json:"version"`
	UpdatedAt string `json:"updatedAt"`
}

func readSettings(t *testing.T, r http.Handler) settingsBody {
	t.Helper()
	rec := do(r, http.MethodGet, settingsPath, "", duties, true)
	var out settingsBody
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
		t.Fatalf("get settings = %d %s", rec.Code, rec.Body)
	}
	return out
}

func TestSettings_DefaultsThenVersionedWrites(t *testing.T) {
	r, _ := newProjectRouter()
	if got := readSettings(t, r); got.Min != 15 || got.Version != 0 || got.UpdatedAt != "" {
		t.Fatalf("defaults = %+v", got)
	}
	rec := do(r, http.MethodPut, settingsPath, `{"version":0,"recurringMinIntervalMin":60}`, writeDuties, true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"version":1`) {
		t.Fatalf("first write = %d %s", rec.Code, rec.Body)
	}
	if got := readSettings(t, r); got.Min != 60 || got.Version != 1 || got.UpdatedAt == "" {
		t.Fatalf("after write = %+v", got)
	}
	// A stale version is refused and names the current one.
	if rec := do(r, http.MethodPut, settingsPath, `{"version":0,"recurringMinIntervalMin":90}`, writeDuties, true); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "current version is 1") {
		t.Fatalf("stale = %d %s", rec.Code, rec.Body)
	}
	if rec := do(r, http.MethodPut, settingsPath, `{"version":1,"recurringMinIntervalMin":1440}`, writeDuties, true); rec.Code != http.StatusOK || readSettings(t, r).Version != 2 {
		t.Fatalf("second write = %d %s", rec.Code, rec.Body)
	}
}

func TestSettings_ValidationAndDuties(t *testing.T) {
	r, _ := newProjectRouter()
	for name, body := range map[string]string{
		"below floor":      `{"version":0,"recurringMinIntervalMin":14}`,
		"above one day":    `{"version":0,"recurringMinIntervalMin":1441}`,
		"missing value":    `{"version":0}`,
		"missing version":  `{"recurringMinIntervalMin":30}`,
		"negative version": `{"version":-1,"recurringMinIntervalMin":30}`,
		"unknown field":    `{"version":0,"recurringMinIntervalMin":30,"max":5}`,
	} {
		if rec := do(r, http.MethodPut, settingsPath, body, writeDuties, true); rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d %s", name, rec.Code, rec.Body)
		}
	}
	if rec := do(r, http.MethodPut, settingsPath, `{"version":0,"recurringMinIntervalMin":30}`, duties, true); rec.Code != http.StatusForbidden {
		t.Fatalf("run duty must not write settings: %d", rec.Code)
	}
	if rec := do(r, http.MethodGet, settingsPath, "", "tenant.administer", true); rec.Code != http.StatusForbidden {
		t.Fatalf("read needs migration.run: %d", rec.Code)
	}
	if rec := do(r, http.MethodGet, settingsPath, "", duties, false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no principal: %d", rec.Code)
	}
}

// The tenant minimum applies to every schedule written after it: a project's cron on create and edit, a binding's own.
func TestSettings_MinimumGovernsProjectAndBindingSchedules(t *testing.T) {
	r, _ := newProjectRouter()
	create := func(name, cron string) int {
		return do(r, http.MethodPost, projectsPath, `{"name":"`+name+`","projectType":"migrate","sourceSystem":"ax2012","scheduleCron":"`+cron+`"}`, writeDuties, true).Code
	}
	if code := create("Quarter", "*/15 * * * *"); code != http.StatusCreated {
		t.Fatalf("default allows 15 minutes: %d", code)
	}
	if rec := do(r, http.MethodPut, settingsPath, `{"version":0,"recurringMinIntervalMin":60}`, writeDuties, true); rec.Code != http.StatusOK {
		t.Fatalf("settings = %d %s", rec.Code, rec.Body)
	}
	if code := create("Half", "*/30 * * * *"); code != http.StatusBadRequest {
		t.Fatalf("30 minutes under a 60 minute minimum: %d", code)
	}
	rec := do(r, http.MethodPost, projectsPath, `{"name":"Hourly","projectType":"migrate","sourceSystem":"ax2012","scheduleCron":"0 * * * *"}`, writeDuties, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("hourly = %d %s", rec.Code, rec.Body)
	}
	p := decodeProject(t, rec)
	path := projectsPath + "/" + p.ID
	if rec := do(r, http.MethodPatch, path, `{"version":1,"scheduleCron":"*/45 * * * *"}`, writeDuties, true); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "at least 60 minutes") {
		t.Fatalf("patch below minimum = %d %s", rec.Code, rec.Body)
	}
	// An edit that leaves the schedule alone is not held to a minimum raised after the schedule was written.
	if rec := do(r, http.MethodPut, settingsPath, `{"version":1,"recurringMinIntervalMin":120}`, writeDuties, true); rec.Code != http.StatusOK {
		t.Fatalf("raise = %d %s", rec.Code, rec.Body)
	}
	if rec := do(r, http.MethodPatch, path, `{"version":1,"name":"Hourly renamed"}`, writeDuties, true); rec.Code != http.StatusOK {
		t.Fatalf("rename keeps the old schedule = %d %s", rec.Code, rec.Body)
	}
	if rec := do(r, http.MethodPatch, path, `{"version":2,"scheduleCron":""}`, writeDuties, true); rec.Code != http.StatusOK {
		t.Fatalf("clearing a schedule is always allowed = %d %s", rec.Code, rec.Body)
	}
}

func TestSettings_MinimumGovernsBindingSchedule(t *testing.T) {
	s := newSyncRig(t)
	if rec := do(s.r, http.MethodPut, settingsPath, `{"version":0,"recurringMinIntervalMin":60}`, liveDuties, true); rec.Code != http.StatusOK {
		t.Fatalf("settings = %d %s", rec.Code, rec.Body)
	}
	cur := s.status()
	if rec := s.patch(`{"version":` + itoa(cur.Version) + `,"scheduleCron":"*/30 * * * *"}`); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "at least 60 minutes") {
		t.Fatalf("binding cron below the tenant minimum = %d %s", rec.Code, rec.Body)
	}
	if rec := s.patch(`{"version":` + itoa(cur.Version) + `,"scheduleCron":"0 * * * *"}`); rec.Code != http.StatusOK {
		t.Fatalf("hourly binding cron = %d %s", rec.Code, rec.Body)
	}
}
