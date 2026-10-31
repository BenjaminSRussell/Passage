package domain_test

// Wave 10 proof (issue #10) on a representative synthetic AX 2012 extract. The extract and the expected outcome
// come from testdata/ax/ax_reference.py, an independent Python reference (python3 -I, standard library only) that
// re-states the mapping rules from the README and never runs this engine; expected.json is its output. Regenerate
// both with `python3 -I testdata/ax/ax_reference.py` after changing the generator or a mapping.

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
)

const axDir = "testdata/ax"

// axTables mirrors the table names and key columns of tools/connectors/ax.go (a separate module this one cannot import).
var axTables = map[string]struct {
	table string
	keys  []string
}{
	"party":    {"DirParty", []string{"PartyNumber"}},
	"customer": {"CustTable", []string{"DataAreaId", "AccountNum"}},
	"vendor":   {"VendTable", []string{"DataAreaId", "AccountNum"}},
	"item":     {"InventTable", []string{"DataAreaId", "ItemId"}},
	"product":  {"EcoResProduct", []string{"DisplayProductNumber"}},
	"bom":      {"BOM", []string{"DataAreaId", "BOMId"}},
	"route":    {"Route", []string{"DataAreaId", "RouteId"}},
}

var axEntities = []string{"party", "customer", "vendor", "product", "item", "bom", "route"}

type axStats struct {
	Extracted     int                 `json:"extracted"`
	Loaded        int                 `json:"loaded"`
	RejectedRows  int                 `json:"rejectedRows"`
	WarnedRows    int                 `json:"warnedRows"`
	RejectCodes   map[string]int      `json:"rejectCodes"`
	WarningCodes  map[string]int      `json:"warningCodes"`
	TargetDigest  string              `json:"targetDigest"`
	RejectDigest  string              `json:"rejectDigest"`
	WarningDigest string              `json:"warningDigest"`
	Rejects       map[string][]string `json:"rejects,omitempty"`
}

func axExpected(t testing.TB) (demo, scale map[string]axStats) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(axDir, "expected.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct{ Demo, Scale map[string]axStats }
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Demo, doc.Scale
}

func axMapping(t testing.TB, entity string) domain.Mapping {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(axDir, "mappings", entity+".json"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := domain.ParseMapping(raw)
	if err != nil {
		t.Fatalf("%s mapping: %v", entity, err)
	}
	return m
}

// axReadCSV reads one demo extract the way tools/connectors.ReadCSV does: trimmed header names, every value a
// string, the key the trimmed key columns joined with "|".
func axReadCSV(t testing.TB, entity string) []domain.Record {
	t.Helper()
	spec := axTables[entity]
	f, err := os.Open(filepath.Join(axDir, "demo", spec.table+".csv"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	header := rows[0]
	var out []domain.Record
	for _, row := range rows[1:] {
		fields := make(map[string]any, len(header))
		for i, name := range header {
			fields[strings.TrimSpace(name)] = row[i]
		}
		out = append(out, domain.Record{Key: axKey(entity, fields), Fields: fields})
	}
	return out
}

func axKey(entity string, fields map[string]any) string {
	parts := make([]string, 0, 2)
	for _, c := range axTables[entity].keys {
		parts = append(parts, strings.TrimSpace(fields[c].(string)))
	}
	return strings.Join(parts, "|")
}

// --- generator: a line-for-line mirror of generate() in ax_reference.py for the four AC1 entities.

func axU(entity string, i int, field string) uint32 {
	h := fnv.New32a()
	fmt.Fprintf(h, "%s:%d:%s", entity, i, field)
	return h.Sum32()
}

type axChoice struct {
	limit uint32
	value string
}

func axPick(r uint32, table []axChoice) string {
	for _, c := range table {
		if r < c.limit {
			return c.value
		}
	}
	return table[len(table)-1].value
}

func axCompany(e string, i int) string {
	return axPick(axU(e, i, "co")%10, []axChoice{{6, "usmf"}, {9, "demf"}, {10, "gbsi"}})
}

func axModified(e string, i int) string {
	r := axU(e, i, "mdt") % 1000
	ts := time.Date(2012, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(axU(e, i, "ts")%378_691_200) * time.Second)
	switch {
	case r < 30:
		return "1900-01-01 00:00:00"
	case r < 32:
		return ts.Format("02.01.2006 15:04:05")
	}
	return ts.Format("2006-01-02 15:04:05")
}

func axBlocked(e string, i int) string {
	return axPick(axU(e, i, "blk")%1000, []axChoice{{850, "0"}, {900, "1"}, {940, "2"}, {960, "3"}, {975, "4"}, {995, "5"}, {1000, "All"}})
}

func axGrouped(n uint32) string {
	s := strconv.FormatUint(uint64(n), 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "." + s[i:]
	}
	return s
}

func axCreditMax(e string, i int) string {
	r, cents := axU(e, i, "cm")%1000, axU(e, i, "cmv")%50_000_000
	switch {
	case r < 3:
		return fmt.Sprintf("%s,%02d", axGrouped(cents/100), cents%100)
	case r < 5:
		return fmt.Sprintf("%d.%02d", 150_000_000_000_000+uint64(cents/100), cents%100)
	case r < 105:
		return fmt.Sprintf("%d.%02d5", cents/100, cents%100)
	case r < 205:
		return "0"
	}
	return fmt.Sprintf("%d.%02d", cents/100, cents%100)
}

var axBOMNames = []string{"Frame assembly", "Café table – oak", "Müller “Pro” kit", "  Pump housing  ",
	"Gehäuse  Typ\tB", "Bracket, steel", "Café chair"}

func axGenRow(e string, i int) map[string]any {
	switch e {
	case "customer":
		return map[string]any{
			"DataAreaId": axCompany(e, i), "AccountNum": fmt.Sprintf("C%06d", i), "Party": strconv.Itoa(5637144576 + i),
			"CustGroup": axPick(axU(e, i, "grp")%100, []axChoice{{35, "10"}, {65, "20"}, {85, "30"}, {95, "40"}, {100, ""}}),
			"Currency":  axPick(axU(e, i, "cur")%100, []axChoice{{60, "USD"}, {80, "EUR"}, {92, "CAD"}, {98, ""}, {100, " usd "}}),
			"CreditMax": axCreditMax(e, i), "Blocked": axBlocked(e, i), "ModifiedDateTime": axModified(e, i),
		}
	case "vendor":
		return map[string]any{
			"DataAreaId": axCompany(e, i), "AccountNum": fmt.Sprintf("V%06d", i), "Party": strconv.Itoa(5637144976 + i),
			"VendGroup": axPick(axU(e, i, "grp")%100, []axChoice{{40, "10"}, {70, "20"}, {95, "30"}, {98, "INTERCO"}, {100, ""}}),
			"Currency":  axPick(axU(e, i, "cur")%1000, []axChoice{{600, "USD"}, {800, "EUR"}, {990, "CAD"}, {1000, ""}}),
			"Blocked":   axBlocked(e, i), "ModifiedDateTime": axModified(e, i),
		}
	case "item":
		item := fmt.Sprintf("I%06d", i)
		if axU(e, i, "pad")%100 < 5 {
			item = " " + item + "  "
		}
		product := strconv.Itoa(5637344576 + i)
		if axU(e, i, "prd")%1000 < 3 {
			product = ""
		}
		weight := "0"
		switch r := axU(e, i, "nw") % 1000; {
		case r < 2:
			weight = "0.12345678"
		case r >= 100:
			w := axU(e, i, "nwv") % 2_500_000
			weight = fmt.Sprintf("%d.%04d", w/10000, w%10000)
		}
		return map[string]any{
			"DataAreaId": axCompany(e, i), "ItemId": item,
			"ItemType": axPick(axU(e, i, "typ")%1000, []axChoice{{700, "0"}, {900, "1"}, {996, "2"}, {1000, "Item"}}),
			"Product":  product, "NetWeight": weight, "ModifiedDateTime": axModified(e, i),
		}
	case "bom":
		name := ""
		if axU(e, i, "nmblank")%1000 >= 4 {
			name = axBOMNames[axU(e, i, "nm")%uint32(len(axBOMNames))] + " " + strconv.Itoa(i)
		}
		return map[string]any{
			"DataAreaId": axCompany(e, i), "BOMId": fmt.Sprintf("B%06d", i), "Name": name,
			"Approved":         axPick(axU(e, i, "apr")%1000, []axChoice{{550, "1"}, {600, "Yes"}, {950, "0"}, {990, "No"}, {1000, "2"}}),
			"ModifiedDateTime": axModified(e, i),
		}
	}
	panic("no generator for " + e)
}

var axDuplicates = map[string]struct {
	permille uint32
	conflict func(map[string]any)
}{
	"customer": {6, func(f map[string]any) { f["Currency"] = axEither(f["Currency"] == "EUR", "USD", "EUR") }},
	"vendor":   {4, func(f map[string]any) { f["Currency"] = axEither(f["Currency"] == "EUR", "USD", "EUR") }},
	"item":     {3, func(f map[string]any) { f["NetWeight"] = axEither(f["NetWeight"] == "0", "1.5", "0") }},
	"bom": {3, func(f map[string]any) {
		f["Approved"] = axEither(f["Approved"] == "1" || f["Approved"] == "Yes", "0", "1")
	}},
}

func axEither(cond bool, yes, no string) string {
	if cond {
		return yes
	}
	return no
}

func axGenerate(entity string, n int) []domain.Record {
	dup := axDuplicates[entity]
	rows := make([]domain.Record, 0, n)
	var prev map[string]any
	for i := 0; i < n; i++ {
		var fields map[string]any
		if prev != nil && axU(entity, i, "dup")%1000 < dup.permille {
			fields = make(map[string]any, len(prev))
			for k, v := range prev {
				fields[k] = v
			}
			if axU(entity, i, "dupkind")%2 == 1 {
				dup.conflict(fields)
			}
		} else {
			fields = axGenRow(entity, i)
		}
		rows = append(rows, domain.Record{Key: axKey(entity, fields), Fields: fields})
		prev = fields
	}
	return rows
}

// --- outcome measured from the engine's report and the loaded target rows.

func axDigest(lines []string) string {
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}

func axValueText(t testing.TB, v any) string {
	switch x := v.(type) {
	case string:
		return "s:" + x
	case int64:
		return "i:" + strconv.FormatInt(x, 10)
	case bool:
		return "b:" + strconv.FormatBool(x)
	}
	t.Fatalf("unexpected mapped value type %T", v)
	return ""
}

func axTargetLine(t testing.TB, key string, fields map[string]any) string {
	names := make([]string, 0, len(fields))
	for n := range fields {
		names = append(names, n)
	}
	sort.Strings(names)
	parts := []string{key}
	for _, n := range names {
		parts = append(parts, n+"="+axValueText(t, fields[n]))
	}
	return strings.Join(parts, "\t")
}

func axCodesByKey(rejects []domain.RowReject, warnings []domain.RowWarning) (map[string][]string, map[string][]string) {
	rej, warn := map[string][]string{}, map[string][]string{}
	for _, r := range rejects {
		for _, x := range r.Rejects {
			rej[r.SourceKey] = append(rej[r.SourceKey], x.Code)
		}
	}
	for _, w := range warnings {
		for _, x := range w.Warnings {
			warn[w.SourceKey] = append(warn[w.SourceKey], x.Code)
		}
	}
	for _, m := range []map[string][]string{rej, warn} {
		for k := range m {
			slices.Sort(m[k])
		}
	}
	return rej, warn
}

func axMeasure(t testing.TB, rep domain.Report, loader *domain.MemLoader) axStats {
	t.Helper()
	rej, warn := axCodesByKey(rep.Rejects, rep.Warnings)
	s := axStats{Extracted: rep.Extracted, Loaded: len(loader.Rows), RejectedRows: len(rep.Rejects), WarnedRows: len(rep.Warnings),
		RejectCodes: map[string]int{}, WarningCodes: map[string]int{}, Rejects: rej}
	var target, rejLines, warnLines []string
	for _, it := range loader.Rows {
		target = append(target, axTargetLine(t, it.SourceKey, it.Fields))
	}
	for k, codes := range rej {
		rejLines = append(rejLines, k+"\t"+strings.Join(codes, ","))
		for _, c := range codes {
			s.RejectCodes[c]++
		}
	}
	for k, codes := range warn {
		warnLines = append(warnLines, k+"\t"+strings.Join(codes, ","))
		for _, c := range codes {
			s.WarningCodes[c]++
		}
	}
	s.TargetDigest, s.RejectDigest, s.WarningDigest = axDigest(target), axDigest(rejLines), axDigest(warnLines)
	return s
}

func axCompare(t testing.TB, entity string, got, want axStats) {
	t.Helper()
	if want.Rejects == nil {
		got.Rejects = nil
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s: engine outcome differs from the independent reference\n got: %+v\nwant: %+v", entity, got, want)
	}
}

func axRun(t testing.TB, entity string, rows []domain.Record) (*domain.Pipeline, *domain.MemXref, *domain.MemLoader, domain.Report) {
	t.Helper()
	p, xref, loader := newPipeline(rows)
	rep, err := p.Run(context.Background(), domain.RunSpec{SourceSystem: system, Entity: entity, Mapping: axMapping(t, entity)})
	if err != nil {
		t.Fatalf("%s: %v", entity, err)
	}
	return p, xref, loader, rep
}

func axReconciled(t testing.TB, entity string, rep domain.Report, loaded int) {
	t.Helper()
	if rep.Reconciliation == nil || !rep.Reconciliation.OK() || rep.Reconciliation.Source != loaded || rep.Reconciliation.Target != loaded {
		t.Fatalf("%s: reconciliation %+v, want every one of %d loaded rows to reconcile", entity, rep.Reconciliation, loaded)
	}
}

// axRerunUnchanged re-runs the same extract in reverse order and asserts nothing reaches the target or the xref.
func axRerunUnchanged(t testing.TB, entity string, p *domain.Pipeline, xref *domain.MemXref, loader *domain.MemLoader, rows []domain.Record, first domain.Report) {
	t.Helper()
	reversed := slices.Clone(rows)
	slices.Reverse(reversed)
	p.Extractor = sliceExtractor{rows: reversed}
	calls, puts := loader.Calls, xref.Puts
	again, err := p.Run(context.Background(), domain.RunSpec{SourceSystem: system, Entity: entity, Mapping: axMapping(t, entity)})
	if err != nil {
		t.Fatal(err)
	}
	if again.Created != 0 || again.Updated != 0 || again.Unchanged != first.Created || loader.Calls != calls || xref.Puts != puts {
		t.Fatalf("%s: reversed re-run created=%d updated=%d unchanged=%d (want %d) loader calls %d->%d xref puts %d->%d",
			entity, again.Created, again.Updated, again.Unchanged, first.Created, calls, loader.Calls, puts, xref.Puts)
	}
	r1, _ := axCodesByKey(first.Rejects, nil)
	r2, _ := axCodesByKey(again.Rejects, nil)
	if !reflect.DeepEqual(r1, r2) {
		t.Fatalf("%s: reversed re-run changed the exception queue", entity)
	}
}

// The Go mirror of the generator must produce the committed demo extract, so the full-size run below measures the
// same dataset the reference script measured.
func TestAXProof_GoGeneratorMatchesCommittedDemoExtract(t *testing.T) {
	demo, _ := axExpected(t)
	for _, entity := range []string{"customer", "vendor", "item", "bom"} {
		csvRows := axReadCSV(t, entity)
		gen := axGenerate(entity, demo[entity].Extracted)
		if !reflect.DeepEqual(csvRows, gen) {
			t.Errorf("%s: Go generator differs from %s.csv", entity, axTables[entity].table)
		}
	}
}

// AC4 (fixture level): a demo manufacturer's master data -- all seven AX tables -- migrates from a synthetic AX
// extract with every loaded row, reject and warning exactly as the independent reference predicts.
func TestAXProof_DemoManufacturerExtractMigratesAllSevenTablesAsTheReferencePredicts(t *testing.T) {
	demo, _ := axExpected(t)
	for _, entity := range axEntities {
		rows := axReadCSV(t, entity)
		p, xref, loader, rep := axRun(t, entity, rows)
		axCompare(t, entity, axMeasure(t, rep, loader), demo[entity])
		axReconciled(t, entity, rep, len(loader.Rows))
		axRerunUnchanged(t, entity, p, xref, loader, rows, rep)
	}
}

// Hand-derived literals for adversarial demo rows, checked against the loaded target, independent of both the
// engine and the reference script.
func TestAXProof_DemoRowsMatchHandDerivedValues(t *testing.T) {
	loaded := map[string]map[string]any{}
	queued := map[string][]string{}
	for _, entity := range []string{"party", "customer", "item", "bom"} {
		_, _, loader, rep := axRun(t, entity, axReadCSV(t, entity))
		for _, it := range loader.Rows {
			loaded[entity+"/"+it.SourceKey] = it.Fields
		}
		rej, _ := axCodesByKey(rep.Rejects, nil)
		for k, codes := range rej {
			queued[entity+"/"+k] = codes
		}
	}
	want := map[string]map[string]any{
		// 258520.915 at scale 2: the digit before the half is odd, so half-even rounds up; group 40 has no mapping.
		"customer/usmf|C000020": {"accountNumber": "C000020", "legalEntity": "USMF", "partyRecId": int64(5637144596), "segment": "other",
			"currency": "USD", "creditLimit": "258520.92", "blocked": "never", "modifiedAt": "2022-11-01T11:38:34Z"},
		// 32324.165: the digit before the half is even, so it stays.
		"customer/usmf|C000010": {"accountNumber": "C000010", "legalEntity": "USMF", "partyRecId": int64(5637144586), "segment": "retail",
			"currency": "USD", "creditLimit": "32324.16", "blocked": "none", "modifiedAt": "2017-01-05T16:53:51Z"},
		// AX null date 1900-01-01 00:00:00 leaves modifiedAt out.
		"customer/usmf|C000032": {"accountNumber": "C000032", "legalEntity": "USMF", "partyRecId": int64(5637144608), "segment": "retail",
			"currency": "EUR", "creditLimit": "42494.68", "blocked": "none"},
		// Padded ItemId " I000013  " and a four-place AX real.
		"item/usmf|I000013": {"sku": "I000013", "legalEntity": "USMF", "itemType": "item", "productRecId": int64(5637344589),
			"netWeight": "93.3477", "modifiedAt": "2015-02-05T20:02:03Z"},
		// 142.0260 loses its trailing zero.
		"item/gbsi|I000032": {"sku": "I000032", "legalEntity": "GBSI", "itemType": "item", "productRecId": int64(5637344608),
			"netWeight": "142.026", "modifiedAt": "2022-07-12T16:09:12Z"},
		// Double space and a tab collapse.
		"bom/demf|B000004": {"bomNumber": "B000004", "legalEntity": "DEMF", "name": "Gehäuse Typ B 4", "approved": true, "modifiedAt": "2022-07-31T01:29:18Z"},
		// e + U+0301 composes to U+00E9.
		"bom/usmf|B000009": {"bomNumber": "B000009", "legalEntity": "USMF", "name": "Café chair 9", "approved": true, "modifiedAt": "2012-12-20T19:55:35Z"},
		// Leading spaces trimmed, no-break space becomes a space, the run of spaces collapses.
		"bom/demf|B000005": {"bomNumber": "B000005", "legalEntity": "DEMF", "name": "Pump housing 5", "approved": true, "modifiedAt": "2015-01-25T16:49:47Z"},
		"party/000000002":  {"partyNumber": "000000002", "name": "O'Brien & Sons 1", "recId": int64(5637144577), "modifiedAt": "2023-04-15T22:39:49Z"},
		"party/000000001":  {"partyNumber": "000000001", "name": "Litware Inc 0", "recId": int64(5637144576), "modifiedAt": "2014-12-23T01:52:24Z"},
		"party/000000008":  {"partyNumber": "000000008", "name": "Zoë Industries 7", "recId": int64(5637144583), "modifiedAt": "2018-07-23T20:59:19Z"},
	}
	for key, fields := range want {
		if got := loaded[key]; !reflect.DeepEqual(got, fields) {
			t.Errorf("%s\n got: %#v\nwant: %#v", key, got, fields)
		}
	}
	wantQueued := map[string][]string{
		"customer/gbsi|C000000": {"CUST-CURRENCY-UNKNOWN"}, // " usd " trims to usd, not in USD/EUR/CAD
		"customer/usmf|C000342": {domain.CodeMapCoerce},    // German "245.762,52"
		"item/demf|I000005":     {domain.CodeMapRange},     // 0.12345678 has eight places
		"bom/demf|B000010":      {domain.CodeMapCoerce},    // Approved 2 is not NoYes
	}
	for key, codes := range wantQueued {
		if got := queued[key]; !slices.Equal(got, codes) {
			t.Errorf("%s queued %v, want %v", key, got, codes)
		}
		if _, ok := loaded[key]; ok {
			t.Errorf("%s was loaded despite its reject", key)
		}
	}
}

// AC1 and AC2 at full size on the representative extract: 50k customers, 20k vendors, 100k items and 10k BOMs load
// under the 30-minute budget, every loaded row reconciles, the outcome matches the independent reference, and a
// re-run of the same extract in reverse order changes nothing. The target is MemLoader, not a context's batch API.
func TestAXProof_FullSizeRepresentativeExtractMatchesReferenceAndRerunsUnchanged(t *testing.T) {
	if testing.Short() {
		t.Skip("full-size AX extract skipped under -short")
	}
	_, scale := axExpected(t)
	var total time.Duration
	for _, entity := range []string{"customer", "vendor", "item", "bom"} {
		rows := axGenerate(entity, scale[entity].Extracted)
		start := time.Now()
		p, xref, loader, rep := axRun(t, entity, rows)
		total += time.Since(start)
		axCompare(t, entity, axMeasure(t, rep, loader), scale[entity])
		axReconciled(t, entity, rep, len(loader.Rows))
		axRerunUnchanged(t, entity, p, xref, loader, rows, rep)
		t.Logf("%s: extracted=%d loaded=%d rejected rows=%d (%.2f%%) warned rows=%d", entity, rep.Extracted, len(loader.Rows),
			len(rep.Rejects), 100*float64(len(rep.Rejects))/float64(rep.Extracted), len(rep.Warnings))
	}
	if total >= scaleBudget {
		t.Fatalf("first loads took %s, budget %s", total, scaleBudget)
	}
	t.Logf("first loads of 180,000 rows took %s", total)
}

// AC2 property: re-running the demo extract in any order, with any key-preserving white-space padding, changes
// nothing; changing the timestamp of n loaded rows updates exactly those n rows under their existing target ids.
func TestAXProof_RerunOfPermutedPaddedExtractIsUnchangedProperty(t *testing.T) {
	extracts := map[string][]domain.Record{}
	for _, entity := range axEntities {
		extracts[entity] = axReadCSV(t, entity)
	}
	rapid.Check(t, func(rt *rapid.T) {
		entity := rapid.SampledFrom(axEntities).Draw(rt, "entity")
		rows := extracts[entity]
		p, xref, loader, first := axRun(t, entity, rows)
		count := map[string]int{}
		for _, r := range rows {
			count[r.Key]++
		}
		rnd := rand.New(rand.NewSource(rapid.Int64().Draw(rt, "seed")))
		pads := []string{" ", "  ", "\t", " \t "}
		next := make([]domain.Record, len(rows))
		for i, r := range rows {
			fields := make(map[string]any, len(r.Fields))
			for k, v := range r.Fields {
				fields[k] = v
				// Padding one occurrence of a repeated key would turn an exact repeat into a conflicting one.
				if count[r.Key] == 1 && rnd.Intn(3) == 0 {
					fields[k] = pads[rnd.Intn(len(pads))] + v.(string) + pads[rnd.Intn(len(pads))]
				}
			}
			next[i] = domain.Record{Key: r.Key, Fields: fields}
		}
		rnd.Shuffle(len(next), func(i, j int) { next[i], next[j] = next[j], next[i] })

		before := map[string]domain.XrefEntry{}
		for k, e := range xref.Entries {
			before[k.SourceKey] = e
		}
		var candidates []string
		for _, e := range first.Ledger {
			if count[e.SourceKey] == 1 {
				candidates = append(candidates, e.SourceKey)
			}
		}
		sort.Strings(candidates)
		rnd.Shuffle(len(candidates), func(i, j int) { candidates[i], candidates[j] = candidates[j], candidates[i] })
		changed := candidates[:rapid.IntRange(0, min(25, len(candidates))).Draw(rt, "changed")]
		touch := map[string]bool{}
		for _, k := range changed {
			touch[k] = true
		}
		for i := range next {
			if touch[next[i].Key] {
				next[i].Fields["ModifiedDateTime"] = "2030-06-01 12:00:00" // after every generated timestamp
			}
		}

		p.Extractor = sliceExtractor{rows: next}
		again, err := p.Run(context.Background(), domain.RunSpec{SourceSystem: system, Entity: entity, Mapping: axMapping(t, entity)})
		if err != nil {
			rt.Fatal(err)
		}
		if again.Created != 0 || again.Updated != len(changed) || again.Unchanged != first.Created-len(changed) || len(again.Rejects) != len(first.Rejects) {
			rt.Fatalf("%s: created=%d updated=%d (want %d) unchanged=%d (want %d) rejects %d->%d", entity, again.Created, again.Updated,
				len(changed), again.Unchanged, first.Created-len(changed), len(first.Rejects), len(again.Rejects))
		}
		for _, e := range again.Ledger {
			if e.TargetID != before[e.SourceKey].TargetID {
				rt.Fatalf("%s: %s moved from target %s to %s", entity, e.SourceKey, before[e.SourceKey].TargetID, e.TargetID)
			}
			if touch[e.SourceKey] != (e.Action == domain.ActionUpdated) {
				rt.Fatalf("%s: %s action %s, touched=%v", entity, e.SourceKey, e.Action, touch[e.SourceKey])
			}
		}
		for _, k := range changed {
			if got := loader.Rows[before[k].TargetID].Fields["modifiedAt"]; got != "2030-06-01T12:00:00Z" {
				rt.Fatalf("%s: %s modifiedAt = %v after update", entity, k, got)
			}
		}
	})
}
