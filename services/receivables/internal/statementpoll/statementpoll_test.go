package statementpoll

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/store"
)

const camtFile = `<?xml version="1.0" encoding="UTF-8"?>
<Document xmlns="urn:iso:std:iso:20022:tech:xsd:camt.053.001.08"><BkToCstmrStmt><Stmt><Id>STMT-%d</Id>
<Acct><Id><IBAN>DE89370400440532013000</IBAN></Id></Acct>
<Ntry><Amt Ccy="EUR">100.00</Amt><CdtDbtInd>CRDT</CdtDbtInd><ValDt><Dt>2027-03-20</Dt></ValDt><NtryRef>E-%d</NtryRef></Ntry>
</Stmt></BkToCstmrStmt></Document>`

// memServer is an in-memory file server: path to content.
type memServer struct {
	files   map[string]string
	listErr error
	moves   []string
}

func (m *memServer) List(_ context.Context, dir string) ([]string, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	var out []string
	for p := range m.files {
		if strings.HasPrefix(p, dir+"/") && !strings.Contains(strings.TrimPrefix(p, dir+"/"), "/") {
			out = append(out, strings.TrimPrefix(p, dir+"/"))
		}
	}
	sort.Strings(out)
	return out, nil
}

func (m *memServer) Read(_ context.Context, p string, max int) ([]byte, error) {
	c, ok := m.files[p]
	if !ok {
		return nil, errors.New("no such file")
	}
	if len(c) > max {
		c = c[:max]
	}
	return []byte(c), nil
}

func (m *memServer) Move(_ context.Context, from, to string) error {
	if _, ok := m.files[to]; ok {
		return fmt.Errorf("%s exists", to)
	}
	m.files[to] = m.files[from]
	delete(m.files, from)
	m.moves = append(m.moves, from+" -> "+to)
	return nil
}

type fakeImporter struct {
	seen    map[string]bool
	imports []store.BankStatementImport
	err     error
	applied []string
}

func (f *fakeImporter) ImportBankStatement(_ context.Context, in store.BankStatementImport) (store.BankStatementBatch, error) {
	if f.err != nil {
		return store.BankStatementBatch{}, f.err
	}
	if f.seen[in.FileSHA256] {
		return store.BankStatementBatch{}, httpx.NewProblem(409, store.CodeLockboxDuplicate, "File already imported")
	}
	f.seen[in.FileSHA256] = true
	f.imports = append(f.imports, in)
	b := store.BankStatementBatch{PaymentCount: len(in.Credits)}
	for i := range in.Credits {
		b.Payments = append(b.Payments, store.Payment{ID: fmt.Sprintf("pay-%d-%d", len(f.imports), i)})
	}
	return b, nil
}

func (f *fakeImporter) AutoApplyPayments(_ context.Context, ids []string) (store.AutoApplyResult, error) {
	f.applied = append(f.applied, ids...)
	return store.AutoApplyResult{Applied: len(ids)}, nil
}

var src = Source{LegalEntityID: "le-1", Inbox: "/in", Done: "/done", Failed: "/failed"}

func TestPoll_ImportsMovesAndSkipsWhatItAlreadyHas(t *testing.T) {
	srv := &memServer{files: map[string]string{
		"/in/b.xml":        fmt.Sprintf(camtFile, 2, 2),
		"/in/a.xml":        fmt.Sprintf(camtFile, 1, 1),
		"/in/a-resent.xml": fmt.Sprintf(camtFile, 1, 1), // the same bytes as a.xml
		"/in/notes.txt":    "not a statement",
		"/in/.hidden.xml":  "ignored",
		"/in/bad.xml":      "<this is not camt",
		"/in/other.xml":    `<Document><BkToCstmrStmt/></Document>`,
	}}
	imp := &fakeImporter{seen: map[string]bool{}}
	res, err := Poll(context.Background(), srv, imp, src)
	if err != nil {
		t.Fatal(err)
	}
	if res.Imported != 2 || res.Duplicates != 1 || res.Failed != 2 || res.Payments != 2 || res.Applied != 0 || res.More {
		t.Fatalf("result = %+v", res)
	}
	if len(imp.imports) != 2 || imp.imports[0].LegalEntityID != "le-1" || imp.imports[0].Credits[0].BankAccount != "DE89370400440532013000" ||
		!imp.imports[0].Credits[0].Amount.Equal(decimal.RequireFromString("100")) {
		t.Fatalf("imports = %+v", imp.imports)
	}
	for _, want := range []string{"/done/a.xml", "/done/b.xml", "/done/a-resent.xml", "/failed/bad.xml", "/failed/other.xml", "/in/notes.txt", "/in/.hidden.xml"} {
		if _, ok := srv.files[want]; !ok {
			t.Errorf("%s is missing; files = %v", want, srv.files)
		}
	}
	for p := range srv.files {
		if strings.HasPrefix(p, "/in/") && p != "/in/notes.txt" && p != "/in/.hidden.xml" {
			t.Errorf("%s was left in the inbox", p)
		}
	}
	// A second poll finds nothing to do.
	if again, err := Poll(context.Background(), srv, imp, src); err != nil || again != (Result{}) {
		t.Fatalf("second poll = %+v, %v", again, err)
	}
}

func TestPoll_AFailureThatMayPassLeavesTheFileAndStops(t *testing.T) {
	srv := &memServer{files: map[string]string{"/in/a.xml": fmt.Sprintf(camtFile, 1, 1), "/in/b.xml": fmt.Sprintf(camtFile, 2, 2)}}
	imp := &fakeImporter{seen: map[string]bool{}, err: errors.New("connection refused")}
	res, err := Poll(context.Background(), srv, imp, src)
	if err == nil || res.Imported != 0 {
		t.Fatalf("a database outage: %+v, %v", res, err)
	}
	if _, ok := srv.files["/in/a.xml"]; !ok || len(srv.moves) != 0 {
		t.Fatalf("the file moved on an outage: %v", srv.moves)
	}
	srv.listErr = errors.New("server down")
	if _, err := Poll(context.Background(), srv, imp, src); err == nil {
		t.Fatal("a listing failure was swallowed")
	}
	// A refusal that repeating cannot change parks the file.
	srv.listErr = nil
	imp.err = httpx.NewProblem(422, "AR-0004", "not valid")
	if res, err := Poll(context.Background(), srv, imp, src); err != nil || res.Failed != 2 {
		t.Fatalf("a refused file: %+v, %v", res, err)
	}
}

func TestPoll_AutoApplyAndNameClashes(t *testing.T) {
	srv := &memServer{files: map[string]string{"/in/a.xml": fmt.Sprintf(camtFile, 1, 1), "/done/a.xml": "an earlier statement of that name"}}
	imp := &fakeImporter{seen: map[string]bool{}}
	auto := src
	auto.AutoApply = true
	res, err := Poll(context.Background(), srv, imp, auto)
	if err != nil || res.Imported != 1 || res.Applied != 1 || len(imp.applied) != 1 {
		t.Fatalf("poll = %+v, %v (applied %v)", res, err, imp.applied)
	}
	if srv.files["/done/a.xml"] != "an earlier statement of that name" {
		t.Fatal("the archive replaced an earlier file")
	}
	archived := 0
	for p := range srv.files {
		if strings.HasPrefix(p, "/done/a.") && strings.HasSuffix(p, ".xml") && p != "/done/a.xml" {
			archived++
		}
	}
	if archived != 1 {
		t.Fatalf("the clashing file was archived %d times under a new name: %v", archived, srv.files)
	}
}

func TestPoll_AnOversizedFileIsParkedAndTheInboxIsCapped(t *testing.T) {
	big := strings.Repeat("x", MaxFileBytes+10)
	srv := &memServer{files: map[string]string{"/in/big.xml": big}}
	if res, err := Poll(context.Background(), srv, &fakeImporter{seen: map[string]bool{}}, src); err != nil || res.Failed != 1 {
		t.Fatalf("an oversized file: %+v, %v", res, err)
	}
	many := &memServer{files: map[string]string{}}
	for i := 0; i < MaxFilesPerPoll+5; i++ {
		many.files[fmt.Sprintf("/in/f%04d.xml", i)] = fmt.Sprintf(camtFile, i, i)
	}
	res, err := Poll(context.Background(), many, &fakeImporter{seen: map[string]bool{}}, src)
	if err != nil || res.Imported != MaxFilesPerPoll || !res.More {
		t.Fatalf("a full inbox: %+v, %v", res, err)
	}
}
