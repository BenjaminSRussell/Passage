package domain

import "testing"

func TestCheckImage(t *testing.T) {
	for name, c := range map[string]struct {
		kind    CaptureKind
		ctype   string
		w, h    int
		size    int
		wantErr bool
	}{
		"photo at the limit":      {CapturePhoto, "image/jpeg", 1600, 1200, 1000, false},
		"photo png":               {CapturePhoto, "image/png", 10, 10, 100, false},
		"photo long edge too big": {CapturePhoto, "image/jpeg", 1601, 100, 1000, true},
		"photo tall too big":      {CapturePhoto, "image/jpeg", 100, 1601, 1000, true},
		"photo gif":               {CapturePhoto, "image/gif", 10, 10, 100, true},
		"signature png":           {CaptureSignature, "image/png", 800, 300, 5000, false},
		"signature jpeg":          {CaptureSignature, "image/jpeg", 800, 300, 5000, true},
		"signature wide":          {CaptureSignature, "image/png", 2001, 300, 5000, true},
		"too many bytes":          {CapturePhoto, "image/png", 10, 10, MaxCaptureBytes + 1, true},
		"empty":                   {CapturePhoto, "image/png", 10, 10, 0, true},
		"no pixels":               {CapturePhoto, "image/png", 0, 10, 100, true},
		"unknown kind":            {"VIDEO", "image/png", 10, 10, 100, true},
	} {
		if err := CheckImage(c.kind, c.ctype, c.w, c.h, c.size); (err != nil) != c.wantErr {
			t.Errorf("%s: err %v, want error %v", name, err, c.wantErr)
		}
	}
}

func TestChecklistOpenListsOnlyRequiredItemsNotDone(t *testing.T) {
	items := []ChecklistItem{{ID: "a", Text: "Lock out", Required: true}, {ID: "b", Text: "Photo"}, {ID: "c", Text: "Test run", Required: true}}
	if got := ChecklistOpen(items, map[string]bool{"a": true}); len(got) != 1 || got[0].ID != "c" {
		t.Fatalf("open %+v", got)
	}
	if got := ChecklistOpen(items, map[string]bool{"a": true, "c": true}); len(got) != 0 {
		t.Fatalf("open %+v", got)
	}
}

func TestCaptureRef(t *testing.T) {
	if id, ok := CaptureIDOf(CaptureRef("ABC-1")); !ok || id != "abc-1" {
		t.Fatalf("round trip %q %v", id, ok)
	}
	if _, ok := CaptureIDOf("s3://bucket/sig.png"); ok {
		t.Fatal("an object-store reference is not a capture")
	}
}
