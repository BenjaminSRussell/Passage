package stack

import "testing"

// Ports are picked while the earlier picks are still held, so no two addresses of one stack can collide.
func TestNewHandsOutDistinctPorts(t *testing.T) {
	for range 20 {
		s, err := New(Config{Root: t.TempDir(), AdminDB: "postgres://unused"})
		if err != nil {
			t.Fatal(err)
		}
		seen := map[int]string{}
		for name, p := range s.ports {
			if other, dup := seen[p]; dup {
				t.Fatalf("port %d given to both %s and %s", p, other, name)
			}
			seen[p] = name
		}
		for name, p := range s.admin {
			if other, dup := seen[p]; dup {
				t.Fatalf("port %d given to both %s and %s admin", p, other, name)
			}
			seen[p] = name + " admin"
		}
	}
}
