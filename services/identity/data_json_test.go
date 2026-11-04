package identity

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

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
