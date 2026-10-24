package domain_test

import (
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/audit/internal/domain"
)

func change(field, oldV, newV, trace string) domain.Change {
	return domain.Change{
		Entity: "party.vendor", RecordID: "v-1", Field: field,
		OldValue: oldV, NewValue: newV, UserID: "user-1", TraceID: trace,
		OccurredAt: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC),
	}
}

func TestHashChainLinksAndDetectsTamper(t *testing.T) {
	first, err := domain.Link("", change("name", "Acme", "Acme Ltd", "trace-1"))
	if err != nil {
		t.Fatal(err)
	}
	if first.PrevHash != domain.Genesis || first.Hash == "" || first.Hash == first.PrevHash {
		t.Fatalf("first entry %+v", first)
	}
	second, err := domain.Link(first.Hash, change("city", "Austin", "Dallas", "trace-2"))
	if err != nil {
		t.Fatal(err)
	}
	if err := domain.Verify([]domain.Entry{first, second}); err != nil {
		t.Fatal(err)
	}
	second.NewValue = "Houston"
	if err := domain.Verify([]domain.Entry{first, second}); err == nil {
		t.Fatal("tampered new value must break the chain")
	}
}

func TestValidateRequiresTraceAndUser(t *testing.T) {
	c := domain.Change{}
	fields := domain.Validate(c)
	for _, key := range []string{"entity", "recordId", "field", "userId", "traceId", "occurredAt"} {
		if fields[key] == "" {
			t.Errorf("missing %s", key)
		}
	}
}

func TestAlertsTextsDocuments(t *testing.T) {
	c := change("creditLimit", "100", "50", "trace-1")
	if !domain.Match(domain.AlertRule{Entity: "party.vendor", Field: "creditLimit", Kind: domain.AlertChange}, c) {
		t.Fatal("change rule should match")
	}
	if domain.Match(domain.AlertRule{Entity: "party.vendor", Field: "name", Kind: domain.AlertChange}, c) {
		t.Fatal("other field must not match")
	}
	due := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	if !domain.Due(domain.AlertRule{Kind: domain.AlertDueDate}, due, due) {
		t.Fatal("due-date rule should fire at the due instant")
	}
	if got := domain.Expand("Hello {name}", map[string]string{"name": "Ada"}); got != "Hello Ada" {
		t.Fatalf("expand = %q", got)
	}
	doc := domain.Document{ID: "d1", Type: "invoice", Location: "s3://bucket/d1", Status: domain.DocDraft, Version: 1}
	out, err := domain.Checkout(doc, "ada")
	if err != nil {
		t.Fatal(err)
	}
	in, err := domain.Checkin(out, "ada")
	if err != nil || in.Version != 2 || in.Status != domain.DocCurrent {
		t.Fatalf("checkin %+v %v", in, err)
	}
	held := domain.Hold(in)
	if domain.CanPurge(held, due.Add(24*time.Hour), due) {
		t.Fatal("legal hold blocks purge")
	}
	if _, err := domain.Checkout(held, "ada"); err == nil {
		t.Fatal("held document cannot be checked out")
	}
	blob := domain.NewMemoryBlob()
	if err := blob.Put("d1", []byte("pdf")); err != nil {
		t.Fatal(err)
	}
	if body, err := blob.Get("d1"); err != nil || string(body) != "pdf" {
		t.Fatalf("blob %q %v", body, err)
	}
	if err := (domain.CleanScanner{}).Scan([]byte("pdf")); err != nil {
		t.Fatal(err)
	}
}
