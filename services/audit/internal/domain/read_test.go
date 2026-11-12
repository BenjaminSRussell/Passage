package domain_test

import (
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/audit/internal/domain"
)

var readNow = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

func read(record, purpose string) domain.Read {
	return domain.Read{
		Entity: "hr.worker.benefits", RecordID: record, Fields: []string{"lifeEvents.kind", "enrollments.optionCode"},
		Purpose: purpose, UserID: "user-1", Source: "hr", TraceID: "trace-1", OccurredAt: readNow,
	}
}

func chainOf(t *testing.T, reads ...domain.Read) []domain.ReadEntry {
	t.Helper()
	prev := ""
	var out []domain.ReadEntry
	for _, r := range reads {
		e, err := domain.LinkRead(prev, r, readNow)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
		prev = e.Hash
	}
	return out
}

func TestReadChainLinksAndVerifies(t *testing.T) {
	entries := chainOf(t, read("w-1", "benefits enquiry"), read("w-2", "audit"), read("w-3", "payroll check"))
	if entries[0].PrevHash != domain.Genesis || entries[1].PrevHash != entries[0].Hash {
		t.Fatalf("entries do not link: %+v", entries)
	}
	if s := domain.VerifyReads(entries); !s.Verified || s.Entries != 3 || s.Head != entries[2].Hash || s.BrokenAt != -1 {
		t.Fatalf("status %+v", s)
	}
	if s := domain.VerifyReads(nil); !s.Verified || s.Entries != 0 || s.Head != domain.Genesis {
		t.Fatalf("an empty chain verifies at Genesis: %+v", s)
	}
}

func TestReadChainDetectsEditDeletionAndReorder(t *testing.T) {
	good := chainOf(t, read("w-1", "a"), read("w-2", "b"), read("w-3", "c"))
	edited := append([]domain.ReadEntry(nil), good...)
	edited[1].Purpose = "something else"
	if s := domain.VerifyReads(edited); s.Verified || s.BrokenAt != 1 {
		t.Fatalf("an edited purpose must break the chain at 1: %+v", s)
	}
	editedUser := append([]domain.ReadEntry(nil), good...)
	editedUser[0].UserID = "someone-else"
	if s := domain.VerifyReads(editedUser); s.Verified || s.BrokenAt != 0 {
		t.Fatalf("an edited reader must break the chain at 0: %+v", s)
	}
	deleted := []domain.ReadEntry{good[0], good[2]}
	if s := domain.VerifyReads(deleted); s.Verified || s.BrokenAt != 1 {
		t.Fatalf("a deleted row must break the chain at the gap: %+v", s)
	}
	reordered := []domain.ReadEntry{good[1], good[0], good[2]}
	if s := domain.VerifyReads(reordered); s.Verified {
		t.Fatal("a reordered chain must not verify")
	}
}

func TestReadHashIsUnambiguousAndOrderInsensitiveOnFields(t *testing.T) {
	a := read("w-1", "x|y")
	b := read("w-1", "x")
	b.UserID = "y|user-1"
	if domain.ReadHash("", a) == domain.ReadHash("", b) {
		t.Fatal("values must not be able to shift across a separator")
	}
	swapped := read("w-1", "x")
	swapped.Fields = []string{"enrollments.optionCode", "lifeEvents.kind"}
	if domain.ReadHash("", read("w-1", "x")) != domain.ReadHash("", swapped) {
		t.Fatal("the field list is a set: its order must not change the hash")
	}
}

func TestReadHashSurvivesMicrosecondStorage(t *testing.T) {
	r := read("w-1", "x")
	r.OccurredAt = time.Date(2026, 11, 11, 12, 0, 0, 123456789, time.UTC)
	stored := r
	stored.OccurredAt = r.OccurredAt.Truncate(time.Microsecond)
	if domain.ReadHash("", r) != domain.ReadHash("", stored) {
		t.Fatal("a row read back from Postgres (microseconds) must hash to the value it was linked with")
	}
}

func TestValidateReadRequiresPurposeAndBoundsInput(t *testing.T) {
	cases := map[string]func(*domain.Read){
		"purpose":    func(r *domain.Read) { r.Purpose = "  " },
		"fields":     func(r *domain.Read) { r.Fields = nil },
		"entity":     func(r *domain.Read) { r.Entity = "" },
		"recordId":   func(r *domain.Read) { r.RecordID = strings.Repeat("r", 129) },
		"userId":     func(r *domain.Read) { r.UserID = "" },
		"traceId":    func(r *domain.Read) { r.TraceID = "" },
		"occurredAt": func(r *domain.Read) { r.OccurredAt = readNow.Add(time.Hour) },
	}
	for field, mutate := range cases {
		r := read("w-1", "ok")
		mutate(&r)
		if got := domain.ValidateRead(r, readNow); got[field] == "" {
			t.Errorf("%s: no problem reported: %v", field, got)
		}
	}
	long := read("w-1", strings.Repeat("p", domain.MaxReadPurpose+1))
	if domain.ValidateRead(long, readNow)["purpose"] == "" {
		t.Error("a purpose over the limit must be refused")
	}
	if got := domain.ValidateRead(read("w-1", strings.Repeat("p", domain.MaxReadPurpose)), readNow); len(got) != 0 {
		t.Errorf("a purpose at the limit is valid: %v", got)
	}
	if _, err := domain.LinkRead("", domain.Read{}, readNow); err == nil {
		t.Error("an invalid read must not link")
	}
}
