package domain

import (
	"errors"
	"strings"
	"testing"
)

func catDay(t *testing.T, s string) Day {
	t.Helper()
	d, err := ParseDay(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func assignment(t *testing.T, category, vendor, status, from, to string) CategoryVendor {
	t.Helper()
	a := CategoryVendor{Category: category, VendorID: vendor, Status: status, ValidFrom: catDay(t, from)}
	if to != "" {
		d := catDay(t, to)
		a.ValidTo = &d
	}
	return a
}

func TestCategoryVendor_ValidOnIncludesBothEnds(t *testing.T) {
	a := assignment(t, "IT", "v1", AssignmentApproved, "2026-03-01", "2026-03-31")
	for day, want := range map[string]bool{"2026-02-28": false, "2026-03-01": true, "2026-03-31": true, "2026-04-01": false} {
		if got := a.ValidOn(catDay(t, day)); got != want {
			t.Errorf("ValidOn(%s) = %v, want %v", day, got, want)
		}
	}
	open := assignment(t, "IT", "v1", AssignmentApproved, "2026-03-01", "")
	if !open.ValidOn(catDay(t, "2099-01-01")) {
		t.Error("an open window holds every later day")
	}
}

func TestCheckCategoryAccess_Table(t *testing.T) {
	on := catDay(t, "2026-03-10")
	assigned := []CategoryVendor{
		assignment(t, "IT", "good", AssignmentApproved, "2026-01-01", ""),
		assignment(t, "IT", "prov", AssignmentProvisional, "2026-01-01", ""),
		assignment(t, "IT", "blocked", AssignmentBlocked, "2026-01-01", ""),
		assignment(t, "IT", "expired", AssignmentApproved, "2025-01-01", "2026-03-09"),
		assignment(t, "IT", "future", AssignmentApproved, "2026-03-11", ""),
	}
	use := func(category, vendor string) []AccessUse {
		return []AccessUse{{LineNo: 1, Ref: "requisition PR-1 line 1", Category: category, VendorID: vendor}}
	}
	for _, tc := range []struct {
		name, mode, category, vendor string
		wantErr                      bool
		wantCodes                    []string
	}{
		{"assigned vendor passes", AccessHard, "IT", "good", false, nil},
		{"unassigned vendor refused in HARD", AccessHard, "IT", "other", true, nil},
		{"blocked vendor refused in HARD", AccessHard, "IT", "blocked", true, nil},
		{"expired window refused in HARD", AccessHard, "IT", "expired", true, nil},
		{"window not yet open refused in HARD", AccessHard, "IT", "future", true, nil},
		{"provisional passes with a warning in HARD", AccessHard, "IT", "prov", false, []string{CodeCategoryProvisional}},
		{"unassigned vendor warns in SOFT", AccessSoft, "IT", "other", false, []string{CodeCategoryAccess}},
		{"empty mode is SOFT", "", "IT", "other", false, []string{CodeCategoryAccess}},
		{"OFF checks nothing", AccessOff, "IT", "blocked", false, nil},
		{"a category with no assignments is open", AccessHard, "MRO", "other", false, nil},
		{"a line without a vendor is not checked", AccessHard, "IT", "", false, nil},
		{"a line without a category is not checked", AccessHard, "", "other", false, nil},
	} {
		got, err := CheckCategoryAccess(tc.mode, assigned, use(tc.category, tc.vendor), on)
		var de *Error
		if tc.wantErr != errors.As(err, &de) || (de != nil && de.Code != CodeCategoryAccess) {
			t.Errorf("%s: error = %v, want error %v", tc.name, err, tc.wantErr)
			continue
		}
		var codes []string
		for _, f := range got {
			codes = append(codes, f.Code)
		}
		if strings.Join(codes, ",") != strings.Join(tc.wantCodes, ",") {
			t.Errorf("%s: findings %v, want %v", tc.name, codes, tc.wantCodes)
		}
	}
}

func TestCheckCategoryAccess_HardNamesTheFirstLineAndCountsTheRest(t *testing.T) {
	assigned := []CategoryVendor{assignment(t, "IT", "good", AssignmentApproved, "2026-01-01", "")}
	uses := []AccessUse{
		{LineNo: 3, Ref: "requisition PR-1 line 3", Category: "IT", VendorID: "bad2"},
		{LineNo: 1, Ref: "requisition PR-1 line 1", Category: "IT", VendorID: "bad1"},
		{LineNo: 2, Ref: "requisition PR-1 line 2", Category: "IT", VendorID: "good"},
	}
	_, err := CheckCategoryAccess(AccessHard, assigned, uses, catDay(t, "2026-03-10"))
	var de *Error
	if !errors.As(err, &de) || de.Code != CodeCategoryAccess {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(de.Detail, "line 1") || !strings.Contains(de.Detail, "bad1") || !strings.Contains(de.Detail, "and 1 more") {
		t.Errorf("detail = %q", de.Detail)
	}
}

func TestAccessModeAndStatusValidation(t *testing.T) {
	for _, m := range []string{"HARD", "SOFT", "OFF"} {
		if !ValidAccessMode(m) {
			t.Errorf("%s should be valid", m)
		}
	}
	if ValidAccessMode("hard") || ValidAccessMode("") || ValidAssignmentStatus("OTHER") || !ValidAssignmentStatus(AssignmentBlocked) {
		t.Error("validation is exact and case sensitive")
	}
}

// A BLOCKED row blocks its vendor only while its window holds the day, and never makes
// the category restricted for other vendors.
func TestCheckCategoryAccess_BlockedWindowAndOpenCategories(t *testing.T) {
	june := []CategoryVendor{assignment(t, "MRO", "bad", AssignmentBlocked, "2026-06-01", "2026-06-30")}
	restrictedJune := append([]CategoryVendor{assignment(t, "MRO", "good", AssignmentApproved, "2025-01-01", "")}, june...)
	lapsed := []CategoryVendor{assignment(t, "MRO", "old", AssignmentApproved, "2025-01-01", "2025-12-31")}
	for _, tc := range []struct {
		name     string
		rows     []CategoryVendor
		vendor   string
		on       string
		wantFail bool
	}{
		{"blocked inside its window, open category", june, "bad", "2026-06-15", true},
		{"blocked before its window, open category", june, "bad", "2026-05-31", false},
		{"blocked after its window, open category", june, "bad", "2026-07-01", false},
		{"another vendor of a category with only a BLOCKED row", june, "other", "2026-06-15", false},
		{"blocked inside its window, restricted category", restrictedJune, "bad", "2026-06-15", true},
		{"blocked after its window, restricted category: still unassigned", restrictedJune, "bad", "2026-07-01", true},
		{"approved vendor of the restricted category", restrictedJune, "good", "2026-06-15", false},
		{"all approved rows expired: the category stays restricted", lapsed, "other", "2026-06-15", true},
	} {
		_, err := CheckCategoryAccess(AccessHard, tc.rows, []AccessUse{{LineNo: 1, Ref: "PO-1 line 1", Category: "MRO", VendorID: tc.vendor}}, catDay(t, tc.on))
		if (err != nil) != tc.wantFail {
			t.Errorf("%s: err = %v, want failure %v", tc.name, err, tc.wantFail)
		}
		if err != nil && tc.vendor == "bad" && tc.on == "2026-06-15" && !strings.Contains(err.Error(), "is blocked for") {
			t.Errorf("%s: %v should say the vendor is blocked", tc.name, err)
		}
	}
}
