// Package connector reads source rows from a connection profile: a SQL table or query (any driver in the probe
// registry), a REST endpoint with paging and delta parameters, or a CSV, JSON or NDJSON file. Every network
// connection goes through the egress guard and every file read stays under the operator's drop root. Nothing here
// writes to the source or logs a credential, a query's parameters or a row.
package connector

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/egress"
	"github.com/BenjaminSRussell/passage/services/migration/internal/probe"
)

// Row is one source record as read: column or JSON field name to value.
type Row = map[string]any

// Query asks a Reader for one page.
type Query struct {
	// Since is the last watermark; only rows changed after it are read ("" reads everything).
	Since string
	// Position is the Next of the previous page ("" starts).
	Position string
	// Limit is the most rows wanted, 1 to domain.MaxBatch.
	Limit int
}

// Page is one page of rows.
type Page struct {
	Rows []Row
	// Next continues after this page; "" means the source is exhausted.
	Next string
	// Watermark is the largest cursor value among Rows ("" without a cursor or rows).
	Watermark string
}

// Reader reads pages from one binding's source.
type Reader interface {
	Read(ctx context.Context, q Query) (Page, error)
}

// Env is what the deployment gives extraction: the egress guard and the drop root, both operator-set.
type Env struct {
	Guard    *egress.Guard
	DropRoot string
}

// Upload is file content sent with the request instead of read from the drop directory.
type Upload struct {
	Name    string
	Content []byte
}

// Spec is everything needed to open a reader for a binding.
type Spec struct {
	Kind        string
	Profile     map[string]any
	Secret      string
	Source      map[string]any
	KeyFields   []string
	CursorField string
	CursorType  string
	Upload      *Upload
}

// Opened is a reader and what to close when done.
type Opened struct {
	Reader
	closer func()
}

func (o Opened) Close() {
	if o.closer != nil {
		o.closer()
	}
}

// ErrNoUpload is returned for an upload sent to a profile kind that reads no files.
var ErrNoUpload = errors.New("an upload can only be read by a file profile")

// Open validates the binding against its profile once more (stored rows may predate a rule) and opens a reader.
func (e Env) Open(s Spec) (Opened, error) {
	if s.Upload != nil && s.Kind != domain.ConnFile {
		return Opened{}, ErrNoUpload
	}
	b := domain.EntityBinding{Entity: "x", Source: s.Source, KeyFields: s.KeyFields, CursorField: s.CursorField, CursorType: s.CursorType}
	if bad := domain.ValidateBinding(b, s.Kind); len(bad) > 0 {
		return Opened{}, fmt.Errorf("binding is invalid: %s", firstProblem(bad))
	}
	switch s.Kind {
	case domain.ConnSQL:
		src, _ := domain.ParseSQLSource(s.Source)
		db, drv, err := probe.OpenSQL(e.Guard, s.Profile, s.Secret)
		if err != nil {
			return Opened{}, scrubErr(err, s.Secret)
		}
		return Opened{Reader: &SQL{DB: db, Driver: drv, Src: src, Keys: s.KeyFields, CursorField: s.CursorField, CursorType: s.CursorType}, closer: func() { _ = db.Close() }}, nil
	case domain.ConnREST:
		r, err := newREST(e, s)
		return Opened{Reader: r}, err
	case domain.ConnFile:
		r, err := newFile(e, s)
		return Opened{Reader: r}, err
	}
	return Opened{}, fmt.Errorf("unknown connection kind %q", s.Kind)
}

func firstProblem(bad map[string]string) string {
	for _, k := range []string{"source", "source.query", "source.table", "source.path", "keyFields", "cursorField", "cursorType"} {
		if m, ok := bad[k]; ok {
			return k + " " + m
		}
	}
	for k, m := range bad {
		return k + " " + m
	}
	return ""
}

// scrubErr removes the secret from an error before it can reach a log or a response.
func scrubErr(err error, secret string) error {
	if err == nil || secret == "" {
		return err
	}
	msg := err.Error()
	if !strings.Contains(msg, secret) {
		return err
	}
	return errors.New(strings.ReplaceAll(msg, secret, "***"))
}

// scalar converts a driver value to what the mapping DSL reads: text, numbers and booleans as they are, timestamps as
// fixed-width UTC RFC 3339, byte strings as text when valid UTF-8 and hex otherwise.
func scalar(v any) any {
	switch x := v.(type) {
	case []byte:
		if isText(x) {
			return string(x)
		}
		return fmt.Sprintf("%x", x)
	case time.Time:
		return domain.FormatTimestamp(x)
	}
	return v
}

func isText(b []byte) bool {
	s := string(b)
	for _, r := range s {
		if r == 0xFFFD || r < ' ' && r != '\n' && r != '\r' && r != '\t' {
			return false
		}
	}
	return true
}

// httpClient is the guard's client; a test guard keeps its own TLS roots.
func httpClient(e Env) (*http.Client, error) {
	if e.Guard == nil {
		return nil, errors.New("outbound connections are not configured on this deployment")
	}
	return e.Guard.HTTPClient(), nil
}
