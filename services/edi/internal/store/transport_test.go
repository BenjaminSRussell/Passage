package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	"github.com/BenjaminSRussell/passage/services/edi/migrations"
)

const (
	entityA = "00000000-0000-0000-0000-0000000000e1"
	entityB = "00000000-0000-0000-0000-0000000000e2"
)

func tradeProfile(le string) domain.TradeProfile {
	return domain.TradeProfile{LegalEntityID: le, CustomerID: "00000000-0000-0000-0000-0000000000c1", DefaultSCAC: "ODFL",
		ShipFrom: domain.Location{Name: "F", GLN: "0614141000012"}, ShipTo: domain.Location{Name: "T", GLN: "0614141000029"},
		BillTo: domain.Location{Name: "B", GLN: "0614141000036"}, RemitTo: domain.Location{Name: "R", GLN: "0614141000043"}}
}

func sftpTransport() domain.PartnerTransport {
	return domain.PartnerTransport{Kind: domain.TransportSFTP, SFTP: &domain.SFTPSettings{Host: "sftp.example", Port: 22, User: "edi",
		HostKeyFingerprint: "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", AuthMethod: "key", CredentialRef: "sftp-key",
		OutboundDir: "/out", InboundDir: "/in", ArchiveDir: "processed", FailedDir: "failed", PollIntervalSeconds: 60}}
}

func entityCtx(tid, le string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: tid, LegalEntityID: le, UserID: "u", Duties: map[string]struct{}{}})
}

// The transport, its deliveries and what it received are scoped to the
// legal entity of the trade profile; the cross-tenant work index answers only
// through its SECURITY DEFINER functions, and only for due work.
func TestTransport_EntityScopedDueIndexAndEndpointLookup(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	st := New(app, 30)
	t1 := testkit.TenantID(1)
	a, b, other := entityCtx(t1, entityA), entityCtx(t1, entityB), entityCtx(testkit.TenantID(2), entityA)
	p, _, err := st.CreatePartner(a, "k", "h", profile("TRANSPORT"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutTransport(a, p.ID, sftpTransport()); !errors.Is(err, ErrNoTradeProfile) {
		t.Fatalf("without a profile: %v", err)
	}
	if _, err := st.PutTransport(a, "0195f0a0-0000-7000-8000-0000000000aa", sftpTransport()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown partner: %v", err)
	}
	if _, err := st.PutTradeProfile(a, p.ID, tradeProfile(entityA)); err != nil {
		t.Fatal(err)
	}
	rec, err := st.PutTransport(a, p.ID, sftpTransport())
	if err != nil || rec.LegalEntityID != entityA || rec.Config.SFTP == nil || rec.Config.SFTP.InboundDir != "/in" {
		t.Fatalf("%+v %v", rec, err)
	}
	for name, ctx := range map[string]context.Context{"other entity": b, "other tenant": other} {
		if _, err := st.GetTransport(ctx, p.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s reads the transport: %v", name, err)
		}
	}
	if _, err := st.PutTransport(b, p.ID, domain.PartnerTransport{Kind: domain.TransportAPI}); !errors.Is(err, ErrNoTradeProfile) {
		t.Fatalf("another entity overwrites: %v", err)
	}

	out, _, err := st.CreateOutbound(a, "o1", "h", outbound(p.ID, "856"))
	if err != nil || out.Delivery == nil || out.Delivery.Transport != "SFTP" || out.Delivery.Status != "PENDING" {
		t.Fatalf("%+v %v", out, err)
	}
	// Another entity records an outbound document to the same partner but
	// cannot see the transport: nothing is queued for it.
	outB, _, err := st.CreateOutbound(b, "o2", "h", outbound(p.ID, "856"))
	if err != nil || outB.Delivery != nil {
		t.Fatalf("%+v %v", outB, err)
	}
	if d, err := st.GetInterchange(b, out.InterchangeID); err != nil || len(d.Deliveries) != 0 {
		t.Fatalf("other entity sees deliveries: %+v %v", d.Deliveries, err)
	}

	due, err := st.DueTenants(context.Background(), "DELIVERY")
	if err != nil || len(due) != 1 || due[0] != t1 {
		t.Fatalf("due delivery tenants %v %v", due, err)
	}
	sys := SystemContext(context.Background(), t1)
	claimed, err := st.ClaimDeliveries(sys, 10, time.Minute)
	if err != nil || len(claimed) != 1 || claimed[0].Payload != out.X12 || claimed[0].Attempts != 1 || claimed[0].TransportCfg.Config.SFTP == nil {
		t.Fatalf("claimed %+v %v", claimed, err)
	}
	// Leased: neither due nor claimable again until the lease ends.
	if again, err := st.ClaimDeliveries(sys, 10, time.Minute); err != nil || len(again) != 0 {
		t.Fatalf("claimed twice: %+v %v", again, err)
	}
	if due, _ := st.DueTenants(context.Background(), "DELIVERY"); len(due) != 0 {
		t.Fatalf("leased delivery still due: %v", due)
	}
	if err := st.FinishDelivery(sys, claimed[0].ID, DeliveryOutcome{Status: "DELIVERED", Reference: "/out/x.x12"}); err != nil {
		t.Fatal(err)
	}
	d, err := st.GetInterchange(a, out.InterchangeID)
	if err != nil || d.Messages[0].Status != "SENT" || d.Deliveries[0].Status != "DELIVERED" || d.Deliveries[0].DeliveredAt == nil {
		t.Fatalf("%+v %v", d, err)
	}

	// SFTP polls are due at once and leased one interval ahead.
	if due, _ := st.DueTenants(context.Background(), "SFTP_POLL"); len(due) != 1 {
		t.Fatalf("due polls %v", due)
	}
	polls, err := st.ClaimSFTPPolls(sys, 5)
	if err != nil || len(polls) != 1 || polls[0].PartnerID != p.ID {
		t.Fatalf("%+v %v", polls, err)
	}
	if due, _ := st.DueTenants(context.Background(), "SFTP_POLL"); len(due) != 0 {
		t.Fatalf("leased poll still due: %v", due)
	}
	if _, err := st.DueTenants(context.Background(), "AS2_ENDPOINT"); err != nil {
		t.Fatal(err)
	}
	if due, _ := st.DueTenants(context.Background(), "AS2_ENDPOINT"); len(due) != 0 {
		t.Fatalf("the endpoint index is not a due list: %v", due)
	}

	// Received files are entity scoped too.
	src := InboundSource{Transport: "SFTP", Ref: "po.x12", SHA256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", LegalEntityID: entityA}
	if n, err := st.RecordInbound(sys, p.ID, src, "FAILED", nil, "x"); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	if n, err := st.RecordInbound(sys, p.ID, src, "PARKED", nil, "y"); err != nil || n != 2 {
		t.Fatalf("%d %v", n, err)
	}
	if got, err := st.FindInbound(a, p.ID, "SFTP", "po.x12"); err != nil || len(got) != 1 || got[0].Status != "PARKED" || got[0].Attempts != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	if got, err := st.FindInbound(b, p.ID, "SFTP", "po.x12"); err != nil || len(got) != 0 {
		t.Fatalf("other entity sees received files: %+v %v", got, err)
	}

	// The AS2 endpoint resolves only while the transport is AS2.
	if _, _, err := st.ResolveAS2Endpoint(context.Background(), rec.EndpointID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SFTP endpoint resolved: %v", err)
	}
	as2 := domain.PartnerTransport{Kind: domain.TransportAS2, AS2: &domain.AS2Settings{PartnerAS2ID: "P", OurAS2ID: "US", URL: "https://p.example/as2",
		PartnerCertificate: "-----BEGIN CERTIFICATE-----\nAA==\n-----END CERTIFICATE-----\n", SigningKeyRef: "as2-us", SigningAlgorithm: "sha-256",
		EncryptionAlgorithm: "none", MDNMode: "sync"}}
	if _, err := st.PutTransport(a, p.ID, as2); err != nil {
		t.Fatal(err)
	}
	tid, pid, err := st.ResolveAS2Endpoint(context.Background(), rec.EndpointID)
	if err != nil || tid != t1 || pid != p.ID {
		t.Fatalf("%s %s %v", tid, pid, err)
	}
	if due, _ := st.DueTenants(context.Background(), "SFTP_POLL"); len(due) != 0 {
		t.Fatalf("an AS2 transport is polled: %v", due)
	}
	if _, err := st.PutTransport(a, p.ID, domain.PartnerTransport{Kind: domain.TransportAPI}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.ResolveAS2Endpoint(context.Background(), rec.EndpointID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("API transport resolved: %v", err)
	}
	// Repointing the trade profile carries the transport to the new entity.
	all := tenant.With(context.Background(), tenant.Principal{TenantID: t1, UserID: "admin", Duties: testkit.ScopeDuties("")})
	if _, err := st.PutTradeProfile(all, p.ID, tradeProfile(entityB)); err != nil {
		t.Fatal(err)
	}
	if got, err := st.GetTransport(b, p.ID); err != nil || got.LegalEntityID != entityB {
		t.Fatalf("after repointing: %+v %v", got, err)
	}
	if _, err := st.GetTransport(a, p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old entity still reads it: %v", err)
	}
}
