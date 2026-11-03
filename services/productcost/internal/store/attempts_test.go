package store

import (
	"context"
	"net/http"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/domain"
	costevents "github.com/BenjaminSRussell/passage/services/productcost/internal/events"
)

// expiredAttempt leaves a pending ambiguous attempt past the ledger replay
// window (400 day retention less the 30 day margin).
func (f activationFixture) expiredAttempt(t *testing.T, ledger *ledgerclient.Fake) {
	t.Helper()
	f.leaveAmbiguousAttempt(t, ledger)
	f.ageAttempts(t, 371)
}

func TestAttempt_ListFlagsTheOnesPastTheReplayWindow(t *testing.T) {
	f := newActivationFixture(t, true)
	ledger := ledgerclient.NewFake()
	ledger.FailPosts(&ledgerclient.Error{Op: "post", Status: http.StatusBadGateway})
	if _, _, err := f.s.ActivateVersion(as(1, ""), f.activate(ledger, "k-act")); err == nil {
		t.Fatal("the post should have failed")
	}
	got, err := f.s.ListActivationAttempts(as(1, ""))
	if err != nil || len(got) != 1 || got[0].NeedsOperator || !got[0].Ambiguous || got[0].VersionID != f.newVer.ID || got[0].Total.String() != "15" {
		t.Fatalf("fresh attempt: %+v %v", got, err)
	}
	f.ageAttempts(t, 371)
	if got, err = f.s.ListActivationAttempts(as(1, "")); err != nil || len(got) != 1 || !got[0].NeedsOperator {
		t.Fatalf("stale attempt: %+v %v", got, err)
	}
	if got, err = f.s.ListActivationAttempts(as(2, "")); err != nil || len(got) != 0 {
		t.Fatalf("another tenant sees %+v %v", got, err)
	}
	if !f.rls {
		return
	}
	if got, err = f.s.ListActivationAttempts(as(1, f.le)); err != nil || len(got) != 1 {
		t.Fatalf("own legal entity: %+v %v", got, err)
	}
	if got, err = f.s.ListActivationAttempts(as(1, ids.New())); err != nil || len(got) != 0 {
		t.Fatalf("another legal entity sees %+v %v", got, err)
	}
	// Nor can an operator scoped to another legal entity resolve it.
	if _, _, err := f.s.ResolveActivationAttempt(as(1, ids.New()), resolveIn(f, OutcomeNotPosted, "", "r-scope")); err == nil {
		t.Fatal("an operator outside the legal entity resolved the attempt")
	}
	if n, _ := pendingAttempts(t, f.admin); n != 1 {
		t.Fatalf("attempts %d after an out-of-scope resolve", n)
	}
}

func TestAttempt_ResolvePostedForAReversalRecordsTheReversal(t *testing.T) {
	f := newActivationFixture(t, true)
	ledger := ledgerclient.NewFake()
	done, _, err := f.s.ActivateVersion(as(1, ""), f.activate(ledger, "k-act"))
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	rev := f.activate(failingReverse{ledger}, "k-rev")
	rev.Expected, rev.ReversesID = done.Version.Version, done.Activation.ID
	if _, _, err := f.s.ActivateVersion(as(1, ""), rev); err == nil {
		t.Fatal("the reversal should have failed")
	}
	f.ageAttempts(t, 371)
	in := resolveIn(f, OutcomePosted, ids.New(), "r-rev")
	in.Expected, in.ReversesID = done.Version.Version, done.Activation.ID
	res, _, err := f.s.ResolveActivationAttempt(as(1, ""), in)
	if err != nil || res.Activation == nil || res.Activation.ReversesActivationID == nil || *res.Activation.ReversesActivationID != done.Activation.ID ||
		res.Activation.JournalEntryID != in.JournalEntryID || res.Version.Status != domain.VersionActive {
		t.Fatalf("resolve reversal: %+v %v", res, err)
	}
	if n, _ := pendingAttempts(t, f.admin); n != 0 {
		t.Fatalf("attempts left %d", n)
	}
}

func resolveIn(f activationFixture, outcome, entry, key string) ResolveInput {
	return ResolveInput{VersionID: f.newVer.ID, Expected: 1, Outcome: outcome, JournalEntryID: entry, Reason: "checked the ledger", Key: key, Fingerprint: outcome + key}
}

func TestAttempt_ResolveNotPostedDiscardsItAndTheVersionActivatesAfresh(t *testing.T) {
	f := newActivationFixture(t, true)
	ledger := ledgerclient.NewFake()
	f.expiredAttempt(t, ledger)
	res, replayed, err := f.s.ResolveActivationAttempt(as(1, ""), resolveIn(f, OutcomeNotPosted, "", "r-1"))
	if err != nil || replayed || res.Outcome != OutcomeNotPosted || res.Activation != nil {
		t.Fatalf("resolve: %+v replayed=%v %v", res, replayed, err)
	}
	if n, _ := pendingAttempts(t, f.admin); n != 0 {
		t.Fatalf("attempts left %d", n)
	}
	if again, replayed, err := f.s.ResolveActivationAttempt(as(1, ""), resolveIn(f, OutcomeNotPosted, "", "r-1")); err != nil || !replayed || again.Outcome != OutcomeNotPosted {
		t.Fatalf("replay: %+v %v %v", again, replayed, err)
	}
	if _, _, err := f.s.ResolveActivationAttempt(as(1, ""), resolveIn(f, OutcomeNotPosted, "", "r-2")); codeOf(err) != domain.CodeAttemptNotFound {
		t.Fatalf("nothing left to resolve: %v", err)
	}
	if types := outboxTypes(t, f.admin, activationIDOf(t, f)); !contains(types, costevents.ActivationAttemptResolved) {
		t.Fatalf("decision not announced: %v", types)
	}
	done, _, err := f.s.ActivateVersion(as(1, ""), f.activate(ledger, "k-act-2"))
	if err != nil || done.Version.Status != domain.VersionActive || len(ledger.Entries()) != 1 {
		t.Fatalf("afresh: %+v %v entries=%d", done, err, len(ledger.Entries()))
	}
}

func TestAttempt_ResolvePostedRecordsTheActivationAroundTheNamedEntryWithoutPosting(t *testing.T) {
	f := newActivationFixture(t, true)
	ledger := ledgerclient.NewFake()
	f.expiredAttempt(t, ledger)
	entry := ids.New()
	res, _, err := f.s.ResolveActivationAttempt(as(1, ""), resolveIn(f, OutcomePosted, entry, "r-1"))
	if err != nil || res.Activation == nil || res.Activation.JournalEntryID != entry || res.Version.Status != domain.VersionActive {
		t.Fatalf("resolve: %+v %v", res, err)
	}
	if len(ledger.Entries()) != 0 {
		t.Fatalf("resolving as posted must not post: %d entries", len(ledger.Entries()))
	}
	if n, _ := pendingAttempts(t, f.admin); n != 0 || activationRows(t, f.admin, f.newVer.ID) != 1 {
		t.Fatalf("attempts %d, activations %d", n, activationRows(t, f.admin, f.newVer.ID))
	}
	if types := outboxTypes(t, f.admin, res.Activation.ID); !contains(types, costevents.ActivationAttemptResolved) || !contains(types, costevents.StandardCostActivated) {
		t.Fatalf("events: %v", types)
	}
}

func TestAttempt_ResolveRefusesAnAttemptInsideTheWindowAndAStaleVersion(t *testing.T) {
	f := newActivationFixture(t, true)
	ledger := ledgerclient.NewFake()
	ledger.FailPosts(&ledgerclient.Error{Op: "post", Status: http.StatusBadGateway})
	if _, _, err := f.s.ActivateVersion(as(1, ""), f.activate(ledger, "k-act")); err == nil {
		t.Fatal("the post should have failed")
	}
	if _, _, err := f.s.ResolveActivationAttempt(as(1, ""), resolveIn(f, OutcomeNotPosted, "", "r-1")); codeOf(err) != domain.CodeAttemptNotExpired || statusOf(err) != http.StatusConflict {
		t.Fatalf("fresh attempt: %v", err)
	}
	f.ageAttempts(t, 371)
	in := resolveIn(f, OutcomeNotPosted, "", "r-2")
	in.Expected = 7
	if _, _, err := f.s.ResolveActivationAttempt(as(1, ""), in); codeOf(err) != domain.CodeVersionConflict {
		t.Fatalf("wrong version: %v", err)
	}
	if n, _ := pendingAttempts(t, f.admin); n != 1 {
		t.Fatalf("refused resolutions left %d attempts", n)
	}
}

// failingReverse loses every reversal answer, leaving the reversal attempt pending.
type failingReverse struct{ ledgerclient.Ledger }

func (failingReverse) Reverse(context.Context, ledgerclient.Reversal) (string, error) {
	return "", &ledgerclient.Error{Op: "reverse", Status: http.StatusBadGateway}
}

func activationIDOf(t *testing.T, f activationFixture) string {
	t.Helper()
	return activationID(testkit.TenantID(1), f.newVer, ActivationInput{Expected: 1})
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// The roller of the calculations cannot confirm their activation as posted either
// (the four-eyes rule of an activation); another user can.
func TestAttempt_ResolvePostedKeepsTheFourEyesRule(t *testing.T) {
	f := newActivationFixture(t, true)
	ledger := ledgerclient.NewFake()
	f.expiredAttempt(t, ledger)
	if _, err := f.admin.Exec(context.Background(), `update cost_calculation set rolled_by = 'cost.accountant' where costing_version_id = $1 and status = 'Pending'`, f.newVer.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.s.ResolveActivationAttempt(as(1, ""), resolveIn(f, OutcomePosted, ids.New(), "r-roller")); codeOf(err) != domain.CodeRollerActivates {
		t.Fatalf("the roller resolved the activation as posted: %v", err)
	}
	if n, _ := pendingAttempts(t, f.admin); n != 1 || activationRows(t, f.admin, f.newVer.ID) != 0 {
		t.Fatalf("a refused resolve changed state: attempts %d", n)
	}
	other := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), UserID: "controller", Duties: testkit.ScopeDuties("")})
	if res, _, err := f.s.ResolveActivationAttempt(other, resolveIn(f, OutcomePosted, ids.New(), "r-other")); err != nil || res.Activation == nil {
		t.Fatalf("another user: %+v %v", res, err)
	}
}
