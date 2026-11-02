package target_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/target"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/target/targettest"
)

const (
	tid = "00000000-0000-7000-8000-00000000000a"
	le  = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	cu  = "11111111-1111-4111-8111-111111111111"
)

func row(key string, amount string) domain.ReceivablesRow {
	a := decimal.RequireFromString(amount)
	return domain.ReceivablesRow{LegacyKey: key, DocumentNumber: key, CustomerID: cu, Kind: "INVOICE", Side: "DEBIT", DocumentDate: "2026-01-01", DueDate: "2026-01-31",
		Currency: "USD", OriginalAmount: a, OpenAmount: a, CashDiscountAmount: decimal.Zero}
}

type slept struct{ waits []time.Duration }

func (s *slept) sleep(_ context.Context, d time.Duration) error {
	s.waits = append(s.waits, d)
	return nil
}

func client(f *targettest.Fake, s *slept) *target.Receivables {
	return target.NewReceivables(target.Config{BaseURL: f.URL, Signer: targettest.HeaderSigner{Name: "migration-factory"}, Attempts: 4, Backoff: time.Second, Sleep: s.sleep})
}

func TestLoadBatch_CreatesThenReportsUnchanged(t *testing.T) {
	f := targettest.New(t)
	c := client(f, &slept{})
	res, err := c.LoadBatch(context.Background(), tid, le, "ax2012.usmf", "k1", []domain.ReceivablesRow{row("A", "10"), row("B", "20")})
	if err != nil || len(res) != 2 || res[0].Outcome != target.OutcomeCreated || res[0].OpenItemID == "" || res[1].LegacyKey != "B" {
		t.Fatalf("first load %+v %v", res, err)
	}
	again, err := c.LoadBatch(context.Background(), tid, le, "ax2012.usmf", "k1", []domain.ReceivablesRow{row("A", "10"), row("B", "20")})
	if err != nil || again[0].Outcome != target.OutcomeUnchanged || again[0].OpenItemID != res[0].OpenItemID {
		t.Fatalf("rerun %+v %v", again, err)
	}
	calls := f.Batches()
	if len(calls) != 2 || calls[0].Issuer != "migration-factory" || calls[0].Act != "service:migration-loader" || len(calls[0].Duties) != 1 || calls[0].Duties[0] != "receivables.import.write" {
		t.Fatalf("calls %+v", calls)
	}
	changed, _ := c.LoadBatch(context.Background(), tid, le, "ax2012.usmf", "k2", []domain.ReceivablesRow{row("A", "9")})
	if changed[0].Outcome != target.OutcomeUpdated {
		t.Fatalf("a changed open amount updates: %+v", changed)
	}
}

func TestLoadBatch_RetriesServerErrorsWithBackoffAndTheSameKey(t *testing.T) {
	f := targettest.New(t)
	f.FailNext(http.StatusServiceUnavailable, http.StatusTooManyRequests, http.StatusInternalServerError)
	f.RetryAfter = "7"
	s := &slept{}
	res, err := client(f, s).LoadBatch(context.Background(), tid, le, "src", "stable-key", []domain.ReceivablesRow{row("A", "10")})
	if err != nil || res[0].Outcome != target.OutcomeCreated {
		t.Fatalf("load after retries: %+v %v", res, err)
	}
	if len(s.waits) != 3 || s.waits[0] != 7*time.Second || s.waits[1] != 7*time.Second || s.waits[2] != 7*time.Second {
		t.Fatalf("waits %v: Retry-After 7s wins over the 1s, 2s, 4s backoff until the backoff passes it", s.waits)
	}
	keys := map[string]int{}
	for _, c := range f.Calls() {
		keys[c.IdempotencyKey]++
	}
	if len(f.Calls()) != 4 || keys["stable-key"] != 4 {
		t.Fatalf("every attempt carries the same Idempotency-Key: %+v", f.Calls())
	}
}

func TestLoadBatch_ExponentialBackoffWithoutRetryAfter(t *testing.T) {
	f := targettest.New(t)
	f.FailNext(502, 502, 502)
	s := &slept{}
	if _, err := client(f, s).LoadBatch(context.Background(), tid, le, "src", "k", []domain.ReceivablesRow{row("A", "10")}); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(s.waits) != fmt.Sprint([]time.Duration{time.Second, 2 * time.Second, 4 * time.Second}) {
		t.Fatalf("waits %v", s.waits)
	}
}

func TestLoadBatch_GivesUpAsUnavailable(t *testing.T) {
	f := targettest.New(t)
	f.FailNext(503, 503, 503, 503, 503)
	_, err := client(f, &slept{}).LoadBatch(context.Background(), tid, le, "src", "k", []domain.ReceivablesRow{row("A", "10")})
	var te *target.Error
	if !errors.Is(err, target.ErrUnavailable) || !errors.As(err, &te) {
		t.Fatalf("error %v", err)
	}
	if n := len(f.Calls()); n != 4 {
		t.Fatalf("%d attempts, want 4", n)
	}
	if f.Held("src") != 0 {
		t.Fatal("nothing should have been stored")
	}
}

func TestLoadBatch_RefusedRowKeepsTargetCode(t *testing.T) {
	f := targettest.New(t)
	c := client(f, &slept{})
	bad := row("BAD", "10")
	bad.OpenAmount = decimal.RequireFromString("11") // more than the original
	dup1, dup2 := row("D", "5"), row("D", "5")
	res, err := c.LoadBatch(context.Background(), tid, le, "src", "k", []domain.ReceivablesRow{row("OK", "10"), bad, dup1, dup2})
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Outcome != target.OutcomeCreated || res[1].Outcome != target.OutcomeRejected || res[1].Code != "AR-OPENING-INVALID" ||
		res[2].Outcome != target.OutcomeCreated || res[3].Code != "AR-OPENING-DUPLICATE-KEY" {
		t.Fatalf("results %+v", res)
	}
	// A key settled in Passage: the snapshot is refused for it.
	f.Settled["src|OK"] = true
	changed := row("OK", "9")
	res, _ = c.LoadBatch(context.Background(), tid, le, "src", "k2", []domain.ReceivablesRow{changed})
	if res[0].Code != "AR-OPENING-SETTLED" {
		t.Fatalf("settled: %+v", res)
	}
	moved := row("OK", "10")
	moved.Currency = "EUR"
	res, _ = c.LoadBatch(context.Background(), tid, le, "src", "k3", []domain.ReceivablesRow{moved})
	if res[0].Code != "AR-OPENING-IDENTITY" {
		t.Fatalf("identity: %+v", res)
	}
}

func TestLoadBatch_BatchSizeAndRequestShape(t *testing.T) {
	f := targettest.New(t)
	c := client(f, &slept{})
	rows := make([]domain.ReceivablesRow, 1000)
	for i := range rows {
		rows[i] = row(fmt.Sprintf("R%04d", i), "1")
	}
	if res, err := c.LoadBatch(context.Background(), tid, le, "src", "k", rows); err != nil || len(res) != 1000 || res[999].OpenItemID == "" {
		t.Fatalf("1000 rows: %d %v", len(res), err)
	}
	if _, err := c.LoadBatch(context.Background(), tid, le, "src", "k", append(rows, row("X", "1"))); err == nil {
		t.Fatal("1001 rows must not be sent")
	}
	if _, err := c.LoadBatch(context.Background(), tid, le, "src", "k", nil); err == nil {
		t.Fatal("an empty batch must not be sent")
	}
	// The fake decodes strictly like the real handler; a bad importSource is a 400 for the whole batch.
	_, err := c.LoadBatch(context.Background(), tid, le, "bad source!", "k", []domain.ReceivablesRow{row("Z", "1")})
	var te *target.Error
	if !errors.Is(err, target.ErrRefused) || !errors.As(err, &te) || te.Status != 400 || te.Code != "COM-0001" {
		t.Fatalf("bad import source: %v", err)
	}
}

func TestLoadBatch_DeniedAndNotConfigured(t *testing.T) {
	f := targettest.New(t)
	// A token without the import duty is refused 403.
	c := target.NewReceivables(target.Config{BaseURL: f.URL, Signer: noDuty{}, Sleep: (&slept{}).sleep})
	if _, err := c.LoadBatch(context.Background(), tid, le, "src", "k", []domain.ReceivablesRow{row("A", "1")}); !errors.Is(err, target.ErrDenied) {
		t.Fatalf("error %v", err)
	}
	if n := len(f.Calls()); n != 1 {
		t.Fatalf("a 403 is final, not retried: %d calls", n)
	}
	un := target.NewReceivables(target.Config{})
	if un.Configured() {
		t.Fatal("zero config is not configured")
	}
	if _, err := un.LoadBatch(context.Background(), tid, le, "src", "k", []domain.ReceivablesRow{row("A", "1")}); !errors.Is(err, target.ErrNotConfigured) {
		t.Fatalf("error %v", err)
	}
	if _, err := un.ImportTotals(context.Background(), tid, le, "src"); !errors.Is(err, target.ErrNotConfigured) {
		t.Fatalf("error %v", err)
	}
}

func TestImportTotals_PerSourceEntityAndCurrency(t *testing.T) {
	f := targettest.New(t)
	c := client(f, &slept{})
	cn := row("CN", "4")
	cn.Kind, cn.Side = "CREDIT_NOTE", "CREDIT"
	eur := row("E", "7")
	eur.Currency = "EUR"
	for _, src := range []string{"a", "b"} {
		if _, err := c.LoadBatch(context.Background(), tid, le, src, "k"+src, []domain.ReceivablesRow{row("I", "10.5"), cn, eur}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := c.ImportTotals(context.Background(), tid, le, "a")
	if err != nil || len(got) != 2 {
		t.Fatalf("totals %+v %v", got, err)
	}
	var usd domain.ImportTotals
	for _, g := range got {
		if g.Currency == "USD" {
			usd = g
		}
	}
	if usd.ItemCount != 2 || !usd.DebitOpen.Equal(decimal.RequireFromString("10.5")) || !usd.CreditOpen.Equal(decimal.NewFromInt(-4)) || !usd.NetOpen.Equal(decimal.RequireFromString("6.5")) {
		t.Fatalf("usd %+v", usd)
	}
	other, _ := c.ImportTotals(context.Background(), tid, "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", "a")
	if len(other) != 0 {
		t.Fatalf("another legal entity has no totals: %+v", other)
	}
	last := f.Calls()[len(f.Calls())-2]
	if last.Duties[0] != "ARView" {
		t.Fatalf("the read is signed with ARView only: %+v", last)
	}
	f.DenyImports = true
	if _, err := c.ImportTotals(context.Background(), tid, le, "a"); !errors.Is(err, target.ErrDenied) {
		t.Fatalf("error %v", err)
	}
}

// noDuty signs a token that holds a duty other than the one asked for.
type noDuty struct{}

func (noDuty) ApplyDuties(req *http.Request, p tenant.Principal, aud, legalEntity string, _ []string) (bool, error) {
	return targettest.HeaderSigner{Name: "migration-factory"}.ApplyDuties(req, p, aud, legalEntity, []string{"ARView"})
}
