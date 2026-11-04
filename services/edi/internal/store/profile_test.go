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

func TestTradeProfile_UpsertAsAppRoleAndTenantIsolated(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	st := New(app, 30)
	a := testkit.Ctx(testkit.TenantID(1), "EDI_ADMIN")
	b := testkit.Ctx(testkit.TenantID(2), "EDI_ADMIN")
	p, _, err := st.CreatePartner(a, "k", "h", profile("TRADE"))
	if err != nil {
		t.Fatal(err)
	}
	tp := domain.TradeProfile{LegalEntityID: "00000000-0000-0000-0000-0000000000e1", CustomerID: "00000000-0000-0000-0000-0000000000c1", DefaultSCAC: "ODFL",
		ShipFrom: domain.Location{Name: "F", GLN: "0614141000012"}, ShipTo: domain.Location{Name: "T", GLN: "0614141000029"},
		BillTo: domain.Location{Name: "B", GLN: "0614141000036"}, RemitTo: domain.Location{Name: "R", GLN: "0614141000043"}}
	if _, err := st.PutTradeProfile(a, p.ID, tp); err != nil {
		t.Fatalf("insert as passage_app: %v", err)
	}
	tp.DefaultSCAC = "UPSN"
	if _, err := st.PutTradeProfile(a, p.ID, tp); err != nil {
		t.Fatalf("update as passage_app: %v", err)
	}
	got, err := st.GetTradeProfile(a, p.ID)
	if err != nil || got != tp {
		t.Fatalf("read back: %+v %v", got, err)
	}
	if _, err := st.GetTradeProfile(b, p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another tenant must not read the profile: %v", err)
	}
	if _, err := st.PutTradeProfile(b, p.ID, tp); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another tenant must not write the profile of a partner it cannot see: %v", err)
	}
	other := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: "00000000-0000-0000-0000-0000000000e2",
		UserID: "user-e2", Duties: map[string]struct{}{"EDI_ADMIN": {}}})
	if _, err := st.GetTradeProfile(other, p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a caller of another legal entity must not read the profile: %v", err)
	}
	moved := tp
	moved.LegalEntityID = "00000000-0000-0000-0000-0000000000e2"
	if _, err := st.PutTradeProfile(other, p.ID, moved); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a caller of another legal entity must not repoint the profile: %v", err)
	}
	all := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), UserID: "admin", Duties: testkit.ScopeDuties("")})
	if got, err := st.GetTradeProfile(all, p.ID); err != nil || got != tp {
		t.Fatalf("an all-entity caller reads the profile unchanged: %+v %v", got, err)
	}
}
