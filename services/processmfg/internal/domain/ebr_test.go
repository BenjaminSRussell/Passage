package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestValidExceptionDispositionAndSeverity(t *testing.T) {
	for _, s := range []string{"accepted", "deviation-raised", "rejected"} {
		if !ValidExceptionDisposition(s) {
			t.Errorf("%s should be a disposition", s)
		}
	}
	for _, s := range []string{"", "ignored", "Accepted"} {
		if ValidExceptionDisposition(s) {
			t.Errorf("%q should not be a disposition", s)
		}
	}
	for _, s := range []string{"minor", "major", "critical"} {
		if !ValidSeverity(s) {
			t.Errorf("%s should be a severity", s)
		}
	}
	if ValidSeverity("") || ValidSeverity("huge") {
		t.Error("unknown severity accepted")
	}
}

func TestExceptionDescription(t *testing.T) {
	if got := ExceptionDescription("potency", "70 is above 60"); got != "potency outside its limits: 70 is above 60" {
		t.Fatalf("got %q", got)
	}
	if got := ExceptionDescription("potency", ""); got != "potency outside its limits" {
		t.Fatalf("got %q", got)
	}
	long := ExceptionDescription("é", strings.Repeat("é", 600))
	if utf8.RuneCountInString(long) != 500 || !utf8.ValidString(long) {
		t.Fatalf("not cut to 500 characters: %d", utf8.RuneCountInString(long))
	}
}

// The canonical document sorts keys and keeps every digit, and the record hash is
// the SHA-256 of exactly that document, so a stored document can be re-hashed.
func TestCanonicalJSON_SortedAndHashed(t *testing.T) {
	in := map[string]any{"b": 1, "a": map[string]any{"z": "x", "y": 12345678901234567890.0}, "c": []int{3, 1}}
	canon, err := CanonicalJSON(in)
	if err != nil {
		t.Fatal(err)
	}
	if string(canon) != `{"a":{"y":12345678901234567000,"z":"x"},"b":1,"c":[3,1]}` {
		t.Fatalf("got %s", canon)
	}
	h, err := RecordHash(in)
	want := sha256.Sum256(canon)
	if err != nil || string(h) != string(want[:]) {
		t.Fatalf("hash mismatch err=%v", err)
	}
	if _, err := CanonicalJSON(make(chan int)); err == nil {
		t.Fatal("unencodable value accepted")
	}
}

// The sealed document must be reproducible outside Go: this vector is what a
// plain sorted-keys, compact, UTF-8 encoder produces (Python's json.dumps with
// sort_keys=True, separators=(",", ":"), ensure_ascii=False), with no HTML
// escaping and the number kept as written.
func TestCanonicalJSON_MatchesAnIndependentEncoder(t *testing.T) {
	in := map[string]any{"b": "<&>", "a": "é", "n": json.Number("1.50")}
	canon, err := CanonicalJSON(in)
	if err != nil {
		t.Fatal(err)
	}
	if string(canon) != `{"a":"é","b":"<&>","n":1.50}` {
		t.Fatalf("got %s", canon)
	}
	sum := sha256.Sum256(canon)
	if got := hex.EncodeToString(sum[:]); got != "303a144c5d68334d52ba6fb3b6add6005a9674012cd28e1840a5913d6072d7e5" {
		t.Fatalf("hash %s", got)
	}
	// Built in another key order, from a struct, the bytes are the same.
	type doc struct {
		N json.Number `json:"n"`
		B string      `json:"b"`
		A string      `json:"a"`
	}
	again, err := CanonicalJSON(doc{N: "1.50", B: "<&>", A: "é"})
	if err != nil || string(again) != string(canon) {
		t.Fatalf("struct order changed the document: %s %v", again, err)
	}
}

func TestCheckApproverIndependent(t *testing.T) {
	tests := []struct {
		name, approver, creator string
		reviewers               []string
		refused                 bool
	}{
		{"independent QA", "qa-1", "planner-1", []string{"rev-1", "rev-2"}, false},
		{"no reviewers and no creator on record", "qa-1", "", nil, false},
		{"approver decided an exception", "rev-2", "planner-1", []string{"rev-1", "rev-2"}, true},
		{"approver created the batch order", "planner-1", "planner-1", nil, true},
		{"anonymous approver", "", "planner-1", nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckApproverIndependent(tc.approver, tc.reviewers, tc.creator)
			if tc.refused != (codeOf(err) == CodeEBRSegregation) || (!tc.refused && err != nil) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}
