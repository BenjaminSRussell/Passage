package domain

import (
	"errors"
	"testing"
)

func TestNestContainersRules(t *testing.T) {
	ok := NestChild{SSCC: "1", Status: "CLOSED"}
	cases := []struct {
		name     string
		from     ShipmentStatus
		children []NestChild
		want     error
	}{
		{"cartons on a packed shipment", StatusPacked, []NestChild{ok, ok}, nil},
		{"picked shipment", StatusPicked, []NestChild{ok}, nil},
		{"no children", StatusPacked, nil, ErrNesting},
		{"shipped shipment", StatusShipped, []NestChild{ok}, ErrInvalidMove},
		{"shipped carton", StatusPacked, []NestChild{{Status: "SHIPPED"}}, ErrNesting},
		{"carton already on a pallet", StatusPacked, []NestChild{{Status: "CLOSED", OnPallet: true}}, ErrNesting},
		{"pallet on pallet", StatusPacked, []NestChild{{Status: "CLOSED", IsPallet: true}}, ErrNesting},
	}
	for _, c := range cases {
		err := NestContainers(c.from, c.children)
		if c.want == nil && err != nil || c.want != nil && !errors.Is(err, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, err, c.want)
		}
	}
}

func TestUnnestAndUnpackNested(t *testing.T) {
	if err := UnnestPallet(StatusPacked, "CLOSED", 2); err != nil {
		t.Fatalf("dissolve a pallet: %v", err)
	}
	if err := UnnestPallet(StatusPacked, "CLOSED", 0); !errors.Is(err, ErrNesting) {
		t.Errorf("carton is not a pallet: %v", err)
	}
	if err := UnnestPallet(StatusPacked, "SHIPPED", 2); !errors.Is(err, ErrInvalidMove) {
		t.Errorf("shipped pallet: %v", err)
	}
	if err := UnnestPallet(StatusShipped, "CLOSED", 2); !errors.Is(err, ErrInvalidMove) {
		t.Errorf("shipped shipment: %v", err)
	}
	if !errors.Is(UnpackNested(true, 0), ErrNesting) || !errors.Is(UnpackNested(false, 2), ErrNesting) || UnpackNested(false, 0) != nil {
		t.Error("unpack rule for nested containers")
	}
}
