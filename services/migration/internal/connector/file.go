package connector

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/text/encoding/ianaindex"
	"golang.org/x/text/transform"

	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
)

const (
	// MaxUploadBytes bounds a file sent with a request.
	MaxUploadBytes = 16 << 20
	// MaxDropFileBytes bounds a file read from the drop directory.
	MaxDropFileBytes = 256 << 20
	maxJSONLine      = 4 << 20
	maxDropEntries   = 5000
)

// File reads a CSV, TSV, JSON (an array of objects) or NDJSON file, from the drop directory under os.Root (which
// refuses to leave the root, symlinks included) or from an upload. Only a regular file is read (a FIFO or a device
// is refused). The file is read and parsed once per extraction, on the first page, and the later pages come from
// that parse; the position is the index of the next record.
type File struct {
	parsed bool
	rows   []Row
	since  string
	all    Page

	load        func() ([]byte, string, error)
	format      string
	delimiter   rune
	codepage    string
	recordsPath string
	cursorField string
	cursorType  string
}

func newFile(e Env, s Spec) (*File, error) {
	str := func(k string) string { v, _ := s.Profile[k].(string); return v }
	src, err := domain.ParseFileSource(s.Source)
	if err != nil {
		return nil, err
	}
	f := &File{format: str("format"), codepage: str("codepage"), recordsPath: src.RecordsPath, cursorField: s.CursorField, cursorType: s.CursorType}
	if f.format == "tsv" {
		f.delimiter = '\t'
	}
	if d := []rune(str("delimiter")); len(d) == 1 {
		f.delimiter = d[0]
	}
	if f.delimiter == 0 {
		f.delimiter = ','
	}
	if s.Upload != nil {
		if len(s.Upload.Content) > MaxUploadBytes {
			return nil, fmt.Errorf("the upload is larger than %d MiB", MaxUploadBytes>>20)
		}
		f.format = formatFor(f.format, s.Upload.Name)
		content := s.Upload.Content
		f.load = func() ([]byte, string, error) { return content, s.Upload.Name, nil }
		return f, nil
	}
	if e.DropRoot == "" {
		return nil, errors.New("no drop directory is mounted for this deployment (MIG_DROP_ROOT is not set); ask the operator, or send the file with the request")
	}
	dir, _ := s.Profile["dropDir"].(string)
	pattern := src.File
	if pattern == "" {
		pattern = src.FilePattern
	}
	if pattern == "" {
		pattern = str("filePattern")
	}
	f.load = func() ([]byte, string, error) { return readDrop(e.DropRoot, dir, pattern) }
	return f, nil
}

// formatFor picks the format of an upload: the profile's, or the file name's extension.
func formatFor(profile, name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".csv":
		return "csv"
	case ".tsv":
		return "tsv"
	case ".json":
		return "json"
	case ".jsonl", ".ndjson":
		return "jsonl"
	}
	return profile
}

func readDrop(root, dir, pattern string) ([]byte, string, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, "", errors.New("the drop root cannot be opened; ask the operator")
	}
	defer r.Close()
	d, err := r.Open(dir)
	if err != nil {
		return nil, "", errors.New("the drop directory does not exist or is not readable")
	}
	entries, err := d.ReadDir(maxDropEntries)
	d.Close()
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, "", errors.New("the drop directory cannot be listed")
	}
	var names []string
	for _, e := range entries {
		if !e.Type().IsRegular() && e.Type()&fs.ModeSymlink == 0 {
			continue // directories, FIFOs, sockets and devices are never read; a symlink is checked once resolved
		}
		if ok, _ := path.Match(pattern, e.Name()); ok {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return nil, "", errors.New("no file in the drop directory matches " + pattern)
	}
	slices.Sort(names)
	name := names[len(names)-1] // the newest by name: drops carry a date or a counter
	// Stat first so a FIFO is never opened (opening one blocks until a writer appears); open without blocking and
	// check the handle too, in case the name was replaced in between.
	if st, err := r.Stat(path.Join(dir, name)); err != nil || !st.Mode().IsRegular() {
		return nil, "", errors.New("the matching file " + name + " is not a regular file")
	}
	fh, err := r.OpenFile(path.Join(dir, name), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, "", errors.New("the file cannot be opened")
	}
	defer fh.Close()
	if st, err := fh.Stat(); err != nil || !st.Mode().IsRegular() {
		return nil, "", errors.New("the matching file " + name + " is not a regular file")
	}
	body, err := io.ReadAll(io.LimitReader(fh, MaxDropFileBytes+1))
	if err != nil {
		return nil, "", errors.New("the file cannot be read")
	}
	if len(body) > MaxDropFileBytes {
		return nil, "", fmt.Errorf("the file is larger than %d MiB", MaxDropFileBytes>>20)
	}
	return body, name, nil
}

func (f *File) Read(_ context.Context, q Query) (Page, error) {
	if q.Limit < 1 || q.Limit > domain.MaxBatch {
		return Page{}, fmt.Errorf("limit must be 1 to %d", domain.MaxBatch)
	}
	start := 0
	if q.Position != "" {
		n, err := strconv.Atoi(q.Position)
		if err != nil || n < 0 {
			return Page{}, errors.New("position is not one this binding returned")
		}
		start = n
	}
	if !f.parsed || f.since != q.Since {
		if !f.parsed {
			body, name, err := f.load()
			if err != nil {
				return Page{}, err
			}
			rows, err := f.parse(body)
			if err != nil {
				return Page{}, fmt.Errorf("%s: %w", name, err)
			}
			f.rows, f.parsed = rows, true
		}
		// The watermark covers the whole file so a delta over a re-dropped file is exact; paging is over the rows
		// newer than since. finish filters in place, so it gets its own copy of the parsed rows.
		all := Page{Rows: slices.Clone(f.rows)}
		all, err := finish(&all, f.cursorField, f.cursorType, q.Since, true)
		if err != nil {
			return Page{}, err
		}
		f.all, f.since = all, q.Since
	}
	all := f.all
	end := min(start+q.Limit, len(all.Rows))
	if start > len(all.Rows) {
		start = len(all.Rows)
	}
	page := Page{Rows: all.Rows[start:end], Watermark: all.Watermark}
	if end < len(all.Rows) {
		page.Next = strconv.Itoa(end)
	}
	return page, nil
}

func (f *File) decode(body []byte) ([]byte, error) {
	body = bytes.TrimPrefix(body, []byte{0xEF, 0xBB, 0xBF})
	cp := strings.ToLower(f.codepage)
	if cp == "" || cp == "utf-8" || cp == "utf8" {
		return body, nil
	}
	enc, err := ianaindex.IANA.Encoding(f.codepage)
	if err != nil || enc == nil {
		return nil, errors.New("the code page " + f.codepage + " is not supported")
	}
	out, _, err := transform.Bytes(enc.NewDecoder(), body)
	if err != nil {
		return nil, errors.New("the file is not valid " + f.codepage)
	}
	return out, nil
}

func (f *File) parse(raw []byte) ([]Row, error) {
	body, err := f.decode(raw)
	if err != nil {
		return nil, err
	}
	switch f.format {
	case "csv", "tsv":
		return f.parseCSV(body)
	case "json":
		return f.parseJSON(body)
	case "jsonl":
		return parseNDJSON(body)
	}
	return nil, errors.New("the file format is not set; the profile needs csv, tsv, json or jsonl")
}

func (f *File) parseCSV(body []byte) ([]Row, error) {
	r := csv.NewReader(bytes.NewReader(body))
	r.Comma = f.delimiter
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	head, err := r.Read()
	if errors.Is(err, io.EOF) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("header: %w", err)
	}
	for i, h := range head {
		head[i] = strings.TrimSpace(h)
		if head[i] == "" {
			return nil, fmt.Errorf("header column %d has no name", i+1)
		}
	}
	if dup := firstDuplicate(head); dup != "" {
		return nil, fmt.Errorf("the header repeats column %s", dup)
	}
	var rows []Row
	for line := 2; ; line++ {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			return rows, nil
		}
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		if len(rec) == 1 && strings.TrimSpace(rec[0]) == "" {
			continue
		}
		if len(rec) != len(head) {
			return nil, fmt.Errorf("line %d has %d fields, the header has %d", line, len(rec), len(head))
		}
		row := make(Row, len(head))
		for i, h := range head {
			row[h] = rec[i]
		}
		rows = append(rows, row)
		if len(rows) > domain.MaxExtractRows {
			return nil, errors.New("the file has too many rows")
		}
	}
}

func firstDuplicate(s []string) string {
	seen := map[string]bool{}
	for _, v := range s {
		if seen[v] {
			return v
		}
		seen[v] = true
	}
	return ""
}

func (f *File) parseJSON(body []byte) ([]Row, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return nil, errors.New("not valid JSON")
	}
	recs, err := records(doc, f.recordsPath)
	if err != nil {
		return nil, err
	}
	rows := make([]Row, 0, len(recs))
	for _, rec := range recs {
		row, ok := rec.(map[string]any)
		if !ok {
			return nil, errors.New("the records are not JSON objects")
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func parseNDJSON(body []byte) ([]Row, error) {
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 64<<10), maxJSONLine)
	var rows []Row
	for line := 1; sc.Scan(); line++ {
		text := bytes.TrimSpace(sc.Bytes())
		if len(text) == 0 {
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(text))
		dec.UseNumber()
		var row Row
		if err := dec.Decode(&row); err != nil || row == nil {
			return nil, fmt.Errorf("line %d is not a JSON object", line)
		}
		rows = append(rows, row)
	}
	if err := sc.Err(); err != nil {
		return nil, errors.New("a line is too long or the file cannot be read")
	}
	return rows, nil
}
