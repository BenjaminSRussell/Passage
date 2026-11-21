package ledgerclient_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/depot/platform/ledgerclient"
)

func TestPostingKey(t *testing.T) {
	long := strings.Repeat("x", ledgerclient.MaxKeyLen)
	cases := []struct {
		name     string
		segments []string
		revision []string
		want     string
		wantErr  bool
	}{
		{"shape", []string{"receiving", "invoice", "abc", "post"}, nil, "post:receiving:invoice:abc:post", false},
		{"revision suffix", []string{"inventory", "receipt", "r1", "valuation"}, []string{"2", "line-1"}, "post:inventory:receipt:r1:valuation:2:line-1", false},
		{"empty service", []string{"", "invoice", "abc", "post"}, nil, "", true},
		{"empty document type", []string{"close", "", "abc", "post"}, nil, "", true},
		{"empty document id", []string{"close", "run", "", "post"}, nil, "", true},
		{"empty purpose", []string{"close", "run", "abc", ""}, nil, "", true},
		{"empty revision", []string{"close", "run", "abc", "post"}, []string{""}, "", true},
		{"colon inside a segment", []string{"close", "run", "a:b", "post"}, nil, "", true},
		{"space inside a segment", []string{"close", "run", "a b", "post"}, nil, "", true},
		{"newline inside a segment", []string{"close", "run", "a\nb", "post"}, nil, "", true},
		{"over the length limit", []string{"close", "run", long, "post"}, nil, "", true},
		{"exactly at the length limit", []string{"close", "run", strings.Repeat("x", ledgerclient.MaxKeyLen-len("post:close:run::post")), "post"}, nil, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ledgerclient.PostingKey(tc.segments[0], tc.segments[1], tc.segments[2], tc.segments[3], tc.revision...)
			if tc.wantErr {
				if !errors.Is(err, ledgerclient.ErrInvalidPostingKey) || got != "" {
					t.Fatalf("got %q, %v; want ErrInvalidPostingKey and no key", got, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.want != "" && got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			if !ledgerclient.ValidPostingKey(got) || len(got) > ledgerclient.MaxKeyLen {
				t.Fatalf("built key %q is not valid", got)
			}
		})
	}
}

func TestMustPostingKeyPanicsOnAnInvalidSegment(t *testing.T) {
	if got := ledgerclient.MustPostingKey("tax", "settlement", "s1", "post"); got != "post:tax:settlement:s1:post" {
		t.Fatalf("got %q", got)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic")
		}
	}()
	ledgerclient.MustPostingKey("tax", "settlement", "", "post")
}

func TestValidPostingKey(t *testing.T) {
	cases := map[string]bool{
		"post:receiving:invoice:abc:post":        true,
		"post:inventory:receipt:r1:val:2":        true,
		"inventory-postings:receipt:r1:1":        false,
		"productcost.activation:abc":             false,
		"post:receiving:invoice:abc":             false,
		"post:receiving::abc:post":               false,
		"k1":                                     false,
		"post:a:b:c:" + strings.Repeat("x", 130): false,
	}
	for key, want := range cases {
		if got := ledgerclient.ValidPostingKey(key); got != want {
			t.Errorf("ValidPostingKey(%q) = %v, want %v", key, got, want)
		}
	}
}
