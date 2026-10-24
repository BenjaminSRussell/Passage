package domain_test

import (
	"fmt"
	"math"
	"math/rand"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
)

const (
	seededParties    = 10000
	plantedDupes     = 500
	seed             = 7
	minPrecision     = 0.95
	minRecall        = 0.90
	likelyOrPossible = domain.PossibleDuplicateWeight
)

var (
	syllables = []string{"ka", "lo", "mi", "ra", "tu", "ven", "dor", "sil", "an", "pex", "gor", "lum", "bri", "cas", "dyn", "el", "fox", "han", "jor", "kel", "mar", "nov", "or", "pra", "qui", "sun", "tek", "ul", "vik", "wen"}
	suffixes  = []string{"Ltd", "GmbH", "Inc", "SA", "BV"}
	streets   = []string{"Oak Road", "Main Street", "Mill Lane", "Station Way", "Park Avenue", "Church Hill", "High Street", "River Drive"}
	countries = []string{"DE", "FR", "NL", "US", "IN", "ES", "IT", "PL"}
)

func word(rng *rand.Rand) string {
	var b strings.Builder
	for i := 0; i < 3; i++ {
		b.WriteString(syllables[rng.Intn(len(syllables))])
	}
	return strings.ToUpper(b.String()[:1]) + b.String()[1:]
}

func basePartyRecord(rng *rand.Rand, i int) domain.PartyRecord {
	return domain.PartyRecord{
		ID:      fmt.Sprintf("p%05d", i),
		Type:    "organization",
		Name:    word(rng) + " " + word(rng) + " " + suffixes[rng.Intn(len(suffixes))],
		TaxID:   fmt.Sprintf("%09d", rng.Intn(1_000_000_000)),
		Street:  fmt.Sprintf("%d %s", 1+rng.Intn(300), streets[rng.Intn(len(streets))]),
		Postal:  fmt.Sprintf("%05d", rng.Intn(100000)),
		Country: countries[rng.Intn(len(countries))],
	}
}

func swapAdjacent(s string, at int) string {
	r := []rune(s)
	r[at], r[at+1] = r[at+1], r[at]
	return string(r)
}

func plantDuplicate(rng *rand.Rand, original domain.PartyRecord, id string) domain.PartyRecord {
	dup := original
	dup.ID = id
	switch rng.Intn(4) {
	case 0:
		dup.Name = swapAdjacent(original.Name, 5+rng.Intn(4))
		dup.TaxID = original.TaxID[:3] + " " + original.TaxID[3:6] + "-" + original.TaxID[6:]
	case 1:
		dup.Name = strings.ToUpper(strings.ReplaceAll(original.Name, " ", "  ")) + "."
		dup.TaxID = ""
	case 2:
		parts := strings.SplitN(original.Street, " ", 2)
		dup.Street = parts[1] + " " + parts[0]
		dup.Name = swapAdjacent(original.Name, 6)
		dup.TaxID = ""
	default:
		dup.Street = strings.ToLower(original.Street)
		dup.TaxID = original.TaxID
		dup.Postal = original.Postal + " "
	}
	return dup
}

func seededDataset() ([]domain.PartyRecord, map[[2]string]bool) {
	rng := rand.New(rand.NewSource(seed))
	records := make([]domain.PartyRecord, 0, seededParties)
	truth := map[[2]string]bool{}
	for i := 0; i < seededParties-plantedDupes; i++ {
		records = append(records, basePartyRecord(rng, i))
	}
	for i := 0; i < plantedDupes; i++ {
		original := records[rng.Intn(seededParties-plantedDupes)]
		dup := plantDuplicate(rng, original, fmt.Sprintf("p%05d", seededParties-plantedDupes+i))
		records = append(records, dup)
		truth[[2]string{original.ID, dup.ID}] = true
	}
	return records, truth
}

func TestDedupPrecisionAndRecallOnSeededParties(t *testing.T) {
	records, truth := seededDataset()
	if len(records) != seededParties || len(truth) != plantedDupes {
		t.Fatalf("dataset has %d records and %d planted pairs", len(records), len(truth))
	}
	found, hits := 0, 0
	for _, m := range domain.FindDuplicatePairs(records) {
		if m.Weight < likelyOrPossible {
			continue
		}
		found++
		if truth[[2]string{m.A, m.B}] || truth[[2]string{m.B, m.A}] {
			hits++
		}
	}
	precision, recall := float64(hits)/float64(found), float64(hits)/float64(plantedDupes)
	t.Logf("pairs=%d hits=%d precision=%.4f recall=%.4f", found, hits, precision, recall)
	if found == 0 || precision < minPrecision || recall < minRecall {
		t.Fatalf("precision %.4f (min %.2f), recall %.4f (min %.2f)", precision, minPrecision, recall, minRecall)
	}
}

func TestFindCandidatesFindsPlantedDuplicate(t *testing.T) {
	records, _ := seededDataset()
	probe := records[seededParties-1]
	candidates := domain.FindCandidates(records, probe)
	if len(candidates) == 0 {
		t.Fatal("planted duplicate has no candidate")
	}
	if candidates[0].Weight < domain.PossibleDuplicateWeight {
		t.Fatalf("top candidate weight %.2f", candidates[0].Weight)
	}
}

func TestFindCandidatesEdgeCases(t *testing.T) {
	base := domain.PartyRecord{ID: "a", Type: "organization", Name: "Acme Foods Ltd", TaxID: "123456789", Street: "1 Oak Road", Postal: "10001", Country: "US"}
	tests := []struct {
		name string
		pool []domain.PartyRecord
		p    domain.PartyRecord
		want int
	}{
		{"empty pool", nil, base, 0},
		{"empty probe fields", []domain.PartyRecord{base}, domain.PartyRecord{ID: "z", Type: "organization"}, 0},
		{"person is not matched to organization", []domain.PartyRecord{func() domain.PartyRecord { r := base; r.ID, r.Type = "b", "person"; return r }()}, base, 0},
		{"different country lowers weight below possible", []domain.PartyRecord{func() domain.PartyRecord {
			r := base
			r.ID, r.Country, r.TaxID, r.Street, r.Postal = "b", "FR", "", "", ""
			return r
		}()}, base, 0},
		{"identical record other id is likely duplicate", []domain.PartyRecord{func() domain.PartyRecord { r := base; r.ID = "b"; return r }()}, base, 1},
		{"probe is excluded from its own candidates", []domain.PartyRecord{base}, base, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.FindCandidates(tc.pool, tc.p); len(got) != tc.want {
				t.Fatalf("got %d candidates (%v), want %d", len(got), got, tc.want)
			}
		})
	}
}

func TestFindDuplicatePairsBoundsDenseNameBlocks(t *testing.T) {
	records := make([]domain.PartyRecord, 1000)
	for i := range records {
		records[i] = domain.PartyRecord{
			ID: fmt.Sprintf("p%04d", i), Type: "organization",
			Name: fmt.Sprintf("Common entity %04d", i), TaxID: fmt.Sprintf("%09d", i),
			Street: fmt.Sprintf("Street %04d", i), Postal: fmt.Sprintf("%05d", i), Country: "US",
		}
	}
	got := domain.FindDuplicatePairs(records)
	if len(got) == 0 {
		t.Fatal("dense name block produced no candidates")
	}
	if len(got) > len(records)*64 {
		t.Fatalf("dense name block produced %d pairs; expected bounded neighbor comparisons", len(got))
	}
}

func TestNormalize(t *testing.T) {
	tests := []struct{ in, want string }{
		{"  ACME,  Foods Ltd. ", "acme foods ltd"},
		{"", ""},
		{"---", ""},
		{"Müller & Söhne", "müller söhne"},
	}
	for _, tc := range tests {
		if got := domain.Normalize(tc.in); got != tc.want {
			t.Errorf("Normalize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestJaroWinkler(t *testing.T) {
	tests := []struct {
		a, b string
		want float64
	}{
		{"martha", "marhta", 0.9611},
		{"dixon", "dicksonx", 0.8133},
		{"same", "same", 1},
		{"", "x", 0},
		{"abc", "xyz", 0},
	}
	for _, tc := range tests {
		if got := domain.JaroWinkler(tc.a, tc.b); math.Abs(got-tc.want) > 0.001 {
			t.Errorf("JaroWinkler(%q,%q) = %.4f, want %.4f", tc.a, tc.b, got, tc.want)
		}
	}
}
