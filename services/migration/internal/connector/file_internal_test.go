package connector

import (
	"context"
	"testing"
)

func TestFile_ParsesOncePerExtraction(t *testing.T) {
	f, err := newFile(Env{}, Spec{Profile: map[string]any{"format": "csv"}, Source: map[string]any{"file": "a.csv"},
		Upload: &Upload{Name: "a.csv", Content: []byte("id\n1\n2\n3\n4\n5\n")}})
	if err != nil {
		t.Fatal(err)
	}
	load := f.load
	loads := 0
	f.load = func() ([]byte, string, error) { loads++; return load() }
	var got int
	q := Query{Limit: 2}
	for {
		p, err := f.Read(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
		got += len(p.Rows)
		if p.Next == "" {
			break
		}
		q.Position = p.Next
	}
	if got != 5 || loads != 1 {
		t.Fatalf("%d rows over %d loads", got, loads)
	}
}
