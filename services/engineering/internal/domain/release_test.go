package domain

import "testing"

func TestReleaseLineMachine(t *testing.T) {
	line := ReleaseLine{Status: LinePending}
	if err := line.Send(); err != nil || line.Status != LineSent {
		t.Fatalf("pending -> sent = %+v, %v", line, err)
	}
	if err := line.Accept("bob", ""); err != nil || line.Status != LineAccepted || line.AcceptedBy != "bob" {
		t.Fatalf("sent -> accepted = %+v, %v", line, err)
	}
	again := ReleaseLine{Status: LineSent}
	if err := again.Reject("bob", "site mismatch"); err != nil || again.Status != LineRejected || again.Reason != "site mismatch" {
		t.Fatalf("sent -> rejected = %+v, %v", again, err)
	}
	failed := ReleaseLine{Status: LineSent}
	for i := 0; i < 4; i++ {
		if err := failed.FailAttempt(); err != nil {
			t.Fatalf("retry %d: %v", i, err)
		}
	}
	if err := failed.FailAttempt(); err != nil || failed.Status != LineFailed {
		t.Fatalf("5th retry should fail the line = %+v, %v", failed, err)
	}
	terminal := ReleaseLine{Status: LineRejected}
	if err := terminal.Accept("bob", ""); !HasCode(err, CodeReleaseState) {
		t.Fatalf("rejected is terminal = %v", err)
	}
}

func TestApplyReleasePolicy_AcceptanceSplit(t *testing.T) {
	policy := ReleasePolicy{
		Code: "RP-1", CopyBOM: true, ActivateBOM: true, CopyRoute: true, ActivateRoute: true,
		RequireAcceptance: map[string]bool{"USMF": true, "DEMF": false},
	}
	session := ReleaseSession{Source: ReleaseManual, CreatedBy: "rita"}
	lines := ApplyRelease(session, policy, "P", "V-02", []string{"DEMF", "USMF"})
	if len(lines) != 2 {
		t.Fatalf("lines = %d", len(lines))
	}
	byLE := map[string]ReleaseLine{}
	for _, l := range lines {
		byLE[l.LegalEntityID] = l
	}
	if byLE["DEMF"].Status != LineAccepted {
		t.Fatalf("DEMF should auto-accept, got %s", byLE["DEMF"].Status)
	}
	if byLE["USMF"].Status != LineSent {
		t.Fatalf("USMF should wait, got %s", byLE["USMF"].Status)
	}
}

func TestOwnedFieldRefusedOutsideEngineeringOrg(t *testing.T) {
	err := CheckOwnedField("BOM", "ENG1", "USMF")
	if !HasCode(err, CodeOwnedField) {
		t.Fatalf("got %v; want %s", err, CodeOwnedField)
	}
	if err := CheckOwnedField("BOM", "ENG1", "ENG1"); err != nil {
		t.Fatalf("same org = %v", err)
	}
}
