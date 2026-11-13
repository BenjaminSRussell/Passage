package target

import (
	"net/url"
	"strings"
	"testing"
)

// Keys are sent at most 500 to a request and within the encoded size budget, in order and none lost.
func TestXrefChunks_BoundCountAndSize(t *testing.T) {
	var keys []string
	for i := 0; i < 1200; i++ {
		keys = append(keys, "USMF|C-"+strings.Repeat("9", i%40))
	}
	long := strings.Repeat("x", 255)
	keys = append(keys, long, long, long)
	var flat []string
	for _, c := range xrefChunks(keys) {
		if len(c) == 0 || len(c) > maxXrefKeysAsked {
			t.Fatalf("chunk of %d keys", len(c))
		}
		size := 0
		for _, k := range c {
			size += len(url.QueryEscape(k)) + 3
		}
		if size > maxXrefQueryBytes {
			t.Fatalf("chunk of %d bytes", size)
		}
		flat = append(flat, c...)
	}
	if strings.Join(flat, ",") != strings.Join(keys, ",") {
		t.Fatal("keys lost or reordered")
	}
	if len(xrefChunks(nil)) != 0 {
		t.Fatal("no keys, no request")
	}
}
