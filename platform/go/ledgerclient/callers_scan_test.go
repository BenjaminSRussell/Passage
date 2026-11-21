package ledgerclient_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const (
	journalEntriesPath = "/v1/ledger/journal-entries"
	idempotencyHeader  = "Idempotency-Key"
)

var scannedRoots = []string{"../../../services", "../../../tools"}

type callerScan struct {
	violations   []string
	builderUsers map[string]bool
	looseKeys    map[string][]string
}

func ownerDir(path string) string {
	parts := strings.Split(filepath.ToSlash(path), "/")
	for i, p := range parts {
		if p == "services" || p == "tools" {
			if i+1 < len(parts) {
				return parts[i+1]
			}
		}
	}
	return path
}

func isLedgerclientSelector(e ast.Expr, names ...string) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "ledgerclient" {
		return false
	}
	for _, n := range names {
		if sel.Sel.Name == n {
			return true
		}
	}
	return false
}

func (s *callerScan) inspectFile(path string) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		s.violations = append(s.violations, "cannot parse "+path+": "+err.Error())
		return
	}
	owner := ownerDir(path)
	hasPath, hasHeader := false, false
	ast.Inspect(file, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.BasicLit:
			if x.Kind == token.STRING {
				if v, err := strconv.Unquote(x.Value); err == nil {
					hasPath = hasPath || strings.Contains(v, journalEntriesPath)
					hasHeader = hasHeader || v == idempotencyHeader
				}
			}
		case *ast.CallExpr:
			if isLedgerclientSelector(x.Fun, "PostingKey", "MustPostingKey") {
				s.builderUsers[owner] = true
			}
		case *ast.CompositeLit:
			if !isLedgerclientSelector(x.Type, "Entry", "Reversal") {
				return true
			}
			for _, el := range x.Elts {
				kv, ok := el.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if k, ok := kv.Key.(*ast.Ident); !ok || k.Name != "IdempotencyKey" {
					continue
				}
				where := fset.Position(kv.Pos()).String()
				switch v := kv.Value.(type) {
				case *ast.CallExpr:
					if !isLedgerclientSelector(v.Fun, "PostingKey", "MustPostingKey") {
						s.violations = append(s.violations, where+": the key is built by a call other than ledgerclient.PostingKey")
					}
				case *ast.Ident, *ast.SelectorExpr:
					s.looseKeys[owner] = append(s.looseKeys[owner], where)
				default:
					s.violations = append(s.violations, where+": the key is a literal or a concatenation, not ledgerclient.PostingKey")
				}
			}
		}
		return true
	})
	if hasPath && hasHeader && owner != "ledger" {
		s.violations = append(s.violations, path+": posts to the ledger with its own client; use ledgerclient")
	}
}

func scanCallers(t *testing.T, roots []string) *callerScan {
	t.Helper()
	s := &callerScan{builderUsers: map[string]bool{}, looseKeys: map[string][]string{}}
	files := 0
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && (d.Name() == "vendor" || d.Name() == "node_modules") {
				return filepath.SkipDir
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			files++
			s.inspectFile(path)
			return nil
		})
		if err != nil {
			t.Fatalf("scan %s: %v", root, err)
		}
	}
	if files == 0 {
		t.Fatal("the scan found no Go files; the roots moved")
	}
	for owner, keys := range s.looseKeys {
		if !s.builderUsers[owner] {
			s.violations = append(s.violations, keys...)
		}
	}
	return s
}

func TestEveryPosterBuildsItsKeyWithPostingKey(t *testing.T) {
	if got := scanCallers(t, scannedRoots); len(got.violations) > 0 {
		t.Fatalf("posting key violations:\n%s", strings.Join(got.violations, "\n"))
	}
}

func TestScanFlagsAHandBuiltKeyAndAnOwnClient(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, src string) {
		path := filepath.Join(dir, "services", rel)
		if err := writeSource(path, src); err != nil {
			t.Fatal(err)
		}
	}
	write("alpha/a.go", `package a
import "github.com/BenjaminSRussell/depot/platform/ledgerclient"
var _ = ledgerclient.Entry{IdempotencyKey: "alpha:" + id}
var _ = ledgerclient.Reversal{IdempotencyKey: fmt.Sprintf("alpha-%s", id)}
`)
	write("beta/b.go", `package b
const path = "/v1/ledger/journal-entries"
func f(r *http.Request) { r.Header.Set("Idempotency-Key", k) }
`)
	write("gamma/g.go", `package g
import "github.com/BenjaminSRussell/depot/platform/ledgerclient"
var _ = ledgerclient.Entry{IdempotencyKey: ledgerclient.MustPostingKey("gamma", "doc", id, "post")}
var _ = ledgerclient.Entry{IdempotencyKey: key}
`)
	write("delta/d.go", `package d
import "github.com/BenjaminSRussell/depot/platform/ledgerclient"
var _ = ledgerclient.Entry{IdempotencyKey: key}
`)
	write("epsilon/e.go", "package e\nfunc broken(")
	got := scanCallers(t, []string{filepath.Join(dir, "services")})
	joined := strings.Join(got.violations, "\n")
	for _, want := range []string{"alpha/a.go:3", "alpha/a.go:4", "beta/b.go", "delta/d.go:3", "cannot parse"} {
		if !strings.Contains(joined, want) {
			t.Errorf("violations do not name %s:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "gamma") {
		t.Errorf("gamma builds its keys with the builder and must pass:\n%s", joined)
	}
}

func writeSource(path, src string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(src), 0o600)
}
