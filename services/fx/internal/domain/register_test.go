package domain

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestNotImplementedRegisterNamesEveryDeliverable(t *testing.T) {
	b, err := os.ReadFile("../../NOT-IMPLEMENTED.md")
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 6; i++ {
		if !strings.Contains(string(b), fmt.Sprintf("Deliverable %d", i)) {
			t.Errorf("register has no row for deliverable %d", i)
		}
	}
}
