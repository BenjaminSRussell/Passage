package store_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
	"github.com/BenjaminSRussell/passage/services/party/internal/store"
)

// registrar is the portal acting for no vendor: a token with one legal entity and no user duties.
func registrar() context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: legalEntityA, UserID: "portal-prospect", Service: tenant.VendorPortalIssuer,
		Duties: map[string]struct{}{tenant.DutyPortalRegister: {}}})
}

func prospect(email string) domain.NewProspect {
	return domain.NewProspect{CompanyName: "Birch Valve Co", ContactName: "Sam Birch", Email: email, Country: "US", TaxID: "12-3456789", Message: "We make valves."}.Clean()
}

func TestProspect_RegistrationWaitsForABuyerAndRepeatsAreQuiet(t *testing.T) {
	r := newSupplierRig(t)
	created, err := r.s.RegisterProspect(registrar(), prospect("Sam@Birch.example"))
	if err != nil || !created {
		t.Fatalf("register: %v %v", created, err)
	}
	if again, err := r.s.RegisterProspect(registrar(), prospect("sam@birch.example")); err != nil || again {
		t.Fatalf("the same address twice created=%v err=%v, want a quiet no-op", again, err)
	}
	list, _ := r.s.ListProspects(r.buyer, "", 50)
	if len(list) != 1 || list[0].Status != domain.ProspectNew || list[0].Email != "sam@birch.example" {
		t.Fatalf("buyer list %+v", list)
	}
	if other, _ := r.s.ListProspects(asUser(1, legalEntityB, "x"), "", 50); len(other) != 0 {
		t.Errorf("another legal entity sees %d registrations", len(other))
	}
	if _, err := r.s.GetProspect(r.buyer, "not-a-uuid"); problemCode(t, err) != store.CodeProspectNotFound {
		t.Errorf("bad id: %v", err)
	}
	rej, err := r.s.RejectProspect(r.approve, list[0].ID, "not a fit")
	if err != nil || rej.Status != domain.ProspectRejected || rej.DecisionNote != "not a fit" {
		t.Fatalf("reject %+v %v", rej, err)
	}
	if _, err := r.s.RejectProspect(r.approve, list[0].ID, "again"); problemCode(t, err) != store.CodeProspectState {
		t.Errorf("deciding twice: %v", err)
	}
	// After a decision the same address may register again.
	if again, err := r.s.RegisterProspect(registrar(), prospect("sam@birch.example")); err != nil || !again {
		t.Errorf("register after a rejection: %v %v", again, err)
	}
}

func TestProspect_AcceptLinksThePartyVendorAndOnboarding(t *testing.T) {
	r := newSupplierRig(t)
	_, _ = r.s.RegisterProspect(registrar(), prospect("sam@birch.example"))
	list, _ := r.s.ListProspects(r.buyer, "", 50)
	acc, err := r.s.AcceptProspect(r.buyer, list[0].ID, r.vendorA, r.vendorB, ids.New())
	if err != nil || acc.Status != domain.ProspectAccepted || acc.VendorID != r.vendorB {
		t.Fatalf("accept %+v %v", acc, err)
	}
	if _, err := r.s.AcceptProspect(r.buyer, list[0].ID, "", "", ""); problemCode(t, err) != store.CodeProspectState {
		t.Errorf("accepting twice: %v", err)
	}
}

func TestInvite_TokenIsShownOnceVerifiedWithoutSpendingAndAcceptedOnce(t *testing.T) {
	r := newSupplierRig(t)
	inv, token, err := r.s.CreateInvite(r.buyer, r.vendorA, "colleague@acme.example", "Casey")
	if err != nil || inv.Status != domain.InvitePending || inv.Role != domain.InviteRole || token == "" {
		t.Fatalf("create %+v %q %v", inv, token, err)
	}
	if strings.Contains(inv.Email+inv.DisplayName+inv.ID, token) {
		t.Fatal("the invitation carries its token")
	}
	list, _ := r.s.ListInvites(r.buyer, r.vendorA, 50)
	if len(list) != 1 {
		t.Fatalf("list %+v", list)
	}
	if other, _ := r.s.ListInvites(r.buyer, r.vendorB, 50); len(other) != 0 {
		t.Errorf("vendor B lists vendor A's invitations: %+v", other)
	}
	// Another vendor's verified caller cannot use the token, even with the real secret.
	if _, err := r.s.VerifyInvite(r.buyer, r.vendorB, token); problemCode(t, err) != store.CodeInviteInvalid {
		t.Errorf("another vendor verifies: %v", err)
	}
	// A wrong secret and a garbled token read the same.
	parsed, _ := domain.ParseInviteToken(token)
	wrong := strings.Join([]string{"inv1", parsed.TenantID, parsed.LegalEntityID, parsed.VendorID, parsed.InviteID, strings.Repeat("A", 43)}, ".")
	for name, tk := range map[string]string{"wrong secret": wrong, "garbled": "nope"} {
		if _, err := r.s.VerifyInvite(r.buyer, r.vendorA, tk); problemCode(t, err) != store.CodeInviteInvalid {
			t.Errorf("%s: %v", name, err)
		}
	}
	d, err := r.s.VerifyInvite(r.buyer, r.vendorA, token)
	if err != nil || d.Email != "colleague@acme.example" || d.VendorName != "Acme" || d.Status != domain.InvitePending {
		t.Fatalf("verify %+v %v", d, err)
	}
	if _, err := r.s.VerifyInvite(r.buyer, r.vendorA, token); err != nil {
		t.Errorf("verifying twice must not spend it: %v", err)
	}
	acc, err := r.s.AcceptInvite(r.buyer, r.vendorA, token)
	if err != nil || acc.Status != domain.InviteAccepted {
		t.Fatalf("accept %+v %v", acc, err)
	}
	if _, err := r.s.AcceptInvite(r.buyer, r.vendorA, token); problemCode(t, err) != store.CodeInviteInvalid {
		t.Errorf("accepting twice: %v", err)
	}
	if _, err := r.s.RevokeInvite(r.buyer, r.vendorA, inv.ID); problemCode(t, err) != store.CodeSupplierState {
		t.Errorf("revoking an accepted invitation: %v", err)
	}
	types := outboxTypes(t, r.app)
	if !strings.Contains(types, "vendorinvite.created.v1") || !strings.Contains(types, "vendorinvite.accepted.v1") {
		t.Errorf("outbox %s", types)
	}
	audit, _ := r.s.ListAudit(r.buyer, r.vendorA, 50)
	var actions []string
	for _, e := range audit {
		actions = append(actions, e.Action)
	}
	if got := strings.Join(actions, ","); !strings.Contains(got, "invite.created") || !strings.Contains(got, "invite.accepted") {
		t.Errorf("audit %s", got)
	}
}

func TestInvite_ResendingReplacesTheOpenOneAndRevokeKillsIt(t *testing.T) {
	r := newSupplierRig(t)
	_, first, _ := r.s.CreateInvite(r.buyer, r.vendorA, "c@acme.example", "")
	second, secondToken, err := r.s.CreateInvite(r.buyer, r.vendorA, "C@acme.example", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.s.VerifyInvite(r.buyer, r.vendorA, first); problemCode(t, err) != store.CodeInviteInvalid {
		t.Errorf("the replaced token still verifies: %v", err)
	}
	if rev, err := r.s.RevokeInvite(r.buyer, r.vendorA, second.ID); err != nil || rev.Status != domain.InviteRevoked {
		t.Fatalf("revoke %+v %v", rev, err)
	}
	if _, err := r.s.VerifyInvite(r.buyer, r.vendorA, secondToken); problemCode(t, err) != store.CodeInviteInvalid {
		t.Errorf("a revoked token verifies: %v", err)
	}
	if _, err := r.s.RevokeInvite(r.buyer, r.vendorB, second.ID); problemCode(t, err) != store.CodeSupplierNotFound {
		t.Errorf("another vendor revokes: %v", err)
	}
}

func TestInvite_AnExpiredInvitationReadsExpiredAndDoesNotVerify(t *testing.T) {
	r := newSupplierRig(t)
	inv, token, _ := r.s.CreateInvite(r.buyer, r.vendorA, "late@acme.example", "")
	ctx := principalContext(1, "")
	if err := dbx.WithTenantTx(ctx, r.app, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update vendor_invite set expires_at = now() - interval '1 minute' where id = $1`, inv.ID)
		return err
	}); err != nil {
		t.Fatalf("cannot age the invitation: %v", err)
	}
	if _, err := r.s.VerifyInvite(r.buyer, r.vendorA, token); problemCode(t, err) != store.CodeInviteInvalid {
		t.Errorf("an expired invitation verifies: %v", err)
	}
	if list, _ := r.s.ListInvites(r.buyer, r.vendorA, 10); len(list) != 1 || list[0].Status != domain.InviteExpired {
		t.Errorf("list %+v", list)
	}
}
