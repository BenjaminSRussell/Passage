package store

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const SysTenantID = "00000000-0000-0000-0000-000000000000"

var (
	countryCode = regexp.MustCompile(`^[A-Z]{2}$`)
	currencyISO = regexp.MustCompile(`^[A-Z]{3}$`)
	numericISO  = regexp.MustCompile(`^[0-9]{3}$`)
	minorUnits  = regexp.MustCompile(`^[0-4]$`)
	regionCode  = regexp.MustCompile(`^[A-Z]{2}-[A-Z0-9]{1,3}$`)
	languageISO = regexp.MustCompile(`^[a-z]{2}$`)
)

type dataset struct {
	file    string
	table   string
	columns []string
	parse   func(rec []string) ([]any, error)
}

var datasets = []dataset{
	{"countries.csv", "country", []string{"code", "name", "region_required", "postal_regex"}, parseCountry},
	{"currencies.csv", "currency", []string{"code", "numeric_code", "name", "minor_units"}, parseCurrency},
	{"regions.csv", "region", []string{"code", "country_code", "name"}, parseRegion},
	{"languages.csv", "language", []string{"code", "name"}, parseLanguage},
}

func parseCountry(rec []string) ([]any, error) {
	if !countryCode.MatchString(rec[0]) {
		return nil, fmt.Errorf("code %q is not two uppercase letters", rec[0])
	}
	if rec[1] == "" {
		return nil, errors.New("name is empty")
	}
	if rec[2] != "true" && rec[2] != "false" {
		return nil, fmt.Errorf("region_required %q must be true or false", rec[2])
	}
	var postal any
	if rec[3] != "" {
		if _, err := regexp.Compile(rec[3]); err != nil {
			return nil, fmt.Errorf("postal_regex: %w", err)
		}
		postal = rec[3]
	}
	return []any{rec[0], rec[1], rec[2] == "true", postal}, nil
}

func parseCurrency(rec []string) ([]any, error) {
	if !currencyISO.MatchString(rec[0]) {
		return nil, fmt.Errorf("code %q is not three uppercase letters", rec[0])
	}
	if !numericISO.MatchString(rec[1]) {
		return nil, fmt.Errorf("numeric_code %q is not three digits", rec[1])
	}
	if rec[2] == "" {
		return nil, errors.New("name is empty")
	}
	if !minorUnits.MatchString(rec[3]) {
		return nil, fmt.Errorf("minor_units %q must be 0 to 4", rec[3])
	}
	units, _ := strconv.Atoi(rec[3])
	return []any{rec[0], rec[1], rec[2], units}, nil
}

func parseRegion(rec []string) ([]any, error) {
	if !regionCode.MatchString(rec[0]) {
		return nil, fmt.Errorf("code %q is not an ISO 3166-2 code", rec[0])
	}
	if !countryCode.MatchString(rec[1]) {
		return nil, fmt.Errorf("country_code %q is not two uppercase letters", rec[1])
	}
	if rec[0][:2] != rec[1] {
		return nil, fmt.Errorf("code %q does not start with country_code %q", rec[0], rec[1])
	}
	if rec[2] == "" {
		return nil, errors.New("name is empty")
	}
	return []any{rec[0], rec[1], rec[2]}, nil
}

func parseLanguage(rec []string) ([]any, error) {
	if !languageISO.MatchString(rec[0]) {
		return nil, fmt.Errorf("code %q is not two lowercase letters", rec[0])
	}
	if rec[1] == "" {
		return nil, errors.New("name is empty")
	}
	return []any{rec[0], rec[1]}, nil
}

func ParseReference(files fs.FS, file string) ([][]any, error) {
	i := slices.IndexFunc(datasets, func(d dataset) bool { return d.file == file })
	if i < 0 {
		return nil, fmt.Errorf("unknown reference file %q", file)
	}
	return datasets[i].load(files)
}

func (d dataset) load(files fs.FS) ([][]any, error) {
	f, err := files.Open("reference/" + d.file)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", d.file, err)
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.FieldsPerRecord = len(d.columns)
	var rows [][]any
	for header := true; ; header = false {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			if header {
				return nil, fmt.Errorf("%s line 1: file is empty", d.file)
			}
			return rows, nil
		}
		var parseErr *csv.ParseError
		if errors.As(err, &parseErr) {
			return nil, fmt.Errorf("%s line %d: %w", d.file, parseErr.Line, parseErr.Err)
		}
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", d.file, err)
		}
		line, _ := r.FieldPos(0)
		if header {
			if !slices.Equal(rec, d.columns) {
				return nil, fmt.Errorf("%s line %d: header %s, want %s", d.file, line, strings.Join(rec, ","), strings.Join(d.columns, ","))
			}
			continue
		}
		row, err := d.parse(rec)
		if err != nil {
			return nil, fmt.Errorf("%s line %d: %w", d.file, line, err)
		}
		rows = append(rows, row)
	}
}

func SeedReference(ctx context.Context, pool *pgxpool.Pool, files fs.FS) error {
	loaded := make([][][]any, len(datasets))
	for i, d := range datasets {
		rows, err := d.load(files)
		if err != nil {
			return err
		}
		loaded[i] = rows
	}
	ctx = tenant.With(ctx, tenant.Principal{TenantID: SysTenantID})
	return dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		for i, d := range datasets {
			if err := d.insert(ctx, tx, loaded[i]); err != nil {
				return fmt.Errorf("insert %s: %w", d.table, err)
			}
		}
		return nil
	})
}

func (d dataset) insert(ctx context.Context, tx pgx.Tx, rows [][]any) error {
	placeholders := make([]string, len(d.columns))
	for i := range placeholders {
		placeholders[i] = "$" + strconv.Itoa(i+2)
	}
	statement := "insert into " + d.table + " (tenant_id, " + strings.Join(d.columns, ", ") +
		") values ($1, " + strings.Join(placeholders, ", ") + ") on conflict (tenant_id, code) do nothing"
	batch := &pgx.Batch{}
	for _, row := range rows {
		batch.Queue(statement, append([]any{SysTenantID}, row...)...)
	}
	results := tx.SendBatch(ctx, batch)
	for range rows {
		if _, err := results.Exec(); err != nil {
			_ = results.Close()
			return err
		}
	}
	return results.Close()
}
