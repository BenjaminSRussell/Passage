package domain

import (
	"errors"
	"testing"

	"pgregory.net/rapid"
)

func codeOf(err error) string {
	var ce *CodedError
	if errors.As(err, &ce) {
		return ce.Code
	}
	return ""
}

func TestTransition_LifecycleTable(t *testing.T) {
	std := func(status string) Version {
		return Version{ID: "v", Type: VersionStandard, Status: status, Fallback: FallbackActive}
	}
	sim := Version{ID: "s", Type: VersionSimulation, Status: VersionOpen, Fallback: FallbackNone}
	cases := []struct {
		name   string
		from   Version
		action string
		want   string // resulting status, or "" when rejected
		code   string
	}{
		{"open blocks", std(VersionOpen), ActionBlock, VersionBlocked, ""},
		{"blocked unblocks", std(VersionBlocked), ActionUnblock, VersionOpen, ""},
		{"open activates", std(VersionOpen), ActionActivate, VersionActive, ""},
		{"blocked activates", std(VersionBlocked), ActionActivate, VersionActive, ""},
		{"active closes", std(VersionActive), ActionClose, VersionClosed, ""},
		{"open cannot close", std(VersionOpen), ActionClose, "", CodeInvalidTransition},
		{"active cannot block", std(VersionActive), ActionBlock, "", CodeInvalidTransition},
		{"closed cannot activate", std(VersionClosed), ActionActivate, "", CodeInvalidTransition},
		{"active cannot activate twice", std(VersionActive), ActionActivate, "", CodeInvalidTransition},
		{"simulation cannot activate", sim, ActionActivate, "", CodeSimulationActivation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Transition(tc.from, tc.action)
			if tc.code != "" {
				if codeOf(err) != tc.code {
					t.Fatalf("err %v, want code %s", err, tc.code)
				}
				if got.Status != tc.from.Status {
					t.Errorf("rejected transition changed status to %s", got.Status)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error %v", err)
			}
			if got.Status != tc.want {
				t.Errorf("status = %s, want %s", got.Status, tc.want)
			}
		})
	}
}

func TestTransition_UnknownActionIsAnError(t *testing.T) {
	if _, err := Transition(Version{Type: VersionStandard, Status: VersionOpen}, "explode"); err == nil {
		t.Fatal("unknown action accepted")
	}
}

func TestVersion_ValidateFallbackAndEnums(t *testing.T) {
	cases := []struct {
		name string
		v    Version
		code string
		bad  bool
	}{
		{"specified fallback needs a version", Version{ID: "a", Type: VersionStandard, Status: VersionOpen, Fallback: FallbackSpecified}, CodeFallbackVersion, true},
		{"fallback cannot point to itself", Version{ID: "a", Type: VersionStandard, Status: VersionOpen, Fallback: FallbackSpecified, FallbackVersionID: "a"}, CodeFallbackVersion, true},
		{"specified fallback with a version is fine", Version{ID: "a", Type: VersionStandard, Status: VersionOpen, Fallback: FallbackSpecified, FallbackVersionID: "b"}, "", false},
		{"fallback None cannot name a version", Version{ID: "a", Type: VersionStandard, Status: VersionOpen, Fallback: FallbackNone, FallbackVersionID: "b"}, "", true},
		{"unknown type", Version{ID: "a", Type: "Budget", Status: VersionOpen, Fallback: FallbackNone}, "", true},
		{"unknown status", Version{ID: "a", Type: VersionStandard, Status: "Frozen", Fallback: FallbackNone}, "", true},
		{"simulation may not be Active", Version{ID: "a", Type: VersionSimulation, Status: VersionActive, Fallback: FallbackNone}, CodeSimulationActivation, true},
		{"conversion version may be Active", Version{ID: "a", Type: VersionConversion, Status: VersionActive, Fallback: FallbackActive}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.v.Validate()
			if (err != nil) != tc.bad {
				t.Fatalf("err = %v, want bad=%v", err, tc.bad)
			}
			if tc.code != "" && codeOf(err) != tc.code {
				t.Errorf("code = %q, want %s (err %v)", codeOf(err), tc.code, err)
			}
		})
	}
}

// TestTransition_PropertyNeverActivatesSimulation drives random action
// sequences: a Simulation version is never Active, and Closed is terminal.
func TestTransition_PropertyNeverActivatesSimulation(t *testing.T) {
	actions := []string{ActionBlock, ActionUnblock, ActionActivate, ActionClose}
	rapid.Check(t, func(rt *rapid.T) {
		kind := rapid.SampledFrom([]string{VersionStandard, VersionSimulation}).Draw(rt, "type")
		v := Version{ID: "p", Type: kind, Status: VersionOpen, Fallback: FallbackNone}
		steps := rapid.IntRange(0, 25).Draw(rt, "steps")
		for i := 0; i < steps; i++ {
			action := rapid.SampledFrom(actions).Draw(rt, "action")
			next, err := Transition(v, action)
			if err != nil {
				if next.Status != v.Status {
					rt.Fatalf("failed %s changed status", action)
				}
				continue
			}
			if v.Status == VersionClosed {
				rt.Fatalf("closed version moved by %s", action)
			}
			v = next
			if v.Type == VersionSimulation && v.Status == VersionActive {
				rt.Fatalf("simulation became Active")
			}
		}
	})
}
