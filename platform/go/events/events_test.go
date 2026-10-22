package events

import "testing"

func TestTypeAndTopic(t *testing.T) {
	typ := Type("ledger", "journal", "posted", 1)
	if typ != "dev.passage.ledger.journal.posted.v1" {
		t.Fatalf("got %s", typ)
	}
	if !ValidType(typ) {
		t.Fatal("expected valid")
	}
	if Topic(typ) != "passage.ledger.journal" {
		t.Fatalf("topic %s", Topic(typ))
	}
	for _, bad := range []string{"ledger.journal.posted", "dev.passage.Ledger.journal.posted.v1", "dev.passage.ledger.journal.posted"} {
		if ValidType(bad) {
			t.Errorf("%s should be invalid", bad)
		}
	}
}

func TestNew(t *testing.T) {
	e, err := New(Type("party", "customer", "created", 1), "/services/party", "cust-1", "tenant-a", map[string]string{"id": "cust-1"})
	if err != nil {
		t.Fatal(err)
	}
	if e.ID == "" || e.SpecVersion != "1.0" || e.TenantID != "tenant-a" || string(e.Data) != `{"id":"cust-1"}` {
		t.Fatalf("bad event %+v", e)
	}
}
