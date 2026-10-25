package domain_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/tax/internal/domain"
)

type fakeGL struct {
	movements map[string]string
	err       error
}

func (f fakeGL) Movements(time.Time, time.Time) (map[string]decimal.Decimal, error) {
	out := map[string]decimal.Decimal{}
	for k, v := range f.movements {
		out[k] = decimal.RequireFromString(v)
	}
	return out, f.err
}

type recordingPoster struct {
	keys []string
}

func (r *recordingPoster) Post(_ context.Context, key string, _ domain.Settlement) error {
	r.keys = append(r.keys, key)
	return nil
}

var january = domain.Period{From: domain.Day("2026-01-01"), To: domain.Day("2026-01-31")}

func txn(id, code, direction, date, amount, run string) domain.Txn {
	return domain.Txn{ID: id, Code: code, Direction: direction, TaxDate: domain.Day(date), Amount: domain.D(amount), SettlementRunID: run}
}

func TestSettle_ReconcilesToGL(t *testing.T) {
	txns := []domain.Txn{
		txn("t1", "VAT19", domain.DirOutput, "2026-01-10", "100.01", ""),
		txn("t2", "VAT19", domain.DirOutput, "2026-01-20", "50.49", ""),
		txn("t3", "VAT19", domain.DirOutput, "2026-01-25", "-10.00", ""),
		txn("t4", "VAT19", domain.DirInput, "2026-01-12", "30.25", ""),
	}
	gl := fakeGL{movements: map[string]string{domain.MovementKey("VAT19", domain.DirOutput): "140.50", domain.MovementKey("VAT19", domain.DirInput): "30.25"}}
	s, err := domain.Settle(txns, january, 0, gl)
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "output", s.OutputTotal, "140.50")
	mustEqual(t, "input", s.InputTotal, "30.25")
	mustEqual(t, "net", s.NetAmount, "110.25")
	if s.Version != 1 || len(s.TransactionIDs) != 4 || len(s.Lines) != 2 {
		t.Errorf("unexpected settlement %+v", s)
	}
}

func TestSettle_MismatchBlocks(t *testing.T) {
	key := domain.MovementKey("VAT19", domain.DirOutput)
	tests := []struct {
		name string
		txns []domain.Txn
		gl   fakeGL
	}{
		{"one cent off", []domain.Txn{txn("t1", "VAT19", domain.DirOutput, "2026-01-10", "100.00", "")}, fakeGL{movements: map[string]string{key: "100.01"}}},
		{"unlinked GL postings without transactions", nil, fakeGL{movements: map[string]string{key: "5.00"}}},
		{"transaction missing from the GL", []domain.Txn{txn("t1", "VAT19", domain.DirOutput, "2026-01-10", "100.00", "")}, fakeGL{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			poster := &recordingPoster{}
			s, err := domain.Settle(tc.txns, january, 0, tc.gl)
			if err == nil {
				_ = domain.PostSettlement(context.Background(), poster, "s1", s)
			}
			var de *domain.Error
			if !errors.As(err, &de) || de.Code != domain.CodeSettlementMismatch {
				t.Fatalf("got %v, want %s", err, domain.CodeSettlementMismatch)
			}
			if len(poster.keys) != 0 {
				t.Fatalf("posted despite mismatch: %v", poster.keys)
			}
		})
	}
}

func TestSettle_ExcludesTransactionsOutsideThePeriod(t *testing.T) {
	txns := []domain.Txn{
		txn("t1", "VAT19", domain.DirOutput, "2026-01-31", "10.00", ""),
		txn("t2", "VAT19", domain.DirOutput, "2026-02-01", "99.00", ""),
		txn("t3", "VAT19", domain.DirOutput, "2025-12-31", "98.00", ""),
	}
	gl := fakeGL{movements: map[string]string{domain.MovementKey("VAT19", domain.DirOutput): "10.00"}}
	s, err := domain.Settle(txns, january, 0, gl)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.TransactionIDs) != 1 || s.TransactionIDs[0] != "t1" {
		t.Errorf("got %v, want only t1", s.TransactionIDs)
	}
}

func TestSettle_DeltaRerun(t *testing.T) {
	txns := []domain.Txn{
		txn("t1", "VAT19", domain.DirOutput, "2026-01-10", "100.00", "run-1"),
		txn("t2", "VAT19", domain.DirOutput, "2026-01-28", "7.00", ""),
	}
	gl := fakeGL{movements: map[string]string{domain.MovementKey("VAT19", domain.DirOutput): "107.00"}}
	s, err := domain.Settle(txns, january, 1, gl)
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "delta", s.NetAmount, "7.00")
	if s.Version != 2 || len(s.TransactionIDs) != 1 || s.TransactionIDs[0] != "t2" {
		t.Errorf("unexpected delta settlement %+v", s)
	}
}

func TestSettle_EmptyIsZeroAndNeverPosts(t *testing.T) {
	poster := &recordingPoster{}
	s, err := domain.Settle(nil, january, 3, fakeGL{})
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "net", s.NetAmount, "0")
	if s.Version != 3 {
		t.Errorf("version %d, want unchanged 3", s.Version)
	}
	if err := domain.PostSettlement(context.Background(), poster, "s1", s); err != nil || len(poster.keys) != 0 {
		t.Fatalf("empty settlement posted: %v %v", err, poster.keys)
	}
}

func TestSettle_GLFailureSurfaces(t *testing.T) {
	boom := errors.New("gl down")
	if _, err := domain.Settle(nil, january, 0, fakeGL{err: boom}); !errors.Is(err, boom) {
		t.Fatalf("got %v, want wrapped %v", err, boom)
	}
}

func TestPostSettlement_UsesIdempotencyKey(t *testing.T) {
	poster := &recordingPoster{}
	s := domain.Settlement{TransactionIDs: []string{"t1"}}
	if err := domain.PostSettlement(context.Background(), poster, "S1", s); err != nil {
		t.Fatal(err)
	}
	if len(poster.keys) != 1 || poster.keys[0] != "post:tax:settlement:S1:settle" {
		t.Fatalf("keys %v", poster.keys)
	}
}
