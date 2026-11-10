package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/store"
)

func TestFailure_SortsStoreErrorsByWhatRetryingCanDo(t *testing.T) {
	evt := events.Event{Type: InvoicePostedType}
	cases := []struct {
		name                 string
		err                  error
		permanent, transient bool
	}{
		{"a refusal", httpx.NewProblem(http.StatusConflict, store.CodeBillingNumberTaken, "taken"), true, false},
		{"a data error", &pgconn.PgError{Code: "22003"}, true, false},
		{"a constraint violation", &pgconn.PgError{Code: "23514"}, true, false},
		{"a lost connection", &pgconn.PgError{Code: "08006"}, false, true},
		{"out of resources", &pgconn.PgError{Code: "53300"}, false, true},
		{"an operator shutdown", &pgconn.PgError{Code: "57P01"}, false, true},
		{"a network error", errors.New("dial tcp: connection refused"), false, true},
		{"a serialization failure", &pgconn.PgError{Code: "40001"}, false, false},
		{"an invoice not here yet", fmt.Errorf("wrapped: %w", store.ErrOriginalInvoiceNotFound), false, false},
	}
	for _, c := range cases {
		got := failure(evt, "INV-1", c.err)
		if eventing.IsPermanent(got) != c.permanent || eventing.IsTransient(got) != c.transient {
			t.Errorf("%s: permanent %v transient %v, want %v %v", c.name, eventing.IsPermanent(got), eventing.IsTransient(got), c.permanent, c.transient)
		}
		if !errors.Is(got, c.err) && c.permanent == false {
			t.Errorf("%s: the cause is lost: %v", c.name, got)
		}
	}
}

func TestLogBillToDiffers_WarnsOnlyWhenTheBillToAccountIsNotTheCustomer(t *testing.T) {
	var out bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&out, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	const customer = "00000000-0000-0000-0000-0000000000c1"
	evt := events.Event{Type: InvoicePostedType}
	for _, same := range []string{"", customer, "00000000-0000-0000-0000-0000000000C1"} {
		logBillToDiffers(context.Background(), evt, "INV-1", customer, same)
	}
	if out.Len() != 0 {
		t.Fatalf("warned for the customer's own account: %s", out.String())
	}
	logBillToDiffers(context.Background(), evt, "INV-2", customer, "00000000-0000-0000-0000-0000000000c9")
	if !bytes.Contains(out.Bytes(), []byte("invoiceAccount=00000000-0000-0000-0000-0000000000c9")) {
		t.Fatalf("no warning for another bill-to account: %q", out.String())
	}
}
