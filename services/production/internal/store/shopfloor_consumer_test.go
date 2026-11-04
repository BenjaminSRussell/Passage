package store

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/production/migrations"
)

// AC-25.1 through the application role (RLS on): the registration consumer runs as a
// tenant-level principal that sees no order, reads only the order's legal entity, and posts
// the job card pinned to it, so the card, its WIP rows and their GL posting stay in that
// legal entity. A user, or the consumer pinned to another legal entity, gets nothing.
func TestShopfloorConsumer_LegalEntityLookupAndPinnedPost(t *testing.T) {
	if os.Getenv(testkit.EnvURL) == "" || os.Getenv(testkit.EnvAppURL) == "" {
		t.Skipf("%s and %s are needed for the RLS test", testkit.EnvURL, testkit.EnvAppURL)
	}
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := New(app)
	tid := testkit.TenantID(9411)
	le, otherLE := ids.New(), ids.New()
	st := startedOrder(t, s, scopedCtx(tid, le), routedOrder(t, "key-sf-consumer", le, 10))

	consumer := func(legalEntity string) context.Context {
		p, err := tenant.ServicePrincipal("production-shopfloor", tid, legalEntity)
		if err != nil {
			t.Fatal(err)
		}
		return tenant.With(context.Background(), p)
	}
	reg := ids.New()

	if _, _, err := s.PostShopfloorJobCard(consumer(""), shopfloorCard(reg, st.orderID, ShopfloorJobCard{})); !errors.Is(err, ErrJobCardRefused) {
		t.Fatalf("tenant-level post: %v, want refused (the order is outside its scope)", err)
	}
	got, ok, err := s.ShopfloorOrderLegalEntity(consumer(""), st.orderID)
	if err != nil || !ok || got != le {
		t.Fatalf("order legal entity %q %v %v, want %s", got, ok, err, le)
	}
	if _, ok, err := s.ShopfloorOrderLegalEntity(consumer(""), ids.New()); err != nil || ok {
		t.Fatalf("unknown order: %v %v", ok, err)
	}
	if _, _, err := s.ShopfloorOrderLegalEntity(scopedCtx(tid, ""), st.orderID); !errors.Is(err, ErrJobCardRefused) {
		t.Fatalf("user lookup: %v, want refused", err)
	}
	if _, _, err := s.ShopfloorOrderLegalEntity(consumer(le), st.orderID); !errors.Is(err, ErrJobCardRefused) {
		t.Fatalf("pinned lookup: %v, want refused", err)
	}
	if _, _, err := s.ShopfloorCardLegalEntity(consumer(""), reg); err != nil {
		t.Fatal(err)
	} else if _, ok, _ := s.ShopfloorCardLegalEntity(consumer(""), reg); ok {
		t.Fatal("card legal entity before any card")
	}

	if _, _, err := s.PostShopfloorJobCard(consumer(otherLE), shopfloorCard(reg, st.orderID, ShopfloorJobCard{})); !errors.Is(err, ErrJobCardRefused) {
		t.Fatalf("post pinned to another legal entity: %v, want refused", err)
	}
	j, posted, err := s.PostShopfloorJobCard(consumer(le), shopfloorCard(reg, st.orderID, ShopfloorJobCard{Actor: "service:production-shopfloor"}))
	if err != nil || !posted || len(j.Lines) != 1 || !j.Lines[0].Amount.Equal(d("52")) {
		t.Fatalf("pinned post: %+v %v %v", j, posted, err)
	}
	if got, ok, err := s.ShopfloorCardLegalEntity(consumer(""), reg); err != nil || !ok || got != le {
		t.Fatalf("card legal entity %q %v %v", got, ok, err)
	}
	if changed, err := s.AdjustShopfloorJobCard(consumer(le), reg, d("0"), "trace-adjust"); err != nil || !changed {
		t.Fatalf("pinned adjust: %v %v", changed, err)
	}
	if l := sfLaborActual(st); !l.IsZero() {
		t.Fatalf("labor after the correction to 0 h: %s", l)
	}
}
