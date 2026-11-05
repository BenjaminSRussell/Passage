package store

import (
	"context"
	"errors"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	"github.com/BenjaminSRussell/passage/services/edi/migrations"
)

// The partner update runs as passage_app under If-Match, and once a trade
// profile binds the partner to a legal entity only callers of that entity
// (or of every entity) may change it.
func TestUpdatePartner_VersionScopeAndIdentity(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	st := New(app, 30)
	const e1, e2 = "00000000-0000-0000-0000-0000000000e1", "00000000-0000-0000-0000-0000000000e2"
	scoped := func(le string) context.Context {
		return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: le, UserID: "u-" + le[len(le)-2:],
			Duties: map[string]struct{}{"edi.admin": {}}})
	}
	ctx := testkit.Ctx(testkit.TenantID(1), "edi.admin")
	p, _, err := st.CreatePartner(ctx, "k", "h", profile("UPD"))
	if err != nil {
		t.Fatal(err)
	}
	prof := profile("UPD")
	prof.Name, prof.AckSLAMinutes = "Renamed", 30
	prof.EDIFACT = &domain.EdifactIdentity{ID: "4012345000009", Qualifier: "14"}
	got, err := st.UpdatePartner(ctx, p.ID, 1, prof, "active")
	if err != nil || got.Version != 2 || got.Name != "Renamed" || got.AckSLAMinutes != 30 || got.EdifactContrl != domain.ContrlRequested || got.Environment != "PROD" {
		t.Fatalf("update: %+v %v", got, err)
	}
	if cur, err := st.UpdatePartner(ctx, p.ID, 1, prof, "active"); !errors.Is(err, ErrVersionConflict) || cur.Version != 2 {
		t.Fatalf("stale version: %+v %v", cur, err)
	}
	if _, err := st.UpdatePartner(ctx, "0195f0a0-0000-7000-8000-0000000000aa", 1, prof, "active"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown partner: %v", err)
	}
	if _, err := st.UpdatePartner(testkit.Ctx(testkit.TenantID(2), "edi.admin"), p.ID, 2, prof, "active"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another tenant: %v", err)
	}
	other, _, err := st.CreatePartner(ctx, "k2", "h2", profile("OTHER"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdatePartner(ctx, other.ID, 1, prof, "active"); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("taking another partner's identity: %v", err)
	}

	// Bound to e1 by its trade profile: e2 may not change it, e1 and an
	// all-entity caller may.
	tp := domain.TradeProfile{LegalEntityID: e1, CustomerID: "00000000-0000-0000-0000-0000000000c1", DefaultSCAC: "ODFL",
		ShipFrom: domain.Location{Name: "F", GLN: "0614141000012"}, ShipTo: domain.Location{Name: "T", GLN: "0614141000029"},
		BillTo: domain.Location{Name: "B", GLN: "0614141000036"}, RemitTo: domain.Location{Name: "R", GLN: "0614141000043"}}
	if _, err := st.PutTradeProfile(scoped(e1), p.ID, tp); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdatePartner(scoped(e2), p.ID, 2, prof, "inactive"); !errors.Is(err, ErrOutOfScope) {
		t.Fatalf("another entity: %v", err)
	}
	unscoped := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), UserID: "u", Duties: map[string]struct{}{"edi.admin": {}}})
	if _, err := st.UpdatePartner(unscoped, p.ID, 2, prof, "inactive"); !errors.Is(err, ErrOutOfScope) {
		t.Fatalf("a caller with no entity and no all-entity duty: %v", err)
	}
	if got, err := st.UpdatePartner(scoped(e1), p.ID, 2, prof, "inactive"); err != nil || got.Status != "inactive" || got.Version != 3 {
		t.Fatalf("its own entity: %+v %v", got, err)
	}
	all := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), UserID: "admin", Duties: testkit.ScopeDuties("")})
	prof.EDIFACT = nil
	if got, err := st.UpdatePartner(all, p.ID, 3, prof, "active"); err != nil || got.EdifactID != "" || got.EdifactContrl != "" || got.Version != 4 {
		t.Fatalf("all entities, EDIFACT identity removed: %+v %v", got, err)
	}
	// Repointing the profile moves the binding with it.
	tp.LegalEntityID = e2
	if _, err := st.PutTradeProfile(all, p.ID, tp); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdatePartner(scoped(e1), p.ID, 4, prof, "active"); !errors.Is(err, ErrOutOfScope) {
		t.Fatalf("after the profile moved to e2: %v", err)
	}
	if _, err := st.UpdatePartner(scoped(e2), p.ID, 4, prof, "active"); err != nil {
		t.Fatalf("e2 after the move: %v", err)
	}
}
