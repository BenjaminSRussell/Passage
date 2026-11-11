package store_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
	"github.com/BenjaminSRussell/passage/services/party/internal/store"
)

type supplierRig struct {
	s              *store.Postgres
	app            *pgxpool.Pool
	buyer, approve context.Context
	vendorA        string
	vendorB        string
}

func newSupplierRig(t *testing.T) *supplierRig {
	t.Helper()
	app := migratedApp(t)
	s := store.New(app).WithKeys(testKeys(t))
	r := &supplierRig{s: s, app: app, buyer: asUser(1, legalEntityA, "buyer"), approve: asUser(1, legalEntityA, "approver")}
	group := ids.New()
	for i, dst := range []*string{&r.vendorA, &r.vendorB} {
		party, err := insertParty(principalContext(1, ""), app, testkit.TenantID(1), []string{"P-A", "P-B"}[i])
		if err != nil {
			t.Fatal(err)
		}
		v, err := s.CreateVendor(r.buyer, party, domain.NewVendor{AccountNumber: []string{"V-A", "V-B"}[i], VendorGroupID: group, CurrencyCode: "USD"})
		if err != nil {
			t.Fatal(err)
		}
		*dst = v.ID
	}
	return r
}

func pdf(n int) domain.Document {
	d, errs := domain.CheckDocument("Spec", "spec.pdf", "application/pdf", make([]byte, n))
	if len(errs) > 0 {
		panic(errs)
	}
	return d
}

func TestSupplier_OnboardingBlocksUntilApprovedAndTheInviterCannotApprove(t *testing.T) {
	r := newSupplierRig(t)
	q, err := r.s.CreateQuestionnaire(r.buyer, "Basics", []domain.Question{
		{ID: "iso", Label: "ISO 9001 certified?", Type: domain.QuestionYesNo, Required: true},
		{ID: "notes", Label: "Anything else", Type: domain.QuestionText},
	})
	if err != nil || q.Version != 1 {
		t.Fatalf("questionnaire %+v %v", q, err)
	}
	if again, _ := r.s.CreateQuestionnaire(r.buyer, "Basics", q.Questions); again.Version != 2 {
		t.Errorf("a second questionnaire of the same name is version %d, want 2", again.Version)
	}
	req, err := r.s.InviteOnboarding(r.buyer, r.vendorA, q.ID)
	if err != nil || req.Status != domain.RequestInvited {
		t.Fatalf("invite %+v %v", req, err)
	}
	if v, _ := r.s.GetVendor(r.buyer, r.vendorA); v.Blocked != "all" {
		t.Fatalf("vendor is %q while onboarding, want blocked all", v.Blocked)
	}
	if _, err := r.s.InviteOnboarding(r.buyer, r.vendorA, q.ID); problemCode(t, err) != store.CodeSupplierState {
		t.Errorf("a second onboarding at once: %v", err)
	}

	// Another vendor can neither read nor answer it.
	if _, err := r.s.GetRequest(r.buyer, r.vendorB, req.ID); problemCode(t, err) != store.CodeSupplierNotFound {
		t.Errorf("other vendor read: %v", err)
	}
	if _, err := r.s.SubmitRequest(r.buyer, r.vendorB, req.ID, map[string]string{"iso": "YES"}); problemCode(t, err) != store.CodeSupplierNotFound {
		t.Errorf("other vendor submit: %v", err)
	}
	// Answers are checked against the questionnaire.
	if _, err := r.s.SubmitRequest(r.buyer, r.vendorA, req.ID, map[string]string{"notes": "x"}); problemCode(t, err) != store.CodeSupplierInvalid {
		t.Errorf("a missing required answer: %v", err)
	}
	if _, err := r.s.SubmitRequest(r.buyer, r.vendorA, req.ID, map[string]string{"iso": "MAYBE"}); problemCode(t, err) != store.CodeSupplierInvalid {
		t.Errorf("a bad answer: %v", err)
	}
	if _, err := r.s.DecideRequest(r.approve, req.ID, true, ""); problemCode(t, err) != store.CodeSupplierState {
		t.Errorf("deciding an unsubmitted request: %v", err)
	}
	sub, err := r.s.SubmitRequest(r.buyer, r.vendorA, req.ID, map[string]string{"iso": "YES"})
	if err != nil || sub.Status != domain.RequestSubmitted || sub.Answers["iso"] != "YES" {
		t.Fatalf("submit %+v %v", sub, err)
	}
	// The user who invited the vendor cannot approve.
	wantProblem(t, errOf(r.s.DecideRequest(r.buyer, req.ID, true, "")), http.StatusForbidden, store.CodeSupplierSoD)
	done, err := r.s.DecideRequest(r.approve, req.ID, true, "welcome")
	if err != nil || done.Status != domain.RequestApproved {
		t.Fatalf("approve %+v %v", done, err)
	}
	if v, _ := r.s.GetVendor(r.buyer, r.vendorA); v.Blocked != "none" {
		t.Fatalf("approved vendor is %q, want unblocked in party", v.Blocked)
	}
	if _, err := r.s.DecideRequest(r.approve, req.ID, false, "x"); problemCode(t, err) != store.CodeSupplierState {
		t.Errorf("deciding twice: %v", err)
	}
	audit, _ := r.s.ListAudit(r.buyer, r.vendorA, 50)
	if len(audit) != 3 {
		t.Errorf("audit has %d entries (invited, submitted, approved), want 3: %+v", len(audit), audit)
	}
}

func errOf[T any](_ T, err error) error { return err }

func TestSupplier_ARejectedOnboardingKeepsTheVendorBlocked(t *testing.T) {
	r := newSupplierRig(t)
	q, _ := r.s.CreateQuestionnaire(r.buyer, "Basics", []domain.Question{{ID: "a", Label: "A?", Type: domain.QuestionText}})
	req, _ := r.s.InviteOnboarding(r.buyer, r.vendorA, q.ID)
	if _, err := r.s.SubmitRequest(r.buyer, r.vendorA, req.ID, map[string]string{}); err != nil {
		t.Fatal(err)
	}
	if d, err := r.s.DecideRequest(r.approve, req.ID, false, "no certificate"); err != nil || d.Status != domain.RequestRejected {
		t.Fatalf("reject %+v %v", d, err)
	}
	if v, _ := r.s.GetVendor(r.buyer, r.vendorA); v.Blocked != "all" {
		t.Errorf("rejected vendor is %q, want blocked", v.Blocked)
	}
}

func TestSupplier_VendorRequestsAreScoped(t *testing.T) {
	r := newSupplierRig(t)
	mine, err := r.s.CreateRequest(r.buyer, r.vendorA, domain.RequestProfileChange, "New address", "We moved.", map[string]string{"tradeName": "Acme West"})
	if err != nil || mine.Status != domain.RequestSubmitted {
		t.Fatalf("create %+v %v", mine, err)
	}
	if got, _ := r.s.ListRequests(r.buyer, r.vendorB, "", 50); len(got) != 0 {
		t.Errorf("vendor B lists %d of vendor A's requests", len(got))
	}
	if got, _ := r.s.ListRequests(r.buyer, "", "", 50); len(got) != 1 {
		t.Errorf("the buyer lists %d requests, want 1", len(got))
	}
	if _, err := r.s.WithdrawRequest(r.buyer, r.vendorB, mine.ID); problemCode(t, err) != store.CodeSupplierNotFound {
		t.Errorf("other vendor withdraws: %v", err)
	}
	if w, err := r.s.WithdrawRequest(r.buyer, r.vendorA, mine.ID); err != nil || w.Status != domain.RequestWithdrawn {
		t.Fatalf("withdraw %+v %v", w, err)
	}
	// Row security: a legal entity the buyer is not in sees nothing.
	if got, _ := r.s.ListRequests(asUser(1, legalEntityB, "x"), "", "", 50); len(got) != 0 {
		t.Errorf("another legal entity lists %d requests", len(got))
	}
}

func TestSupplier_DocumentsAreScopedToTheirVendorAndEveryReadIsAudited(t *testing.T) {
	r := newSupplierRig(t)
	up, err := r.s.AddDocument(r.buyer, r.vendorA, domain.DirectionFromVendor, pdf(100))
	if err != nil || up.SizeBytes != 100 || len(up.SHA256) != 64 {
		t.Fatalf("upload %+v %v", up, err)
	}
	if _, err := r.s.AddDocument(r.buyer, "not-a-uuid", domain.DirectionToVendor, pdf(10)); problemCode(t, err) != store.CodeSupplierNotFound {
		t.Errorf("upload for a bad vendor id: %v", err)
	}
	if _, err := r.s.AddDocument(r.buyer, ids.New(), domain.DirectionToVendor, pdf(10)); problemCode(t, err) != store.CodeSupplierNotFound {
		t.Errorf("upload for an unknown vendor: %v", err)
	}
	if got, _ := r.s.ListDocuments(r.buyer, r.vendorB, 50); len(got) != 0 {
		t.Errorf("vendor B lists vendor A's documents: %+v", got)
	}
	if _, err := r.s.GetDocument(r.buyer, r.vendorB, up.ID); problemCode(t, err) != store.CodeSupplierNotFound {
		t.Errorf("vendor B downloads vendor A's document: %v", err)
	}
	d, err := r.s.GetDocument(r.buyer, r.vendorA, up.ID)
	if err != nil || len(d.Content) != 100 {
		t.Fatalf("own download %v", err)
	}
	var downloads int
	audit, _ := r.s.ListAudit(r.buyer, r.vendorA, 50)
	for _, e := range audit {
		if e.Action == "document.downloaded" {
			downloads++
		}
	}
	if downloads != 1 {
		t.Errorf("audit holds %d downloads, want 1: %+v", downloads, audit)
	}
	if b, _ := r.s.ListAudit(r.buyer, r.vendorB, 50); len(b) != 0 {
		t.Errorf("vendor B's audit has vendor A's entries: %+v", b)
	}
}

func TestSupplier_ControlledDistributionNeedsAnAcknowledgementPerVendorAndRevision(t *testing.T) {
	r := newSupplierRig(t)
	rev1, err := r.s.PublishControlled(r.buyer, "QA-001", true, pdf(50))
	if err != nil || rev1.Revision != 1 {
		t.Fatalf("publish %+v %v", rev1, err)
	}
	rev2, _ := r.s.PublishControlled(r.buyer, "QA-001", true, pdf(60))
	if rev2.Revision != 2 {
		t.Fatalf("second revision is %d", rev2.Revision)
	}
	sent, err := r.s.Distribute(r.buyer, rev1.ID, []string{r.vendorA})
	if err != nil || len(sent) != 1 || sent[0].AcknowledgedAt != nil {
		t.Fatalf("distribute %+v %v", sent, err)
	}
	// Distributing again keeps the one record.
	if again, _ := r.s.Distribute(r.buyer, rev1.ID, []string{r.vendorA}); len(again) != 1 {
		t.Errorf("a repeat distribution made %d records", len(again))
	}
	if _, err := r.s.Distribute(r.buyer, rev1.ID, []string{ids.New()}); problemCode(t, err) == "" {
		t.Error("distributing to an unknown vendor was accepted")
	}

	// Vendor B holds nothing: no list, no content, no acknowledgement.
	if got, _ := r.s.ListDistributions(r.buyer, "", r.vendorB); len(got) != 0 {
		t.Errorf("vendor B lists %+v", got)
	}
	if _, err := r.s.GetDistributedContent(r.buyer, r.vendorB, rev1.ID); problemCode(t, err) != store.CodeSupplierNotFound {
		t.Errorf("vendor B reads an undistributed document: %v", err)
	}
	if _, err := r.s.Acknowledge(r.buyer, r.vendorB, rev1.ID); problemCode(t, err) != store.CodeSupplierNotFound {
		t.Errorf("vendor B acknowledges an undistributed document: %v", err)
	}
	// Revision 2 was not distributed to A, so A cannot read or acknowledge it.
	if _, err := r.s.GetDistributedContent(r.buyer, r.vendorA, rev2.ID); problemCode(t, err) != store.CodeSupplierNotFound {
		t.Errorf("A reads an undistributed revision: %v", err)
	}

	if _, err := r.s.GetDistributedContent(r.buyer, r.vendorA, rev1.ID); err != nil {
		t.Fatal(err)
	}
	ack, err := r.s.Acknowledge(asUser(1, legalEntityA, "vendor-user"), r.vendorA, rev1.ID)
	if err != nil || ack.AcknowledgedAt == nil || ack.AcknowledgedBy != "vendor-user" {
		t.Fatalf("acknowledge %+v %v", ack, err)
	}
	first := *ack.AcknowledgedAt
	if again, _ := r.s.Acknowledge(asUser(1, legalEntityA, "someone-else"), r.vendorA, rev1.ID); again.AcknowledgedBy != "vendor-user" || !again.AcknowledgedAt.Equal(first) {
		t.Errorf("a second acknowledgement replaced the first: %+v", again)
	}
	seen, _ := r.s.ListDistributions(r.buyer, rev1.ID, "")
	if len(seen) != 1 || seen[0].AcknowledgedAt == nil {
		t.Errorf("the buyer sees %+v, want one acknowledged distribution", seen)
	}
	var acks int
	audit, _ := r.s.ListAudit(r.buyer, r.vendorA, 50)
	for _, e := range audit {
		if e.Action == "distribution.acknowledged" {
			acks++
		}
	}
	if acks != 1 {
		t.Errorf("audit holds %d acknowledgements, want 1", acks)
	}
}

func TestSupplier_ApprovingAProfileChangeAppliesItToTheVendorsParty(t *testing.T) {
	r := newSupplierRig(t)
	v, _ := r.s.GetVendor(r.buyer, r.vendorA)
	before, _, err := r.s.GetParty(r.buyer, v.PartyID)
	if err != nil {
		t.Fatal(err)
	}
	req, err := r.s.CreateRequest(r.buyer, r.vendorA, domain.RequestProfileChange, "Rename", "We rebranded.", map[string]string{"legalName": "Acme Industrial Ltd", "tradeName": "Acme Industrial", "languageCode": "en"})
	if err != nil || req.Changes["legalName"] != "Acme Industrial Ltd" {
		t.Fatalf("create %+v %v", req, err)
	}
	// Nothing changes until a buyer approves.
	if p, _, _ := r.s.GetParty(r.buyer, v.PartyID); p.Version != before.Version || *p.LegalName != "Acme" {
		t.Fatalf("party changed before approval: %+v", p)
	}
	done, err := r.s.DecideRequest(r.approve, req.ID, true, "ok")
	if err != nil || done.Status != domain.RequestApproved || done.AppliedAt == nil {
		t.Fatalf("approve %+v %v", done, err)
	}
	after, _, _ := r.s.GetParty(r.buyer, v.PartyID)
	if *after.LegalName != "Acme Industrial Ltd" || *after.TradeName != "Acme Industrial" || *after.LanguageCode != "en" || after.Name != "Acme Industrial Ltd" || after.Version != before.Version+1 {
		t.Fatalf("party after approval: %+v", after)
	}
	audit, _ := r.s.ListAudit(r.buyer, r.vendorA, 50)
	var applied string
	for _, e := range audit {
		if e.Action == "profile.applied" {
			applied = e.Detail
		}
	}
	if applied != "languageCode,legalName,tradeName" {
		t.Errorf("audit detail %q, want the changed fields", applied)
	}
	if got := outboxTypes(t, r.app); !strings.Contains(got, "dev.passage.party.party.updated.v1") {
		t.Errorf("outbox %s has no party.updated.v1", got)
	}
	// Another vendor's party is untouched.
	vb, _ := r.s.GetVendor(r.buyer, r.vendorB)
	if p, _, _ := r.s.GetParty(r.buyer, vb.PartyID); p.Version != 1 {
		t.Errorf("vendor B's party version %d, want untouched", p.Version)
	}
}

func TestSupplier_RejectingOrWithdrawingAProfileChangeLeavesThePartyAlone(t *testing.T) {
	r := newSupplierRig(t)
	v, _ := r.s.GetVendor(r.buyer, r.vendorA)
	req, _ := r.s.CreateRequest(r.buyer, r.vendorA, domain.RequestProfileChange, "Rename", "x", map[string]string{"legalName": "Other Name"})
	if d, err := r.s.DecideRequest(r.approve, req.ID, false, "no"); err != nil || d.AppliedAt != nil {
		t.Fatalf("reject %+v %v", d, err)
	}
	if p, _, _ := r.s.GetParty(r.buyer, v.PartyID); *p.LegalName != "Acme" {
		t.Errorf("rejected change was applied: %v", *p.LegalName)
	}
}

func TestSupplier_AProfileChangeThePartyCannotTakeFailsTheApprovalAndKeepsItOpen(t *testing.T) {
	r := newSupplierRig(t)
	v, _ := r.s.GetVendor(r.buyer, r.vendorA)
	// A change that would empty the legal name is refused at creation by the API; the store refuses it again at approval.
	req, err := r.s.CreateRequest(r.buyer, r.vendorA, domain.RequestProfileChange, "Rename", "x", map[string]string{"legalName": ""})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.s.DecideRequest(r.approve, req.ID, true, ""); problemCode(t, err) != store.CodeSupplierInvalid {
		t.Fatalf("approve an invalid change: %v", err)
	}
	if got, _ := r.s.GetRequest(r.buyer, "", req.ID); got.Status != domain.RequestSubmitted || got.AppliedAt != nil {
		t.Errorf("request after a failed approval: %+v", got)
	}
	if p, _, _ := r.s.GetParty(r.buyer, v.PartyID); p.Version != 1 {
		t.Errorf("party version %d after a failed approval", p.Version)
	}
}
