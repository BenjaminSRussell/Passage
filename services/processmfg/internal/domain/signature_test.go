package domain

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

// AC-24.15: three failed re-authentications lock signing for 15 minutes.
func TestLockout_AC2415(t *testing.T) {
	var l Lockout
	now := time.Date(2027, 1, 1, 9, 0, 0, 0, time.UTC)
	l.Fail(now)
	l.Fail(now)
	if l.Locked(now) {
		t.Fatal("two failures must not lock")
	}
	l.Fail(now)
	if !l.Locked(now.Add(14 * time.Minute)) {
		t.Fatal("locked for the first 15 minutes")
	}
	if l.Locked(now.Add(15 * time.Minute)) {
		t.Fatal("lock must expire after 15 minutes")
	}
	if codeOf(l.CheckAttempt(now)) != CodeSignatureLocked {
		t.Fatal("attempt while locked must return PMF-0011")
	}
}

func TestLockout_SuccessResetsCount(t *testing.T) {
	var l Lockout
	now := time.Now()
	l.Fail(now)
	l.Fail(now)
	l.Succeed()
	l.Fail(now)
	if l.Locked(now) {
		t.Fatal("success must reset the failure count")
	}
}

func TestRequiredAuth_BR2414(t *testing.T) {
	if RequiredAuth(true) != AuthPasswordAndOTP || RequiredAuth(false) != AuthPassword {
		t.Fatal("first signing needs two factors, later signings the password")
	}
}

func TestContinuousSession(t *testing.T) {
	last := time.Date(2027, 1, 1, 9, 0, 0, 0, time.UTC)
	if !ContinuousSession(last, last.Add(10*time.Minute), false) {
		t.Fatal("10 minutes idle is still continuous")
	}
	if ContinuousSession(last, last.Add(11*time.Minute), false) {
		t.Fatal("more than 10 minutes idle ends the session")
	}
	if ContinuousSession(last, last, true) {
		t.Fatal("logout ends the session")
	}
}

func TestSignerSubjectAndDistinctAuthor(t *testing.T) {
	if codeOf(CheckSignerSubject("alice", "bob")) != CodeSignatureRejected {
		t.Fatal("mismatched subject must be rejected")
	}
	if CheckSignerSubject("alice", "alice") != nil {
		t.Fatal("matching subject must pass")
	}
	if codeOf(CheckDistinctSigner("alice", "alice")) != CodeSameAuthor {
		t.Fatal("author cannot sign own record")
	}
}

// The hash is the same for equal records regardless of map construction order,
// and changes when any field changes.
func TestRecordHash_Canonical(t *testing.T) {
	a := map[string]any{"batch": "B-77", "qty": 10, "item": map[string]any{"x": 1, "y": 2}}
	b := map[string]any{"item": map[string]any{"y": 2, "x": 1}, "qty": 10, "batch": "B-77"}
	ha, err := RecordHash(a)
	if err != nil {
		t.Fatal(err)
	}
	hb, _ := RecordHash(b)
	if !bytes.Equal(ha, hb) || len(ha) != 32 {
		t.Fatal("canonical hash must be stable")
	}
	b["qty"] = 11
	hc, _ := RecordHash(b)
	if bytes.Equal(ha, hc) {
		t.Fatal("hash must change with the record")
	}
	if codeOf(CheckSignatureValid(ha, hc)) != CodeSignatureInvalid {
		t.Fatal("changed record must invalidate the signature")
	}
	if CheckSignatureValid(ha, ha) != nil {
		t.Fatal("unchanged record keeps the signature valid")
	}
}

// Numbers keep every digit: two records that differ beyond float64 precision
// must not share a hash, or a changed record could keep a valid signature.
func TestRecordHash_KeepsNumberPrecision(t *testing.T) {
	a, err := RecordHash(map[string]any{"qty": json.Number("9007199254740993")})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := RecordHash(map[string]any{"qty": json.Number("9007199254740992")})
	if bytes.Equal(a, b) {
		t.Fatal("records that differ in the last digit hashed equally")
	}
}

func TestRecordHash_Unencodable(t *testing.T) {
	if _, err := RecordHash(make(chan int)); codeOf(err) != CodeInvalid {
		t.Fatalf("channel must not encode: %v", err)
	}
}

func TestCheckApprove_BR2415(t *testing.T) {
	cases := []struct {
		name string
		exc  []Exception
		ok   bool
	}{
		{"none", nil, true},
		{"dispositioned and signed", []Exception{{ID: "e1", Disposition: "accepted", SignatureID: "s1"}}, true},
		{"no disposition", []Exception{{ID: "e1"}}, false},
		{"disposition without signature", []Exception{{ID: "e1", Disposition: "rejected"}}, false},
		{"unknown disposition", []Exception{{ID: "e1", Disposition: "maybe", SignatureID: "s"}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := CheckApprove(c.exc)
			if c.ok && err != nil {
				t.Fatal(err)
			}
			if !c.ok && err == nil {
				t.Fatal("approval must be refused")
			}
		})
	}
	if codeOf(CheckApprove([]Exception{{ID: "e"}})) != CodeEBRExceptions {
		t.Fatal("open exceptions must return PMF-0014")
	}
}

func TestCheckRelease(t *testing.T) {
	if codeOf(CheckRelease(true, false, "B-1")) != CodeEBRNotApproved {
		t.Fatal("unapproved record must block release")
	}
	if CheckRelease(false, false, "B-1") != nil || CheckRelease(true, true, "B-1") != nil {
		t.Fatal("release allowed when not required or approved")
	}
}
