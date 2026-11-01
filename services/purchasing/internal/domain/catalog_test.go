package domain

import (
	"errors"
	"testing"
)

func TestNextCatalogStatus(t *testing.T) {
	for _, c := range []struct {
		status, action string
		entries        int
		want, code     string
	}{
		{CatalogDraft, CatalogPublish, 1, CatalogPublished, ""},
		{CatalogDraft, CatalogPublish, 0, "", CodeCatalogEmpty},
		{CatalogPublished, CatalogRetire, 3, CatalogRetired, ""},
		{CatalogDraft, CatalogRetire, 3, "", CodeCatalogState},
		{CatalogPublished, CatalogPublish, 3, "", CodeCatalogState},
		{CatalogRetired, CatalogPublish, 3, "", CodeCatalogState},
	} {
		got, err := NextCatalogStatus(c.status, c.action, c.entries)
		var de *Error
		switch {
		case c.code == "" && err != nil:
			t.Errorf("%s %s: %v", c.status, c.action, err)
		case c.code != "" && (!errors.As(err, &de) || de.Code != c.code):
			t.Errorf("%s %s: err %v, want %s", c.status, c.action, err, c.code)
		case got != c.want:
			t.Errorf("%s %s = %q, want %q", c.status, c.action, got, c.want)
		}
	}
}

func TestCatalogAvailableOn(t *testing.T) {
	to := day("2027-06-30")
	c := Catalog{Status: CatalogPublished, ValidFrom: day("2027-01-01"), ValidTo: &to, OrgUnitIDs: []string{"ou-1"}}
	for _, k := range []struct {
		name string
		d    Day
		unit string
		want bool
	}{
		{"inside, listed unit", day("2027-03-01"), "ou-1", true},
		{"first day", day("2027-01-01"), "ou-1", true},
		{"last day", day("2027-06-30"), "ou-1", true},
		{"before validity", day("2026-12-31"), "ou-1", false},
		{"after validity", day("2027-07-01"), "ou-1", false},
		{"other unit", day("2027-03-01"), "ou-2", false},
		{"no unit on the requisition", day("2027-03-01"), "", false},
	} {
		if got := c.AvailableOn(k.d, k.unit); got != k.want {
			t.Errorf("%s = %v, want %v", k.name, got, k.want)
		}
	}
	c.OrgUnitIDs = nil
	if !c.AvailableOn(day("2027-03-01"), "") {
		t.Error("a catalog with no units is open to every unit")
	}
	for _, s := range []string{CatalogDraft, CatalogRetired} {
		c.Status = s
		if c.AvailableOn(day("2027-03-01"), "") {
			t.Errorf("%s catalog must not be available", s)
		}
	}
}

func TestCatalogCheckForRequisition(t *testing.T) {
	c := Catalog{Number: "CAT-1", LegalEntityID: "le", Status: CatalogPublished, ValidFrom: day("2027-01-01")}
	e := CatalogEntry{Currency: "USD"}
	if err := c.CheckForRequisition(e, "le", "", "USD", day("2027-02-01")); err != nil {
		t.Fatal(err)
	}
	var de *Error
	if err := c.CheckForRequisition(e, "other", "", "USD", day("2027-02-01")); !errors.As(err, &de) || de.Code != CodeCatalogUnavailable {
		t.Errorf("other legal entity: %v", err)
	}
	if err := c.CheckForRequisition(e, "le", "", "EUR", day("2027-02-01")); !errors.As(err, &de) || de.Code != CodeCatalogCurrency {
		t.Errorf("currency: %v", err)
	}
}
