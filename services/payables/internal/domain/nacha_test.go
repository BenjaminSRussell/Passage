package domain_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/payables/internal/domain"
)

const (
	nachaFixtureDir = "../../testdata/nacha"
	hashModulus     = 10000000000
)

var nachaFixtures = []string{"ccd-10", "ppd", "ctx", "prenote"}

type nachaSummary struct {
	entries, addenda, maxAddenda int
	credit                       int64
}

func loadNachaDTO(t tester, name string) domain.NachaInputDTO {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(nachaFixtureDir, name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var dto domain.NachaInputDTO
	if err := json.Unmarshal(raw, &dto); err != nil {
		t.Fatal(err)
	}
	return dto
}

func buildNacha(dto domain.NachaInputDTO) (domain.PaymentFile, error) {
	in, err := domain.ParseNachaInput(dto)
	if err != nil {
		return domain.PaymentFile{}, err
	}
	return domain.GenerateNacha(in)
}

func atoi(t tester, raw string) int64 {
	t.Helper()
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		t.Fatalf("not a number %q: %v", raw, err)
	}
	return v
}

func requireEqual(t tester, what string, got, want int64) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: got %d, want %d", what, got, want)
	}
}

func validateNacha(t tester, content []byte, prenote bool) nachaSummary {
	t.Helper()
	text := string(content)
	if !strings.HasSuffix(text, "\n") {
		t.Fatal("file does not end with a newline")
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	for i, line := range lines {
		if len(line) != 94 {
			t.Fatalf("line %d is %d characters", i+1, len(line))
		}
		for _, c := range []byte(line) {
			if c < 0x20 || c > 0x7e {
				t.Fatalf("line %d has a non-ASCII byte", i+1)
			}
		}
	}
	if len(lines)%10 != 0 {
		t.Fatalf("%d lines is not a multiple of 10", len(lines))
	}
	if lines[0][0] != '1' {
		t.Fatal("first record is not a file header")
	}

	var sum nachaSummary
	var fileHash, fileDebit, batches int64
	lastTrace := ""
	i := 1
	for ; lines[i][0] == '5'; i++ {
		batches++
		header := lines[i]
		requireEqual(t, "batch number", atoi(t, header[87:94]), batches)
		var count, hash, debit, credit int64
		for i++; lines[i][0] == '6'; {
			entry := lines[i]
			code := entry[1:3]
			if prenote && code != "23" && code != "33" {
				t.Fatalf("prenote file holds transaction code %s", code)
			}
			cents := atoi(t, entry[29:39])
			if prenote {
				requireEqual(t, "prenote amount", cents, 0)
			}
			switch code {
			case "22", "23", "32", "33":
				credit += cents
			default:
				debit += cents
			}
			hash += atoi(t, entry[3:11])
			trace := entry[79:94]
			if trace <= lastTrace {
				t.Fatalf("trace %s not ascending after %s", trace, lastTrace)
			}
			lastTrace = trace
			count++
			sum.entries++
			addenda := 0
			for i++; lines[i][0] == '7'; i++ {
				addenda++
				if lines[i][1:3] != "05" {
					t.Fatalf("addenda type %s", lines[i][1:3])
				}
				requireEqual(t, "addenda sequence", atoi(t, lines[i][83:87]), int64(addenda))
				if lines[i][87:94] != trace[8:15] {
					t.Fatalf("addenda entry sequence %s does not match trace %s", lines[i][87:94], trace)
				}
			}
			count += int64(addenda)
			sum.addenda += addenda
			sum.maxAddenda = max(sum.maxAddenda, addenda)
			if wantIndicator := map[bool]string{true: "1", false: "0"}[addenda > 0]; entry[78:79] != wantIndicator {
				t.Fatalf("addenda indicator %s with %d addenda", entry[78:79], addenda)
			}
			if header[50:53] == "CTX" {
				requireEqual(t, "CTX addenda count", atoi(t, entry[54:58]), int64(addenda))
			} else if addenda > 1 {
				t.Fatalf("%s entry has %d addenda", header[50:53], addenda)
			}
		}
		control := lines[i]
		if control[0] != '8' {
			t.Fatalf("record %d is %c, want batch control", i+1, control[0])
		}
		hash %= hashModulus
		requireEqual(t, "batch entry/addenda count", atoi(t, control[4:10]), count)
		requireEqual(t, "batch entry hash", atoi(t, control[10:20]), hash)
		requireEqual(t, "batch total debit", atoi(t, control[20:32]), debit)
		requireEqual(t, "batch total credit", atoi(t, control[32:44]), credit)
		requireEqual(t, "batch control number", atoi(t, control[87:94]), batches)
		if control[44:54] != header[40:50] || control[79:87] != header[79:87] {
			t.Fatal("batch control company id or ODFI differs from the batch header")
		}
		fileHash += hash
		fileDebit += debit
		sum.credit += credit
	}
	control := lines[i]
	if control[0] != '9' {
		t.Fatalf("record %d is %c, want file control", i+1, control[0])
	}
	requireEqual(t, "file batch count", atoi(t, control[1:7]), batches)
	requireEqual(t, "file block count", atoi(t, control[7:13]), int64(len(lines)/10))
	requireEqual(t, "file entry/addenda count", atoi(t, control[13:21]), int64(sum.entries+sum.addenda))
	requireEqual(t, "file entry hash", atoi(t, control[21:31]), fileHash%hashModulus)
	requireEqual(t, "file total debit", atoi(t, control[31:43]), fileDebit)
	requireEqual(t, "file total credit", atoi(t, control[43:55]), sum.credit)
	if strings.TrimSpace(control[55:94]) != "" {
		t.Fatal("file control reserved positions are not blank")
	}
	for _, padding := range lines[i+1:] {
		if padding != strings.Repeat("9", 94) {
			t.Fatalf("padding line %q is not all 9s", padding)
		}
	}
	return sum
}

func TestGenerateNacha_Golden(t *testing.T) {
	for _, name := range nachaFixtures {
		t.Run(name, func(t *testing.T) {
			dto := loadNachaDTO(t, name)
			file, err := buildNacha(dto)
			if err != nil {
				t.Fatal(err)
			}
			again, err := buildNacha(dto)
			if err != nil || again.SHA256 != file.SHA256 {
				t.Fatalf("generation is not deterministic: %v", err)
			}
			digest := sha256.Sum256(file.Content)
			if file.Format != domain.FormatNacha || file.SHA256 != hex.EncodeToString(digest[:]) {
				t.Fatalf("format %s or digest %s wrong", file.Format, file.SHA256)
			}

			sum := validateNacha(t, file.Content, dto.Prenote)
			requireEqual(t, "payment count", int64(file.PaymentCount), int64(sum.entries))
			if want := decimal.New(sum.credit, -2); !file.ControlSum.Equal(want) {
				t.Fatalf("control sum %s, want %s", file.ControlSum, want)
			}
			if name == "ctx" && sum.maxAddenda < 3 {
				t.Fatalf("ctx fixture has %d addenda, want at least 3", sum.maxAddenda)
			}
			if name == "prenote" && !file.ControlSum.IsZero() {
				t.Fatalf("prenote control sum %s", file.ControlSum)
			}

			golden := filepath.Join(nachaFixtureDir, name+".golden.ach")
			if *update {
				if err := os.WriteFile(golden, file.Content, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("golden missing, regenerate with -update: %v", err)
			}
			if string(want) != string(file.Content) {
				t.Fatalf("generated file differs from %s; regenerate with go test ./internal/domain -run Golden -update", golden)
			}
		})
	}
}

func TestGenerateNacha_OrdersBatchesAndTraces(t *testing.T) {
	dto := loadNachaDTO(t, "ccd-10")
	dto.Entries[0].EffectiveDate = "2026-03-09"
	file, err := buildNacha(dto)
	if err != nil {
		t.Fatal(err)
	}
	validateNacha(t, file.Content, false)
	var dates []string
	for _, line := range strings.Split(string(file.Content), "\n") {
		if strings.HasPrefix(line, "5") {
			dates = append(dates, line[69:75])
		}
	}
	if strings.Join(dates, ",") != "260304,260305,260309" {
		t.Fatalf("batch dates %v", dates)
	}
}

func TestGenerateNacha_RejectsInvalid(t *testing.T) {
	cases := []struct {
		name   string
		code   string
		detail string
		mutate func(*domain.NachaInputDTO)
	}{
		{"bad receiving routing", domain.CodeRunInvalid, "aba", func(d *domain.NachaInputDTO) { d.Entries[0].RoutingNumber = "021000022" }},
		{"non-USD currency", "PAY-0001", "currency", func(d *domain.NachaInputDTO) { d.Entries[0].Currency = "EUR" }},
		{"three decimals", "PAY-0001", "decimal", func(d *domain.NachaInputDTO) { d.Entries[0].Amount = "1.005" }},
		{"oversize amount", "PAY-0001", "10 digits", func(d *domain.NachaInputDTO) { d.Entries[0].Amount = "100000000.00" }},
		{"zero payment amount", "PAY-0001", "greater than zero", func(d *domain.NachaInputDTO) { d.Entries[0].Amount = "0.00" }},
		{"non-zero prenote amount", "PAY-0001", "zero amount", func(d *domain.NachaInputDTO) { d.Prenote = true }},
		{"bad file id modifier", "PAY-0001", "fileIdModifier", func(d *domain.NachaInputDTO) { d.FileIDModifier = "a" }},
		{"unmappable name", domain.CodeFileInvalid, "name", func(d *domain.NachaInputDTO) { d.Entries[0].Name = "Vendor 東京" }},
		{"no entries", "PAY-0001", "entry", func(d *domain.NachaInputDTO) { d.Entries = nil }},
		{"account with dash", "PAY-0001", "****", func(d *domain.NachaInputDTO) { d.Entries[0].AccountNumber = "9000-0037" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dto := loadNachaDTO(t, "ccd-10")
			c.mutate(&dto)
			_, err := buildNacha(dto)
			var de *domain.Error
			if !errors.As(err, &de) || de.Code != c.code || !strings.Contains(de.Detail, c.detail) {
				t.Fatalf("got %v, want %s mentioning %q", err, c.code, c.detail)
			}
			if strings.Contains(de.Detail, "9000000037") || strings.Contains(de.Detail, "90000") {
				t.Fatalf("detail echoes an account number: %s", de.Detail)
			}
		})
	}
}
