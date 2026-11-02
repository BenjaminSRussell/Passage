// Package probe checks a connection profile: it connects the way an extractor will and reports what it found, so a
// customer learns about a wrong host, password or drop directory before a run depends on it. Every network connection
// goes through the egress guard. A probe never returns the secret and never includes response bodies or row data.
package probe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	mssql "github.com/microsoft/go-mssqldb"

	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/egress"
	"github.com/BenjaminSRussell/passage/services/migration/internal/oauth"
)

// Failure codes a probe reports (the HTTP call itself still answers 200: the probe ran, the connection did not work).
const (
	CodeEgressDenied   = "MIG-EGRESS-DENIED"
	CodeConnectFailed  = "MIG-CONNECT-FAILED"
	CodeAuthFailed     = "MIG-AUTH-FAILED"
	CodeProbeFailed    = "MIG-PROBE-FAILED"
	CodeDropNotSet     = "MIG-DROP-NOT-CONFIGURED"
	CodeDropMissing    = "MIG-DROP-MISSING"
	CodeNoSecret       = "MIG-SECRET-MISSING"
	maxSchemas         = 50
	maxTables          = 200
	maxColumnTables    = 20
	maxColumnsPerTable = 100
	maxFiles           = 20
	maxDirEntries      = 5000
)

// Target is what a probe needs: the stored settings and the opened secret.
type Target struct {
	Kind   string
	Config map[string]any
	Secret string
}

type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Column struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Nullable bool   `json:"nullable"`
}

type Table struct {
	Schema  string   `json:"schema"`
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Columns []Column `json:"columns,omitempty"`
}

type File struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
}

// Result is the probe report shown on the connection page.
type Result struct {
	OK            bool     `json:"ok"`
	Kind          string   `json:"kind"`
	DurationMs    int64    `json:"durationMs"`
	Failure       *Failure `json:"failure,omitempty"`
	ServerVersion string   `json:"serverVersion,omitempty"`
	Schemas       []string `json:"schemas,omitempty"`
	Tables        []Table  `json:"tables,omitempty"`
	HTTPStatus    int      `json:"httpStatus,omitempty"`
	FileCount     int      `json:"fileCount,omitempty"`
	Files         []File   `json:"files,omitempty"`
	// Truncated is true when the schema, table, column or file lists were cut at their limits.
	Truncated bool `json:"truncated,omitempty"`
}

// Prober runs probes through one egress guard.
type Prober struct {
	Guard *egress.Guard
	// DropRoot is the operator-mounted directory file profiles are relative to (MIG_DROP_ROOT); empty disables file probes.
	DropRoot string
	Timeout  time.Duration
}

func (p *Prober) timeout() time.Duration {
	if p.Timeout > 0 {
		return p.Timeout
	}
	return 15 * time.Second
}

// Probe checks one profile. The secret is scrubbed from every message it reports.
func (p *Prober) Probe(ctx context.Context, t Target) Result {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, p.timeout())
	defer cancel()
	res := Result{Kind: t.Kind}
	var err error
	switch t.Kind {
	case domain.ConnSQL:
		err = p.sql(ctx, t, &res)
	case domain.ConnREST:
		err = p.rest(ctx, t, &res)
	case domain.ConnFile:
		err = p.file(t, &res)
	default:
		err = &failure{CodeProbeFailed, "unknown connection kind"}
	}
	res.DurationMs = time.Since(start).Milliseconds()
	if err != nil {
		res.OK = false
		res.Failure = Classify(err, t.Secret)
	} else {
		res.OK = true
	}
	return res
}

type failure struct{ code, msg string }

func (f *failure) Error() string { return f.msg }

// Classify turns an error from a connection or a read into a failure code and a scrubbed, capped message.
func Classify(err error, secret string) *Failure {
	var f *failure
	var pg *pgconn.PgError
	var ms mssql.Error
	code := CodeConnectFailed
	switch {
	case errors.As(err, &f):
		code = f.code
	case errors.Is(err, egress.ErrDenied):
		code = CodeEgressDenied
	case errors.As(err, &pg) && strings.HasPrefix(pg.Code, "28"):
		code = CodeAuthFailed
	case errors.As(err, &ms) && ms.Number == 18456: // SQL Server: login failed
		code = CodeAuthFailed
	case errors.Is(err, context.DeadlineExceeded):
		return &Failure{CodeConnectFailed, "timed out"}
	case errors.As(err, &pg):
		code = CodeProbeFailed
	}
	return &Failure{Code: code, Message: scrub(err.Error(), secret)}
}

// scrub removes the secret from a message, also in the escaped forms a URL, DSN or quoted error would carry, and caps it.
func scrub(msg, secret string) string {
	if secret != "" {
		quoted := strconv.Quote(secret)
		for _, form := range []string{secret, url.QueryEscape(secret), url.PathEscape(secret), quoted[1 : len(quoted)-1]} {
			msg = strings.ReplaceAll(msg, form, "***")
		}
	}
	if len(msg) > 400 {
		msg = strings.ToValidUTF8(msg[:400], "")
	}
	return msg
}

func str(cfg map[string]any, k string) string {
	s, _ := cfg[k].(string)
	return s
}

func (p *Prober) rest(ctx context.Context, t Target, res *Result) error {
	if p.Guard == nil {
		return &failure{CodeEgressDenied, "outbound connections are not configured on this deployment"}
	}
	authType := str(t.Config, "authType")
	if authType == "" {
		authType = "none"
	}
	if authType != "none" && t.Secret == "" {
		return &failure{CodeNoSecret, "no secret is set for this connection; set the " + map[string]string{"basic": "password", "bearer": "token", "header": "header value", domain.AuthOAuth2: "client secret"}[authType] + " first"}
	}
	target := strings.TrimRight(str(t.Config, "baseUrl"), "/") + str(t.Config, "probePath")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return &failure{CodeProbeFailed, "invalid base URL"}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "passage-migration-probe")
	switch authType {
	case "basic":
		req.SetBasicAuth(str(t.Config, "username"), t.Secret)
	case "bearer":
		req.Header.Set("Authorization", "Bearer "+t.Secret)
	case "header":
		req.Header.Set(str(t.Config, "headerName"), t.Secret)
	case domain.AuthOAuth2:
		tok, err := oauth.ClientCredentials(ctx, p.Guard.HTTPClient(), str(t.Config, "authUrl"), str(t.Config, "clientId"), t.Secret, str(t.Config, "scope"))
		if err != nil {
			return &failure{CodeAuthFailed, err.Error()}
		}
		req.Header.Set("Authorization", "Bearer "+tok.Value)
	}
	resp, err := p.Guard.HTTPClient().Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // the URL error would repeat the request URL
		}
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	res.HTTPStatus = resp.StatusCode
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return &failure{CodeAuthFailed, fmt.Sprintf("the endpoint answered %d; check the credentials", resp.StatusCode)}
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return &failure{CodeProbeFailed, fmt.Sprintf("the endpoint answered %d (a redirect); redirects are not followed, use the final base URL", resp.StatusCode)}
	case resp.StatusCode >= 400:
		return &failure{CodeProbeFailed, fmt.Sprintf("the endpoint answered %d", resp.StatusCode)}
	}
	return nil
}

func (p *Prober) file(t Target, res *Result) error {
	if p.DropRoot == "" {
		return &failure{CodeDropNotSet, "no drop directory is mounted for this deployment (MIG_DROP_ROOT is not set); ask the operator"}
	}
	root, err := os.OpenRoot(p.DropRoot)
	if err != nil {
		return &failure{CodeDropNotSet, "the drop root cannot be opened; ask the operator"}
	}
	defer root.Close()
	dir, err := root.Open(str(t.Config, "dropDir")) // os.Root refuses to leave the root, symlinks included
	if err != nil {
		return &failure{CodeDropMissing, "the drop directory does not exist or is not readable"}
	}
	defer dir.Close()
	if st, err := dir.Stat(); err != nil || !st.IsDir() {
		return &failure{CodeDropMissing, "the drop directory path is not a directory"}
	}
	entries, err := dir.ReadDir(maxDirEntries)
	if err != nil && !errors.Is(err, io.EOF) {
		return &failure{CodeDropMissing, "the drop directory cannot be listed"}
	}
	if len(entries) == maxDirEntries {
		res.Truncated = true // only the first maxDirEntries entries were looked at
	}
	pattern := str(t.Config, "filePattern")
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if ok, _ := path.Match(pattern, e.Name()); pattern != "" && !ok {
			continue
		}
		res.FileCount++
		if len(res.Files) < maxFiles {
			f := File{Name: e.Name()}
			if info, err := e.Info(); err == nil {
				f.Bytes = info.Size()
			}
			res.Files = append(res.Files, f)
		} else {
			res.Truncated = true
		}
	}
	return nil
}
