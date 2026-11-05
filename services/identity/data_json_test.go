package identity

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
)

// dutyName is the #210 convention (policies/authz/README.md): dotted lower-case
// <service>.<object>.<action>, or <service>.<action> for a service-wide duty.
var dutyName = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*(?:\.[a-z][a-z0-9]*(?:-[a-z0-9]+)*){1,2}$`)

type seedDuties struct {
	Duties            map[string]json.RawMessage     `json:"duties"`
	ServiceGrants     map[string]map[string][]string `json:"service_grants"`
	ServicePrincipals map[string][]string            `json:"service_principals"`
}

func readSeedDuties(t *testing.T) seedDuties {
	t.Helper()
	raw, err := os.ReadFile("../../policies/authz/data.json")
	if err != nil {
		t.Fatal(err)
	}
	var s seedDuties
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

// TestDutyNamesFollowConvention keeps every seeded, granted and principal duty in the one naming style (#210).
func TestDutyNamesFollowConvention(t *testing.T) {
	s := readSeedDuties(t)
	for name := range s.Duties {
		if !dutyName.MatchString(name) {
			t.Errorf("duty %q is not dotted lower-case <service>.<object>.<action>", name)
		}
	}
	for caller, targets := range s.ServiceGrants {
		for target, duties := range targets {
			for _, d := range duties {
				if !dutyName.MatchString(d) {
					t.Errorf("service_grants[%s][%s] duty %q is not dotted lower-case", caller, target, d)
				}
			}
		}
	}
	for principal, duties := range s.ServicePrincipals {
		for _, d := range duties {
			if !dutyName.MatchString(d) {
				t.Errorf("service_principals[%s] duty %q is not dotted lower-case", principal, d)
			}
		}
	}
}

// renameRow is one ('old', 'new') row of the #210 role-assignment migration.
var renameRow = regexp.MustCompile(`(?m)^\s*\('((?:[^']|'')+)', '((?:[^']|'')+)'\),?$`)

// unseededRenameTargets are #210 targets that are duties but not catalogue entries: the
// all-scope duty lives in platform/go/tenant and the break-glass approver is checked by
// compliance before its route is seeded.
var unseededRenameTargets = map[string]bool{
	"org.switcher.all-scope":         true,
	"compliance.break-glass.approve": true,
}

// TestDutyRenameMigrationMatchesCatalogue checks 0010_unify_duty_names.sql against data.json:
// every old name is gone from the catalogue and every new name is seeded, so the migration moves
// role assignments onto duties that exist.
func TestDutyRenameMigrationMatchesCatalogue(t *testing.T) {
	raw, err := os.ReadFile("migrations/0010_unify_duty_names.sql")
	if err != nil {
		t.Fatal(err)
	}
	rows := renameRow.FindAllStringSubmatch(string(raw), -1)
	if len(rows) == 0 {
		t.Fatal("no rename rows found")
	}
	s := readSeedDuties(t)
	seenOld, seenNew := map[string]bool{}, map[string]bool{}
	for _, r := range rows {
		oldName, newName := strings.ReplaceAll(r[1], "''", "'"), strings.ReplaceAll(r[2], "''", "'")
		if seenOld[oldName] || seenNew[newName] {
			t.Errorf("rename %q -> %q repeats a name; the rename must be one-to-one", oldName, newName)
		}
		seenOld[oldName], seenNew[newName] = true, true
		if _, ok := s.Duties[oldName]; ok {
			t.Errorf("old duty name %q is still seeded", oldName)
		}
		if !dutyName.MatchString(newName) {
			t.Errorf("rename target %q is not dotted lower-case", newName)
		}
		if _, ok := s.Duties[newName]; !ok && !unseededRenameTargets[newName] {
			t.Errorf("rename target %q is not seeded in data.json", newName)
		}
	}
	for name := range seenOld {
		if seenNew[name] {
			t.Errorf("%q is both an old and a new name", name)
		}
	}
}

// TestDataJSONHasNoDuplicateKeys guards policies/authz/data.json against a key written twice in one object
// (#473): encoding/json and OPA both keep the last value silently, so a later edit to one copy is lost.
func TestDataJSONHasNoDuplicateKeys(t *testing.T) {
	raw, err := os.ReadFile("../../policies/authz/data.json")
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	if err := checkObjectKeys(dec, "$"); err != nil {
		t.Fatal(err)
	}
}

// checkObjectKeys walks one JSON value from dec and reports the first object that repeats a key.
func checkObjectKeys(dec *json.Decoder, path string) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch tok {
	case json.Delim('{'):
		seen := map[string]bool{}
		for dec.More() {
			k, err := dec.Token()
			if err != nil {
				return err
			}
			key := k.(string)
			if seen[key] {
				return fmt.Errorf("duplicate key %q in %s", key, path)
			}
			seen[key] = true
			if err := checkObjectKeys(dec, path+"."+key); err != nil {
				return err
			}
		}
		_, err = dec.Token()
		return err
	case json.Delim('['):
		for i := 0; dec.More(); i++ {
			if err := checkObjectKeys(dec, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
		_, err = dec.Token()
		return err
	}
	return nil
}
