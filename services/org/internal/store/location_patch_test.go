package store

import (
	"reflect"
	"testing"
)

func TestLocationPatchApply(t *testing.T) {
	address := "00000000-0000-0000-0004-000000000001"
	current := Location{Name: "One", Type: "storage", Status: "active", AddressID: &address}
	other, empty, name := "00000000-0000-0000-0004-000000000002", "", "Two"
	for _, tc := range []struct {
		name        string
		patch       LocationPatch
		wantChanged []string
		wantAddress *string
	}{
		{"nil keeps everything", LocationPatch{}, nil, &address},
		{"same values change nothing", LocationPatch{Name: &current.Name, AddressID: &address}, nil, &address},
		{"empty clears the address", LocationPatch{AddressID: &empty}, []string{"address_id"}, nil},
		{"uuid sets the address", LocationPatch{AddressID: &other}, []string{"address_id"}, &other},
		{"fields list in apply order", LocationPatch{Name: &name, Status: &empty, Type: &empty, AddressID: &other}, []string{"name", "status", "type", "address_id"}, &other},
	} {
		next, changed := tc.patch.apply(current)
		if !reflect.DeepEqual(changed, tc.wantChanged) || !reflect.DeepEqual(next.AddressID, tc.wantAddress) {
			t.Fatalf("%s: changed %v address %v; want %v %v", tc.name, changed, next.AddressID, tc.wantChanged, tc.wantAddress)
		}
	}
}
