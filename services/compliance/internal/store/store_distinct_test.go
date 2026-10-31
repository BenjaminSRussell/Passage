package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/compliance/internal/domain"
	cevents "github.com/BenjaminSRussell/passage/services/compliance/internal/events"
)

// AC2 end to end on fx-sod-50-distinct: the 50 distinct conflicting role
// combinations are evaluated and stored (81 OPEN conflicts, one
// conflict.detected each); each conflict's user is refused as its own
// approver; a different approver then mitigates every conflict, and each
// approval is recorded on the conflict with its control, reason and approver,
// with one mitigation.approved event per conflict.
func TestMitigate_FiftyDistinctCombinationsRecordEveryApproval(t *testing.T) {
	s, admin := migrated(t)
	raw, err := os.ReadFile("../../testdata/sod-50-distinct.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx fixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	tenantID, approver := ids.New(), ids.New()
	type stored struct{ id, user, rule string }
	var all []stored
	for i, u := range fx.Users {
		userID := fmt.Sprintf("00000000-0000-0000-0000-%012d", i+1)
		var rs []domain.Role
		for _, name := range u.Roles {
			rs = append(rs, domain.Role{Name: name, Duties: fx.Roles[name]})
		}
		got, err := s.Evaluate(as(tenantID, "sod-batch"), userID, rs, t0)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(u.Expect) {
			t.Fatalf("%s: stored %d conflicts, want %v", u.UserID, len(got), u.Expect)
		}
		for j, c := range got {
			if c.RuleID != u.Expect[j] || c.State != domain.StateOpen {
				t.Errorf("%s: stored %s %s, want OPEN %s", u.UserID, c.RuleID, c.State, u.Expect[j])
			}
			all = append(all, stored{c.ConflictID, userID, c.RuleID})
		}
	}
	if len(all) != 81 {
		t.Fatalf("stored %d conflicts, want 81", len(all))
	}
	count := func(typ string) int {
		var n int
		if err := admin.QueryRow(context.Background(), `select count(*) from outbox where tenant_id = $1 and type = $2`, tenantID, typ).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(cevents.ConflictDetected); n != 81 {
		t.Fatalf("conflict.detected events %d, want 81", n)
	}
	for i, c := range all {
		in := MitigationInput{ControlRef: "CTL-" + c.rule, Reason: "compensating review of " + c.rule + " for this user", ExpiresAt: t0.AddDate(0, 0, 90)}
		if _, err := s.Mitigate(as(tenantID, c.user), c.id, 1, in, 365, fmt.Sprintf("self-%d", i), t0); code(err) != domain.CodeSelfApproval {
			t.Fatalf("%s %s: self approval not refused: %v", c.user, c.rule, err)
		}
		got, err := s.Mitigate(as(tenantID, approver), c.id, 1, in, 365, fmt.Sprintf("k-%d", i), t0)
		if err != nil {
			t.Fatalf("%s %s: %v", c.user, c.rule, err)
		}
		if got.State != domain.StateMitigated || len(got.Mitigations) != 1 {
			t.Fatalf("%s %s: not mitigated with one approval: %+v", c.user, c.rule, got)
		}
		m := got.Mitigations[0]
		if m.ApproverID != approver || m.ControlRef != in.ControlRef || m.Reason != in.Reason || !m.ExpiresAt.Equal(in.ExpiresAt) {
			t.Errorf("%s %s: approval recorded as %+v", c.user, c.rule, m)
		}
	}
	if n := count(cevents.MitigationApproved); n != 81 {
		t.Fatalf("mitigation.approved events %d, want 81", n)
	}
	mitigated, cursor := 0, ""
	for {
		page, err := s.List(as(tenantID, "x"), Filter{State: domain.StateMitigated, Cursor: cursor}, t0)
		if err != nil {
			t.Fatal(err)
		}
		mitigated += len(page.Items)
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if mitigated != 81 {
		t.Fatalf("MITIGATED conflicts %d, want 81", mitigated)
	}
}
