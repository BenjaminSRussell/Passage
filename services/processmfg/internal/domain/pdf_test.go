package domain

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestRenderTextPDF_DeterministicPagedAndWellFormed(t *testing.T) {
	lines := []string{"Batch (L-1) \\ ok", "Grün 中", strings.Repeat("x", 250)}
	for i := 0; i < 130; i++ {
		lines = append(lines, fmt.Sprint("line ", i))
	}
	a := RenderTextPDF("Batch record L-1", lines)
	if !bytes.Equal(a, RenderTextPDF("Batch record L-1", lines)) {
		t.Fatal("the same input rendered to different bytes")
	}
	s := string(a)
	if !strings.HasPrefix(s, "%PDF-1.4\n") || !strings.HasSuffix(s, "%%EOF\n") || !strings.Contains(s, "/Count 3") {
		t.Fatalf("header, trailer or page count wrong: %.200s", s)
	}
	// Parentheses and backslashes are escaped, Latin-1 is an octal escape and
	// anything beyond is '?'; the 250-character line is wrapped.
	if !strings.Contains(s, `(Batch \(L-1\) \\ ok)`) || !strings.Contains(s, `(Gr\374n ?)`) || strings.Contains(s, strings.Repeat("x", 111)) {
		t.Fatal("escaping or wrapping wrong")
	}
	// Every xref entry points at its object.
	m := regexp.MustCompile(`startxref\n(\d+)`).FindStringSubmatch(s)
	start, _ := strconv.Atoi(m[1])
	entries := strings.Split(strings.TrimSpace(s[start:strings.Index(s, "trailer")]), "\n")[3:]
	for i, e := range entries {
		off, _ := strconv.Atoi(e[:10])
		if !strings.HasPrefix(s[off:], fmt.Sprintf("%d 0 obj", i+1)) {
			t.Fatalf("xref entry %d points at %q", i+1, s[off:off+10])
		}
	}
}
