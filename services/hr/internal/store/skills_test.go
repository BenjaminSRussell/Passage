package store

import (
	"context"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/hr/internal/domain"
	hrevents "github.com/BenjaminSRussell/passage/services/hr/internal/events"
)

func mustSkill(t *testing.T, s *Postgres, ctx context.Context, code string, scale int) Skill {
	t.Helper()
	k, _, err := s.CreateSkill(ctx, Skill{Code: code, Name: code + " skill", ScaleMax: scale}, "")
	if err != nil {
		t.Fatalf("create skill %s: %v", code, err)
	}
	return k
}

func mustRate(t *testing.T, s *Postgres, ctx context.Context, workerID, code string, rating int, on time.Time) {
	t.Helper()
	if _, _, err := s.RateWorkerSkill(ctx, workerID, code, rating, on); err != nil {
		t.Fatalf("rate %s %d: %v", code, rating, err)
	}
}

func mustCert(t *testing.T, s *Postgres, ctx context.Context, workerID, code, issued string, expires *string) Certification {
	t.Helper()
	c, _, err := s.IssueCertification(ctx, Certification{WorkerID: workerID, Code: code, Name: code, IssuedOn: issued, ExpiresOn: expires},
		day(time.June, 1), "")
	if err != nil {
		t.Fatalf("issue %s: %v", code, err)
	}
	return c
}

func strp(v string) *string { return &v }

// TestSkillCatalogReplayDuplicateAndPaging: a replayed key returns the stored
// skill, a different body under the key is refused, a second skill with the
// same code is HCM-SKL-DUPLICATE, and the catalog pages by code.
func TestSkillCatalogReplayDuplicateAndPaging(t *testing.T) {
	s, ctx := newStore(t)
	welding, inserted, err := s.CreateSkill(ctx, Skill{Code: "WELDING", Name: "Welding", ScaleMax: 5}, "skill-key-1")
	if err != nil || !inserted {
		t.Fatalf("create: inserted=%v err=%v", inserted, err)
	}
	replay, inserted, err := s.CreateSkill(ctx, Skill{Code: "WELDING", Name: "Welding", ScaleMax: 5}, "skill-key-1")
	if err != nil || inserted || replay.ID != welding.ID {
		t.Fatalf("replay: id=%s inserted=%v err=%v", replay.ID, inserted, err)
	}
	_, _, err = s.CreateSkill(ctx, Skill{Code: "OTHER", Name: "Other", ScaleMax: 5}, "skill-key-1")
	requireCode(t, err, CodeIdempotencyReuse)
	_, _, err = s.CreateSkill(ctx, Skill{Code: "WELDING", Name: "Again", ScaleMax: 3}, "")
	requireCode(t, err, CodeSkillDuplicate)

	mustSkill(t, s, ctx, "PLC", 5)
	mustSkill(t, s, ctx, "FORKLIFT", 3)
	first, err := s.ListSkills(ctx, "", 2)
	if err != nil || len(first.Items) != 2 || first.Items[0].Code != "FORKLIFT" || first.Items[1].Code != "PLC" || first.NextAfter != "PLC" {
		t.Fatalf("first page = %+v, err %v", first, err)
	}
	rest, err := s.ListSkills(ctx, first.NextAfter, 2)
	if err != nil || len(rest.Items) != 1 || rest.Items[0].Code != "WELDING" || rest.NextAfter != "" {
		t.Fatalf("second page = %+v, err %v", rest, err)
	}
}

// TestRatingsReplaceStaleAndEvents (FR-42.10): a rating must sit on the
// skill's scale, replaces the earlier one, publishes workerskill.changed with
// the previous rating, and an older assessment cannot overwrite a newer one.
// Recording the same rating and date again publishes nothing.
func TestRatingsReplaceStaleAndEvents(t *testing.T) {
	s, ctx := newStore(t)
	mustSkill(t, s, ctx, "FORKLIFT", 3)
	w := mustWorker(t, s, ctx, "Rater")

	if _, _, err := s.RateWorkerSkill(ctx, w.ID, "FORKLIFT", 4, day(time.March, 1)); err == nil {
		t.Fatal("rating 4 on a 1-3 scale must fail")
	} else {
		requireCode(t, err, domain.CodeSkillRating)
	}
	_, _, err := s.RateWorkerSkill(ctx, w.ID, "NOPE", 1, day(time.March, 1))
	requireCode(t, err, CodeSkillNotFound)
	_, _, err = s.RateWorkerSkill(ctx, ids.New(), "FORKLIFT", 1, day(time.March, 1))
	requireCode(t, err, CodeWorkerNotFound)

	first, changed, err := s.RateWorkerSkill(ctx, w.ID, "FORKLIFT", 2, day(time.March, 1))
	if err != nil || !changed || first.Rating != 2 || first.AssessedOn != "2026-03-01" || first.ScaleMax != 3 {
		t.Fatalf("first rating = %+v changed=%v err=%v", first, changed, err)
	}
	if _, changed, err := s.RateWorkerSkill(ctx, w.ID, "FORKLIFT", 2, day(time.March, 1)); err != nil || changed {
		t.Fatalf("same rating again: changed=%v err=%v, want a no-op", changed, err)
	}
	up, changed, err := s.RateWorkerSkill(ctx, w.ID, "FORKLIFT", 3, day(time.April, 1))
	if err != nil || !changed || up.Rating != 3 {
		t.Fatalf("raise: %+v changed=%v err=%v", up, changed, err)
	}
	_, _, err = s.RateWorkerSkill(ctx, w.ID, "FORKLIFT", 1, day(time.March, 15))
	requireCode(t, err, CodeSkillStale)

	profile, err := s.ListWorkerSkills(ctx, w.ID)
	if err != nil || len(profile) != 1 || profile[0].Rating != 3 || profile[0].AssessedOn != "2026-04-01" {
		t.Fatalf("profile = %+v, err %v; the stale assessment must not land", profile, err)
	}

	var skillEvents []outboxRow
	for _, e := range outboxFor(t, s, ctx, w.ID) {
		if e.Type == hrevents.WorkerSkillChanged {
			skillEvents = append(skillEvents, e)
		}
	}
	if len(skillEvents) != 2 {
		t.Fatalf("workerskill.changed events = %d, want 2 (first rating and the raise)", len(skillEvents))
	}
	if skillEvents[0].Data["previous_rating"] != nil || skillEvents[1].Data["previous_rating"] != float64(2) ||
		skillEvents[1].Data["rating"] != float64(3) || skillEvents[1].Data["skill_code"] != "FORKLIFT" {
		t.Fatalf("event payloads = %+v", skillEvents)
	}
}

// TestSkillGapAC4216: position POS-200 requires WELDING >= 3 and PLC >= 2; a
// worker rated WELDING 4, PLC 1 has the single gap (PLC, 2, 1, -1). The
// comparison lists both requirements; removing PLC closes the gap.
func TestSkillGapAC4216(t *testing.T) {
	s, ctx := newStore(t)
	mustSkill(t, s, ctx, "WELDING", 5)
	mustSkill(t, s, ctx, "PLC", 5)
	w := mustWorker(t, s, ctx, "Ana")
	pos := mustPosition(t, s, ctx, "POS-200", ids.New(), ids.New(), "1.00", false, day(time.January, 1), nil)

	if _, err := s.SetRequirement(ctx, pos.ID, "WELDING", 3); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetRequirement(ctx, pos.ID, "PLC", 1); err != nil {
		t.Fatal(err)
	}
	if r, err := s.SetRequirement(ctx, pos.ID, "PLC", 2); err != nil || r.MinRating != 2 {
		t.Fatalf("replace PLC minimum: %+v %v", r, err)
	}
	_, err := s.SetRequirement(ctx, pos.ID, "PLC", 6)
	requireCode(t, err, domain.CodeSkillRating)
	_, err = s.SetRequirement(ctx, ids.New(), "PLC", 2)
	requireCode(t, err, CodePositionNotFound)

	mustRate(t, s, ctx, w.ID, "WELDING", 4, day(time.March, 1))
	mustRate(t, s, ctx, w.ID, "PLC", 1, day(time.March, 1))

	reqs, err := s.ListRequirements(ctx, pos.ID)
	if err != nil || len(reqs) != 2 || reqs[0].SkillCode != "PLC" || reqs[0].MinRating != 2 || reqs[1].SkillCode != "WELDING" {
		t.Fatalf("requirements = %+v, err %v", reqs, err)
	}
	gap, err := s.SkillGapFor(ctx, w.ID, pos.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := domain.GapRow{Skill: "PLC", Required: 2, Actual: 1, Delta: -1}
	if gap.Met || len(gap.Gaps) != 1 || gap.Gaps[0] != want {
		t.Fatalf("gaps = %+v, want only %+v", gap.Gaps, want)
	}
	if len(gap.Comparison) != 2 || gap.Comparison[1] != (domain.GapRow{Skill: "WELDING", Required: 3, Actual: 4, Delta: 1}) {
		t.Fatalf("comparison = %+v", gap.Comparison)
	}

	if err := s.RemoveRequirement(ctx, pos.ID, "PLC"); err != nil {
		t.Fatal(err)
	}
	requireCode(t, s.RemoveRequirement(ctx, pos.ID, "PLC"), CodeRequirementNotFound)
	if gap, err := s.SkillGapFor(ctx, w.ID, pos.ID); err != nil || !gap.Met || len(gap.Gaps) != 0 {
		t.Fatalf("after removing PLC: %+v %v", gap, err)
	}
}

// TestCertificationsIssueReplayAndValidity (BR-42.8, AC-42.9's 24 months): the
// certificate is valid the day before its expiry and not on it, a replay
// returns it, the same key for another worker is refused, and the same code
// and issue date twice is a duplicate. certification.issued is published once.
func TestCertificationsIssueReplayAndValidity(t *testing.T) {
	s, ctx := newStore(t)
	w := mustWorker(t, s, ctx, "Cert")
	other := mustWorker(t, s, ctx, "CertOther")
	in := Certification{WorkerID: w.ID, Code: "FORKLIFT", Name: "Forklift operator", Issuer: strp("OSHA"), IssuedOn: "2026-05-10", ExpiresOn: strp("2028-05-10")}
	cert, inserted, err := s.IssueCertification(ctx, in, day(time.June, 1), "cert-key-1")
	if err != nil || !inserted || !cert.Valid || *cert.ExpiresOn != "2028-05-10" || *cert.Issuer != "OSHA" {
		t.Fatalf("issue: %+v inserted=%v err=%v", cert, inserted, err)
	}
	again, inserted, err := s.IssueCertification(ctx, in, day(time.June, 1), "cert-key-1")
	if err != nil || inserted || again.ID != cert.ID {
		t.Fatalf("replay: %+v inserted=%v err=%v", again, inserted, err)
	}
	moved := in
	moved.WorkerID = other.ID
	_, _, err = s.IssueCertification(ctx, moved, day(time.June, 1), "cert-key-1")
	requireCode(t, err, CodeIdempotencyReuse)
	_, _, err = s.IssueCertification(ctx, in, day(time.June, 1), "")
	requireCode(t, err, CodeCertificationDuplicate)
	_, _, err = s.IssueCertification(ctx, Certification{WorkerID: ids.New(), Code: "X", Name: "X", IssuedOn: "2026-01-01"}, day(time.June, 1), "")
	requireCode(t, err, CodeWorkerNotFound)

	for _, c := range []struct {
		asOf  time.Time
		valid bool
	}{
		{time.Date(2026, 5, 9, 0, 0, 0, 0, time.UTC), false},
		{time.Date(2028, 5, 9, 0, 0, 0, 0, time.UTC), true},
		{time.Date(2028, 5, 10, 0, 0, 0, 0, time.UTC), false},
	} {
		list, err := s.ListCertifications(ctx, w.ID, c.asOf)
		if err != nil || len(list) != 1 || list[0].Valid != c.valid {
			t.Fatalf("as-of %s: %+v err %v, want valid=%v", c.asOf.Format("2006-01-02"), list, err, c.valid)
		}
	}
	events := outboxFor(t, s, ctx, cert.ID)
	if len(events) != 1 || events[0].Type != hrevents.CertificationIssued || events[0].Data["expires_on"] != "2028-05-10" {
		t.Fatalf("certification events = %+v", events)
	}
}

// TestExpiringCertificationsSkipRenewedAndPage: the inquiry lists certificates
// valid on asOf that expire inside the window, soonest first, leaves out the
// ones already renewed and pages with a cursor.
func TestExpiringCertificationsSkipRenewedAndPage(t *testing.T) {
	s, ctx := newStore(t)
	a := mustWorker(t, s, ctx, "ExpA")
	b := mustWorker(t, s, ctx, "ExpB")
	asOf := day(time.June, 1)
	mustCert(t, s, ctx, a.ID, "FORKLIFT", "2024-06-20", strp("2026-06-20")) // 19 days left
	mustCert(t, s, ctx, b.ID, "CRANE", "2024-07-05", strp("2026-07-05"))    // 34 days left
	mustCert(t, s, ctx, b.ID, "FIRSTAID", "2024-06-10", strp("2026-06-10")) // renewed below
	mustCert(t, s, ctx, b.ID, "FIRSTAID", "2026-05-20", strp("2028-05-20")) // the renewal
	mustCert(t, s, ctx, a.ID, "WELD", "2024-09-01", strp("2026-09-01"))     // outside 60 days
	mustCert(t, s, ctx, a.ID, "OLD", "2024-01-01", strp("2026-05-31"))      // already expired
	mustCert(t, s, ctx, a.ID, "FOREVER", "2024-01-01", nil)                 // never expires

	page, err := s.ListExpiringCertifications(ctx, asOf, 60, "", 1)
	if err != nil || len(page.Items) != 1 || page.Items[0].Code != "FORKLIFT" || page.Items[0].DaysLeft != 19 ||
		page.Items[0].PersonnelNumber != "WRK-ExpA" || page.NextAfter == "" {
		t.Fatalf("first page = %+v, err %v", page, err)
	}
	next, err := s.ListExpiringCertifications(ctx, asOf, 60, page.NextAfter, 1)
	if err != nil || len(next.Items) != 1 || next.Items[0].Code != "CRANE" || next.Items[0].DaysLeft != 34 || next.NextAfter != "" {
		t.Fatalf("second page = %+v, err %v; renewed, distant, expired and open-ended certificates are not listed", next, err)
	}
	wide, err := s.ListExpiringCertifications(ctx, asOf, 120, "", 10)
	if err != nil || len(wide.Items) != 3 || wide.Items[2].Code != "WELD" {
		t.Fatalf("120-day window = %+v, err %v; WELD comes in", wide, err)
	}
	_, err = s.ListExpiringCertifications(ctx, asOf, 60, "garbage", 1)
	requireCode(t, err, "COM-0001")
}

// TestSkillSearch (FR-42.18): workers employed on the date, rated at least the
// minimum on every skill and holding every certificate valid on the date.
func TestSkillSearch(t *testing.T) {
	s, ctx := newStore(t)
	mustSkill(t, s, ctx, "WELDING", 5)
	mustSkill(t, s, ctx, "PLC", 5)
	le := ids.New()
	hire := func(name string) Worker {
		w := mustWorker(t, s, ctx, name)
		mustEmployment(t, s, ctx, w.ID, le, "PRIMARY", day(time.January, 1))
		return w
	}
	ana := hire("Ana")
	ben := hire("Ben")
	cy := hire("Cy")
	gone := mustWorker(t, s, ctx, "Gone")
	end := day(time.March, 1)
	if _, _, err := s.CreateEmployment(ctx, gone.ID, Employment{LegalEntityID: le, EmploymentType: "FULL_TIME", AssignmentKind: "PRIMARY",
		HireDate: "2026-01-01", TerminationDate: strp("2026-02-28"), ValidFrom: day(time.January, 1), ValidTo: &end}, ""); err != nil {
		t.Fatal(err)
	}
	for _, w := range []Worker{ana, ben, gone} {
		mustRate(t, s, ctx, w.ID, "WELDING", 4, day(time.January, 5))
	}
	mustRate(t, s, ctx, cy.ID, "WELDING", 2, day(time.January, 5))
	mustRate(t, s, ctx, ana.ID, "PLC", 2, day(time.January, 5))
	mustCert(t, s, ctx, ana.ID, "OSHA10", "2026-01-10", strp("2026-07-10"))
	mustCert(t, s, ctx, ben.ID, "OSHA10", "2025-01-10", strp("2026-01-10"))

	names := func(found SkillSearch) []string {
		out := []string{}
		for _, m := range found.Items {
			out = append(out, m.LegalName)
		}
		return out
	}
	asOf := day(time.April, 1)
	found, err := s.SearchWorkers(ctx, []domain.Requirement{{Skill: "WELDING", MinRating: 3}}, nil, asOf)
	if got := names(found); err != nil || len(got) != 2 || got[0] != "Ana" || got[1] != "Ben" {
		t.Fatalf("WELDING>=3 = %v, err %v; Cy is rated 2 and Gone is no longer employed", got, err)
	}
	if found.Items[0].Skills[0] != (domain.GapRow{Skill: "WELDING", Required: 3, Actual: 4, Delta: 1}) {
		t.Fatalf("match rows = %+v", found.Items[0].Skills)
	}
	found, err = s.SearchWorkers(ctx, []domain.Requirement{{Skill: "WELDING", MinRating: 3}}, []string{"OSHA10"}, asOf)
	if got := names(found); err != nil || len(got) != 1 || got[0] != "Ana" {
		t.Fatalf("WELDING>=3 with OSHA10 = %v, err %v; Ben's certificate expired", got, err)
	}
	found, err = s.SearchWorkers(ctx, []domain.Requirement{{Skill: "WELDING", MinRating: 3}}, []string{"OSHA10"}, day(time.August, 1))
	if got := names(found); err != nil || len(got) != 0 {
		t.Fatalf("after Ana's certificate expires = %v, err %v", got, err)
	}
	found, err = s.SearchWorkers(ctx, nil, []string{"OSHA10"}, asOf)
	if got := names(found); err != nil || len(got) != 1 || got[0] != "Ana" || len(found.Items[0].Skills) != 0 {
		t.Fatalf("certificate-only search = %+v, err %v", found, err)
	}
	found, err = s.SearchWorkers(ctx, []domain.Requirement{{Skill: "WELDING", MinRating: 3}, {Skill: "PLC", MinRating: 2}, {Skill: "PLC", MinRating: 1}}, nil, asOf)
	if got := names(found); err != nil || len(got) != 1 || got[0] != "Ana" {
		t.Fatalf("WELDING>=3 and PLC>=2 = %v, err %v", got, err)
	}
	found, err = s.SearchWorkers(ctx, []domain.Requirement{{Skill: "UNKNOWN", MinRating: 1}}, nil, asOf)
	if err != nil || len(found.Items) != 0 {
		t.Fatalf("unknown skill = %+v, err %v", found, err)
	}
	found, err = s.SearchWorkers(ctx, []domain.Requirement{{Skill: "WELDING", MinRating: 3}}, nil, day(time.February, 1))
	if got := names(found); err != nil || len(got) != 3 {
		t.Fatalf("on 1 February Gone is still employed: %v, err %v", got, err)
	}
}
