package usage_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/metadata/internal/usage"
)

type source struct {
	principals []usage.Principal
	entities   []usage.EntityCount
}

func (s source) Principals(context.Context, time.Time) ([]usage.Principal, error) {
	return s.principals, nil
}

func (s source) EntityCounts(context.Context, time.Time) ([]usage.EntityCount, error) {
	return s.entities, nil
}

func TestCollectAggregatesNamedUsersPrincipalsRequestsAndRows(t *testing.T) {
	day := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	meter := usage.NewMeter(source{
		principals: []usage.Principal{
			{TenantID: "t1", LegalEntityID: "le1", Role: "buyer", UserID: "u1", NamedUser: true},
			{TenantID: "t1", LegalEntityID: "le1", Role: "buyer", UserID: "u2", NamedUser: true},
			{TenantID: "t1", LegalEntityID: "le1", Role: "service", UserID: "svc", NamedUser: false},
		},
		entities: []usage.EntityCount{{TenantID: "t1", Entity: "Vendor", Rows: 42}},
	})
	meter.RecordRequest("t1", "Vendor", 3)

	rows, err := meter.Collect(context.Background(), day)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %#v", rows)
	}
	if rows[0].NamedUsers != 2 || rows[0].Principals != 3 {
		t.Fatalf("principal usage = %#v", rows[0])
	}
	if rows[1].Requests != 1 || rows[1].RowsRead != 3 || rows[1].RowsStored != 42 {
		t.Fatalf("entity usage = %#v", rows[1])
	}
}

func TestWriteCSVProducesStableLicensingReport(t *testing.T) {
	var out bytes.Buffer
	rows := []usage.Row{{
		Day: time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC), TenantID: "t1",
		LegalEntityID: "le1", Role: "buyer", NamedUsers: 2, Principals: 3,
	}}
	if err := usage.WriteCSV(&out, rows); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.HasPrefix(got, "day,tenant_id,legal_entity_id,role,entity,named_users,principals,api_requests,rows_read,rows_stored\n") {
		t.Fatalf("CSV header:\n%s", got)
	}
	if !strings.Contains(got, "2026-10-24,t1,le1,buyer,,2,3,0,0,0\n") {
		t.Fatalf("CSV row:\n%s", got)
	}
}
