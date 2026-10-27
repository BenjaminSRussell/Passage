package domain

import (
	"bufio"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The Cube model and queryRewrite live in data/ (same wave). These tests keep
// the Go catalog and scope mirror in step with them, so a dimension the planner
// allows always exists in Cube, every Cube measure is a governed metric, and the
// two scope maps agree (FR-46.6, FR-46.7).

const dataDir = "../../../../data/cube/"

// cubeMembers reads the dimension and measure names of a Cube YAML model.
func cubeMembers(t *testing.T, path string) (dims, measures []string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	section := ""
	name := regexp.MustCompile(`^\s{6}- name: (\w+)\s*$`)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		switch strings.TrimSpace(line) {
		case "dimensions:", "measures:":
			section = strings.TrimSuffix(strings.TrimSpace(line), ":")
			continue
		}
		if m := name.FindStringSubmatch(line); m != nil {
			if section == "dimensions" {
				dims = append(dims, m[1])
			} else if section == "measures" {
				measures = append(measures, m[1])
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if len(dims) == 0 || len(measures) == 0 {
		t.Fatalf("%s: parsed no dimensions or measures", path)
	}
	return dims, measures
}

func TestCubeContract_FinJournalLineMatchesCatalog(t *testing.T) {
	dims, measures := cubeMembers(t, dataDir+"model/fin_journal_line.yml")
	catalog := GovernedMetrics()
	for _, id := range measures {
		m, ok := CatalogMetric(catalog, id)
		if !ok {
			t.Errorf("Cube measure %s is not a published governed metric", id)
			continue
		}
		if ModelForGrain(m.Grain) != "fin_journal_line" {
			t.Errorf("Cube measure %s is in fin_journal_line but the metric grain is %s", id, m.Grain)
		}
	}
	for _, m := range catalog {
		if ModelForGrain(m.Grain) != "fin_journal_line" {
			continue
		}
		for _, d := range m.AllowedDimensions {
			if !slices.Contains(dims, d) {
				t.Errorf("metric %s allows dimension %s, which fin_journal_line does not have", m.ID, d)
			}
		}
	}
	if _, column := modelScopeColumns("fin_journal_line"); !column || !slices.Contains(dims, "legal_entity_id") {
		t.Error("fin_journal_line must carry the legal_entity_id scope dimension")
	}
}

func TestCubeContract_ScopeColumnsMatchQueryRewrite(t *testing.T) {
	raw, err := os.ReadFile(dataDir + "cube.js")
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile(`(?s)const SCOPE_COLUMNS = \{(.*?)\};`).FindSubmatch(raw)
	if block == nil {
		t.Fatal("cube.js has no SCOPE_COLUMNS map")
	}
	entries := regexp.MustCompile(`(\w+): "(\w+)"`).FindAllSubmatch(block[1], -1)
	if len(entries) == 0 {
		t.Fatal("SCOPE_COLUMNS is empty")
	}
	seen := map[string]bool{}
	for _, e := range entries {
		model, column := string(e[1]), string(e[2])
		seen[model] = true
		got, ok := modelScopeColumns(model)
		if !ok || got != column {
			t.Errorf("cube.js maps %s to %s; scope.go maps it to %q (mapped %v)", model, column, got, ok)
		}
	}
	for _, grain := range []string{GrainJournalLine, GrainSalesLine, GrainResourceShift} {
		if model := ModelForGrain(grain); !seen[model] {
			t.Errorf("model %s (grain %s) has no SCOPE_COLUMNS entry in cube.js", model, grain)
		}
	}
}
