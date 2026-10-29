package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/hr/internal/domain"
	hrevents "github.com/BenjaminSRussell/passage/services/hr/internal/events"
)

func mustCourse(t *testing.T, s *Postgres, ctx context.Context, c Course) Course {
	t.Helper()
	out, _, err := s.CreateCourse(ctx, c, "")
	if err != nil {
		t.Fatalf("create course %s: %v", c.Code, err)
	}
	return out
}

// mustSession schedules a classroom session of a course.
func mustSession(t *testing.T, s *Postgres, ctx context.Context, courseID string, start time.Time, capacity, waitlist int) Session {
	t.Helper()
	out, _, err := s.CreateSession(ctx, Session{CourseID: courseID, StartsAt: start, EndsAt: start.Add(8 * time.Hour),
		Location: strp("Yard B"), Capacity: capacity, WaitlistCapacity: waitlist}, "")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return out
}

func mustRegister(t *testing.T, s *Postgres, ctx context.Context, sessionID, workerID string) Registration {
	t.Helper()
	reg, _, _, err := s.RegisterWorker(ctx, sessionID, workerID, false)
	if err != nil {
		t.Fatalf("register %s: %v", workerID, err)
	}
	return reg
}

func registrationOf(t *testing.T, s *Postgres, ctx context.Context, sessionID, workerID string) Registration {
	t.Helper()
	regs, err := s.ListRegistrations(ctx, sessionID)
	if err != nil {
		t.Fatalf("list registrations: %v", err)
	}
	for _, r := range regs {
		if r.WorkerID == workerID {
			return r
		}
	}
	t.Fatalf("no registration for worker %s", workerID)
	return Registration{}
}

func typesOf(rows []outboxRow) map[string]int {
	out := map[string]int{}
	for _, r := range rows {
		out[r.Type]++
	}
	return out
}

func forkliftCourse(code string) Course {
	return Course{
		Code: code, Name: "Forklift operation",
		SkillOutcomes:     []CourseSkillOutcome{{SkillCode: "FORKLIFT", Rating: 3}},
		CertificationCode: strp("FORKLIFT"), CertificationName: strp("Forklift licence"), ValidityMonths: 24,
	}
}

// TestCourseWaitlistAndCompletionAC429 is AC-42.9 through the database: a
// session with capacity 10 and waitlist 2 takes 13 registrations as 10
// CONFIRMED, 2 WAITLISTED and HCM-CRS-001; a cancellation promotes the oldest
// waitlisted; a PASS on a course mapping FORKLIFT rating 3 and a 24-month
// certificate rates the worker 3 and issues a certificate expiring 24 months
// after the session end, with course.completed, workerskill.changed and
// certification.issued in the outbox. A replayed completion changes nothing.
func TestCourseWaitlistAndCompletionAC429(t *testing.T) {
	s, ctx := newStore(t)
	at(s, day(time.May, 1))
	mustSkill(t, s, ctx, "FORKLIFT", 5)
	course := mustCourse(t, s, ctx, forkliftCourse("FORK-101"))
	if len(course.SkillOutcomes) != 1 || course.SkillOutcomes[0].SkillCode != "FORKLIFT" || course.SkillOutcomes[0].Rating != 3 {
		t.Fatalf("course outcomes = %+v", course.SkillOutcomes)
	}
	start := day(time.June, 1).Add(9 * time.Hour)
	sess := mustSession(t, s, ctx, course.ID, start, 10, 2)

	workers := make([]Worker, 13)
	for i := range workers {
		workers[i] = mustWorker(t, s, ctx, fmt.Sprintf("CRS%02d", i+1))
	}
	for i := 0; i < 12; i++ {
		reg, warnings, inserted, err := s.RegisterWorker(ctx, sess.ID, workers[i].ID, false)
		if err != nil || !inserted || len(warnings) != 0 {
			t.Fatalf("register %d: inserted=%v warnings=%v err=%v", i, inserted, warnings, err)
		}
		want := domain.RegConfirmed
		if i >= 10 {
			want = domain.RegWaitlisted
		}
		if reg.Status != want || reg.PersonnelNumber != workers[i].PersonnelNumber {
			t.Fatalf("registration %d = %+v, want %s", i, reg, want)
		}
	}
	_, _, _, err := s.RegisterWorker(ctx, sess.ID, workers[12].ID, false)
	requireCode(t, err, domain.CodeCourseFull)

	again, _, inserted, err := s.RegisterWorker(ctx, sess.ID, workers[0].ID, false)
	if err != nil || inserted || again.Status != domain.RegConfirmed {
		t.Fatalf("registering again: %+v inserted=%v err=%v", again, inserted, err)
	}

	first := registrationOf(t, s, ctx, sess.ID, workers[0].ID)
	canceled, changed, err := s.CancelRegistration(ctx, first.ID, false)
	if err != nil || !changed || canceled.Status != domain.RegCanceled {
		t.Fatalf("cancel: %+v changed=%v err=%v", canceled, changed, err)
	}
	if got := registrationOf(t, s, ctx, sess.ID, workers[10].ID); got.Status != domain.RegConfirmed {
		t.Fatalf("oldest waitlisted is %s after a cancel, want CONFIRMED", got.Status)
	}
	if got := registrationOf(t, s, ctx, sess.ID, workers[11].ID); got.Status != domain.RegWaitlisted {
		t.Fatalf("second waitlisted is %s, want WAITLISTED", got.Status)
	}
	if _, changed, err := s.CancelRegistration(ctx, first.ID, false); err != nil || changed {
		t.Fatalf("second cancel: changed=%v err=%v, want a no-op", changed, err)
	}
	seats, err := s.GetSession(ctx, sess.ID)
	if err != nil || seats.Confirmed != 10 || seats.Waitlisted != 1 || seats.SeatsLeft != 0 || seats.CourseCode != "FORK-101" {
		t.Fatalf("session = %+v, err %v", seats, err)
	}
	// The freed waitlist place takes the 13th worker now.
	if reg := mustRegister(t, s, ctx, sess.ID, workers[12].ID); reg.Status != domain.RegWaitlisted {
		t.Fatalf("13th worker after the cancel = %s, want WAITLISTED", reg.Status)
	}

	// Attendance cannot be recorded before the session starts.
	pass := []Outcome{{WorkerID: workers[10].ID, Status: domain.RegAttended, Result: domain.ResultPass, Score: "88.00"}, {WorkerID: workers[1].ID, Status: domain.RegNoShow}}
	_, err = s.CompleteSession(ctx, sess.ID, pass)
	requireCode(t, err, domain.CodeCourseState)

	at(s, day(time.June, 2))
	res, err := s.CompleteSession(ctx, sess.ID, pass)
	if err != nil || res.Changed != 2 || res.Passed != 1 || len(res.Items) != 2 {
		t.Fatalf("complete = %+v, err %v", res, err)
	}
	if r := res.Items[0]; r.Status != domain.RegAttended || deref(r.Result) != "PASS" || deref(r.Score) != "88.00" || deref(r.CompletedOn) != "2026-06-01" {
		t.Fatalf("passed registration = %+v", r)
	}
	skills, err := s.ListWorkerSkills(ctx, workers[10].ID)
	if err != nil || len(skills) != 1 || skills[0].SkillCode != "FORKLIFT" || skills[0].Rating != 3 || skills[0].AssessedOn != "2026-06-01" {
		t.Fatalf("skills after PASS = %+v, err %v; want FORKLIFT rated 3 on 2026-06-01", skills, err)
	}
	certs, err := s.ListCertifications(ctx, workers[10].ID, day(time.June, 2))
	if err != nil || len(certs) != 1 || certs[0].Code != "FORKLIFT" || certs[0].IssuedOn != "2026-06-01" || deref(certs[0].ExpiresOn) != "2028-06-01" || !certs[0].Valid {
		t.Fatalf("certificates after PASS = %+v, err %v; want one valid FORKLIFT expiring 2028-06-01", certs, err)
	}
	if skills, _ := s.ListWorkerSkills(ctx, workers[1].ID); len(skills) != 0 {
		t.Fatalf("a no-show was rated: %+v", skills)
	}

	events := outboxFor(t, s, ctx, registrationOf(t, s, ctx, sess.ID, workers[10].ID).ID)
	if len(events) != 1 || events[0].Type != hrevents.CourseCompleted {
		t.Fatalf("registration events = %+v, want one course.completed", events)
	}
	data := events[0].Data
	if data["worker_id"] != workers[10].ID || data["course_id"] != course.ID || data["session_id"] != sess.ID || data["certification_id"] != certs[0].ID {
		t.Fatalf("course.completed payload = %v", data)
	}
	if awards, ok := data["skills"].([]any); !ok || len(awards) != 1 || awards[0].(map[string]any)["skill_code"] != "FORKLIFT" || awards[0].(map[string]any)["rating"] != float64(3) {
		t.Fatalf("course.completed skills = %v", data["skills"])
	}
	if got := typesOf(outboxFor(t, s, ctx, workers[10].ID)); got[hrevents.WorkerSkillChanged] != 1 {
		t.Fatalf("worker events = %v, want one workerskill.changed", got)
	}
	if got := outboxFor(t, s, ctx, certs[0].ID); len(got) != 1 || got[0].Type != hrevents.CertificationIssued {
		t.Fatalf("certificate events = %+v", got)
	}

	// A retried completion is a no-op: no second certificate or event.
	res, err = s.CompleteSession(ctx, sess.ID, pass)
	if err != nil || res.Changed != 0 || res.Passed != 0 {
		t.Fatalf("replayed completion = %+v, err %v", res, err)
	}
	if certs, _ := s.ListCertifications(ctx, workers[10].ID, day(time.June, 2)); len(certs) != 1 {
		t.Fatalf("replay issued another certificate: %d", len(certs))
	}
	if got := outboxFor(t, s, ctx, registrationOf(t, s, ctx, sess.ID, workers[10].ID).ID); len(got) != 1 {
		t.Fatalf("replay published again: %+v", got)
	}

	// A recorded outcome is final; waitlisted, unregistered and canceled
	// workers cannot be completed; a completed registration cannot be canceled.
	_, err = s.CompleteSession(ctx, sess.ID, []Outcome{{WorkerID: workers[10].ID, Status: domain.RegNoShow}})
	requireCode(t, err, domain.CodeCourseState)
	_, err = s.CompleteSession(ctx, sess.ID, []Outcome{{WorkerID: workers[11].ID, Status: domain.RegAttended}})
	requireCode(t, err, domain.CodeCourseState)
	_, err = s.CompleteSession(ctx, sess.ID, []Outcome{{WorkerID: workers[0].ID, Status: domain.RegAttended}})
	requireCode(t, err, domain.CodeCourseState)
	_, err = s.CompleteSession(ctx, sess.ID, []Outcome{{WorkerID: ids.New(), Status: domain.RegAttended}})
	requireCode(t, err, CodeRegistrationNotFound)
	_, _, err = s.CancelRegistration(ctx, registrationOf(t, s, ctx, sess.ID, workers[10].ID).ID, false)
	requireCode(t, err, domain.CodeCourseState)

	// A failing line rolls the whole completion back.
	_, err = s.CompleteSession(ctx, sess.ID, []Outcome{{WorkerID: workers[2].ID, Status: domain.RegAttended}, {WorkerID: workers[11].ID, Status: domain.RegAttended}})
	requireCode(t, err, domain.CodeCourseState)
	if got := registrationOf(t, s, ctx, sess.ID, workers[2].ID); got.Status != domain.RegConfirmed {
		t.Fatalf("partial completion was kept: %s", got.Status)
	}

	// Registration closes when the session ends.
	at(s, day(time.June, 3))
	_, _, _, err = s.RegisterWorker(ctx, sess.ID, mustWorker(t, s, ctx, "Late").ID, false)
	requireCode(t, err, domain.CodeCourseState)
}

// TestCourseNeverLowersARatingAndResultsLater (BR-42.9): rating =
// max(existing, course rating) and a higher existing rating is left alone
// with its date; attendance can be recorded first and the PASS later; a
// certificate the worker already holds for that code and day is reused.
func TestCourseNeverLowersARatingAndResultsLater(t *testing.T) {
	s, ctx := newStore(t)
	at(s, day(time.May, 1))
	mustSkill(t, s, ctx, "FORKLIFT", 5)
	mustSkill(t, s, ctx, "SAFETY", 5)
	course := mustCourse(t, s, ctx, Course{Code: "FORK-201", Name: "Forklift advanced",
		SkillOutcomes:     []CourseSkillOutcome{{SkillCode: "FORKLIFT", Rating: 3}, {SkillCode: "SAFETY", Rating: 2}},
		CertificationCode: strp("FORKLIFT"), CertificationName: strp("Forklift licence")})
	w := mustWorker(t, s, ctx, "Expert")
	mustRate(t, s, ctx, w.ID, "FORKLIFT", 4, day(time.April, 1))
	mustRate(t, s, ctx, w.ID, "SAFETY", 1, day(time.April, 1))
	sess := mustSession(t, s, ctx, course.ID, day(time.May, 10).Add(9*time.Hour), 5, 0)
	reg := mustRegister(t, s, ctx, sess.ID, w.ID)
	// The worker got the same certificate by hand on the session day.
	manual := mustCert(t, s, ctx, w.ID, "FORKLIFT", "2026-05-10", nil)
	before := len(outboxFor(t, s, ctx, w.ID))

	at(s, day(time.May, 11))
	if _, err := s.CompleteSession(ctx, sess.ID, []Outcome{{WorkerID: w.ID, Status: domain.RegAttended}}); err != nil {
		t.Fatalf("record attendance: %v", err)
	}
	if skills, _ := s.ListWorkerSkills(ctx, w.ID); skills[0].Rating != 4 || skills[1].Rating != 1 {
		t.Fatalf("attendance without a result changed skills: %+v", skills)
	}
	res, err := s.CompleteSession(ctx, sess.ID, []Outcome{{WorkerID: w.ID, Status: domain.RegAttended, Result: domain.ResultPass}})
	if err != nil || res.Passed != 1 {
		t.Fatalf("record the PASS later: %+v, err %v", res, err)
	}
	skills, err := s.ListWorkerSkills(ctx, w.ID)
	if err != nil || len(skills) != 2 {
		t.Fatalf("skills = %+v, err %v", skills, err)
	}
	if f := skills[0]; f.SkillCode != "FORKLIFT" || f.Rating != 4 || f.AssessedOn != "2026-04-01" {
		t.Errorf("FORKLIFT = %+v, want the higher 4 kept with its date", f)
	}
	if sf := skills[1]; sf.SkillCode != "SAFETY" || sf.Rating != 2 || sf.AssessedOn != "2026-05-10" {
		t.Errorf("SAFETY = %+v, want raised to 2 on the session day", sf)
	}
	if got := len(outboxFor(t, s, ctx, w.ID)) - before; got != 1 {
		t.Errorf("worker events from the PASS = %d, want one workerskill.changed (SAFETY)", got)
	}
	certs, _ := s.ListCertifications(ctx, w.ID, day(time.May, 11))
	if len(certs) != 1 || certs[0].ID != manual.ID {
		t.Fatalf("certificates = %+v, want the existing %s reused", certs, manual.ID)
	}
	events := outboxFor(t, s, ctx, reg.ID)
	if len(events) != 1 || events[0].Data["certification_id"] != manual.ID {
		t.Fatalf("course.completed = %+v", events)
	}
	if awards := events[0].Data["skills"].([]any); awards[0].(map[string]any)["rating"] != float64(4) {
		t.Errorf("course.completed FORKLIFT rating = %v, want the 4 the worker holds", awards[0])
	}
}

// TestApprovalCourseRegistration (FR-42.14, HCM-CRS-002): a course that needs
// manager approval registers as REQUESTED with the warning and holds no seat;
// approval seats it in order; an approver cannot approve their own
// registration; approving twice is HCM-CRS-STATE; declining is a cancel.
func TestApprovalCourseRegistration(t *testing.T) {
	s, ctx := newStore(t)
	at(s, day(time.June, 1))
	course := mustCourse(t, s, ctx, Course{Code: "SAFE-1", Name: "Safety induction", RequiresManagerApproval: true})
	start := day(time.July, 1).Add(9 * time.Hour)
	sess, _, err := s.CreateSession(ctx, Session{CourseID: course.ID, StartsAt: start, EndsAt: start.Add(time.Hour),
		VirtualURL: strp("https://learn.example/s1"), Capacity: 1, WaitlistCapacity: 0}, "")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	a := mustWorker(t, s, ctx, "APR-A")
	b := mustWorker(t, s, ctx, "APR-B")

	reg, warnings, _, err := s.RegisterWorker(ctx, sess.ID, a.ID, false)
	if err != nil || reg.Status != domain.RegRequested || len(warnings) != 1 || warnings[0].Code != domain.CodeCourseApproval {
		t.Fatalf("approval course registration = %+v warnings %v err %v; want REQUESTED with HCM-CRS-002", reg, warnings, err)
	}
	regB := mustRegister(t, s, ctx, sess.ID, b.ID)
	if got, _ := s.GetSession(ctx, sess.ID); got.Requested != 2 || got.Confirmed != 0 || got.SeatsLeft != 1 {
		t.Fatalf("requested registrations hold seats: %+v", got)
	}
	approved, err := s.ApproveRegistration(ctx, reg.ID)
	if err != nil || approved.Status != domain.RegConfirmed {
		t.Fatalf("approve = %+v, err %v; want CONFIRMED", approved, err)
	}
	_, err = s.ApproveRegistration(ctx, reg.ID)
	requireCode(t, err, domain.CodeCourseState)
	_, err = s.ApproveRegistration(ctx, regB.ID)
	requireCode(t, err, domain.CodeCourseFull)
	if declined, _, err := s.CancelRegistration(ctx, regB.ID, false); err != nil || declined.Status != domain.RegCanceled {
		t.Fatalf("decline = %+v, err %v", declined, err)
	}
	_, err = s.ApproveRegistration(ctx, ids.New())
	requireCode(t, err, CodeRegistrationNotFound)

	// The approver's own registration.
	me := ids.New()
	mine := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), UserID: me, Duties: map[string]struct{}{}})
	self, _, err := s.CreateWorker(mine, WorkerInput{Worker: Worker{PersonnelNumber: "WRK-APPROVER", WorkerType: "EMPLOYEE",
		PartyPersonID: ids.New(), LegalName: "Approver", UserID: &me}, NationalID: "123-45-6789"}, "")
	if err != nil {
		t.Fatal(err)
	}
	other := mustSession(t, s, ctx, course.ID, start.AddDate(0, 0, 7), 5, 0)
	own := mustRegister(t, s, mine, other.ID, self.ID)
	_, err = s.ApproveRegistration(mine, own.ID)
	requireCode(t, err, domain.CodeCourseSelfApproval)
}

// TestOwnOnlyCallersActOnTheirOwnRegistration: an employee-only caller may
// register themselves and cancel their own registration, not another
// worker's (HCM-CRS-NOT-OWN).
func TestOwnOnlyCallersActOnTheirOwnRegistration(t *testing.T) {
	s, ctx := newStore(t)
	at(s, day(time.June, 1))
	me := ids.New()
	mine := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), UserID: me, Duties: map[string]struct{}{}})
	self, _, err := s.CreateWorker(ctx, WorkerInput{Worker: Worker{PersonnelNumber: "WRK-ME", WorkerType: "EMPLOYEE",
		PartyPersonID: ids.New(), LegalName: "Me", UserID: &me}, NationalID: "123-45-6789"}, "")
	if err != nil {
		t.Fatal(err)
	}
	other := mustWorker(t, s, ctx, "Colleague")
	course := mustCourse(t, s, ctx, Course{Code: "INTRO", Name: "Introduction"})
	sess := mustSession(t, s, ctx, course.ID, day(time.July, 1), 5, 1)

	_, _, _, err = s.RegisterWorker(mine, sess.ID, other.ID, true)
	requireCode(t, err, CodeNotOwnCourse)
	reg, _, inserted, err := s.RegisterWorker(mine, sess.ID, self.ID, true)
	if err != nil || !inserted || reg.Status != domain.RegConfirmed {
		t.Fatalf("own registration = %+v inserted %v err %v", reg, inserted, err)
	}
	colleague := mustRegister(t, s, ctx, sess.ID, other.ID)
	_, _, err = s.CancelRegistration(mine, colleague.ID, true)
	requireCode(t, err, CodeNotOwnCourse)
	if out, changed, err := s.CancelRegistration(mine, reg.ID, true); err != nil || !changed || out.Status != domain.RegCanceled {
		t.Fatalf("own cancel = %+v changed %v err %v", out, changed, err)
	}
	// Registering again after a cancel queues anew on the same row.
	again, _, inserted, err := s.RegisterWorker(mine, sess.ID, self.ID, true)
	if err != nil || !inserted || again.ID != reg.ID || again.Status != domain.RegConfirmed || !again.QueuedAt.After(reg.QueuedAt) {
		t.Fatalf("re-registration = %+v inserted %v err %v", again, inserted, err)
	}
}

// TestCourseCatalogRulesAndReplay: a replayed key returns the stored course
// or session, a different body under the key is refused, a duplicate code
// is HCM-CRS-DUPLICATE, outcomes must name catalog skills on their scale,
// the catalog pages by code and sessions need an existing course.
func TestCourseCatalogRulesAndReplay(t *testing.T) {
	s, ctx := newStore(t)
	at(s, day(time.May, 1))
	mustSkill(t, s, ctx, "FORKLIFT", 3)
	course, inserted, err := s.CreateCourse(ctx, Course{Code: "FORK-1", Name: "Forklift", SkillOutcomes: []CourseSkillOutcome{{SkillCode: "FORKLIFT", Rating: 2}}}, "crs-key")
	if err != nil || !inserted {
		t.Fatalf("create: inserted=%v err=%v", inserted, err)
	}
	replay, inserted, err := s.CreateCourse(ctx, Course{Code: "FORK-1", Name: "Forklift", SkillOutcomes: []CourseSkillOutcome{{SkillCode: "FORKLIFT", Rating: 2}}}, "crs-key")
	if err != nil || inserted || replay.ID != course.ID {
		t.Fatalf("replay: %+v inserted=%v err=%v", replay, inserted, err)
	}
	_, _, err = s.CreateCourse(ctx, Course{Code: "FORK-1", Name: "Forklift", SkillOutcomes: []CourseSkillOutcome{{SkillCode: "FORKLIFT", Rating: 3}}}, "crs-key")
	requireCode(t, err, CodeIdempotencyReuse)
	_, _, err = s.CreateCourse(ctx, Course{Code: "FORK-1", Name: "Again"}, "")
	requireCode(t, err, CodeCourseDuplicate)
	_, _, err = s.CreateCourse(ctx, Course{Code: "FORK-2", Name: "x", SkillOutcomes: []CourseSkillOutcome{{SkillCode: "WELDING", Rating: 2}}}, "")
	requireCode(t, err, CodeSkillNotFound)
	_, _, err = s.CreateCourse(ctx, Course{Code: "FORK-3", Name: "x", SkillOutcomes: []CourseSkillOutcome{{SkillCode: "FORKLIFT", Rating: 4}}}, "")
	requireCode(t, err, domain.CodeSkillRating)

	mustCourse(t, s, ctx, Course{Code: "A-1", Name: "A"})
	mustCourse(t, s, ctx, Course{Code: "Z-1", Name: "Z"})
	page, err := s.ListCourses(ctx, "", 2)
	if err != nil || len(page.Items) != 2 || page.Items[0].Code != "A-1" || page.Items[1].Code != "FORK-1" || page.NextAfter != "FORK-1" ||
		len(page.Items[1].SkillOutcomes) != 1 {
		t.Fatalf("first page = %+v, err %v", page, err)
	}
	rest, err := s.ListCourses(ctx, page.NextAfter, 2)
	if err != nil || len(rest.Items) != 1 || rest.Items[0].Code != "Z-1" || rest.NextAfter != "" {
		t.Fatalf("second page = %+v, err %v", rest, err)
	}

	start := day(time.June, 1).Add(9 * time.Hour)
	in := Session{CourseID: course.ID, StartsAt: start, EndsAt: start.Add(time.Hour), Location: strp("Room 1"), Capacity: 4, WaitlistCapacity: 2,
		CostPerAttendee: strp("250.5"), Currency: strp("USD")}
	sess, inserted, err := s.CreateSession(ctx, in, "ses-key")
	if err != nil || !inserted || deref(sess.CostPerAttendee) != "250.50" || deref(sess.Currency) != "USD" {
		t.Fatalf("create session = %+v inserted=%v err=%v", sess, inserted, err)
	}
	again, inserted, err := s.CreateSession(ctx, in, "ses-key")
	if err != nil || inserted || again.ID != sess.ID {
		t.Fatalf("session replay = %+v inserted=%v err=%v", again, inserted, err)
	}
	in.Capacity = 5
	_, _, err = s.CreateSession(ctx, in, "ses-key")
	requireCode(t, err, CodeIdempotencyReuse)
	_, _, err = s.CreateSession(ctx, Session{CourseID: ids.New(), StartsAt: start, EndsAt: start.Add(time.Hour), Location: strp("x"), Capacity: 1}, "")
	requireCode(t, err, CodeCourseNotFound)
	instructor := ids.New()
	_, _, err = s.CreateSession(ctx, Session{CourseID: course.ID, StartsAt: start, EndsAt: start.Add(time.Hour), Location: strp("x"), Capacity: 1,
		InstructorWorkerID: &instructor}, "")
	requireCode(t, err, CodeWorkerNotFound)
	sessions, err := s.ListSessions(ctx, course.ID)
	if err != nil || len(sessions) != 1 || sessions[0].ID != sess.ID {
		t.Fatalf("sessions = %+v, err %v", sessions, err)
	}
	_, err = s.ListSessions(ctx, ids.New())
	requireCode(t, err, CodeCourseNotFound)
	_, err = s.GetSession(ctx, ids.New())
	requireCode(t, err, CodeSessionNotFound)
	_, _, _, err = s.RegisterWorker(ctx, ids.New(), mustWorker(t, s, ctx, "Nobody").ID, false)
	requireCode(t, err, CodeSessionNotFound)
	_, _, _, err = s.RegisterWorker(ctx, sess.ID, ids.New(), false)
	requireCode(t, err, CodeWorkerNotFound)
}

// TestConcurrentRegistrationsNeverOverfill: 16 workers registering at once
// into capacity 10 and waitlist 2 get exactly 10 CONFIRMED, 2 WAITLISTED and
// 4 HCM-CRS-001, because seats are counted under the session row lock.
func TestConcurrentRegistrationsNeverOverfill(t *testing.T) {
	s, ctx := newStore(t)
	at(s, day(time.May, 1))
	course := mustCourse(t, s, ctx, Course{Code: "RUSH", Name: "Rush"})
	sess := mustSession(t, s, ctx, course.ID, day(time.June, 1), 10, 2)
	workers := make([]Worker, 16)
	for i := range workers {
		workers[i] = mustWorker(t, s, ctx, fmt.Sprintf("RUSH%02d", i))
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	got := map[string]int{}
	for _, w := range workers {
		wg.Add(1)
		go func(workerID string) {
			defer wg.Done()
			reg, _, _, err := s.RegisterWorker(ctx, sess.ID, workerID, false)
			key := reg.Status
			var p *httpx.Problem
			if errors.As(err, &p) {
				key = p.Code
			} else if err != nil {
				key = err.Error()
			}
			mu.Lock()
			got[key]++
			mu.Unlock()
		}(w.ID)
	}
	wg.Wait()
	if got[domain.RegConfirmed] != 10 || got[domain.RegWaitlisted] != 2 || got[domain.CodeCourseFull] != 4 || len(got) != 3 {
		t.Fatalf("concurrent registrations = %v, want 10 CONFIRMED, 2 WAITLISTED, 4 HCM-CRS-001", got)
	}
}
