package domain

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// TestChildLocation_DepthLimit covers FR-44.1: roots are depth 1, the path joins
// codes with slashes, and depth 13 is refused with EAM-FL-001.
func TestChildLocation_DepthLimit(t *testing.T) {
	cases := []struct {
		name        string
		parentDepth int
		parentPath  string
		code        string
		wantDepth   int
		wantPath    string
		wantCode    string
	}{
		{"root", 0, "", "PLANT-1", 1, "PLANT-1", ""},
		{"child", 1, "PLANT-1", "LINE-2", 2, "PLANT-1/LINE-2", ""},
		{"depth 12 allowed", 11, "A/B/C/D/E/F/G/H/I/J/K", "L", 12, "A/B/C/D/E/F/G/H/I/J/K/L", ""},
		{"depth 13 refused", 12, "A/B/C/D/E/F/G/H/I/J/K/L", "M", 0, "", CodeLocationDepth},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			depth, path, err := ChildLocation(tc.parentDepth, tc.parentPath, tc.code)
			if tc.wantCode != "" {
				var ge GateError
				if !errors.As(err, &ge) || ge.Code != tc.wantCode {
					t.Fatalf("err = %v, want %s", err, tc.wantCode)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if depth != tc.wantDepth || path != tc.wantPath {
				t.Fatalf("got depth %d path %q, want %d %q", depth, path, tc.wantDepth, tc.wantPath)
			}
		})
	}
}

// TestLocationChange_Kinds covers the install, move and remove classification and
// refuses a no-op change.
func TestLocationChange_Kinds(t *testing.T) {
	cases := []struct {
		name, current, next string
		want                AssetMove
		wantCode            string
	}{
		{"install", "", "loc-1", MoveInstall, ""},
		{"move", "loc-1", "loc-2", MoveMove, ""},
		{"remove", "loc-1", "", MoveRemove, ""},
		{"no-op", "loc-1", "loc-1", "", CodeLocationRule},
		{"no-op empty", "", "", "", CodeLocationRule},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := LocationChange(tc.current, tc.next)
			if tc.wantCode != "" {
				var ge GateError
				if !errors.As(err, &ge) || ge.Code != tc.wantCode {
					t.Fatalf("err = %v, want %s", err, tc.wantCode)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

// TestChildLocation_PathDepthProperty checks that the path of a chain of any
// length has exactly as many segments as its depth, and that depth never exceeds
// the maximum (FR-44.1).
func TestChildLocation_PathDepthProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(1, 15).Draw(rt, "chain")
		depth, path := 0, ""
		for i := 0; i < n; i++ {
			code := fmt.Sprintf("C%d", i)
			d, p, err := ChildLocation(depth, path, code)
			if err != nil {
				if d != 0 || i < MaxLocationDepth {
					rt.Fatalf("refused at level %d: %v", i+1, err)
				}
				if PathDepth(path) != MaxLocationDepth {
					rt.Fatalf("refusal with path depth %d", PathDepth(path))
				}
				return
			}
			depth, path = d, p
			if PathDepth(path) != depth || !strings.HasSuffix(path, code) {
				rt.Fatalf("path %q does not match depth %d", path, depth)
			}
		}
		if n > MaxLocationDepth {
			rt.Fatalf("chain of %d accepted", n)
		}
	})
}
