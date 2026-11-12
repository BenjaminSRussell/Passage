package domain

import "testing"

func TestIsBreakGlass(t *testing.T) {
	for purpose, want := range map[string]bool{
		"BREAK-GLASS":                      true,
		"break-glass: patient unconscious": true,
		"  Break-Glass ER admission":       true,
		"BREAK-GLASS:":                     true,
		"BREAK-GLASSES are not a reason":   false,
		"benefits enrollment, BREAK-GLASS": false,
		"":                                 false,
		"self-service":                     false,
	} {
		if got := IsBreakGlass(purpose); got != want {
			t.Errorf("IsBreakGlass(%q) = %v, want %v", purpose, got, want)
		}
	}
}

func TestValidateReview(t *testing.T) {
	if p := ValidateReview(ReviewApproved, ""); len(p) != 0 {
		t.Errorf("approved without a note: %v", p)
	}
	if p := ValidateReview(ReviewFlagged, "  "); p["note"] == "" {
		t.Errorf("flagged without a note must fail: %v", p)
	}
	if p := ValidateReview("OK", ""); p["outcome"] == "" {
		t.Errorf("unknown outcome must fail: %v", p)
	}
	long := make([]byte, MaxReviewNote+1)
	for i := range long {
		long[i] = 'x'
	}
	if p := ValidateReview(ReviewApproved, string(long)); p["note"] == "" {
		t.Errorf("over-long note must fail: %v", p)
	}
}

func TestReviewRefusal(t *testing.T) {
	cases := []struct {
		name, purpose, reader, reviewer string
		reviewed, refused               bool
	}{
		{"another person reviews a break-glass read", "BREAK-GLASS: ER", "u1", "u2", false, false},
		{"not break-glass", "benefits enrollment", "u1", "u2", false, true},
		{"already reviewed", "BREAK-GLASS", "u1", "u2", true, true},
		{"the reader reviews their own access", "BREAK-GLASS", "u1", "u1", false, true},
	}
	for _, c := range cases {
		if got := ReviewRefusal(c.purpose, c.reader, c.reviewer, c.reviewed) != ""; got != c.refused {
			t.Errorf("%s: refused = %v, want %v", c.name, got, c.refused)
		}
	}
}
