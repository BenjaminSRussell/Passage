package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/domain"
	tevents "github.com/BenjaminSRussell/passage/services/treasury/internal/events"
	"github.com/BenjaminSRussell/passage/services/treasury/migrations"
)

const testIBAN = "DE89370400440532013000"

// newStore migrates a throwaway schema with the admin pool and returns a store
// on the application role, so row-level security applies (superusers bypass it).
func newStore(t *testing.T) (*Postgres, *pgxpool.Pool) {
	t.Helper()
	if os.Getenv(testkit.EnvURL) == "" || os.Getenv(testkit.EnvAppURL) == "" {
		t.Skipf("%s and %s are needed for the store tests", testkit.EnvURL, testkit.EnvAppURL)
	}
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return New(app), admin
}

// ctxFor is a principal of tenant n; an empty legal entity means every entity.
func ctxFor(n int, legalEntityID string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(n), LegalEntityID: legalEntityID, UserID: "user-test", Duties: map[string]struct{}{}})
}

func problemCode(t *testing.T, err error) string {
	t.Helper()
	var p *httpx.Problem
	if errors.As(err, &p) {
		return p.Code
	}
	var de *domain.Error
	if errors.As(err, &de) {
		return de.Code
	}
	t.Fatalf("want a coded error, got %v", err)
	return ""
}

func newAccount(le string) NewBankAccount {
	return NewBankAccount{LegalEntityID: le, AccountCode: "OPS-01", IBAN: testIBAN, Currency: "EUR", MainAccountID: ids.New()}
}

func activeAccount(t *testing.T, s *Postgres, ctx context.Context, le string) BankAccount {
	t.Helper()
	acct, err := s.CreateBankAccount(ctx, newAccount(le), ids.New())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	acct, err = s.TransitionBankAccount(ctx, acct.ID, acct.Version, domain.AccountActive)
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	return acct
}

// camt builds a camt.053.001.08 file with n entries of 1.25 EUR credit each.
func camt(id string, seq, n int, closingOverride string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, `<?xml version="1.0" encoding="UTF-8"?><Document xmlns="urn:iso:std:iso:20022:tech:xsd:camt.053.001.08"><BkToCstmrStmt><Stmt><Id>%s</Id>`, id)
	if seq > 0 {
		fmt.Fprintf(&b, `<ElctrncSeqNb>%d</ElctrncSeqNb>`, seq)
	}
	closing := decimal.RequireFromString("1000.00").Add(decimal.RequireFromString("1.25").Mul(decimal.NewFromInt(int64(n))))
	closingText := closing.StringFixed(2)
	if closingOverride != "" {
		closingText = closingOverride
	}
	fmt.Fprintf(&b, `<Acct><Id><IBAN>%s</IBAN></Id><Ccy>EUR</Ccy></Acct>`, testIBAN)
	b.WriteString(`<Bal><Tp><CdOrPrtry><Cd>OPBD</Cd></CdOrPrtry></Tp><Amt Ccy="EUR">1000.00</Amt><CdtDbtInd>CRDT</CdtDbtInd><Dt><Dt>2026-10-22</Dt></Dt></Bal>`)
	fmt.Fprintf(&b, `<Bal><Tp><CdOrPrtry><Cd>CLBD</Cd></CdOrPrtry></Tp><Amt Ccy="EUR">%s</Amt><CdtDbtInd>CRDT</CdtDbtInd><Dt><Dt>2026-10-22</Dt></Dt></Bal>`, closingText)
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, `<Ntry><Amt Ccy="EUR">1.25</Amt><CdtDbtInd>CRDT</CdtDbtInd><BookgDt><Dt>2026-10-22</Dt></BookgDt><NtryDtls><TxDtls><Refs><EndToEndId>E2E-%04d</EndToEndId></Refs></TxDtls></NtryDtls></Ntry>`, i)
	}
	b.WriteString(`</Stmt></BkToCstmrStmt></Document>`)
	return []byte(b.String())
}

// importInput parses data the way the handler does. A file whose balances do
// not add up is returned unchecked so the store's own invariant check runs.
func importInput(t *testing.T, accountID string, data []byte) StatementImport {
	t.Helper()
	sum := sha256.Sum256(data)
	parsed, err := domain.ParseCAMT053(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return StatementImport{BankAccountID: accountID, FileHash: hex.EncodeToString(sum[:]), Parsed: parsed[0]}
}

func count(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// AC-36.1: 1,000 entries persist, opening + Σ lines = closing, a re-upload
// returns the same statement, and statement.imported is in the outbox once.
func TestImportStatement_ThousandLinesReplayAndOutbox(t *testing.T) {
	s, admin := newStore(t)
	ctx := ctxFor(1, "")
	acct := activeAccount(t, s, ctx, ids.New())
	data := camt("STMT-1000", 0, 1000, "")

	first, err := s.ImportStatement(ctx, importInput(t, acct.ID, data))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if first.Replayed || first.Statement.LineCount != 1000 {
		t.Fatalf("first import = %+v", first)
	}
	if n := count(t, admin, `select count(*) from bank_statement_line where statement_id = $1`, first.Statement.ID); n != 1000 {
		t.Fatalf("persisted %d lines, want 1000", n)
	}
	var sum string
	if err := admin.QueryRow(context.Background(), `select (s.opening_amount + coalesce(sum(l.amount), 0))::text from bank_statement s
		join bank_statement_line l on l.tenant_id = s.tenant_id and l.statement_id = s.id where s.id = $1 group by s.opening_amount`, first.Statement.ID).Scan(&sum); err != nil {
		t.Fatal(err)
	}
	if !decimal.RequireFromString(sum).Equal(decimal.RequireFromString(first.Statement.ClosingBalance)) {
		t.Fatalf("opening + lines = %s, closing = %s", sum, first.Statement.ClosingBalance)
	}

	again, err := s.ImportStatement(ctx, importInput(t, acct.ID, data))
	if err != nil || !again.Replayed || again.Statement.ID != first.Statement.ID {
		t.Fatalf("re-upload = %+v, %v; want replay of %s", again, err, first.Statement.ID)
	}
	if n := count(t, admin, `select count(*) from bank_statement`); n != 1 {
		t.Fatalf("%d statements after replay, want 1", n)
	}

	var typ string
	var payload []byte
	if err := admin.QueryRow(context.Background(), `select type, payload from outbox where aggregate_id = $1`, first.Statement.ID).Scan(&typ, &payload); err != nil {
		t.Fatalf("outbox row: %v", err)
	}
	if n := count(t, admin, `select count(*) from outbox`); n != 1 || typ != tevents.StatementImported {
		t.Fatalf("outbox has %d rows of type %s; want one %s", n, typ, tevents.StatementImported)
	}
	var evt struct {
		TenantID string                        `json:"tenantid"`
		Data     tevents.StatementImportedData `json:"data"`
	}
	if err := json.Unmarshal(payload, &evt); err != nil {
		t.Fatal(err)
	}
	if evt.TenantID != testkit.TenantID(1) || evt.Data.LineCount != 1000 || evt.Data.BankAccountID != acct.ID || evt.Data.LegalEntityID != acct.LegalEntityID ||
		evt.Data.ClosingBalance != first.Statement.ClosingBalance {
		t.Fatalf("event = %+v", evt)
	}
}

func TestImportStatement_DuplicateIDsConflictsAndAccountRules(t *testing.T) {
	s, _ := newStore(t)
	ctx := ctxFor(1, "")
	acct := activeAccount(t, s, ctx, ids.New())
	if _, err := s.ImportStatement(ctx, importInput(t, acct.ID, camt("STMT-A", 0, 2, ""))); err != nil {
		t.Fatal(err)
	}
	// BR-36.8: same statement id, same closing balance, different bytes (another
	// entry reference) replays; a different closing balance is a conflict.
	same := importInput(t, acct.ID, camt("STMT-A", 0, 2, ""))
	same.FileHash = strings.Repeat("0", 64)
	if res, err := s.ImportStatement(ctx, same); err != nil || !res.Replayed {
		t.Fatalf("same id and closing = %+v, %v; want replay", res, err)
	}
	if _, err := s.ImportStatement(ctx, importInput(t, acct.ID, camt("STMT-A", 0, 3, ""))); problemCode(t, err) != domain.CodeStatementConflict {
		t.Fatalf("same id, other closing: %v, want %s", err, domain.CodeStatementConflict)
	}

	unbalanced := importInput(t, acct.ID, camt("STMT-B", 0, 1, ""))
	unbalanced.Parsed.ClosingAmount = decimal.RequireFromString("999.99")
	if _, err := s.ImportStatement(ctx, unbalanced); problemCode(t, err) != domain.CodeStatementBalance {
		t.Fatalf("unbalanced: %v, want %s", err, domain.CodeStatementBalance)
	}

	usd := importInput(t, acct.ID, camt("STMT-C", 0, 1, ""))
	usd.Parsed.Currency = "USD"
	if _, err := s.ImportStatement(ctx, usd); problemCode(t, err) != domain.CodeStatementCurrency {
		t.Fatalf("currency mismatch: %v, want %s", err, domain.CodeStatementCurrency)
	}

	other := importInput(t, acct.ID, camt("STMT-D", 0, 1, ""))
	other.Parsed.Account = "GB82WEST12345698765432"
	if _, err := s.ImportStatement(ctx, other); problemCode(t, err) != domain.CodeStatementAccount {
		t.Fatalf("wrong IBAN: %v, want %s", err, domain.CodeStatementAccount)
	}

	draft, err := s.CreateBankAccount(ctx, NewBankAccount{LegalEntityID: acct.LegalEntityID, AccountCode: "OPS-02", IBAN: "GB82WEST12345698765432", Currency: "EUR", MainAccountID: ids.New()}, "")
	if err != nil {
		t.Fatal(err)
	}
	draftFile := importInput(t, draft.ID, camt("STMT-E", 0, 1, ""))
	draftFile.Parsed.Account = "GB82WEST12345698765432"
	if _, err := s.ImportStatement(ctx, draftFile); problemCode(t, err) != CodeAccountInactive {
		t.Fatalf("draft account: %v, want %s", err, CodeAccountInactive)
	}
	if _, err := s.ImportStatement(ctx, importInput(t, ids.New(), camt("STMT-F", 0, 1, ""))); problemCode(t, err) != CodeAccountNotFound {
		t.Fatalf("unknown account: %v, want %s", err, CodeAccountNotFound)
	}
}

func TestImportStatement_ReplayAfterDeactivationAndSequenceGap(t *testing.T) {
	s, _ := newStore(t)
	ctx := ctxFor(1, "")
	acct := activeAccount(t, s, ctx, ids.New())
	first, err := s.ImportStatement(ctx, importInput(t, acct.ID, camt("S-412", 412, 1, "")))
	if err != nil || len(first.Warnings) != 0 {
		t.Fatalf("first numbered statement = %+v, %v; want no warning", first, err)
	}
	gap, err := s.ImportStatement(ctx, importInput(t, acct.ID, camt("S-414", 414, 1, "")))
	if err != nil || len(gap.Warnings) != 1 || gap.Warnings[0].Code != domain.CodeStatementGap {
		t.Fatalf("414 after 412 = %+v, %v; want one %s warning", gap, err, domain.CodeStatementGap)
	}
	fill, err := s.ImportStatement(ctx, importInput(t, acct.ID, camt("S-413", 413, 1, "")))
	if err != nil || len(fill.Warnings) != 0 {
		t.Fatalf("413 filling the gap = %+v, %v; want no warning", fill, err)
	}

	acct, err = s.GetBankAccount(ctx, acct.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.TransitionBankAccount(ctx, acct.ID, acct.Version, domain.AccountInactive); err != nil {
		t.Fatal(err)
	}
	replay, err := s.ImportStatement(ctx, importInput(t, acct.ID, camt("S-412", 412, 1, "")))
	if err != nil || !replay.Replayed || replay.Statement.ID != first.Statement.ID {
		t.Fatalf("re-upload to an inactive account = %+v, %v; want replay (BR-36.8)", replay, err)
	}
	if _, err := s.ImportStatement(ctx, importInput(t, acct.ID, camt("S-415", 415, 1, ""))); problemCode(t, err) != CodeAccountInactive {
		t.Fatalf("new statement on an inactive account: %v, want %s", err, CodeAccountInactive)
	}
}

func TestBankAccount_IdempotencyDuplicatesAndVersioning(t *testing.T) {
	s, admin := newStore(t)
	ctx := ctxFor(1, "")
	in := newAccount(ids.New())
	first, err := s.CreateBankAccount(ctx, in, "key-1")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if first.Status != "draft" || first.IBANLast4 != "3000" || strings.Contains(first.IBANMasked, "370400440532") {
		t.Fatalf("created = %+v", first)
	}
	replay, err := s.CreateBankAccount(ctx, in, "key-1")
	if err != nil || replay.ID != first.ID {
		t.Fatalf("replay = %+v, %v; want %s", replay, err, first.ID)
	}
	changed := in
	changed.Currency = "USD"
	if _, err := s.CreateBankAccount(ctx, changed, "key-1"); problemCode(t, err) != CodeIdempotencyReuse {
		t.Fatalf("key reused with another currency: %v, want %s", err, CodeIdempotencyReuse)
	}
	if _, err := s.CreateBankAccount(ctx, in, "key-2"); problemCode(t, err) != CodeAccountDuplicate {
		t.Fatalf("same code and IBAN under a new key: %v, want %s", err, CodeAccountDuplicate)
	}
	if n := count(t, admin, `select count(*) from idempotency_keys`); n != 1 {
		t.Fatalf("%d idempotency rows; a refused create must not keep its key", n)
	}

	if _, err := s.TransitionBankAccount(ctx, first.ID, first.Version+1, domain.AccountActive); problemCode(t, err) != CodeVersionConflict {
		t.Fatalf("stale version: %v, want %s", err, CodeVersionConflict)
	}
	if _, err := s.TransitionBankAccount(ctx, first.ID, first.Version, domain.AccountClosed); problemCode(t, err) != CodeAccountState {
		t.Fatalf("close a draft: %v, want %s", err, CodeAccountState)
	}
	active, err := s.TransitionBankAccount(ctx, first.ID, first.Version, domain.AccountActive)
	if err != nil || active.Status != "active" || active.Version != first.Version+1 {
		t.Fatalf("activate = %+v, %v", active, err)
	}
	if _, err := s.TransitionBankAccount(ctx, ids.New(), 1, domain.AccountActive); problemCode(t, err) != CodeAccountNotFound {
		t.Fatalf("unknown account: %v, want %s", err, CodeAccountNotFound)
	}
}

func TestRowLevelSecurity_TenantAndLegalEntityScope(t *testing.T) {
	s, _ := newStore(t)
	mine, le := ctxFor(1, ""), ids.New()
	acct := activeAccount(t, s, mine, le)
	st, err := s.ImportStatement(mine, importInput(t, acct.ID, camt("STMT-RLS", 0, 1, "")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateForecastEntry(mine, ForecastEntry{LegalEntityID: le, SourceType: "Manual", ExpectedDate: "2026-11-02", Amount: "10", Currency: "EUR", Confidence: "1"}, ""); err != nil {
		t.Fatal(err)
	}
	for name, ctx := range map[string]context.Context{"tenant 2": ctxFor(2, ""), "another legal entity": ctxFor(1, ids.New())} {
		if _, err := s.GetBankAccount(ctx, acct.ID); problemCode(t, err) != CodeAccountNotFound {
			t.Fatalf("%s read the bank account: %v", name, err)
		}
		if _, err := s.GetStatement(ctx, st.Statement.ID); problemCode(t, err) != CodeStatementNotFound {
			t.Fatalf("%s read the statement: %v", name, err)
		}
		if lines, err := s.ListStatementLines(ctx, st.Statement.ID, 0, 10); err != nil || len(lines) != 0 {
			t.Fatalf("%s read %d statement lines, %v", name, len(lines), err)
		}
		if entries, err := s.ListForecastEntries(ctx, le, "", "2026-01-01", "2027-01-01"); err != nil || len(entries) != 0 {
			t.Fatalf("%s read %d forecast entries, %v", name, len(entries), err)
		}
	}
	scoped := ctxFor(1, le)
	if got, err := s.GetBankAccount(scoped, acct.ID); err != nil || got.ID != acct.ID {
		t.Fatalf("a caller scoped to the account's legal entity: %+v, %v", got, err)
	}
	// Writes into another tenant are refused by the policy's WITH CHECK, not only filtered.
	if _, err := s.CreateBankAccount(ctxFor(2, le), NewBankAccount{LegalEntityID: ids.New(), AccountCode: "X", IBAN: testIBAN, Currency: "EUR", MainAccountID: ids.New()}, ""); err == nil {
		t.Fatal("a caller scoped to one legal entity created an account in another")
	}
}

func TestForecastEntries_IdempotentAndFiltered(t *testing.T) {
	s, _ := newStore(t)
	ctx, le := ctxFor(1, ""), ids.New()
	entry := ForecastEntry{LegalEntityID: le, SourceType: "Manual", ExpectedDate: "2026-11-02", Amount: "-250.50", Currency: "EUR", Confidence: "0.8"}
	first, err := s.CreateForecastEntry(ctx, entry, "fc-1")
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.CreateForecastEntry(ctx, entry, "fc-1")
	if err != nil || again.ID != first.ID || again.SourceID != first.SourceID || first.SourceID == "" {
		t.Fatalf("retry = %+v, %v; want %+v with a generated source id", again, err, first)
	}
	if _, err := s.CreateForecastEntry(ctx, ForecastEntry{LegalEntityID: le, SourceType: "AR", ExpectedDate: "2026-12-15", Amount: "99", Currency: "USD", Confidence: "1"}, "fc-2"); err != nil {
		t.Fatal(err)
	}
	all, err := s.ListForecastEntries(ctx, le, "", "2026-11-01", "2026-12-31")
	if err != nil || len(all) != 2 {
		t.Fatalf("all = %d entries, %v; want 2", len(all), err)
	}
	if !decimal.RequireFromString(all[0].Amount).Equal(decimal.RequireFromString("-250.50")) || all[0].Confidence == "" {
		t.Fatalf("first entry = %+v", all[0])
	}
	if eur, err := s.ListForecastEntries(ctx, le, "EUR", "2026-11-01", "2026-12-31"); err != nil || len(eur) != 1 {
		t.Fatalf("EUR = %d entries, %v; want 1", len(eur), err)
	}
	if window, err := s.ListForecastEntries(ctx, le, "", "2026-11-03", "2026-12-14"); err != nil || len(window) != 0 {
		t.Fatalf("window between the entries = %d, %v; want 0", len(window), err)
	}
	if _, err := s.CreateForecastEntry(ctx, entry, "fc-2"); problemCode(t, err) != CodeIdempotencyReuse {
		t.Fatalf("key reuse: %v", err)
	}
}

// parsedInput parses any supported format, as the import handler does.
func parsedInput(t *testing.T, accountID string, data []byte) StatementImport {
	t.Helper()
	sum := sha256.Sum256(data)
	parsed, err := domain.ParseStatements(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return StatementImport{BankAccountID: accountID, FileHash: hex.EncodeToString(sum[:]), Parsed: parsed[0]}
}

// TestImportStatement_BAI2AndMT940ByStatementAccount stores both new formats
// (the widened format check of migration 0003) and matches a file's bare account
// number against the account's statement account, not only its IBAN.
func TestImportStatement_BAI2AndMT940ByStatementAccount(t *testing.T) {
	s, admin := newStore(t)
	ctx := ctxFor(1, "")
	in := newAccount(ids.New())
	in.Currency, in.StatementAccount = "USD", "123456789"
	acct, err := s.CreateBankAccount(ctx, in, ids.New())
	if err != nil {
		t.Fatal(err)
	}
	if acct.StatementAccountLast4 == nil || *acct.StatementAccountLast4 != "6789" {
		t.Fatalf("statementAccountLast4 = %v", acct.StatementAccountLast4)
	}
	if n := count(t, admin, `select count(*) from bank_account where id = $1 and statement_account_hash is not null and statement_account_last4 = '6789'`, acct.ID); n != 1 {
		t.Fatal("only the hash and last four characters of the statement account are stored")
	}
	if acct, err = s.TransitionBankAccount(ctx, acct.ID, acct.Version, domain.AccountActive); err != nil {
		t.Fatal(err)
	}

	bai2 := []byte("01,BANK,CO,241001,1200,1,,,2/\n02,CO,BANK,1,241001,1200,USD,2/\n03,123456789,USD,010,100000,,,015,95000,,/\n" +
		"16,475,5000,Z,BREF,CHK1001,CHECK PAID/\n49,195000,3/\n98,195000,1,5/\n99,195000,1,7/\n")
	res, err := s.ImportStatement(ctx, parsedInput(t, acct.ID, bai2))
	if err != nil || res.Replayed || res.Statement.Format != domain.FormatBAI2 || !decimal.RequireFromString(res.Statement.ClosingBalance).Equal(decimal.RequireFromString("950")) {
		t.Fatalf("BAI2 import = %+v, %v", res, err)
	}
	var e2e, raw string
	if err := admin.QueryRow(context.Background(), `select end_to_end_ref, raw_fragment from bank_statement_line where statement_id = $1 and line_no = 1`, res.Statement.ID).Scan(&e2e, &raw); err != nil {
		t.Fatal(err)
	}
	if e2e != "CHK1001" || !strings.HasPrefix(raw, "16,475,5000") {
		t.Fatalf("line kept e2e %q raw %q", e2e, raw)
	}

	// MT940 naming the IBAN in :25: matches the same account.
	mt940 := []byte(":20:REF1\n:25:DE89370400440532013000\n:28C:00007/001\n:60F:C241001USD950,00\n" +
		":61:2410011001C25,00NTRFINV-1//B1\n:86:INVOICE 1\n:62F:C241001USD975,00\n")
	res, err = s.ImportStatement(ctx, parsedInput(t, acct.ID, mt940))
	if err != nil || res.Statement.Format != domain.FormatMT940 || res.Statement.Sequence == nil || *res.Statement.Sequence != 7 {
		t.Fatalf("MT940 import = %+v, %v", res, err)
	}

	// A bare number that is neither the IBAN nor the statement account is refused.
	other := []byte(strings.Replace(string(bai2), "03,123456789", "03,987654321", 1))
	if _, err := s.ImportStatement(ctx, parsedInput(t, acct.ID, other)); problemCode(t, err) != domain.CodeStatementAccount {
		t.Fatalf("other account number: %v, want %s", err, domain.CodeStatementAccount)
	}

	// An account without a statement account accepts only its IBAN.
	plain := activeAccount(t, s, ctxFor(2, ""), ids.New())
	if _, err := s.ImportStatement(ctxFor(2, ""), parsedInput(t, plain.ID, bai2)); problemCode(t, err) != domain.CodeStatementAccount {
		t.Fatalf("no statement account: %v, want %s", err, domain.CodeStatementAccount)
	}
}
