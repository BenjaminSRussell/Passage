package store

import (
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
)

// FR-25.11 through Postgres as passage_app: while a tenant has no reason defined a downtime takes
// free text; once it has, coding needs an existing active reason (SF-0158, SF-0159) whose category
// the downtime takes (SF-0160 for another one); a reason is versioned (SF-0143), its code is unique
// (SF-0142) and every save writes downtimereason.saved.
func TestDowntimeReason_MasterGuardsCoding(t *testing.T) {
	s, _, admin := setup(t)
	tenantID := testkit.TenantID(76)
	ctx := testkit.Ctx(tenantID)
	mustMachine(t, s, ctx, "M-76", ids.New())
	at := time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)
	if _, err := s.IngestMachineEvents(ctx, at.Add(5*time.Minute), []MachineEventIn{floorEvent("M-76", 1, "state", at, domain.MachineDown)}); err != nil {
		t.Fatal(err)
	}
	downs, err := s.ListDowntimes(ctx, DowntimeFilter{Limit: 10})
	if err != nil || len(downs) != 1 {
		t.Fatalf("downtimes = %+v, %v", downs, err)
	}
	id := downs[0].ID

	if d, err := s.CodeDowntime(ctx, id, "ANYTHING", "", Idem{Key: "k0", Fingerprint: "f0"}); err != nil || d.ReasonCode == nil || *d.ReasonCode != "ANYTHING" {
		t.Fatalf("free text before a master exists = %+v, %v", d, err)
	}
	jam, err := s.CreateDowntimeReason(ctx, DowntimeReasonInput{Code: "JAM", Name: "Material jam", Category: "unplanned", Active: true}, "sup", Idem{Key: "r1", Fingerprint: "r1"})
	if err != nil || jam.Version != 1 {
		t.Fatalf("create = %+v, %v", jam, err)
	}
	chg, err := s.CreateDowntimeReason(ctx, DowntimeReasonInput{Code: "CHG", Name: "Changeover", Category: "planned", Active: true}, "sup", Idem{Key: "r2", Fingerprint: "r2"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateDowntimeReason(ctx, DowntimeReasonInput{Code: "JAM", Name: "Again", Category: "planned", Active: true}, "sup", Idem{Key: "r3", Fingerprint: "r3"}); problemCode(err) != CodeSetupCodeTaken {
		t.Fatalf("duplicate code = %v, want %s", err, CodeSetupCodeTaken)
	}

	if _, err := s.CodeDowntime(ctx, id, "ANYTHING", "", Idem{Key: "k1", Fingerprint: "f1"}); problemCode(err) != CodeReasonNotFound {
		t.Fatalf("free text after a master exists = %v, want %s", err, CodeReasonNotFound)
	}
	if _, err := s.CodeDowntime(ctx, id, "JAM", "planned", Idem{Key: "k2", Fingerprint: "f2"}); problemCode(err) != CodeReasonCategory {
		t.Fatalf("other category = %v, want %s", err, CodeReasonCategory)
	}
	d, err := s.CodeDowntime(ctx, id, "CHG", "", Idem{Key: "k3", Fingerprint: "f3"})
	if err != nil || d.Category != "planned" || d.ReasonCode == nil || *d.ReasonCode != "CHG" {
		t.Fatalf("coded = %+v, %v", d, err)
	}

	if _, err := s.UpdateDowntimeReason(ctx, chg.ID, 7, DowntimeReasonInput{Name: "Changeover", Category: "planned"}, "sup"); problemCode(err) != CodeVersionMismatch {
		t.Fatalf("stale version = %v, want %s", err, CodeVersionMismatch)
	}
	off, err := s.UpdateDowntimeReason(ctx, chg.ID, chg.Version, DowntimeReasonInput{Name: "Changeover", Category: "planned", Active: false}, "sup")
	if err != nil || off.Active || off.Version != 2 {
		t.Fatalf("deactivate = %+v, %v", off, err)
	}
	if _, err := s.CodeDowntime(ctx, id, "CHG", "", Idem{Key: "k4", Fingerprint: "f4"}); problemCode(err) != CodeReasonInactive {
		t.Fatalf("inactive reason = %v, want %s", err, CodeReasonInactive)
	}
	if _, err := s.UpdateDowntimeReason(ctx, ids.New(), 1, DowntimeReasonInput{Name: "x", Category: "planned"}, "sup"); problemCode(err) != CodeReasonNotFound {
		t.Fatalf("unknown reason = %v", err)
	}
	all, err := s.ListDowntimeReasons(ctx, false, 10)
	active, err2 := s.ListDowntimeReasons(ctx, true, 10)
	if err != nil || err2 != nil || len(all) != 2 || len(active) != 1 || active[0].Code != "JAM" {
		t.Fatalf("lists = %+v / %+v, %v %v", all, active, err, err2)
	}
	if n := countEvents(t, admin, tenantID, "dev.passage.shopfloor.downtimereason.saved.v1"); n != 3 {
		t.Fatalf("downtimereason.saved events = %d, want 3", n)
	}
}
